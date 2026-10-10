package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Query statistics for the Monitor → Statistics page.  Everything is kept in memory for
// the last qsRetain (30 days) and saved to disk by persist.go, so a restart does not lose it.
//
//   - Totals by response code, one slot per minute (the line chart and the tiles).
//   - Query types, transports, clients and domains (the donuts and the top lists) in two
//     tiers: 10-minute slots for the last day, so a short range is exact, and hour slots for
//     the whole 30 days.  A range inside the last day reads the fine tier, a longer one the
//     coarse tier.  The distinct clients and domains per slot are capped, so a flood of
//     random names cannot grow memory without bound; the rest count as "(others)".

const (
	qsRetain      = 30 * 24 * time.Hour
	qsMinSlots    = 30 * 24 * 60
	qsFineSpan    = 10  // minutes per fine top-list slot (used for ranges within the last day)
	qsFineSlots   = 150 // 25 hours of fine slots
	qsFineWithin  = 24 * time.Hour
	qsCoarseSpan  = 60 // minutes per coarse top-list slot (older data, and longer ranges)
	qsCoarseSlots = 30 * 24
	qsCapClients  = 300
	qsCapDomains  = 600
	qsKeepResult  = 100 // entries per top list in a reply
	qsMaxPoints   = 800
	qsOthers      = "(others)"
	qsRDNSTTL     = 10 * time.Minute
	qsRDNSNegTTL  = 2 * time.Minute // a client with no name found is asked again after this
	qsRDNSTimeout = 2 * time.Second
)

type qsMin struct {
	stamp                                      int64 // minutes since the epoch; a slot with another stamp is stale
	total, noerror, servfail, nxdomain, refuse uint32
	other                                      uint32
	updates, updfail                           uint32 // dynamic DNS updates (also counted in total) and those the primary did not accept
	chit, cmiss                                uint32 // lookups of the answer cache: answered from it / not found (so asked upstream)
}

// The kinds of answer the top lists are kept apart by, so a tile can show who asked for
// what and got, say, NXDOMAIN.  The order is the index into qsTop.c.
const (
	qcNoError = iota
	qcServFail
	qcNXDomain
	qcRefused
	qcOther
	qcUpdate // dynamic DNS updates are a kind of their own, whatever the primary answered
	qcCount
)

var qcNames = [qcCount]string{"noerror", "servfail", "nxdomain", "refused", "other", "update"}

// qcOf maps a response code to its kind.
func qcOf(rcode int) int {
	switch rcode {
	case 0:
		return qcNoError
	case 2:
		return qcServFail
	case 3:
		return qcNXDomain
	case 5:
		return qcRefused
	}
	return qcOther
}

// qsCaps are the most distinct clients and domains one slot keeps per kind of answer;
// anything beyond counts as "(others)".  Failures are the rare kind, so they get less
// room — but a flood of random names is mostly NXDOMAIN, so that one keeps a good share.
var qsCaps = [qcCount][2]int{
	qcNoError:  {qsCapClients, qsCapDomains},
	qcServFail: {100, 200},
	qcNXDomain: {150, 300},
	qcRefused:  {100, 200},
	qcOther:    {50, 100},
	qcUpdate:   {100, 100},
}

// qsPairCaps are the most distinct (client, domain) pairs one slot keeps per kind of answer.
// They answer "what did this client ask for" and "who asked for this".  A pair beyond the cap
// is not kept; the reply makes up the difference as "(others)" from the client and domain totals.
var qsPairCaps = [qcCount]int{
	qcNoError:  500,
	qcServFail: 100,
	qcNXDomain: 200,
	qcRefused:  50,
	qcOther:    30,
	qcUpdate:   100,
}

// qsKind is the top lists of one kind of answer in one slot.
type qsKind struct {
	clients  map[string]uint32
	domains  map[string]uint32
	types    map[string]uint32
	pairs    map[string]map[string]uint32 // client → domain → count
	npairs   int
	udp, tcp uint32
}

