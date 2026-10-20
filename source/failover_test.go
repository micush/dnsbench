package main

import (
	"context"
	"testing"
	"time"
)

func TestFailoverEnabled(t *testing.T) {
	for _, c := range []struct {
		mode    string
		members int
		want    bool
	}{{"", 1, false}, {"", 2, false}, {"", 3, true}, {"", 5, true}, {"on", 2, true}, {"off", 5, false}} {
		if got := failoverEnabled(c.mode, c.members); got != c.want {
			t.Errorf("mode %q with %d members: %v, want %v", c.mode, c.members, got, c.want)
		}
	}
	for in, want := range map[string]string{"": "", "auto": "", "default": "", "on": "on", "off": "off"} {
		if got, err := normalizeFailover(in); err != nil || got != want {
			t.Errorf("normalizeFailover(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := normalizeFailover("maybe"); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

func TestPickFailover(t *testing.T) {
	pv := func(addr string, rev uint64, ver string, mods ...func(*PeerView)) PeerView {
		p := PeerView{Addr: addr, Reachable: true, SharedRev: rev, Version: ver}
		for _, m := range mods {
			m(&p)
		}
		return p
	}
	prim := pv("10.0.0.1", 9, "5", func(p *PeerView) { p.IsPrimary = true; p.Reachable = false })
	if got := pickFailover([]PeerView{prim, pv("10.0.0.3", 8, "5"), pv("10.0.0.2", 9, "5")}, "10.0.0.1"); got != "10.0.0.2" {
		t.Fatalf("newest settings: %q", got)
	}
	if got := pickFailover([]PeerView{prim, pv("10.0.0.2", 9, "4"), pv("10.0.0.3", 9, "5")}, "10.0.0.1"); got != "10.0.0.3" {
		t.Fatalf("newest version on a tie: %q", got)
	}
	if got := pickFailover([]PeerView{prim, pv("10.0.0.3", 9, "5"), pv("10.0.0.2", 9, "5")}, "10.0.0.1"); got != "10.0.0.2" {
		t.Fatalf("lowest address on a tie: %q", got)
	}
	if got := pickFailover([]PeerView{prim, pv("10.0.0.2", 9, "5", func(p *PeerView) { p.NodePaused = true }), pv("10.0.0.3", 3, "5")}, "10.0.0.1"); got != "10.0.0.3" {
		t.Fatalf("a paused member was picked: %q", got)
	}
	if got := pickFailover([]PeerView{prim, pv("10.0.0.2", 9, "5", func(p *PeerView) { p.Updating = true })}, "10.0.0.1"); got != "" {
		t.Fatalf("an updating member was picked: %q", got)
	}
}

// Three nodes, the primary goes silent: after the wait, and only with a majority agreeing, one replica (the same one every
// time) promotes itself.
func TestAutomaticFailover(t *testing.T) {
	defer func(d time.Duration) { failoverAfter = d }(failoverAfter)
	failoverAfter = 50 * time.Millisecond
	a, b, c := newTNode(t, "A"), newTNode(t, "B"), newTNode(t, "C")
	b.join(a)
	c.join(a)
	for i := 0; i < 3; i++ {
		a.sync()
		b.sync()
		c.sync()
	}
	if got := len(a.mg.cl.node.Snapshot().Peers) + 1; got != 3 {
		t.Fatalf("cluster has %d members", got)
	}
	a.mg.cl.Stop() // the primary stops answering (its process keeps running)
	round := func() {
		for _, n := range []*tnode{b, c} {
			n.mg.cl.SyncOnce(context.Background()) // fails while the primary is gone; that is the point
			n.mg.cl.failoverTick()
		}
	}
	round()
	if b.mg.cl.node.Snapshot().Role != RoleReplica || c.mg.cl.node.Snapshot().Role != RoleReplica {
		t.Fatal("promoted before the wait")
	}
	for i := 0; i < 6; i++ {
		time.Sleep(80 * time.Millisecond)
		round()
	}
	rb, rc := b.mg.cl.node.Snapshot(), c.mg.cl.node.Snapshot()
	primaries := 0
	for _, s := range []ClusterSnapshot{rb, rc} {
		if s.Role == RolePrimary {
			primaries++
		}
	}
	if primaries != 1 || rb.Epoch != rc.Epoch || rb.PrimaryAddr != rc.PrimaryAddr || rb.Epoch < 2 {
		t.Fatalf("after failover: B %s epoch %d primary %s; C %s epoch %d primary %s", rb.Role, rb.Epoch, rb.PrimaryAddr, rc.Role, rc.Epoch, rc.PrimaryAddr)
	}
}

// With failover off, or with two members, nothing promotes however long the primary is gone.
func TestFailoverStaysManual(t *testing.T) {
	defer func(d time.Duration) { failoverAfter = d }(failoverAfter)
	failoverAfter = 20 * time.Millisecond
	a, b := newTNode(t, "A"), newTNode(t, "B")
	b.join(a)
	for i := 0; i < 3; i++ {
		a.sync()
		b.sync()
	}
	a.mg.cl.Stop()
	for i := 0; i < 5; i++ {
		time.Sleep(40 * time.Millisecond)
		b.mg.cl.SyncOnce(context.Background())
		b.mg.cl.failoverTick()
	}
	if b.mg.cl.node.Snapshot().Role != RoleReplica {
		t.Fatal("a two-node cluster promoted by itself")
	}
}

// A replica that is cut off from the others as well as the primary has no majority and never promotes.
func TestFailoverNeedsAMajority(t *testing.T) {
	defer func(d time.Duration) { failoverAfter = d }(failoverAfter)
	failoverAfter = 20 * time.Millisecond
	a, b, c := newTNode(t, "A"), newTNode(t, "B"), newTNode(t, "C")
	b.join(a)
	c.join(a)
	for i := 0; i < 3; i++ {
		a.sync()
		b.sync()
		c.sync()
	}
	a.mg.cl.Stop()
	c.mg.cl.Stop()
	for i := 0; i < 6; i++ {
		time.Sleep(40 * time.Millisecond)
		b.mg.cl.SyncOnce(context.Background())
		b.mg.cl.failoverTick()
	}
	if b.mg.cl.node.Snapshot().Role != RoleReplica {
		t.Fatal("a replica on its own promoted without a majority")
	}
}
