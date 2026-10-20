package main

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
)

// A gateway's shared address (and an anycast address) must not be an address a node already has: the cluster reaches
// each member on its own address, and a shared address moves between nodes, so a member's address that is also a
// shared address ends up answered by whichever node holds it (a cluster member then fails its identity check).  Only
// addresses that are new in a change are checked, so a configuration that already has such an address can still be
// saved and fixed.

// nodeAddrOwners maps each address a node is known by to the node's name: this host's own interface addresses (not
// loopback or link-local) and every address the cluster members are reached on.  Nil cluster: this host only.
func nodeAddrOwners(cl *Cluster) map[netip.Addr]string {
	out := map[netip.Addr]string{}
	add := func(s, who string) {
		if h, _, err := net.SplitHostPort(s); err == nil {
			s = h
		}
		if a, err := netip.ParseAddr(s); err == nil {
			a = a.Unmap()
			if !a.IsLoopback() && !a.IsLinkLocalUnicast() && !a.IsUnspecified() {
				if _, dup := out[a]; !dup {
					out[a] = who
				}
			}
		}
	}
	if ifs, err := net.InterfaceAddrs(); err == nil {
		for _, x := range ifs {
			if p, err := netip.ParsePrefix(x.String()); err == nil {
				add(p.Addr().String(), "this node")
			}
		}
	}
	if cl != nil && cl.Enabled() {
		snap := cl.Snapshot()
		self := cl.node.Self()
		add(self.Addr, "this node")
		for _, a := range self.Alts {
			add(a, "this node")
		}
		for _, p := range snap.Peers {
			who := p.Addr
			if h, _, err := net.SplitHostPort(who); err == nil {
				who = h
			}
			add(p.Addr, "the cluster member "+who)
			for _, a := range p.Alts {
				add(a, "the cluster member "+who)
			}
		}
	}
	return out
}

// checkVIPsNotNodes refuses a shared or anycast address that is new in next and is an address of a node.
func checkVIPsNotNodes(cur, next *DaemonConfig, cl *Cluster) error {
	had := map[netip.Addr]bool{}
	list := func(g GroupConfig) []string {
		l := append([]string{g.VIP4, g.VIP6}, g.MoreVIP4...)
		l = append(l, g.MoreVIP6...)
		return append(l, g.ExtraVIPs...)
	}
	parse := func(v string) (netip.Addr, bool) {
		if v == "" {
			return netip.Addr{}, false
		}
		if p, err := netip.ParsePrefix(v); err == nil {
			return p.Addr().Unmap(), true
		}
		if a, err := netip.ParseAddr(v); err == nil {
			return a.Unmap(), true
		}
		return netip.Addr{}, false
	}
	if cur != nil {
		for _, g := range cur.Groups {
			for _, v := range list(g) {
				if a, ok := parse(v); ok {
					had[a] = true
				}
			}
		}
	}
	var owners map[netip.Addr]string
	var bad []string
	for _, g := range next.Groups {
		for _, v := range list(g) {
			a, ok := parse(v)
			if !ok || had[a] {
				continue
			}
			if owners == nil {
				owners = nodeAddrOwners(cl)
			}
			if who, hit := owners[a]; hit {
				bad = append(bad, fmt.Sprintf("%s is an address of %s", a, who))
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("a shared (or anycast) address must be one no node already uses: %s. Pick a free address in the subnet", bad[0])
}
