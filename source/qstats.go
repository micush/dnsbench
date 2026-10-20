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
	qsKeepResult  = 500  // entries per top list in a reply
	qsKeepPicked  = 5000 // entries in the list of what a picked client asked for, or of who asked for a picked domain
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

// qsKind is the top lists of one kind of answer in one slot.
type qsKind struct {
	clients  map[string]uint32
	domains  map[string]uint32
	types    map[string]uint32
	udp, tcp uint32
	// proto splits those by the IP version of the client: index 0 UDP over IPv4, 1 UDP over IPv6, 2 TCP over IPv4,
	// 3 TCP over IPv6 (see protoIdx).  Counts saved by an older version have no split: what udp and tcp hold beyond
	// the split is shown as plain UDP and TCP.
	proto [4]uint32
}

// protoNames are the names of the proto counters, in index order.
var protoNames = [4]string{"UDP · IPv4", "UDP · IPv6", "TCP · IPv4", "TCP · IPv6"}

// protoOrder is every name a transport slice can have, in the order they are listed: the split ones and, from counts made
// before the split existed (or a node on an older version), plain UDP and TCP.
var protoOrder = []string{"UDP · IPv4", "UDP · IPv6", "UDP", "TCP · IPv4", "TCP · IPv6", "TCP"}

// protoIdx is the index into qsKind.proto of a query: its transport and whether the client address is IPv6 (an unknown
// address counts as IPv4).
func protoIdx(tcp bool, client netip.Addr) int {
	i := 0
	if tcp {
		i = 2
	}
	if client.IsValid() && client.Unmap().Is6() {
		i++
	}
	return i
}

type qsTop struct {
	stamp int64 // slot periods since the epoch
	c     [qcCount]qsKind
}

// QStats is the collector.  The zero value is not usable: call NewQStats.
type QStats struct {
	mu         sync.Mutex
	pairs      *pairStore            // who asked for what: on disk (qpairs.go)
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
	names map[string]string // names given by hand (Settings), under rmu
	rdns  map[string]rdnsEntry
	rwork map[string]bool
	// ptrVia asks the DNS servers dnsdir forwards to for the names of an address (rdns.go); set by the daemon.
	ptrVia func(ctx context.Context, ip string) []string
}

type rdnsEntry struct {
	names []string
	at    time.Time
}

func NewQStats() *QStats {
	return &QStats{pairs: newPairStore(), start: time.Now(), now: time.Now, rdns: map[string]rdnsEntry{}, rwork: map[string]bool{}}
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
	s.pairs.add(e.t, kind, who, name, 1)
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
		}
		bump(k.clients, who, qsCaps[kind][0])
		bump(k.domains, name, qsCaps[kind][1])
		k.types[tn]++
		if tcp {
			k.tcp++
		} else {
			k.udp++
		}
		k.proto[protoIdx(tcp, client)]++
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
	Name   string   `json:"name"`
	Count  uint64   `json:"count"`
	Host   string   `json:"host,omitempty"`   // reverse-DNS name of a client, when known
	Hosts  []string `json:"hosts,omitempty"`  // every reverse-DNS name it has, for the tooltip
	Manual bool     `json:"manual,omitempty"` // Host is a name given by hand (Name…), not a reverse-DNS one
}

