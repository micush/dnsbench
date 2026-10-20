package main

import "testing"

// Peer addresses are tried before peer names, so cluster traffic does not depend on DNS.
func TestAddrsForTriesIPsBeforeNames(t *testing.T) {
	c := &Cluster{goodAddr: map[string]string{}}
	p := ClusterPeer{Addr: "ns2:53854", Alts: []string{"ns2.example:53854", "192.168.5.55:53854", "[fdf5:168:5::55]:53854"}}
	want := []string{"192.168.5.55:53854", "[fdf5:168:5::55]:53854", "ns2:53854", "ns2.example:53854"}
	got := c.addrsFor(p)
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	c.noteGoodAddr(p, "[fdf5:168:5::55]:53854")
	if got = c.addrsFor(p); got[0] != "[fdf5:168:5::55]:53854" || got[2] != "ns2:53854" {
		t.Fatalf("the last good address should lead its group: %v", got)
	}
	c.noteGoodAddr(p, "ns2:53854")
	if got = c.addrsFor(p); got[0] != "192.168.5.55:53854" || got[2] != "ns2:53854" {
		t.Fatalf("a good name must not jump ahead of addresses: %v", got)
	}
}
