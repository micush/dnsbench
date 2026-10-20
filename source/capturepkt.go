package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// pktInfo is what the filter and the one-line summary need to know about a captured frame.
type pktInfo struct {
	hasEth       bool
	ethSrc       [6]byte
	ethDst       [6]byte
	etype        uint16 // 0x0800 IPv4, 0x86dd IPv6, 0x0806 ARP, else what the frame says
	isIP4, isIP6 bool
	isARP        bool
	src, dst     netip.Addr // the IP addresses (for ARP, the sender and target protocol addresses)
	proto        uint8      // the IP protocol (the next header, past any extension headers)
	l4           int        // offset of the transport header in the frame, 0 when there is none to read
	hasPorts     bool
	sport, dport uint16
	arpOp        uint16
	arpSHA       [6]byte
}

// parsePkt reads the headers of a frame (Ethernet, with VLAN tags, or bare IP for an interface without a hardware address).
func parsePkt(linktype int, data []byte) (pi pktInfo) {
	off := 0
	if linktype == linktypeEthernet {
		if len(data) < 14 {
			return
		}
		pi.hasEth = true
		copy(pi.ethDst[:], data[0:6])
		copy(pi.ethSrc[:], data[6:12])
		pi.etype = binary.BigEndian.Uint16(data[12:14])
		off = 14
		for n := 0; n < 2 && (pi.etype == 0x8100 || pi.etype == 0x88a8) && len(data) >= off+4; n++ {
			pi.etype = binary.BigEndian.Uint16(data[off+2 : off+4])
			off += 4
		}
	} else {
		if len(data) < 1 {
			return
		}
		switch data[0] >> 4 {
		case 4:
			pi.etype = 0x0800
		case 6:
			pi.etype = 0x86dd
		default:
			return
		}
	}
	p := data[off:]
	switch pi.etype {
	case 0x0800:
		if len(p) < 20 {
			return
		}
		pi.isIP4 = true
		ihl := int(p[0]&0x0f) * 4
		if ihl < 20 {
			ihl = 20
		}
		pi.proto = p[9]
		pi.src, _ = netip.AddrFromSlice(p[12:16])
		pi.dst, _ = netip.AddrFromSlice(p[16:20])
		if binary.BigEndian.Uint16(p[6:8])&0x1fff == 0 { // the first fragment, or not fragmented
			pi.l4 = off + ihl
		}
	case 0x86dd:
		if len(p) < 40 {
			return
		}
		pi.isIP6 = true
		pi.src, _ = netip.AddrFromSlice(p[8:24])
		pi.dst, _ = netip.AddrFromSlice(p[24:40])
		next, o := p[6], 40
	walk:
		for len(p) >= o+8 {
			switch next {
			case 0, 43, 60: // hop-by-hop, routing, destination options
				next, o = p[o], o+(int(p[o+1])+1)*8
			case 51: // authentication header
				next, o = p[o], o+(int(p[o+1])+2)*4
			case 44: // fragment
				if binary.BigEndian.Uint16(p[o+2:o+4])&0xfff8 != 0 {
					pi.proto = p[o]
					return // not the first fragment: no transport header
				}
				next, o = p[o], o+8
			default:
				break walk
			}
		}
		pi.proto = next
		if len(p) >= o {
			pi.l4 = off + o
		}
	case 0x0806:
		if len(p) < 28 || binary.BigEndian.Uint16(p[0:2]) != 1 || binary.BigEndian.Uint16(p[2:4]) != 0x0800 || p[4] != 6 || p[5] != 4 {
			return
		}
		pi.isARP = true
		pi.arpOp = binary.BigEndian.Uint16(p[6:8])
		copy(pi.arpSHA[:], p[8:14])
		pi.src, _ = netip.AddrFromSlice(p[14:18])
		pi.dst, _ = netip.AddrFromSlice(p[24:28])
	}
	if (pi.isIP4 || pi.isIP6) && pi.l4 > 0 && (pi.proto == 6 || pi.proto == 17 || pi.proto == 132) && len(data) >= pi.l4+4 {
		pi.hasPorts = true
		pi.sport = binary.BigEndian.Uint16(data[pi.l4:])
		pi.dport = binary.BigEndian.Uint16(data[pi.l4+2:])
	}
	return
}

