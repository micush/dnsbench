package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// What a cluster replicates.  Gateway groups are the same group on every
// member (same VIP, key, timers), but each node has its own interface,
// priority, weight, preempt flag and unicast neighbours — those are local and
// never replicated.  The dns block is shared as a whole; web, cluster and
// log_level are local.

// SharedGroup is the replicated part of a GroupConfig.
type SharedGroup struct {
	// Name is omitted when empty so a cluster with no names keeps the same hash
	// as before (members on different versions during a rolling update agree).
	Name     string   `json:"name,omitempty"`
	GroupID  int      `json:"group_id"`
	VIP4     string   `json:"vip4"`
	VIP6     string   `json:"vip6"`
	LBMethod LBMethod `json:"lb_method"`
	HelloMS  int      `json:"hello_ms"`
	HoldMS   int      `json:"hold_ms"`
	MaxAFNs  int      `json:"max_afns"`
	Key      string   `json:"key"`
	DNSProxy bool     `json:"dns_proxy"`
	// ExtraVIPs are the anycast addresses (omitted when none, so the hash of a
	// cluster without any is unchanged).
	ExtraVIPs []string `json:"extra_vips,omitempty"`
	// MoreVIP4 / MoreVIP6 are the further shared addresses in the subnet (omitted when none: the hash is unchanged).
	MoreVIP4 []string `json:"more_vip4,omitempty"`
	MoreVIP6 []string `json:"more_vip6,omitempty"`
	// Neighbors are the unicast addresses of every node of the gateway, this one included (a node skips its own);
	// omitted when empty (multicast), so the hash of a multicast cluster is unchanged.
	Neighbors []string `json:"neighbors,omitempty"`
	// PausedVIPs are the anycast addresses paused on every node (omitted when none: the hash is unchanged).
	PausedVIPs []string `json:"paused_vips,omitempty"`
	// PausedAll: the gateway is paused on every node (omitted when not).
	PausedAll bool `json:"paused_all,omitempty"`
	// RealMACs: the gateway runs without virtual MACs on every node (omitted when not, so the hash is unchanged).
	RealMACs bool `json:"real_macs,omitempty"`
	// ExcludedNodes are the nodes (by node ID) removed from the gateway (omitted when none: the hash is unchanged).
	ExcludedNodes []string `json:"excluded_nodes,omitempty"`
	// DNS is the group's own upstream pool (nil: it uses the shared one).
	DNS *DNSConfig `json:"dns,omitempty"`
}

// SharedConfig is the replicated document.
type SharedConfig struct {
	DNS    DNSConfig     `json:"dns"`
	Groups []SharedGroup `json:"groups"`
}

// GroupSeed carries the primary's local values for a group, used only to
// initialise a group a replica doesn't have yet.
type GroupSeed struct {
	GroupID   int    `json:"group_id"`
	Interface string `json:"interface"`
	Priority  int    `json:"priority"`
	Weight    int    `json:"weight"`
	Preempt   bool   `json:"preempt"`
}

func sharedOf(dc *DaemonConfig) SharedConfig {
	sc := SharedConfig{DNS: dc.DNS, Groups: []SharedGroup{}}
	if sc.DNS.Servers == nil {
		sc.DNS.Servers = []string{}
	}
	if sc.DNS.Queries == nil {
		sc.DNS.Queries = []DNSQuery{}
	}
	sc.DNS = cloneDNS(sc.DNS)
	for _, g := range dc.Groups {
		sc.Groups = append(sc.Groups, SharedGroup{
			GroupID: g.GroupID, Name: g.Name, VIP4: g.VIP4, VIP6: g.VIP6, LBMethod: g.LBMethod,
			HelloMS: g.HelloMS, HoldMS: g.HoldMS, MaxAFNs: g.MaxAFNs, Key: g.Key, DNSProxy: g.DNSProxy,
			DNS: cloneDNSPtr(g.DNS), ExtraVIPs: nonEmpty(g.ExtraVIPs), MoreVIP4: nonEmpty(g.MoreVIP4), MoreVIP6: nonEmpty(g.MoreVIP6), PausedVIPs: nonEmpty(g.PausedVIPs), PausedAll: g.PausedAll, Neighbors: nonEmpty(g.Neighbors), RealMACs: g.RealMACs, ExcludedNodes: nonEmpty(g.ExcludedNodes),
		})
	}
	sort.Slice(sc.Groups, func(i, j int) bool { return sc.Groups[i].GroupID < sc.Groups[j].GroupID })
	return sc
}

func seedsOf(dc *DaemonConfig) []GroupSeed {
	var out []GroupSeed
	for _, g := range dc.Groups {
		out = append(out, GroupSeed{g.GroupID, g.Interface, g.Priority, g.Weight, g.Preempt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GroupID < out[j].GroupID })
	return out
}

func (s SharedConfig) hash() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s SharedConfig) Validate() error {
	dc := newDaemonConfig()
	dc.Groups = nil
	for _, g := range s.Groups {
		gc := defaultGroup()
		applySharedGroup(&gc, g)
		dc.Groups = append(dc.Groups, gc)
	}
	dc.DNS = s.DNS
	return dc.Validate()
}

