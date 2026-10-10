package main

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"
)

// The canvas is a picture of the configuration: one circle per gateway group
// (its shared address), a square under it per DNS server, and a trapezoid under
// each square per domain that server must answer.  Both the web GUI and the
// CLI (--canvas) show the same view built here, with the same colours:
//
//	ok    green   working
//	warn  yellow  degraded (works, but something underneath it does not)
//	bad   red     down
//	idle  grey    not known yet (not applied, or not probed yet)
//
// A blue line from the gateway to a server (in_band) means spread is on and the server is within the
// band of the fastest one, so queries take turns across the blue ones.

type CanvasTest struct {
	Name   string  `json:"name"`
	Type   string  `json:"type"`
	Status string  `json:"status"`
	Detail string  `json:"detail"`
	MS     float64 `json:"ms"`
	Uptime *UpInfo `json:"uptime,omitempty"`
	Paused string  `json:"paused,omitempty"` // "node" or "all" when this probe domain is paused
}

type CanvasServer struct {
	Addr   string       `json:"addr"`
	Name   string       `json:"name,omitempty"` // optional label shown instead of the address
	Status string       `json:"status"`
	Detail string       `json:"detail"`
	MS     float64      `json:"ms"`
	Rank   int          `json:"rank"`
	InBand bool         `json:"in_band"` // takes its turn at the front under spread (a blue line)
	Tests  []CanvasTest `json:"tests"`
	Uptime *UpInfo      `json:"uptime,omitempty"`
	Paused string       `json:"paused,omitempty"` // "node" or "all" when the server is paused
}

