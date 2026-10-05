package main

import (
	"errors"
	"net"
	"strings"
)

// qtypes maps the query-type names offered by the UI and API to their
// numeric DNS TYPE values.
var qtypes = map[string]uint16{
	"A": 1, "NS": 2, "CNAME": 5, "SOA": 6, "PTR": 12, "MX": 15,
	"TXT": 16, "AAAA": 28, "SRV": 33, "ANY": 255,
}

// qtypeID returns the numeric TYPE for name; unknown names fall back to A.
func qtypeID(name string) uint16 {
	if v, ok := qtypes[strings.ToUpper(strings.TrimSpace(name))]; ok {
		return v
	}
	return 1
}

// encodeName converts a presentation-format domain name to DNS wire labels.
func encodeName(name string) ([]byte, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" {
		return []byte{0}, nil // root
	}
	if len(name) > 253 {
		return nil, errors.New("name too long")
	}
	out := make([]byte, 0, len(name)+2)
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			return nil, errors.New("empty label")
		}
		if len(label) > 63 {
			return nil, errors.New("label too long")
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	return append(out, 0), nil
}

// buildQuery returns a complete DNS query packet with a zero transaction ID;
// callers patch bytes 0-1 with the ID they want.
func buildQuery(name string, qtype uint16, recurse bool) ([]byte, error) {
	if qtype == 12 { // PTR: accept a bare IP address and turn it into an arpa name
		if ip := net.ParseIP(strings.TrimSpace(name)); ip != nil {
			name = arpaName(ip)
		}
	}
	qname, err := encodeName(name)
	if err != nil {
		return nil, err
	}
	pkt := make([]byte, 0, 12+len(qname)+4)
	var flags byte
	if recurse {
		flags = 0x01 // RD
	}
	pkt = append(pkt,
		0, 0, // ID
		flags, 0, // flags
		0, 1, // QDCOUNT
		0, 0, 0, 0, 0, 0, // AN/NS/AR
	)
	pkt = append(pkt, qname...)
	pkt = append(pkt, byte(qtype>>8), byte(qtype), 0, 1) // QTYPE, QCLASS=IN
	return pkt, nil
}

// arpaName builds the in-addr.arpa / ip6.arpa name for ip.
func arpaName(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return itoa(int(v4[3])) + "." + itoa(int(v4[2])) + "." + itoa(int(v4[1])) + "." + itoa(int(v4[0])) + ".in-addr.arpa"
	}
	const hex = "0123456789abcdef"
	ip = ip.To16()
	var b strings.Builder
	for i := 15; i >= 0; i-- {
		b.WriteByte(hex[ip[i]&0x0f])
		b.WriteByte('.')
		b.WriteByte(hex[ip[i]>>4])
		b.WriteByte('.')
	}
	b.WriteString("ip6.arpa")
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// Result slots used by the counters: 0-15 are DNS RCODEs, then two specials.
const (
	rcTooShort = 16
	rcError    = 17
	rcSlots    = 18
)

// rcodeSlot classifies a response packet.
func rcodeSlot(resp []byte) int {
	if len(resp) < 4 {
		return rcTooShort
	}
	return int(resp[3] & 0x0f)
}

// rcodeLabel is the display name for a counter slot. All RCODEs above 5
// share the label UNKNOWN.
func rcodeLabel(slot int) string {
	switch slot {
	case 0:
		return "NOERROR"
	case 1:
		return "FORMERR"
	case 2:
		return "SERVFAIL"
	case 3:
		return "NXDOMAIN"
	case 4:
		return "NOTIMP"
	case 5:
		return "REFUSED"
	case rcTooShort:
		return "TOOSHORT"
	case rcError:
		return "ERROR"
	}
	return "UNKNOWN"
}
