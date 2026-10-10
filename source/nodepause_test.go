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