type qsTop struct {
	stamp int64 // slot periods since the epoch
	c     [qcCount]qsKind
}

// QStats is the collector.  The zero value is not usable: call NewQStats.
type QStats struct {
	mu         sync.Mutex
	who        map[netip.Addr]string // client address strings in use (whoString)
	start      time.Time
	now        func() time.Time
	pend       [qsShards]qsPending // events not yet counted (see Record)
	pendN      atomic.Uint32       // picks the shard
	flushArmed atomic.Bool
	mins       [qsMinSlots]qsMin
	fine       [qsFineSlots]qsTop
	coarse     [qsCoarseSlots]qsTop

	rmu   sync.Mutex
	rdns  map[string]rdnsEntry
	rwork map[string]bool
	// ptrVia asks the DNS servers ddgw forwards to for the names of an address (rdns.go); set by the daemon.
	ptrVia func(ctx context.Context, ip string) []string
}

type rdnsEntry struct {
	names []string
	at    time.Time
}

func NewQStats() *QStats {
	return &QStats{start: time.Now(), now: time.Now, rdns: map[string]rdnsEntry{}, rwork: map[string]bool{}}
}

// qstats is the daemon-wide collector, fed by every DNS frontend.
var qstats = NewQStats()

// qtUpdate stands for a dynamic DNS update in the statistics (its own "type"; the wire has none).
const qtUpdate = 65535

var qtypeNames = map[uint16]string{65535: "UPDATE", 1: "A", 2: "NS", 5: "CNAME", 6: "SOA", 12: "PTR", 15: "MX", 16: "TXT", 28: "AAAA", 33: "SRV",
	35: "NAPTR", 43: "DS", 46: "RRSIG", 48: "DNSKEY", 64: "SVCB", 65: "HTTPS", 99: "SPF", 255: "ANY", 257: "CAA"}

func qtypeName(t uint16) string {
	if n, ok := qtypeNames[t]; ok {
		return n
	}
	return "TYPE" + strconv.Itoa(int(t))
}

// questionOf reads the first question of a DNS message: its name (lower-case, no
// trailing dot, "." for the root) and type.  ok is false for a message without one.
func questionOf(q []byte) (name string, qtype uint16, ok bool) {
	var qi qinfo
	parseQuestion(q, &qi)
	if !qi.ok {
		return "", 0, false
	}
	return qi.name(), qi.qtype, true
}

// qinfo is the first question of a query, read once for everything that needs it (the cache key, the statistics).
// The name is lower-cased in a buffer inside the struct, so reading it allocates nothing; name() makes the string
// when one is wanted.
type qinfo struct {
	buf     [256]byte
	n       int    // length of the name in buf; 0 is the root
	qtype   uint16 //
	nameEnd int    // offset just after the name in the message
	ok      bool
}

func (qi *qinfo) name() string {
	if qi.n == 0 {
		return "."
	}
	return string(qi.buf[:qi.n])
}

// parseQuestion fills qi from the first question of q; qi.ok is false for a message without one.
func parseQuestion(q []byte, qi *qinfo) {
	qi.ok = false
	if len(q) < 12 || binary.BigEndian.Uint16(q[4:]) < 1 {
		return
	}
	n := 0
	i := 12
	for {
		if i >= len(q) {
			return
		}
		l := int(q[i])
		if l == 0 {
			i++
			break
		}
		if l&0xC0 != 0 || i+1+l > len(q) {
			return
		}
		if n > 0 {
			if n >= len(qi.buf) {
				return
			}
			qi.buf[n] = '.'
			n++
		}
		if n+l > 255 {
			return
		}
		for _, c := range q[i+1 : i+1+l] {
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			qi.buf[n] = c
			n++
		}
		i += l + 1
	}
	if i+2 > len(q) {
		return
	}
	qi.n, qi.qtype, qi.nameEnd, qi.ok = n, binary.BigEndian.Uint16(q[i:]), i, true
}

