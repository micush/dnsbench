package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// withLocalNode pretends to be the node with this ID for one test.
func withLocalNode(t *testing.T, id string) {
	t.Helper()
	old := localNodeID()
	setLocalNodeID(id)
	t.Cleanup(func() { setLocalNodeID(old) })
}

func nodeEdit(dc *DaemonConfig, action, id string, members ...string) (string, error) {
	return applyCanvasEdit(dc, canvasEdit{Action: action, Kind: "node", Group: 1, Node: id, nodeID: id, members: strings.Join(members, ",")})
}

// A node removed from a gateway serves nothing of it, the others and the gateway's other nodes carry on.
func TestExcludedNodeDoesNotServeTheGateway(t *testing.T) {
	dc := newDaemonConfig()
	g2 := defaultGroup()
	g2.GroupID, g2.VIP4 = 2, "10.0.1.1/24"
	dc.Groups = append(dc.Groups, g2)
	dc.Groups[0].ExcludedNodes = []string{"aaaa1111"}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	withLocalNode(t, "aaaa1111")
	e := dc.effective()
	if !e.Groups[0].Paused || !e.Groups[0].ExcludedHere {
		t.Fatalf("the removed node still serves gateway 1: %+v", e.Groups[0])
	}
	if e.Groups[1].Paused || e.Groups[1].ExcludedHere {
		t.Fatal("removing it from gateway 1 stopped gateway 2 too")
	}
	if dc.Groups[0].Paused || dc.Groups[0].ExcludedHere {
		t.Fatal("effective() changed the saved configuration")
	}
	withLocalNode(t, "bbbb2222")
	if e := dc.effective(); e.Groups[0].Paused || e.Groups[0].ExcludedHere {
		t.Fatal("a node that was not removed stopped serving")
	}
	// the node that joins later serves it (nothing lists it)
	withLocalNode(t, "cccc3333")
	if e := dc.effective(); e.Groups[0].Paused {
		t.Fatal("a new node does not serve the gateway")
	}
	// never written to the file: ExcludedHere has no key
	e = dc.effective()
	withLocalNode(t, "aaaa1111")
	e = dc.effective()
	b, _ := json.Marshal(e.Groups[0])
	if strings.Contains(string(b), "ExcludedHere") || strings.Contains(string(b), "excluded_here") {
		t.Fatalf("the runtime flag is written: %s", b)
	}
}

// The list is shared, sorted, de-duplicated, checked, and absent from the file (and the hash) when empty.
func TestExcludedNodesAreSharedAndTidy(t *testing.T) {
	dc := newDaemonConfig()
	b0, _ := json.Marshal(dc.Groups[0])
	if strings.Contains(string(b0), "excluded_nodes") {
		t.Fatalf("an empty list is written: %s", b0)
	}
	h0 := sharedOf(dc).hash()
	dc.Groups[0].ExcludedNodes = []string{"bb", "aa", "bb"}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(dc.Groups[0].ExcludedNodes, ","); got != "aa,bb" {
		t.Fatalf("the list is %q, want sorted and without duplicates", got)
	}
	if sharedOf(dc).hash() == h0 {
		t.Fatal("removing a node does not change what the cluster shares")
	}
	other := newDaemonConfig()
	changed, err := mergeShared(other, sharedOf(dc), nil)
	if err != nil || !changed || strings.Join(other.Groups[0].ExcludedNodes, ",") != "aa,bb" {
		t.Fatalf("a replica did not take the list: %v %v %v", changed, err, other.Groups[0].ExcludedNodes)
	}
	// bringing everyone back empties it and the key goes away
	dc.Groups[0].ExcludedNodes = nil
	if sharedOf(dc).hash() != h0 {
		t.Fatal("an empty list changes the hash")
	}
	for _, bad := range []string{"", "not hex!", strings.Repeat("a", 65)} {
		dc.Groups[0].ExcludedNodes = []string{bad}
		if err := dc.Validate(); err == nil {
			t.Errorf("node ID %q accepted", bad)
		}
	}
}

