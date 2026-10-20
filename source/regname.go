package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/netip"
	"strings"
	"time"
)

// Registering a client's name (Statistics ▸ Top clients ▸ right-click ▸ Name… / Rename…).  After the name is saved this node
// sends a dynamic update (RFC 2136, what nsupdate sends) to the primary server of the name's zone, found like the
// updates a client sends through the gateway (the SOA's MNAME): the name's A or AAAA record is replaced by the client's
// address, and the client's PTR record by the name.  A key (hmac-sha256 TSIG) may be configured; without one the update
// goes unsigned, which a primary that accepts updates by source address takes.

const (
	typePTR      = 12
	classIN      = 1
	classNONE    = 254
	classANY     = 255
	tsigFudge    = 300
	defaultRegTT = 300
)

// validHostName checks a name given to a client: a DNS name of at least two labels (letters, digits, hyphens, underscores).
func validHostName(s string) (string, error) {
	s = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
	if s == "" || len(s) > 253 {
		return "", errors.New("the name is empty or longer than 253 characters")
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return "", errors.New("give the full host name, e.g. ann-pc.corp.example")
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return "", errors.New("a part of the name between dots is empty or longer than 63 characters")
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return "", fmt.Errorf("%q is not allowed in a host name (letters, digits, - and _)", string(c))
			}
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return "", errors.New("a part of the name starts or ends with a hyphen")
		}
	}
	return s, nil
}

// dnsWireName is a domain name in wire form, without compression, lower-cased.
func dnsWireName(name string) []byte {
	var b []byte
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name != "" && name != "." {
		for _, l := range strings.Split(name, ".") {
			b = append(b, byte(len(l)))
			b = append(b, l...)
		}
	}
	return append(b, 0)
}

type updRR struct {
	name  string
	typ   uint16
	class uint16
	ttl   uint32
	rdata []byte
}

func (r updRR) wire() []byte {
	b := dnsWireName(r.name)
	b = binary.BigEndian.AppendUint16(b, r.typ)
	b = binary.BigEndian.AppendUint16(b, r.class)
	b = binary.BigEndian.AppendUint32(b, r.ttl)
	b = binary.BigEndian.AppendUint16(b, uint16(len(r.rdata)))
	return append(b, r.rdata...)
}

// buildUpdate is an UPDATE message for zone with the given records in its update section; with a key it carries a TSIG.
func buildUpdate(id uint16, zone string, rrs []updRR, keyName string, key []byte, now time.Time) []byte {
	m := make([]byte, 12)
	binary.BigEndian.PutUint16(m, id)
	m[2] = opcodeUpdate << 3
	binary.BigEndian.PutUint16(m[4:], 1)
	binary.BigEndian.PutUint16(m[8:], uint16(len(rrs)))
	m = append(m, dnsWireName(zone)...)
	m = binary.BigEndian.AppendUint16(m, typeSOA)
	m = binary.BigEndian.AppendUint16(m, classIN)
	for _, r := range rrs {
		m = append(m, r.wire()...)
	}
	if keyName == "" || len(key) == 0 {
		return m
	}
	return signTSIG(m, keyName, key, now)
}

// signTSIG appends a TSIG record (RFC 8945, hmac-sha256) to a request.
func signTSIG(msg []byte, keyName string, key []byte, now time.Time) []byte {
	alg := dnsWireName("hmac-sha256")
	name := dnsWireName(keyName)
	t := uint64(now.Unix())
	timeFudge := make([]byte, 0, 8)
	timeFudge = append(timeFudge, byte(t>>40), byte(t>>32), byte(t>>24), byte(t>>16), byte(t>>8), byte(t))
	timeFudge = binary.BigEndian.AppendUint16(timeFudge, tsigFudge)
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	mac.Write(name)
	mac.Write(binary.BigEndian.AppendUint16(nil, classANY))
	mac.Write(binary.BigEndian.AppendUint32(nil, 0))
	mac.Write(alg)
	mac.Write(timeFudge)
	mac.Write([]byte{0, 0, 0, 0}) // error, other length
	sum := mac.Sum(nil)
	var rd []byte
	rd = append(rd, alg...)
	rd = append(rd, timeFudge...)
	rd = binary.BigEndian.AppendUint16(rd, uint16(len(sum)))
	rd = append(rd, sum...)
	rd = binary.BigEndian.AppendUint16(rd, binary.BigEndian.Uint16(msg)) // original ID
	rd = append(rd, 0, 0, 0, 0)                                          // error, other length
	out := append([]byte(nil), msg...)
	out = append(out, updRR{name: keyName, typ: typeTSIG, class: classANY, rdata: rd}.wire()...)
	binary.BigEndian.PutUint16(out[10:], binary.BigEndian.Uint16(out[10:])+1)
	return out
}

// findZone asks for the SOA of name and returns the zone that holds it and the primary server its SOA names.
func findZone(ctx context.Context, p *Pool, name string) (zone, mname string, err error) {
	resp, err := lookup(ctx, p, name, typeSOA)
	if err != nil {
		return "", "", err
	}
	rrs, _ := parseRRs(resp)
	for _, rr := range rrs {
		if rr.typ == typeSOA && rr.sec <= 1 {
			m, _, err := readName(resp, rr.off)
			if err != nil {
				return "", "", err
			}
			return rr.name, m, nil
		}
	}
	return "", "", errNoZone
}

