package main

import (
	"context"
	"testing"
)

func pauseTestConfig() *DaemonConfig {
	dc := newDaemonConfig()
	g := defaultGroup()
	g.DNS = nil
	dc.Groups = []GroupConfig{g}
	dc.DNS.Servers = []string{"192.0.2.1:53", "192.0.2.2:53"}
	dc.DNS.Queries = []DNSQuery{{Name: "a.example", Type: "A"}, {Name: "b.example", Type: "A"}}
	if err := dc.Validate(); err != nil {
		panic(err)
	}
	return dc
}

func TestPauseScopeGatewayServerDomain(t *testing.T) {
	dc := pauseTestConfig()
	ed := func(kind, action, scope string) error {
		_, err := applyCanvasEdit(dc, canvasEdit{Action: action, Kind: kind, Group: 1, Server: "192.0.2.1:53", Name: "a.example", Scope: scope})
		return err
	}
	// gateway: default node, all is shared
	if err := ed("gateway", "pause", ""); err != nil || !dc.Groups[0].Paused {
		t.Fatalf("gateway default scope: %v %v", err, dc.Groups[0].Paused)
	}
	if err := ed("gateway", "pause", "all"); err != nil || !dc.Groups[0].PausedAll {
		t.Fatalf("gateway all: %v", err)
	}
	// server: default all, node is local
	if err := ed("server", "pause", ""); err != nil || len(dc.Groups[0].DNS.PausedServers) != 1 {
		t.Fatalf("server default scope: %v %v", err, dc.Groups[0].DNS.PausedServers)
	}
	if err := ed("server", "pause", "node"); err != nil || len(dc.PausedServersHere) != 1 {
		t.Fatalf("server node: %v", err)
	}
	// domain: scope required; the last active domain of a server can be paused too
	if err := ed("domain", "pause", ""); err == nil {
		t.Fatal("domain pause without a scope accepted")
	}
	if err := ed("domain", "pause", "all"); err != nil || len(dc.Groups[0].DNS.PausedQueries) != 1 {
		t.Fatalf("domain all: %v %v", err, dc.Groups[0].DNS.PausedQueries)
	}
	if err := ed("domain", "pause", "all"); err == nil {
		t.Fatal("pausing twice accepted")
	}
	dc.Groups[0].DNS.PausedQueries = nil
	if err := ed("domain", "pause", "node"); err != nil || len(dc.PausedQueriesHere) != 1 {
		t.Fatalf("domain node: %v", err)
	}
	// the last active domain can be paused too: the server then counts as down (TestPauseAllDomainsMakesServerDown)
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "pause", Kind: "domain", Group: 1, Server: "192.0.2.1:53", Name: "b.example", Scope: "all"}); err != nil {
		t.Fatalf("the last active domain could not be paused: %v", err)
	}
	dc.Groups[0].DNS.PausedQueries = nil
	if err := ed("domain", "resume", "node"); err != nil || len(dc.PausedQueriesHere) != 0 {
		t.Fatalf("domain resume: %v", err)
	}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPauseScopeEffectiveLiveShared(t *testing.T) {
	dc := pauseTestConfig()
	k := queryKey("192.0.2.1:53", "a.example", "A")
	dc.PausedQueriesHere = []string{k}
	dc.PausedServersHere = []string{"192.0.2.2:53"}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	eff := dc.effective()
	if eff.DNS.queryPausedScope("192.0.2.1:53", DNSQuery{Name: "a.example", Type: "A"}) != "node" || eff.DNS.serverPausedScope("192.0.2.2:53") != "node" {
		t.Fatal("effective config lost the node-local pauses")
	}
	live := eff.DNS.live()
	if len(live.Servers) != 1 || live.Servers[0] != "192.0.2.1:53" {
		t.Fatalf("live servers: %v", live.Servers)
	}
	for _, q := range live.queriesFor("192.0.2.1:53") {
		if q.Name == "a.example" {
			t.Fatal("paused domain still probed")
		}
	}
	// the node-local lists are never replicated; the shared ones are
	if h1 := sharedOf(dc).hash(); h1 != sharedOf(pauseTestConfig()).hash() {
		t.Fatal("node-local pauses changed the shared hash")
	}
	dc.DNS.PausedQueries = []string{queryKey("192.0.2.2:53", "b.example", "A")}
	if sharedOf(dc).hash() == sharedOf(pauseTestConfig()).hash() {
		t.Fatal("shared domain pause not replicated")
	}
	// pausing a domain must restart the pool; a rename of a server label must not
	a, b := pauseTestConfig(), pauseTestConfig()
	b.DNS.PausedQueries = []string{k}
	if reflectEqualLive(a.DNS, b.DNS) {
		t.Fatal("domain pause does not change the live pool")
	}
}

