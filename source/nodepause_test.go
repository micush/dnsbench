package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNodePauseIsEffectiveButKeepsEachGatewaysOwnFlag(t *testing.T) {
	dc := newDaemonConfig()
	g1, g2 := defaultGroup(), defaultGroup()
	g2.GroupID, g2.Paused = 2, true
	dc.Groups = []GroupConfig{g1, g2}

	if e := dc.effective(); e.Groups[0].Paused || !e.Groups[1].Paused {
		t.Fatal("a running node must use each gateway's own flag as it is")
	}
	dc.NodePaused = true
	eff := dc.effective()
	if eff == dc || !eff.Groups[0].Paused || !eff.Groups[1].Paused {
		t.Fatalf("every gateway must be paused while the node is: %+v", eff.Groups)
	}
	if dc.Groups[0].Paused {
		t.Fatal("the file's own gateway flag must not be changed by the node pause")
	}
	dc.NodePaused = false
	if dc.effective().Groups[0].Paused || !dc.effective().Groups[1].Paused {
		t.Fatal("after resuming, each gateway is back to its own flag")
	}
}

func TestNodePauseFlagIsLocalAndOptional(t *testing.T) {
	dc := newDaemonConfig()
	if b, _ := json.Marshal(dc); strings.Contains(string(b), "node_paused") {
		t.Fatalf("a config that never paused the node must have no node_paused key (older versions reject unknown keys): %s", b)
	}
	dc.NodePaused = true
	b, _ := json.Marshal(dc)
	var back DaemonConfig
	if err := json.Unmarshal(b, &back); err != nil || !back.NodePaused {
		t.Fatalf("round trip: %v %+v", err, back.NodePaused)
	}
	if sb, _ := json.Marshal(sharedOf(dc)); strings.Contains(string(sb), "paused") {
		t.Fatalf("the node pause is per node and must not be replicated: %s", sb)
	}
}

func TestNodePauseStartsNothingAndShowsOnTheCanvas(t *testing.T) {
	a := newFakeDNS(t)
	d := testDNSCfg(a.addr)
	g := defaultGroup()
	g.DNSProxy, g.DNS = true, &d
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	dc.NodePaused = true

	sup := NewSupervisor(context.Background(), dc)
	sup.StartAll()
	if n := len(sup.engineList()); n != 0 {
		t.Fatalf("a paused node must start no engines, got %d", n)
	}
	if sup.poolFor(1) != nil {
		t.Fatal("a paused node needs no probe pool")
	}
	cv := buildCanvas(sup.config(), nil, sup.poolList())[0]
	if cv.Status != "paused" || !cv.Paused || !cv.NodePaused || !strings.Contains(cv.Detail, "This node is paused") {
		t.Fatalf("canvas: %+v", cv)
	}
	// a reload that keeps the node paused changes nothing
	nu := *dc
	sup.Reload(&nu)
	if n := len(sup.engineList()); n != 0 {
		t.Fatalf("still paused after a reload, got %d engines", n)
	}
	sup.StopAll()
}

func TestNodePauseThroughMgmt(t *testing.T) {
	mg := newPowerMgmt(t)
	if err := newDaemonConfig().save(mg.confPath); err != nil {
		t.Fatal(err)
	}
	if st, err := mg.NodePauseStatus(); err != nil || st.Paused {
		t.Fatalf("fresh node: %+v %v", st, err)
	}
	// the only node in service cannot be paused: nothing would answer
	mg.othersFn = func() int { return 0 }
	if _, err := mg.NodePause(true, "test"); err == nil || !strings.Contains(err.Error(), "only node in service") {
		t.Fatalf("pausing the only node: %v", err)
	}
	if st, _ := mg.NodePauseStatus(); st.Paused {
		t.Fatal("the refused pause was saved")
	}
	// other nodes exist, but this one is the last to serve a gateway: refused, and not saved
	mg.othersFn = func() int { return 2 }
	mg.lastFn = func() string { return "this is the only node serving gateway 1" }
	if _, err := mg.NodePause(true, "test"); err == nil || !strings.Contains(err.Error(), "only node serving gateway 1") {
		t.Fatalf("pausing the last node of a gateway: %v", err)
	}
	if st, _ := mg.NodePauseStatus(); st.Paused {
		t.Fatal("the refused pause was saved")
	}
	mg.lastFn = nil
	mg.othersFn = func() int { return 1 } // another node carries on
	if msg, err := mg.NodePause(true, "test"); err != nil || !strings.Contains(msg, "paused") {
		t.Fatalf("pause: %q %v", msg, err)
	}
	if st, _ := mg.NodePauseStatus(); !st.Paused {
		t.Fatal("not paused in the file")
	}
	if msg, _ := mg.NodePause(true, "test"); !strings.Contains(msg, "already paused") {
		t.Fatalf("second pause: %q", msg)
	}
	if _, err := mg.Op("node.pause", json.RawMessage(`{"paused":false}`), "test"); err != nil {
		t.Fatal(err)
	}
	if st, _ := mg.NodePauseStatus(); st.Paused {
		t.Fatal("still paused after resume")
	}
}

func TestNodePauseHistoryReadsInWords(t *testing.T) {
	a, b := newDaemonConfig(), newDaemonConfig()
	b.NodePaused = true
	var found bool
	for _, s := range configSections(a, b) {
		if s.Label == "Node" && strings.Contains(s.Detail, "paused") {
			found = true
		}
	}
	if !found {
		t.Fatalf("history diff: %+v", configSections(a, b))
	}
}

func TestLastServing(t *testing.T) {
	mine := []GwState{{GroupID: 1, Serving: true}, {GroupID: 2, Serving: true}}
	peer := func(down bool, serving ...int) peerGateways {
		p := peerGateways{Addr: "10.0.0.2", Down: down, Known: true, Serving: map[int]bool{}}
		for _, g := range serving {
			p.Serving[g] = true
		}
		return p
	}
	if why := lastServing(mine, []peerGateways{peer(false, 1, 2)}); why != "" {
		t.Fatalf("both covered: %q", why)
	}
	// another member exists but serves only gateway 1: this node is the last for gateway 2
	if why := lastServing(mine, []peerGateways{peer(false, 1)}); !strings.Contains(why, "gateway 2") {
		t.Fatalf("last for gateway 2: %q", why)
	}
	// an unreachable member is no cover
	if why := lastServing(mine, []peerGateways{peer(true)}); why == "" {
		t.Fatal("a down member covered a gateway")
	}
	// a gateway this node is not serving is nothing to lose
	if why := lastServing([]GwState{{GroupID: 3}}, []peerGateways{peer(false)}); why != "" {
		t.Fatalf("not serving: %q", why)
	}
}