// ── one-line summaries (tcpdump-like) ────────────────────────────────────────

func summarizePacket(linktype int, data []byte) string {
	pi := parsePkt(linktype, data)
	switch {
	case pi.isARP:
		return summarizeARP(pi, len(data))
	case pi.isIP4 || pi.isIP6:
		return summarizeIP(pi, data)
	case linktype == linktypeEthernet && len(data) < 14:
		return fmt.Sprintf("short frame (%d bytes)", len(data))
	case linktype != linktypeEthernet && len(data) == 0:
		return "empty"
	case linktype == linktypeEthernet && pi.hasEth:
		return fmt.Sprintf("ethertype 0x%04x, length %d", pi.etype, len(data))
	}
	return fmt.Sprintf("non-IP (%d bytes)", len(data))
}

func summarizeARP(pi pktInfo, total int) string {
	eth := ""
	if pi.hasEth {
		eth = fmt.Sprintf(" (eth %s > %s)", net.HardwareAddr(pi.ethSrc[:]), net.HardwareAddr(pi.ethDst[:]))
	}
	switch pi.arpOp {
	case 1:
		return fmt.Sprintf("ARP, who-has %s tell %s%s, length %d", pi.dst, pi.src, eth, total)
	case 2:
		return fmt.Sprintf("ARP, %s is-at %s%s, length %d", pi.src, net.HardwareAddr(pi.arpSHA[:]), eth, total)
	}
	return fmt.Sprintf("ARP, op %d, length %d%s", pi.arpOp, total, eth)
}

func summarizeIP(pi pktInfo, data []byte) string {
	total := len(data)
	switch pi.proto {
	case 6, 17:
		name := "UDP"
		if pi.proto == 6 {
			name = "TCP"
		}
		if !pi.hasPorts {
			return fmt.Sprintf("%s %s > %s, length %d", name, pi.src, pi.dst, total)
		}
		flags := ""
		hdr := 8
		if pi.proto == 6 {
			hdr = 20
			if len(data) >= pi.l4+14 {
				flags = " [" + tcpFlags(data[pi.l4+13]) + "]"
				hdr = int(data[pi.l4+12]>>4) * 4
			}
		}
		line := fmt.Sprintf("%s %s.%d > %s.%d%s, length %d", name, pi.src, pi.sport, pi.dst, pi.dport, flags, total)
		if pi.sport == 53 || pi.dport == 53 {
			if pl := data[min(len(data), pi.l4+hdr):]; len(pl) > 0 {
				if pi.proto == 6 {
					if len(pl) < 2 {
						return line
					}
					pl = pl[2:] // the DNS message follows a two-byte length
				}
				if d := dnsSummary(pl); d != "" {
					line += " " + d
				}
			}
		}
		return line
	case 1:
		return fmt.Sprintf("ICMP %s > %s%s, length %d", pi.src, pi.dst, icmpText(pi, data, false), total)
	case 58:
		return fmt.Sprintf("ICMPv6 %s > %s%s, length %d", pi.src, pi.dst, icmpText(pi, data, true), total)
	}
	return fmt.Sprintf("IP proto %d %s > %s, length %d", pi.proto, pi.src, pi.dst, total)
}

