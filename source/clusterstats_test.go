package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func qpart(name string, r *QStatsResult) clusterPart[QStatsResult] {
	return clusterPart[QStatsResult]{Name: name, Addr: name + ":1", R: r}
}
func infosOf(names ...string) []ClusterNodeInfo {
	var out []ClusterNodeInfo
	for _, n := range names {
		out = append(out, ClusterNodeInfo{Name: n, Addr: n + ":1", OK: true})
	}
	return out
}

func TestMergeQStatsAddsSeriesSumsAndLists(t *testing.T) {
	a := &QStatsResult{Start: 600, Step: 60, From: 600, To: 780, Since: 100,
		Total: []uint64{1, 2, 3}, NoError: []uint64{1, 2, 3}, ServFail: []uint64{0, 0, 0}, NXDomain: []uint64{0, 0, 0}, Refused: []uint64{0, 0, 0}, Other: []uint64{0, 0, 0}, Updates: []uint64{0, 1, 0},
		Sums:  QStatsSums{Total: 6, NoError: 6, Updates: 1, CacheHit: 4, CacheMis: 1, Clients: 3},
		Guard: MemGuardInfo{Limit: 90, Trims: 1, Last: 50, LastNote: "a"},
		Types: []NameCount{{Name: "A", Count: 5}, {Name: "AAAA", Count: 1}}, Protos: []NameCount{{Name: "UDP", Count: 6}},
		Clients: []NameCount{{Name: "10.0.0.1", Count: 4, Host: "pc1."}, {Name: "10.0.0.2", Count: 2}, {Name: qsOthers, Count: 0}},
		Domains: []NameCount{{Name: "x.example", Count: 6}}}
	b := &QStatsResult{Start: 660, Step: 60, From: 660, To: 900, Since: 200,
		Total: []uint64{10, 20, 30, 40}, NoError: []uint64{9, 20, 30, 40}, ServFail: []uint64{1, 0, 0, 0}, NXDomain: []uint64{0, 0, 0, 0}, Refused: []uint64{0, 0, 0, 0}, Other: []uint64{0, 0, 0, 0}, Updates: []uint64{0, 0, 0, 0},
		Sums:  QStatsSums{Total: 100, NoError: 99, ServFail: 1, CacheHit: 10, CacheMis: 10, Clients: 5},
		Guard: MemGuardInfo{Limit: 80, Trims: 2, Last: 70, LastNote: "b"},
		Types: []NameCount{{Name: "A", Count: 70}, {Name: "TXT", Count: 30}}, Protos: []NameCount{{Name: "TCP", Count: 20}, {Name: "UDP", Count: 80}},
		Clients: []NameCount{{Name: "10.0.0.2", Count: 50, Hosts: []string{"pc2.", "pc2b."}}, {Name: "10.0.0.3", Count: 50}, {Name: qsOthers, Count: 3}},
		Domains: []NameCount{{Name: "y.example", Count: 60}, {Name: "x.example", Count: 40}}}
	r := mergeQStats([]clusterPart[QStatsResult]{qpart("A", a), qpart("B", b)}, infosOf("A", "B"))

	if r.Start != 600 || r.Step != 60 || len(r.Total) != 5 {
		t.Fatalf("time axis: start %d step %d points %d", r.Start, r.Step, len(r.Total))
	}
	if want := []uint64{1, 12, 23, 30, 40}; !reflect.DeepEqual(r.Total, want) {
		t.Fatalf("total series %v, want %v (aligned by time, not by index)", r.Total, want)
	}
	if want := []uint64{1, 11, 23, 30, 40}; !reflect.DeepEqual(r.NoError, want) {
		t.Fatalf("no-error series %v, want %v", r.NoError, want)
	}
	if r.Updates[1] != 1 {
		t.Fatalf("updates %v", r.Updates)
	}
	s := r.Sums
	if s.Total != 106 || s.NoError != 105 || s.ServFail != 1 || s.Updates != 1 || s.CacheHit != 14 || s.CacheMis != 11 {
		t.Fatalf("sums %+v", s)
	}
	if r.Since != 200 || r.From != 600 || r.To != 900 {
		t.Fatalf("since/from/to: %d %d %d (the cluster has been counting since the last node began)", r.Since, r.From, r.To)
	}
	if r.Guard.Trims != 3 || r.Guard.Limit != 90 || r.Guard.LastNote != "b" {
		t.Fatalf("guard %+v", r.Guard)
	}
	// lists: by name, biggest first, "(others)" last, reverse-DNS names kept
	if len(r.Types) != 3 || r.Types[0].Name != "A" || r.Types[0].Count != 75 || r.Types[1].Name != "TXT" {
		t.Fatalf("types %+v", r.Types)
	}
	if len(r.Protos) != 2 || r.Protos[0].Name != "UDP" || r.Protos[0].Count != 86 || r.Protos[1].Name != "TCP" || r.Protos[1].Count != 20 {
		t.Fatalf("protocols %+v", r.Protos)
	}
	c := r.Clients
	if len(c) != 4 || c[0].Name != "10.0.0.2" || c[0].Count != 52 || len(c[0].Hosts) != 2 || c[1].Name != "10.0.0.3" || c[2].Name != "10.0.0.1" || c[2].Host != "pc1." || c[3].Name != qsOthers || c[3].Count != 3 {
		t.Fatalf("clients %+v", c)
	}
	if d := r.Domains; len(d) != 2 || d[0].Name != "y.example" || d[1].Name != "x.example" || d[1].Count != 46 {
		t.Fatalf("domains %+v", d)
	}
	// distinct clients cannot be added: at least the busiest node's and the ones listed (10.0.0.1, .2, .3)
	if s.Clients != 5 {
		t.Fatalf("distinct clients %d, want the larger of 5 and 3", s.Clients)
	}
	if r.Cluster == nil || len(r.Cluster.Nodes) != 2 {
		t.Fatalf("cluster info %+v", r.Cluster)
	}
}

