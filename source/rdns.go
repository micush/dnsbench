package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
)

// Reverse lookups for the client list (qstats.go) ask the DNS servers ddgw itself forwards to, through
// the pools, before they try the machine's own resolver: the node's /etc/resolv.conf may point at
// nothing useful (a resolver that moved to another port, the VIP before it is up), while the servers
// ddgw probes and forwards to are known to answer and are the ones that hold the PTR records.

// reverseName is the PTR owner name of an address: d.c.b.a.in-addr.arpa, or the 32 nibbles under ip6.arpa.
func reverseName(ip string) (string, bool) {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return "", false
	}
	a = a.Unmap()
	if a.Is4() {
		b := a.As4()
		return itoa(int(b[3])) + "." + itoa(int(b[2])) + "." + itoa(int(b[1])) + "." + itoa(int(b[0])) + ".in-addr.arpa", true
	}
	const hex = "0123456789abcdef"
	b := a.As16()
	var sb strings.Builder
	for i := 15; i >= 0; i-- {
		sb.WriteByte(hex[b[i]&0x0F])
		sb.WriteByte('.')
		sb.WriteByte(hex[b[i]>>4])
		sb.WriteByte('.')
	}
	sb.WriteString("ip6.arpa")
	return sb.String(), true
}

// ptrNames reads the PTR targets out of a response (lower case, no trailing dot), in the order given.
func ptrNames(resp []byte) []string {
	h, ok := parseHeader(resp)
	if !ok || !h.qr || h.rcode != rcodeNoError {
		return nil
	}
	off := 12
	for i := 0; i < int(h.qdcount); i++ {
		n, ok := skipName(resp, off)
		if !ok || n+4 > len(resp) {
			return nil
		}
		off = n + 4
	}
	var out []string
	for i := 0; i < int(h.ancount); i++ {
		n, ok := skipName(resp, off)
		if !ok || n+10 > len(resp) {
			break
		}
		typ := binary.BigEndian.Uint16(resp[n:])
		rdlen := int(binary.BigEndian.Uint16(resp[n+8:]))
		rd := n + 10
		if rd+rdlen > len(resp) {
			break
		}
		if typ == 12 {
			if name, _, err := readName(resp, rd); err == nil && name != "." {
				out = append(out, name)
			}
		}
		off = rd + rdlen
	}
	return out
}

// lookupPTR asks the pool's servers for the PTR records of ip.  The query is not counted as a client's.
func (p *Pool) lookupPTR(ctx context.Context, ip string) ([]string, error) {
	name, ok := reverseName(ip)
	if !ok {
		return nil, errors.New("not an address")
	}
	var id [2]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	q, err := buildQuery(binary.BigEndian.Uint16(id[:]), name, "PTR")
	if err != nil {
		return nil, err
	}
	resp, err := p.forward(ctx, q, false, netip.Addr{}, false)
	if err != nil {
		return nil, err
	}
	return ptrNames(resp), nil
}

// ptrViaPools tries every pool of the supervisor in turn and returns the first names found.
func (s *Supervisor) ptrViaPools(ctx context.Context, ip string) []string {
	for _, pi := range s.poolList() {
		if names, err := pi.Pool.lookupPTR(ctx, ip); err == nil && len(names) > 0 {
			return names
		}
	}
	return nil
}
