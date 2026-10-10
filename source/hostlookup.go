package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"
)

// Name and address lookups for the "add DNS server" form (and `ddgw --dns-lookup`): given an IP address, its reverse
// (PTR) name; given a host name, its address.  They are asked of this node's own resolver, once, when the person fills
// in one of the two fields, and nothing is stored: the form puts the answer in the other field, where it can be
// changed freely.

const lookupTimeout = 3 * time.Second

// LookupResult is what a lookup found.  Kind says which way it went: "name" (an address was given, Name is its reverse
// record) or "address" (a name was given, Addr is its address, IPv4 preferred).
type LookupResult struct {
	Query string `json:"query"`
	Kind  string `json:"kind"`
	Name  string `json:"name,omitempty"`
	Addr  string `json:"addr,omitempty"`
	Found bool   `json:"found"`
	Error string `json:"error,omitempty"` // why nothing was found, in words for the form
}

// lookupHost takes what the form holds in the address field ("8.8.8.8", "[::1]:5353", "dns.lan:53") and
// returns the bare host.  A tls:// or https:// server is left alone: its host is what it is.
func lookupHost(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") {
		return ""
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().String()
	}
	if a, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return a.String()
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

func hostLookup(q string) (LookupResult, error) {
	q = strings.TrimSpace(q)
	if q == "" || len(q) > 255 {
		return LookupResult{}, errors.New("nothing to look up")
	}
	host := lookupHost(q)
	if host == "" {
		return LookupResult{}, errors.New("a tls:// or https:// server has no separate address to look up")
	}
	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	r := LookupResult{Query: q}
	if a, err := netip.ParseAddr(host); err == nil {
		r.Kind = "name"
		names, err := net.DefaultResolver.LookupAddr(ctx, a.String())
		if err != nil || len(names) == 0 {
			r.Error = "no reverse (PTR) record for " + a.String()
			return r, nil
		}
		sort.Strings(names)
		n := strings.TrimSuffix(names[0], ".")
		if len([]rune(n)) > 40 { // the diagram label is at most 40 characters: fall back to the first label
			n = strings.SplitN(n, ".", 2)[0]
		}
		r.Name, r.Found = n, true
		return r, nil
	}
	r.Kind = "address"
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		r.Error = "could not find an address for " + host
		return r, nil
	}
	best := ips[0].Unmap()
	for _, ip := range ips {
		if ip.Unmap().Is4() {
			best = ip.Unmap()
			break
		}
	}
	r.Addr, r.Found = best.String(), true
	return r, nil
}
