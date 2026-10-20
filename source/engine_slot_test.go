package main

import (
	"testing"
	"time"
)

// slotFakes replaces the commands that touch the host and records what the engines asked for.
type slotFakes struct {
	deleted  []int // macvlans removed
	vipsDel  []int // slots a VIP was taken off
	claimed  []int
	restores func()
}

func stubSlotHost(t *testing.T) *slotFakes {
	f := &slotFakes{}
	oc, od, ov, oa, on := claimVmacFn, delVmacFn, delVIPFn, addVmacFn, announceVmacFn
	claimVmacFn = func(_ string, _, slot int) bool { f.claimed = append(f.claimed, slot); return true }
	addVmacFn = claimVmacFn
	delVmacFn = func(_, slot int) { f.deleted = append(f.deleted, slot) }
	delVIPFn = func(_, slot int, _ string) { f.vipsDel = append(f.vipsDel, slot) }
	announceVmacFn = func(string, int, int, string) {}
	t.Cleanup(func() { claimVmacFn, delVmacFn, delVIPFn, addVmacFn, announceVmacFn = oc, od, ov, oa, on })
	return f
}

func slotEngine(group int) *Engine {
	e := newTestEngine()
	e.cfg.GroupID = group
	e.cfg.Priority = 100
	return e
}