func reflectEqualLive(a, b DNSConfig) bool {
	x, y := a.live(), b.live()
	return len(x.queriesFor("192.0.2.1:53")) == len(y.queriesFor("192.0.2.1:53"))
}

// every probe domain paused: the server is down and out of the pool; all but one paused: the server is amber
func TestPauseAllDomainsMakesServerDown(t *testing.T) {
	dc := pauseTestConfig()
	s1, s2 := "192.0.2.1:53", "192.0.2.2:53"
	dc.DNS.PausedQueriesHere = []string{queryKey(s1, "a.example", "A"), queryKey(s1, "b.example", "A"), queryKey(s2, "a.example", "A")}
	cfg := dc.DNS
	if !cfg.allQueriesPaused(s1) || cfg.allQueriesPaused(s2) {
		t.Fatal("allQueriesPaused")
	}
	live := cfg.live()
	if len(live.Servers) != 1 || live.Servers[0] != s2 {
		t.Fatalf("live servers %v: a server with no probe domain left must be out of the pool", live.Servers)
	}
	if q := live.queriesFor(s2); len(q) != 1 || q[0].Name != "b.example" {
		t.Fatalf("remaining domains %v", q)
	}
	gws := buildCanvas(dc, nil, nil)
	if len(gws) != 1 || len(gws[0].Servers) != 2 {
		t.Fatalf("canvas: %+v", gws)
	}
	if st := gws[0].Servers[0].Status; st != "bad" {
		t.Fatalf("all paused: %q", st)
	}
	// without probe results the second is idle, not amber; give it an ok state to see the amber rule
	if st := gws[0].Servers[1].Status; st != "idle" && st != "warn" {
		t.Fatalf("one left: %q", st)
	}
}

func TestPauseAllButOneDomainIsAmber(t *testing.T) {
	main := newFakeDNS(t)
	dc := newDaemonConfig()
	g := defaultGroup()
	g.DNS = nil
	dc.Groups = []GroupConfig{g}
	dc.DNS = testDNSCfg(main.addr)
	dc.DNS.Queries = []DNSQuery{{Name: "a.example", Type: "A"}, {Name: "b.example", Type: "A"}}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	status := func() string {
		p := NewPool(dc.DNS.live())
		p.ProbeNow(context.Background())
		cv := buildCanvas(dc, nil, []poolInfo{{Key: 0, Pool: p}})
		return cv[0].Servers[0].Status
	}
	if st := status(); st != "ok" {
		t.Fatalf("nothing paused: %q", st)
	}
	dc.DNS.PausedQueriesHere = []string{queryKey(dc.DNS.Servers[0], "a.example", "A")}
	if st := status(); st != "warn" {
		t.Fatalf("all but one paused: %q", st)
	}
	dc.DNS.PausedQueriesHere = append(dc.DNS.PausedQueriesHere, queryKey(dc.DNS.Servers[0], "b.example", "A"))
	if st := status(); st != "bad" {
		t.Fatalf("all paused: %q", st)
	}
}
