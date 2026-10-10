package main

import "testing"

func TestNeighborsAreShared(t *testing.T) {
	plain := newDaemonConfig()
	h0 := sharedOf(plain).hash()
	dc := newDaemonConfig()
	dc.Groups[0].Neighbors = []string{"10.0.0.11", "10.0.0.12", "10.0.0.13"}
	if sharedOf(dc).hash() == h0 {
		t.Fatal("neighbors are not part of the shared document")
	}
	rep := newDaemonConfig()
	if changed, err := mergeShared(rep, sharedOf(dc), nil); err != nil || !changed {
		t.Fatalf("merge: %v %v", changed, err)
	}
	if got := rep.Groups[0].Neighbors; len(got) != 3 || got[2] != "10.0.0.13" {
		t.Fatalf("replica neighbors: %v", got)
	}
	e := &Engine{myIP: "10.0.0.12"}
	if !e.isSelf("10.0.0.12") || e.isSelf("10.0.0.11") || e.isSelf("junk") {
		t.Fatal("isSelf wrong")
	}
}
