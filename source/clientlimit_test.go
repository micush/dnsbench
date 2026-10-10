package main

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func limCfg(f func(*DNSConfig)) DNSConfig {
	d := defaultDNS()
	d.Servers = []string{"127.0.0.1:5353"}
	d.Queries = []DNSQuery{{Name: "a.example", Type: "A"}}
	f(&d)
	if err := d.Validate(); err != nil {
		panic(err)
	}
	return d
}

func TestClientRateBurstThenRefill(t *testing.T) {
	l := newClientLimits(limCfg(func(d *DNSConfig) { d.ClientRate = 2; d.ClientBurst = 5 }))
	t0 := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	c := mustAddr("192.0.2.7")
	for i := 0; i < 5; i++ {
		if v := l.check(c, t0); v != admitOK {
			t.Fatalf("query %d inside the burst: %v", i, v)
		}
	}
	if v := l.check(c, t0); v != admitLimited {
		t.Fatalf("the 6th at once must be limited, got %v", v)
	}
	if v := l.check(mustAddr("192.0.2.8"), t0); v != admitOK {
		t.Fatal("another client has its own bucket")
	}
	// 2 a second: after 1.5 s there are 3 tokens
	for i := 0; i < 3; i++ {
		if v := l.check(c, t0.Add(1500*time.Millisecond)); v != admitOK {
			t.Fatalf("refilled query %d: %v", i, v)
		}
	}
	if v := l.check(c, t0.Add(1500*time.Millisecond)); v != admitLimited {
		t.Fatal("only 3 tokens had come back")
	}
	// a long pause refills to the burst, never beyond it
	n := 0
	for l.check(c, t0.Add(time.Hour)) == admitOK {
		n++
		if n > 100 {
			break
		}
	}
	if n != 5 {
		t.Fatalf("after a pause the client gets exactly the burst, got %d", n)
	}
}

func TestClientKeyIsAnIPv6Slash64(t *testing.T) {
	l := newClientLimits(limCfg(func(d *DNSConfig) { d.ClientRate = 1; d.ClientBurst = 1 }))
	t0 := time.Now()
	if l.check(mustAddr("2001:db8:1:2::1"), t0) != admitOK {
		t.Fatal("first")
	}
	if l.check(mustAddr("2001:db8:1:2:ffff::9"), t0) != admitLimited {
		t.Fatal("another address in the same /64 shares the bucket")
	}
	if l.check(mustAddr("2001:db8:1:3::1"), t0) != admitOK {
		t.Fatal("a different /64 has its own")
	}
	// ::ffff:a.b.c.d is the same client as a.b.c.d
	if l.check(mustAddr("198.51.100.4"), t0) != admitOK || l.check(mustAddr("::ffff:198.51.100.4"), t0) != admitLimited {
		t.Fatal("an IPv4-mapped address counts as its IPv4 address")
	}
}

func TestAllowedClientsExemptAndLoopback(t *testing.T) {
	l := newClientLimits(limCfg(func(d *DNSConfig) {
		d.AllowedClients = []string{"10.0.0.0/8", "192.168.1.5", "2001:db8::/32"}
		d.ClientRate = 1
		d.ClientBurst = 1
		d.ClientExempt = []string{"10.9.0.0/16"}
	}))
	t0 := time.Now()
	cases := []struct {
		ip   string
		want admitVerdict
	}{
		{"10.1.2.3", admitOK}, {"192.168.1.5", admitOK}, {"2001:db8:5::1", admitOK},
		{"192.168.1.6", admitDenied}, {"8.8.8.8", admitDenied}, {"2001:db9::1", admitDenied},
		{"127.0.0.1", admitOK}, {"::1", admitOK}, // the node itself: neither listed nor limited
	}
	for _, c := range cases {
		if got := l.check(mustAddr(c.ip), t0); got != c.want {
			t.Errorf("%s: got %v want %v", c.ip, got, c.want)
		}
	}
	for i := 0; i < 20; i++ {
		if l.check(mustAddr("127.0.0.1"), t0) != admitOK || l.check(mustAddr("10.9.1.1"), t0) != admitOK {
			t.Fatal("loopback and exempt networks are never limited")
		}
	}
	if l.check(mustAddr("10.1.2.3"), t0) != admitLimited {
		t.Fatal("a listed client is still rate limited")
	}
}

func TestNoLimitsMeansNoLimiter(t *testing.T) {
	if newClientLimits(limCfg(func(d *DNSConfig) {})) != nil {
		t.Fatal("nothing configured must cost nothing")
	}
	if NewPool(limCfg(func(d *DNSConfig) {})).admit(mustAddr("8.8.8.8")) != admitOK {
		t.Fatal("everyone is allowed by default")
	}
}

func TestLimiterMemoryIsBounded(t *testing.T) {
	l := newClientLimits(limCfg(func(d *DNSConfig) { d.ClientRate = 1; d.ClientBurst = 1 }))
	t0 := time.Now()
	for i := 0; i < limShards*limShardMax*2; i++ {
		l.check(netip.AddrFrom4([4]byte{byte(i >> 24), byte(i >> 16), byte(i >> 8), byte(i)}), t0)
	}
	if n := l.clients(); n > limShards*limShardMax {
		t.Fatalf("%d clients kept, the bound is %d", n, limShards*limShardMax)
	}
}

