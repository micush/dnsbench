package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The first node to take a new version is the canary: the others hold while it
// has run the version for less than updateSoak or shows a bad gateway, and while
// an update to that version has failed on any member.
func TestCanaryHold(t *testing.T) {
	a, b := newTNode(t, "A"), newTNode(t, "B")
	b.join(a)
	for i := 0; i < 2; i++ {
		a.sync()
		b.sync()
	}
	next := fmt.Sprintf("%d", mustInt(version())+1)
	cl := a.mg.cl
	peer := b.addr
	set := func(m PeerStatusMsg) {
		cl.mu.Lock()
		pi := cl.info[peer]
		pi.Reachable, pi.Msg = true, m
		cl.mu.Unlock()
	}
	old := updateSoak
	updateSoak = 2 * time.Minute
	defer func() { updateSoak = old }()

	set(PeerStatusMsg{Version: version()})
	if w := cl.canaryHold(next); w != "" {
		t.Fatalf("nobody runs %s yet; got hold %q", next, w)
	}
	set(PeerStatusMsg{Version: next, UptimeSec: 30})
	if w := cl.canaryHold(next); !strings.Contains(w, "min left") {
		t.Fatalf("a young canary must hold the rest: %q", w)
	}
	set(PeerStatusMsg{Version: next, UptimeSec: 3600, Gateways: []GwState{{GroupID: 1, Serving: true, Health: "bad"}}})
	if w := cl.canaryHold(next); !strings.Contains(w, "bad") {
		t.Fatalf("a bad gateway must hold the rest: %q", w)
	}
	set(PeerStatusMsg{Version: next, UptimeSec: 3600, Gateways: []GwState{{GroupID: 1, Serving: true, Health: "ok"}}})
	if w := cl.canaryHold(next); w != "" {
		t.Fatalf("a soaked healthy canary must release the rest: %q", w)
	}
	set(PeerStatusMsg{Version: version(), UpdateFailed: next})
	if w := cl.canaryHold(next); !strings.Contains(w, "failed") {
		t.Fatalf("a failed update must hold the rest: %q", w)
	}
	set(PeerStatusMsg{Version: next, UptimeSec: 3600, Updating: true})
	if w := cl.canaryHold(next); w != "" {
		t.Fatalf("an updating member is the rolling rule's business, not the canary's: %q", w)
	}
}

// The canary shows a countdown while it soaks, and only while another member still runs an older version.
func TestSoakLeft(t *testing.T) {
	a, b := newTNode(t, "A"), newTNode(t, "B")
	b.join(a)
	for i := 0; i < 2; i++ {
		a.sync()
		b.sync()
	}
	next := fmt.Sprintf("%d", mustInt(version())+1)
	cl := a.mg.cl
	old := updateSoak
	updateSoak = 2 * time.Minute
	defer func() { updateSoak = old }()
	left := func(m PeerStatusMsg) map[string]int {
		cl.mu.Lock()
		defer cl.mu.Unlock()
		pi := cl.info[b.addr]
		pi.Reachable, pi.Msg = true, m
		return cl.soakLeftLocked(cl.node.Snapshot())
	}
	if l := left(PeerStatusMsg{Version: next, UptimeSec: 20}); l[b.addr] < 99 || l[b.addr] > 101 {
		t.Fatalf("the canary has ~100s left: %v", l)
	}
	if l := left(PeerStatusMsg{Version: next, UptimeSec: 200}); len(l) != 0 {
		t.Fatalf("a soaked canary shows nothing: %v", l)
	}
	// A (this node) is on the old version here, so make the roles clear: a soaked peer on the newest version ends the soak for all
	if l := left(PeerStatusMsg{Version: next, UptimeSec: 3600}); len(l) != 0 {
		t.Fatalf("after the canary soaked no node shows a soak: %v", l)
	}
	if l := left(PeerStatusMsg{Version: version(), UptimeSec: 5}); len(l) != 0 {
		t.Fatalf("with no newer version nothing soaks: %v", l)
	}
}

