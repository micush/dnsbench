package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

type engineKey struct {
	group int
	af    AF
}

// Supervisor owns the running engines, the shared DNS pool, and hot reload.
//
// Live fields (no engine restart): priority, lb_method, weight, hello_ms,
// hold_ms, preempt, max_afns.  Restart fields: interface, vip4, vip6,
// group_id, key, neighbors, dns_proxy.  The "dns" block is applied live by
// swapping in a freshly probed pool.
type Supervisor struct {
	ctx context.Context

	reloadMu sync.Mutex // serialises Start/Reload/Stop
	mu       sync.Mutex // guards dc and engines
	pausedMu sync.RWMutex
	pausedAC map[int]map[string]string // gateway -> anycast address -> where it is paused; read by the anycast loops without s.mu
	dc       *DaemonConfig
	engines  map[engineKey]*Engine

	// Upstream pools.  Key 0 is the shared pool built from the top-level "dns"
	// block; any other key is a group id whose group has its own "dns" block.
	poolMu sync.RWMutex
	pools  map[int]*Pool
	dnss   map[int]DNSConfig
	keyOf  map[int]int // group id -> pool key

	// Groups waiting for their DNS servers to answer before the node starts
	// serving them (guarded by mu).  The value identifies one wait so a later
	// reload can cancel it.
	warming map[int]*warmup

	// Groups held back because this node has no address in the gateway's subnet
	// (guarded by mu); the value identifies one wait so a reload can cancel it.
	offnet map[int]*offnetWait

	// Anycast address sets of the running groups (guarded by mu).
	anycast map[int]*anycastSet
}

type offnetWait struct {
	why string
}

type warmup struct {
	since               time.Time
	every, floor, limit time.Duration // the settings in force when the wait began
}

// How long a node holds back from serving a gateway while its DNS servers are
// not all answering yet.  After warmFloor one answering server is enough; after
// warmMax it starts regardless (and shows red if nothing answers).
var (
	warmFloor = 10 * time.Second
	warmMax   = 60 * time.Second
	warmEvery = time.Second
)

func NewSupervisor(ctx context.Context, dc *DaemonConfig) *Supervisor {
	s := &Supervisor{ctx: ctx, dc: dc.effective(), engines: map[engineKey]*Engine{},
		pools: map[int]*Pool{}, dnss: map[int]DNSConfig{}, keyOf: map[int]int{},
		warming: map[int]*warmup{}, offnet: map[int]*offnetWait{}, anycast: map[int]*anycastSet{}}
	s.notePausedAnycast(s.dc)
	return s
}

// notePausedAnycast records where each anycast address is paused, for the anycast loops (which run while s.mu is held).
func (s *Supervisor) notePausedAnycast(dc *DaemonConfig) {
	m := map[int]map[string]string{}
	for _, g := range dc.Groups {
		if p := g.pausedAnycast(); len(p) > 0 {
			m[g.GroupID] = p
		}
	}
	s.pausedMu.Lock()
	s.pausedAC = m
	s.pausedMu.Unlock()
}

// poolFor returns the pool serving a group (nil if it has none).
func (s *Supervisor) poolFor(gid int) *Pool {
	s.poolMu.RLock()
	defer s.poolMu.RUnlock()
	k, ok := s.keyOf[gid]
	if !ok {
		return nil
	}
	return s.pools[k]
}

// groupName is the name given to a gateway ("" when it has none).
func (s *Supervisor) groupName(gid int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dc != nil {
		for i := range s.dc.Groups {
			if s.dc.Groups[i].GroupID == gid {
				return s.dc.Groups[i].Name
			}
		}
	}
	return ""
}

// dnsFor returns the DNS settings in force for a group.
func (s *Supervisor) dnsFor(gid int) DNSConfig {
	s.poolMu.RLock()
	defer s.poolMu.RUnlock()
	if k, ok := s.keyOf[gid]; ok {
		return s.dnss[k]
	}
	return defaultDNS()
}

// serverNames is the names given to the DNS servers of the pool key (0 is the shared pool, otherwise a gateway's own).
func (s *Supervisor) serverNames(key int) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dc == nil {
		return nil
	}
	if key != 0 {
		for i := range s.dc.Groups {
			if s.dc.Groups[i].GroupID == key {
				_, c := s.dc.poolFor(&s.dc.Groups[i])
				return c.ServerNames
			}
		}
	}
	return s.dc.DNS.ServerNames
}

