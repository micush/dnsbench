package main

import (
	"sync"
	"time"
)

// AnycastUp is what a node says about one anycast address it is announcing now: since when this node has had it up without
// a break (Since) and since when somebody in the cluster has (UpSince, as this node knows it; see sampleAnycastUp).
type AnycastUp struct {
	Addr    string `json:"addr"`
	Since   int64  `json:"since"`
	UpSince int64  `json:"up_since,omitempty"`
}

// anycastLocal is this node's anycast addresses that are up and announced, as the last topology sample saw them.
var anycastLocal struct {
	mu   sync.Mutex
	list []AnycastUp
}

// storeAnycastLocal notes from a topology sample (canvasGroups) which anycast addresses this node has up, for the status
// message and for sampleAnycastUp.
func storeAnycastLocal(groups []CanvasGateway) {
	best := map[string]int64{}
	var order []string
	for _, g := range groups {
		for _, a := range g.Anycast {
			if !a.Up || a.Paused != "" || a.Status == "bad" || a.Uptime == nil || !a.Uptime.Up {
				continue
			}
			if t, ok := best[a.Addr]; !ok {
				best[a.Addr] = a.Uptime.Since
				order = append(order, a.Addr)
			} else if a.Uptime.Since < t {
				best[a.Addr] = a.Uptime.Since
			}
		}
	}
	list := make([]AnycastUp, 0, len(order))
	for _, ad := range order {
		list = append(list, AnycastUp{Addr: ad, Since: best[ad]})
	}
	anycastLocal.mu.Lock()
	anycastLocal.list = list
	anycastLocal.mu.Unlock()
}

// anycastReport is this node's list for the status message, with the cluster-wide start this node knows.
func (m *Mgmt) anycastReport() []AnycastUp {
	anycastLocal.mu.Lock()
	out := append([]AnycastUp(nil), anycastLocal.list...)
	anycastLocal.mu.Unlock()
	m.gwUpMu.Lock()
	for i := range out {
		if t, ok := m.anyUp[out[i].Addr]; ok {
			out[i].UpSince = t.Unix()
		}
	}
	m.gwUpMu.Unlock()
	return out
}

// anyUpSeconds is how long somebody in the cluster has had the anycast address up without a break (0: nobody now, or not known yet).
func (m *Mgmt) anyUpSeconds(addr string) int64 {
	m.gwUpMu.Lock()
	defer m.gwUpMu.Unlock()
	if t, ok := m.anyUp[addr]; ok {
		return int64(time.Since(t) / time.Second)
	}
	return 0
}

// sampleAnycastUp does for the anycast addresses what sampleGwUp does for gateways: it keeps since when somebody in the
// cluster has had each one up without a break, takes the earliest of its own and what the members report, and drops it
// when no node announces the address (so a rolling update does not reset it; a real outage does).
func (c *Cluster) sampleAnycastUp() {
	m := c.mg
	if m == nil {
		return
	}
	anycastLocal.mu.Lock()
	mine := append([]AnycastUp(nil), anycastLocal.list...)
	anycastLocal.mu.Unlock()
	best := map[string]time.Time{}
	note := func(addr string, unix int64) {
		if unix <= 0 {
			return
		}
		t := time.Unix(unix, 0)
		if old, ok := best[addr]; !ok || t.Before(old) {
			best[addr] = t
		}
	}
	for _, a := range mine {
		note(a.Addr, a.Since)
	}
	c.mu.Lock()
	for _, pi := range c.info {
		if pi == nil || !pi.Reachable {
			continue
		}
		for _, a := range pi.Msg.AnycastUp {
			note(a.Addr, a.Since)
			note(a.Addr, a.UpSince)
		}
	}
	c.mu.Unlock()
	m.gwUpMu.Lock()
	defer m.gwUpMu.Unlock()
	next := map[string]time.Time{}
	for addr, t := range best {
		if old, ok := m.anyUp[addr]; ok && old.Before(t) {
			t = old
		}
		next[addr] = t
	}
	m.anyUp = next
}
