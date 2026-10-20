package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// cmdLog records, in order, the commands the daemon would run and the packets the engines send.
type cmdLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *cmdLog) add(s string) {
	l.mu.Lock()
	l.lines = append(l.lines, s)
	l.mu.Unlock()
}

func (l *cmdLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func (l *cmdLog) reset() { l.mu.Lock(); l.lines = nil; l.mu.Unlock() }

// index of the first line containing all of the words at or after from, or -1.
func idx(lines []string, from int, words ...string) int {
	for i := from; i < len(lines); i++ {
		ok := true
		for _, w := range words {
			if !strings.Contains(lines[i], w) {
				ok = false
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func recordCmds(t *testing.T) *cmdLog {
	t.Helper()
	l := &cmdLog{}
	h := func(name string, args []string) bool {
		l.add("cmd: " + name + " " + strings.Join(args, " "))
		return true
	}
	cmdHook.Store(&h)
	t.Cleanup(func() { cmdHook.Store(nil) })
	return l
}

// quiet: nothing fires by itself in the background of a test.
func quietTimers(t *testing.T) {
	t.Helper()
	oa, ol, om := announceBurst, lingerMin, lingerMax
	announceBurst, lingerMin, lingerMax = nil, time.Hour, time.Hour
	t.Cleanup(func() { announceBurst, lingerMin, lingerMax = oa, ol, om })
}

func slotOf(e *Engine) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.afnID
}

// A controller that steps down keeps its own virtual MAC, its DNS and the MACs it covers up, and moves the VIP onto lo
// before it takes it off the macvlan: at no moment is the node without the MAC or the VIP.
func TestStepDownIsInPlace(t *testing.T) {
	quietTimers(t)
	m, a, b, c := steadyThree(t)
	// c asks for the role and becomes the controller; b is then the one that steps down in place
	c.mu.Lock()
	c.assertAGCLocked()
	c.mu.Unlock()
	m.pump(t)
	m.round(t, "10.0.0.204", "10.0.0.203", "10.0.0.202")
	_ = a
	// give b the role by hand, with a MAC it covers for a dead peer, so every part of the role is in place
	b.mu.Lock()
	b.agcIP = b.myIP
	b.becomeAGCLocked()
	own := b.afnID
	b.takeover[5] = true
	vmacReg.Lock()
	vmacReg.users[vmacKey{b.cfg.GroupID, 5}] = map[*Engine]bool{b: true}
	vmacReg.Unlock()
	b.mu.Unlock()

	log := recordCmds(t)
	b.mu.Lock()
	b.stepDownToLocked("10.0.0.202")
	st, slot, vip, lo := b.state, b.afnID, b.vipSlot, b.dnsLoAdded
	b.mu.Unlock()
	lines := log.snapshot()
	t.Logf("commands:\n  %s", strings.Join(lines, "\n  "))

	if st != stateForward || slot != own || vip != 0 {
		t.Fatalf("after stepping down: state %v slot %d (was %d) vipSlot %d", st, slot, own, vip)
	}
	if !lo {
		t.Fatal("the VIP is not on lo after stepping down")
	}
	if idx(lines, 0, "link del") >= 0 {
		t.Fatalf("a macvlan was removed while stepping down: %v", lines)
	}
	onLo := idx(lines, 0, "addr replace", "dev lo")
	offMac := idx(lines, 0, "addr del", "dev "+vmacName(b.cfg.GroupID, own))
	if onLo < 0 || offMac < 0 || onLo > offMac {
		t.Fatalf("the VIP must go onto lo (%d) before it comes off the macvlan (%d): %v", onLo, offMac, lines)
	}
	// the MAC it covered for another node is still there (released after a grace)
	b.mu.Lock()
	covered, lingering := b.takeover[5], b.lingering[5]
	b.mu.Unlock()
	if covered || !lingering {
		t.Fatalf("covered MAC: takeover %v lingering %v", covered, lingering)
	}
}

// The controller that holds slot 1 must give it up when it steps down; its macvlan stays up for a grace, then goes.
func TestSlotOneIsReleasedAfterAGrace(t *testing.T) {
	quietTimers(t)
	lingerMin, lingerMax = 40*time.Millisecond, 40*time.Millisecond
	m, a, _, _ := steadyThree(t)
	if slotOf(a) != 1 {
		t.Fatalf("expected the controller to hold slot 1, has %d", slotOf(a))
	}
	_ = m
	log := recordCmds(t)
	a.mu.Lock()
	a.stepDownToLocked("10.0.0.202")
	newSlot := a.afnID
	a.mu.Unlock()
	if newSlot == 1 || newSlot == 0 {
		t.Fatalf("slot after stepping down: %d", newSlot)
	}
	lines := log.snapshot()
	if idx(lines, 0, "link del", vmacName(a.cfg.GroupID, 1)) >= 0 {
		t.Fatalf("slot 1's macvlan was deleted at once: %v", lines)
	}
	if idx(lines, 0, "macvlan-for", vmacName(a.cfg.GroupID, newSlot)) < 0 {
		t.Fatalf("no macvlan for the new slot: %v", lines)
	}
	time.Sleep(200 * time.Millisecond)
	if idx(log.snapshot(), 0, "link del", vmacName(a.cfg.GroupID, 1)) < 0 {
		t.Fatalf("slot 1's macvlan was never released: %v", log.snapshot())
	}
}

// The node that asks for the role is the controller (VIP on its macvlan, announced) before the incumbent is asked to
// step down, and takes slot 1's MAC over when the incumbent holds it.
func TestAssertIsMakeBeforeBreak(t *testing.T) {
	quietTimers(t)
	m, a, _, c := steadyThree(t)
	log := recordCmds(t)
	m.logTo = log
	c.mu.Lock()
	c.assertAGCLocked()
	c.mu.Unlock()
	lines := log.snapshot()
	t.Logf("sequence:\n  %s", strings.Join(lines, "\n  "))
	vipUp := idx(lines, 0, "addr add", "dev "+vmacName(c.cfg.GroupID, slotOf(c)))
	resign := idx(lines, 0, "send resign to 10.0.0.204")
	if vipUp < 0 || resign < 0 || vipUp > resign {
		t.Fatalf("the VIP must be up on the new controller (%d) before the incumbent is asked to step down (%d)", vipUp, resign)
	}
	c.mu.Lock()
	took := c.takeover[1]
	c.mu.Unlock()
	if !took {
		t.Fatal("the incumbent held slot 1 and gives it up: the new controller must cover that MAC")
	}
	m.pump(t)
	a.mu.Lock()
	st := a.state
	a.mu.Unlock()
	if st != stateForward {
		t.Fatalf("the incumbent did not step down: %v", st)
	}
}

// A MAC taken over is announced again and again for a while, so the last word is the new owner's.
func TestTakeoverIsAnnouncedInABurst(t *testing.T) {
	quietTimers(t)
	announceBurst = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}
	var mu sync.Mutex
	n := 0
	old := announceVmacFn
	announceVmacFn = func(string, int, int, string) { mu.Lock(); n++; mu.Unlock() }
	defer func() { announceVmacFn = old }()

	_, a, _, _ := steadyThree(t)
	mu.Lock()
	n = 0 // what the setting up of the three engines announced does not count
	mu.Unlock()
	a.mu.Lock()
	a.takeoverAFNLocked(7)
	a.mu.Unlock()
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	got := n
	mu.Unlock()
	if got != 4 {
		t.Fatalf("%d announcements, want 1 and 3 more", got)
	}
	// once the MAC is let go, no more
	a.mu.Lock()
	a.releaseTakeoverLocked(7)
	a.mu.Unlock()
	announceBurst = []time.Duration{10 * time.Millisecond}
	mu.Lock()
	n = 0
	mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if n != 0 {
		t.Fatalf("%d announcements for a MAC that is not covered", n)
	}
}

// A controller that stops tells the group twice: one lost multicast must not leave the others waiting for the hold time.
func TestStoppingControllerSaysItTwice(t *testing.T) {
	old := leaveGrace
	leaveGrace = 5 * time.Millisecond
	defer func() { leaveGrace = old }()
	e := newTestEngine()
	e.cfg.Neighbors = []string{"10.0.0.9"}
	cc := &capConn{}
	e.conn = cc
	e.state = stateSpeak
	e.mu.Lock()
	e.runElectionLocked()
	e.mu.Unlock()
	e.Stop()
	cc.mu.Lock()
	defer cc.mu.Unlock()
	n := 0
	for _, b := range cc.sent {
		if p, err := parsePacket(b, e.cfg.keyBytes()); err == nil && p.Type == pktResign {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d resign packets, want 2", n)
	}
}
