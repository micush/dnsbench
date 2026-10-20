package main

import (
	"sort"
	"time"
)

// Staggering.  A task's place in its window comes from a hash of the node, the task and the occurrence (schedOffset), which
// spreads the nodes.  Tasks whose windows overlap would still land on the same second now and then, or a few seconds
// apart, so on one node the occurrences whose windows overlap are placed together: fixed ones (no window) first, then
// the others in a fixed order, each at its hashed place or, when another job is already closer than schedStagger, at
// the next free place further into its window (wrapping round).  Every input is the same on every call, so the answer
// is the same each time and a node never moves a run it has already worked out.

const (
	schedStagger   = time.Minute // jobs on one node start at least this far apart, and the gap between nodes of a "run on all nodes" (less when the window is too small)
	schedClusterMx = 300         // overlapping occurrences looked at together; beyond this the plain hashed place is used
)

type schedOcc struct {
	id   string
	base int64
	w    int64
	task *SchedTask
}

// runOffset is where after base this node runs the occurrence of t, keeping clear of the node's other jobs.  s.mu is held.
func (s *Scheduler) runOffset(node string, t SchedTask, base time.Time) time.Duration {
	if t.WindowSec <= 0 {
		return 0
	}
	key := t.ID + "|" + strconvI(base.Unix())
	if s.slots == nil || len(s.slots) > 20000 {
		s.slots = map[string]time.Duration{}
	}
	if d, ok := s.slots[key]; ok {
		return d
	}
	gap := int64(schedStagger / time.Second)
	set := map[string]schedOcc{}
	self := schedOcc{id: t.ID, base: base.Unix(), w: int64(t.WindowSec)}
	set[key] = self
	lo, hi := self.base, self.base+self.w
	for iter := 0; iter < 30; iter++ {
		changed := false
		for i := range s.tasks {
			u := &s.tasks[i]
			if u.Disabled || !u.appliesTo(node) {
				continue
			}
			w := int64(u.WindowSec)
			for _, b := range u.basesFor(node, time.Unix(lo-w-gap, 0), time.Unix(hi+gap, 0)) {
				k := u.ID + "|" + strconvI(b.Unix())
				if _, ok := set[k]; ok {
					continue
				}
				if b.Unix() > hi+gap || b.Unix()+w < lo-gap {
					continue
				}
				set[k] = schedOcc{id: u.ID, base: b.Unix(), w: w, task: u}
				lo, hi = min(lo, b.Unix()), max(hi, b.Unix()+w)
				changed = true
			}
		}
		if !changed || len(set) > schedClusterMx {
			break
		}
	}
	plain := schedOffset(node, t.ID, base, t.WindowSec)
	if len(set) > schedClusterMx {
		return plain
	}
	occs := make([]schedOcc, 0, len(set))
	minW := int64(1 << 40)
	for _, o := range set {
		occs = append(occs, o)
		if o.w > 0 && o.w < minW {
			minW = o.w
		}
	}
	g := max(1, min(gap, minW/int64(len(occs))))
	sort.Slice(occs, func(i, j int) bool {
		a, b := occs[i], occs[j]
		if (a.w == 0) != (b.w == 0) {
			return a.w == 0
		}
		if a.base != b.base {
			return a.base < b.base
		}
		return a.id < b.id
	})
	var placed []int64
	free := func(c int64) bool {
		for _, p := range placed {
			if c-p < g && p-c < g {
				return false
			}
		}
		return true
	}
	for _, o := range occs {
		k := o.id + "|" + strconvI(o.base)
		if o.w == 0 {
			placed = append(placed, o.base)
			continue
		}
		start := int64(schedOffset(node, o.id, time.Unix(o.base, 0), int(o.w)) / time.Second)
		off := start
		for tries := int64(0); tries <= o.w/g+1; tries++ {
			if free(o.base + off) {
				break
			}
			off = (off + g) % (o.w + 1)
			if tries == o.w/g+1 {
				off = start // no free place: the window is full
			}
		}
		placed = append(placed, o.base+off)
		s.slots[k] = time.Duration(off) * time.Second
	}
	if d, ok := s.slots[key]; ok {
		return d
	}
	return plain
}