func TestClientConfigValidation(t *testing.T) {
	d := limCfg(func(d *DNSConfig) {
		d.AllowedClients = []string{"10.1.2.3/8", "10.0.0.0/8", "::ffff:192.0.2.1", "192.0.2.1"}
		d.ClientAction = "drop"
	})
	if len(d.AllowedClients) != 2 || d.AllowedClients[0] != "10.0.0.0/8" || d.AllowedClients[1] != "192.0.2.1/32" {
		t.Fatalf("normalised/deduplicated: %v", d.AllowedClients)
	}
	if d.ClientAction != "" {
		t.Fatalf("the default action is not stored: %q", d.ClientAction)
	}
	for name, f := range map[string]func(*DNSConfig){
		"bad network": func(d *DNSConfig) { d.AllowedClients = []string{"not-an-ip"} },
		"bad exempt":  func(d *DNSConfig) { d.ClientExempt = []string{"10.0.0.0/33"} },
		"bad action":  func(d *DNSConfig) { d.ClientAction = "ban" },
		"neg rate":    func(d *DNSConfig) { d.ClientRate = -1 },
		"neg burst":   func(d *DNSConfig) { d.ClientBurst = -5 },
	} {
		x := defaultDNS()
		x.Servers = []string{"127.0.0.1:5353"}
		x.Queries = []DNSQuery{{Name: "a.example", Type: "A"}}
		f(&x)
		if x.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// a config without the keys writes none of them (older versions refuse unknown keys)
	b, _ := json.Marshal(defaultDNS())
	for _, k := range []string{"allowed_clients", "client_rate", "client_burst", "client_action", "client_exempt"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("%s is written for a config that does not use it", k)
		}
	}
}

// through the frontend: the list and the rate apply to every transport, before the cache and to updates
func TestFrontendRefusesAndLimits(t *testing.T) {
	up := newFakeDNS(t)
	run := func(f func(*DNSConfig)) (*DNSFrontend, *Pool) {
		c := testDNSCfg(up.addr)
		f(&c)
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		p := NewPool(c)
		p.ProbeNow(context.Background())
		fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return p })
		fe.ctx, fe.cancel = context.WithCancel(context.Background())
		t.Cleanup(fe.cancel)
		return fe, p
	}
	q := cacheQuery("lim.example", 1, nil)
	out := mustAddr("203.0.113.9")

	// not on the list: REFUSED, over UDP and TCP, nothing reaches the server; the node itself is fine
	fe, p := run(func(c *DNSConfig) { c.AllowedClients = []string{"10.0.0.0/8"} })
	before := up.hits.Load()
	for _, tcp := range []bool{false, true} {
		if rc := rcodeOf(t, fe.resolve(q, tcp, out)); rc != rcodeRefused {
			t.Fatalf("tcp=%v: not allowed must be REFUSED, got %d", tcp, rc)
		}
	}
	if up.hits.Load() != before || p.Denied.Load() != 2 {
		t.Fatalf("refused queries must not reach the server (%d), denied=%d", up.hits.Load()-before, p.Denied.Load())
	}
	if rc := rcodeOf(t, fe.resolve(q, false, mustAddr("10.2.3.4"))); rc != rcodeNoError {
		t.Fatalf("an allowed client: %d", rc)
	}
	if rc := rcodeOf(t, fe.resolve(q, false, mustAddr("127.0.0.1"))); rc != rcodeNoError {
		t.Fatalf("the node itself: %d", rc)
	}
	// dynamic updates are held to the list as well
	if rc := rcodeOf(t, fe.resolve(updateMsg(1, "corp.test", typeSOA, 1), false, out)); rc != rcodeRefused {
		t.Fatalf("an update from outside the list: %d", rc)
	}

	// over the rate: drop (default), truncate, refused; a stream always gets REFUSED
	for _, tc := range []struct{ action string }{{""}, {actTruncate}, {actRefused}} {
		fe, p := run(func(c *DNSConfig) { c.ClientRate = 1; c.ClientBurst = 2; c.ClientAction = tc.action })
		for i := 0; i < 2; i++ {
			if rc := rcodeOf(t, fe.resolve(q, false, out)); rc != rcodeNoError {
				t.Fatalf("%q: inside the burst: %d", tc.action, rc)
			}
		}
		r := fe.resolve(q, false, out)
		switch tc.action {
		case "":
			if r != nil {
				t.Fatalf("drop must send nothing, got %x", r)
			}
		case actTruncate:
			h, _ := parseHeader(r)
			if h.rcode != rcodeNoError || r[2]&0x02 == 0 || h.ancount != 0 || h.id != uint16(q[0])<<8|uint16(q[1]) {
				t.Fatalf("truncate: %+v flags %x", h, r[2:4])
			}
		case actRefused:
			if rc := rcodeOf(t, r); rc != rcodeRefused {
				t.Fatalf("refused: %d", rc)
			}
		}
		if rc := rcodeOf(t, fe.resolve(q, true, out)); rc != rcodeRefused {
			t.Fatalf("%q over TCP must be REFUSED, got %d", tc.action, rc)
		}
		if p.Limited.Load() != 2 {
			t.Fatalf("%q: limited=%d", tc.action, p.Limited.Load())
		}
	}
}

func TestRefusalLogIsThrottled(t *testing.T) {
	l := newClientLimits(limCfg(func(d *DNSConfig) { d.ClientRate = 1 }))
	t0 := time.Now()
	c := mustAddr("192.0.2.1")
	l.noteRefusal(c, "x", t0) // logged at once: pending reset
	if l.pending != 0 {
		t.Fatalf("the first refusal is logged at once, pending=%d", l.pending)
	}
	for i := 1; i <= 50; i++ {
		l.noteRefusal(c, "x", t0.Add(time.Duration(i)*time.Second/2))
	}
	if l.pending != 50 {
		t.Fatalf("within a minute only count: pending=%d", l.pending)
	}
	l.noteRefusal(c, "x", t0.Add(61*time.Second))
	if l.pending != 0 {
		t.Fatalf("after a minute the next one logs and resets: pending=%d", l.pending)
	}
}