// RecordCache counts one lookup of the answer cache (a query that can be cached): hit = answered from it.
func (s *QStats) RecordCache(hit bool) {
	var k uint8 = qeCacheMiss
	if hit {
		k = qeCacheHit
	}
	s.enqueue(qEvent{t: s.now().Unix(), kind: k})
}

func (s *QStats) applyCache(e qEvent) {
	m := e.t / 60
	ms := &s.mins[m%qsMinSlots]
	if ms.stamp != m {
		*ms = qsMin{stamp: m}
	}
	if e.kind == qeCacheHit {
		ms.chit++
	} else {
		ms.cmiss++
	}
}

// Record counts one answered client query.
func (s *QStats) Record(client netip.Addr, name string, qtype uint16, tcp bool, rcode int) {
	s.enqueue(qEvent{t: s.now().Unix(), kind: qeQuery, client: client, name: name, qtype: qtype, tcp: tcp, rcode: int32(rcode)})
}

// qsWhoMax is how many client address strings are kept for reuse (see whoString).
const qsWhoMax = 4096

// whoString is the text of a client address: made once and reused, since String() allocates every time and the
// same few clients account for most queries.  The table is emptied when it is full, so a flood of distinct
// addresses cannot grow it past qsWhoMax.  s.mu is held.
func (s *QStats) whoString(client netip.Addr) string {
	if !client.IsValid() {
		return "unknown"
	}
	client = client.Unmap()
	if w, ok := s.who[client]; ok {
		return w
	}
	if s.who == nil || len(s.who) >= qsWhoMax {
		s.who = make(map[netip.Addr]string, 256)
	}
	w := client.String()
	s.who[client] = w
	return w
}

// apply counts one query; s.mu is held.
func (s *QStats) apply(e qEvent) {
	client, name, qtype, tcp, rcode := e.client, e.name, e.qtype, e.tcp, int(e.rcode)
	m := e.t / 60
	who := s.whoString(client)
	ms := &s.mins[m%qsMinSlots]
	if ms.stamp != m {
		*ms = qsMin{stamp: m}
	}
	ms.total++
	kind := qcOf(rcode)
	if qtype == qtUpdate { // counted as an update (and in the total), not under the answer the primary gave
		kind = qcUpdate
		ms.updates++
		if rcode != 0 {
			ms.updfail++
		}
	}
	switch kind {
	case qcUpdate:
	case qcNoError:
		ms.noerror++
	case qcServFail:
		ms.servfail++
	case qcNXDomain:
		ms.nxdomain++
	case qcRefused:
		ms.refuse++
	default:
		ms.other++
	}
	tn := qtypeName(qtype)
	for _, tier := range []struct {
		slots []qsTop
		span  int64
	}{{s.fine[:], qsFineSpan}, {s.coarse[:], qsCoarseSpan}} {
		p := m / tier.span
		tp := &tier.slots[p%int64(len(tier.slots))]
		if tp.stamp != p {
			*tp = qsTop{stamp: p}
		}
		k := &tp.c[kind]
		if k.clients == nil {
			k.clients, k.domains, k.types = map[string]uint32{}, map[string]uint32{}, map[string]uint32{}
			k.pairs = map[string]map[string]uint32{}
		}
		bump(k.clients, who, qsCaps[kind][0])
		bump(k.domains, name, qsCaps[kind][1])
		k.types[tn]++
		if dm := k.pairs[who]; dm != nil && dm[name] > 0 {
			dm[name]++
		} else if k.npairs < qsPairCaps[kind] {
			if dm == nil {
				dm = map[string]uint32{}
				k.pairs[who] = dm
			}
			dm[name]++
			k.npairs++
		}
		if tcp {
			k.tcp++
		} else {
			k.udp++
		}
	}
}