// QStatsResult is the answer to a query: parallel series, one point per Step seconds
// from Start, plus the totals and top lists over the whole range.
type QStatsResult struct {
	Client     string       `json:"client,omitempty"` // when set, Domains lists what this client asked for
	Domain     string       `json:"domain,omitempty"` // when set, Clients lists who asked for this domain
	Rcode      string       `json:"rcode"`            // the answer kind the types, transports and top lists are limited to; empty is all
	Start      int64        `json:"start"`
	Step       int          `json:"step"`
	From       int64        `json:"from"`
	To         int64        `json:"to"`
	Since      int64        `json:"since"` // when the daemon started counting
	Total      []uint64     `json:"total"`
	NoError    []uint64     `json:"no_error"`
	ServFail   []uint64     `json:"server_failure"`
	NXDomain   []uint64     `json:"nx_domain"`
	Refused    []uint64     `json:"refused"`
	Other      []uint64     `json:"other"`
	Updates    []uint64     `json:"updates"`              // dynamic DNS updates (a part of Total)
	ClientsN   []uint64     `json:"client_counts"`        // distinct clients seen so far in the range, per point (10-minute resolution up to a day and a half, an hour beyond; a lower bound)
	ClientsNew [][]string   `json:"client_new,omitempty"` // the clients first seen at each point, so a cluster can take the union exactly
	Sums       QStatsSums   `json:"sums"`
	Guard      MemGuardInfo `json:"guard"` // what the memory guard has dropped
	Types      []NameCount  `json:"types"`
	Protos     []NameCount  `json:"protos"`
	Clients    []NameCount  `json:"clients"`
	Domains    []NameCount  `json:"domains"`
	Cluster    *ClusterInfo `json:"cluster,omitempty"` // set when the numbers are those of several nodes added together
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
		Refused: make([]uint64, n), Other: make([]uint64, n), Updates: make([]uint64, n), ClientsN: make([]uint64, n),
		Types: []NameCount{}, Protos: []NameCount{}, Clients: []NameCount{}, Domains: []NameCount{}}
	types, clients, domains := map[string]uint64{}, map[string]uint64{}, map[string]uint64{}
	var protos [4]uint64
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
	r.ClientsN, r.ClientsNew = distinctPerPoint(slots, span, rcode, start, step, n, from.Unix()/60/span, to.Unix()/60/span)
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
			for i, n := range k.proto {
				protos[i] += uint64(n)
			}
		}
	}
	s.mu.Unlock()
	cset, dset := pickSet(f.Client), pickSet(f.Domain)
	if cset != nil || dset != nil { // the pairs are on disk: read the units the slots above cover
		s.pairs.scan(from.Unix()/60/span*span/pairUnitMin, (to.Unix()/60/span+1)*span/pairUnitMin-1, func(kind int, c, name string, v uint32) {
			if rcode != "" && qcNames[kind] != rcode {
				return
			}
			if cset[c] {
				pairDomains[name] += uint64(v)
			}
			if dset[name] {
				pairClients[c] += uint64(v)
			}
		})
	}

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
	add := func(name string, n uint64) {
		if n > 0 {
			r.Protos = append(r.Protos, NameCount{Name: name, Count: n})
		}
	}
	add(protoNames[0], protos[0])
	add(protoNames[1], protos[1])
	add("UDP", udp-min(udp, protos[0]+protos[1])) // counted before the split existed
	add(protoNames[2], protos[2])
	add(protoNames[3], protos[3])
	add("TCP", tcp-min(tcp, protos[2]+protos[3]))
	if f.Client != "" {
		domains = withRest(pairDomains, sumSet(clients, cset))
	}
	if f.Domain != "" {
		clients = withRest(pairClients, sumSet(domains, dset))
	}
	nc, nd := qsKeepResult, qsKeepResult
	if f.Domain != "" {
		nc = qsKeepPicked
	}
	if f.Client != "" {
		nd = qsKeepPicked
	}
	r.Clients = topList(s.keepMatchingClients(clients, f.CMatch), nc)
	r.Domains = topList(keepMatching(domains, f.DMatch), nd)
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
		if n := s.names[ip]; n != "" { // a name given by hand comes first, ahead of what reverse DNS says
			cl[i].Host, cl[i].Manual = n, true
			cl[i].Hosts = append([]string{n}, e.names...)
			if !ok && !s.rwork[ip] {
				s.rwork[ip] = true
				go s.lookup(ip) // the reverse names still go in the tooltip
			}
			continue
		}
		if ok && time.Since(e.at) < qsRDNSTTL && (len(e.names) > 0 || time.Since(e.at) < qsRDNSNegTTL) {
			setHosts(&cl[i], e.names)
			continue
		}
		if !s.rwork[ip] && i < qsKeepResult { // only the first rows are looked up: a list of thousands would queue for minutes
			s.rwork[ip] = true
			go s.lookup(ip)
		}
		if ok {
			setHosts(&cl[i], e.names) // stale beats nothing while the refresh runs
		}
	}
}