func TestMergeQStatsKeepsOnlyTheTopOfALongList(t *testing.T) {
	mk := func(prefix string) *QStatsResult {
		r := &QStatsResult{Start: 0, Step: 60, Total: []uint64{0}, NoError: []uint64{0}, ServFail: []uint64{0}, NXDomain: []uint64{0}, Refused: []uint64{0}, Other: []uint64{0}, Updates: []uint64{0}}
		for i := 0; i < 90; i++ {
			r.Domains = append(r.Domains, NameCount{Name: fmt.Sprintf("%s%03d.example", prefix, i), Count: uint64(1000 - i)})
		}
		return r
	}
	r := mergeQStats([]clusterPart[QStatsResult]{qpart("A", mk("a")), qpart("B", mk("b"))}, infosOf("A", "B"))
	if len(r.Domains) != qsKeepResult {
		t.Fatalf("%d domains kept, want %d", len(r.Domains), qsKeepResult)
	}
	if r.Domains[0].Count != 1000 || r.Domains[len(r.Domains)-1].Count < 1000-60 {
		t.Fatalf("the wrong ones were kept: %+v ... %+v", r.Domains[0], r.Domains[len(r.Domains)-1])
	}
}

func TestMergeQStatsLeavesOutAnotherResolution(t *testing.T) {
	mk := func(step int, v uint64) *QStatsResult {
		return &QStatsResult{Start: 0, Step: step, Total: []uint64{v}, NoError: []uint64{v}, ServFail: []uint64{0}, NXDomain: []uint64{0}, Refused: []uint64{0}, Other: []uint64{0}, Updates: []uint64{0}, Sums: QStatsSums{Total: v}}
	}
	infos := infosOf("A", "B", "C")
	r := mergeQStats([]clusterPart[QStatsResult]{qpart("A", mk(60, 1)), qpart("B", mk(60, 2)), qpart("C", mk(600, 100))}, infos)
	if r.Sums.Total != 3 || r.Step != 60 {
		t.Fatalf("total %d step %d: the odd one out must not be added", r.Sums.Total, r.Step)
	}
	if infos[2].OK || !strings.Contains(infos[2].Error, "resolution") || !infos[0].OK {
		t.Fatalf("infos %+v", infos)
	}
}

