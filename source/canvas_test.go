package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPerServerQueriesAndHostnames(t *testing.T) {
	var d DNSConfig
	in := `{"servers":["8.8.8.8","dns.example.com","9.9.9.9:5353"],
		"queries":[],
		"server_queries":{"8.8.8.8":["google.com","microsoft.com"],"dns.example.com":["apple.com"],"9.9.9.9:5353":[{"name":"ford.com","type":"AAAA"}]}}`
	if err := json.Unmarshal([]byte(in), &d); err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := d.Servers[1]; got != "dns.example.com:53" {
		t.Fatalf("hostname normalised to %q", got)
	}
	if q := d.queriesFor("8.8.8.8:53"); len(q) != 2 || q[0].Name != "google.com" {
		t.Fatalf("queriesFor(8.8.8.8) = %v", q)
	}
	if q := d.queriesFor("9.9.9.9:5353"); len(q) != 1 || q[0].Type != "AAAA" {
		t.Fatalf("queriesFor(9.9.9.9) = %v", q)
	}

	// a server without any queries (own or global) is refused
	bad := testDNSCfg("1.1.1.1", "2.2.2.2")
	bad.ServerQueries = map[string][]DNSQuery{"2.2.2.2": {}}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "2.2.2.2") {
		t.Fatalf("empty per-server list must be refused: %v", err)
	}
	// ...but a server with no list of its own falls back to the global queries
	ok := testDNSCfg("1.1.1.1", "2.2.2.2")
	ok.ServerQueries = map[string][]DNSQuery{"2.2.2.2": {{Name: "x.example", Type: "A"}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(ok.queriesFor("1.1.1.1:53")) != 2 {
		t.Fatal("global queries must apply to servers without their own")
	}
	// an entry for a server that is not in the list is a typo, not silently ignored
	typo := testDNSCfg("1.1.1.1")
	typo.ServerQueries = map[string][]DNSQuery{"3.3.3.3": {{Name: "x.example", Type: "A"}}}
	if err := typo.Validate(); err == nil {
		t.Fatal("server_queries for an unknown server must be refused")
	}
	for _, s := range []string{"not a host!", "a..b", "-x.com", "1.2.3.4:99999"} {
		if _, err := normalizeServer(s); err == nil {
			t.Errorf("normalizeServer(%q) must fail", s)
		}
	}
}

func TestGroupsKeyMeaning(t *testing.T) {
	load := func(js string) *DaemonConfig {
		t.Helper()
		dc := newDaemonConfig()
		if err := json.Unmarshal([]byte(js), dc); err != nil {
			t.Fatal(err)
		}
		return dc
	}
	if n := len(load(`{}`).Groups); n != 1 {
		t.Fatalf("absent groups = default group, got %d", n)
	}
	if n := len(load(`{"groups":null}`).Groups); n != 1 {
		t.Fatalf("null groups = default group, got %d", n)
	}
	dc := load(`{"groups":[]}`)
	if dc.Groups == nil || len(dc.Groups) != 0 {
		t.Fatalf("explicit empty groups must stay empty: %v", dc.Groups)
	}
	if err := dc.Validate(); err != nil {
		t.Fatalf("a config with no gateways is valid: %v", err)
	}
	// and it survives a save/load round trip as [] (not null, not the default)
	path := t.TempDir() + "/c.json"
	if err := dc.save(path); err != nil {
		t.Fatal(err)
	}
	back, err := loadConfig(path)
	if err != nil || len(back.Groups) != 0 {
		t.Fatalf("round trip: %v %v", back, err)
	}
}

func TestDuplicateSharedAddressRefused(t *testing.T) {
	dc := newDaemonConfig()
	g2 := defaultGroup()
	g2.GroupID = 2
	dc.Groups = append(dc.Groups, g2) // same 10.0.0.1/24 as group 1
	err := dc.Validate()
	if err == nil || !strings.Contains(err.Error(), "shared address") {
		t.Fatalf("two gateways on one address must be refused: %v", err)
	}
	dc.Groups[1].VIP4 = "10.0.1.1/24"
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPoolProbesEachServerWithItsOwnQueries(t *testing.T) {
	a, b := newFakeDNS(t), newFakeDNS(t)
	cfg := testDNSCfg(a.addr, b.addr)
	cfg.Queries = nil
	cfg.ServerQueries = map[string][]DNSQuery{
		a.addr: {{Name: "one.example", Type: "A"}},
		b.addr: {{Name: "two.example", Type: "A"}, {Name: "three.example", Type: "AAAA"}, {Name: "four.example", Type: "A"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	if a.hits.Load() != 1 || b.hits.Load() != 3 {
		t.Fatalf("probes sent: a=%d b=%d (want 1 and 3)", a.hits.Load(), b.hits.Load())
	}
	for _, s := range p.Snapshot() {
		want := 1
		if s.Addr == b.addr {
			want = 3
		}
		if len(s.Tests) != want {
			t.Fatalf("%s: %d tests, want %d", s.Addr, len(s.Tests), want)
		}
		for _, ts := range s.Tests {
			if !ts.OK {
				t.Fatalf("%s: %v failed: %s", s.Addr, ts, ts.Error)
			}
		}
	}
	// when the server stops answering, every test shows the failure
	b.mode.Store(3)
	p.ProbeNow(context.Background())
	for _, s := range p.Snapshot() {
		if s.Addr != b.addr {
			continue
		}
		if s.Healthy {
			t.Fatal("b must be down")
		}
		for _, ts := range s.Tests {
			if ts.OK || ts.Error == "" {
				t.Fatalf("test should fail with a reason: %+v", ts)
			}
		}
	}
}

func TestSupervisorPerGroupPools(t *testing.T) {
	a, b, shared := newFakeDNS(t), newFakeDNS(t), newFakeDNS(t)
	mk := func(id int, vip string, own *DNSConfig) GroupConfig {
		g := defaultGroup()
		g.GroupID, g.VIP4, g.DNSProxy, g.DNS = id, vip, true, own
		return g
	}
	da, db := testDNSCfg(a.addr), testDNSCfg(b.addr)
	dc := newDaemonConfig()
	dc.DNS = testDNSCfg(shared.addr)
	dc.Groups = []GroupConfig{mk(1, "10.1.0.1/24", &da), mk(2, "10.2.0.1/24", &db), mk(3, "10.3.0.1/24", nil)}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	s := NewSupervisor(context.Background(), dc)
	changed := s.refreshPool(dc, true)
	t.Cleanup(func() { s.StopAll() })
	if len(changed) != 0 {
		t.Fatalf("nothing had a port before: %v", changed)
	}
	if n := len(s.poolList()); n != 3 {
		t.Fatalf("want 3 pools (two own + shared), got %d", n)
	}
	addrOf := func(gid int) string { return s.poolFor(gid).servers[0].Addr }
	if addrOf(1) != a.addr || addrOf(2) != b.addr || addrOf(3) != shared.addr {
		t.Fatalf("groups use the wrong pools: %s %s %s", addrOf(1), addrOf(2), addrOf(3))
	}
	// an unchanged pool is kept (same object), a changed one replaced, a removed one stopped
	p1, p2 := s.poolFor(1), s.poolFor(2)
	nu := *dc
	db2 := da
	db2.ListenPort = 5300
	nu.Groups = []GroupConfig{mk(1, "10.1.0.1/24", &da), mk(2, "10.2.0.1/24", &db2)}
	ch := s.refreshPool(&nu, true)
	if s.poolFor(1) != p1 {
		t.Fatal("unchanged pool must be kept")
	}
	if s.poolFor(2) == p2 {
		t.Fatal("changed pool must be replaced")
	}
	if !ch[2] || ch[1] {
		t.Fatalf("only group 2 changed its port: %v", ch)
	}
	if s.poolFor(3) != nil || len(s.poolList()) != 2 {
		t.Fatal("removed group's pool must be gone")
	}
	if s.dnsFor(2).ListenPort != 5300 {
		t.Fatal("dnsFor must follow the new settings")
	}
}

func TestSharedConfigCarriesGroupPools(t *testing.T) {
	g := defaultGroup()
	d := testDNSCfg("1.1.1.1")
	d.ServerQueries = map[string][]DNSQuery{"1.1.1.1": {{Name: "a.example", Type: "A"}}}
	g.DNS, g.DNSProxy = &d, true
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	dc.DNS = testDNSCfg("9.9.9.9")
	if err := dc.Validate(); err != nil { // normalises addresses, as every real save does
		t.Fatal(err)
	}
	sh := sharedOf(dc)
	h1 := sh.hash()

	// the replica starts without the group's pool and adopts it
	rep := newDaemonConfig()
	rep.DNS = testDNSCfg("9.9.9.9")
	rg := defaultGroup()
	rg.Interface = "eth7"
	rg.GroupID, rg.VIP4 = 1, g.VIP4
	rep.Groups = []GroupConfig{rg}
	changed, err := mergeShared(rep, sh, seedsOf(dc))
	if err != nil || !changed {
		t.Fatalf("merge: %v %v", changed, err)
	}
	if rep.Groups[0].DNS == nil || len(rep.Groups[0].DNS.ServerQueries) != 1 || !rep.Groups[0].DNSProxy {
		t.Fatalf("the pool must replicate: %+v", rep.Groups[0].DNS)
	}
	if rep.Groups[0].Interface != "eth7" {
		t.Fatal("the interface stays local")
	}
	if sharedOf(rep).hash() != h1 {
		t.Fatal("after the merge both nodes must hash the same")
	}
	// zero gateways replicate too
	empty := SharedConfig{DNS: dc.DNS, Groups: []SharedGroup{}}
	if err := empty.Validate(); err != nil {
		t.Fatalf("an empty shared config is valid: %v", err)
	}
	if _, err := mergeShared(rep, empty, nil); err != nil || len(rep.Groups) != 0 {
		t.Fatalf("empty merge: %v %d", err, len(rep.Groups))
	}
}

func TestApplyCanvasEdit(t *testing.T) {
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{}
	do := func(e canvasEdit) {
		t.Helper()
		if _, err := applyCanvasEdit(dc, e); err != nil {
			t.Fatalf("%+v: %v", e, err)
		}
	}
	fail := func(e canvasEdit, want string) {
		t.Helper()
		if _, err := applyCanvasEdit(dc, e); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%+v: want error containing %q, got %v", e, want, err)
		}
	}
	do(canvasEdit{Action: "add", Kind: "gateway", VIP: "10.0.0.1/24", Interface: "eth0"})
	if err := dc.Validate(); err != nil {
		t.Fatalf("a bare gateway (no DNS yet) is valid: %v", err)
	}
	do(canvasEdit{Action: "add", Kind: "gateway", VIP: "2001:db8::1/64"})
	if len(dc.Groups) != 2 || dc.Groups[0].GroupID != 1 || dc.Groups[1].GroupID != 2 || dc.Groups[1].VIP6 == "" || dc.Groups[1].Interface != "eth0" {
		t.Fatalf("gateways: %+v", dc.Groups)
	}
	fail(canvasEdit{Action: "add", Kind: "gateway", Group: 1, VIP: "10.9.9.9/24"}, "already exists")
	fail(canvasEdit{Action: "add", Kind: "gateway"}, "--vip")
	// a server and domains
	do(canvasEdit{Action: "add", Kind: "server", Group: 1, Server: "8.8.8.8"})
	fail(canvasEdit{Action: "add", Kind: "server", Group: 1, Server: "8.8.8.8:53"}, "already")
	fail(canvasEdit{Action: "add", Kind: "server", Group: 9, Server: "8.8.8.8"}, "no gateway")
	if err := dc.Validate(); err == nil {
		t.Fatal("a server with no domain must not validate yet")
	}
	do(canvasEdit{Action: "add", Kind: "domain", Group: 1, Server: "8.8.8.8", Name: "google.com"})
	do(canvasEdit{Action: "add", Kind: "domain", Group: 1, Server: "8.8.8.8", Name: "microsoft.com", Type: "aaaa"})
	fail(canvasEdit{Action: "add", Kind: "domain", Group: 1, Server: "8.8.8.8", Name: "GOOGLE.com"}, "already")
	fail(canvasEdit{Action: "add", Kind: "domain", Group: 1, Server: "1.1.1.1", Name: "x.com"}, "not on gateway")
	do(canvasEdit{Action: "add", Kind: "server", Group: 1, Server: "8.8.4.4"})
	do(canvasEdit{Action: "add", Kind: "domain", Group: 1, Server: "8.8.4.4", Name: "apple.com"})
	do(canvasEdit{Action: "add", Kind: "server", Group: 2, Server: "9.9.9.9"})
	do(canvasEdit{Action: "add", Kind: "domain", Group: 2, Server: "9.9.9.9", Name: "ford.com"})
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	g1 := dc.Groups[0].DNS
	if len(g1.Servers) != 2 || len(g1.ServerQueries["8.8.8.8:53"]) != 2 || g1.ServerQueries["8.8.8.8:53"][1].Type != "AAAA" {
		t.Fatalf("group 1 pool: %+v", g1)
	}
	// a gateway can be drawn complete in one step
	do(canvasEdit{Action: "add", Kind: "gateway", Group: 7, VIP: "10.7.0.1/24", Server: "4.4.4.4", Name: "example.org", Type: "mx"})
	if g := dc.Groups[2]; g.GroupID != 7 || !g.DNSProxy || g.DNS.ServerQueries["4.4.4.4:53"][0].Type != "MX" {
		t.Fatalf("one-step gateway: %+v", g)
	}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	do(canvasEdit{Action: "del", Kind: "gateway", Group: 7})
	// delete a trapezoid: only that test goes
	do(canvasEdit{Action: "del", Kind: "domain", Group: 1, Server: "8.8.8.8", Name: "google.com"})
	if q := dc.Groups[0].DNS.ServerQueries["8.8.8.8:53"]; len(q) != 1 || q[0].Name != "microsoft.com" {
		t.Fatalf("after domain delete: %v", q)
	}
	fail(canvasEdit{Action: "del", Kind: "domain", Group: 1, Server: "8.8.8.8", Name: "nope.com"}, "not tested")
	// delete a square: its domains go with it, siblings stay
	do(canvasEdit{Action: "del", Kind: "server", Group: 1, Server: "8.8.8.8"})
	g1 = dc.Groups[0].DNS
	if len(g1.Servers) != 1 || g1.Servers[0] != "8.8.4.4:53" || len(g1.ServerQueries) != 1 {
		t.Fatalf("after server delete: %+v", g1)
	}
	// delete a circle: the whole gateway goes, the other one is untouched
	do(canvasEdit{Action: "del", Kind: "gateway", Group: 1})
	if len(dc.Groups) != 1 || dc.Groups[0].GroupID != 2 || len(dc.Groups[0].DNS.Servers) != 1 {
		t.Fatalf("after gateway delete: %+v", dc.Groups)
	}
	do(canvasEdit{Action: "del", Kind: "gateway", Group: 2})
	if len(dc.Groups) != 0 {
		t.Fatal("deleting the last circle leaves no configuration")
	}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	fail(canvasEdit{Action: "del", Kind: "gateway", Group: 2}, "no gateway")
	fail(canvasEdit{Action: "zap", Kind: "gateway"}, "unknown canvas edit")
}

func TestCanvasEditFromSharedPoolCopiesIt(t *testing.T) {
	dc := newDaemonConfig() // group 1 uses the shared dns block
	dc.DNS = testDNSCfg("1.1.1.1")
	dc.Groups[0].DNSProxy = true
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "add", Kind: "server", Group: 1, Server: "2.2.2.2"}); err != nil {
		t.Fatal(err)
	}
	if len(dc.DNS.Servers) != 1 {
		t.Fatalf("the shared pool must not change: %v", dc.DNS.Servers)
	}
	if g := dc.Groups[0].DNS; g == nil || len(g.Servers) != 2 {
		t.Fatalf("the group gets its own copy: %+v", g)
	}
}

func TestBuildCanvasColours(t *testing.T) {
	good, bad := newFakeDNS(t), newFakeDNS(t)
	bad.mode.Store(3)
	d := testDNSCfg(good.addr, bad.addr)
	g := defaultGroup()
	g.DNSProxy, g.DNS = true, &d
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	s := NewSupervisor(context.Background(), dc)
	s.refreshPool(dc, true)
	t.Cleanup(func() { s.StopAll() })

	// not running: grey circle
	cv := buildCanvas(dc, nil, s.poolList())
	if cv[0].Status != "idle" || cv[0].Servers[0].Status == "" {
		t.Fatalf("not running: %+v", cv[0])
	}
	rows := []SnapshotRow{{GroupID: 1, AF: "v4", Local: true, State: "active", DNSUp: true}}
	cv = buildCanvas(dc, rows, s.poolList())
	gw := cv[0]
	if gw.Status != "warn" {
		t.Fatalf("one server down => yellow, got %s (%s)", gw.Status, gw.Detail)
	}
	by := map[string]CanvasServer{}
	for _, sv := range gw.Servers {
		by[sv.Addr] = sv
	}
	if by[good.addr].Status != "ok" || by[bad.addr].Status != "bad" {
		t.Fatalf("server colours: good=%s bad=%s", by[good.addr].Status, by[bad.addr].Status)
	}
	if len(by[good.addr].Tests) != 2 || by[good.addr].Tests[0].Status != "ok" || by[bad.addr].Tests[0].Status != "bad" {
		t.Fatalf("test colours: %+v / %+v", by[good.addr].Tests, by[bad.addr].Tests)
	}
	// every server down => red; healthy but not serving yet => yellow
	// the pool's own probe loop runs beside the test's probes, so a round that started before the change can
	// land after it: probe again until the colour settles instead of trusting the first round
	settle := func(want string) []CanvasGateway {
		var out []CanvasGateway
		for i := 0; i < 40; i++ {
			s.poolFor(1).ProbeNow(context.Background())
			if out = buildCanvas(dc, rows, s.poolList()); out[0].Status == want {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		return out
	}
	good.mode.Store(3)
	if cv = settle("bad"); cv[0].Status != "bad" {
		t.Fatalf("all servers down => red, got %s", cv[0].Status)
	}
	good.mode.Store(0)
	bad.mode.Store(0)
	if cv = settle("ok"); cv[0].Status != "ok" {
		t.Fatalf("all fine => green, got %s (%s)", cv[0].Status, cv[0].Detail)
	}
	standby := []SnapshotRow{{GroupID: 1, AF: "v4", Local: true, State: "listen"}}
	if cv = buildCanvas(dc, standby, s.poolList()); cv[0].Status != "warn" {
		t.Fatalf("not serving => yellow, got %s", cv[0].Status)
	}
}

func TestWebCanvasEndpoint(t *testing.T) {
	e := newWebEnv(t)
	e.login("alice", "pw")
	r := e.do("GET", "/api/canvas", nil, withAuth(e, false))
	if r.code != 200 {
		t.Fatalf("canvas: %d %s", r.code, r.raw)
	}
	if _, ok := r.body["data"].([]any); !ok {
		t.Fatalf("data must be a list: %s", r.raw)
	}
	if r2 := e.do("GET", "/api/canvas", nil); r2.code != 401 {
		t.Fatalf("canvas needs a login: %d", r2.code)
	}
}

func TestHistoryDescribesCanvasChanges(t *testing.T) {
	mk := func(mut func(*GroupConfig)) *DaemonConfig {
		dc := newDaemonConfig()
		dc.Groups[0].DNSProxy = true
		d := testDNSCfg("1.1.1.1:53")
		d.Queries = nil
		d.ServerQueries = map[string][]DNSQuery{"1.1.1.1:53": {{Name: "a.example", Type: "A"}}}
		dc.Groups[0].DNS = &d
		if mut != nil {
			mut(&dc.Groups[0])
		}
		return dc
	}
	before := mk(nil)
	after := mk(func(g *GroupConfig) {
		g.DNS.Servers = append(g.DNS.Servers, "2.2.2.2:53")
		g.DNS.ServerQueries["2.2.2.2:53"] = []DNSQuery{{Name: "b.example", Type: "A"}}
		g.DNS.ServerQueries["1.1.1.1:53"] = nil
	})
	var detail string
	for _, s := range configSections(before, after) {
		detail += s.Label + ": " + s.Detail + "\n"
	}
	for _, want := range []string{"servers added 2.2.2.2:53", "domains added 2.2.2.2:53 b.example/A", "domains removed 1.1.1.1:53 a.example/A"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("history should say %q, got:\n%s", want, detail)
		}
	}
}

func TestCanvasDualStackAndECSEdits(t *testing.T) {
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{}
	do := func(e canvasEdit) {
		t.Helper()
		if _, err := applyCanvasEdit(dc, e); err != nil {
			t.Fatalf("%+v: %v", e, err)
		}
	}
	do(canvasEdit{Action: "add", Kind: "gateway", VIP: "10.0.0.1/24", VIP6: "2001:db8::1/64", ECS: "on", ECSv4: 20,
		Server: "2001:db8::53", Name: "example.org"})
	g := dc.Groups[0]
	if g.VIP4 != "10.0.0.1/24" || g.VIP6 != "2001:db8::1/64" {
		t.Fatalf("both addresses: %q %q", g.VIP4, g.VIP6)
	}
	if !g.DNS.ECS || g.DNS.ECSPrefix4 != 20 || g.DNS.ECSPrefix6 != 56 {
		t.Fatalf("ecs settings: %+v", g.DNS)
	}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	if g.DNS.Servers[0] != "[2001:db8::53]:53" {
		t.Fatalf("an IPv6 server is stored bracketed with its port: %q", g.DNS.Servers[0])
	}
	// an IPv6 address in --vip goes to the IPv6 slot
	do(canvasEdit{Action: "add", Kind: "gateway", VIP: "2001:db8:2::1/64"})
	if g2 := dc.Groups[1]; g2.VIP6 == "" || g2.VIP4 != "" {
		t.Fatalf("v6-only gateway: %+v", g2)
	}
	// set: change address, interface and ECS; ECS off keeps the prefixes
	do(canvasEdit{Action: "set", Kind: "gateway", Group: 1, VIP: "10.0.9.1/24", Interface: "eth5", ECS: "off", ECSv6: 48})
	g = dc.Groups[0]
	if g.VIP4 != "10.0.9.1/24" || g.VIP6 != "2001:db8::1/64" || g.Interface != "eth5" || g.DNS.ECS || g.DNS.ECSPrefix6 != 48 {
		t.Fatalf("after set: %+v %+v", g, g.DNS)
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: 1}); err == nil {
		t.Fatal("set without anything to change must say so")
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: 1, ECS: "maybe"}); err == nil {
		t.Fatal("--ecs must be on or off")
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: 9, ECS: "on"}); err == nil {
		t.Fatal("unknown gateway")
	}
	// setting ECS on a gateway that used the shared pool copies the pool first
	dc2 := newDaemonConfig()
	dc2.DNS = testDNSCfg("1.1.1.1")
	dc2.DNS.ECS = false // it is on by default since v107; this test is about a shared pool that has it off
	dc2.Groups[0].DNSProxy = true
	if _, err := applyCanvasEdit(dc2, canvasEdit{Action: "set", Kind: "gateway", Group: 1, ECS: "on"}); err != nil {
		t.Fatal(err)
	}
	if dc2.DNS.ECS || dc2.Groups[0].DNS == nil || !dc2.Groups[0].DNS.ECS || len(dc2.Groups[0].DNS.Servers) != 1 {
		t.Fatalf("shared pool must stay as it was; group gets a copy with ECS on: %+v", dc2.Groups[0].DNS)
	}
}