func applySharedGroup(gc *GroupConfig, g SharedGroup) {
	gc.GroupID, gc.Name, gc.VIP4, gc.VIP6, gc.LBMethod = g.GroupID, g.Name, g.VIP4, g.VIP6, g.LBMethod
	gc.HelloMS, gc.HoldMS, gc.MaxAFNs, gc.Key, gc.DNSProxy = g.HelloMS, g.HoldMS, g.MaxAFNs, g.Key, g.DNSProxy
	gc.DNS = cloneDNSPtr(g.DNS)
	gc.ExtraVIPs = append([]string(nil), g.ExtraVIPs...)
	gc.MoreVIP4 = append([]string(nil), g.MoreVIP4...)
	gc.MoreVIP6 = append([]string(nil), g.MoreVIP6...)
	gc.PausedVIPs = append([]string(nil), g.PausedVIPs...)
	gc.PausedAll = g.PausedAll
	gc.RealMACs = g.RealMACs
	gc.ExcludedNodes = append([]string(nil), g.ExcludedNodes...)
	gc.Neighbors = append([]string{}, g.Neighbors...)
}

// nonEmpty returns a copy of l, or nil when it is empty (keeps omitempty and hashes stable).
func nonEmpty(l []string) []string {
	if len(l) == 0 {
		return nil
	}
	return append([]string(nil), l...)
}

// mergeShared overwrites the shared parts of dc with sh, keeping local fields.
// Groups missing locally are created (initialised from seeds); local groups
// absent from sh are removed.  It reports whether dc changed.
func mergeShared(dc *DaemonConfig, sh SharedConfig, seeds []GroupSeed) (bool, error) {
	before := sharedOf(dc).hash()
	beforeGroups := len(dc.Groups)
	byID := map[int]int{}
	for i, g := range dc.Groups {
		byID[g.GroupID] = i
	}
	seedBy := map[int]GroupSeed{}
	for _, s := range seeds {
		seedBy[s.GroupID] = s
	}
	want := map[int]bool{}
	for _, g := range sh.Groups {
		want[g.GroupID] = true
		if i, ok := byID[g.GroupID]; ok {
			applySharedGroup(&dc.Groups[i], g)
			continue
		}
		gc := defaultGroup()
		applySharedGroup(&gc, g)
		if s, ok := seedBy[g.GroupID]; ok {
			gc.Interface, gc.Priority, gc.Weight, gc.Preempt = s.Interface, s.Priority, s.Weight, s.Preempt
		}
		dc.Groups = append(dc.Groups, gc)
	}
	kept := dc.Groups[:0]
	for _, g := range dc.Groups {
		if want[g.GroupID] {
			kept = append(kept, g)
		}
	}
	dc.Groups = kept
	dc.DNS = sh.DNS
	if err := dc.Validate(); err != nil {
		return false, err
	}
	return sharedOf(dc).hash() != before || len(dc.Groups) != beforeGroups, nil
}

// cloneDNS copies a DNSConfig deeply enough that two configs never share
// slices or maps (Validate normalises in place).
func cloneDNS(d DNSConfig) DNSConfig {
	d.Servers = append([]string{}, d.Servers...)
	d.Queries = append([]DNSQuery{}, d.Queries...)
	if len(d.FallbackServers) > 0 {
		d.FallbackServers = append([]string{}, d.FallbackServers...)
	} else {
		d.FallbackServers = nil
	}
	d.AllowedClients = append([]string(nil), d.AllowedClients...)
	if len(d.AllowedClients) == 0 {
		d.AllowedClients = nil
	}
	d.Policy = clonePolicy(d.Policy)
	d.SortList = append([]string(nil), d.SortList...)
	if len(d.SortList) == 0 {
		d.SortList = nil
	}
	d.ClientExempt = append([]string(nil), d.ClientExempt...)
	if len(d.ClientExempt) == 0 {
		d.ClientExempt = nil
	}
	d.PausedServers = append([]string(nil), d.PausedServers...)
	if len(d.PausedServers) == 0 {
		d.PausedServers = nil
	}
	d.PausedQueries = append([]string(nil), d.PausedQueries...)
	if len(d.PausedQueries) == 0 {
		d.PausedQueries = nil
	}
	if d.ServerQueries != nil {
		m := make(map[string][]DNSQuery, len(d.ServerQueries))
		for k, v := range d.ServerQueries {
			m[k] = append([]DNSQuery{}, v...)
		}
		d.ServerQueries = m
	}
	if len(d.ServerNames) > 0 {
		m := make(map[string]string, len(d.ServerNames))
		for k, v := range d.ServerNames {
			m[k] = v
		}
		d.ServerNames = m
	} else {
		d.ServerNames = nil
	}
	if d.LB != nil {
		l := *d.LB
		d.LB = &l
	}
	return d
}

func cloneDNSPtr(d *DNSConfig) *DNSConfig {
	if d == nil {
		return nil
	}
	c := cloneDNS(*d)
	return &c
}