// Counting a query takes the collector's one lock, and with every worker doing it for every query (and a second
// time for the cache) the lock itself was about 6% of the CPU on a busy node and made the workers wait on each
// other.  So the workers only append an event to one of a few small buffers, each behind its own lock, and the
// events are counted in one batch a moment later (or at once, when something reads the statistics).  The count is
// the same; it just reaches the tables up to qsFlushEvery late.
const (
	qsShards     = 16
	qsFlushEvery = 200 * time.Millisecond
	qsShardMax   = 1 << 15 // events held per shard; a full shard is counted by the caller instead
)

const (
	qeQuery = iota
	qeCacheHit
	qeCacheMiss
)

type qEvent struct {
	t      int64 // unix seconds when it happened
	client netip.Addr
	name   string
	qtype  uint16
	rcode  int32
	kind   uint8
	tcp    bool
}

type qsPending struct {
	mu sync.Mutex
	ev []qEvent
	_  [40]byte // keep neighbouring shards off one cache line
}

// enqueue files an event; the first one after a flush also arms the timer that counts the batch.
func (s *QStats) enqueue(e qEvent) {
	p := &s.pend[s.pendN.Add(1)%qsShards]
	p.mu.Lock()
	if len(p.ev) >= qsShardMax {
		p.mu.Unlock()
		s.flush() // way behind: count what is waiting now rather than grow without bound
		p.mu.Lock()
	}
	p.ev = append(p.ev, e)
	p.mu.Unlock()
	if s.flushArmed.CompareAndSwap(false, true) {
		time.AfterFunc(qsFlushEvery, func() { s.flushArmed.Store(false); s.flush() })
	}
}

// flush counts every waiting event.  Everything that reads or saves the tables calls it first.
func (s *QStats) flush() {
	var batch [][]qEvent
	for i := range s.pend {
		p := &s.pend[i]
		p.mu.Lock()
		if len(p.ev) > 0 {
			batch = append(batch, p.ev)
			p.ev = nil
		}
		p.mu.Unlock()
	}
	if len(batch) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, evs := range batch {
		for _, e := range evs {
			if e.kind == qeQuery {
				s.apply(e)
			} else {
				s.applyCache(e)
			}
		}
	}
}

func bump(m map[string]uint32, k string, limit int) {
	if _, ok := m[k]; !ok && len(m) >= limit {
		k = qsOthers
	}
	m[k]++
}

// NameCount is one row of a top list.
type NameCount struct {
	Name  string   `json:"name"`
	Count uint64   `json:"count"`
	Host  string   `json:"host,omitempty"`  // reverse-DNS name of a client, when known
	Hosts []string `json:"hosts,omitempty"` // every reverse-DNS name it has, for the tooltip
}

// QStatsResult is the answer to a query: parallel series, one point per Step seconds
// from Start, plus the totals and top lists over the whole range.
type QStatsResult struct {
	Client   string       `json:"client,omitempty"` // when set, Domains lists what this client asked for
	Domain   string       `json:"domain,omitempty"` // when set, Clients lists who asked for this domain
	Rcode    string       `json:"rcode"`            // the answer kind the types, transports and top lists are limited to; empty is all
	Start    int64        `json:"start"`
	Step     int          `json:"step"`
	From     int64        `json:"from"`
	To       int64        `json:"to"`
	Since    int64        `json:"since"` // when the daemon started counting
	Total    []uint64     `json:"total"`
	NoError  []uint64     `json:"no_error"`
	ServFail []uint64     `json:"server_failure"`
	NXDomain []uint64     `json:"nx_domain"`
	Refused  []uint64     `json:"refused"`
	Other    []uint64     `json:"other"`
	Updates  []uint64     `json:"updates"` // dynamic DNS updates (a part of Total)
	Sums     QStatsSums   `json:"sums"`
	Guard    MemGuardInfo `json:"guard"` // what the memory guard has dropped
	Types    []NameCount  `json:"types"`
	Protos   []NameCount  `json:"protos"`
	Clients  []NameCount  `json:"clients"`
	Domains  []NameCount  `json:"domains"`
	Cluster  *ClusterInfo `json:"cluster,omitempty"` // set when the numbers are those of several nodes added together
}

