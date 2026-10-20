package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cluster-wide statistics and host load: the Monitor ▸ Statistics and Monitor ▸ Host pages can show the cluster as one,
// with every node's numbers added together (the "Cluster" entry of the Node menu, `--stats --cluster`, `--host --cluster`).
//
// Each node answers for itself as always; the node asked fans out (itself directly, the others through the cluster relay
// that the Node menu already uses), over one absolute time range, and the answers are merged by the pure functions below.

// ClusterInfo says which nodes a cluster-wide answer adds together and which could not answer.
type ClusterInfo struct {
	Nodes []ClusterNodeInfo `json:"nodes"`
}

// ClusterNodeInfo is one node's part in a cluster-wide answer.
type ClusterNodeInfo struct {
	Name  string `json:"name"`
	Addr  string `json:"addr"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

const clusterStatsTimeout = 12 * time.Second

// clusterPart is one node's answer.
type clusterPart[T any] struct {
	Name, Addr string
	R          *T
}

// clusterTarget is a node a cluster-wide request goes to.
type clusterTarget struct {
	Name, Addr string
	Self       bool
	Err        string // why it cannot be asked (not reachable)
}

// clusterTargets lists the cluster's nodes, this node first as the cluster view has it, named by host name (or by
// address when two nodes share a name, as the Node menu does).  Not clustered: this node alone.
func clusterTargets(m *Mgmt) []clusterTarget {
	var peers []PeerView
	if m.cl != nil && m.cl.Enabled() {
		peers = m.cl.View().Peers
	}
	if len(peers) == 0 {
		return []clusterTarget{{Name: "this node", Self: true}}
	}
	same := map[string]int{}
	for _, p := range peers {
		same[p.Hostname]++
	}
	var out []clusterTarget
	for _, p := range peers {
		t := clusterTarget{Name: p.Hostname, Addr: p.Addr, Self: p.Self}
		if t.Name == "" || same[p.Hostname] > 1 {
			t.Name = p.Addr
		}
		if !p.Self && !p.Reachable {
			t.Err = "not reachable"
		}
		out = append(out, t)
	}
	return out
}

// clusterGather asks every reachable node for path (this one through local, the others through the relay) and decodes
// each answer into a T.  Nodes that could not answer are listed, with the reason, in the second result.
func clusterGather[T any](ctx context.Context, m *Mgmt, actor, path string, local func() *T) ([]clusterPart[T], []ClusterNodeInfo) {
	return clusterGatherReq(ctx, m, proxyReq{User: actor, Method: "GET", Path: path}, clusterStatsTimeout,
		func() (*T, error) { return local(), nil })
}

// clusterGatherReq is clusterGather for any request (a POST with a body, say) and a longer time allowed.  Every node,
// this one included, works at the same time, so a request that takes a while (a capture) takes that while for all of them.
func clusterGatherReq[T any](ctx context.Context, m *Mgmt, req proxyReq, timeout time.Duration, local func() (*T, error)) ([]clusterPart[T], []ClusterNodeInfo) {
	return clusterGatherSel(ctx, m, req, timeout, local, nil)
}

// clusterGatherSel is clusterGatherReq for the nodes keep accepts (nil: every node).  A cluster of one node is always asked.
func clusterGatherSel[T any](ctx context.Context, m *Mgmt, req proxyReq, timeout time.Duration, local func() (*T, error), keep func(PeerView) bool) ([]clusterPart[T], []ClusterNodeInfo) {
	var peers []PeerView
	if m.cl != nil && m.cl.Enabled() {
		peers = m.cl.View().Peers
	}
	// the host name, or the address when two nodes share a name (as the Node menu does)
	same := map[string]int{}
	for _, p := range peers {
		same[p.Hostname]++
	}
	label := func(p PeerView) string {
		if p.Hostname == "" || same[p.Hostname] > 1 {
			return p.Addr
		}
		return p.Hostname
	}
	type slot struct {
		part *clusterPart[T]
		info ClusterNodeInfo
		self bool
	}
	slots := make([]slot, 0, len(peers)+1)
	if len(peers) == 0 {
		slots = append(slots, slot{info: ClusterNodeInfo{Name: "this node"}, self: true})
	}
	for _, p := range peers {
		if keep != nil && !keep(p) {
			continue
		}
		sl := slot{info: ClusterNodeInfo{Name: label(p), Addr: p.Addr}, self: p.Self}
		if !p.Self && !p.Reachable {
			sl.info.Error = "not reachable"
		}
		slots = append(slots, sl)
	}
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for i := range slots {
		s := &slots[i]
		if s.info.Error != "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.self {
				r, err := local()
				if err != nil {
					s.info.Error = err.Error()
					return
				}
				s.part, s.info.OK = &clusterPart[T]{Name: s.info.Name, Addr: s.info.Addr, R: r}, true
				return
			}
			resp, err := m.cl.Relay(ctx, s.info.Addr, req)
			if err != nil {
				s.info.Error = err.Error()
				return
			}
			var env struct {
				OK    bool            `json:"ok"`
				Data  json.RawMessage `json:"data"`
				Error string          `json:"error"`
			}
			if json.Unmarshal(resp.Body, &env) != nil || resp.Status != 200 || !env.OK {
				s.info.Error = firstNonEmpty(env.Error, fmt.Sprintf("answered %d", resp.Status))
				return
			}
			r := new(T)
			if err := json.Unmarshal(env.Data, r); err != nil {
				s.info.Error = "unreadable answer"
				return
			}
			s.part, s.info.OK = &clusterPart[T]{Name: s.info.Name, Addr: s.info.Addr, R: r}, true
		}()
	}
	wg.Wait()
	var parts []clusterPart[T]
	infos := make([]ClusterNodeInfo, 0, len(slots))
	for _, s := range slots {
		if s.part != nil {
			parts = append(parts, *s.part)
		}
		infos = append(infos, s.info)
	}
	return parts, infos
}

// clusterQStats is qstats.get for the whole cluster.
func (m *Mgmt) clusterQStats(ctx context.Context, actor string, from, to time.Time, f QFilter) (*QStatsResult, error) {
	return m.clusterQStatsGW(ctx, actor, from, to, f, 0)
}

// gatewayNodes accepts the cluster nodes that serve a gateway: every node but the ones removed from it.
func gatewayNodes(g *GroupConfig) func(PeerView) bool {
	return func(p PeerView) bool { return p.NodeID == "" || !containsStr(g.ExcludedNodes, p.NodeID) }
}

// clusterQStatsGW is clusterQStats for the nodes of one gateway (gid > 0), or every node (0).  Statistics are kept per node, not
// per gateway, so a node that serves several gateways counts all its traffic in each of them.
func (m *Mgmt) clusterQStatsGW(ctx context.Context, actor string, from, to time.Time, f QFilter, gid int) (*QStatsResult, error) {
	var keep func(PeerView) bool
	if gid > 0 {
		dc, _, err := m.LiveConfig()
		if err != nil {
			return nil, err
		}
		g, err := findGroup(dc, gid)
		if err != nil {
			return nil, err
		}
		keep = gatewayNodes(g)
	}
	q := url.Values{"from": {strconv.FormatInt(from.Unix(), 10)}, "to": {strconv.FormatInt(to.Unix(), 10)},
		"rcode": {f.Rcode}, "client": {f.Client}, "domain": {f.Domain}, "cmatch": {f.CMatch}, "dmatch": {f.DMatch}}
	parts, infos := clusterGatherSel(ctx, m, proxyReq{User: actor, Method: "GET", Path: "/api/qstats?" + q.Encode()}, clusterStatsTimeout,
		func() (*QStatsResult, error) { return qstats.Query(from, to, f), nil }, keep)
	if len(parts) == 0 {
		return nil, errors.New("no node answered")
	}
	return mergeQStats(parts, infos), nil
}

// clusterHost is host.get for the whole cluster.
func (m *Mgmt) clusterHost(ctx context.Context, actor string, from, to time.Time) (*HostResult, error) {
	return m.clusterHostGW(ctx, actor, from, to, 0)
}

// clusterHostGW is clusterHost for the nodes of one gateway (gid > 0), or every node (0).
func (m *Mgmt) clusterHostGW(ctx context.Context, actor string, from, to time.Time, gid int) (*HostResult, error) {
	var keep func(PeerView) bool
	if gid > 0 {
		dc, _, err := m.LiveConfig()
		if err != nil {
			return nil, err
		}
		g, err := findGroup(dc, gid)
		if err != nil {
			return nil, err
		}
		keep = gatewayNodes(g)
	}
	q := url.Values{"from": {strconv.FormatInt(from.Unix(), 10)}, "to": {strconv.FormatInt(to.Unix(), 10)}}
	parts, infos := clusterGatherSel(ctx, m, proxyReq{User: actor, Method: "GET", Path: "/api/host?" + q.Encode()}, clusterStatsTimeout,
		func() (*HostResult, error) { return hoststats.Query(from, to), nil }, keep)
	if len(parts) == 0 {
		return nil, errors.New("no node answered")
	}
	return mergeHost(parts, infos), nil
}

// ── merging ──────────────────────────────────────────────────────────────────

// commonStep is the point spacing most of the answers use (the larger on a tie).  An answer with another spacing cannot be
// added point by point, so it is left out (and said to be).  Each node derives the spacing from the same range, so this
// only matters at a boundary between two spacings and when the clocks differ.
func commonStep(steps []int) int {
	count := map[int]int{}
	best := 0
	for _, s := range steps {
		count[s]++
		if count[s] > count[best] || count[s] == count[best] && s > best {
			best = s
		}
	}
	return best
}

// span of the points of answers sharing a step: the first point's time, and how many points cover them all.
func commonSpan(starts []int64, lens []int, step int) (start int64, n int) {
	if len(starts) == 0 {
		return 0, 0
	}
	start = starts[0]
	end := starts[0] + int64(lens[0]*step)
	for i := range starts {
		if starts[i] < start {
			start = starts[i]
		}
		if e := starts[i] + int64(lens[i]*step); e > end {
			end = e
		}
	}
	n = int((end - start) / int64(step))
	if n > qsMaxPoints {
		n = qsMaxPoints
	}
	return start, n
}

func markExcluded(infos []ClusterNodeInfo, name, why string) {
	for i := range infos {
		if infos[i].Name == name && infos[i].OK {
			infos[i].OK, infos[i].Error = false, why
			return
		}
	}
}

func addGuard(dst *MemGuardInfo, g MemGuardInfo) {
	dst.Trims += g.Trims
	if g.Limit > dst.Limit {
		dst.Limit = g.Limit
	}
	if g.Last > dst.Last {
		dst.Last, dst.LastNote = g.Last, g.LastNote
	}
}

// addCounts adds every row of src to dst (by name), keeping a client's reverse-DNS names.
func addCounts(dst map[string]*NameCount, src []NameCount) {
	for _, e := range src {
		d := dst[e.Name]
		if d == nil {
			d = &NameCount{Name: e.Name}
			dst[e.Name] = d
		}
		d.Count += e.Count
		if e.Manual && !d.Manual { // a name given by hand wins over a reverse-DNS one another node found first
			d.Host, d.Hosts, d.Manual = e.Host, e.Hosts, true
		}
		if d.Host == "" {
			d.Host = e.Host
		}
		if len(d.Hosts) == 0 {
			d.Hosts = e.Hosts
		}
	}
}

// sortedCounts orders rows like topList (count, then name; "(others)" last) and keeps limit of them (0 keeps all).
func sortedCounts(m map[string]*NameCount, limit int) []NameCount {
	out := make([]NameCount, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		oi, oj := out[i].Name == qsOthers, out[j].Name == qsOthers
		if oi != oj {
			return oj
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// mergeQStats adds the statistics of several nodes together: the series point by point (they are aligned to the same
// step), the totals, and the top lists by name.  The distinct client count cannot be added (a client that asked two nodes
// is one client): it is the larger of the busiest node's count and the clients seen in the nodes' lists, a lower bound.
func mergeQStats(parts []clusterPart[QStatsResult], infos []ClusterNodeInfo) *QStatsResult {
	steps := make([]int, len(parts))
	for i, p := range parts {
		steps[i] = p.R.Step
	}
	step := commonStep(steps)
	use := parts[:0:0]
	for _, p := range parts {
		if p.R.Step == step {
			use = append(use, p)
		} else {
			markExcluded(infos, p.Name, "answered with another time resolution")
		}
	}
	starts, lens := make([]int64, len(use)), make([]int, len(use))
	for i, p := range use {
		starts[i], lens[i] = p.R.Start, len(p.R.Total)
	}
	start, n := commonSpan(starts, lens, step)
	r := &QStatsResult{Rcode: use[0].R.Rcode, Client: use[0].R.Client, Domain: use[0].R.Domain,
		Start: start, Step: step, From: use[0].R.From, To: use[0].R.To, Since: use[0].R.Since,
		Total: make([]uint64, n), NoError: make([]uint64, n), ServFail: make([]uint64, n), NXDomain: make([]uint64, n),
		Refused: make([]uint64, n), Other: make([]uint64, n), Updates: make([]uint64, n), ClientsN: make([]uint64, n),
		Types: []NameCount{}, Protos: []NameCount{}, Clients: []NameCount{}, Domains: []NameCount{}}
	types, protos, clients, domains := map[string]*NameCount{}, map[string]*NameCount{}, map[string]*NameCount{}, map[string]*NameCount{}
	maxClients := 0
	for _, p := range use {
		q := p.R
		if q.From < r.From {
			r.From = q.From
		}
		if q.To > r.To {
			r.To = q.To
		}
		if q.Since > r.Since {
			r.Since = q.Since // the cluster has been counting since the last node began
		}
		off := int((q.Start - start) / int64(step))
		add := func(dst, src []uint64) {
			for i, v := range src {
				if j := off + i; j >= 0 && j < len(dst) {
					dst[j] += v
				}
			}
		}
		add(r.Total, q.Total)
		add(r.NoError, q.NoError)
		add(r.ServFail, q.ServFail)
		add(r.NXDomain, q.NXDomain)
		add(r.Refused, q.Refused)
		add(r.Other, q.Other)
		add(r.Updates, q.Updates)
		s := q.Sums
		r.Sums.Total, r.Sums.NoError, r.Sums.ServFail = r.Sums.Total+s.Total, r.Sums.NoError+s.NoError, r.Sums.ServFail+s.ServFail
		r.Sums.NXDomain, r.Sums.Refused, r.Sums.Other = r.Sums.NXDomain+s.NXDomain, r.Sums.Refused+s.Refused, r.Sums.Other+s.Other
		r.Sums.Updates, r.Sums.UpdFail = r.Sums.Updates+s.Updates, r.Sums.UpdFail+s.UpdFail
		r.Sums.CacheHit, r.Sums.CacheMis = r.Sums.CacheHit+s.CacheHit, r.Sums.CacheMis+s.CacheMis
		if s.Clients > maxClients {
			maxClients = s.Clients
		}
		addGuard(&r.Guard, q.Guard)
		addCounts(types, q.Types)
		addCounts(protos, q.Protos)
		addCounts(clients, q.Clients)
		addCounts(domains, q.Domains)
	}
	r.Types = sortedCounts(types, 0)
	for _, name := range protoOrder {
		if e := protos[name]; e != nil {
			r.Protos = append(r.Protos, *e)
		}
	}
	nc, nd := qsKeepResult, qsKeepResult // a picked client or domain lists everything it has
	if parts[0].R.Domain != "" {
		nc = qsKeepPicked
	}
	if parts[0].R.Client != "" {
		nd = qsKeepPicked
	}
	r.Clients, r.Domains = sortedCounts(clients, nc), sortedCounts(domains, nd)
	seen := len(clients)
	if _, ok := clients[qsOthers]; ok {
		seen--
	}
	r.ClientsN = unionPerPoint(use, start, int64(step), n, r.ClientsN)
	if n > 0 && int(r.ClientsN[n-1]) > maxClients {
		maxClients = int(r.ClientsN[n-1])
	}
	r.Sums.Clients = maxClients
	if seen > r.Sums.Clients {
		r.Sums.Clients = seen
	}
	r.Cluster = &ClusterInfo{Nodes: infos}
	return r
}

// hostWeight is the weight of a node's percentage in the cluster's: its share of the capacity (cores, memory).
func hostWeight(v float64) float64 {
	if v <= 0 {
		return 1
	}
	return v
}

// mergeHost adds the host load of several nodes together.  Quantities that add (network rates, load averages, memory,
// swap and disk sizes, cores) are summed; percentages are those of the cluster's capacity taken as one machine: CPU
// weighted by cores, memory and filesystems by size, the disk-busy figure averaged.  A peak is the busiest node's (CPU,
// disk) or, for network rates, the sum of the nodes' peaks (an upper bound: they need not have been at the same moment).
func mergeHost(parts []clusterPart[HostResult], infos []ClusterNodeInfo) *HostResult {
	steps := make([]int, len(parts))
	for i, p := range parts {
		steps[i] = p.R.Step
	}
	step := commonStep(steps)
	use := parts[:0:0]
	for _, p := range parts {
		if p.R.Step == step {
			use = append(use, p)
		} else {
			markExcluded(infos, p.Name, "answered with another time resolution")
		}
	}
	starts, lens := make([]int64, len(use)), make([]int, len(use))
	for i, p := range use {
		starts[i], lens[i] = p.R.Start, len(p.R.CPU)
	}
	start, n := commonSpan(starts, lens, step)
	neg := func() []float64 {
		s := make([]float64, n)
		for i := range s {
			s[i] = -1
		}
		return s
	}
	r := &HostResult{Start: start, Step: step, From: use[0].R.From, To: use[0].R.To, Since: use[0].R.Since,
		CPU: neg(), CPUMax: neg(), Mem: neg(), IO: neg(), IOMax: neg(), Rx: neg(), RxMax: neg(), Tx: neg(), TxMax: neg(), FS: []HostFSSeries{}}

	// per-point accumulators
	type acc struct{ sum, w, max float64 }
	cpu, mem, io := make([]acc, n), make([]acc, n), make([]acc, n)
	rx, tx := make([]acc, n), make([]acc, n)
	fsW := map[string]float64{} // capacity of each mount, summed over the nodes that have it
	var mounts []string
	fsAcc := map[string][]acc{}
	fsTotal := func(p clusterPart[HostResult], mount string) float64 {
		for _, f := range p.R.Now.FS {
			if f.Mount == mount {
				return hostWeight(float64(f.Total))
			}
		}
		return 1
	}
	for _, p := range use {
		h := p.R
		if h.From < r.From {
			r.From = h.From
		}
		if h.To > r.To {
			r.To = h.To
		}
		if h.Since > r.Since {
			r.Since = h.Since
		}
		addGuard(&r.Guard, h.Guard)
		off := int((h.Start - start) / int64(step))
		cw, mw := hostWeight(float64(h.Now.Cores)), hostWeight(float64(h.Now.MemTotal))
		each := func(src []float64, f func(i int, v float64)) {
			for i, v := range src {
				if j := off + i; v >= 0 && j >= 0 && j < n {
					f(j, v)
				}
			}
		}
		each(h.CPU, func(j int, v float64) { cpu[j].sum += v * cw; cpu[j].w += cw })
		each(h.CPUMax, func(j int, v float64) { cpu[j].max = math.Max(cpu[j].max, v) })
		each(h.Mem, func(j int, v float64) { mem[j].sum += v * mw; mem[j].w += mw })
		each(h.IO, func(j int, v float64) { io[j].sum += v; io[j].w++ })
		each(h.IOMax, func(j int, v float64) { io[j].max = math.Max(io[j].max, v) })
		each(h.Rx, func(j int, v float64) { rx[j].sum += v; rx[j].w++ })
		each(h.RxMax, func(j int, v float64) { rx[j].max += v })
		each(h.Tx, func(j int, v float64) { tx[j].sum += v; tx[j].w++ })
		each(h.TxMax, func(j int, v float64) { tx[j].max += v })
		for _, s := range h.FS {
			w := fsTotal(p, s.Mount)
			if _, ok := fsAcc[s.Mount]; !ok {
				fsAcc[s.Mount] = make([]acc, n)
				mounts = append(mounts, s.Mount)
			}
			fsW[s.Mount] += w
			a := fsAcc[s.Mount]
			each(s.Pct, func(j int, v float64) { a[j].sum += v * w; a[j].w += w })
		}
	}
	for i := 0; i < n; i++ {
		if cpu[i].w > 0 {
			r.CPU[i], r.CPUMax[i] = round2(cpu[i].sum/cpu[i].w), round2(cpu[i].max)
		}
		if mem[i].w > 0 {
			r.Mem[i] = round2(mem[i].sum / mem[i].w)
		}
		if io[i].w > 0 {
			r.IO[i], r.IOMax[i] = round2(io[i].sum/io[i].w), round2(io[i].max)
		}
		if rx[i].w > 0 {
			r.Rx[i], r.RxMax[i] = round2(rx[i].sum), round2(rx[i].max)
		}
		if tx[i].w > 0 {
			r.Tx[i], r.TxMax[i] = round2(tx[i].sum), round2(tx[i].max)
		}
	}
	sort.Strings(mounts)
	for _, m := range mounts {
		s := HostFSSeries{Mount: m, Pct: neg()}
		for i, a := range fsAcc[m] {
			if a.w > 0 {
				s.Pct[i] = round2(a.sum / a.w)
			}
		}
		r.FS = append(r.FS, s)
	}

	// the latest sample, for the tiles and tables
	now := HostNow{NetUtil: -1, FS: []fsUsage{}, Ifaces: []IfaceNow{}}
	var cpuW, cpuS, ioS float64
	var ioN int
	for _, p := range use {
		h := p.R.Now
		if h.At == 0 {
			continue // no sample yet on that node
		}
		if h.At > now.At {
			now.At = h.At
		}
		now.Cores += h.Cores
		cw := hostWeight(float64(h.Cores))
		cpuS, cpuW = cpuS+h.CPU*cw, cpuW+cw
		for k := range now.Load {
			now.Load[k] += h.Load[k]
		}
		now.MemTotal, now.MemUsed = now.MemTotal+h.MemTotal, now.MemUsed+h.MemUsed
		now.SwapTotal, now.SwapUsed = now.SwapTotal+h.SwapTotal, now.SwapUsed+h.SwapUsed
		ioS, ioN = ioS+h.IO, ioN+1
		now.Rx, now.Tx = now.Rx+h.Rx, now.Tx+h.Tx
		if h.NetUtil > now.NetUtil {
			now.NetUtil = h.NetUtil
		}
		for _, f := range h.FS { // one row per node, named like the interfaces: the node can then be told from the others
			f.Mount = p.Name + " " + f.Mount
			now.FS = append(now.FS, f)
		}
		for _, i := range h.Ifaces {
			i.Name = p.Name + " " + i.Name
			now.Ifaces = append(now.Ifaces, i)
		}
	}
	if cpuW > 0 {
		now.CPU = cpuS / cpuW
	}
	if now.MemTotal > 0 {
		now.MemPct = 100 * float64(now.MemUsed) / float64(now.MemTotal)
	}
	if ioN > 0 {
		now.IO = ioS / float64(ioN)
	}
	r.Now = now
	r.Cluster = &ClusterInfo{Nodes: infos}
	return r
}

var _ = strings.TrimSpace

// unionPerPoint adds up, for every point, the clients the nodes first saw at or before it, each client once:
// the cluster's distinct clients so far.  A node that does not send its first-seen lists (an older version)
// leaves the point as the busiest single node's count.
func unionPerPoint(use []clusterPart[QStatsResult], start, step int64, n int, fallback []uint64) []uint64 {
	out := make([]uint64, n)
	seen := map[string]struct{}{}
	any := false
	for _, p := range use {
		if p.R.ClientsNew != nil {
			any = true
		}
	}
	for j := 0; j < n; j++ {
		for _, p := range use {
			i := j - int((p.R.Start-start)/step)
			if i >= 0 && i < len(p.R.ClientsNew) {
				for _, c := range p.R.ClientsNew[i] {
					seen[c] = struct{}{}
				}
			}
		}
		out[j] = uint64(len(seen))
	}
	if !any {
		return fallback
	}
	return out
}
