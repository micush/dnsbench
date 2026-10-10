package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"strconv"
	"strings"
)

// Local records for a policy row: instead of servers, the Destination servers cell holds the answer itself.
//
//	A 10.5.5.5, 10.5.5.6              the name is these addresses
//	AAAA 2001:db8::5
//	A 10.5.5.5; AAAA 2001:db8::5      several kinds at once, separated by ;
//	CNAME host.example.com            the name is another name: the gateway looks that up for the client
//	TXT "v=spf1 -all"                 text (quoted strings, or the rest of the entry)
//	TTL 300                           how long clients may keep it (default 60 seconds), with any of the above
//
// A query for a type the row has no records of (AAAA for a row with only A) gets "no data"; ANY gets everything.
// A CNAME row answers CNAME queries with the CNAME, and any other type with the CNAME followed by what the pool
// answers for the target (so a CNAME cannot be mixed with other records).

const polRecord = "record"

type localData struct {
	a, aaaa []netip.Addr
	cname   string
	txt     [][]string // one entry per TXT record: its character-strings
	ttl     uint32
}

// isRecordSyntax reports whether s is local record syntax (a record type and a value) and not a server.
func isRecordSyntax(s string) bool {
	f := strings.Fields(s)
	if len(f) < 2 {
		return false
	}
	switch strings.ToUpper(f[0]) {
	case "A", "AAAA", "CNAME", "TXT", "TTL":
		return true
	}
	return false
}

// localDest reads a destination name that is really a local answer: record syntax ("A 10.5.5.5; TTL 300",
// "CNAME other.example", "TXT ...") or just addresses ("10.5.5.5", "10.5.5.5, 2001:db8::5").  It returns the record
// text that parseLocal reads.
func localDest(d string) (string, bool) {
	d = strings.TrimSpace(d)
	if d == "" {
		return "", false
	}
	if isRecordSyntax(d) {
		return d, true
	}
	var a, aaaa []string
	for _, f := range strings.FieldsFunc(d, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' }) {
		ip, err := netip.ParseAddr(f)
		if err != nil {
			return "", false
		}
		if ip.Is4() || ip.Is4In6() {
			a = append(a, ip.Unmap().String())
		} else {
			aaaa = append(aaaa, ip.String())
		}
	}
	var parts []string
	if len(a) > 0 {
		parts = append(parts, "A "+strings.Join(a, ", "))
	}
	if len(aaaa) > 0 {
		parts = append(parts, "AAAA "+strings.Join(aaaa, ", "))
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "; "), true
}