func TestBuildCanvasPerFamilyStatus(t *testing.T) {
	good := newFakeDNS(t)
	d := testDNSCfg(good.addr)
	g := defaultGroup()
	g.VIP6 = "2001:db8::1/64"
	g.DNSProxy, g.DNS = true, &d
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	s := NewSupervisor(context.Background(), dc)
	s.refreshPool(dc, true)
	t.Cleanup(func() { s.StopAll() })

	v4ok := SnapshotRow{GroupID: 1, AF: "v4", Local: true, State: "active", DNSUp: true}
	v6ok := SnapshotRow{GroupID: 1, AF: "v6", Local: true, State: "active", DNSUp: true}
	v6wait := SnapshotRow{GroupID: 1, AF: "v6", Local: true, State: "listen"}

	both := buildCanvas(dc, []SnapshotRow{v4ok, v6ok}, s.poolList())[0]
	if both.Status != "ok" || len(both.Families) != 2 {
		t.Fatalf("both families fine: %s %+v", both.Status, both.Families)
	}
	mixed := buildCanvas(dc, []SnapshotRow{v4ok, v6wait}, s.poolList())[0]
	if mixed.Status != "warn" || mixed.Families[0].Status != "ok" || mixed.Families[1].Status != "warn" {
		t.Fatalf("IPv6 not serving must not hide behind a healthy IPv4: %s %+v", mixed.Status, mixed.Families)
	}
	if !strings.Contains(mixed.Detail, "IPv6") || !strings.Contains(mixed.Detail, "IPv4") {
		t.Fatalf("the detail must name both families: %q", mixed.Detail)
	}
	missing := buildCanvas(dc, []SnapshotRow{v4ok}, s.poolList())[0]
	if missing.Status != "warn" || missing.Families[1].Status != "idle" {
		t.Fatalf("an IPv6 engine that is missing is a warning: %s %+v", missing.Status, missing.Families)
	}
	none := buildCanvas(dc, nil, s.poolList())[0]
	if none.Status != "idle" {
		t.Fatalf("nothing running: %s", none.Status)
	}
}

