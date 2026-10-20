package main

import (
	"context"
	"fmt"
	"sort"
)

// pickSuccessor chooses the member that should take the primary role from a node that is about to go away on purpose: the
// reachable replica with the newest shared settings (the highest revision), one that is not itself installing an update or
// failed one, and not a paused node (it is out of service); the same version as this node first, then the lowest address, so the choice is the same every time.
// "" when there is none.
func pickSuccessor(peers []PeerView, myVersion string) string {
	var c []PeerView
	for _, p := range peers {
		if p.Self || !p.Reachable || p.NodePaused || p.Updating || p.UpdateFailed != "" || p.IsPrimary {
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
		si, sj := c[i].Version == myVersion, c[j].Version == myVersion
		if si != sj {
			return si
		}
		return c[i].Addr < c[j].Addr
	})
	return c[0].Addr
}

// HandOverPrimary gives the primary role to the best other member before this node is shut down, restarted or updated on
// purpose, and says who got it ("" when this node is not the primary or nobody can take it: then nothing changes).  The role
// then stays where it lands: this node comes back as a replica.  A node that goes offline unexpectedly never gets here; its
// role stays put until an administrator promotes another node.
func (c *Cluster) HandOverPrimary(by, why string) (string, error) {
	if c == nil || !c.Enabled() {
		return "", nil
	}
	snap := c.node.Snapshot()
	if snap.Role != RolePrimary {
		return "", nil
	}
	target := pickSuccessor(c.View().Peers, version())
	if target == "" {
		return "", nil
	}
	var tp *ClusterPeer
	for i := range snap.Peers {
		if snap.Peers[i].Addr == target {
			tp = &snap.Peers[i]
		}
	}
	if tp == nil {
		return "", nil
	}
	epoch := snap.Epoch + 1
	ctx, cancel := context.WithTimeout(context.Background(), peerTimeout)
	defer cancel()
	// the successor first: if it does not take the role, nothing has changed here
	if err := c.call(ctx, *tp, "POST", "/cluster/announce", announceMsg{epoch, target}, nil, peerTimeout); err != nil {
		return "", fmt.Errorf("%s did not take over as primary: %v", target, err)
	}
	if _, err := c.node.AdoptAnnounce(epoch, target); err != nil {
		return "", err
	}
	warnf("cluster: primary role handed to %s (epoch %d) before %s, by %s", target, epoch, why, by)
	c.announce(c.node.Snapshot())
	return target, nil
}

// handOver is the daemon's side of it: the primary role moves before a restart, shut down or update, and a failure is logged,
// never a reason to stop (the administrator asked for the restart).
func (m *Mgmt) handOver(by, why string) {
	if m == nil || m.cl == nil {
		return
	}
	if _, err := m.cl.HandOverPrimary(by, why); err != nil {
		warnf("cluster: could not hand the primary role over before %s: %v", why, err)
	}
}
