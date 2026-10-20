package main

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
)

func TestServerPasses(t *testing.T) {
	for _, c := range []struct {
		ok, n, pct int
		want       bool
	}{
		{3, 3, 50, true}, {2, 3, 50, true}, // one of three failing is 33 %: degraded, still in use
		{1, 2, 50, false}, {2, 4, 50, false}, // exactly half failing: down
		{3, 4, 50, true}, {1, 3, 50, false}, {1, 1, 50, true}, {0, 1, 50, false},
		{0, 3, 100, false}, {1, 3, 100, true}, // 100: down only when every query fails
		{3, 3, 1, true}, {2, 3, 1, false}, // 1: down on any failure (the old "all")
		{0, 0, 50, false},
		{5, 10, 50, false}, {6, 10, 50, true}, {5, 10, 51, true}, // the boundary is "at least"
	} {
		if got := serverPasses(c.ok, c.n, c.pct); got != c.want {
			t.Errorf("%d of %d passing at %d%%: %v, want %v", c.ok, c.n, c.pct, got, c.want)
		}
	}
}

// nameDNS answers every query except those for names that start with "bad", which get NXDOMAIN.
func nameDNS(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := append([]byte(nil), buf[:n]...)
			name, _, _ := readName(q, 12)
			if strings.HasPrefix(name, "bad") {
				pc.WriteTo(reply(q, 3, 0), from)
			} else {
				pc.WriteTo(reply(q, 0, 1), from)
			}
		}
	}()
	t.Cleanup(func() { pc.Close() })
	return pc.LocalAddr().String()
}

func poolWith(t *testing.T, addr string, pct int, names ...string) *Pool {
	t.Helper()
	cfg := testDNSCfg(addr)
	cfg.Queries = nil
	for _, n := range names {
		cfg.Queries = append(cfg.Queries, DNSQuery{Name: n, Type: "A"})
	}
	cfg.DownPercent = pct
	cfg.FailThreshold = 1
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	return p
}

func TestDownPercentOnARealPool(t *testing.T) {
	addr := nameDNS(t)
	// one dead domain of three (the report: the same dead domain on every server took every server down)
	p := poolWith(t, addr, 50, "a.example", "b.example", "bad1.example")
	st := p.Snapshot()[0]
	if !st.Healthy || st.Rank != 1 {
		t.Fatalf("1 of 3 failing must leave the server in use: %+v", st)
	}
	bad := 0
	for _, ts := range st.Tests {
		if !ts.OK {
			bad++
		}
	}
	if bad != 1 {
		t.Fatalf("the failing domain must still be reported: %+v", st.Tests)
	}
	// it still takes client queries
	q, _ := buildQuery(0x77, "www.example", "A")
	if _, err := p.Forward(context.Background(), q, false); err != nil {
		t.Fatalf("a degraded server did not answer: %v", err)
	}
	// half failing: down
	if p := poolWith(t, addr, 50, "a.example", "bad1.example"); p.Snapshot()[0].Healthy {
		t.Fatal("1 of 2 failing is 50 %: the server must be down")
	}
	if p := poolWith(t, addr, 50, "a.example", "b.example", "bad1.example", "bad2.example"); p.Snapshot()[0].Healthy {
		t.Fatal("2 of 4 failing is 50 %: the server must be down")
	}
	// the cutoff can be moved
	if p := poolWith(t, addr, 100, "a.example", "bad1.example"); !p.Snapshot()[0].Healthy {
		t.Fatal("at 100 % only every query failing takes the server down")
	}
	if p := poolWith(t, addr, 1, "a.example", "b.example", "bad1.example"); p.Snapshot()[0].Healthy {
		t.Fatal("at 1 % any failing query takes the server down")
	}
	// every query failing is down at any setting
	if p := poolWith(t, addr, 100, "bad1.example", "bad2.example"); p.Snapshot()[0].Healthy {
		t.Fatal("nothing answering must be down")
	}
}

func TestCanvasShowsDegradedForAFailingDomain(t *testing.T) {
	addr := nameDNS(t)
	d := testDNSCfg(addr)
	d.Queries = []DNSQuery{{Name: "a.example", Type: "A"}, {Name: "b.example", Type: "A"}, {Name: "bad1.example", Type: "A"}}
	g := defaultGroup()
	g.DNSProxy, g.DNS = true, &d
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	s := NewSupervisor(context.Background(), dc)
	s.refreshPool(dc, true)
	t.Cleanup(func() { s.StopAll() })
	rows := []SnapshotRow{{GroupID: 1, AF: "v4", Local: true, State: "active", DNSUp: true}}
	cv := buildCanvas(dc, rows, s.poolList())
	sv := cv[0].Servers[0]
	if sv.Status != "warn" || !strings.Contains(sv.Detail, "1 test(s) failing") {
		t.Fatalf("a server with one of three domains failing is degraded (amber), got %s: %s", sv.Status, sv.Detail)
	}
	if cv[0].Status == "bad" {
		t.Fatalf("the gateway must not be red while a server is in use: %s", cv[0].Detail)
	}
}

func TestDownPercentConfig(t *testing.T) {
	if defaultDNS().DownPercent != 100 {
		t.Fatalf("default %d, want 100", defaultDNS().DownPercent)
	}
	d := defaultDNS()
	if err := d.UnmarshalJSON([]byte(`{"servers":["10.0.0.53"],"queries":["example.com"],"down_percent":75}`)); err != nil || d.DownPercent != 75 || d.Validate() != nil {
		t.Fatalf("parsed %+v: %v", d, err)
	}
	for _, bad := range []int{0, -1, 101} {
		d.DownPercent = bad
		if d.Validate() == nil {
			t.Fatalf("down_percent %d accepted", bad)
		}
	}
	// a file from before has "require": it is accepted, ignored, and not written back; the default applies
	for _, old := range []string{"all", "any", "whatever"} {
		var o DNSConfig
		if err := o.UnmarshalJSON([]byte(`{"servers":["10.0.0.53"],"queries":["example.com"],"require":"` + old + `"}`)); err != nil {
			t.Fatalf("old require %q refused: %v", old, err)
		}
		if o.DownPercent != 100 || o.Validate() != nil {
			t.Fatalf("old file %q: %+v", old, o)
		}
		b, _ := json.Marshal(o)
		if strings.Contains(string(b), "require") || !strings.Contains(string(b), `"down_percent":100`) {
			t.Fatalf("written back as %s", b)
		}
	}
}