// SetNames sets the names given to clients by hand (address -> name); the daemon calls it with every configuration it loads.
func (s *QStats) SetNames(m map[string]string) {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	s.rmu.Lock()
	s.names = c
	s.rmu.Unlock()
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
	if s.ptrVia != nil { // the servers dnsdir forwards to first
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
	if from == "now" {
		// the top lists are kept in 10-minute slots; starting at a slot boundary makes the totals and the top
		// lists cover the same period, and a start in the last 5 minutes of a slot reaches back one more slot
		slot := int64(qsFineSpan * 60)
		start := end.Unix() / slot * slot
		if end.Unix()-start < slot/2 {
			start -= slot
		}
		return time.Unix(start, 0), end, nil
	}
	d, err := parseSince(from)
	if err != nil || d == 0 {
		return time.Time{}, time.Time{}, fmt.Errorf("%q is not a range: use now, 1h, 1d, 7d, 30d or unix seconds", from)
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
	// CMatch and DMatch keep only the clients / domains whose name contains the text (any case): the filter boxes of the
	// two top lists.  They look through everything the counters kept, not only the entries the reply lists.
	CMatch string
	DMatch string
}

// withMatch adds the two filter-box texts (trimmed, lower case, at most 100 characters) to a filter.
func (f QFilter) withMatch(client, domain string) QFilter {
	clean := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		if len(s) > 200 {
			s = s[:200]
		}
		return s
	}
	f.CMatch, f.DMatch = clean(client), clean(domain)
	return f
}

// qsHostLookups is how many clients without a host name yet are looked up in the background when a filter that looks like a
// host name is typed (their names are there on a later refresh).
const qsHostLookups = 200

// keepMatchingClients is keepMatching for the client list: a client is kept when its address or any of its reverse-DNS
// host names contains the text.  Only the names already looked up can match; a text that is not just digits, a-f, dots and
// colons (so it can be part of a host name) starts the lookups of the busiest clients that have none yet.
func (s *QStats) keepMatchingClients(m map[string]uint64, text string) map[string]uint64 {
	expr := parseMatch(text)
	if expr == nil {
		return m
	}
	hostLike := expr.hostLike()
	out := make(map[string]uint64, len(m))
	var todo []NameCount
	s.rmu.Lock()
	for k, v := range m {
		if k == qsOthers {
			continue
		}
		e, seen := s.rdns[k]
		texts := make([]string, 0, 2+len(e.names)) // the address, the name given by hand and the reverse names
		texts = append(texts, strings.ToLower(k), strings.ToLower(s.names[k]))
		for _, n := range e.names {
			texts = append(texts, strings.ToLower(n))
		}
		if expr.matches(texts...) {
			out[k] = v
			continue
		}
		if hostLike && !seen && !s.rwork[k] {
			if _, err := netip.ParseAddr(k); err == nil {
				todo = append(todo, NameCount{Name: k, Count: v})
			}
		}
	}
	if len(todo) > 0 {
		sort.Slice(todo, func(i, j int) bool { return todo[i].Count > todo[j].Count })
		if len(todo) > qsHostLookups {
			todo = todo[:qsHostLookups]
		}
		for _, c := range todo {
			s.rwork[c.Name] = true
			go s.lookup(c.Name)
		}
	}
	s.rmu.Unlock()
	return out
}

// keepMatching drops the entries whose name does not contain text; "(others)" goes too, as it stands for names not kept.
func keepMatching(m map[string]uint64, text string) map[string]uint64 {
	expr := parseMatch(text)
	if expr == nil {
		return m
	}
	out := make(map[string]uint64, len(m))
	for k, v := range m {
		if k != qsOthers && expr.matches(strings.ToLower(k)) {
			out[k] = v
		}
	}
	return out
}

// matchExpr is the text of a filter box: words joined by AND or OR (any case), AND binding first, so "ubuntu AND ntp OR debian"
// is (ubuntu and ntp) or debian.  It is a list of alternatives, each a list of terms that must all be found.  Words with no
// operator between them stay one term (a space is part of what is looked for).
type matchExpr [][]string

func parseMatch(text string) matchExpr {
	var expr matchExpr
	var group, term []string
	endTerm := func() {
		if len(term) > 0 {
			group = append(group, strings.Join(term, " "))
			term = nil
		}
	}
	endGroup := func() {
		endTerm()
		if len(group) > 0 {
			expr = append(expr, group)
			group = nil
		}
	}
	for _, w := range strings.Fields(strings.ToLower(text)) {
		switch w {
		case "and":
			endTerm()
		case "or":
			endGroup()
		default:
			term = append(term, w)
		}
	}
	endGroup()
	return expr
}

// matches says whether the expression holds for something known by these (lower-case) texts: each term must be found in at
// least one of them.
func (e matchExpr) matches(texts ...string) bool {
	for _, group := range e {
		all := true
		for _, term := range group {
			found := false
			for _, t := range texts {
				if strings.Contains(t, term) {
					found = true
					break
				}
			}
			if !found {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// hostLike says whether some term can be part of a host name (it is not just digits, a-f, dots and colons).
func (e matchExpr) hostLike() bool {
	for _, group := range e {
		for _, term := range group {
			if strings.Trim(term, "0123456789abcdef:.") != "" {
				return true
			}
		}
	}
	return false
}

// qstatsFilter checks and normalises the rcode, client and domain arguments of the API and CLI.
// qsMaxPicked is the most clients (or domains) a filter may name: the page sends several when rows are Ctrl-clicked.
const qsMaxPicked = 100

// pickSet is the set of names in a filter's comma-separated list (nil for none).
func pickSet(s string) map[string]bool {
	if s == "" {
		return nil
	}
	m := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p != "" {
			m[p] = true
		}
	}
	return m
}

// sumSet adds up what the counts hold for the names in the set.
func sumSet(m map[string]uint64, set map[string]bool) uint64 {
	var t uint64
	for k := range set {
		t += m[k]
	}
	return t
}

// cleanPicks checks each name of a comma-separated list with one, and returns the list without repeats.
func cleanPicks(s string, one func(string) (string, error)) (string, error) {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p, err := one(p)
		if err != nil {
			return "", err
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) > qsMaxPicked {
		return "", fmt.Errorf("at most %d names can be picked", qsMaxPicked)
	}
	return strings.Join(out, ","), nil
}

func qstatsFilter(rcode, client, domain string) (QFilter, error) {
	var f QFilter
	var err error
	if f.Rcode, err = qstatsRcode(rcode); err != nil {
		return f, err
	}
	client, err = cleanPicks(client, func(c string) (string, error) {
		if a, err := netip.ParseAddr(c); err == nil {
			return a.Unmap().String(), nil
		} else if c != "unknown" {
			return "", fmt.Errorf("%q is not a client address", c)
		}
		return c, nil
	})
	if err != nil {
		return f, err
	}
	domain, err = cleanPicks(domain, func(n string) (string, error) {
		n = strings.ToLower(strings.TrimSuffix(n, "."))
		if len(n) > 255 || strings.ContainsAny(n, " \t\r\n") {
			return "", fmt.Errorf("%q is not a domain name", n)
		}
		return n, nil
	})
	if err != nil {
		return f, err
	}
	if client != "" && domain != "" {
		return f, fmt.Errorf("pick a client or a domain, not both")
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
	s.pairs.clear()
}

// distinctPerPoint counts, for each of the n points of a chart, the distinct clients seen from the start
// of the range up to and including that point (limited to one rcode when set), so the last point is the
// Clients tile.  It also returns the clients first seen at each point.  Top-list slots run from pMin to
// pMax; a slot longer than a point is counted at the first point that overlaps it.
func distinctPerPoint(slots []qsTop, span int64, rcode string, start, step int64, n int, pMin, pMax int64) ([]uint64, [][]string) {
	out := make([]uint64, n)
	news := make([][]string, n)
	seen := map[string]struct{}{}
	done := map[int64]bool{}
	for i := 0; i < n; i++ {
		p0, p1 := (start+int64(i)*step)/60/span, (start+int64(i+1)*step-1)/60/span
		for p := max(p0, pMin); p <= min(p1, pMax); p++ {
			if done[p] {
				continue
			}
			done[p] = true
			tp := &slots[p%int64(len(slots))]
			if tp.stamp != p {
				continue
			}
			for ci := range tp.c {
				if rcode != "" && qcNames[ci] != rcode {
					continue
				}
				for c := range tp.c[ci].clients {
					if _, ok := seen[c]; !ok && c != qsOthers {
						seen[c] = struct{}{}
						news[i] = append(news[i], c)
					}
				}
			}
		}
		out[i] = uint64(len(seen))
	}
	return out, news
}