func icmpText(pi pktInfo, data []byte, v6 bool) string {
	if pi.l4 == 0 || len(data) < pi.l4+2 {
		return ""
	}
	typ := data[pi.l4]
	if !v6 {
		switch typ {
		case 0:
			return ": echo reply"
		case 3:
			return ": destination unreachable"
		case 8:
			return ": echo request"
		case 11:
			return ": time exceeded"
		}
		return fmt.Sprintf(": type %d", typ)
	}
	switch typ {
	case 128:
		return ": echo request"
	case 129:
		return ": echo reply"
	case 1:
		return ": destination unreachable"
	case 135, 136:
		if len(data) < pi.l4+24 {
			return ": neighbor discovery"
		}
		tgt := netip.AddrFrom16([16]byte(data[pi.l4+8 : pi.l4+24]))
		eth := ""
		if pi.hasEth {
			eth = fmt.Sprintf(" (eth %s > %s)", net.HardwareAddr(pi.ethSrc[:]), net.HardwareAddr(pi.ethDst[:]))
		}
		if typ == 135 {
			return ": neighbor solicitation, who has " + tgt.String() + eth
		}
		mac := ""
		for o := pi.l4 + 24; o+8 <= len(data) && data[o+1] > 0; o += int(data[o+1]) * 8 { // options: type 2 is the target's MAC
			if data[o] == 2 {
				mac = " is at " + net.HardwareAddr(data[o+2:o+8]).String()
			}
		}
		return ": neighbor advertisement, " + tgt.String() + mac + eth
	}
	return fmt.Sprintf(": type %d", typ)
}

// dnsSummary says what a DNS message is, in a few words: "DNS A? example.com" or "DNS NOERROR 2 ans".
func dnsSummary(b []byte) string {
	h, ok := parseHeader(b)
	if !ok {
		return ""
	}
	if !h.qr {
		if name, qt, ok := questionOf(b); ok {
			return "DNS " + qtypeName(qt) + "? " + name
		}
		return "DNS query"
	}
	rc := []string{"NOERROR", "FORMERR", "SERVFAIL", "NXDOMAIN", "NOTIMP", "REFUSED"}
	r := "rcode " + strconv.Itoa(h.rcode)
	if h.rcode < len(rc) {
		r = rc[h.rcode]
	}
	s := "DNS " + r + " " + strconv.Itoa(h.ancount) + " ans"
	if h.tc {
		s += " [TC]"
	}
	if name, qt, ok := questionOf(b); ok {
		s += " " + qtypeName(qt) + " " + name
	}
	return s
}

func tcpFlags(b byte) string {
	out := ""
	for _, f := range []struct {
		bit  byte
		char string
	}{{0x02, "S"}, {0x10, "."}, {0x01, "F"}, {0x04, "R"}, {0x08, "P"}, {0x20, "U"}} {
		if b&f.bit != 0 {
			out += f.char
		}
	}
	if out == "" {
		out = "-"
	}
	return out
}

// ── filter: a small tcpdump-like expression, evaluated on each packet ────────
//
// host A.B.C.D | src host … | dst host …      net CIDR | src net … | dst net …
// port N | src port N | dst port N | portrange A-B      ether host|src|dst MAC
// ip | ip6 | arp | icmp | icmp6 | tcp | udp | dns (port 53)
// a protocol before host/net/port narrows it ("tcp port 53", "ip6 host …"); and / or / not / ( ) combine.

type capFilter struct{ root capExpr }

type capExpr interface{ eval(pi *pktInfo) bool }

type (
	capAnd struct{ a, b capExpr }
	capOr  struct{ a, b capExpr }
	capNot struct{ a capExpr }
	capFn  func(pi *pktInfo) bool
)

func (e capAnd) eval(pi *pktInfo) bool { return e.a.eval(pi) && e.b.eval(pi) }
func (e capOr) eval(pi *pktInfo) bool  { return e.a.eval(pi) || e.b.eval(pi) }
func (e capNot) eval(pi *pktInfo) bool { return !e.a.eval(pi) }
func (e capFn) eval(pi *pktInfo) bool  { return e(pi) }