// A member that vanishes while updating holds the others for a while: it may be crash-looping.
func TestCanaryHoldLostContact(t *testing.T) {
	a, b := newTNode(t, "A"), newTNode(t, "B")
	b.join(a)
	for i := 0; i < 2; i++ {
		a.sync()
		b.sync()
	}
	next := fmt.Sprintf("%d", mustInt(version())+1)
	cl := a.mg.cl
	set := func(reach bool, m PeerStatusMsg, seen time.Time) {
		cl.mu.Lock()
		pi := cl.info[b.addr]
		pi.Reachable, pi.Msg, pi.LastSeen = reach, m, seen
		cl.mu.Unlock()
	}
	set(false, PeerStatusMsg{Version: version(), Updating: true}, time.Now().Add(-30*time.Second))
	if w := cl.canaryHold(next); !strings.Contains(w, "lost contact") {
		t.Fatalf("a member lost while updating must hold the rest: %q", w)
	}
	set(false, PeerStatusMsg{Version: version(), Updating: true}, time.Now().Add(-time.Hour))
	if w := cl.canaryHold(next); w != "" {
		t.Fatalf("a member gone for an hour must not hold updates forever: %q", w)
	}
	set(false, PeerStatusMsg{Version: version()}, time.Now())
	if w := cl.canaryHold(next); w != "" {
		t.Fatalf("a member that was down and not updating is not a canary: %q", w)
	}
}

