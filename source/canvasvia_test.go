package main

import (
	"encoding/json"
	"testing"
)

func viaNodes(self string, others ...CanvasNode) []CanvasNode {
	return append([]CanvasNode{{Name: "me", Addr: "10.0.0.1:1", Self: true, Gw: self, Reachable: true}}, others...)
}

func peer(name, gw string) CanvasNode {
	return CanvasNode{Name: name, Addr: "10.0.0." + name + ":1", Gw: gw, Reachable: true}
}

// Only a node that does not serve the gateway takes the picture of one that does, best serving node first.
func TestServingPeersAreAskedOnlyWhenThisNodeDoesNotServe(t *testing.T) {
	others := []CanvasNode{peer("5", "degraded"), peer("4", "ok"), peer("3", "notserving"), peer("2", "ok")}
	for _, self := range []string{"ok", "degraded", "bad", "starting", ""} {
		if l := servingPeers(CanvasGateway{Nodes: viaNodes(self, others...)}); len(l) != 0 {
			t.Errorf("this node is %q: %d nodes asked", self, len(l))
		}
	}
	for _, self := range []string{"notserving", "paused", "removed"} {
		l := servingPeers(CanvasGateway{Nodes: viaNodes(self, others...)})
		if len(l) != 3 || l[0].Name != "2" || l[1].Name != "4" || l[2].Name != "5" {
			t.Errorf("this node is %q: %+v", self, l)
		}
	}
	gone := peer("6", "ok")
	gone.Reachable = false
	removed := peer("7", "ok")
	removed.Excluded = true
	if l := servingPeers(CanvasGateway{Nodes: viaNodes("notserving", gone, removed, peer("8", "paused"), peer("9", "down"))}); len(l) != 0 {
		t.Errorf("nodes that cannot say were asked: %+v", l)
	}
}

func TestTakeFromKeepsThisNodesOwnEntry(t *testing.T) {
	mine := CanvasGateway{GroupID: 1, Name: "x", Status: "idle", Detail: "not running here", Nodes: viaNodes("notserving"), Servers: []CanvasServer{}, Paused: true, NodePaused: true}
	theirs := CanvasGateway{GroupID: 1, Status: "ok", Detail: "running and answering", Servers: []CanvasServer{{Addr: "192.0.2.1:53", Status: "ok"}}, Families: []CanvasFamily{{AF: "v4", Status: "ok"}}}
	mine.takeFrom(theirs, peer("2", "ok"))
	if mine.Status != "ok" || mine.Detail != "running and answering" || len(mine.Servers) != 1 || len(mine.Families) != 1 {
		t.Fatalf("not taken: %+v", mine)
	}
	if mine.Via != "2" || mine.ViaAddr != "10.0.0.2:1" || len(mine.Nodes) != 1 || mine.Name != "x" || !mine.Paused {
		t.Fatalf("this node's own part changed: %+v", mine)
	}
}

// A gateway this node does not run is added from a serving node's answer, nobody starred, in group order.
func TestTakeGatewayRowsAddsTheOtherGroupsWithoutStars(t *testing.T) {
	mine := []GatewayGroup{{GroupID: 2, AF: "v4", Members: []GatewayMember{{IP: "10.2.0.1", Local: true}}}}
	theirs := []GatewayGroup{
		{GroupID: 1, AF: "v6", Members: []GatewayMember{{IP: "fd00::1", Local: true}}},
		{GroupID: 1, AF: "v4", Members: []GatewayMember{{IP: "10.1.0.1", Local: true}, {IP: "10.1.0.2"}}},
		{GroupID: 2, AF: "v4", Members: []GatewayMember{{IP: "10.2.0.9"}}},
	}
	got := takeGatewayRows(mine, theirs, 1)
	if len(got) != 3 || got[0].GroupID != 1 || got[0].AF != "v4" || got[1].AF != "v6" || got[2].GroupID != 2 {
		t.Fatalf("wrong rows or order: %+v", got)
	}
	for _, g := range got[:2] {
		for _, m := range g.Members {
			if m.Local {
				t.Errorf("group %d: %s starred", g.GroupID, m.IP)
			}
		}
	}
	if !got[2].Members[0].Local || got[2].Members[0].IP != "10.2.0.1" {
		t.Errorf("this node's own group changed: %+v", got[2])
	}
	if !theirs[0].Members[0].Local {
		t.Errorf("the answer was modified in place")
	}
}

func TestAddDNSPoolsTakesTheOtherGatewaysPool(t *testing.T) {
	resp := map[string]any{"ok": true, "data": map[string]any{
		"pools":     []map[string]any{{"key": 2, "groups": []int{2}}},
		"listeners": []string{"group 2 10.2.0.1:53"},
		"servers":   "x",
	}}
	var from dnsAnswer
	if err := json.Unmarshal([]byte(`{"data":{"pools":[{"key":1,"groups":[1],"servers":[]},{"key":2,"groups":[2]}],"listeners":["group 1 10.1.0.1:53","group 2 10.2.0.9:53"]}}`), &from); err != nil {
		t.Fatal(err)
	}
	if addDNSPools(resp, from, 3) {
		t.Fatal("a group nobody has was found")
	}
	if !addDNSPools(resp, from, 1) {
		t.Fatal("group 1 not found")
	}
	d := resp["data"].(map[string]any)
	pools := d["pools"].([]map[string]any)
	if len(pools) != 2 || pools[0]["key"].(float64) != 1 || pools[1]["key"].(int) != 2 {
		t.Fatalf("wrong pools or order: %+v", pools)
	}
	if ls := d["listeners"].([]string); len(ls) != 2 || ls[1] != "group 1 10.1.0.1:53" {
		t.Fatalf("listeners: %v", ls)
	}
	if !addDNSPools(resp, from, 2) || len(d["pools"].([]map[string]any)) != 2 {
		t.Fatal("a pool this node has was added again")
	}
	// a node with no pool at all gets a whole answer
	none := map[string]any{"ok": false, "error": "no gateway"}
	if !addDNSPools(none, from, 1) || none["ok"] != true || none["error"] != nil || len(none["data"].(map[string]any)["pools"].([]map[string]any)) != 1 {
		t.Fatalf("empty node: %+v", none)
	}
}