// match reports whether the frame passes (a nil filter passes everything).
func (f *capFilter) match(linktype int, data []byte) bool {
	if f == nil || f.root == nil {
		return true
	}
	pi := parsePkt(linktype, data)
	return f.root.eval(&pi)
}

type capParser struct {
	toks []string
	i    int
}

func (p *capParser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return ""
}

func (p *capParser) next() string {
	t := p.peek()
	if p.i < len(p.toks) {
		p.i++
	}
	return t
}

func capTokens(s string) []string {
	var toks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		case r == '(' || r == ')':
			flush()
			toks = append(toks, string(r))
		case (r == '&' || r == '|') && i+1 < len(rs) && rs[i+1] == r:
			flush()
			toks = append(toks, string(r)+string(r))
			i++
		case r == '!':
			flush()
			toks = append(toks, "!")
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return toks
}

// compileCapFilter turns the text into a filter ("" is no filter).
func compileCapFilter(text string) (*capFilter, error) {
	toks := capTokens(text)
	if len(toks) == 0 {
		return nil, nil
	}
	p := &capParser{toks: toks}
	e, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.i < len(p.toks) {
		return nil, fmt.Errorf("filter: unexpected %q", p.toks[p.i])
	}
	return &capFilter{root: e}, nil
}

func (p *capParser) parseOr() (capExpr, error) {
	a, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek() == "or" || p.peek() == "||" {
		p.next()
		b, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		a = capOr{a, b}
	}
	return a, nil
}

func (p *capParser) parseAnd() (capExpr, error) {
	a, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek() == "and" || p.peek() == "&&" {
		p.next()
		b, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		a = capAnd{a, b}
	}
	return a, nil
}

func (p *capParser) parseNot() (capExpr, error) {
	if t := p.peek(); t == "not" || t == "!" {
		p.next()
		a, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return capNot{a}, nil
	}
	return p.parsePrimary()
}

var capPorts = map[string]uint16{"dns": 53, "domain": 53, "http": 80, "https": 443, "ssh": 22, "ntp": 123, "smtp": 25, "ldap": 389,
	"ldaps": 636, "bgp": 179, "dhcp": 67, "bootps": 67, "bootpc": 68, "snmp": 161, "syslog": 514}

func isProtoWord(t string) bool {
	switch t {
	case "ip", "ip6", "arp", "icmp", "icmp6", "tcp", "udp":
		return true
	}
	return false
}

func protoFn(w string) capFn {
	switch w {
	case "ip":
		return func(pi *pktInfo) bool { return pi.isIP4 }
	case "ip6":
		return func(pi *pktInfo) bool { return pi.isIP6 }
	case "arp":
		return func(pi *pktInfo) bool { return pi.isARP }
	case "icmp":
		return func(pi *pktInfo) bool { return pi.isIP4 && pi.proto == 1 }
	case "icmp6":
		return func(pi *pktInfo) bool { return pi.isIP6 && pi.proto == 58 }
	case "tcp":
		return func(pi *pktInfo) bool { return (pi.isIP4 || pi.isIP6) && pi.proto == 6 }
	}
	return func(pi *pktInfo) bool { return (pi.isIP4 || pi.isIP6) && pi.proto == 17 } // udp
}

