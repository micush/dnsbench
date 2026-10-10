package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func lbTestConfig() *DaemonConfig {
	dc := newDaemonConfig()
	dc.DNS.Servers = []string{"192.0.2.1:53"}
	g := dc.Groups[0]
	own := defaultDNS()
	own.Servers = []string{"192.0.2.9:53"}
	own.Queries = []DNSQuery{{Name: "example.com", Type: "A"}}
	dc.DNS.Queries = []DNSQuery{{Name: "example.com", Type: "A"}}
	g.DNS = &own
	dc.Groups = []GroupConfig{g}
	return dc
}

// A gateway's own pool follows Settings for load balancing until it is given its own values.
func TestGatewayPoolFollowsSettingsLoadBalancing(t *testing.T) {
	dc := lbTestConfig()
	dc.DNS.SpreadBand, dc.DNS.MaxAttempts, dc.DNS.Spread = 200, 5, false
	_, c := dc.poolFor(&dc.Groups[0])
	if c.SpreadBand != 200 || c.MaxAttempts != 5 || c.Spread {
		t.Fatalf("a pool at the defaults must follow Settings, got band %d attempts %d spread %v", c.SpreadBand, c.MaxAttempts, c.Spread)
	}
	if c.Servers[0] != "192.0.2.9:53" {
		t.Fatalf("the gateway's own servers must stay: %v", c.Servers)
	}
	// its own values win, and a later Settings change does not touch them
	lb := LBConfig{Spread: true, SpreadBand: 50, DownPercent: 60, FailThreshold: 4, MaxAttempts: 2, LatencyAlpha: 0.5}
	dc.Groups[0].DNS.LB = &lb
	dc.DNS.SpreadBand = 300
	_, c = dc.poolFor(&dc.Groups[0])
	if c.SpreadBand != 50 || c.DownPercent != 60 || c.FailThreshold != 4 || c.MaxAttempts != 2 || c.LatencyAlpha != 0.5 || !c.Spread || c.LB != nil {
		t.Fatalf("own load balancing: %+v", c)
	}
	// a gateway without its own pool is the shared pool itself
	dc.Groups[0].DNS = nil
	if k, c := dc.poolFor(&dc.Groups[0]); k != 0 || c.SpreadBand != 300 {
		t.Fatalf("shared pool: key %d band %d", k, c.SpreadBand)
	}
}

// A pool written before LB existed, at values other than the defaults, keeps them (they were set on purpose).
func TestLegacyGatewayPoolKeepsItsOwnValues(t *testing.T) {
	dc := lbTestConfig()
	dc.DNS.SpreadBand = 200
	dc.Groups[0].DNS.SpreadBand = 75
	if l, own := dc.Groups[0].DNS.ownLB(); !own || l.SpreadBand != 75 {
		t.Fatalf("a legacy pool at non-default values is the gateway's own: %+v %v", l, own)
	}
	if _, c := dc.poolFor(&dc.Groups[0]); c.SpreadBand != 75 {
		t.Fatalf("band %d, want the pool's own 75", c.SpreadBand)
	}
	dc.Groups[0].DNS.SpreadBand = 20
	if _, own := dc.Groups[0].DNS.ownLB(); own {
		t.Fatal("a pool at the defaults follows Settings")
	}
}

func TestCanvasSetOwnLoadBalancingAndBack(t *testing.T) {
	dc := lbTestConfig()
	dc.DNS.SpreadBand, dc.DNS.MaxAttempts = 200, 4
	gid := dc.Groups[0].GroupID
	msg, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: gid, SpreadBand: 80, Spread: "off"})
	if err != nil || !strings.Contains(msg, "own load balancing") {
		t.Fatalf("set: %q %v", msg, err)
	}
	lb := dc.Groups[0].DNS.LB
	if lb == nil || lb.SpreadBand != 80 || lb.Spread || lb.MaxAttempts != 4 {
		t.Fatalf("the rest must start from what the gateway used (Settings): %+v", lb)
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: gid, SpreadBand: 5000}); err == nil {
		t.Fatal("a band of 5000 must be refused")
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: gid, Spread: "maybe"}); err == nil {
		t.Fatal("--spread must be on or off")
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: gid, LB: "settings", SpreadBand: 10}); err == nil {
		t.Fatal("--lb settings with values must be refused")
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: gid, LB: "own"}); err == nil {
		t.Fatal("--lb only takes settings")
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: gid, LB: "settings"}); err != nil {
		t.Fatal(err)
	}
	if dc.Groups[0].DNS.LB != nil {
		t.Fatal("lb must be gone")
	}
	if _, own := dc.Groups[0].DNS.ownLB(); own {
		t.Fatal("after --lb settings the pool must not count as own again")
	}
	if _, c := dc.poolFor(&dc.Groups[0]); c.SpreadBand != 200 || c.MaxAttempts != 4 {
		t.Fatalf("back to Settings: band %d attempts %d", c.SpreadBand, c.MaxAttempts)
	}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
}

