package main

import (
	"net/netip"
	"sort"
	"strings"
)

// Shared presentation logic for the CLI (--show-gateways) and the web GUI so
// both always agree on roles, ordering and vMACs.

type GatewayMember struct {
	IP        string `json:"ip"`
	Priority  int    `json:"priority"`
	Slot      int    `json:"slot"`
	Weight    int    `json:"weight"`
	Role      string `json:"role"`  // AGC | AFN | INIT
	State     string `json:"state"` // engine state for this node; active|expired for peers
	AgeMS     int64  `json:"age_ms"`
	Local     bool   `json:"local"`
	Preempt   bool   `json:"preempt"`
	VMAC      string `json:"vmac"` // the virtual MAC, or in real-MAC mode the node's real MAC
	DNSListen bool   `json:"dns_listening"`
	Name      string `json:"name,omitempty"` // the node that has this address, when the cluster can say (see nodeNamesByIP)
}

type GatewayGroup struct {
	Name    string          `json:"name"`
	GroupID int             `json:"group_id"`
	AF      string          `json:"af"`
	VIP     string          `json:"vip"`
	AGC     string          `json:"agc"`
	Members []GatewayMember `json:"members"`
}

// buildGateways groups snapshot rows by (group, address family).
//
// Role: the AGC is the node the group has elected (agc_ip), or this node while
// it is ACTIVE; any other node holding an AFN slot is an AFN.  A peer's liveness
// string "active" says nothing about its role.
func buildGateways(rows []SnapshotRow) []GatewayGroup {
	type key struct {
		gid int
		af  string
	}
	idx := map[key]*GatewayGroup{}
	var order []key
	for _, r := range rows {
		k := key{r.GroupID, r.AF}
		g := idx[k]
		if g == nil {
			vip := r.VIP4
			if r.AF == "v6" {
				vip = r.VIP6
			}
			g = &GatewayGroup{GroupID: r.GroupID, AF: r.AF, VIP: vip}
			idx[k] = g
			order = append(order, k)
		}
		if g.AGC == "" && r.AGCIP != "" {
			g.AGC = r.AGCIP
		}
	}
	for _, r := range rows {
		g := idx[key{r.GroupID, r.AF}]
		role := "INIT"
		switch {
		case r.PeerIP == g.AGC || (r.Local && r.State == "active"):
			role = "AGC"
		case r.AfnID != 0:
			role = "AFN"
		}
		vmac := ""
		switch {
		case r.RealMACs:
			vmac = r.MAC // the node's real MAC address, when it is known (no virtual MACs in this mode)
		case r.AfnID != 0:
			vmac = vmacStr(r.GroupID, r.AfnID)
		}
		g.Members = append(g.Members, GatewayMember{
			IP: r.PeerIP, Priority: r.Priority, Slot: r.AfnID, Weight: r.Weight,
			Role: role, State: r.State, AgeMS: r.AgeMS, Local: r.Local, Preempt: r.Preempt,
			VMAC: vmac, DNSListen: r.DNSUp,
		})
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].gid != order[j].gid {
			return order[i].gid < order[j].gid
		}
		return order[i].af < order[j].af
	})
	out := make([]GatewayGroup, 0, len(order))
	for _, k := range order {
		g := idx[k]
		sort.Slice(g.Members, func(i, j int) bool { return g.Members[i].IP < g.Members[j].IP })
		out = append(out, *g)
	}
	return out
}

// peersOnly returns remote neighbours (CLI --show-neighbors).
func peersOnly(rows []SnapshotRow) []SnapshotRow {
	out := []SnapshotRow{}
	for _, r := range rows {
		if !r.Local {
			out = append(out, r)
		}
	}
	return out
}

// sortRows orders snapshot rows by group, address family, then node address, so a table is stable between polls.
func sortRows(rows []SnapshotRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		if a.AF != b.AF {
			return a.AF < b.AF
		}
		return a.PeerIP < b.PeerIP
	})
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// nameGateways fills in each group's name from the configuration.
func nameGateways(gs []GatewayGroup, dc *DaemonConfig) {
	names := map[int]string{}
	for _, g := range dc.Groups {
		names[g.GroupID] = g.Name
	}
	for i := range gs {
		gs[i].Name = names[gs[i].GroupID]
	}
}

// normIP is an address in one spelling (no IPv4-in-IPv6 form), so the same address from two sources compares equal.
func normIP(a string) string {
	if x, err := netip.ParseAddr(a); err == nil {
		return x.Unmap().String()
	}
	return a
}

// nodeNamesByIP maps the addresses of the cluster's nodes to their names: the host name, or the cluster address when two
// nodes have one host name.  The addresses a node uses in the gateway protocol come first (they are exactly what the
// Gateways page lists); its interface addresses fill in what those do not cover (an IPv6 gateway address is link-local, a
// node that does not send its gateway addresses yet still has its IPv4 ones here).
func nodeNamesByIP(peers []PeerView) map[string]string {
	same := map[string]int{}
	for _, p := range peers {
		same[p.Hostname]++
	}
	name := func(p PeerView) string {
		if p.Hostname == "" || same[p.Hostname] > 1 {
			return p.Addr
		}
		return p.Hostname
	}
	out := map[string]string{}
	for _, p := range peers {
		for _, ip := range p.GwIPs {
			if _, ok := out[normIP(ip)]; !ok { // the first node to claim an address keeps it (the list has this node first)
				out[normIP(ip)] = name(p)
			}
		}
	}
	for _, p := range peers {
		for _, ip := range p.IPs {
			if _, ok := out[normIP(ip)]; !ok {
				out[normIP(ip)] = name(p)
			}
		}
	}
	return out
}

// nameMembers puts each member's node name on the rows (names maps addresses to node names).
func nameMembers(gs []GatewayGroup, names map[string]string) {
	for i := range gs {
		for j := range gs[i].Members {
			gs[i].Members[j].Name = names[normIP(gs[i].Members[j].IP)]
		}
	}
}