type QStatsSums struct {
	Total    uint64 `json:"total"`
	NoError  uint64 `json:"no_error"`
	ServFail uint64 `json:"server_failure"`
	NXDomain uint64 `json:"nx_domain"`
	Refused  uint64 `json:"refused"`
	Other    uint64 `json:"other"`
	Updates  uint64 `json:"updates"`        // dynamic DNS updates forwarded (a part of Total)
	UpdFail  uint64 `json:"updates_failed"` // those answered with anything but NOERROR
	CacheHit uint64 `json:"cache_hits"`     // lookups answered from the answer cache
	CacheMis uint64 `json:"cache_misses"`   // cacheable lookups it could not answer (hit rate = hits / (hits + misses))
	Clients  int    `json:"clients"`        // distinct clients (a lower bound once a slot hit its cap)
}

// stepFor picks the point spacing for a span: a minute up to 2 hours, ten minutes up to
// a day and a half, an hour up to a week, three hours beyond.
func stepFor(span time.Duration) int64 {
	switch {
	case span <= 2*time.Hour:
		return 60
	case span <= 36*time.Hour:
		return 600
	case span <= 7*24*time.Hour:
		return 3600
	}
	return 10800
}

// Query returns the statistics for [from, to).  The range is cut to what is retained,
// and aligned to the step so every node answers with the same points.
//
// f.Rcode limits the types, transports, clients and domains to one kind of answer ("noerror",
// "servfail", "nxdomain", "refused" or "other"); empty means every query.  f.Client replaces
// the domain list by what that client asked for, f.Domain the client list by who asked for
// that domain (both within the answer kind).  The time series and the sums always cover everything.
func (s *QStats) Query(from, to time.Time, f QFilter) *QStatsResult {
	rcode := f.Rcode
	now := s.now()
	if to.IsZero() || to.After(now) {
		to = now
	}
	if oldest := now.Add(-qsRetain); from.Before(oldest) {
		from = oldest
	}
	if !from.Before(to) {
		from = to.Add(-time.Hour)
	}
	step := stepFor(to.Sub(from))
	start := from.Unix() / step * step
	end := (to.Unix() + step) / step * step // the step containing `to` is included
	n := int((end - start) / step)
	if n > qsMaxPoints {
		n = qsMaxPoints
	}
	r := &QStatsResult{Rcode: rcode, Client: f.Client, Domain: f.Domain, Start: start, Step: int(step), From: from.Unix(), To: to.Unix(), Since: s.start.Unix(),
		Total: make([]uint64, n), NoError: make([]uint64, n), ServFail: make([]uint64, n), NXDomain: make([]uint64, n),
		Refused: make([]uint64, n), Other: make([]uint64, n), Updates: make([]uint64, n),
		Types: []NameCount{}, Protos: []NameCount{}, Clients: []NameCount{}, Domains: []NameCount{}}
	types, clients, domains := map[string]uint64{}, map[string]uint64{}, map[string]uint64{}
	var udp, tcp uint64
	pairDomains, pairClients := map[string]uint64{}, map[string]uint64{}

	s.flush()
	s.mu.Lock()
	for i := 0; i < n; i++ {
		for m := (start + int64(i)*step) / 60; m < (start+int64(i+1)*step)/60; m++ {
			ms := &s.mins[m%qsMinSlots]
			if ms.stamp != m {
				continue
			}
			r.Total[i] += uint64(ms.total)
			r.NoError[i] += uint64(ms.noerror)
			r.ServFail[i] += uint64(ms.servfail)
			r.NXDomain[i] += uint64(ms.nxdomain)
			r.Refused[i] += uint64(ms.refuse)
			r.Other[i] += uint64(ms.other)
			r.Updates[i] += uint64(ms.updates)
			r.Sums.UpdFail += uint64(ms.updfail)
			r.Sums.CacheHit += uint64(ms.chit)
			r.Sums.CacheMis += uint64(ms.cmiss)
		}
	}
	slots, span := s.coarse[:], int64(qsCoarseSpan)
	if now.Sub(from) <= qsFineWithin {
		slots, span = s.fine[:], qsFineSpan
	}
	for p := from.Unix() / 60 / span; p <= to.Unix()/60/span; p++ {
		tp := &slots[p%int64(len(slots))]
		if tp.stamp != p {
			continue
		}
		for ci := range tp.c {
			if rcode != "" && qcNames[ci] != rcode {
				continue
			}
			k := &tp.c[ci]
			for n, v := range k.clients {
				clients[n] += uint64(v)
			}
			for n, v := range k.domains {
				domains[n] += uint64(v)
			}
			for n, v := range k.types {
				types[n] += uint64(v)
			}
			udp += uint64(k.udp)
			tcp += uint64(k.tcp)
			if f.Client != "" {
				for n, v := range k.pairs[f.Client] {
					pairDomains[n] += uint64(v)
				}
			}
			if f.Domain != "" {
				for c, m := range k.pairs {
					if v := m[f.Domain]; v > 0 {
						pairClients[c] += uint64(v)
					}
				}
			}
		}
	}
	s.mu.Unlock()

	for i := 0; i < n; i++ {
		r.Sums.Total += r.Total[i]
		r.Sums.NoError += r.NoError[i]
		r.Sums.ServFail += r.ServFail[i]
		r.Sums.NXDomain += r.NXDomain[i]
		r.Sums.Refused += r.Refused[i]
		r.Sums.Other += r.Other[i]
		r.Sums.Updates += r.Updates[i]
	}
	r.Guard = memguard.Info()
	r.Sums.Clients = len(clients)
	if _, ok := clients[qsOthers]; ok {
		r.Sums.Clients--
	}
	r.Types = topList(types, 0)
	if udp > 0 {
		r.Protos = append(r.Protos, NameCount{Name: "UDP", Count: udp})
	}
	if tcp > 0 {
		r.Protos = append(r.Protos, NameCount{Name: "TCP", Count: tcp})
	}
	if f.Client != "" {
		domains = withRest(pairDomains, clients[f.Client])
	}
	if f.Domain != "" {
		clients = withRest(pairClients, domains[f.Domain])
	}
	r.Clients = topList(clients, qsKeepResult)
	r.Domains = topList(domains, qsKeepResult)
	s.fillHosts(r.Clients)
	return r
}

