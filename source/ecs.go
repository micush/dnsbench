package main

import (
	"encoding/binary"
	"net"
	"net/netip"
)

// EDNS Client Subnet (RFC 7871).  Without it every upstream server sees all
// queries coming from the ddgw node.  With it the proxy tells the server which
// network the client is on (a prefix, /24 for IPv4 and /56 for IPv6 by default
// — never the full address), so servers that understand the option (Google
// 8.8.8.8, BIND, Unbound, PowerDNS, ...) can log or answer per client network.
//
// The proxy only touches queries it can rewrite safely:
//   - no EDNS record in the query: one is added (payload 512 over UDP so the
//     client's own limit still holds) and removed again from the answer;
//   - exactly one EDNS record and nothing after it: the option is added to it
//     and removed again from the answer;
//   - a client that already sent an ECS option (a downstream resolver) is
//     passed through untouched — its choice wins;
//   - anything else (TSIG-signed queries, extra records) goes out unchanged.

const (
	typeOPT       = 41
	optionECS     = 8
	ecsFamilyIPv4 = 1
	ecsFamilyIPv6 = 2
)

// ecsState remembers what addECS did so the answer can be put back the way the
// client expects it.
type ecsState struct {
	addedOPT bool // we added the whole OPT record
	addedECS bool // we added the ECS option (possibly inside the client's OPT)
}

// ecsAddr reports whether a client address is worth sending (not loopback,
// link-local, multicast or unspecified).
func ecsUsable(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !a.IsMulticast() && !a.IsUnspecified()
}

// ecsOption encodes the option (code, length and payload) for client.
func ecsOption(client netip.Addr, p4, p6 int) []byte {
	client = client.Unmap()
	var addr []byte
	family, prefix := ecsFamilyIPv4, p4
	if client.Is4() {
		raw := client.As4()
		addr = raw[:]
	} else {
		family, prefix = ecsFamilyIPv6, p6
		r6 := client.As16()
		addr = r6[:]
	}
	if prefix <= 0 {
		return nil
	}
	n := (prefix + 7) / 8
	a := append([]byte(nil), addr[:n]...)
	if rem := prefix % 8; rem != 0 {
		a[n-1] &= 0xFF << (8 - rem)
	}
	opt := make([]byte, 0, 8+n)
	opt = binary.BigEndian.AppendUint16(opt, optionECS)
	opt = binary.BigEndian.AppendUint16(opt, uint16(4+n))
	opt = binary.BigEndian.AppendUint16(opt, uint16(family))
	opt = append(opt, byte(prefix), 0) // source prefix, scope prefix (0 in queries)
	return append(opt, a...)
}

// skipName returns the offset just after the (possibly compressed) name at off.
func skipName(b []byte, off int) (int, bool) {
	for steps := 0; off < len(b) && steps < 130; steps++ {
		l := int(b[off])
		switch {
		case l == 0:
			return off + 1, true
		case l&0xC0 == 0xC0:
			if off+2 > len(b) {
				return 0, false
			}
			return off + 2, true
		case l&0xC0 != 0:
			return 0, false
		}
		off += 1 + l
	}
	return 0, false
}

// dnsRR is the location of one resource record inside a message.
type dnsRR struct {
	start, rdataOff, end int
	typ                  uint16
	class                uint16
	rdlen                int
}

// recordsAfterQuestion lists every record (answer, authority, additional) and
// the index at which the additional section begins.
func recordsAfterQuestion(b []byte) (rrs []dnsRR, addStart int, ok bool) {
	if len(b) < 12 {
		return nil, 0, false
	}
	qd := int(binary.BigEndian.Uint16(b[4:]))
	an := int(binary.BigEndian.Uint16(b[6:]))
	ns := int(binary.BigEndian.Uint16(b[8:]))
	ar := int(binary.BigEndian.Uint16(b[10:]))
	off := 12
	for i := 0; i < qd; i++ {
		o, k := skipName(b, off)
		if !k || o+4 > len(b) {
			return nil, 0, false
		}
		off = o + 4
	}
	for i := 0; i < an+ns+ar; i++ {
		start := off
		o, k := skipName(b, off)
		if !k || o+10 > len(b) {
			return nil, 0, false
		}
		rd := int(binary.BigEndian.Uint16(b[o+8:]))
		if o+10+rd > len(b) {
			return nil, 0, false
		}
		rrs = append(rrs, dnsRR{start: start, rdataOff: o + 10, end: o + 10 + rd,
			typ: binary.BigEndian.Uint16(b[o:]), class: binary.BigEndian.Uint16(b[o+2:]), rdlen: rd})
		off = o + 10 + rd
	}
	return rrs, an + ns, off == len(b)
}