// sendRegistration sends one update to the primary of the zone and says how it went.
func sendRegistration(ctx context.Context, p *Pool, zone, mname string, rrs []updRR, keyName string, key []byte) (string, error) {
	var addrs []netip.Addr
	if mname != "." && mname != "" {
		addrs = addrsOf(ctx, p, mname)
	}
	if len(addrs) == 0 {
		addrs = nsAddrs(ctx, p, zone)
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("the primary server %q of zone %s does not resolve", mname, zone)
	}
	msg := buildUpdate(uint16(rand.Uint32()), zone, rrs, keyName, key, time.Now())
	var last error
	for i, a := range addrs {
		if i >= updMaxTargets {
			break
		}
		resp, _, err := exchange(ctx, net.JoinHostPort(a.String(), updPort), msg, false, updTimeout)
		if err == nil {
			if h, _ := parseHeader(resp); h.tc {
				resp, _, err = exchange(ctx, net.JoinHostPort(a.String(), updPort), msg, true, updTimeout)
			}
		}
		if err != nil {
			last = err
			continue
		}
		h, ok := parseHeader(resp)
		if !ok {
			last = errors.New("the answer is not a DNS message")
			continue
		}
		if h.rcode != 0 {
			return "", fmt.Errorf("zone %s, primary %s: %s", zone, a, rcodeName(h.rcode))
		}
		return fmt.Sprintf("zone %s, primary %s: NOERROR", zone, a), nil
	}
	if last == nil {
		last = errors.New("no answer")
	}
	return "", fmt.Errorf("zone %s: %v", zone, last)
}

// registerHostName replaces the address record of name (of the family of addr) and the PTR record of addr; old, when it is
// another name of the same client, loses its address record.  It returns one line for each step.
func registerHostName(ctx context.Context, p *Pool, name, old string, addr netip.Addr, ttl uint32, keyName string, keyB64 string) ([]string, error) {
	var key []byte
	if keyB64 != "" {
		k, err := base64.StdEncoding.DecodeString(keyB64)
		if err != nil {
			return nil, errors.New("the registration key is not valid base64")
		}
		key = k
	}
	if ttl == 0 {
		ttl = defaultRegTT
	}
	addr = addr.Unmap()
	typ, raw := uint16(typeA), addr.AsSlice()
	if addr.Is6() {
		typ = typeAAAA
	}
	var out []string
	var firstErr error
	note := func(what string, msg string, err error) {
		if err != nil {
			out = append(out, what+": "+err.Error())
			if firstErr == nil {
				firstErr = err
			}
			return
		}
		out = append(out, what+": "+msg)
	}

	// the address record: the name's RRset of this family becomes the client's address
	zone, mname, err := findZone(ctx, p, name)
	if err == nil {
		del := []updRR{{name: name, typ: typ, class: classANY}}
		if old != "" && old != name {
			if z2, _, e2 := findZone(ctx, p, old); e2 == nil && z2 == zone {
				del = append(del, updRR{name: old, typ: typ, class: classNONE, rdata: raw}) // the old name no longer is this client
			}
		}
		// the old records go first, in an update of their own, and the new one follows
		var msg string
		msg, err = sendRegistration(ctx, p, zone, mname, del, keyName, key)
		if err == nil {
			msg, err = sendRegistration(ctx, p, zone, mname, []updRR{{name: name, typ: typ, class: classIN, ttl: ttl, rdata: raw}}, keyName, key)
		}
		note(qtypeName(typ)+" of "+name, msg, err)
	} else {
		note(qtypeName(typ)+" of "+name, "", fmt.Errorf("no zone found for the name: %w", err))
	}

	// the reverse record
	if rev, ok := reverseName(addr.String()); ok {
		zone, mname, err := findZone(ctx, p, rev)
		if err == nil {
			ptr := append([]byte(nil), dnsWireName(name)...)
			var msg string
			msg, err = sendRegistration(ctx, p, zone, mname, []updRR{{name: rev, typ: typePTR, class: classANY}}, keyName, key)
			if err == nil {
				msg, err = sendRegistration(ctx, p, zone, mname, []updRR{{name: rev, typ: typePTR, class: classIN, ttl: ttl, rdata: ptr}}, keyName, key)
			}
			note("PTR of "+addr.String(), msg, err)
		} else {
			note("PTR of "+addr.String(), "", fmt.Errorf("no reverse zone found: %w", err))
		}
	}
	return out, firstErr
}

// registerClientName is registerHostName through the first pool of this node that can find the zone.
func (s *Supervisor) registerClientName(ctx context.Context, cfg DNSConfig, name, old string, addr netip.Addr) ([]string, error) {
	pools := s.poolList()
	if len(pools) == 0 {
		return nil, errors.New("this node has no DNS pool to ask")
	}
	var lines []string
	var err error
	for _, pi := range pools {
		lines, err = registerHostName(ctx, pi.Pool, name, old, addr, uint32(cfg.RegisterTTL), cfg.RegisterKeyName, cfg.RegisterKey)
		if err == nil || !strings.Contains(strings.Join(lines, " "), "no zone found") {
			break
		}
	}
	return lines, err
}