// withRest adds an "(others)" entry for the part of total the pairs do not account for.
func withRest(m map[string]uint64, total uint64) map[string]uint64 {
	var sum uint64
	for _, v := range m {
		sum += v
	}
	if total > sum {
		m[qsOthers] = total - sum
	}
	return m
}

// topList sorts by count (name breaks ties); limit 0 keeps all.  "(others)" always sorts last.
func topList(m map[string]uint64, limit int) []NameCount {
	out := make([]NameCount, 0, len(m))
	for k, v := range m {
		out = append(out, NameCount{Name: k, Count: v})
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

// fillHosts adds the reverse-DNS names to the listed clients, from a cache.  A miss starts a
// background lookup (a few at a time), so the page never waits for it: the names show up on a
// later refresh.
func (s *QStats) fillHosts(cl []NameCount) {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	for i := range cl {
		ip := cl[i].Name
		if _, err := netip.ParseAddr(ip); err != nil {
			continue
		}
		e, ok := s.rdns[ip]
		if ok && time.Since(e.at) < qsRDNSTTL && (len(e.names) > 0 || time.Since(e.at) < qsRDNSNegTTL) {
			setHosts(&cl[i], e.names)
			continue
		}
		if !s.rwork[ip] {
			s.rwork[ip] = true
			go s.lookup(ip)
		}
		if ok {
			setHosts(&cl[i], e.names) // stale beats nothing while the refresh runs
		}
	}
}

func setHosts(c *NameCount, names []string) {
	if len(names) > 0 {
		c.Host, c.Hosts = names[0], names
	}
}

// qsRDNSParallel bounds the reverse lookups running at once.
var qsRDNSSem = make(chan struct{}, 8)

func (s *QStats) lookup(ip string) {
	qsRDNSSem <- struct{}{}
	defer func() { <-qsRDNSSem }()
	var out []string
	add := func(names []string) {
		for _, n := range names {
			if n = strings.TrimSuffix(n, "."); n != "" && len(out) < 5 {
				out = append(out, n)
			}
		}
	}
	if s.ptrVia != nil { // the servers ddgw forwards to first
		ctx, cancel := context.WithTimeout(context.Background(), qsRDNSTimeout)
		add(s.ptrVia(ctx, ip))
		cancel()
	}
	if len(out) == 0 { // then whatever this machine's own resolver knows (hosts file, resolv.conf)
		ctx, cancel := context.WithTimeout(context.Background(), qsRDNSTimeout)
		if names, err := net.DefaultResolver.LookupAddr(ctx, ip); err == nil {
			add(names)
		}
		cancel()
	}
	s.rmu.Lock()
	s.rdns[ip] = rdnsEntry{names: out, at: time.Now()}
	delete(s.rwork, ip)
	s.rmu.Unlock()
}

// qstatsRange reads the from/to arguments of the API and CLI: unix seconds, or a span
// such as "1h", "1d", "7d", "30d" meaning that long up to now.  Empty means the last hour.
func qstatsRange(from, to string) (time.Time, time.Time, error) {
	now := time.Now()
	end := now
	if to != "" {
		n, err := strconv.ParseInt(to, 10, 64)
		if err != nil || n <= 0 {
			return time.Time{}, time.Time{}, fmt.Errorf("%q is not a time (unix seconds)", to)
		}
		end = time.Unix(n, 0)
	}
	if from == "" {
		return end.Add(-time.Hour), end, nil
	}
	if n, err := strconv.ParseInt(from, 10, 64); err == nil && n > 1e9 {
		return time.Unix(n, 0), end, nil
	}
	d, err := parseSince(from)
	if err != nil || d == 0 {
		return time.Time{}, time.Time{}, fmt.Errorf("%q is not a range: use 1h, 1d, 7d, 30d or unix seconds", from)
	}
	return end.Add(-d), end, nil
}

// qstatsRcode checks the rcode argument of the API and CLI.
func qstatsRcode(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "all" {
		return "", nil
	}
	for _, n := range qcNames {
		if n == s {
			return s, nil
		}
	}
	return "", fmt.Errorf("%q is not an answer kind: use %s", s, strings.Join(qcNames[:], ", "))
}

// QFilter narrows a Query; zero values mean "no filter".
type QFilter struct {
	Rcode  string // an answer kind, see qcNames
	Client string // an address as listed in Top clients
	Domain string // a name as listed in Top domains
}

// qstatsFilter checks and normalises the rcode, client and domain arguments of the API and CLI.
func qstatsFilter(rcode, client, domain string) (QFilter, error) {
	var f QFilter
	var err error
	if f.Rcode, err = qstatsRcode(rcode); err != nil {
		return f, err
	}
	client = strings.TrimSpace(client)
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if client != "" && domain != "" {
		return f, fmt.Errorf("pick a client or a domain, not both")
	}
	if client != "" {
		if a, err := netip.ParseAddr(client); err == nil {
			client = a.Unmap().String()
		} else if client != "unknown" {
			return f, fmt.Errorf("%q is not a client address", client)
		}
	}
	if len(domain) > 255 || strings.ContainsAny(domain, " \t\r\n") {
		return f, fmt.Errorf("%q is not a domain name", domain)
	}
	f.Client, f.Domain = client, domain
	return f, nil
}

// Clear forgets every count and top list (Statistics ▸ Clear).  The counting-since time starts again.
func (s *QStats) Clear() {
	s.flush()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mins = [qsMinSlots]qsMin{}
	s.fine = [qsFineSlots]qsTop{}
	s.coarse = [qsCoarseSlots]qsTop{}
	s.who = nil
	s.start = s.now()
}
