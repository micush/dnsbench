package main

import "testing"

// A node that joins is left out of every gateway until an administrator adds it.
func TestNewNodeIsNotAddedToTheGateways(t *testing.T) {
	a, b := twoNodeCluster(t)
	id := b.mg.cl.node.Snapshot().NodeID
	dc, _, err := a.mg.LiveConfig()
	if err != nil || len(dc.Groups) == 0 {
		t.Fatalf("no gateways to check: %v", err)
	}
	for _, g := range dc.Groups {
		if !containsStr(g.ExcludedNodes, id) {
			t.Fatalf("gateway %d: the node that joined is not left out: %v", g.GroupID, g.ExcludedNodes)
		}
	}
	// the joiner follows the shared list
	db, _, err := b.mg.LiveConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range db.Groups {
		if !containsStr(g.ExcludedNodes, id) {
			t.Fatalf("on the joiner, gateway %d does not carry the exclusion: %v", g.GroupID, g.ExcludedNodes)
		}
	}
	// a node that is already a member and talks to the primary again is not touched
	n0 := len(dc.Groups[0].ExcludedNodes)
	b.sync()
	a.sync()
	dc2, _, _ := a.mg.LiveConfig()
	if len(dc2.Groups[0].ExcludedNodes) != n0 {
		t.Fatalf("the list changed on a later sync: %v", dc2.Groups[0].ExcludedNodes)
	}
}