func TestCanvasMembers(t *testing.T) {
	rows := []SnapshotRow{
		{GroupID: 1, AF: "v4", PeerIP: "10.0.0.5", Local: true, AfnID: 1, Weight: 100, AGCIP: "10.0.0.5"},
		{GroupID: 1, AF: "v6", PeerIP: "10.0.0.5", Local: true, AfnID: 1, Weight: 100, AGCIP: "10.0.0.5"},
		{GroupID: 1, AF: "v4", PeerIP: "10.0.0.6", AfnID: 2, State: "active", AGCIP: "10.0.0.5"},
		{GroupID: 1, AF: "v4", PeerIP: "10.0.0.7", AfnID: 3, State: "expired"}, // gone quiet
		{GroupID: 1, AF: "v4", PeerIP: "10.0.0.8", AfnID: 0, State: "active"},  // no slot, not serving
		{GroupID: 2, AF: "v4", PeerIP: "10.0.0.9", AfnID: 1, State: "active"},  // another gateway
	}
	m := membersOf(rows, 1)
	if len(m) != 2 || m[0].IP != "10.0.0.5" || !m[0].Local || !m[0].AGC || m[0].AFs != "v4+v6" || m[1].IP != "10.0.0.6" || m[1].AGC {
		t.Fatalf("members: %+v", m)
	}
	if got := membersOf(nil, 1); got == nil || len(got) != 0 {
		t.Fatalf("empty must be a non-nil list for JSON: %#v", got)
	}
}

