package main

import (
	"fmt"
	"sort"
	"time"
)

// Automatic failover: when the primary has been unreachable for failoverAfter and a strict majority of the members agree, the
// best replica promotes itself.  A replica cut off from the others never has a majority, so it never promotes; one candidate
// is chosen by a rule every member computes alike.  The setting is "" (automatic: on from three members), "on" or "off".
var failoverAfter = 5 * time.Minute

func normalizeFailover(in string) (string, error) {
	switch in {
	case "", "auto", "automatic", "default":
		return "", nil
	case "on", "off":
		return in, nil
	}
	return "", fmt.Errorf("failover must be auto, on or off")
}

// failoverEnabled says whether failover is in force: "on" and "off" are as set, automatic is on from three members.
func failoverEnabled(mode string, members int) bool {
	switch mode {
	case "on":
		return true
	case "off":
		return false
	}
	return members >= 3
}

// pickFailover chooses the replica that takes over when the primary is gone: reachable (this node counts), not paused, not installing an
// update or with one failed; the newest shared settings, then the newest version, then the lowest address.  "" when none.
func pickFailover(peers []PeerView, primaryAddr string) string {
	var c []PeerView
	for _, p := range peers {
		if p.Addr == primaryAddr || p.IsPrimary || !p.Reachable || p.NodePaused || p.Updating || p.UpdateFailed != "" {
			continue
		}
		c = append(c, p)
	}
	if len(c) == 0 {
		return ""
	}
	sort.Slice(c, func(i, j int) bool {
		if c[i].SharedRev != c[j].SharedRev {
			return c[i].SharedRev > c[j].SharedRev
		}
		if c[i].Version != c[j].Version {
			return versionGreater(c[i].Version, c[j].Version)
		}
		return c[i].Addr < c[j].Addr
	})
	return c[0].Addr
}

// primaryDownSec is this node's vote: how long it has gone without reaching the primary.
func (c *Cluster) primaryDownSec() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.primaryDownSince.IsZero() {
		return 0
	}
	return int64(time.Since(c.primaryDownSince) / time.Second)
}

// failoverTick runs on every sync: it keeps the "primary unreachable since" clock and, when the rules are met, promotes this
// node.
func (c *Cluster) failoverTick() {
	if !c.Enabled() {
		return
	}
	snap := c.node.Snapshot()
	view := c.View()
	var prim *PeerView
	for i := range view.Peers {
		if view.Peers[i].Addr == snap.PrimaryAddr && !view.Peers[i].Self {
			prim = &view.Peers[i]
		}
	}
	c.mu.Lock()
	// a primary that answers, or is restarting into an update, is not gone
	if snap.Role == RolePrimary || prim == nil || prim.Reachable || prim.Restarting || prim.Updating {
		c.primaryDownSince = time.Time{}
		c.mu.Unlock()
		return
	}
	if c.primaryDownSince.IsZero() {
		c.primaryDownSince = time.Now()
	}
	down := time.Since(c.primaryDownSince)
	c.mu.Unlock()
	members := len(snap.Peers) + 1
	if !failoverEnabled(c.mg.upd.Intent().Failover, members) || down < failoverAfter {
		return
	}
	votes := 1
	c.mu.Lock()
	for addr, pi := range c.info {
		if addr == snap.PrimaryAddr || pi == nil || !pi.Reachable {
			continue
		}
		if pi.Msg.PrimaryAddr == snap.PrimaryAddr && pi.Msg.PrimaryDownSec >= int64(failoverAfter/time.Second) {
			votes++
		}
	}
	c.mu.Unlock()
	if votes < members/2+1 || pickFailover(view.Peers, snap.PrimaryAddr) != snap.SelfAddr {
		return
	}
	warnf("cluster: the primary %s has not answered for %s and %d of %d members agree: promoting this node (automatic failover)", snap.PrimaryAddr, down.Round(time.Second), votes, members)
	if _, err := c.Promote("automatic failover"); err != nil {
		warnf("cluster: automatic failover failed: %v", err)
		return
	}
	c.mu.Lock()
	c.primaryDownSince = time.Time{}
	c.mu.Unlock()
}
