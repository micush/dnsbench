package main

import (
	"container/list"
	"encoding/binary"
	"hash/maphash"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// The response cache.  A pool answers a repeated query from memory instead of asking an upstream server
// again, for as long as the records' TTLs allow (RFC 1035 and RFC 2308 for negative answers).  It lives in the
// pool; a pool that is rebuilt (servers added, paused or removed) takes the old pool's cache along as long as the cache
// settings are the same (see cacheCarries), and a node that has just started may fill it from another node (cachewarm.go).
//
// What is cached: an answer to a plain query (opcode QUERY, one question, class IN or any) that came back
// NOERROR (with data or without: "no data") or NXDOMAIN, not truncated.  A negative answer needs the zone's SOA in
// the authority section, whose MINIMUM sets how long it may be kept.  Nothing else is cached: SERVFAIL, REFUSED, truncated
// answers, zone transfers, anything signed with TSIG, and queries carrying EDNS options other than a client cookie.
//
// The cache key is the lower-cased name, type and class, the RD, AD and CD flags, whether the query had EDNS and its DO
// bit, the UDP size the client allows (UDP answers are limited to it), the transport, and — when ECS is on — the client's
// network, because the upstream may answer each network differently.  An entry lives for the smallest TTL among its
// records (the SOA minimum for a negative answer), at most cache_max_ttl; the TTLs a client sees count down with the entry's
// age and are capped to cache_max_ttl.  The ID and the case of the name (0x20 randomisation) are taken from the client's query.
// EDNS options in the stored answer (cookie, padding, NSID) are dropped.

const (
	cacheDefaultEntries = 10000
	cacheDefaultMaxTTL  = 3600
	cacheMinEntries     = 100
	cacheMaxEntries     = 1000000
	cacheMaxTTLLimit    = 7 * 24 * 3600
	optCookie           = 10
	optNSID             = 3
	optPadding          = 12
	typeTSIG            = 250
	typeTKEY            = 249
)

type cacheEntry struct {
	key  string
	msg  []byte
	ttls []int // offsets of the TTL fields that count down
	at   time.Time
	life time.Duration
	elem *list.Element
	sh   *cacheShard
	qEnd int // offset just after the question in msg, worked out once when the entry is stored
	size int // what the entry counts for against the shard's byte budget
}

// cacheShard is one slice of the cache with its own lock and its own LRU order.  At 100k queries a second one
// lock around the whole cache was the largest lock wait on a production node.
type cacheShard struct {
	mu       sync.Mutex
	max      int
	maxBytes int // budget for the sizes of the entries (see cacheBytesPerEntry)
	bytes    int
	m        map[string]*cacheEntry
	lru      *list.List // front = most recently used
}

// cacheShardMin: a cache smaller than this stays in one shard, so its LRU order is exact.
const (
	cacheShards   = 16
	cacheShardMin = 4096

	// The cache is bounded by bytes as well as by entries: cache_entries times cacheBytesPerEntry is its memory
	// budget.  A count alone lets a client that asks for names with very large answers (its own zone, big TXT
	// records) make a "10000 entry" cache hold hundreds of megabytes.  An ordinary answer is a few hundred
	// bytes, so the budget is only reached by unusually large ones; the oldest entries leave first, as for the count.
	cacheBytesPerEntry = 4096
	cacheEntryOverhead = 256 // bookkeeping counted on top of the message and the key
)

type respCache struct {
	max    int
	maxTTL uint32
	shards []*cacheShard
	seed   maphash.Seed
	now    func() time.Time

	Hits     atomic.Uint64 // answered from the cache
	Misses   atomic.Uint64 // cacheable queries that had to go upstream
	Bypassed atomic.Uint64 // queries that are never cached
	Evicted  atomic.Uint64 // entries pushed out because the cache was full
}

func newRespCache(max, maxTTL int) *respCache {
	if max < 1 {
		max = cacheDefaultEntries
	}
	if maxTTL < 1 {
		maxTTL = cacheDefaultMaxTTL
	}
	n := 1
	if max >= cacheShardMin {
		n = cacheShards
	}
	c := &respCache{max: max, maxTTL: uint32(maxTTL), seed: maphash.MakeSeed(), now: time.Now}
	for i := 0; i < n; i++ {
		c.shards = append(c.shards, &cacheShard{max: (max + n - 1) / n, maxBytes: ((max + n - 1) / n) * cacheBytesPerEntry,
			m: map[string]*cacheEntry{}, lru: list.New()})
	}
	return c
}

func (c *respCache) shardFor(key string) *cacheShard {
	if len(c.shards) == 1 {
		return c.shards[0]
	}
	return c.shards[maphash.String(c.seed, key)%uint64(len(c.shards))]
}

func (c *respCache) shardForBytes(key []byte) *cacheShard {
	if len(c.shards) == 1 {
		return c.shards[0]
	}
	return c.shards[maphash.Bytes(c.seed, key)%uint64(len(c.shards))]
}

// Bytes is what the entries count for against the byte budget.
func (c *respCache) Bytes() int {
	n := 0
	for _, sh := range c.shards {
		sh.mu.Lock()
		n += sh.bytes
		sh.mu.Unlock()
	}
	return n
}

func (c *respCache) Len() int {
	n := 0
	for _, sh := range c.shards {
		sh.mu.Lock()
		n += len(sh.m)
		sh.mu.Unlock()
	}
	return n
}

// keyFor returns the cache key of a client query, or ok=false when the query is not one to cache.
// ecs is the pool's ECS setting: with it the client's network is part of the key.
func (c *respCache) keyFor(q []byte, tcp bool, client netip.Addr, ecs bool, p4, p6 int) (string, bool) {
	var qi qinfo
	parseQuestion(q, &qi)
	var kb [384]byte
	b, ok := c.keyBytes(kb[:0], q, &qi, tcp, client, ecs, p4, p6)
	if !ok {
		return "", false
	}
	return string(b), true
}

// keyBytes appends the cache key of the client query q (whose question is already read into qi) to dst, or
// returns ok=false when the query is not one to cache.  Nothing is allocated when dst has room: a lookup is made
// with the bytes (a map index of string(b) does not copy) and a string is made only for a miss.
func (c *respCache) keyBytes(dst, q []byte, qi *qinfo, tcp bool, client netip.Addr, ecs bool, p4, p6 int) ([]byte, bool) {
	if len(q) < 12 || q[2]&0x80 != 0 || (q[2]>>3)&0x0F != 0 || binary.BigEndian.Uint16(q[4:]) != 1 ||
		binary.BigEndian.Uint16(q[6:]) != 0 || binary.BigEndian.Uint16(q[8:]) != 0 {
		return nil, false
	}
	if !qi.ok || qi.qtype == 251 || qi.qtype == 252 { // IXFR, AXFR (ANY is cached like any other type: a benchmark that asks it, and Technitium, serve it from memory)
		return nil, false
	}
	qEnd := qi.nameEnd + 4
	if qEnd > len(q) {
		return nil, false
	}
	qclass := binary.BigEndian.Uint16(q[qEnd-2:])
	flags := int(q[2] & 0x01)    // RD
	flags |= int(q[3]&0x30) >> 3 // AD (bit 1), CD (bit 2)
	size := 0
	if !tcp {
		size = 512
	}
	// what follows the question: nothing, or exactly one record, which must be an OPT with no options but a cookie
	switch binary.BigEndian.Uint16(q[10:]) {
	case 0:
		if qEnd != len(q) {
			return nil, false
		}
	case 1:
		o, ok := skipName(q, qEnd)
		if !ok || o+10 > len(q) {
			return nil, false
		}
		end := o + 10 + int(binary.BigEndian.Uint16(q[o+8:]))
		if end != len(q) || binary.BigEndian.Uint16(q[o:]) != typeOPT || !optionsOnly(q[o+10:end], optCookie) {
			return nil, false
		}
		flags |= 1 << 4
		if ttl := binary.BigEndian.Uint32(q[o+4:]); ttl&0x8000 != 0 { // DO
			flags |= 1 << 3
		}
		if !tcp {
			if size = int(binary.BigEndian.Uint16(q[o+2:])); size < 512 {
				size = 512
			}
		}
	default:
		return nil, false
	}
	if tcp {
		flags |= 1 << 5
	}
	if qi.n == 0 {
		dst = append(dst, '.')
	} else {
		dst = append(dst, qi.buf[:qi.n]...)
	}
	dst = append(dst, '|')
	dst = strconv.AppendInt(dst, int64(qi.qtype), 10)
	dst = append(dst, '|')
	dst = strconv.AppendInt(dst, int64(qclass), 10)
	dst = append(dst, '|')
	dst = strconv.AppendInt(dst, int64(flags), 10)
	dst = append(dst, '|')
	dst = strconv.AppendInt(dst, int64(size), 10)
	dst = append(dst, '|')
	if ecs && ecsUsable(client) {
		a := client.Unmap()
		bits := p6
		if a.Is4() {
			bits = p4
		}
		if pf, err := a.Prefix(bits); err == nil {
			dst = pf.AppendTo(dst)
		}
	}
	return dst, true
}

// questionEnd is the offset just after the (single) question of a message.
func questionEnd(m []byte) (int, bool) {
	o, ok := skipName(m, 12)
	if !ok || o+4 > len(m) {
		return 0, false
	}
	return o + 4, true
}

// optionsOnly reports whether every EDNS option in rdata has one of the given codes.
func optionsOnly(rdata []byte, codes ...uint16) bool {
	for i := 0; i < len(rdata); {
		if i+4 > len(rdata) {
			return false
		}
		code, l := binary.BigEndian.Uint16(rdata[i:]), int(binary.BigEndian.Uint16(rdata[i+2:]))
		if i+4+l > len(rdata) {
			return false
		}
		found := false
		for _, c := range codes {
			if c == code {
				found = true
			}
		}
		if !found {
			return false
		}
		i += 4 + l
	}
	return true
}

// get returns the cached answer for the query, with its ID, question and TTLs made right, or nil.
func (c *respCache) get(key string, q []byte) []byte {
	sh := c.shardFor(key)
	sh.mu.Lock()
	e := sh.m[key]
	return c.serve(sh, e, q, -1)
}

// getBytes is get for a key held as bytes, so that a hit allocates no key.  qEnd is the offset just after the
// question in q, when the caller has it already (-1 to work it out).
func (c *respCache) getBytes(key, q []byte, qEnd int) []byte {
	sh := c.shardForBytes(key)
	sh.mu.Lock()
	e := sh.m[string(key)] // does not allocate
	return c.serve(sh, e, q, qEnd)
}

// serve finishes a lookup: sh.mu is held on entry and released here.
func (c *respCache) serve(sh *cacheShard, e *cacheEntry, q []byte, qEnd int) []byte {
	if e == nil {
		sh.mu.Unlock()
		return nil
	}
	age := c.now().Sub(e.at)
	if age >= e.life || age < 0 {
		sh.drop(e)
		sh.mu.Unlock()
		return nil
	}
	sh.lru.MoveToFront(e.elem)
	sh.mu.Unlock()
	// an entry's message is never changed after it is stored, so it is copied outside the lock
	out := append([]byte(nil), e.msg...)
	ttls := e.ttls

	if qEnd < 0 {
		var ok bool
		if qEnd, ok = questionEnd(q); !ok {
			return nil
		}
	}
	if qEnd != e.qEnd {
		return nil
	}
	copy(out[0:2], q[0:2])
	copy(out[12:qEnd], q[12:qEnd]) // the client's own spelling of the name
	secs := uint32(age / time.Second)
	for _, off := range ttls {
		v := binary.BigEndian.Uint32(out[off:])
		if v > secs {
			v -= secs
		} else {
			v = 0
		}
		binary.BigEndian.PutUint32(out[off:], v)
	}
	return out
}

func (sh *cacheShard) drop(e *cacheEntry) {
	sh.lru.Remove(e.elem)
	delete(sh.m, e.key)
	sh.bytes -= e.size
}

// put stores a response to a query whose key is key, when it is one worth keeping.
func (c *respCache) put(key string, resp []byte) {
	c.store(key, resp, 0, false)
}

// store keeps a response that is age old already (a warm start hands over entries from another node with the age they
// had there, so their TTLs go on counting down).  An entry that is taken over (fromPeer) never replaces one this node
// has, never pushes one out, and goes to the cold end of the LRU order, so a warm start cannot displace what clients
// here are asking for.  It reports whether the entry was kept.
func (c *respCache) store(key string, resp []byte, age time.Duration, fromPeer bool) bool {
	msg, ttls, life, ok := c.prepare(resp)
	if !ok || age < 0 || age >= life {
		return false
	}
	sh := c.shardFor(key)
	qEnd, _ := questionEnd(msg)
	e := &cacheEntry{key: key, msg: msg, ttls: ttls, at: c.now().Add(-age), life: life, sh: sh, qEnd: qEnd,
		size: len(msg) + len(key) + cacheEntryOverhead}
	if e.size > sh.maxBytes { // one answer that is more than the whole budget of its shard is not kept
		return false
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if fromPeer {
		if old := sh.m[key]; old != nil || len(sh.m) >= sh.max || sh.bytes+e.size > sh.maxBytes {
			return false
		}
		e.elem = sh.lru.PushBack(e)
		sh.m[key] = e
		sh.bytes += e.size
		return true
	}
	if old := sh.m[key]; old != nil {
		sh.drop(old)
	}
	e.elem = sh.lru.PushFront(e)
	sh.m[key] = e
	sh.bytes += e.size
	for (len(sh.m) > sh.max || sh.bytes > sh.maxBytes) && sh.lru.Len() > 1 {
		sh.drop(sh.lru.Back().Value.(*cacheEntry))
		c.Evicted.Add(1)
	}
	return true
}

// prepare checks a response and makes the copy to keep: TTLs capped, options dropped.  ok is false for
// one that must not be cached.  ttls are the TTL field offsets, life how long the copy may be served.
func (c *respCache) prepare(resp []byte) (msg []byte, ttls []int, life time.Duration, ok bool) {
	h, hok := parseHeader(resp)
	if !hok || !h.qr || h.tc || h.qdcount != 1 || (resp[2]>>3)&0x0F != 0 || (h.rcode != rcodeNoError && h.rcode != rcodeNXDomain) {
		return nil, nil, 0, false
	}
	rrs, _, rok := recordsAfterQuestion(resp)
	if !rok {
		return nil, nil, 0, false
	}
	an := int(binary.BigEndian.Uint16(resp[6:]))
	ns := int(binary.BigEndian.Uint16(resp[8:]))
	msg = append([]byte(nil), resp...)
	// an OPT record may carry a cookie, padding or NSID, none of which belongs to another client: drop them (anything else: no caching)
	for i, rr := range rrs {
		if rr.typ != typeOPT {
			continue
		}
		if i != len(rrs)-1 || rr.end != len(msg) || !optionsOnly(msg[rr.rdataOff:rr.end], optCookie, optNSID, optPadding) {
			return nil, nil, 0, false
		}
		binary.BigEndian.PutUint16(msg[rr.rdataOff-2:], 0)
		msg = msg[:rr.rdataOff]
		rrs[i].end = rr.rdataOff
		rrs[i].rdlen = 0
	}
	min := uint32(c.maxTTL)
	negative := h.rcode == rcodeNXDomain || an == 0
	soaMin := uint32(0xFFFFFFFF)
	for i, rr := range rrs {
		if rr.typ == typeOPT {
			continue
		}
		if rr.typ == typeTSIG || rr.typ == typeTKEY {
			return nil, nil, 0, false
		}
		off := rr.rdataOff - 6
		ttl := binary.BigEndian.Uint32(msg[off:])
		if ttl > c.maxTTL {
			ttl = c.maxTTL
		}
		if rr.typ == typeSOA && i >= an && i < an+ns { // the SOA of a negative answer
			if _, n, err := readName(msg, rr.rdataOff); err == nil {
				if _, n, err = readName(msg, n); err == nil && n+20 <= rr.end {
					if m := binary.BigEndian.Uint32(msg[n+16:]); m < soaMin {
						soaMin = m
					}
					if m := binary.BigEndian.Uint32(msg[n+16:]); negative && m < ttl {
						ttl = m
					}
				}
			}
		}
		binary.BigEndian.PutUint32(msg[off:], ttl)
		if ttl < min {
			min = ttl
		}
		ttls = append(ttls, off)
	}
	if negative {
		if soaMin == 0xFFFFFFFF { // no SOA: no way to know for how long
			return nil, nil, 0, false
		}
	} else if len(ttls) == 0 {
		return nil, nil, 0, false
	}
	if min == 0 {
		return nil, nil, 0, false
	}
	return msg, ttls, time.Duration(min) * time.Second, true
}

// cacheStats is what the DNS views show.
type cacheStats struct {
	On       bool   `json:"on"`
	Entries  int    `json:"entries"`
	Max      int    `json:"max"`
	MaxTTL   int    `json:"max_ttl"`
	Bytes    int    `json:"bytes"`
	Hits     uint64 `json:"hits"`
	Misses   uint64 `json:"misses"`
	Bypassed uint64 `json:"bypassed"`
	Evicted  uint64 `json:"evicted"`
}

func (c *respCache) stats() cacheStats {
	if c == nil {
		return cacheStats{}
	}
	return cacheStats{On: true, Entries: c.Len(), Max: c.max, MaxTTL: int(c.maxTTL), Bytes: c.Bytes(), Hits: c.Hits.Load(), Misses: c.Misses.Load(), Bypassed: c.Bypassed.Load(), Evicted: c.Evicted.Load()}
}