func hpart(name string, cores int, memTotal uint64, mk func(h *HostResult)) clusterPart[HostResult] {
	h := &HostResult{Start: 0, Step: 60, Since: 1,
		CPU: []float64{-1, -1}, CPUMax: []float64{-1, -1}, Mem: []float64{-1, -1}, IO: []float64{-1, -1}, IOMax: []float64{-1, -1},
		Rx: []float64{-1, -1}, RxMax: []float64{-1, -1}, Tx: []float64{-1, -1}, TxMax: []float64{-1, -1}, FS: []HostFSSeries{}}
	h.Now = HostNow{At: 1000, Cores: cores, MemTotal: memTotal, NetUtil: -1}
	mk(h)
	return clusterPart[HostResult]{Name: name, Addr: name + ":1", R: h}
}

func TestMergeHostAddsWhatAddsAndWeightsWhatDoesNot(t *testing.T) {
	a := hpart("A", 4, 8<<30, func(h *HostResult) {
		h.CPU, h.CPUMax = []float64{50, 40}, []float64{80, 60}
		h.Mem = []float64{50, 50}
		h.IO, h.IOMax = []float64{10, -1}, []float64{20, -1}
		h.Rx, h.RxMax, h.Tx, h.TxMax = []float64{1000, 2000}, []float64{3000, 4000}, []float64{100, 200}, []float64{300, 400}
		h.FS = []HostFSSeries{{Mount: "/", Pct: []float64{50, 50}}}
		h.Now.CPU, h.Now.Load = 50, [3]float64{1, 2, 3}
		h.Now.MemUsed, h.Now.MemPct = 4<<30, 50
		h.Now.SwapTotal, h.Now.SwapUsed = 1<<30, 1<<28
		h.Now.IO, h.Now.Rx, h.Now.Tx, h.Now.NetUtil = 10, 1000, 100, 5
		h.Now.FS = []fsUsage{{Mount: "/", Device: "/dev/sda1", Type: "ext4", Total: 100, Used: 50, Pct: 50}}
		h.Now.Ifaces = []IfaceNow{{Name: "eth0", Up: true, Rx: 1000, Tx: 100, UtilPct: 5}}
	})
	b := hpart("B", 12, 24<<30, func(h *HostResult) {
		h.CPU, h.CPUMax = []float64{10, -1}, []float64{30, -1}
		h.Mem = []float64{10, 10}
		h.IO, h.IOMax = []float64{30, 40}, []float64{50, 60}
		h.Rx, h.RxMax, h.Tx, h.TxMax = []float64{500, 600}, []float64{700, 800}, []float64{50, 60}, []float64{70, 80}
		h.FS = []HostFSSeries{{Mount: "/", Pct: []float64{10, 10}}, {Mount: "/var", Pct: []float64{70, 70}}}
		h.Now.CPU, h.Now.Load = 10, [3]float64{4, 5, 6}
		h.Now.MemUsed, h.Now.MemPct = 2<<30, 10
		h.Now.IO, h.Now.Rx, h.Now.Tx, h.Now.NetUtil = 30, 500, 50, 40
		h.Now.FS = []fsUsage{{Mount: "/", Device: "/dev/vda1", Type: "ext4", Total: 300, Used: 30, Pct: 10}, {Mount: "/var", Device: "/dev/vdb", Type: "xfs", Total: 100, Used: 70, Pct: 70}}
		h.Now.Ifaces = []IfaceNow{{Name: "eth0", Up: true, Rx: 500, Tx: 50, UtilPct: 40}}
	})
	r := mergeHost([]clusterPart[HostResult]{a, b}, infosOf("A", "B"))
	near := func(got, want float64, what string) {
		t.Helper()
		if d := got - want; d > 0.01 || d < -0.01 {
			t.Errorf("%s = %v, want %v", what, got, want)
		}
	}
	near(r.CPU[0], (50*4+10*12)/16.0, "cpu (weighted by cores)")
	near(r.CPU[1], 40, "cpu where only one node has a point")
	near(r.CPUMax[0], 80, "cpu peak (the busiest node)")
	near(r.Mem[0], (50*8+10*24)/32.0, "memory (weighted by size)")
	near(r.IO[0], 20, "disk busy (mean)")
	near(r.IO[1], 40, "disk busy where one node has a point")
	near(r.IOMax[0], 50, "disk busy peak")
	near(r.Rx[0], 1500, "in (sum)")
	near(r.Tx[1], 260, "out (sum)")
	near(r.RxMax[0], 3700, "in peak (sum of peaks)")
	if len(r.FS) != 2 || r.FS[0].Mount != "/" || r.FS[1].Mount != "/var" {
		t.Fatalf("filesystems %+v", r.FS)
	}
	near(r.FS[0].Pct[0], (50*100+10*300)/400.0, "/ use (weighted by size)")
	near(r.FS[1].Pct[0], 70, "/var use (one node)")
	n := r.Now
	if n.Cores != 16 || n.Load != [3]float64{5, 7, 9} || n.MemTotal != 32<<30 || n.MemUsed != 6<<30 || n.SwapTotal != 1<<30 || n.Rx != 1500 || n.Tx != 150 {
		t.Fatalf("now: %+v", n)
	}
	near(n.CPU, 20, "now cpu")
	near(n.MemPct, 100*6.0/32.0, "now memory")
	near(n.IO, 20, "now disk busy")
	near(n.NetUtil, 40, "busiest link")
	if len(n.FS) != 2 || n.FS[0].Total != 400 || n.FS[0].Used != 80 || n.FS[0].Device != "(several)" || n.FS[1].Device != "/dev/vdb" {
		t.Fatalf("filesystems now: %+v", n.FS)
	}
	near(n.FS[0].Pct, 20, "/ now")
	if len(n.Ifaces) != 2 || n.Ifaces[0].Name != "A eth0" || n.Ifaces[1].Name != "B eth0" {
		t.Fatalf("interfaces %+v", n.Ifaces)
	}
	if r.Cluster == nil || len(r.Cluster.Nodes) != 2 {
		t.Fatalf("cluster info %+v", r.Cluster)
	}
}

