package main

import (
	"sync"
	"time"
)

// The v4 and v6 engines of a group run side by side on one node and share the macvlan of a slot: its name and its
// MAC depend only on the group and the slot.  Two things follow, and both bit in the field:
//
//   - A macvlan may be deleted only when no engine on the node is still using it.  A v4 engine that gave up the
//     controller role used to delete "ddgwN.1" under the v6 engine that was still the controller there, taking the
//     v6 VIP with it.
//   - A slot is one MAC on the wire whatever the family, so two nodes must not use the same slot in different
//     families either (v4 slot 3 on one node, v6 slot 3 on another is one MAC on two switch ports).  The engines of
//     a node therefore tell each other which slots they know of, and a joining engine prefers the slot its sibling
//     already has, so a node ends up with the same slot in both families whenever that is possible.

type vmacKey struct{ group, slot int }

// slotView is what one engine knows: its own slot and the slots its peers use.
type slotView struct {
	group int
	own   int
	peers map[int]time.Time // slot → when a peer using it was last heard
}

var vmacReg = struct {
	sync.Mutex
	users map[vmacKey]map[*Engine]bool
	views map[*Engine]slotView
}{users: map[vmacKey]map[*Engine]bool{}, views: map[*Engine]slotView{}}

// delVmacFn is delVmac, replaceable in tests.
var delVmacFn = delVmac

// claimVmacFn creates the macvlan for a slot this engine uses itself; takeovers go through addVmacFn.
var claimVmacFn = addVmac

// delVIPFn is delVIP, replaceable in tests.
var delVIPFn = delVIP

// claimVmacLocked makes sure the slot's macvlan exists and records that this engine uses it.
func (e *Engine) claimVmacLocked(slot int, add func(iface string, group, slot int) bool) bool {
	if e.cfg.RealMACs {
		return true // real-MAC mode has no virtual MACs: nothing to create
	}
	k := vmacKey{e.cfg.GroupID, slot}
	vmacReg.Lock()
	if vmacReg.users[k] == nil {
		vmacReg.users[k] = map[*Engine]bool{}
	}
	vmacReg.users[k][e] = true
	vmacReg.Unlock()
	return add(e.cfg.Interface, e.cfg.GroupID, slot)
}

// releaseVmacLocked ends this engine's use of the slot: its VIP comes off the macvlan, and the macvlan itself goes
// only when no other engine on the node still uses it.
func (e *Engine) releaseVmacLocked(slot int) {
	if e.cfg.RealMACs {
		return
	}
	if e.vipSlot == slot {
		e.delVIPsLocked(slot)
		e.vipSlot = 0
	}
	k := vmacKey{e.cfg.GroupID, slot}
	vmacReg.Lock()
	delete(vmacReg.users[k], e)
	last := len(vmacReg.users[k]) == 0
	if last {
		delete(vmacReg.users, k)
	}
	vmacReg.Unlock()
	if last {
		delVmacFn(e.cfg.GroupID, slot)
	}
}

// publishSlotsLocked tells the sibling engine which slots this one knows of.
func (e *Engine) publishSlotsLocked() {
	v := slotView{group: e.cfg.GroupID, own: e.afnID, peers: map[int]time.Time{}}
	for _, p := range e.peers {
		if p.AfnID == 0 {
			continue
		}
		if t, seen := v.peers[p.AfnID]; !seen || p.LastSeen.After(t) {
			v.peers[p.AfnID] = p.LastSeen
		}
	}
	vmacReg.Lock()
	vmacReg.views[e] = v
	vmacReg.Unlock()
}

// forgetSlots drops this engine from the registry when it stops.
func (e *Engine) forgetSlots() {
	vmacReg.Lock()
	delete(vmacReg.views, e)
	vmacReg.Unlock()
}

// siblingSlots is the view of the other engine of this group on this node, if there is one: its own slot, every
// slot its peers use, and those it heard from within the last fresh (a slot whose user has gone quiet is probably
// on its way out and is not treated as in use).
func (e *Engine) siblingSlots(fresh time.Duration) (own int, peers, live map[int]bool) {
	vmacReg.Lock()
	defer vmacReg.Unlock()
	for o, v := range vmacReg.views {
		if o != e && v.group == e.cfg.GroupID {
			own = v.own
			peers, live = map[int]bool{}, map[int]bool{}
			for s, t := range v.peers {
				peers[s] = true
				if time.Since(t) < fresh {
					live[s] = true
				}
			}
			return
		}
	}
	return 0, nil, nil
}

// freshWindow is how recently a peer must have been heard to count as alive for the cross-family checks.
func (e *Engine) freshWindow() time.Duration {
	return time.Duration(e.cfg.HoldMS) * time.Millisecond / 2
}

// resolveCrossFamilyLocked settles the clashes between the two families of a node that the per-family elections
// cannot see.  The families rank nodes by their own addresses, so after a restart the IPv4 controller and the IPv6
// controller are often different nodes, and both would use slot 1 — one MAC (00:1a:7c:<group>:01:00) answering on two
// switch ports, which sent a share of the queries to a node with no VIP on that MAC.
//
//   - The IPv6 controller gives way: if the IPv4 side of this node hears a controller (or anyone) live in the slot
//     it holds, it moves to a free slot and announces the VIP there.  IPv4 keeps its slot, so exactly one side moves.
//   - A MAC covered for a dead peer is let go when the other family hears that slot's owner alive after all.
func (e *Engine) resolveCrossFamilyLocked() {
	if !e.running {
		return
	}
	_, _, live := e.siblingSlots(e.freshWindow())
	if len(live) == 0 {
		return
	}
	for slot := range e.takeover {
		if live[slot] {
			infof("Slot %d is in use by a live node in the other family — releasing the takeover MAC (%s)", slot, e.tag())
			e.releaseTakeoverLocked(slot)
		}
	}
	if e.state == stateActive && e.af == afIPv6 && e.afnID != 0 && live[e.afnID] {
		slot := e.freeSlotLocked()
		if slot == 0 || slot == e.afnID {
			return
		}
		infof("Controller slot %d is in use by the IPv4 controller on another node — moving to slot %d (%s)", e.afnID, slot, e.tag())
		e.releaseVmacLocked(e.afnID)
		e.afnID = slot
		e.setupVmacsLocked()
		e.publishSlotsLocked()
	}
}

// freeSlotLocked picks the forwarder slot to use: the sibling engine's slot when it is free here, otherwise the
// lowest slot nobody this node knows of uses, in either family.  Slot 1 is the controller's.  0 means none is free.
func (e *Engine) freeSlotLocked() int {
	own, sib, _ := e.siblingSlots(0)
	used := map[int]bool{1: true}
	for _, p := range e.peers {
		if p.AfnID != 0 {
			used[p.AfnID] = true
		}
	}
	if own >= 2 && own <= e.cfg.MaxAFNs && !used[own] && !sib[own] {
		return own
	}
	for s := range sib {
		used[s] = true
	}
	if own != 0 {
		used[own] = true
	}
	for slot := 2; slot <= e.cfg.MaxAFNs; slot++ {
		if !used[slot] {
			return slot
		}
	}
	// every slot is taken somewhere: fall back to what this family's own peers allow
	for slot := 2; slot <= e.cfg.MaxAFNs; slot++ {
		clash := false
		for _, p := range e.peers {
			if p.AfnID == slot {
				clash = true
			}
		}
		if !clash {
			return slot
		}
	}
	return 0
}