// CanvasFamily is the state of one address family of a gateway (a gateway with
// both an IPv4 and an IPv6 address runs one engine per family).
type CanvasFamily struct {
	AF     string `json:"af"` // v4 | v6
	VIP    string `json:"vip"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type CanvasGateway struct {
	Name      string         `json:"name"`
	GroupID   int            `json:"group_id"`
	Interface string         `json:"interface"`
	VIP4      string         `json:"vip4"`
	VIP6      string         `json:"vip6"`
	Anycast   []AnycastState `json:"anycast,omitempty"`
	Status    string         `json:"status"`
	Detail    string         `json:"detail"`
	Families  []CanvasFamily `json:"families"`
	// Vmac is the last virtual-MAC test on this node (see vmactest.go); absent until one has run.
	Vmac *VmacResult `json:"vmac,omitempty"`
	ECS  bool        `json:"ecs"`
	// LB is the load-balancing settings this gateway runs with; LBOwn says they are its own rather than Settings'.
	LB      LBConfig       `json:"lb"`
	LBOwn   bool           `json:"lb_own"`
	Servers []CanvasServer `json:"servers"`
	// Fallback are the servers used only while every one of Servers is down; UsingFallback says they are in use now.
	Fallback      []string       `json:"fallback"`
	UsingFallback bool           `json:"using_fallback,omitempty"`
	Members       []CanvasMember `json:"members"`
	Paused        bool           `json:"paused"`
	// NodePaused says the pause comes from the whole node being paused, not from this gateway.
	NodePaused bool `json:"node_paused,omitempty"`
	// PausedScope says where this gateway is paused by its own setting: "all" (every node) or "node" (this one).
	PausedScope string `json:"paused_scope,omitempty"`
	// Excluded says this node has been removed from the gateway (shared setting); ExcludedNodes lists the node IDs removed.
	// ClusterStatus / ClusterDetail are the gateway's state for the whole cluster, from its nodes (the sidebar's dot): the same
	// whichever node is asked.  Absent without a cluster, where Status is it.
	ClusterStatus string `json:"cluster_status,omitempty"`
	ClusterDetail string `json:"cluster_detail,omitempty"`
	// Via names the node whose picture of the gateway this is, when this node does not serve it itself (see viaServing).
	Via           string   `json:"via,omitempty"`
	ViaAddr       string   `json:"via_addr,omitempty"`
	Excluded      bool     `json:"excluded,omitempty"`
	ExcludedNodes []string `json:"excluded_nodes,omitempty"`
	Uptime        *UpInfo  `json:"uptime,omitempty"`
	// Nodes are the cluster's nodes and how each stands for this gateway (empty without a cluster); this node first.
	Nodes []CanvasNode `json:"nodes,omitempty"`
}

// CanvasMember is one cluster node currently serving the gateway: it holds an
// address-forwarding slot (AFN) and is still sending hellos.
type CanvasMember struct {
	IP     string `json:"ip"`
	Local  bool   `json:"local"`
	AGC    bool   `json:"agc"`
	AfnID  int    `json:"afn_id"`
	Weight int    `json:"weight"`
	AFs    string `json:"afs"`
}

// membersOf lists the nodes serving group gid, one entry per node even when
// both address families are running.
func membersOf(rows []SnapshotRow, gid int) []CanvasMember {
	out := []CanvasMember{}
	idx := map[string]int{}
	for _, r := range rows {
		if r.GroupID != gid || r.AfnID == 0 || r.State == "expired" || r.PeerIP == "" {
			continue
		}
		i, ok := idx[r.PeerIP]
		if !ok {
			i = len(out)
			idx[r.PeerIP] = i
			out = append(out, CanvasMember{IP: r.PeerIP, Local: r.Local, AfnID: r.AfnID, Weight: r.Weight})
		}
		if out[i].AFs != "" {
			out[i].AFs += "+"
		}
		out[i].AFs += r.AF
		if r.AGCIP == r.PeerIP {
			out[i].AGC = true
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].IP < out[b].IP })
	return out
}

// buildCanvas combines the running configuration with what the engines and
// the probe pools currently report.
func buildCanvas(dc *DaemonConfig, rows []SnapshotRow, pools []poolInfo) []CanvasGateway {
	out := make([]CanvasGateway, 0, len(dc.Groups))
	for i := range dc.Groups {
		g := &dc.Groups[i]
		cg := CanvasGateway{Name: g.Name, GroupID: g.GroupID, Interface: g.Interface, VIP4: g.VIP4, VIP6: g.VIP6,
			Servers: []CanvasServer{}, Fallback: []string{}, Families: []CanvasFamily{}, Members: membersOf(rows, g.GroupID), Paused: g.Paused, NodePaused: dc.NodePaused, ExcludedNodes: append([]string(nil), g.ExcludedNodes...)}

		stats := map[string]ServerStat{}
		var cfg DNSConfig
		{
			var key int
			key, cfg = dc.poolFor(g)
			for _, p := range pools {
				if p.Key == key {
					for _, s := range p.Pool.Snapshot() {
						stats[s.Addr] = s
					}
				}
			}
			for _, addr := range cfg.Servers {
				cs := CanvasServer{Addr: addr, Name: cfg.ServerNames[addr], Tests: []CanvasTest{}}
				st, probed := stats[addr]
				switch {
				case cfg.isPaused(addr):
					cs.Paused = cfg.serverPausedScope(addr)
					cs.Status, cs.Detail = "paused", "paused on "+pausedWhere(cs.Paused)+" — not queried until resumed"
				case cfg.allQueriesPaused(addr):
					cs.Status, cs.Detail = "bad", "every probe domain is paused, so the server counts as down — not queried until one is resumed"
				case !probed || (st.LastProbeAgo == 0 && len(st.Tests) == 0):
					cs.Status, cs.Detail = "idle", "waiting for the first probe"
					if g.Paused {
						cs.Detail = "gateway paused on this node — not probed"
					}
				case !st.Healthy:
					cs.Status, cs.Detail = "bad", orDefault(st.LastError, "not answering")
				default:
					bad := 0
					for _, t := range st.Tests {
						if !t.OK {
							bad++
						}
					}
					cs.MS, cs.Rank, cs.InBand = st.EWMAMS, st.Rank, st.InBand
					if bad > 0 {
						cs.Status, cs.Detail = "warn", fmt.Sprintf("%d test(s) failing, server still eligible", bad)
					} else {
						cs.Status, cs.Detail = "ok", fmt.Sprintf("healthy, %.2f ms", st.EWMAMS)
					}
				}
				if np, nt := cfg.pausedQueryCount(addr); cs.Status == "ok" && nt > 1 && nt-np == 1 {
					cs.Status, cs.Detail = "warn", fmt.Sprintf("only one probe domain is active (%d paused), so a failure elsewhere would not be noticed", np)
				}
				for _, q := range cfg.queriesFor(addr) {
					ct := CanvasTest{Name: q.Name, Type: q.Type, Status: "idle", Detail: "not tested yet"}
					if cs.Status == "paused" {
						ct.Status, ct.Detail = "paused", "server paused"
						cs.Tests = append(cs.Tests, ct)
						continue
					}
					if sc := cfg.queryPausedScope(addr, q); sc != "" {
						ct.Paused = sc
						ct.Status, ct.Detail = "paused", "paused on "+pausedWhere(sc)+" — not asked until resumed"
						cs.Tests = append(cs.Tests, ct)
						continue
					}
					for _, t := range st.Tests {
						if t.Name == q.Name && t.Type == q.Type {
							ct.MS = t.MS
							if t.OK {
								ct.Status, ct.Detail = "ok", fmt.Sprintf("answered in %.2f ms", t.MS)
							} else {
								ct.Status, ct.Detail = "bad", orDefault(t.Error, "failed")
							}
						}
					}
					cs.Tests = append(cs.Tests, ct)
				}
				cg.Servers = append(cg.Servers, cs)
			}
		}

		cg.Fallback = append(cg.Fallback, cfg.FallbackServers...)
		fbUp := false
		for _, addr := range cfg.FallbackServers {
			if st, ok := stats[addr]; ok && st.Healthy {
				fbUp = true
			}
		}
		// the gateway itself, one state per address family
		anyHealthy, anyDown, nPaused := false, false, 0
		for _, s := range cg.Servers {
			if s.Status == "paused" {
				nPaused++
			}
			if s.Status == "ok" || s.Status == "warn" {
				anyHealthy = true
			}
			if s.Status == "bad" {
				anyDown = true
			}
		}
		for _, f := range []struct{ af, vip, name string }{{"v4", g.VIP4, "IPv4"}, {"v6", g.VIP6, "IPv6"}} {
			if f.vip == "" {
				continue
			}
			running, active, answering := false, false, false
			state := "starting"
			for _, r := range rows {
				if r.GroupID != g.GroupID || r.AF != f.af {
					continue
				}
				running = true
				if !r.Local {
					continue
				}
				state = r.State
				if r.State == "active" || r.State == "forward" {
					active = true
				}
				if r.DNSUp {
					answering = true
				}
			}
			cf := CanvasFamily{AF: f.af, VIP: f.vip}
			switch {
			case !running:
				cf.Status, cf.Detail = "idle", "not running"
			case fbUp && !anyHealthy && (anyDown || nPaused > 0 || len(cg.Servers) == 0):
				cg.UsingFallback = true
				cf.Status, cf.Detail = "warn", "every DNS server is down or paused — answering from the fallback servers"
			case len(cg.Servers) > 0 && !anyHealthy && anyDown:
				cf.Status, cf.Detail = "bad", "no DNS server is healthy, so clients cannot be answered"
			case len(cg.Servers) > 0 && nPaused == len(cg.Servers):
				cf.Status, cf.Detail = "bad", "every DNS server is paused, so clients cannot be answered"
			case !active:
				cf.Status, cf.Detail = "warn", "this node is not serving the gateway yet (state: "+state+")"
			case !answering:
				cf.Status, cf.Detail = "warn", "the gateway is up but DNS is not answering yet"
			case len(cg.Servers) == 0:
				cf.Status, cf.Detail = "warn", "running, but no DNS server is configured yet, so clients cannot be answered"
			case anyDown:
				cf.Status, cf.Detail = "warn", "running, but some DNS servers are down"
			default:
				cf.Status, cf.Detail = "ok", "running and answering"
			}
			cg.Families = append(cg.Families, cf)
		}
		cg.Status, cg.Detail = combineFamilies(cg.Families)
		if g.Paused {
			cg.Status, cg.Detail = "paused", "Paused on this node — it is not serving; the other nodes carry on"
			if dc.NodePaused {
				cg.Detail = "This node is paused — it is not serving; the other nodes carry on"
			}
			if g.PausedAll {
				cg.PausedScope = "all"
				cg.Detail = "Paused on all nodes — none of them serves it until it is resumed"
			} else if !dc.NodePaused {
				cg.PausedScope = "node"
			}
			for i := range cg.Families {
				cg.Families[i].Status, cg.Families[i].Detail = "paused", "paused on this node"
			}
			if g.ExcludedHere { // removed from the gateway, which says more than the pause it is carried out as
				cg.Excluded, cg.PausedScope = true, ""
				cg.Detail = "This node was removed from the gateway — it does not serve it; the other nodes carry on"
				for i := range cg.Families {
					cg.Families[i].Detail = "this node was removed from the gateway"
				}
			}
		}
		cg.ECS = cfg.ECS
		cg.LB = cfg.lbValues()
		if g.DNS != nil {
			_, cg.LBOwn = g.DNS.ownLB()
		}
		out = append(out, cg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GroupID < out[j].GroupID })
	return out
}

// combineFamilies folds the per-family states into the gateway's own: the worst
// one wins, and a family that is not running while the other is counts as
// degraded.  The detail names the family when there is more than one.
func combineFamilies(fs []CanvasFamily) (status, detail string) {
	if len(fs) == 0 {
		return "idle", "no address"
	}
	rank := map[string]int{"ok": 0, "idle": 1, "warn": 2, "bad": 3}
	allIdle := true
	worst := "ok"
	for _, f := range fs {
		if f.Status != "idle" {
			allIdle = false
		}
		if rank[f.Status] > rank[worst] {
			worst = f.Status
		}
	}
	switch {
	case allIdle:
		worst = "idle"
	case worst == "idle":
		worst = "warn" // one family runs, the other does not
	}
	if len(fs) == 1 {
		return worst, fs[0].Detail
	}
	var parts []string
	for _, f := range fs {
		name := "IPv4"
		if f.AF == "v6" {
			name = "IPv6"
		}
		parts = append(parts, name+": "+f.Detail)
	}
	return worst, strings.Join(parts, "; ")
}

// config returns the supervisor's current configuration.
func (s *Supervisor) config() *DaemonConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dc
}

// canvasGroups is the picture as the GUI and CLI show it, with the uptimes noted.
func (s *StatusServer) canvasGroups() []CanvasGateway {
	groups := buildCanvas(s.sup.config(), s.snapshot(), s.sup.poolList())
	markWarming(groups, s.sup.warmingGroups())
	markOffnet(groups, s.sup.offnetGroups())
	cfg := s.sup.config()
	real := map[int]bool{}
	for _, g := range cfg.Groups {
		real[g.GroupID] = g.RealMACs
	}
	markVmac(groups, real)
	s.markAnycast(groups)
	s.markNodes(groups)
	s.up.annotate(groups, time.Now())
	return groups
}

func (s *StatusServer) canvasView() map[string]any {
	return map[string]any{"ok": true, "data": s.canvasGroups()}
}

// markAnycast lists each gateway's anycast addresses and whether this node
// announces them now.
func (s *StatusServer) markAnycast(groups []CanvasGateway) {
	cfg := s.sup.config()
	for i := range groups {
		st := s.sup.AnycastStates(groups[i].GroupID)
		if st == nil {
			for _, g := range cfg.Groups {
				if g.GroupID != groups[i].GroupID {
					continue
				}
				why := "gateway not running on this node"
				paused := g.pausedAnycast()
				for _, a := range g.ExtraVIPs {
					st = append(st, AnycastState{Addr: a, Reason: why, Paused: paused[a]})
				}
			}
		}
		switch {
		case cfg.BGP.Active():
			peers, ok := liveBGPPeers()
			for j := range st {
				if st[j].Up && st[j].Paused == "" {
					st[j].Status, st[j].Detail, st[j].BGP = anycastBGPStatus(st[j].Addr, cfg.BGP.Neighbors, peers, ok)
				}
			}
		case cfg.BGP.Configured(): // BGP is disabled here (Operate ▸ Anycast): nothing is announced
			for j := range st {
				if st[j].Up && st[j].Paused == "" {
					st[j].Status, st[j].Detail, st[j].BGP = "bad", "BGP is disabled on this node (Operate ▸ Anycast): the address is not announced", "disabled"
				}
			}
		}
		groups[i].Anycast = st
	}
}

// markWarming shows the gateways this node is holding back until their DNS
// servers answer, instead of an alarming "not running".
func markWarming(groups []CanvasGateway, warming map[int]bool) {
	const why = "starting — waiting for the DNS servers to answer before this node serves; the other nodes carry on"
	for i := range groups {
		if !warming[groups[i].GroupID] {
			continue
		}
		groups[i].Status, groups[i].Detail = "warn", why
		for j := range groups[i].Families {
			groups[i].Families[j].Status, groups[i].Families[j].Detail = "warn", why
		}
	}
}

// markOffnet shows the gateways this node does not run because it has no address
// in their subnet: grey, with the reason.  The other members are unaffected.
func markOffnet(groups []CanvasGateway, off map[int]string) {
	for i := range groups {
		why, ok := off[groups[i].GroupID]
		if !ok {
			continue
		}
		detail := "not running here — " + why + "; members on that subnet serve it"
		groups[i].Status, groups[i].Detail = "idle", detail
		for j := range groups[i].Families {
			groups[i].Families[j].Status, groups[i].Families[j].Detail = "idle", detail
		}
	}
}

// ── editing (CLI: --canvas-add / --canvas-del) ───────────────────────────────

type canvasEdit struct {
	Action string `json:"action"` // add | del | set | pause | resume | move
	Kind   string `json:"kind"`   // gateway | server | domain
	Group  int    `json:"group"`
	VIP    string `json:"vip"`  // IPv4 (or IPv6) address/prefix
	VIP6   string `json:"vip6"` // IPv6 address/prefix
	ECS    string `json:"ecs"`  // on | off | "" (unchanged)
	// RealMACs on | off | "" (unchanged): the gateway runs without virtual MACs on every node (a shared setting)
	RealMACs string `json:"real_macs"`
	ECSv4    int    `json:"ecs_v4"`
	ECSv6    int    `json:"ecs_v6"`
	// load balancing of the gateway's own pool: any of these makes the gateway use its own values (starting from what
	// it uses now); LB "settings" goes back to following Settings
	LB            string  `json:"lb"`
	Spread        string  `json:"spread"` // on | off | "" (unchanged)
	SpreadBand    int     `json:"spread_band"`
	DownPercent   int     `json:"down_percent"`
	FailThreshold int     `json:"fail_threshold"`
	MaxAttempts   int     `json:"max_attempts"`
	LatencyAlpha  float64 `json:"latency_alpha"`
	Interface     string  `json:"interface"`
	Label         string  `json:"label"`   // gateway or server name; "-" clears it
	Anycast       string  `json:"anycast"` // anycast addresses, comma separated; "-" clears them
	Address       string  `json:"address"` // one anycast address (--canvas-add|del|pause|resume anycast)
	Scope         string  `json:"scope"`   // pausing an anycast address: node (this one) or all (every node)
	Server        string  `json:"server"`
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	Pos           int     `json:"pos"` // --canvas-move: the new place in the list, 1 = first
	// Node names a cluster node for --canvas-add|del node: its address, host name or node ID.  CanvasEdit turns it into
	// nodeID and lists the cluster's node IDs in members, so the last member cannot be removed.
	Node    string `json:"node"`
	nodeID  string
	members string // the cluster's node IDs, comma separated (a slice would make canvasEdit incomparable)
}

// moveTo returns list with the item at index from moved to index to (both 0-based, to clamped).
func moveTo[T any](list []T, from, to int) []T {
	if to < 0 {
		to = 0
	}
	if to > len(list)-1 {
		to = len(list) - 1
	}
	out := append([]T(nil), list...)
	it := out[from]
	out = append(out[:from], out[from+1:]...)
	out = append(out[:to], append([]T{it}, out[to:]...)...)
	return out
}

// setVIPs assigns the shared addresses: vip may be IPv4 or IPv6, vip6 is IPv6.
// An empty argument leaves that family as it is.
func setVIPs(g *GroupConfig, vip, vip6 string) {
	for _, v := range []string{vip, vip6} {
		if v == "" {
			continue
		}
		if p, err := netip.ParsePrefix(v); err == nil && p.Addr().Is6() {
			g.VIP6 = v
		} else {
			g.VIP4 = v
		}
	}
}

// parseAnycastList reads --anycast: addresses separated by commas or spaces,
// "-" for none.
func parseAnycastList(v string) ([]string, error) {
	if strings.TrimSpace(v) == "-" {
		return nil, nil
	}
	var out []string
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		n, err := normalizeAnycast(f)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// setECS applies --ecs on|off and the prefix lengths to the group's own pool.
func setECS(dc *DaemonConfig, g *GroupConfig, e canvasEdit) (string, error) {
	if e.ECS == "" && e.ECSv4 == 0 && e.ECSv6 == 0 {
		return "", nil
	}
	if e.ECS != "" && e.ECS != "on" && e.ECS != "off" {
		return "", errors.New("--ecs must be on or off")
	}
	d := g.DNS
	if d == nil {
		c := ownPoolFrom(dc.DNS)
		g.DNS = &c
		d = g.DNS
	}
	if d.ServerQueries == nil {
		d.ServerQueries = map[string][]DNSQuery{}
	}
	var parts []string
	if e.ECS != "" {
		d.ECS = e.ECS == "on"
		parts = append(parts, "client subnet (ECS) "+e.ECS)
	}
	if e.ECSv4 > 0 {
		d.ECSPrefix4 = e.ECSv4
		parts = append(parts, fmt.Sprintf("ECS IPv4 prefix /%d", e.ECSv4))
	}
	if e.ECSv6 > 0 {
		d.ECSPrefix6 = e.ECSv6
		parts = append(parts, fmt.Sprintf("ECS IPv6 prefix /%d", e.ECSv6))
	}
	return strings.Join(parts, ", "), nil
}

// setLB applies --spread, --spread-band, --down-percent, --fail-threshold, --max-attempts, --latency-alpha (the gateway
// then uses its own values, starting from the ones it uses now) or --lb settings (back to following Settings).
func setLB(dc *DaemonConfig, g *GroupConfig, e canvasEdit) (string, error) {
	have := e.Spread != "" || e.SpreadBand != 0 || e.DownPercent != 0 || e.FailThreshold != 0 || e.MaxAttempts != 0 || e.LatencyAlpha != 0
	if e.LB != "" && e.LB != "settings" {
		return "", errors.New("--lb must be settings (to follow Settings again); giving any load-balancing value makes the gateway use its own")
	}
	if e.LB == "" && !have {
		return "", nil
	}
	if e.Spread != "" && e.Spread != "on" && e.Spread != "off" {
		return "", errors.New("--spread must be on or off")
	}
	if e.LB == "settings" {
		if have {
			return "", errors.New("--lb settings cannot be combined with load-balancing values")
		}
		if g.DNS != nil {
			g.DNS.LB = nil
			def := defaultDNS()
			g.DNS.setLBValues(def.lbValues()) // so an old pool at other values does not count as its own
		}
		return "load balancing follows Settings", nil
	}
	d := ownDNS(dc, g)
	cur, own := d.ownLB()
	if !own {
		cur = dc.DNS.lbValues()
	}
	var parts []string
	if e.Spread != "" {
		cur.Spread = e.Spread == "on"
		parts = append(parts, "spread "+e.Spread)
	}
	if e.SpreadBand != 0 {
		cur.SpreadBand = e.SpreadBand
		parts = append(parts, fmt.Sprintf("spread band %d%%", e.SpreadBand))
	}
	if e.DownPercent != 0 {
		cur.DownPercent = e.DownPercent
		parts = append(parts, fmt.Sprintf("down at %d%%", e.DownPercent))
	}
	if e.FailThreshold != 0 {
		cur.FailThreshold = e.FailThreshold
		parts = append(parts, fmt.Sprintf("%d failures before down", e.FailThreshold))
	}
	if e.MaxAttempts != 0 {
		cur.MaxAttempts = e.MaxAttempts
		parts = append(parts, fmt.Sprintf("%d servers tried at most", e.MaxAttempts))
	}
	if e.LatencyAlpha != 0 {
		cur.LatencyAlpha = e.LatencyAlpha
		parts = append(parts, fmt.Sprintf("latency smoothing %g", e.LatencyAlpha))
	}
	if err := cur.validate(); err != nil {
		return "", err
	}
	d.LB = &cur
	return "this gateway's own load balancing: " + strings.Join(parts, ", "), nil
}

// ownPoolFrom is the start of a gateway's own pool: a copy of the shared one, with the load-balancing values back at
// their defaults, so the gateway keeps following Settings for them until it is given its own.
func ownPoolFrom(shared DNSConfig) DNSConfig {
	c := cloneDNS(shared)
	c.LB = nil
	c.setLBValues(defaultDNS().lbValues())
	return c
}

// ownDNS makes sure the group has its own DNS pool, copying the shared one the
// first time.
func ownDNS(dc *DaemonConfig, g *GroupConfig) *DNSConfig {
	if g.DNS == nil {
		c := ownPoolFrom(dc.DNS)
		g.DNS = &c
	}
	if g.DNS.ServerQueries == nil {
		g.DNS.ServerQueries = map[string][]DNSQuery{}
	}
	g.DNSProxy = true
	return g.DNS
}

// setServerName gives a server a name, or clears it with "".
func setServerName(d *DNSConfig, addr, name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		delete(d.ServerNames, addr)
		if len(d.ServerNames) == 0 {
			d.ServerNames = nil
		}
		return
	}
	if d.ServerNames == nil {
		d.ServerNames = map[string]string{}
	}
	d.ServerNames[addr] = name
}

func findGroup(dc *DaemonConfig, id int) (*GroupConfig, error) {
	for i := range dc.Groups {
		if dc.Groups[i].GroupID == id {
			return &dc.Groups[i], nil
		}
	}
	return nil, fmt.Errorf("there is no gateway with group number %d (see --canvas)", id)
}

// applyCanvasEdit changes dc as asked.  The caller validates and saves.
func applyCanvasEdit(dc *DaemonConfig, e canvasEdit) (string, error) {
	switch e.Kind {
	case "gateway":
		switch e.Action {
		case "set":
			g, err := findGroup(dc, e.Group)
			if err != nil {
				return "", err
			}
			var did []string
			if e.VIP != "" || e.VIP6 != "" {
				setVIPs(g, e.VIP, e.VIP6)
				did = append(did, "address")
			}
			if e.Interface != "" {
				g.Interface = e.Interface
				did = append(did, "interface")
			}
			if e.Label != "" {
				if e.Label == "-" {
					g.Name = ""
				} else {
					g.Name = e.Label
				}
				did = append(did, "name")
			}
			if e.Anycast != "" {
				l, err := parseAnycastList(e.Anycast)
				if err != nil {
					return "", err
				}
				g.ExtraVIPs = l
				did = append(did, "anycast addresses")
			}
			switch e.RealMACs {
			case "":
			case "on", "off":
				g.RealMACs = e.RealMACs == "on"
				did = append(did, "real MAC addresses "+e.RealMACs)
			default:
				return "", errors.New("--real-macs must be on or off")
			}
			if msg, err := setECS(dc, g, e); err != nil {
				return "", err
			} else if msg != "" {
				did = append(did, msg)
			}
			if msg, err := setLB(dc, g, e); err != nil {
				return "", err
			} else if msg != "" {
				did = append(did, msg)
			}
			if len(did) == 0 {
				return "", errors.New("nothing to change: give --vip, --vip6, --interface, --label, --anycast, --ecs, --real-macs or a load-balancing setting")
			}
			return fmt.Sprintf("gateway %d: changed %s", e.Group, strings.Join(did, ", ")), nil
		case "add":
			if e.VIP == "" && e.VIP6 == "" {
				return "", errors.New("--vip is required (the shared address, e.g. 10.0.0.1/24; add --vip6 for an IPv6 address too)")
			}
			id := e.Group
			if id == 0 {
				used := map[int]bool{}
				for _, g := range dc.Groups {
					used[g.GroupID] = true
				}
				for id = 1; used[id] && id < 256; id++ {
				}
			}
			for _, g := range dc.Groups {
				if g.GroupID == id {
					return "", fmt.Errorf("gateway %d already exists", id)
				}
			}
			g := newGatewayGroup()
			g.GroupID = id
			g.VIP4, g.VIP6 = "", ""
			if e.Label != "-" {
				g.Name = e.Label
			}
			setVIPs(&g, e.VIP, e.VIP6)
			if e.Anycast != "" {
				l, err := parseAnycastList(e.Anycast)
				if err != nil {
					return "", err
				}
				g.ExtraVIPs = l
			}
			if e.Interface != "" {
				g.Interface = e.Interface
			} else if len(dc.Groups) > 0 {
				g.Interface = dc.Groups[0].Interface
			}
			d := defaultDNS()
			d.ServerQueries = map[string][]DNSQuery{}
			g.DNS = &d
			if e.ECS != "" || e.ECSv4 > 0 || e.ECSv6 > 0 {
				d := defaultDNS()
				d.ServerQueries = map[string][]DNSQuery{}
				g.DNS = &d
				if _, err := setECS(dc, &g, e); err != nil {
					return "", err
				}
			}
			dc.Groups = append(dc.Groups, g)
			if e.Server != "" { // draw the first server (and its first domain) in the same step
				if _, err := applyCanvasEdit(dc, canvasEdit{Action: "add", Kind: "server", Group: id, Server: e.Server, Name: e.Name, Type: e.Type}); err != nil {
					return "", err
				}
				return fmt.Sprintf("added gateway %d (%s) with DNS server %s", id, orDefault(e.VIP, e.VIP6), e.Server), nil
			}
			return fmt.Sprintf("added gateway %d (%s) — now add a DNS server with --canvas-add server --group %d --server ADDR --name DOMAIN", id, orDefault(e.VIP, e.VIP6), id), nil
		case "pause", "resume":
			g, err := findGroup(dc, e.Group)
			if err != nil {
				return "", err
			}
			scope := orDefault(e.Scope, "node") // this node unless said otherwise
			if scope != "node" && scope != "all" {
				return "", errors.New("--scope must be node (this node only) or all (every node)")
			}
			want := e.Action == "pause"
			flag, where := &g.Paused, "this node"
			if scope == "all" {
				flag, where = &g.PausedAll, "all nodes"
			}
			if *flag == want {
				return fmt.Sprintf("gateway %d is already %s on %s", e.Group, map[bool]string{true: "paused", false: "running"}[want], where), nil
			}
			*flag = want
			if want {
				return fmt.Sprintf("gateway %d paused on %s — it stops serving there; resume it when maintenance is done", e.Group, where), nil
			}
			return fmt.Sprintf("gateway %d resumed on %s", e.Group, where), nil
		case "del":
			g, err := findGroup(dc, e.Group)
			if err != nil {
				return "", err
			}
			kept := dc.Groups[:0]
			for _, x := range dc.Groups {
				if x.GroupID != g.GroupID {
					kept = append(kept, x)
				}
			}
			dc.Groups = kept
			return fmt.Sprintf("deleted gateway %d with all its DNS servers and domains", e.Group), nil
		}
	case "node":
		g, err := findGroup(dc, e.Group)
		if err != nil {
			return "", err
		}
		if e.nodeID == "" {
			return "", errors.New("--node is required: the node's address, host name or node ID (see --cluster)")
		}
		who := orDefault(e.Node, e.nodeID)
		out := containsStr(g.ExcludedNodes, e.nodeID)
		switch e.Action {
		case "del": // removed from the gateway
			if out {
				return fmt.Sprintf("node %s is already removed from gateway %d", who, e.Group), nil
			}
			if e.members != "" {
				left := 0
				for _, id := range strings.Split(e.members, ",") {
					if id != e.nodeID && !containsStr(g.ExcludedNodes, id) {
						left++
					}
				}
				if left == 0 {
					return "", fmt.Errorf("that would leave no node serving gateway %d — pause it on all nodes instead (--canvas-pause gateway --group %d --scope all)", e.Group, e.Group)
				}
			}
			g.ExcludedNodes = append(g.ExcludedNodes, e.nodeID)
			return fmt.Sprintf("node %s removed from gateway %d — it stops serving it and the other nodes carry on", who, e.Group), nil
		case "add": // back in the gateway
			if !out {
				return fmt.Sprintf("node %s is already serving gateway %d", who, e.Group), nil
			}
			kept := g.ExcludedNodes[:0:0]
			for _, id := range g.ExcludedNodes {
				if id != e.nodeID {
					kept = append(kept, id)
				}
			}
			g.ExcludedNodes = kept
			return fmt.Sprintf("node %s added back to gateway %d — it serves it again once its DNS servers answer", who, e.Group), nil
		}
		return "", fmt.Errorf("a node can be added to or removed from a gateway: --canvas-add|--canvas-del node --group N --node NODE")
	case "server":
		g, err := findGroup(dc, e.Group)
		if err != nil {
			return "", err
		}
		if e.Server == "" {
			return "", errors.New("--server is required")
		}
		addr, err := normalizeServer(e.Server)
		if err != nil {
			return "", err
		}
		d := ownDNS(dc, g)
		idx := -1
		for i, s := range d.Servers {
			if n, _ := normalizeServer(s); n == addr {
				idx = i
			}
		}
		switch e.Action {
		case "add":
			if idx >= 0 {
				return "", fmt.Errorf("server %s is already on gateway %d", addr, e.Group)
			}
			d.Servers = append(d.Servers, addr)
			d.ServerQueries[addr] = []DNSQuery{}
			if e.Name != "" {
				d.ServerQueries[addr] = []DNSQuery{{Name: e.Name, Type: strings.ToUpper(orDefault(e.Type, "A"))}}
			}
			if e.Label != "" && e.Label != "-" {
				setServerName(d, addr, e.Label)
			}
			return fmt.Sprintf("added server %s to gateway %d", addr, e.Group), nil
		case "move":
			if idx < 0 {
				return "", fmt.Errorf("server %s is not on gateway %d", addr, e.Group)
			}
			if e.Pos < 1 {
				return "", errors.New("--to is required: the new place in the row, 1 = leftmost")
			}
			d.Servers = moveTo(d.Servers, idx, e.Pos-1)
			return fmt.Sprintf("moved server %s to place %d of %d", addr, min(e.Pos, len(d.Servers)), len(d.Servers)), nil
		case "set":
			if idx < 0 {
				return "", fmt.Errorf("server %s is not on gateway %d", addr, e.Group)
			}
			if e.Label == "" {
				return "", errors.New("nothing to change: give --label (a name, or - to clear it)")
			}
			if e.Label == "-" {
				setServerName(d, addr, "")
				return fmt.Sprintf("server %s: name cleared", addr), nil
			}
			setServerName(d, addr, e.Label)
			return fmt.Sprintf("server %s: now named %q", addr, strings.TrimSpace(e.Label)), nil
		case "pause", "resume":
			if idx < 0 {
				return "", fmt.Errorf("server %s is not on gateway %d", addr, e.Group)
			}
			scope := orDefault(e.Scope, "all") // every node unless said otherwise (what pausing a server always did)
			if scope != "node" && scope != "all" {
				return "", errors.New("--scope must be node (this node only) or all (every node)")
			}
			list, where := &dc.PausedServersHere, "this node"
			if scope == "all" {
				list, where = &d.PausedServers, "all nodes"
			}
			have := containsStr(*list, addr)
			if e.Action == "pause" {
				if !have {
					*list = append(*list, addr)
				}
				return fmt.Sprintf("server %s paused on %s — no queries or probes there until you resume it", addr, where), nil
			}
			kept := []string{}
			for _, p := range *list {
				if p != addr {
					kept = append(kept, p)
				}
			}
			if len(kept) == 0 {
				kept = nil
			}
			*list = kept
			return fmt.Sprintf("server %s resumed on %s", addr, where), nil
		case "del":
			if idx < 0 {
				return "", fmt.Errorf("server %s is not on gateway %d", addr, e.Group)
			}
			n := len(d.queriesFor(d.Servers[idx]))
			d.Servers = append(d.Servers[:idx], d.Servers[idx+1:]...)
			delete(d.ServerQueries, addr)
			setServerName(d, addr, "")
			return fmt.Sprintf("deleted server %s and its %d domain(s)", addr, n), nil
		}
	case "fallback":
		g, err := findGroup(dc, e.Group)
		if err != nil {
			return "", err
		}
		if e.Server == "" {
			return "", errors.New("--server is required")
		}
		addr, err := normalizeServer(e.Server)
		if err != nil {
			return "", err
		}
		d := ownDNS(dc, g)
		idx := -1
		for i, s := range d.FallbackServers {
			if n, _ := normalizeServer(s); n == addr {
				idx = i
			}
		}
		switch e.Action {
		case "add":
			if idx >= 0 {
				return "", fmt.Errorf("%s is already a fallback server of gateway %d", addr, e.Group)
			}
			for _, s := range d.Servers {
				if n, _ := normalizeServer(s); n == addr {
					return "", fmt.Errorf("%s is already a normal server of gateway %d", addr, e.Group)
				}
			}
			d.FallbackServers = append(d.FallbackServers, addr)
			return fmt.Sprintf("added fallback server %s to gateway %d (used only while every other server is down)", addr, e.Group), nil
		case "del":
			if idx < 0 {
				return "", fmt.Errorf("%s is not a fallback server of gateway %d", addr, e.Group)
			}
			d.FallbackServers = append(d.FallbackServers[:idx], d.FallbackServers[idx+1:]...)
			if len(d.FallbackServers) == 0 {
				d.FallbackServers = nil
			}
			return fmt.Sprintf("removed fallback server %s", addr), nil
		}
	case "anycast":
		g, err := findGroup(dc, e.Group)
		if err != nil {
			return "", err
		}
		if e.Address == "" {
			return "", errors.New("--address is required")
		}
		addr, err := normalizeAnycast(e.Address)
		if err != nil {
			return "", err
		}
		idx := -1
		for i, a := range g.ExtraVIPs {
			if n, _ := normalizeAnycast(a); n == addr {
				idx = i
			}
		}
		switch e.Action {
		case "add":
			if idx >= 0 {
				return "", fmt.Errorf("anycast address %s is already on gateway %d", addr, e.Group)
			}
			g.ExtraVIPs = append(g.ExtraVIPs, addr)
			return fmt.Sprintf("added anycast address %s to gateway %d", addr, e.Group), nil
		case "move":
			if idx < 0 {
				return "", fmt.Errorf("anycast address %s is not on gateway %d", addr, e.Group)
			}
			if e.Pos < 1 {
				return "", errors.New("--to is required: the new place in the list, 1 = top")
			}
			g.ExtraVIPs = moveTo(g.ExtraVIPs, idx, e.Pos-1)
			return fmt.Sprintf("moved anycast address %s to place %d of %d", addr, min(e.Pos, len(g.ExtraVIPs)), len(g.ExtraVIPs)), nil
		case "pause", "resume":
			if idx < 0 {
				return "", fmt.Errorf("anycast address %s is not on gateway %d", addr, e.Group)
			}
			if e.Scope != "node" && e.Scope != "all" {
				return "", errors.New("--scope node|all is required: pause on this node only, or on every node")
			}
			list, where := &g.PausedVIPsHere, "this node"
			if e.Scope == "all" {
				list, where = &g.PausedVIPs, "all nodes"
			}
			have := containsStr(*list, addr)
			if e.Action == "pause" {
				if have {
					return fmt.Sprintf("anycast address %s is already paused on %s", addr, where), nil
				}
				*list = append(*list, addr)
				return fmt.Sprintf("anycast address %s paused on %s — it is withdrawn there until you resume it", addr, where), nil
			}
			if !have {
				return fmt.Sprintf("anycast address %s is not paused on %s", addr, where), nil
			}
			kept := []string{}
			for _, p := range *list {
				if p != addr {
					kept = append(kept, p)
				}
			}
			if len(kept) == 0 {
				kept = nil
			}
			*list = kept
			return fmt.Sprintf("anycast address %s resumed on %s", addr, where), nil
		case "del":
			if idx < 0 {
				return "", fmt.Errorf("anycast address %s is not on gateway %d", addr, e.Group)
			}
			g.ExtraVIPs = append(g.ExtraVIPs[:idx], g.ExtraVIPs[idx+1:]...)
			return fmt.Sprintf("removed anycast address %s from gateway %d", addr, e.Group), nil
		}
	case "domain":
		g, err := findGroup(dc, e.Group)
		if err != nil {
			return "", err
		}
		addr, err := normalizeServer(e.Server)
		if err != nil || e.Server == "" {
			return "", errors.New("--server is required (the server the domain belongs to)")
		}
		if e.Name == "" {
			return "", errors.New("--name is required (the domain, e.g. google.com)")
		}
		d := ownDNS(dc, g)
		found := false
		for _, s := range d.Servers {
			if n, _ := normalizeServer(s); n == addr {
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("server %s is not on gateway %d", addr, e.Group)
		}
		typ := strings.ToUpper(orDefault(e.Type, "A"))
		list, ok := d.ServerQueries[addr]
		if !ok {
			list = append([]DNSQuery{}, d.Queries...)
		}
		switch e.Action {
		case "pause", "resume":
			if e.Scope != "node" && e.Scope != "all" {
				return "", errors.New("--scope is required: node (this node only) or all (every node)")
			}
			var q *DNSQuery
			for i := range list {
				if strings.EqualFold(list[i].Name, e.Name) && (e.Type == "" || list[i].Type == typ) {
					q = &list[i]
					break
				}
			}
			if q == nil {
				return "", fmt.Errorf("%s is not tested on %s", e.Name, addr)
			}
			key := queryKey(addr, q.Name, q.Type)
			pl, where := &dc.PausedQueriesHere, "this node"
			if e.Scope == "all" {
				pl, where = &d.PausedQueries, "all nodes"
			}
			if e.Action == "pause" {
				if containsStr(*pl, key) {
					return "", fmt.Errorf("%s is already paused on %s", e.Name, where)
				}
				*pl = append(*pl, key)
				return fmt.Sprintf("%s paused on %s for server %s — not asked until resumed", q.Name, where, addr), nil
			}
			if !containsStr(*pl, key) {
				return "", fmt.Errorf("%s is not paused on %s", e.Name, where)
			}
			kept := []string{}
			for _, p := range *pl {
				if p != key {
					kept = append(kept, p)
				}
			}
			if len(kept) == 0 {
				kept = nil
			}
			*pl = kept
			return fmt.Sprintf("%s resumed on %s for server %s", q.Name, where, addr), nil
		case "add":
			for _, q := range list {
				if strings.EqualFold(q.Name, e.Name) && q.Type == typ {
					return "", fmt.Errorf("%s/%s is already tested on %s", e.Name, typ, addr)
				}
			}
			d.ServerQueries[addr] = append(list, DNSQuery{Name: e.Name, Type: typ})
			return fmt.Sprintf("server %s will now also be tested with %s/%s", addr, e.Name, typ), nil
		case "move":
			if e.Pos < 1 {
				return "", errors.New("--to is required: the new place under the server, 1 = top")
			}
			at := -1
			for i, q := range list {
				if strings.EqualFold(q.Name, e.Name) && (e.Type == "" || q.Type == typ) {
					at = i
					break
				}
			}
			if at < 0 {
				return "", fmt.Errorf("%s is not tested on %s", e.Name, addr)
			}
			d.ServerQueries[addr] = moveTo(list, at, e.Pos-1)
			return fmt.Sprintf("moved the %s test to place %d of %d under server %s", e.Name, min(e.Pos, len(list)), len(list), addr), nil
		case "del":
			var kept []DNSQuery
			for _, q := range list {
				if !(strings.EqualFold(q.Name, e.Name) && (e.Type == "" || q.Type == typ)) {
					kept = append(kept, q)
				}
			}
			if len(kept) == len(list) {
				return "", fmt.Errorf("%s is not tested on %s", e.Name, addr)
			}
			if kept == nil {
				kept = []DNSQuery{}
			}
			d.ServerQueries[addr] = kept
			return fmt.Sprintf("deleted the %s test from server %s", e.Name, addr), nil
		}
	}
	return "", fmt.Errorf("unknown canvas edit %q %q (use add, del or move with gateway, server, domain or anycast)", e.Action, e.Kind)
}

// CanvasEdit applies one edit to the saved configuration.
func (m *Mgmt) CanvasEdit(e canvasEdit, actor string) (string, error) {
	dc, _, err := m.LiveConfig()
	if err != nil {
		return "", err
	}
	if e.Action == "add" && e.Kind == "server" && e.Name == "" {
		return "", errors.New("a DNS server needs at least one domain to test it with: add --name example.com")
	}
	if e.Kind == "node" {
		if err := m.resolveNode(dc, &e); err != nil {
			return "", err
		}
	}
	msg, err := applyCanvasEdit(dc, e)
	if err != nil {
		return "", err
	}
	if err := m.PutConfig(dc, actor, "canvas: "+e.Action+" "+e.Kind); err != nil {
		return "", err
	}
	return msg, nil
}

// pausedWhere words a pause scope: "all nodes" or "this node".
func pausedWhere(scope string) string {
	if scope == "all" {
		return "all nodes"
	}
	return "this node"
}
