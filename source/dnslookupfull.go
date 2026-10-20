package main

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Lookup of a client address or a name, from Statistics ▸ right-click ▸ Lookup (and `dnsdir --lookup`): what this node's
// resolver says about it, asked once when requested.  An address gets its reverse (PTR) names; a name gets its
// addresses, canonical name, mail servers, name servers and text records.  Nothing is stored.
const lookupFullTimeout = 5 * time.Second

// LookupFull is the answer: Text is what the popup shows.
type LookupFull struct {
	Query string `json:"query"`
	Kind  string `json:"kind"` // address or name
	Text  string `json:"text"`
}

func lookupFull(q string) (*LookupFull, error) {
	key, ip, name, err := netTarget(q)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), lookupFullTimeout)
	defer cancel()
	r := &LookupFull{Query: key}
	var b strings.Builder
	if name == "" {
		r.Kind = "address"
		names, err := net.DefaultResolver.LookupAddr(ctx, ip.String())
		sort.Strings(names)
		if err != nil || len(names) == 0 {
			fmt.Fprintf(&b, "No reverse (PTR) record for %s", ip)
		}
		for _, n := range names {
			fmt.Fprintf(&b, "PTR    %s\n", n)
		}
		r.Text = strings.TrimRight(b.String(), "\n")
		return r, nil
	}
	r.Kind = "name"
	var mu sync.Mutex
	got := map[string][]string{}
	add := func(t string, v ...string) {
		mu.Lock()
		got[t] = append(got[t], v...)
		mu.Unlock()
	}
	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	res := net.DefaultResolver
	run(func() {
		if ips, err := res.LookupNetIP(ctx, "ip4", name); err == nil {
			for _, a := range ips {
				add("A", a.Unmap().String())
			}
		}
	})
	run(func() {
		if ips, err := res.LookupNetIP(ctx, "ip6", name); err == nil {
			for _, a := range ips {
				add("AAAA", a.String())
			}
		}
	})
	run(func() {
		if c, err := res.LookupCNAME(ctx, name); err == nil && strings.TrimSuffix(strings.ToLower(c), ".") != name {
			add("CNAME", c)
		}
	})
	run(func() {
		if mx, err := res.LookupMX(ctx, name); err == nil {
			for _, m := range mx {
				add("MX", fmt.Sprintf("%d %s", m.Pref, m.Host))
			}
		}
	})
	run(func() {
		if ns, err := res.LookupNS(ctx, name); err == nil {
			for _, n := range ns {
				add("NS", n.Host)
			}
		}
	})
	run(func() {
		if txt, err := res.LookupTXT(ctx, name); err == nil {
			for _, t := range txt {
				if len(t) > 300 {
					t = t[:300] + "…"
				}
				add("TXT", `"`+t+`"`)
			}
		}
	})
	wg.Wait()
	for _, t := range []string{"A", "AAAA", "CNAME", "MX", "NS", "TXT"} {
		v := got[t]
		sort.Strings(v)
		for _, s := range v {
			fmt.Fprintf(&b, "%-6s %s\n", t, s)
		}
	}
	if b.Len() == 0 {
		fmt.Fprintf(&b, "Nothing found for %s", name)
	}
	r.Text = strings.TrimRight(b.String(), "\n")
	return r, nil
}
