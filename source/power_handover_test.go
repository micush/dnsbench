package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A scheduled restart or shut down hands the primary role over a minute before it fires, not before.
func TestScheduledPowerHandsOverThePrimary(t *testing.T) {
	a, b := newTNode(t, "A"), newTNode(t, "B")
	b.join(a)
	for i := 0; i < 3; i++ {
		a.sync()
		b.sync()
	}
	old := powerScheduledFile
	defer func() { powerScheduledFile = old }()
	powerScheduledFile = filepath.Join(t.TempDir(), "scheduled")
	write := func(in time.Duration) {
		body := fmt.Sprintf("USEC=%d\nWARN_WALL=1\nMODE=reboot\n", time.Now().Add(in).UnixMicro())
		if err := os.WriteFile(powerScheduledFile, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(time.Hour) // an hour away: too early
	a.mg.powerHandoverTick()
	if a.mg.cl.node.Snapshot().Role != RolePrimary {
		t.Fatal("handed over an hour early")
	}
	os.Remove(powerScheduledFile) // cancelled or none
	a.mg.powerHandoverTick()
	if a.mg.cl.node.Snapshot().Role != RolePrimary {
		t.Fatal("handed over with nothing scheduled")
	}
	write(30 * time.Second) // inside the last minute
	a.mg.powerHandoverTick()
	if a.mg.cl.node.Snapshot().Role != RoleReplica || b.mg.cl.node.Snapshot().Role != RolePrimary {
		t.Fatalf("not handed over: A %s, B %s", a.mg.cl.node.Snapshot().Role, b.mg.cl.node.Snapshot().Role)
	}
}

// Pausing the primary node hands the primary role over, to a node that is in service; pausing a replica leaves it where it
// is; resuming never moves it.  With every other node paused the role stays put (never back to a paused node).
func TestPausingThePrimaryHandsItOver(t *testing.T) {
	a, b, c := newTNode(t, "A"), newTNode(t, "B"), newTNode(t, "C")
	b.join(a)
	c.join(a)
	paused := map[*tnode]*bool{}
	for _, n := range []*tnode{a, b, c} {
		v := new(bool)
		paused[n] = v
		n.mg.pausedFn = func() bool { return *v }
		n.mg.othersFn = func() int { return 1 }
	}
	settle := func() {
		for i := 0; i < 3; i++ {
			a.sync()
			b.sync()
			c.sync()
		}
	}
	pause := func(n *tnode) {
		t.Helper()
		if _, err := n.mg.NodePause(true, "test"); err != nil {
			t.Fatal(err)
		}
		*paused[n] = true
		settle()
	}
	role := func(n *tnode) Role { return n.mg.cl.node.Snapshot().Role }
	settle()
	pause(b)
	if role(a) != RolePrimary {
		t.Fatalf("pausing a replica moved the role")
	}
	pause(a) // B is paused: only C is in service
	if role(c) != RolePrimary || role(a) != RoleReplica {
		t.Fatalf("the role must go to C, the node in service: A %s, B %s, C %s", role(a), role(b), role(c))
	}
	pause(c) // everyone else is paused: nowhere to go, so it stays
	if role(c) != RolePrimary || role(a) == RolePrimary || role(b) == RolePrimary {
		t.Fatalf("the role moved to a paused node: A %s, B %s, C %s", role(a), role(b), role(c))
	}
	if _, err := c.mg.NodePause(false, "test"); err != nil {
		t.Fatal(err)
	}
	*paused[c] = false
	settle()
	if role(c) != RolePrimary {
		t.Fatalf("resuming moved the role: C %s", role(c))
	}
}