// After the boot guard rolls a version back, the node reports it failed (it survives the restart) until a newer one is staged.
func TestRolledBackVersionStaysFailed(t *testing.T) {
	u, err := NewUpdater(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	next := fmt.Sprintf("%d", mustInt(version())+1)
	if u.FailedFor() != "" {
		t.Fatal("nothing failed yet")
	}
	u.writeGuard(bootGuard{State: "rolled-back", From: version(), To: next})
	if got := u.FailedFor(); got != next {
		t.Fatalf("FailedFor = %q, want %q", got, next)
	}
	u.writeGuard(bootGuard{State: "applying", From: version(), To: next})
	if got := u.FailedFor(); got != "" {
		t.Fatalf("a new attempt clears the failure: %q", got)
	}
}

// Every node serves its own gateway and is excluded from the others' (as in a cluster of single-node gateways in four
// subnets).  The update order must still apply: no member may look "held back by the safety rule" just because the others
// do not report its gateway.  Before v341 every member did, so all of them updated at once and the canary never held.
func TestOrderAppliesWhenEveryNodeServesItsOwnGateway(t *testing.T) {
	names := []string{"A", "B", "C", "D"}
	var members []peerGateways
	var groups []GroupConfig
	for i, n := range names {
		gid := i + 1
		pg := selfGateways(n, true, []GwState{{GroupID: gid, Serving: true}})
		pg.NodeID = "id" + n
		members = append(members, pg)
		var ex []string
		for _, o := range names {
			if o != n {
				ex = append(ex, "id"+o)
			}
		}
		groups = append(groups, GroupConfig{GroupID: gid, ExcludedNodes: ex})
	}
	if !heldBySafety("A", members[0].gwList(), members) {
		t.Fatal("test set-up: without the exclusions A must look held (that is the bug)")
	}
	markGatewayExcluded(members, groups)
	for _, m := range members {
		if heldBySafety(m.Addr, m.gwList(), members) {
			t.Fatalf("%s must not look held back once the exclusions are known", m.Addr)
		}
	}
}

// gwList turns the map form back into the list heldBySafety takes.
func (p peerGateways) gwList() []GwState {
	var out []GwState
	for id, s := range p.Serving {
		out = append(out, GwState{GroupID: id, Serving: s})
	}
	return out
}

// A member that stops answering while it installs an update is "updating", not down: the Topology and Upgrade pages
// show it so, and the gateway it serves reads "updating" (grey) instead of "no node is serving" (red).
func TestRestartingMemberIsNotDown(t *testing.T) {
	next := fmt.Sprintf("%d", mustInt(version())+1)
	recent := time.Now().Add(-20 * time.Second)
	cases := []struct {
		name string
		pi   *peerInfo
		want bool
	}{
		{"installing", &peerInfo{Msg: PeerStatusMsg{Updating: true}, LastSeen: recent}, true},
		{"newer source staged", &peerInfo{Msg: PeerStatusMsg{Version: version(), SourceVersion: next}, LastSeen: recent}, true},
		{"answering", &peerInfo{Reachable: true, Msg: PeerStatusMsg{Updating: true}, LastSeen: recent}, false},
		{"down, nothing pending", &peerInfo{Msg: PeerStatusMsg{Version: version(), SourceVersion: version()}, LastSeen: recent}, false},
		{"gone for an hour", &peerInfo{Msg: PeerStatusMsg{Updating: true}, LastSeen: time.Now().Add(-time.Hour)}, false},
		{"never seen", &peerInfo{Msg: PeerStatusMsg{Updating: true}}, false},
	}
	for _, c := range cases {
		if got := restartingInfo(c.pi); got != c.want {
			t.Errorf("%s: restarting = %v, want %v", c.name, got, c.want)
		}
	}
	st, _ := clusterGateway([]CanvasNode{{Gw: "updating"}})
	if st != "idle" {
		t.Errorf("a gateway whose only node is updating is %q, want idle", st)
	}
	if st, _ := clusterGateway([]CanvasNode{{Gw: "updating"}, {Gw: "down"}}); st != "bad" {
		t.Errorf("an updating node must not hide a node that is really down: %q", st)
	}
	if st, _ := clusterGateway([]CanvasNode{{Gw: "updating"}, {Gw: "ok"}}); st != "ok" {
		t.Errorf("a gateway still served by another node stays ok: %q", st)
	}
}

// The Topology node tooltip shows how long the daemon on that node has been running: this node's own, and a peer's from its
// cluster status message.
func TestCanvasNodeUptime(t *testing.T) {
	a, b := newTNode(t, "A"), newTNode(t, "B")
	b.join(a)
	for i := 0; i < 2; i++ {
		a.sync()
		b.sync()
	}
	a.mg.cl.mu.Lock()
	pi := a.mg.cl.info[b.addr]
	pi.Reachable = true
	pi.Msg.UptimeSec = 4242
	a.mg.cl.mu.Unlock()
	var self, peer *CanvasNode
	nodes := a.mg.cl.canvasNodesEx(1, "ok", "", false, nil)
	for i := range nodes {
		if nodes[i].Self {
			self = &nodes[i]
		} else if nodes[i].Addr == b.addr {
			peer = &nodes[i]
		}
	}
	if self == nil || peer == nil {
		t.Fatalf("nodes: %+v", nodes)
	}
	if peer.UptimeSec != 4242 {
		t.Errorf("peer uptime = %d, want 4242", peer.UptimeSec)
	}
	if self.UptimeSec < 0 || self.UptimeSec > 3600 {
		t.Errorf("own uptime = %d, want the age of this process", self.UptimeSec)
	}
}

// The gateway's uptime is the cluster's: the longest unbroken time any node serving it now has served it.
func TestClusterUpSec(t *testing.T) {
	nodes := []CanvasNode{
		{Gw: "ok", ServingSec: 90},
		{Gw: "degraded", ServingSec: 4000},
		{Gw: "down", ServingSec: 99999}, // not serving now: does not count
		{Gw: "ok", ServingSec: 5000, Excluded: true},
	}
	if got := clusterUpSec(nodes); got != 4000 {
		t.Errorf("clusterUpSec = %d, want 4000", got)
	}
	if got := clusterUpSec(nil); got != 0 {
		t.Errorf("no nodes: %d", got)
	}
}

// The gateway's uptime is the cluster's and survives a rolling update: a node that has just restarted (its own serving time
// is seconds) learns from a member that is still serving how long the gateway has been up; it starts again only when nobody
// serves it.
func TestGatewayUptimeSurvivesRestart(t *testing.T) {
	a, b := newTNode(t, "A"), newTNode(t, "B")
	b.join(a)
	for i := 0; i < 2; i++ {
		a.sync()
		b.sync()
	}
	a.mg.gwFn = func() []GwState { return []GwState{{GroupID: 1, Serving: true}} }
	setPeer := func(gs []GwState) {
		a.mg.cl.mu.Lock()
		pi := a.mg.cl.info[b.addr]
		pi.Reachable = true
		pi.Msg.Gateways = gs
		a.mg.cl.mu.Unlock()
	}
	hour := time.Now().Add(-time.Hour).Unix()
	// A has just come back; B has served for an hour
	setPeer([]GwState{{GroupID: 1, Serving: true, ServingSec: 3600, UpSince: hour}})
	a.mg.cl.sampleGwUp()
	if got := a.mg.gwUpSeconds(1); got < 3590 || got > 3700 {
		t.Fatalf("after a restart the uptime must come from the serving member: %d s, want ~3600", got)
	}
	// B restarts in turn: it reports nothing yet, A keeps what it learned
	setPeer(nil)
	a.mg.cl.sampleGwUp()
	if got := a.mg.gwUpSeconds(1); got < 3590 {
		t.Fatalf("the uptime must survive the other node restarting: %d s", got)
	}
	// and A reports it, so B (restarted) can learn it again
	var up int64
	for _, g := range a.mg.localGateways() {
		if g.GroupID == 1 {
			up = g.UpSince
		}
	}
	if up == 0 || time.Now().Unix()-up < 3590 {
		t.Fatalf("A must pass the start on in its report: up_since %d", up)
	}
	// nobody serves it: the uptime is dropped, and starts again when somebody does
	a.mg.gwFn = func() []GwState { return []GwState{{GroupID: 1, Serving: false}} }
	a.mg.cl.sampleGwUp()
	if got := a.mg.gwUpSeconds(1); got != 0 {
		t.Fatalf("an outage ends the uptime: %d", got)
	}
}

// An anycast address's uptime is the cluster's too: a node that restarts learns from the members still announcing it, and
// it is dropped only when no node announces it.
func TestAnycastUptimeSurvivesRestart(t *testing.T) {
	a, b := newTNode(t, "A"), newTNode(t, "B")
	b.join(a)
	for i := 0; i < 2; i++ {
		a.sync()
		b.sync()
	}
	anycastLocal.mu.Lock()
	saved := anycastLocal.list
	anycastLocal.mu.Unlock()
	defer func() {
		anycastLocal.mu.Lock()
		anycastLocal.list = saved
		anycastLocal.mu.Unlock()
	}()
	setLocal := func(l []AnycastUp) {
		anycastLocal.mu.Lock()
		anycastLocal.list = l
		anycastLocal.mu.Unlock()
	}
	setPeer := func(l []AnycastUp) {
		a.mg.cl.mu.Lock()
		pi := a.mg.cl.info[b.addr]
		pi.Reachable = true
		pi.Msg.AnycastUp = l
		a.mg.cl.mu.Unlock()
	}
	hour := time.Now().Add(-time.Hour).Unix()
	// A has just started announcing it; B has had it up for an hour
	setLocal([]AnycastUp{{Addr: "10.9.9.9", Since: time.Now().Unix() - 5}})
	setPeer([]AnycastUp{{Addr: "10.9.9.9", Since: hour, UpSince: hour}})
	a.mg.cl.sampleAnycastUp()
	if got := a.mg.anyUpSeconds("10.9.9.9"); got < 3590 || got > 3700 {
		t.Fatalf("uptime must come from the member that has it up: %d s, want ~3600", got)
	}
	// B restarts in turn: A keeps it, and reports it
	setPeer(nil)
	a.mg.cl.sampleAnycastUp()
	if got := a.mg.anyUpSeconds("10.9.9.9"); got < 3590 {
		t.Fatalf("the uptime must survive the other node restarting: %d s", got)
	}
	rep := a.mg.anycastReport()
	if len(rep) != 1 || rep[0].UpSince == 0 || time.Now().Unix()-rep[0].UpSince < 3590 {
		t.Fatalf("A must pass the start on: %+v", rep)
	}
	// nobody announces it: dropped
	setLocal(nil)
	a.mg.cl.sampleAnycastUp()
	if got := a.mg.anyUpSeconds("10.9.9.9"); got != 0 {
		t.Fatalf("an outage ends the uptime: %d", got)
	}
}