type poolInfo struct {
	Key    int
	Groups []int
	Pool   *Pool
	Cfg    DNSConfig
}

// poolList returns the running pools, shared pool first.
func (s *Supervisor) poolList() []poolInfo {
	s.poolMu.RLock()
	defer s.poolMu.RUnlock()
	var out []poolInfo
	for k, p := range s.pools {
		pi := poolInfo{Key: k, Pool: p, Cfg: s.dnss[k]}
		for gid, kk := range s.keyOf {
			if kk == k {
				pi.Groups = append(pi.Groups, gid)
			}
		}
		sort.Ints(pi.Groups)
		out = append(out, pi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (s *Supervisor) newEngine(gc GroupConfig, af AF) *Engine {
	gid := gc.GroupID
	return NewEngine(gc, af,
		func() DNSConfig { return s.dnsFor(gid) },
		func() *Pool { return s.poolFor(gid) })
}

func (s *Supervisor) StartAll() {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	s.refreshPool(s.dc, false)
	s.mu.Lock()
	for _, gc := range s.dc.Groups {
		s.startGroupWhenReadyLocked(gc)
	}
	s.mu.Unlock()
}

func (s *Supervisor) StopAll() {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	s.mu.Lock()
	engines := make([]*Engine, 0, len(s.engines))
	for _, e := range s.engines {
		engines = append(engines, e)
	}
	s.engines = map[engineKey]*Engine{}
	s.warming = map[int]*warmup{}
	s.offnet = map[int]*offnetWait{}
	for gid := range s.anycast {
		s.stopAnycastLocked(gid)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range engines {
		wg.Add(1)
		go func() { defer wg.Done(); e.Stop() }()
	}
	wg.Wait()
	s.poolMu.Lock()
	old := s.pools
	s.pools, s.dnss, s.keyOf = map[int]*Pool{}, map[int]DNSConfig{}, map[int]int{}
	s.poolMu.Unlock()
	for _, p := range old {
		p.Stop()
	}
}

func (s *Supervisor) startGroupLocked(gc GroupConfig) {
	if gc.Paused {
		infof("Group %d is paused on this node — not starting", gc.GroupID)
		return
	}
	for _, af := range []AF{afIPv4, afIPv6} {
		if gc.wants(af) {
			e := s.newEngine(gc, af)
			s.engines[engineKey{gc.GroupID, af}] = e
			e.Start(s.ctx)
		}
	}
	s.startAnycastLocked(gc)
}

// startAnycastLocked holds and serves the gateway's anycast addresses (see anycast.go).
func (s *Supervisor) startAnycastLocked(gc GroupConfig) {
	if len(gc.ExtraVIPs) == 0 || s.anycast[gc.GroupID] != nil {
		return
	}
	gid := gc.GroupID
	a := newAnycastSet(gid, gc.ExtraVIPs, func() *Pool { return s.poolFor(gid) },
		func() int { return s.dnsFor(gid).ListenPort })
	a.dot = func() int { return s.dnsFor(gid).DoTPort }
	a.doh = func() int { return s.dnsFor(gid).DoHPort }
	a.paused = func() map[string]string { return s.pausedAnycast(gid) }
	s.anycast[gid] = a
	a.start(s.ctx)
}

func (s *Supervisor) stopAnycastLocked(gid int) {
	if a := s.anycast[gid]; a != nil {
		delete(s.anycast, gid)
		a.stop()
	}
}

// pausedAnycast is where each anycast address of the gateway is paused, from the live configuration.
func (s *Supervisor) pausedAnycast(gid int) map[string]string {
	s.pausedMu.RLock()
	defer s.pausedMu.RUnlock()
	return s.pausedAC[gid]
}

// AnycastStates lists the anycast addresses of a group and whether each is announced now.
func (s *Supervisor) AnycastStates(gid int) []AnycastState {
	s.mu.Lock()
	a := s.anycast[gid]
	s.mu.Unlock()
	if a == nil {
		return nil
	}
	st := a.state()
	paused := s.pausedAnycast(gid)
	for i := range st {
		st[i].Paused = paused[st[i].Addr]
	}
	return st
}

// AllAnycastStates lists every anycast address of every gateway with whether this
// node announces it now.  An address of a gateway that is not running here is
// listed as withdrawn.
// allCaches lists the answer caches of the running pools.
func (s *Supervisor) allCaches() []*respCache {
	s.poolMu.RLock()
	defer s.poolMu.RUnlock()
	var out []*respCache
	for _, p := range s.pools {
		if p != nil && p.cache != nil {
			out = append(out, p.cache)
		}
	}
	return out
}

func (s *Supervisor) AllAnycastStates() []AnycastState {
	var out []AnycastState
	at := map[string]int{} // address -> index in out
	rank := map[string]int{}
	for _, g := range s.config().Groups {
		if g.ExcludedHere {
			continue // this node was removed from the gateway: its anycast addresses are not this node's, so they are not listed
		}
		st := s.AnycastStates(g.GroupID)
		have := map[string]AnycastState{}
		for _, a := range st {
			have[a.Addr] = a
		}
		for _, addr := range g.ExtraVIPs {
			a, ok := have[addr]
			if !ok {
				why := "gateway not running on this node"
				if g.Paused {
					why = "gateway paused"
				}
				a = AnycastState{Addr: addr, Reason: why}
			}
			a.Paused = g.pausedAnycast()[addr]
			// one address may be carried by several gateways: it is shown once, as its best gateway has it (announced, else
			// one that is running and says why not, else one that is not running, else one that is paused)
			r := 1
			switch {
			case a.Up:
				r = 4
			case a.Paused == "" && !g.Paused && ok:
				r = 3
			case a.Paused == "" && !g.Paused:
				r = 2
			}
			if i, dup := at[addr]; dup {
				if r > rank[addr] {
					out[i], rank[addr] = a, r
				} else if a.Paused == "" && out[i].Paused != "" && r == rank[addr] {
					out[i].Paused = ""
				}
				continue
			}
			at[addr], rank[addr] = len(out), r
			out = append(out, a)
		}
	}
	return out
}

// startGroupWhenReadyLocked starts a gateway on this node, but a gateway that
// serves DNS only once its DNS servers are answering.  Starting at once joins
// the election and takes traffic (the AGC steers clients here) while the servers
// are still unprobed, so clients got SERVFAIL until the next probe round — an
// outage right after a resume or a restart even though other nodes were serving
// fine.  The wait runs in the background; a reload that pauses, restarts or
// removes the group cancels it.
func (s *Supervisor) startGroupWhenReadyLocked(gc GroupConfig) {
	if !gc.Paused {
		if ok, why := onGatewaySubnet(gc); !ok {
			w := &offnetWait{why: why}
			s.offnet[gc.GroupID] = w
			warnf("Group %d: %s — not starting here until it is", gc.GroupID, why)
			go s.waitForSubnet(gc.GroupID, w)
			return
		}
	}
	s.startAfterSubnetLocked(gc)
}

// startAfterSubnetLocked is the rest of the start: wait for the DNS servers, then go.
func (s *Supervisor) startAfterSubnetLocked(gc GroupConfig) {
	vmacAtStart(gc) // a background check that clients' replies can reach this node; no-op in tests
	if gc.Paused || s.dnsReady(gc.GroupID, 0, warmFloor) {
		s.startGroupLocked(gc)
		return
	}
	w := &warmup{since: time.Now(), every: warmEvery, floor: warmFloor, limit: warmMax}
	s.warming[gc.GroupID] = w
	infof("Group %d: waiting for its DNS servers to answer before serving", gc.GroupID)
	go s.warmThenStart(gc.GroupID, w)
}

// dnsReady reports whether the group's DNS pool can answer clients: every
// server answering, or — once waited is past warmFloor — at least one.
func (s *Supervisor) dnsReady(gid int, waited, floor time.Duration) bool {
	p := s.poolFor(gid)
	if p == nil || len(p.servers) == 0 {
		return true // nothing to wait for
	}
	healthy := len(p.Ranked())
	// every server that is meant to be used answering (the fallbacks count only when there is nothing else),
	// or — once waited is past the floor — anything that can answer, fallbacks included
	want := p.nNormal()
	if want == 0 {
		want = len(p.servers)
	}
	return (healthy == want && !p.UsingFallback()) || (healthy > 0 && waited >= floor) || (p.nNormal() == 0 && healthy == want)
}

func (s *Supervisor) warmThenStart(gid int, w *warmup) {
	t := time.NewTicker(w.every)
	defer t.Stop()
	for {
		if p := s.poolFor(gid); p != nil {
			ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
			p.ProbeNow(ctx) // do not wait for the pool's own schedule
			cancel()
		}
		s.mu.Lock()
		if s.warming[gid] != w || s.ctx.Err() != nil {
			s.mu.Unlock() // cancelled: paused, restarted, removed or stopping
			return
		}
		waited := time.Since(w.since)
		if s.dnsReady(gid, waited, w.floor) || waited >= w.limit {
			delete(s.warming, gid)
			if waited >= w.limit && !s.dnsReady(gid, waited, w.floor) {
				warnf("Group %d: its DNS servers still do not answer after %s — starting anyway", gid, waited.Round(time.Second))
			} else {
				infof("Group %d: DNS servers answer (%s) — starting", gid, waited.Round(100*time.Millisecond))
			}
			for i := range s.dc.Groups {
				if s.dc.Groups[i].GroupID == gid {
					s.startGroupLocked(s.dc.Groups[i])
				}
			}
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// waitForSubnet holds a gateway back while this node has no address in the
// gateway's subnet, and carries on (through the DNS warm-up) when it gets one.
// A node on another subnet is still a full cluster member; it just does not
// join an election it cannot hear, nor claim an address it cannot serve.
func (s *Supervisor) waitForSubnet(gid int, w *offnetWait) {
	t := time.NewTicker(offnetEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		if s.offnet[gid] != w || s.ctx.Err() != nil {
			s.mu.Unlock() // cancelled: paused, changed, removed or stopping
			return
		}
		for i := range s.dc.Groups {
			gc := s.dc.Groups[i]
			if gc.GroupID != gid {
				continue
			}
			if ok, _ := onGatewaySubnet(gc); ok {
				delete(s.offnet, gid)
				infof("Group %d: this node is on the gateway's subnet now — starting", gid)
				s.startAfterSubnetLocked(gc)
				s.mu.Unlock()
				return
			}
		}
		s.mu.Unlock()
	}
}

// offnetGroups maps the groups held back for being on the wrong subnet to the reason.
func (s *Supervisor) offnetGroups() map[int]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int]string{}
	for gid, w := range s.offnet {
		out[gid] = w.why
	}
	return out
}

// warmingGroups lists the groups held back while their DNS servers warm up.
func (s *Supervisor) warmingGroups() map[int]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int]bool{}
	for gid := range s.warming {
		out[gid] = true
	}
	return out
}

func (s *Supervisor) stopGroupLocked(gid int) []*Engine {
	delete(s.warming, gid) // a pending warm-up must not start a group that was stopped
	delete(s.offnet, gid)
	s.stopAnycastLocked(gid)
	var out []*Engine
	for _, af := range []AF{afIPv4, afIPv6} {
		k := engineKey{gid, af}
		if e := s.engines[k]; e != nil {
			delete(s.engines, k)
			out = append(out, e)
		}
	}
	return out
}

// refreshPool makes the running pools match the new config.  A changed pool
// is probed once (when preprobe is set) before it replaces the old one so there
// is no window of SERVFAILs.  It reports the groups whose listen port changed.
func (s *Supervisor) refreshPool(nu *DaemonConfig, preprobe bool) map[int]bool {
	want := map[int]DNSConfig{}
	keyOf := map[int]int{}
	for i := range nu.Groups {
		g := &nu.Groups[i]
		if g.Paused {
			continue
		}
		k, c := nu.poolFor(g)
		keyOf[g.GroupID] = k
		want[k] = c.live()
	}

	s.poolMu.RLock()
	oldPools, oldCfg, oldKey := s.pools, s.dnss, s.keyOf
	s.poolMu.RUnlock()

	next := map[int]*Pool{}
	for k, c := range want {
		if cur := oldPools[k]; cur != nil && reflect.DeepEqual(oldCfg[k], c) {
			next[k] = cur
			continue
		}
		p := NewPool(c)
		if cur := oldPools[k]; cur != nil && cur.cache != nil && p.cache != nil && cacheCarries(oldCfg[k], c) {
			p.cache = cur.cache // servers changed, not the cache: what it holds is still right (cachewarm.go)
		}
		if preprobe {
			most := 1
			for _, sv := range c.Servers {
				if n := len(c.queriesFor(sv)); n > most {
					most = n
				}
			}
			ctx, cancel := context.WithTimeout(s.ctx, c.probeTimeout()*time.Duration(most+1))
			p.ProbeNow(ctx)
			cancel()
		}
		p.Start()
		next[k] = p
		name := "shared"
		if k != 0 {
			name = "group " + itoa(k)
		}
		if c.TLSInsecure {
			warnf("dns: %s pool: tls_insecure is on — the certificates of tls:// and https:// servers are not checked", name)
		}
		infof("dns: %s pool started — %d server(s), down at %d%% of tests failing, %d policy row(s)%s", name, len(c.Servers), c.DownPercent, len(p.policy), map[bool]string{true: "", false: " (policy off)"}[c.PolicyOn])
	}

	changed := map[int]bool{}
	for gid, k := range keyOf {
		if ok, had := oldKey[gid]; had && (oldCfg[ok].ListenPort != want[k].ListenPort || oldCfg[ok].DoTPort != want[k].DoTPort || oldCfg[ok].DoHPort != want[k].DoHPort) {
			changed[gid] = true
		}
	}

	s.poolMu.Lock()
	s.pools, s.dnss, s.keyOf = next, want, keyOf
	s.poolMu.Unlock()

	for k, p := range oldPools {
		if next[k] != p {
			p.Stop()
		}
	}
	return changed
}

func (s *Supervisor) Reload(nu *DaemonConfig) {
	nu = nu.effective()
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	s.mu.Lock()
	cur := s.dc
	s.mu.Unlock()

	if !strings.EqualFold(nu.LogLevel, cur.LogLevel) {
		logLevel.Set(parseLevel(nu.LogLevel))
		infof("Log level → %s", nu.LogLevel)
	}

	s.notePausedAnycast(nu)
	portChanged := s.refreshPool(nu, true)

	oldGroups := map[int]*GroupConfig{}
	for i := range cur.Groups {
		oldGroups[cur.Groups[i].GroupID] = &cur.Groups[i]
	}
	newGroups := map[int]*GroupConfig{}
	for i := range nu.Groups {
		newGroups[nu.Groups[i].GroupID] = &nu.Groups[i]
	}

	type restartJob struct {
		gc      GroupConfig
		engines []*Engine
	}
	var restarts []restartJob
	var stopped []*Engine
	s.mu.Lock()
	for gid := range oldGroups {
		if _, ok := newGroups[gid]; !ok {
			infof("Group %d removed — stopping engines", gid)
			stopped = append(stopped, s.stopGroupLocked(gid)...)
		}
	}
	gids := make([]int, 0, len(newGroups))
	for gid := range newGroups {
		gids = append(gids, gid)
	}
	sort.Ints(gids)
	for _, gid := range gids {
		gc := newGroups[gid]
		oldGC, existed := oldGroups[gid]
		switch {
		case !existed:
			infof("Group %d added — starting engines", gid)
			s.startGroupWhenReadyLocked(*gc)
		case restartDiffers(oldGC, gc):
			infof("Group %d core config changed — restarting engines", gid)
			// Stop before start (the new engine recreates the same macvlan names), but not while holding s.mu: Stop
			// says goodbye to the group, tears down interfaces and runs commands, and every status request needs
			// s.mu, so a slow or stuck stop used to make the whole node stop answering the cluster.
			restarts = append(restarts, restartJob{*gc, s.stopGroupLocked(gid)})
		default:
			if !sameList(oldGC.ExtraVIPs, gc.ExtraVIPs) {
				// anycast addresses are independent of the election: change them
				// in place, without restarting the gateway
				running := s.anycast[gid] != nil
				for _, af := range []AF{afIPv4, afIPv6} {
					running = running || s.engines[engineKey{gid, af}] != nil
				}
				if running {
					infof("Group %d anycast addresses changed", gid)
					s.stopAnycastLocked(gid)
					s.startAnycastLocked(*gc)
				}
			}
			if liveDiffers(oldGC, gc) {
				infof("Group %d live update", gid)
				for _, af := range []AF{afIPv4, afIPv6} {
					if e := s.engines[engineKey{gid, af}]; e != nil {
						e.UpdateLive(gc)
					}
				}
			}
			if portChanged[gid] {
				for _, af := range []AF{afIPv4, afIPv6} {
					if e := s.engines[engineKey{gid, af}]; e != nil {
						go e.RestartDNS()
					}
				}
			}
		}
	}
	s.dc = nu
	s.mu.Unlock()

	// Stop outside the lock; Stop tears down interfaces and can be slow.  Gateways that restart are stopped with the
	// removed ones, all at once, and started again when every stop is done.
	for _, r := range restarts {
		stopped = append(stopped, r.engines...)
	}
	var wg sync.WaitGroup
	for _, e := range stopped {
		wg.Add(1)
		go func() { defer wg.Done(); e.Stop() }()
	}
	wg.Wait()
	if len(restarts) > 0 {
		s.mu.Lock()
		for _, r := range restarts {
			s.startGroupWhenReadyLocked(r.gc) // reloadMu is held, so no other Reload has changed the group since
		}
		s.mu.Unlock()
	}
}

func liveDiffers(a, b *GroupConfig) bool {
	return a.Priority != b.Priority || a.LBMethod != b.LBMethod || a.Weight != b.Weight ||
		a.HelloMS != b.HelloMS || a.HoldMS != b.HoldMS || a.Preempt != b.Preempt ||
		a.MaxAFNs != b.MaxAFNs
}

func (s *Supervisor) engineList() []*Engine {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]engineKey, 0, len(s.engines))
	for k := range s.engines {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].group != keys[j].group {
			return keys[i].group < keys[j].group
		}
		return keys[i].af < keys[j].af
	})
	out := make([]*Engine, len(keys))
	for i, k := range keys {
		out[i] = s.engines[k]
	}
	return out
}