// hasECS reports whether the OPT rdata already carries an ECS option.
func hasECS(rdata []byte) bool {
	for i := 0; i+4 <= len(rdata); {
		code := binary.BigEndian.Uint16(rdata[i:])
		l := int(binary.BigEndian.Uint16(rdata[i+2:]))
		if code == optionECS {
			return true
		}
		i += 4 + l
	}
	return false
}

// addECS returns the query to send upstream.  ok is false when nothing was
// added (the returned slice is then the original query).
func addECS(q []byte, client netip.Addr, p4, p6 int, tcp bool) (out []byte, st ecsState, ok bool) {
	if !ecsUsable(client) {
		return q, st, false
	}
	opt := ecsOption(client, p4, p6)
	if opt == nil {
		return q, st, false
	}
	rrs, addStart, parsed := recordsAfterQuestion(q)
	if !parsed || binary.BigEndian.Uint16(q[2:])&0x8000 != 0 { // unparsable, or not a query
		return q, st, false
	}
	ar := int(binary.BigEndian.Uint16(q[10:]))
	switch {
	case ar == 0 && addStart == len(rrs):
		size := uint16(512)
		if tcp {
			size = 1232
		}
		out = append(append([]byte(nil), q...), 0, 0, typeOPT, byte(size>>8), byte(size), 0, 0, 0, 0)
		out = binary.BigEndian.AppendUint16(out, uint16(len(opt)))
		out = append(out, opt...)
		binary.BigEndian.PutUint16(out[10:], 1)
		return out, ecsState{addedOPT: true, addedECS: true}, true
	case ar == 1 && len(rrs) == addStart+1 && rrs[addStart].typ == typeOPT:
		r := rrs[addStart]
		if hasECS(q[r.rdataOff:r.end]) {
			return q, st, false
		}
		out = append([]byte(nil), q...)
		out = append(out, opt...)
		binary.BigEndian.PutUint16(out[r.rdataOff-2:], uint16(r.rdlen+len(opt)))
		return out, ecsState{addedECS: true}, true
	}
	return q, st, false
}

// stripECS puts a reply back the way the client expects after addECS: the OPT
// record we added is removed, or the ECS option we added is cut out of the
// client's own OPT.  Anything unusual is returned unchanged.
func stripECS(resp []byte, st ecsState) []byte {
	if !st.addedECS {
		return resp
	}
	rrs, addStart, parsed := recordsAfterQuestion(resp)
	if !parsed || len(rrs) == 0 {
		return resp
	}
	last := rrs[len(rrs)-1]
	if len(rrs)-1 < addStart || last.typ != typeOPT { // only touch an OPT that closes the message
		return resp
	}
	if st.addedOPT {
		out := append([]byte(nil), resp[:last.start]...)
		binary.BigEndian.PutUint16(out[10:], binary.BigEndian.Uint16(out[10:])-1)
		return out
	}
	// cut just the ECS option out of the OPT rdata
	rd := resp[last.rdataOff:last.end]
	var kept []byte
	for i := 0; i+4 <= len(rd); {
		l := int(binary.BigEndian.Uint16(rd[i+2:]))
		if i+4+l > len(rd) {
			return resp
		}
		if binary.BigEndian.Uint16(rd[i:]) != optionECS {
			kept = append(kept, rd[i:i+4+l]...)
		}
		i += 4 + l
	}
	out := append([]byte(nil), resp[:last.rdataOff-2]...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(kept)))
	return append(out, kept...)
}

// addrOf extracts the IP address from a net.Addr (UDP or TCP).
func addrOf(a net.Addr) netip.Addr {
	switch v := a.(type) {
	case *net.UDPAddr:
		return v.AddrPort().Addr()
	case *net.TCPAddr:
		return v.AddrPort().Addr()
	}
	if a == nil {
		return netip.Addr{}
	}
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr()
}