// Giving a gateway its own pool (adding a server, setting ECS) must not freeze today's Settings values into it.
func TestNewOwnPoolStillFollowsSettings(t *testing.T) {
	dc := newDaemonConfig()
	dc.DNS.SpreadBand = 200
	dc.Groups[0].DNS = nil
	gid := dc.Groups[0].GroupID
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: gid, ECS: "off"}); err != nil {
		t.Fatal(err)
	}
	if _, own := dc.Groups[0].DNS.ownLB(); own {
		t.Fatalf("a pool made by setting ECS must follow Settings, has band %d", dc.Groups[0].DNS.SpreadBand)
	}
	dc.DNS.SpreadBand = 300
	if _, c := dc.poolFor(&dc.Groups[0]); c.SpreadBand != 300 {
		t.Fatalf("band %d, want 300 after Settings changed", c.SpreadBand)
	}
}

func TestLoadBalancingConfigRules(t *testing.T) {
	dc := lbTestConfig()
	dc.Groups[0].DNS.LB = &LBConfig{Spread: true, SpreadBand: 0, DownPercent: 50, FailThreshold: 2, MaxAttempts: 3, LatencyAlpha: 0.3}
	if err := dc.Validate(); err == nil || !strings.Contains(err.Error(), "spread_band") {
		t.Fatalf("a gateway's lb is validated: %v", err)
	}
	dc = lbTestConfig()
	dc.DNS.LB = &LBConfig{Spread: true, SpreadBand: 20, DownPercent: 50, FailThreshold: 2, MaxAttempts: 3, LatencyAlpha: 0.3}
	if err := dc.Validate(); err == nil {
		t.Fatal("the shared pool has no lb of its own")
	}
	// absent unless used, so an older version can still read the file
	b, _ := json.Marshal(defaultDNS())
	if strings.Contains(string(b), `"lb"`) {
		t.Fatalf("lb must be omitted when unused: %s", b)
	}
	// round trip, and a deep copy
	d := defaultDNS()
	d.LB = &LBConfig{Spread: true, SpreadBand: 33, DownPercent: 50, FailThreshold: 2, MaxAttempts: 3, LatencyAlpha: 0.3}
	b, _ = json.Marshal(d)
	var back DNSConfig
	if err := json.Unmarshal(b, &back); err != nil || back.LB == nil || back.LB.SpreadBand != 33 {
		t.Fatalf("round trip: %v %+v", err, back.LB)
	}
	c := cloneDNS(d)
	c.LB.SpreadBand = 1
	if d.LB.SpreadBand != 33 {
		t.Fatal("cloneDNS must copy lb")
	}
}

func TestCanvasShowsLoadBalancingInUse(t *testing.T) {
	dc := lbTestConfig()
	dc.DNS.SpreadBand = 200
	cg := buildCanvas(dc, nil, nil)[0]
	if cg.LB.SpreadBand != 200 || cg.LBOwn {
		t.Fatalf("following Settings: %+v own=%v", cg.LB, cg.LBOwn)
	}
	dc.Groups[0].DNS.LB = &LBConfig{Spread: true, SpreadBand: 40, DownPercent: 50, FailThreshold: 2, MaxAttempts: 3, LatencyAlpha: 0.3}
	cg = buildCanvas(dc, nil, nil)[0]
	if cg.LB.SpreadBand != 40 || !cg.LBOwn {
		t.Fatalf("own: %+v own=%v", cg.LB, cg.LBOwn)
	}
}

// The answer cache and the client rules are edited on the Settings page only, so a gateway's own pool must follow
// them: switching the cache off, or setting a rate, there has to reach every gateway.
func TestGatewayPoolFollowsSettingsCacheAndClientRules(t *testing.T) {
	dc := lbTestConfig()
	dc.DNS.Cache = false
	dc.DNS.ClientRate, dc.DNS.ClientBurst, dc.DNS.ClientAction = 5, 7, "refused"
	dc.DNS.AllowedClients = []string{"10.0.0.0/8"}
	dc.DNS.ClientExempt = []string{"10.1.0.0/16"}
	_, c := dc.poolFor(&dc.Groups[0])
	if c.Cache || c.ClientRate != 5 || c.ClientBurst != 7 || c.ClientAction != "refused" ||
		len(c.AllowedClients) != 1 || len(c.ClientExempt) != 1 {
		t.Fatalf("own pool must follow Settings: %+v", c)
	}
	if c.Servers[0] != "192.0.2.9:53" {
		t.Fatalf("the gateway's own servers must stay: %v", c.Servers)
	}
	p := NewPool(c)
	if p.cache != nil {
		t.Fatal("cache switched off in Settings, but the gateway's pool still has one")
	}
	if p.lim == nil {
		t.Fatal("rate set in Settings, but the gateway's pool has no limiter")
	}
	// and back again
	dc.DNS.Cache, dc.DNS.ClientRate, dc.DNS.AllowedClients, dc.DNS.ClientExempt = true, 0, nil, nil
	_, c = dc.poolFor(&dc.Groups[0])
	if p := NewPool(c); p.cache == nil || p.lim != nil {
		t.Fatalf("settings turned back: cache %v limiter %v", p.cache != nil, p.lim != nil)
	}
	// the Settings slices are not shared with the pool's copy
	dc.DNS.AllowedClients = []string{"192.0.2.0/24"}
	_, c = dc.poolFor(&dc.Groups[0])
	c.AllowedClients[0] = "x"
	if dc.DNS.AllowedClients[0] != "192.0.2.0/24" {
		t.Fatal("poolFor must copy the lists")
	}
}
