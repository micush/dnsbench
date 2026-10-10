package main

import (
	"context"
	"strings"
	"testing"
)

func TestNormalizePolicy(t *testing.T) {
	rows, err := normalizePolicy([]PolicyRule{
		{Client: " 10.1.1.1 ", Name: "Reddit.com.", Servers: []string{"10.2.2.2, 10.3.3.3"}},
		{Client: "", Name: "", Servers: []string{"8.8.8.8"}},
		{}, // an empty row is dropped
	})
	if err != nil || len(rows) != 2 {
		t.Fatalf("%v %+v", err, rows)
	}
	if len(rows[0].Servers) != 2 || rows[1].Client != "*" || rows[1].Name != "*" {
		t.Fatalf("%+v", rows)
	}
	for _, bad := range []PolicyRule{
		{Client: "10.1.1", Name: "a.example", Servers: []string{"10.0.0.1"}},
		{Client: "*", Name: "a b", Servers: []string{"10.0.0.1"}},
		{Client: "*", Name: "a.example"},
		{Client: "*", Name: "a.example", Servers: []string{"not a server!"}},
	} {
		if _, err := normalizePolicy([]PolicyRule{bad}); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	d := testDNSCfg("192.0.2.1")
	d.Policy = []PolicyRule{{Client: "*", Name: "x.example", Servers: []string{"192.0.2.9"}}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if !d.PolicyOn {
		t.Fatal("policy must be on by default")
	}
}

func TestPolicyMatching(t *testing.T) {
	p := &Pool{policy: buildPolicy([]PolicyRule{
		{Client: "10.1.1.1", Name: "reddit.com", Servers: []string{"10.2.2.2", "10.3.3.3"}},
		{Client: "10.10.10.0/24", Name: "*.reddit.com", Servers: []string{"10.10.10.1"}},
		{Client: "*", Name: "*.reddit.com", Servers: []string{"8.8.8.8"}},
	})}
	cases := []struct {
		client, name, want string
	}{
		{"10.1.1.1", "reddit.com", "10.2.2.2:53,10.3.3.3:53"},
		{"10.1.1.1", "REDDIT.com.", "10.2.2.2:53,10.3.3.3:53"},
		{"10.1.1.2", "reddit.com", "8.8.8.8:53"}, // *.reddit.com is reddit.com and everything below it
		{"10.10.10.7", "www.reddit.com", "10.10.10.1:53"},
		{"10.10.11.7", "www.reddit.com", "8.8.8.8:53"},
		{"192.0.2.1", "a.b.reddit.com", "8.8.8.8:53"},
		{"192.0.2.1", "notreddit.com", ""},
		{"::ffff:10.1.1.1", "reddit.com", "10.2.2.2:53,10.3.3.3:53"},
	}
	for _, c := range cases {
		var qi qinfo
		parseQuestion(cacheQuery(c.name, 1, nil), &qi)
		r := p.policyFor(mustAddr(c.client), &qi)
		got := ""
		if r != nil {
			got = r.tag
		}
		if got != c.want {
			t.Errorf("%s asks %s: got %q want %q", c.client, c.name, got, c.want)
		}
	}
}

func TestPolicyRoutesAndCachesApart(t *testing.T) {
	main, a, b := newFakeDNS(t), newFakeDNS(t), newFakeDNS(t)
	cfg := testDNSCfg(main.addr)
	cfg.ECS = false
	cfg.Policy = []PolicyRule{
		{Client: "10.1.1.1", Name: "x.example", Servers: []string{a.addr}},
		{Client: "*", Name: "*.example", Servers: []string{b.addr}},
	}
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return p })
	fe.ctx, fe.cancel = context.WithCancel(context.Background())
	t.Cleanup(fe.cancel)

	ask := func(client, name string) {
		t.Helper()
		if h, _ := parseHeader(fe.resolve(cacheQuery(name, 1, nil), false, mustAddr(client))); h.rcode != 0 {
			t.Fatalf("%s %s: rcode %d", client, name, h.rcode)
		}
	}
	m0, a0, b0 := main.hits.Load(), a.hits.Load(), b.hits.Load()
	ask("10.1.1.1", "x.example")  // row 1
	ask("10.9.9.9", "x.example")  // not row 1: the same name must not come from row 1's cache entry
	ask("10.9.9.9", "y.example")  // row 2
	ask("10.9.9.9", "other.test") // no row: the pool
	if a.hits.Load()-a0 != 1 || b.hits.Load()-b0 != 2 || main.hits.Load()-m0 != 1 {
		t.Fatalf("hits: row1 %d row2 %d pool %d, want 1 2 1", a.hits.Load()-a0, b.hits.Load()-b0, main.hits.Load()-m0)
	}
	ask("10.1.1.1", "x.example") // cached per row: no new upstream query
	ask("10.9.9.9", "x.example")
	if a.hits.Load()-a0 != 1 || b.hits.Load()-b0 != 2 {
		t.Fatal("repeats were not answered from the cache")
	}

	// switched off: every query goes to the pool
	cfg.PolicyOn = false
	off := NewPool(cfg)
	if len(off.policy) != 0 {
		t.Fatal("policy built while switched off")
	}
}