func (p *capParser) parsePrimary() (capExpr, error) {
	t := p.next()
	switch {
	case t == "":
		return nil, fmt.Errorf("filter: it ends where more is expected")
	case t == "(":
		e, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.next() != ")" {
			return nil, fmt.Errorf("filter: a ( has no )")
		}
		return e, nil
	case t == "dns":
		return capFn(func(pi *pktInfo) bool { return pi.hasPorts && (pi.sport == 53 || pi.dport == 53) }), nil
	case t == "ether":
		dir := "host"
		if n := p.peek(); n == "src" || n == "dst" || n == "host" {
			dir = n
			p.next()
		}
		mac, err := net.ParseMAC(p.next())
		if err != nil || len(mac) != 6 {
			return nil, fmt.Errorf("filter: ether needs a MAC address such as 00:1a:7c:01:02:00")
		}
		var m [6]byte
		copy(m[:], mac)
		return capFn(func(pi *pktInfo) bool {
			if !pi.hasEth {
				return false
			}
			s, d := pi.ethSrc == m, pi.ethDst == m
			return (dir == "host" && (s || d)) || (dir == "src" && s) || (dir == "dst" && d)
		}), nil
	case isProtoWord(t):
		qual := protoFn(t)
		switch n := p.peek(); n {
		case "src", "dst", "host", "net", "port", "portrange":
			inner, err := p.parseAddrPrim()
			if err != nil {
				return nil, err
			}
			return capFn(func(pi *pktInfo) bool { return qual(pi) && inner.eval(pi) }), nil
		}
		return qual, nil
	case t == "src" || t == "dst" || t == "host" || t == "net" || t == "port" || t == "portrange":
		p.i--
		return p.parseAddrPrim()
	}
	return nil, fmt.Errorf("filter: don't know %q (try host, net, port, tcp, udp, icmp, arp, ip, ip6, dns, ether, and, or, not)", t)
}

// parseAddrPrim reads [src|dst] host|net|port|portrange VALUE.
func (p *capParser) parseAddrPrim() (capExpr, error) {
	dir := ""
	if n := p.peek(); n == "src" || n == "dst" {
		dir = n
		p.next()
	}
	kind := p.next()
	val := p.next()
	if val == "" {
		return nil, fmt.Errorf("filter: %s needs a value", kind)
	}
	pick := func(pi *pktInfo) (a, b netip.Addr) {
		switch dir {
		case "src":
			return pi.src, netip.Addr{}
		case "dst":
			return pi.dst, netip.Addr{}
		}
		return pi.src, pi.dst
	}
	switch kind {
	case "host":
		a, err := netip.ParseAddr(val)
		if err != nil {
			return nil, fmt.Errorf("filter: host needs an IP address, not %q", val)
		}
		return capFn(func(pi *pktInfo) bool {
			x, y := pick(pi)
			return (x.IsValid() && x == a) || (y.IsValid() && y == a)
		}), nil
	case "net":
		pf, err := netip.ParsePrefix(val)
		if err != nil {
			return nil, fmt.Errorf("filter: net needs a prefix such as 10.0.0.0/24, not %q", val)
		}
		return capFn(func(pi *pktInfo) bool {
			x, y := pick(pi)
			return (x.IsValid() && pf.Contains(x)) || (y.IsValid() && pf.Contains(y))
		}), nil
	case "port", "portrange":
		lo, hi, err := capPortRange(kind, val)
		if err != nil {
			return nil, err
		}
		in := func(v uint16) bool { return v >= lo && v <= hi }
		return capFn(func(pi *pktInfo) bool {
			if !pi.hasPorts {
				return false
			}
			switch dir {
			case "src":
				return in(pi.sport)
			case "dst":
				return in(pi.dport)
			}
			return in(pi.sport) || in(pi.dport)
		}), nil
	}
	return nil, fmt.Errorf("filter: after %s expected host, net, port or portrange, not %q", dir, kind)
}

func capPortRange(kind, val string) (lo, hi uint16, err error) {
	one := func(s string) (uint16, error) {
		if v, ok := capPorts[s]; ok {
			return v, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 65535 {
			return 0, fmt.Errorf("filter: %q is not a port", s)
		}
		return uint16(n), nil
	}
	if kind == "port" {
		v, err := one(val)
		return v, v, err
	}
	a, b, ok := strings.Cut(val, "-")
	if !ok {
		return 0, 0, fmt.Errorf("filter: portrange needs A-B, not %q", val)
	}
	if lo, err = one(a); err != nil {
		return
	}
	if hi, err = one(b); err != nil {
		return
	}
	if lo > hi {
		lo, hi = hi, lo
	}
	return
}
