package main

import "testing"

func gws(states ...string) []CanvasNode {
	var out []CanvasNode
	for _, s := range states {
		out = append(out, CanvasNode{Gw: s, Excluded: s == "removed"})
	}
	return out
}

// The sidebar's dot is the gateway for the cluster as a whole, whichever node is asked.
func TestClusterGatewayStatus(t *testing.T) {
	for _, c := range []struct {
		name   string
		nodes  []CanvasNode
		status string
	}{
		{"two serve, two are on another subnet", gws("ok", "ok", "notserving", "notserving"), "ok"},
		{"one serves, the rest down", gws("ok", "down", "paused"), "ok"},
		{"served only degraded", gws("degraded", "notserving"), "warn"},
		{"a degraded one does not hide a good one", gws("degraded", "ok"), "ok"},
		{"nothing serves", gws("notserving", "notserving"), "bad"},
		{"serving but broken is not serving", gws("bad", "notserving"), "bad"},
		{"every node has it paused", gws("paused", "paused"), "paused"},
		{"paused and not serving", gws("paused", "notserving"), "bad"},
		{"all starting", gws("starting", "starting"), "idle"},
		{"one starting, one broken", gws("starting", "bad"), "bad"},
		{"removed nodes do not count", gws("ok", "removed", "removed"), "ok"},
		{"only the removed", gws("removed"), "idle"},
		{"an old node that reports nothing counts as serving", gws(""), "ok"},
	} {
		if got, why := clusterGateway(c.nodes); got != c.status || why == "" {
			t.Errorf("%s: %q (%q), want %q", c.name, got, why, c.status)
		}
	}
	if _, d := clusterGateway(gws("ok", "ok", "notserving")); d != "Served by 2 of 3 nodes" {
		t.Errorf("detail %q", d)
	}
}

// What a node reports decides its state for the gateway.
func TestGwStateOf(t *testing.T) {
	for _, c := range []struct {
		gs     *GwState
		paused bool
		want   string
	}{
		{&GwState{Serving: true}, false, "ok"},
		{&GwState{Serving: true, Health: "warn"}, false, "degraded"},
		{&GwState{Serving: true, Health: "bad"}, false, "bad"},
		{&GwState{Serving: false, Health: "idle"}, false, "notserving"},
		{&GwState{Serving: false, HealthWhy: "starting — waiting for the DNS servers"}, false, "starting"},
		{nil, false, "paused"},
		{&GwState{Serving: true}, true, "paused"},
	} {
		if got := gwStateOf(c.gs, c.paused); got != c.want {
			t.Errorf("%+v paused=%v: %q, want %q", c.gs, c.paused, got, c.want)
		}
	}
}

// On two real nodes, one serving and one not (another subnet), the cluster reads served whichever node is asked, though
// the gateway's own colour differs between them.
func TestClusterGatewayIsTheSameFromEveryNode(t *testing.T) {
	old := hostLoadFn
	t.Cleanup(func() { hostLoadFn = old })
	hostLoadFn = func() (HostLoad, bool) { return HostLoad{CPU: 1, Mem: 1, Disk: 1}, true }
	a, b := twoNodeCluster(t)
	a.mg.gwFn = func() []GwState { return []GwState{{GroupID: 1, Serving: true, Health: "ok"}} }
	b.mg.gwFn = func() []GwState {
		return []GwState{{GroupID: 1, Serving: false, Health: "idle", HealthWhy: "not running here — this node has no address in the gateway's subnet"}}
	}
	a.sync()
	b.sync()
	fromA := a.mg.cl.canvasNodes(1, "ok", "running and answering", false)
	fromB := b.mg.cl.canvasNodes(1, "idle", "not running here", false)
	for name, ns := range map[string][]CanvasNode{"A": fromA, "B": fromB} {
		if len(ns) != 2 {
			t.Fatalf("from %s: %d nodes", name, len(ns))
		}
		st, why := clusterGateway(ns)
		if st != "ok" || why != "Served by 1 of 2 nodes" {
			t.Errorf("asked %s: %q %q, want ok, served by 1 of 2 nodes", name, st, why)
		}
	}
	if fromA[0].Gw != "ok" || fromA[1].Gw != "notserving" || fromB[0].Gw != "notserving" || fromB[1].Gw != "ok" {
		t.Errorf("states from A %q %q, from B %q %q", fromA[0].Gw, fromA[1].Gw, fromB[0].Gw, fromB[1].Gw)
	}
}
