package main

import "testing"

func TestGatewayNodesSkipsTheRemovedOnes(t *testing.T) {
	g := &GroupConfig{ExcludedNodes: []string{"n2"}}
	keep := gatewayNodes(g)
	for id, want := range map[string]bool{"n1": true, "n2": false, "n3": true, "": true} {
		if got := keep(PeerView{NodeID: id}); got != want {
			t.Errorf("node %q: %v, want %v", id, got, want)
		}
	}
}