// A node that has no sample yet is not counted in the tiles (its cores, memory and rates would be zero anyway).
func TestMergeHostIgnoresANodeWithoutASample(t *testing.T) {
	a := hpart("A", 4, 8<<30, func(h *HostResult) { h.Now.CPU = 50; h.Now.Rx = 7 })
	b := hpart("B", 4, 8<<30, func(h *HostResult) { h.Now = HostNow{} })
	r := mergeHost([]clusterPart[HostResult]{a, b}, infosOf("A", "B"))
	if r.Now.Cores != 4 || r.Now.CPU != 50 || r.Now.Rx != 7 {
		t.Fatalf("now: %+v", r.Now)
	}
}

// ── through two real cluster nodes ───────────────────────────────────────────

func withWeb(t *testing.T, n *tnode) {
	t.Helper()
	sup := NewSupervisor(context.Background(), newDaemonConfig())
	st := NewStatusServer("", sup)
	st.mg = n.mg
	ws := NewWebServer(n.mg, st, &fakeAuth{})
	n.mg.webH = ws.Handler()
}

// Both nodes answer: the cluster's numbers are the two added (both nodes of this test share one process, so each answers
// with the same numbers, and the sum is twice any one's).
func TestClusterQStatsAndHostThroughTheRelay(t *testing.T) {
	a, b := twoNodeCluster(t)
	withWeb(t, a)
	withWeb(t, b)
	for i := 0; i < 40; i++ {
		qstats.Record(netip.MustParseAddr("192.0.2.77"), "relay.example", 1, false, 0)
	}
	hoststats.Sample()
	args, _ := json.Marshal(map[string]string{"from": "1h"})

	// statistics
	one, err := a.mg.Op("qstats.get", args, "tester")
	if err != nil {
		t.Fatal(err)
	}
	both, err := a.mg.Op("qstats.cluster", args, "tester")
	if err != nil {
		t.Fatal(err)
	}
	o, c := one.(*QStatsResult), both.(*QStatsResult)
	if o.Sums.Total == 0 {
		t.Fatal("nothing was recorded")
	}
	if c.Sums.Total != 2*o.Sums.Total {
		t.Fatalf("cluster total %d, one node %d", c.Sums.Total, o.Sums.Total)
	}
	if c.Cluster == nil || len(c.Cluster.Nodes) != 2 || !c.Cluster.Nodes[0].OK || !c.Cluster.Nodes[1].OK {
		t.Fatalf("cluster info %+v", c.Cluster)
	}

	// host load
	oh, err := a.mg.Op("host.get", args, "tester")
	if err != nil {
		t.Fatal(err)
	}
	bh, err := a.mg.Op("host.cluster", args, "tester")
	if err != nil {
		t.Fatal(err)
	}
	h1, h2 := oh.(*HostResult), bh.(*HostResult)
	if h1.Now.Cores == 0 || h2.Now.Cores != 2*h1.Now.Cores || h2.Now.MemTotal != 2*h1.Now.MemTotal {
		t.Fatalf("cores %d vs %d, memory %d vs %d", h2.Now.Cores, h1.Now.Cores, h2.Now.MemTotal, h1.Now.MemTotal)
	}

	// a node that cannot be reached is named, and the rest are still added
	b.mg.webH = nil // it answers 503 to the relay
	both, err = a.mg.Op("qstats.cluster", args, "tester")
	if err != nil {
		t.Fatal(err)
	}
	c = both.(*QStatsResult)
	if c.Sums.Total != o.Sums.Total || c.Cluster.Nodes[1].OK || c.Cluster.Nodes[1].Error == "" {
		t.Fatalf("with one node down: total %d, info %+v", c.Sums.Total, c.Cluster)
	}
}