// splitOutsideQuotes splits s at sep, leaving separators inside double quotes alone.
func splitOutsideQuotes(s string, sep byte) []string {
	var out []string
	in, start := false, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			in = !in
		case s[i] == sep && !in:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func txtStrings(rest string) ([]string, error) {
	rest = strings.TrimSpace(rest)
	var strs []string
	if strings.Contains(rest, `"`) {
		parts := strings.Split(rest, `"`)
		if len(parts)%2 == 0 {
			return nil, errors.New("TXT: a quote is not closed")
		}
		for i := 1; i < len(parts); i += 2 {
			strs = append(strs, parts[i])
		}
	} else if rest != "" {
		strs = []string{rest}
	}
	var out []string
	for _, s := range strs { // a character-string holds at most 255 bytes
		for len(s) > 255 {
			out = append(out, s[:255])
			s = s[255:]
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, errors.New("TXT: no text")
	}
	return out, nil
}

// parseLocal reads a Destination servers cell written as local records.
func parseLocal(s string) (*localData, error) {
	d := &localData{ttl: 60}
	for _, seg := range splitOutsideQuotes(s, ';') {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		kind, rest, _ := strings.Cut(seg, " ")
		rest = strings.TrimSpace(rest)
		switch strings.ToUpper(kind) {
		case "A", "AAAA":
			want6 := strings.EqualFold(kind, "AAAA")
			n := 0
			for _, f := range strings.FieldsFunc(rest, func(c rune) bool { return c == ',' || c == ' ' || c == '\t' }) {
				a, err := netip.ParseAddr(f)
				if err != nil || a.Is4In6() || a.Is6() != want6 {
					return nil, fmt.Errorf("%s: %q is not an IPv%d address", strings.ToUpper(kind), f, map[bool]int{false: 4, true: 6}[want6])
				}
				if want6 {
					d.aaaa = append(d.aaaa, a)
				} else {
					d.a = append(d.a, a)
				}
				n++
			}
			if n == 0 {
				return nil, fmt.Errorf("%s: no address", strings.ToUpper(kind))
			}
		case "CNAME":
			t := strings.ToLower(strings.TrimSuffix(rest, "."))
			if d.cname != "" || !validHostname(t) {
				return nil, fmt.Errorf("CNAME: one name like host.example.com, not %q", rest)
			}
			d.cname = t
		case "TXT":
			strs, err := txtStrings(rest)
			if err != nil {
				return nil, err
			}
			d.txt = append(d.txt, strs)
		case "TTL":
			n, err := strconv.Atoi(rest)
			if err != nil || n < 0 || n > 86400 {
				return nil, fmt.Errorf("TTL: 0-86400 seconds, not %q", rest)
			}
			d.ttl = uint32(n)
		default:
			return nil, fmt.Errorf("%q is not A, AAAA, CNAME, TXT or TTL", kind)
		}
	}
	if len(d.a)+len(d.aaaa)+len(d.txt) == 0 && d.cname == "" {
		return nil, errors.New("no records")
	}
	if d.cname != "" && len(d.a)+len(d.aaaa)+len(d.txt) > 0 {
		return nil, errors.New("a CNAME cannot be combined with other records")
	}
	return d, nil
}

func appendRR(b []byte, typ uint16, ttl uint32, rdata []byte) []byte {
	b = append(b, 0xC0, 0x0C, byte(typ>>8), byte(typ), 0, 1, byte(ttl>>24), byte(ttl>>16), byte(ttl>>8), byte(ttl), byte(len(rdata)>>8), byte(len(rdata)))
	return append(b, rdata...)
}

func packPlainName(name string) []byte {
	var b []byte
	for _, l := range splitLabels(name) {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	return append(b, 0)
}

// recordAnswer is the row's answer to query: its records, or for a CNAME the CNAME and the pool's answer for the target.
func (r *policyRule) recordAnswer(ctx context.Context, p *Pool, query []byte, qi *qinfo) []byte {
	d := r.rec
	resp := errorResponse(query, rcodeNoError)
	if resp == nil || len(resp) <= 12 {
		return resp
	}
	resp[2] |= 0x04 // AA: the gateway is the authority for this name
	t := qi.qtype
	if d.cname != "" {
		if t == 5 || t == 255 {
			resp = appendRR(resp, 5, d.ttl, packPlainName(d.cname))
			binary.BigEndian.PutUint16(resp[6:], 1)
			return resp
		}
		return r.chase(ctx, p, resp, query, qi)
	}
	n := 0
	if t == typeA || t == 255 {
		for _, a := range d.a {
			resp = appendRR(resp, typeA, d.ttl, a.AsSlice())
			n++
		}
	}
	if t == typeAAAA || t == 255 {
		for _, a := range d.aaaa {
			resp = appendRR(resp, typeAAAA, d.ttl, a.AsSlice())
			n++
		}
	}
	if t == 16 || t == 255 {
		for _, strs := range d.txt {
			var rd []byte
			for _, s := range strs {
				rd = append(rd, byte(len(s)))
				rd = append(rd, s...)
			}
			resp = appendRR(resp, 16, d.ttl, rd)
			n++
		}
	}
	binary.BigEndian.PutUint16(resp[6:], uint16(n))
	return resp
}

// chase answers a query for a name that is a CNAME of another: the CNAME, then the pool's answer for the target.
func (r *policyRule) chase(ctx context.Context, p *Pool, resp, query []byte, qi *qinfo) []byte {
	d := r.rec
	q := make([]byte, 12, 12+len(d.cname)+6)
	binary.BigEndian.PutUint16(q[0:], uint16(rand.Uint32()))
	binary.BigEndian.PutUint16(q[2:], 0x0100)
	binary.BigEndian.PutUint16(q[4:], 1)
	q = append(q, packPlainName(d.cname)...)
	q = append(q, byte(qi.qtype>>8), byte(qi.qtype), 0, 1)
	tr, err := p.forward(ctx, q, false, netip.Addr{}, false)
	if err != nil || len(tr) < 12 {
		return errorResponse(query, rcodeServFail)
	}
	rc := int(tr[3] & 0x0F)
	if rc != rcodeNoError && rc != rcodeNXDomain {
		return errorResponse(query, rcodeServFail)
	}
	w := &msgWriter{buf: resp, seen: map[string]int{}}
	w.buf = appendRR(w.buf, 5, d.ttl, nil)
	at := len(w.buf)
	w.name(splitLabels(d.cname), true)
	binary.BigEndian.PutUint16(w.buf[at-2:], uint16(len(w.buf)-at))
	off, ok := questionEnd(tr)
	if !ok {
		return errorResponse(query, rcodeServFail)
	}
	an, ns := int(binary.BigEndian.Uint16(tr[6:])), int(binary.BigEndian.Uint16(tr[8:]))
	for i := 0; i < an+ns; i++ {
		if off, err = copyRR(w, tr, off, func(l []string) []string { return l }); err != nil {
			return errorResponse(query, rcodeServFail)
		}
	}
	out := w.buf
	out[3] = out[3]&0xF0 | byte(rc)
	binary.BigEndian.PutUint16(out[6:], uint16(1+an))
	binary.BigEndian.PutUint16(out[8:], uint16(ns))
	return out
}