func TestPolicyFailsWithoutFallingBackToPool(t *testing.T) {
	main, dead := newFakeDNS(t), newFakeDNS(t)
	dead.mode.Store(2) // drops everything
	cfg := testDNSCfg(main.addr)
	cfg.ECS = false
	cfg.Policy = []PolicyRule{{Client: "*", Name: "x.example", Servers: []string{dead.addr}}}
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	m0 := main.hits.Load()
	q, _ := buildQuery(7, "x.example", "A")
	if _, err := p.ForwardFrom(context.Background(), q, false, mustAddr("10.1.1.1")); err == nil || strings.Contains(err.Error(), "no servers") {
		t.Fatalf("want a timeout from the policy's server, got %v", err)
	}
	if main.hits.Load() != m0 {
		t.Fatal("a policy row's query leaked to the pool")
	}
}

func TestPolicyKeywordsAnswerLocally(t *testing.T) {
	for _, bad := range []PolicyRule{
		{Client: "*", Name: "a.example", Servers: []string{"null", "192.0.2.9"}},
		{Client: "*", Name: "a.example", Servers: []string{"NXDOMAIN"}, Dest: "b.example"},
	} {
		if _, err := normalizePolicy([]PolicyRule{bad}); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	main := newFakeDNS(t)
	cfg := testDNSCfg(main.addr)
	cfg.ECS = false
	cfg.Policy = []PolicyRule{
		{Client: "10.1.1.1", Name: "ok.example", Servers: []string{"pool"}},
		{Client: "*", Name: "*.example", Servers: []string{"Null"}},
		{Client: "*", Name: "gone.test", Servers: []string{"nxdomain"}},
		{Client: "*", Name: "empty.test", Servers: []string{"nodata"}},
		{Client: "*", Name: "no.test", Servers: []string{"refused"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return p })
	fe.ctx, fe.cancel = context.WithCancel(context.Background())
	t.Cleanup(fe.cancel)
	m0 := main.hits.Load()
	ask := func(client, name string, qt uint16) (dnsHeader, []byte) {
		resp := fe.resolve(cacheQuery(name, qt, nil), false, mustAddr(client))
		h, _ := parseHeader(resp)
		return h, resp
	}
	h, resp := ask("10.9.9.9", "x.example", 1)
	if h.rcode != 0 || h.ancount != 1 || !strings.HasSuffix(string(resp), string([]byte{0, 4, 0, 0, 0, 0})) {
		t.Fatalf("null A: %+v", h)
	}
	if h, resp = ask("10.9.9.9", "x.example", 28); h.rcode != 0 || h.ancount != 1 || len(resp) < 16 {
		t.Fatalf("null AAAA: %+v", h)
	}
	if h, _ = ask("10.9.9.9", "x.example", 15); h.rcode != 0 || h.ancount != 0 {
		t.Fatalf("null MX must be no data: %+v", h)
	}
	if h, _ = ask("10.9.9.9", "gone.test", 1); h.rcode != rcodeNXDomain {
		t.Fatalf("nxdomain: %+v", h)
	}
	if h, _ = ask("10.9.9.9", "empty.test", 1); h.rcode != 0 || h.ancount != 0 {
		t.Fatalf("nodata: %+v", h)
	}
	if h, _ = ask("10.9.9.9", "no.test", 1); h.rcode != rcodeRefused {
		t.Fatalf("refused: %+v", h)
	}
	if main.hits.Load() != m0 {
		t.Fatal("a local answer reached a server")
	}
	// the pool row is an exception to the row below it, for that client only
	if h, _ = ask("10.1.1.1", "ok.example", 1); h.rcode != 0 || h.ancount != 1 || main.hits.Load() != m0+1 {
		t.Fatalf("pool exception: %+v hits %d", h, main.hits.Load()-m0)
	}
	if h, resp = ask("10.9.9.9", "ok.example", 1); h.ancount != 1 || main.hits.Load() != m0+1 || resp[len(resp)-1] != 0 {
		t.Fatalf("other clients are still blocked: %+v", h)
	}
}