// Not clustered: just this node.
func TestClusterQStatsWithoutACluster(t *testing.T) {
	e := newWebEnv(t)
	args, _ := json.Marshal(map[string]string{"from": "1h"})
	got, err := e.mg.Op("qstats.cluster", args, "tester")
	if err != nil {
		t.Fatal(err)
	}
	r := got.(*QStatsResult)
	if r.Cluster == nil || len(r.Cluster.Nodes) != 1 || !r.Cluster.Nodes[0].OK {
		t.Fatalf("cluster info %+v", r.Cluster)
	}
}

// The cluster endpoints ask the other nodes themselves, so they must not be relayable (a relayed one would ask again).
func TestClusterStatsEndpointsAreNotRelayable(t *testing.T) {
	for _, p := range []string{"/api/clusterstats?from=1h", "/api/clusterhost?from=1h"} {
		if err := proxyAllowed("GET", p); err == nil {
			t.Errorf("%s can be relayed", p)
		}
	}
	for _, p := range []string{"/api/qstats?from=1h", "/api/host?from=1h"} {
		if err := proxyAllowed("GET", p); err != nil {
			t.Errorf("%s cannot be relayed: %v", p, err)
		}
	}
}

// Two nodes with one host name are told apart by address, as in the Node menu.
func TestClusterNodesWithOneHostNameAreNamedByAddress(t *testing.T) {
	a, b := twoNodeCluster(t)
	withWeb(t, a)
	withWeb(t, b)
	// both nodes of this test run in one process, so they have one host name
	args, _ := json.Marshal(map[string]string{"from": "1h"})
	got, err := a.mg.Op("qstats.cluster", args, "tester")
	if err != nil {
		t.Fatal(err)
	}
	nodes := got.(*QStatsResult).Cluster.Nodes
	if len(nodes) != 2 || nodes[0].Name == nodes[1].Name {
		t.Fatalf("nodes %+v", nodes)
	}
}