func TestPauseServerAndGateway(t *testing.T) {
	a, b := newFakeDNS(t), newFakeDNS(t)
	d := testDNSCfg(a.addr, b.addr)
	g := defaultGroup()
	g.DNSProxy, g.DNS = true, &d
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}

	// pausing a server takes it out of the live pool but keeps it in the config
	if msg, err := applyCanvasEdit(dc, canvasEdit{Action: "pause", Kind: "server", Group: 1, Server: b.addr}); err != nil || !strings.Contains(msg, "paused") {
		t.Fatalf("pause server: %q %v", msg, err)
	}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	own := dc.Groups[0].DNS
	if len(own.Servers) != 2 || !own.isPaused(b.addr) || own.isPaused(a.addr) {
		t.Fatalf("config after pause: %+v", own)
	}
	if lv := own.live(); len(lv.Servers) != 1 || lv.Servers[0] != a.addr || lv.PausedServers != nil {
		t.Fatalf("live pool config: %+v", lv)
	}
	s := NewSupervisor(context.Background(), dc)
	s.refreshPool(dc, true)
	t.Cleanup(func() { s.StopAll() })
	if n := len(s.poolFor(1).servers); n != 1 {
		t.Fatalf("a paused server must not be in the running pool, pool has %d", n)
	}
	rows := []SnapshotRow{{GroupID: 1, AF: "v4", Local: true, State: "active", DNSUp: true}}
	cv := buildCanvas(dc, rows, s.poolList())[0]
	by := map[string]CanvasServer{}
	for _, sv := range cv.Servers {
		by[sv.Addr] = sv
	}
	if by[b.addr].Status != "paused" || by[b.addr].Tests[0].Status != "paused" || by[a.addr].Status != "ok" {
		t.Fatalf("paused colours: %+v", cv.Servers)
	}
	if cv.Status != "ok" {
		t.Fatalf("a paused server is not a fault, got %s (%s)", cv.Status, cv.Detail)
	}
	// every server paused: clients cannot be answered
	do := func(e canvasEdit) {
		t.Helper()
		if _, err := applyCanvasEdit(dc, e); err != nil {
			t.Fatal(err)
		}
	}
	do(canvasEdit{Action: "pause", Kind: "server", Group: 1, Server: a.addr})
	_ = dc.Validate()
	s.refreshPool(dc, true)
	if cv = buildCanvas(dc, rows, s.poolList())[0]; cv.Status != "bad" || !strings.Contains(cv.Detail, "paused") {
		t.Fatalf("all paused => red: %s (%s)", cv.Status, cv.Detail)
	}
	// resume; deleting a paused server forgets the pause
	do(canvasEdit{Action: "resume", Kind: "server", Group: 1, Server: a.addr})
	do(canvasEdit{Action: "del", Kind: "server", Group: 1, Server: b.addr})
	_ = dc.Validate()
	if p := dc.Groups[0].DNS.PausedServers; len(p) != 0 {
		t.Fatalf("pause must go with the server: %v", p)
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "pause", Kind: "server", Group: 1, Server: "10.9.9.9"}); err == nil {
		t.Fatal("pausing a server that is not there must fail")
	}

	// gateway: paused is local, shows as paused, and starts no engines
	do(canvasEdit{Action: "pause", Kind: "gateway", Group: 1})
	if !dc.Groups[0].Paused {
		t.Fatal("not paused")
	}
	if cv = buildCanvas(dc, nil, s.poolList())[0]; cv.Status != "paused" || !cv.Paused || cv.Families[0].Status != "paused" {
		t.Fatalf("paused gateway: %+v", cv)
	}
	if sh := sharedOf(dc); strings.Contains(string(mustJSON(t, sh)), "paused\":true") {
		t.Fatal("the gateway pause is per node and must not be replicated")
	}
	sup := NewSupervisor(context.Background(), dc)
	sup.StartAll()
	if n := len(sup.engineList()); n != 0 {
		t.Fatalf("a paused gateway must start no engines, got %d", n)
	}
	if k := sup.poolFor(1); k != nil {
		t.Fatal("a paused gateway needs no probe pool")
	}
	sup.StopAll()
	do(canvasEdit{Action: "resume", Kind: "gateway", Group: 1})
	if dc.Groups[0].Paused {
		t.Fatal("still paused")
	}
	// history reads in words
	old, nu := *dc, *dc
	old.Groups = []GroupConfig{g}
	nu.Groups = []GroupConfig{g}
	nu.Groups[0].Paused = true
	if txt := string(mustJSON(t, configSections(&old, &nu))); !strings.Contains(txt, "paused on this node") {
		t.Fatalf("history wording: %s", txt)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A gateway always serves DNS; one with no servers yet is amber, not green: it
// is up but cannot answer clients.
func TestBuildCanvasGatewayWithoutServers(t *testing.T) {
	d := testDNSCfg()
	g := defaultGroup()
	g.DNS = &d
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	s := NewSupervisor(context.Background(), dc)
	s.refreshPool(dc, true)
	t.Cleanup(func() { s.StopAll() })
	rows := []SnapshotRow{{GroupID: 1, AF: "v4", Local: true, State: "active", DNSUp: true}}
	cv := buildCanvas(dc, rows, s.poolList())
	if cv[0].Status != "warn" || !strings.Contains(cv[0].Detail, "no DNS server") {
		t.Fatalf("no servers: %s (%s)", cv[0].Status, cv[0].Detail)
	}
}

// Dragging in the drawing (and --canvas-move) reorders servers, a server's domains and the anycast addresses.
func TestCanvasMove(t *testing.T) {
	dc := newDaemonConfig()
	dc.DNS = testDNSCfg("1.1.1.1", "2.2.2.2", "3.3.3.3")
	dc.Groups[0].DNSProxy = true
	dc.Groups[0].ExtraVIPs = []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"}
	must := func(e canvasEdit) {
		t.Helper()
		e.Group = 1
		if _, err := applyCanvasEdit(dc, e); err != nil {
			t.Fatalf("%+v: %v", e, err)
		}
	}
	must(canvasEdit{Action: "move", Kind: "server", Server: "3.3.3.3", Pos: 1})
	if got := dc.Groups[0].DNS.Servers; got[0] != "3.3.3.3" || got[1] != "1.1.1.1" || got[2] != "2.2.2.2" {
		t.Fatalf("servers after move: %v", got)
	}
	if len(dc.DNS.Servers) != 3 || dc.DNS.Servers[0] != "1.1.1.1" {
		t.Fatalf("the shared pool must not change: %v", dc.DNS.Servers)
	}
	must(canvasEdit{Action: "move", Kind: "server", Server: "3.3.3.3", Pos: 99}) // past the end: last
	if got := dc.Groups[0].DNS.Servers; got[2] != "3.3.3.3" {
		t.Fatalf("clamped to the end: %v", got)
	}
	must(canvasEdit{Action: "add", Kind: "domain", Server: "1.1.1.1", Name: "b.example"})
	must(canvasEdit{Action: "add", Kind: "domain", Server: "1.1.1.1", Name: "c.example"})
	must(canvasEdit{Action: "move", Kind: "domain", Server: "1.1.1.1", Name: "c.example", Pos: 1})
	if q := dc.Groups[0].DNS.ServerQueries["1.1.1.1:53"]; len(q) < 3 || q[0].Name != "c.example" {
		t.Fatalf("domains after move: %v", q)
	}
	must(canvasEdit{Action: "move", Kind: "anycast", Address: "203.0.113.3", Pos: 1})
	if got := dc.Groups[0].ExtraVIPs; got[0] != "203.0.113.3" || got[1] != "203.0.113.1" || got[2] != "203.0.113.2" {
		t.Fatalf("anycast after move: %v", got)
	}
	for _, e := range []canvasEdit{
		{Action: "move", Kind: "server", Group: 1, Server: "9.9.9.9", Pos: 1},
		{Action: "move", Kind: "server", Group: 1, Server: "1.1.1.1"},
		{Action: "move", Kind: "domain", Group: 1, Server: "1.1.1.1", Name: "nope.example", Pos: 1},
		{Action: "move", Kind: "anycast", Group: 1, Address: "203.0.113.99", Pos: 1},
	} {
		if _, err := applyCanvasEdit(dc, e); err == nil {
			t.Fatalf("%+v must be refused", e)
		}
	}
	if err := dc.Validate(); err != nil {
		t.Fatalf("a moved configuration must stay valid: %v", err)
	}
}