func TestCanvasEditAddsAndRemovesNodes(t *testing.T) {
	dc := pauseTestConfig()
	if _, err := nodeEdit(dc, "del", "aa", "aa", "bb"); err != nil || len(dc.Groups[0].ExcludedNodes) != 1 {
		t.Fatalf("remove: %v %v", err, dc.Groups[0].ExcludedNodes)
	}
	if msg, _ := nodeEdit(dc, "del", "aa", "aa", "bb"); !strings.Contains(msg, "already removed") || len(dc.Groups[0].ExcludedNodes) != 1 {
		t.Fatalf("removing twice: %q", msg)
	}
	if _, err := nodeEdit(dc, "del", "bb", "aa", "bb"); err == nil || !strings.Contains(err.Error(), "no node serving") {
		t.Fatalf("the last serving node was removed: %v", err)
	}
	if _, err := nodeEdit(dc, "add", "aa", "aa", "bb"); err != nil || len(dc.Groups[0].ExcludedNodes) != 0 {
		t.Fatalf("add back: %v %v", err, dc.Groups[0].ExcludedNodes)
	}
	if msg, _ := nodeEdit(dc, "add", "aa", "aa", "bb"); !strings.Contains(msg, "already serving") {
		t.Fatalf("adding twice: %q", msg)
	}
	// a node that already left the cluster does not count as one that could serve
	dc.Groups[0].ExcludedNodes = []string{"dead"}
	if _, err := nodeEdit(dc, "del", "aa", "aa", "dead"); err == nil {
		t.Fatal("removed the only node left in the cluster")
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "del", Kind: "node", Group: 9, nodeID: "aa"}); err == nil {
		t.Fatal("a gateway that does not exist")
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "del", Kind: "node", Group: 1}); err == nil {
		t.Fatal("no node named")
	}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPickNodeByAddressHostOrID(t *testing.T) {
	peers := []PeerView{
		{Addr: "10.0.0.1:53854", NodeID: "aa", Hostname: "lsitedns01.example.net"},
		{Addr: "10.0.0.2:53854", NodeID: "bb", Hostname: "lsitedns02"},
		{Addr: "10.0.1.2:53854", NodeID: "cc", Hostname: "lsitedns02.other.net"},
		{Addr: "10.0.2.1:53854", NodeID: ""},
	}
	for name, want := range map[string]string{"10.0.0.1:53854": "aa", "10.0.0.1": "aa", "lsitedns01": "aa", "LSITEDNS01.example.net": "aa", "bb": "bb", "10.0.1.2": "cc"} {
		if id, err := pickNode(peers, nil, name); err != nil || id != want {
			t.Errorf("%q: %q %v, want %q", name, id, err, want)
		}
	}
	if _, err := pickNode(peers, nil, "lsitedns02"); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Errorf("an ambiguous host name: %v", err)
	}
	if _, err := pickNode(peers, nil, "nobody"); err == nil {
		t.Error("an unknown node")
	}
	if _, err := pickNode(peers, nil, "10.0.2.1"); err == nil || !strings.Contains(err.Error(), "node ID") {
		t.Errorf("a node with no ID yet: %v", err)
	}
	if id, err := pickNode(peers, []string{"stale99"}, "stale99"); err != nil || id != "stale99" {
		t.Errorf("an entry of a node that left cannot be cleared: %q %v", id, err)
	}
	if _, err := pickNode(peers, nil, " "); err == nil {
		t.Error("an empty name")
	}
}

// Whichever node you ask, a removed node reads "removed", and the others do not.
func TestRemovedNodeIsShownOnEveryNode(t *testing.T) {
	old := hostLoadFn // a full disk on the machine running the test turns a shape yellow whatever it is doing
	t.Cleanup(func() { hostLoadFn = old })
	hostLoadFn = func() (HostLoad, bool) { return HostLoad{CPU: 1, Mem: 1, Disk: 1}, true }
	a, b := twoNodeCluster(t)
	bid := b.mg.cl.node.Self().NodeID
	for _, from := range []*tnode{a, b} {
		ns := from.mg.cl.canvasNodesEx(1, "ok", "IPv4: running", false, []string{bid})
		if len(ns) != 2 {
			t.Fatalf("%d nodes", len(ns))
		}
		for _, n := range ns {
			removed := n.NodeID == bid
			if n.NodeID == "" || n.Excluded != removed {
				t.Errorf("from %s: %s (%q) excluded=%v", from.name, n.Name, n.NodeID, n.Excluded)
			}
			if removed && (n.Label != "removed" || n.Status != "paused") {
				t.Errorf("from %s: %s reads %q %q", from.name, n.Name, n.Label, n.Status)
			}
			if !removed && n.Label == "removed" {
				t.Errorf("from %s: %s reads removed", from.name, n.Name)
			}
		}
	}
}

// The whole path on two real nodes: removed from A by name, B's file has it, B stops serving that gateway and A does not.
func TestRemoveNodeThroughTheClusterReachesTheNode(t *testing.T) {
	a, b := twoNodeCluster(t)
	if _, err := a.mg.CanvasEdit(canvasEdit{Action: "del", Kind: "node", Group: 1, Node: b.addr}, "test"); err != nil {
		t.Fatal(err)
	}
	b.sync()
	bid := b.mg.cl.node.Self().NodeID
	if got := b.cfg().Groups[0].ExcludedNodes; len(got) != 1 || got[0] != bid {
		t.Fatalf("B's file lists %v, want [%s]", got, bid)
	}
	withLocalNode(t, bid)
	if e := b.cfg().effective(); !e.Groups[0].Paused || !e.Groups[0].ExcludedHere {
		t.Fatal("B still serves the gateway")
	}
	withLocalNode(t, a.mg.cl.node.Self().NodeID)
	if e := a.cfg().effective(); e.Groups[0].Paused {
		t.Fatal("A stopped serving too")
	}
	// the last node cannot be removed, and a node can be added back from the other side
	if _, err := b.mg.CanvasEdit(canvasEdit{Action: "del", Kind: "node", Group: 1, Node: a.addr}, "test"); err == nil {
		t.Fatal("removed the last serving node")
	}
	if _, err := b.mg.CanvasEdit(canvasEdit{Action: "add", Kind: "node", Group: 1, Node: b.addr}, "test"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	if got := a.cfg().Groups[0].ExcludedNodes; len(got) != 0 {
		t.Fatalf("A still lists %v", got)
	}
}

// The gateway's own circle on the removed node says it was removed, not paused.
func TestCanvasSaysThisNodeWasRemoved(t *testing.T) {
	dc := pauseTestConfig()
	dc.Groups[0].ExcludedNodes = []string{"aaaa1111"}
	withLocalNode(t, "aaaa1111")
	cv := buildCanvas(dc.effective(), nil, nil)
	if len(cv) != 1 || !cv[0].Excluded || cv[0].Status != "paused" || !strings.Contains(cv[0].Detail, "removed from the gateway") || cv[0].PausedScope != "" {
		t.Fatalf("%+v", cv[0])
	}
	if len(cv[0].ExcludedNodes) != 1 {
		t.Fatalf("the list is not in the view: %v", cv[0].ExcludedNodes)
	}
}