// offnetEvery is how often a held-back gateway re-checks the interface's addresses.
var offnetEvery = 5 * time.Second

// ifaceAddrsFn lists an interface's addresses (replaceable in tests).
var ifaceAddrsFn = func(name string) ([]net.Addr, error) {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	return ifc.Addrs()
}

// onGatewaySubnet reports whether this node can take part in the gateway: for
// each family the gateway has, the node's interface must hold an address inside
// the VIP's subnet (the election runs on that network).  IPv4 is always checked.
// IPv6 is checked only when the interface has a global (non link-local) address
// — a node with only link-local addresses gives nothing to compare, and the
// election itself runs over link-local, so it is let through.  Every family that
// can be checked must match.
func onGatewaySubnet(gc GroupConfig) (bool, string) {
	addrs, err := ifaceAddrsFn(gc.Interface)
	if err != nil {
		return true, "" // no such interface (yet): the engine itself waits for it to appear
	}
	for _, v := range []struct {
		vip string
		v6  bool
	}{{gc.VIP4, false}, {gc.VIP6, true}} {
		if v.vip == "" {
			continue
		}
		pfx, err := netip.ParsePrefix(v.vip)
		if err != nil {
			continue // Validate reports a bad prefix; do not hide it here
		}
		if v.v6 && !hasGlobalV6(addrs) {
			continue
		}
		if !addrsInPrefix(addrs, pfx) {
			return false, fmt.Sprintf("this node has no address in the gateway's subnet %s on %s", pfx.Masked(), gc.Interface)
		}
	}
	return true, ""
}

// hasGlobalV6 reports whether any address is an IPv6 one that is not link-local or loopback.
func hasGlobalV6(addrs []net.Addr) bool {
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.To4() != nil {
			continue
		}
		if ip, ok := netip.AddrFromSlice(n.IP); ok && !ip.IsLinkLocalUnicast() && !ip.IsLoopback() && !ip.IsMulticast() {
			return true
		}
	}
	return false
}

func addrsInPrefix(addrs []net.Addr, pfx netip.Prefix) bool {
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip, ok := netip.AddrFromSlice(n.IP); ok && pfx.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}