func has(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// The field failure: a controller that yielded to a higher-ranked controller kept slot 1, so two nodes
// had the same virtual MAC, and its hellos with slot 1 made the others take it for a controller.
func TestYieldingControllerLeavesSlotOne(t *testing.T) {
	stubSlotHost(t)
	e := slotEngine(71)
	e.state = stateSpeak
	e.mu.Lock()
	e.runElectionLocked()                                                 // lone node: controller in slot 1
	e.onPacketLocked(helloFlags(t, e, "10.0.0.7", 100, 2, false, 0), nil) // a forwarder in slot 2
	if e.state != stateActive || e.afnID != 1 {
		e.mu.Unlock()
		t.Fatalf("setup: state=%v slot=%d", e.state, e.afnID)
	}
	// a controller that outranks it appears, also holding slot 1
	e.onPacketLocked(helloFlags(t, e, "10.0.0.9", 100, 1, false, flagController), nil)
	st, slot, agc := e.state, e.afnID, e.agcIP
	e.mu.Unlock()
	defer e.Stop()
	if st != stateForward {
		t.Fatalf("state=%v, want forward", st)
	}
	if slot < 3 {
		t.Fatalf("slot=%d: must be a free forwarder slot (1 is the new controller's, 2 is taken)", slot)
	}
	if agc != "10.0.0.9" {
		t.Fatalf("agcIP=%q", agc)
	}
}

// The v4 engine yielding must not remove the macvlan the v6 engine of the same group still uses as controller,
// and must take its own VIP off it; the macvlan goes when the last user lets go.
func TestMacvlanIsDeletedOnlyByItsLastUser(t *testing.T) {
	f := stubSlotHost(t)
	v4 := slotEngine(72)
	v6 := slotEngine(72)
	v6.af = afIPv6
	v4.mu.Lock()
	v6.mu.Lock()
	v4.afnID, v4.state = 1, stateActive
	v6.afnID, v6.state = 1, stateActive
	v4.setupVmacsLocked()
	v6.setupVmacsLocked()
	v6.mu.Unlock()
	v4.vipSlot = 1
	v4.peers["10.0.0.9"] = &Peer{IP: "10.0.0.9", Priority: 200, AfnID: 1, Controller: true}
	v4.becomeAFNLocked() // yields to the better controller
	if has(f.deleted, 1) {
		t.Fatalf("v4 yielding deleted the macvlan v6 still uses: %v", f.deleted)
	}
	if !has(f.vipsDel, 1) || v4.vipSlot != 0 {
		t.Fatalf("v4 kept its VIP on the shared macvlan: vipsDel=%v vipSlot=%d", f.vipsDel, v4.vipSlot)
	}
	v4.mu.Unlock()
	v4.Stop()
	v6.mu.Lock()
	v6.cleanupVmacsLocked()
	v6.mu.Unlock()
	if !has(f.deleted, 1) {
		t.Fatalf("the last user did not remove the macvlan: %v", f.deleted)
	}
}

// A slot is one MAC whatever the family: a joining engine avoids the slots its sibling's peers use and prefers the
// slot its sibling has.
func TestJoinSlotAvoidsOtherFamilysSlots(t *testing.T) {
	stubSlotHost(t)
	sib := slotEngine(73)
	sib.af = afIPv6
	sib.mu.Lock()
	sib.peers["fe80::1"] = &Peer{IP: "fe80::1", AfnID: 2}
	sib.peers["fe80::2"] = &Peer{IP: "fe80::2", AfnID: 3}
	sib.afnID = 1 // controller of the other family here
	sib.publishSlotsLocked()
	sib.mu.Unlock()
	defer sib.forgetSlots()

	e := slotEngine(73)
	e.state = stateSpeak
	e.mu.Lock()
	e.onPacketLocked(helloFlags(t, e, "10.0.0.9", 100, 1, false, flagController), nil)
	e.peers["10.0.0.7"] = &Peer{IP: "10.0.0.7", AfnID: 2}
	slot := e.afnID
	e.mu.Unlock()
	e.Stop() // leaves the registry: the sibling is the only other engine of the group
	if slot == 0 || slot == 1 || slot == 2 || slot == 3 {
		t.Fatalf("slot=%d clashes with a slot in use in the other family", slot)
	}

	// and the sibling's own slot is taken when it is free here
	sib.mu.Lock()
	sib.afnID = 5
	sib.publishSlotsLocked()
	sib.mu.Unlock()
	f := slotEngine(73)
	f.mu.Lock()
	got := f.freeSlotLocked()
	f.mu.Unlock()
	if got != 5 {
		t.Fatalf("did not prefer the sibling's slot 5: got %d", got)
	}
}

// Two forwarders that picked the same slot at the same time: the lower-ranked one moves.
func TestForwarderMovesOffSlotAnotherNodeUses(t *testing.T) {
	stubSlotHost(t)
	join := func(group int) *Engine {
		e := slotEngine(group)
		e.state = stateSpeak
		e.mu.Lock()
		e.onPacketLocked(helloFlags(t, e, "10.0.0.2", 100, 1, false, flagController), nil)
		e.mu.Unlock()
		if e.state != stateForward || e.afnID < 2 {
			t.Fatalf("setup: state=%v slot=%d", e.state, e.afnID)
		}
		return e
	}
	// the other node ranks higher (greater IP as text): we move
	e := join(74)
	defer e.Stop()
	old := e.afnID
	e.mu.Lock()
	e.onPacketLocked(helloFlags(t, e, "10.0.0.9", 100, old, false, 0), nil)
	now, st := e.afnID, e.state
	e.mu.Unlock()
	if now == old || now < 2 || st != stateForward {
		t.Fatalf("did not move: slot %d -> %d state=%v", old, now, st)
	}
	// the other node ranks lower: it will move, we stay
	g := join(75)
	defer g.Stop()
	old = g.afnID
	g.mu.Lock()
	g.onPacketLocked(helloFlags(t, g, "10.0.0.3", 100, old, false, 0), nil)
	now = g.afnID
	g.mu.Unlock()
	if now != old {
		t.Fatalf("a lower-ranked clash made us move: %d -> %d", old, now)
	}
	// a controller holding the slot always wins it
	h := join(76)
	defer h.Stop()
	old = h.afnID
	h.mu.Lock()
	h.onPacketLocked(helloFlags(t, h, "10.0.0.3", 100, old, false, flagController), nil)
	now = h.afnID
	h.mu.Unlock()
	if now == old {
		t.Fatalf("kept a slot the controller holds")
	}
}

// A forwarder follows the controller its hellos name, even when an election among the peers it heard picked another.
func TestForwarderFollowsControllerHellos(t *testing.T) {
	stubSlotHost(t)
	e := slotEngine(77)
	e.state = stateSpeak
	e.mu.Lock()
	defer e.Stop()
	e.onPacketLocked(helloFlags(t, e, "10.0.0.2", 100, 1, false, flagController), nil)
	e.onPacketLocked(helloFlags(t, e, "10.0.0.8", 100, 2, false, 0), nil) // a forwarder that ranks higher
	e.agcIP = "10.0.0.8"                                                  // as a missed hello left it
	e.onPacketLocked(helloFlags(t, e, "10.0.0.2", 100, 1, false, flagController), nil)
	got := e.agcIP
	// two controllers heard: the higher-ranked one is followed, whichever speaks last
	e.onPacketLocked(helloFlags(t, e, "10.0.0.9", 100, 1, false, flagController), nil)
	e.onPacketLocked(helloFlags(t, e, "10.0.0.2", 100, 1, false, flagController), nil)
	both := e.agcIP
	e.mu.Unlock()
	if got != "10.0.0.2" {
		t.Errorf("agcIP=%q after the controller's hello, want 10.0.0.2", got)
	}
	if both != "10.0.0.9" {
		t.Errorf("agcIP=%q with two controllers, want the higher-ranked 10.0.0.9", both)
	}
}

func TestFreeSlotSkipsUsedAndStaysInRange(t *testing.T) {
	e := slotEngine(78)
	e.cfg.MaxAFNs = 4
	e.peers["a"] = &Peer{IP: "a", AfnID: 2}
	e.peers["b"] = &Peer{IP: "b", AfnID: 4}
	e.mu.Lock()
	got := e.freeSlotLocked()
	e.mu.Unlock()
	if got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
	e.peers["c"] = &Peer{IP: "c", AfnID: 3}
	e.mu.Lock()
	got = e.freeSlotLocked()
	e.mu.Unlock()
	if got != 0 {
		t.Fatalf("all slots taken: got %d, want 0", got)
	}
}

// The IPv4 and IPv6 elections rank nodes by their own addresses, so they often pick different controller nodes; both
// would use slot 1 (one MAC on two ports).  The IPv6 controller moves to a free slot when its node's IPv4 side hears
// the slot in use; a quiet (dying) user of the slot does not count.
func TestV6ControllerMovesOffSlotTheV4ControllerUses(t *testing.T) {
	stubSlotHost(t)
	for _, c := range []struct {
		name  string
		age   time.Duration
		moves bool
	}{{"live v4 controller in slot 1", 0, true}, {"v4 controller gone quiet", time.Minute, false}} {
		v4 := slotEngine(81)
		v4.mu.Lock()
		v4.peers["10.0.0.9"] = &Peer{IP: "10.0.0.9", AfnID: 1, LastSeen: time.Now().Add(-c.age)}
		v4.peers["10.0.0.7"] = &Peer{IP: "10.0.0.7", AfnID: 2, LastSeen: time.Now()}
		v4.afnID = 3
		v4.publishSlotsLocked()
		v4.mu.Unlock()

		v6 := slotEngine(81)
		v6.af = afIPv6
		v6.mu.Lock()
		v6.state, v6.afnID = stateActive, 1
		v6.resolveCrossFamilyLocked()
		slot, vip := v6.afnID, v6.vipSlot
		v6.mu.Unlock()
		if c.moves && (slot == 1 || slot == 2 || vip != slot) {
			t.Errorf("%s: slot=%d vipSlot=%d, want a free slot with the VIP on it", c.name, slot, vip)
		}
		if !c.moves && slot != 1 {
			t.Errorf("%s: moved to %d", c.name, slot)
		}
		// the IPv4 side never moves for this
		v4.mu.Lock()
		v4.state, v4.afnID = stateActive, 1
		v4.resolveCrossFamilyLocked()
		got := v4.afnID
		v4.mu.Unlock()
		if got != 1 {
			t.Errorf("%s: the IPv4 controller moved to %d", c.name, got)
		}
		v4.forgetSlots()
		v6.forgetSlots()
	}
}

// A MAC covered for a dead peer is released when the other family hears that slot's owner alive after all.
func TestTakeoverReleasedWhenOtherFamilyHearsOwner(t *testing.T) {
	f := stubSlotHost(t)
	for _, live := range []bool{true, false} {
		v4 := slotEngine(82)
		v4.mu.Lock()
		age := time.Minute
		if live {
			age = 0
		}
		v4.peers["10.0.0.9"] = &Peer{IP: "10.0.0.9", AfnID: 1, LastSeen: time.Now().Add(-age)}
		v4.publishSlotsLocked()
		v4.mu.Unlock()
		v6 := slotEngine(82)
		v6.af = afIPv6
		v6.mu.Lock()
		v6.state, v6.afnID = stateActive, 5
		v6.claimVmacLocked(1, addVmacFn)
		v6.takeover[1] = true
		v6.resolveCrossFamilyLocked()
		kept := v6.takeover[1]
		v6.mu.Unlock()
		if live == kept {
			t.Errorf("owner heard alive=%v: takeover kept=%v", live, kept)
		}
		v4.forgetSlots()
		v6.forgetSlots()
		_ = f
	}
}

// A forwarder repeats its virtual-MAC announcement so a switch entry that moved to the controller's port (a takeover
// whose announcement came last) is corrected in seconds rather than when it ages out; a controller does not.
func TestForwarderRepeatsItsAnnouncement(t *testing.T) {
	stubSlotHost(t)
	var n int
	announceVmacFn = func(string, int, int, string) { n++ }
	old := reannounceEvery
	reannounceEvery = 20 * time.Millisecond
	defer func() { reannounceEvery = old }()

	e := slotEngine(91)
	e.mu.Lock()
	e.state, e.afnID = stateForward, 3
	e.reannounceLocked()
	e.reannounceLocked() // too soon
	first := n
	e.mu.Unlock()
	time.Sleep(40 * time.Millisecond)
	e.mu.Lock()
	e.reannounceLocked()
	second := n
	e.state = stateActive
	time.Sleep(40 * time.Millisecond)
	e.reannounceLocked()
	third := n
	e.takeover[5] = true // a controller repeats the announcement for every slot it covers
	time.Sleep(40 * time.Millisecond)
	e.reannounceLocked()
	fourth := n
	e.mu.Unlock()
	if first != 1 || second != 2 || third != 2 || fourth != 3 {
		t.Fatalf("announcements: %d, %d, %d, %d (want 1, 2, 2, 3)", first, second, third, fourth)
	}
}

// An answer sent on behalf of a slot names the slot's virtual MAC in its payload but must not use it as the
// Ethernet source: a switch learns MACs from source addresses, and every answer then moved the forwarder's MAC to
// the controller's port.
func TestNAAnswerIsSentFromTheNodesOwnMAC(t *testing.T) {
	self := [6]byte{0xaa, 0xbb, 0xcc, 0, 0, 1}
	vm := vmacBytes(1, 3)
	vip := mustAddr("2001:db8::1").As16()
	dst := mustAddr("fe80::2").As16()
	f := buildNA([6]byte{1, 2, 3, 4, 5, 6}, [6]byte(ethSource(self, vm)), vm, vip, dst, 0x60000000)
	if [6]byte(f[6:12]) != self {
		t.Fatalf("Ethernet source %x, want the node's own %x", f[6:12], self)
	}
	if [6]byte(f[14+40+8+16+2:14+40+8+16+2+6]) != vm {
		t.Fatal("the advertised link-layer address is not the slot's virtual MAC")
	}
	if got := ethSource([6]byte{}, vm); [6]byte(got) != vm {
		t.Fatal("with no readable interface MAC the slot's own is the fallback")
	}
}

func TestARPAnswerIsSentFromTheNodesOwnMAC(t *testing.T) {
	self := [6]byte{0xaa, 0xbb, 0xcc, 0, 0, 1}
	vm := vmacBytes(1, 3)
	req := [6]byte{1, 2, 3, 4, 5, 6}
	f := buildARPReply(req, self, vm, [4]byte{10, 0, 0, 205}, [4]byte{10, 0, 0, 9})
	if len(f) != 42 || [6]byte(f[0:6]) != req || [6]byte(f[6:12]) != self {
		t.Fatalf("Ethernet header wrong: %x", f[:14])
	}
	if [6]byte(f[22:28]) != vm || [4]byte(f[28:32]) != [4]byte{10, 0, 0, 205} || [6]byte(f[32:38]) != req || [4]byte(f[38:42]) != [4]byte{10, 0, 0, 9} {
		t.Fatalf("ARP payload wrong: %x", f[14:])
	}
}

// A controller heard while listening is the incumbent: a newcomer that outranks it still joins as a forwarder
// (it used to win the election at the end of SPEAK because only hellos heard in SPEAK were looked at).
func TestControllerHeardWhileListeningIsRespected(t *testing.T) {
	stubSlotHost(t)
	for _, c := range []struct {
		name   string
		ip     string
		active bool
	}{{"incumbent with the lower address", "10.0.0.2", false}, {"nobody heard: wins", "", true}} {
		e := slotEngine(92)
		e.state = stateListen
		e.mu.Lock()
		if c.ip != "" {
			e.onPacketLocked(helloFlags(t, e, c.ip, 100, 1, false, flagController), nil)
			e.onPacketLocked(helloFlags(t, e, "10.0.0.3", 100, 2, false, 0), nil)
		}
		e.state = stateSpeak // the listen window is over, nothing more heard
		e.runElectionLocked()
		st, agc := e.state, e.agcIP
		e.mu.Unlock()
		if (st == stateActive) != c.active {
			t.Errorf("%s: state=%v agc=%q", c.name, st, agc)
		}
		e.Stop()
	}
}

// The interface a controller creates to cover a dead node's slot receives the queries of clients that cached that
// MAC; for IPv4 it must get the address and loose reverse-path filtering every virtual-MAC interface gets, or the
// kernel drops every one of them.
func TestTakeoverInterfaceAcceptsQueries(t *testing.T) {
	stubSlotHost(t)
	type call struct {
		slot      int
		forwarder bool
	}
	var calls []call
	old := prepareV4InputFn
	prepareV4InputFn = func(_, slot int, forwarder bool, _ string) (c, f []string) {
		calls = append(calls, call{slot, forwarder})
		return
	}
	defer func() { prepareV4InputFn = old }()

	for _, af := range []AF{afIPv4, afIPv6} {
		calls = nil
		e := slotEngine(93)
		e.af = af
		e.mu.Lock()
		e.state, e.afnID = stateActive, 1
		e.takeoverAFNLocked(4)
		e.mu.Unlock()
		want := 0
		if af == afIPv4 {
			want = 1
		}
		if len(calls) != want || (want == 1 && calls[0] != (call{4, true})) {
			t.Errorf("%v: prepare calls %v", af, calls)
		}
		e.Stop()
	}
}
