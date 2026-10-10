package main

import (
	"context"
	"testing"
	"time"
)

// A gateway that serves DNS must not join the election and take traffic until
// its DNS servers answer: starting at once sent clients to a node that could
// only SERVFAIL until its next probe round.
func TestGatewayWaitsForDNSBeforeServing(t *testing.T) {
	oldE, oldF, oldM := warmEvery, warmFloor, warmMax
	warmEvery, warmFloor, warmMax = 20*time.Millisecond, 10*time.Second, 10*time.Second
	defer func() { warmEvery, warmFloor, warmMax = oldE, oldF, oldM }()

	up := newFakeDNS(t)
	up.mode.Store(3) // SERVFAIL: not answering yet
	g := defaultGroup()
	g.Interface = "ddgwnone0" // no such interface: the engine starts and idles, nothing on the host changes
	d := testDNSCfg(up.addr)
	g.DNSProxy, g.DNS = true, &d
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	s := NewSupervisor(context.Background(), dc)
	t.Cleanup(s.StopAll)
	s.refreshPool(dc, true)
	s.mu.Lock()
	s.startGroupWhenReadyLocked(g)
	s.mu.Unlock()
	time.Sleep(300 * time.Millisecond)
	if n := len(s.engineList()); n != 0 {
		t.Fatalf("started %d engine(s) while no DNS server answered", n)
	}
	if !s.warmingGroups()[1] {
		t.Fatal("not reported as warming up")
	}
	groups := buildCanvas(dc, nil, s.poolList())
	markWarming(groups, s.warmingGroups())
	if groups[0].Status != "warn" || groups[0].Families[0].Status != "warn" {
		t.Fatalf("canvas while warming: %s %s", groups[0].Status, groups[0].Detail)
	}
	up.mode.Store(0) // the server starts answering
	deadline := time.Now().Add(5 * time.Second)
	for len(s.engineList()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(s.engineList()) == 0 {
		t.Fatal("never started after the server began answering")
	}
	if s.warmingGroups()[1] {
		t.Fatal("still reported as warming up")
	}
}

// Pausing (or removing) the gateway while it waits cancels the start; a gateway
// with nothing to wait for starts at once; and after the floor one answering
// server is enough while another stays down.
func TestWarmupCancelAndFloor(t *testing.T) {
	oldE, oldF, oldM := warmEvery, warmFloor, warmMax
	warmEvery, warmFloor, warmMax = 20*time.Millisecond, 200*time.Millisecond, 10*time.Second
	defer func() { warmEvery, warmFloor, warmMax = oldE, oldF, oldM }()

	good, bad := newFakeDNS(t), newFakeDNS(t)
	bad.mode.Store(2)
	g := defaultGroup()
	g.Interface = "ddgwnone0"
	d := testDNSCfg(good.addr, bad.addr)
	g.DNSProxy, g.DNS = true, &d
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	s := NewSupervisor(context.Background(), dc)
	t.Cleanup(s.StopAll)
	s.refreshPool(dc, true)

	// one server up, one down: waits for the floor, then starts with the one
	s.mu.Lock()
	s.startGroupWhenReadyLocked(g)
	s.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	if n := len(s.engineList()); n != 0 {
		t.Fatalf("started before the floor with a server still down (%d)", n)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(s.engineList()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(s.engineList()) == 0 {
		t.Fatal("did not start once one server answered past the floor")
	}
	s.mu.Lock()
	for _, e := range s.stopGroupLocked(1) {
		e.Stop()
	}
	s.mu.Unlock()

	// cancelled by stopping the group (pause/remove)
	good.mode.Store(2)
	s.mu.Lock()
	s.startGroupWhenReadyLocked(g)
	s.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	s.mu.Lock()
	s.stopGroupLocked(1)
	s.mu.Unlock()
	good.mode.Store(0)
	time.Sleep(300 * time.Millisecond)
	if n := len(s.engineList()); n != 0 || s.warmingGroups()[1] {
		t.Fatalf("a cancelled warm-up still started the group (%d engines)", n)
	}

	// nothing to wait for (no DNS servers configured yet): starts at once
	g2 := defaultGroup()
	g2.Interface = "ddgwnone0"
	d2 := testDNSCfg()
	g2.DNS = &d2
	dc2 := newDaemonConfig()
	dc2.Groups = []GroupConfig{g2}
	if err := dc2.Validate(); err != nil {
		t.Fatal(err)
	}
	s2 := NewSupervisor(context.Background(), dc2)
	t.Cleanup(s2.StopAll)
	s2.refreshPool(dc2, true)
	s2.mu.Lock()
	s2.startGroupWhenReadyLocked(g2)
	s2.mu.Unlock()
	if len(s2.engineList()) == 0 {
		t.Fatal("a gateway with no DNS servers has nothing to wait for and must start at once")
	}
}
