package main

import (
	"context"
	"encoding/binary"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"
)

func cacheQuery(name string, qt uint16, mut func(q []byte) []byte) []byte {
	q := make([]byte, 12)
	binary.BigEndian.PutUint16(q, 0x1111)
	q[2] = 0x01 // RD
	binary.BigEndian.PutUint16(q[4:], 1)
	q = append(q, wireName(name)...)
	q = binary.BigEndian.AppendUint16(q, qt)
	q = binary.BigEndian.AppendUint16(q, 1)
	if mut != nil {
		q = mut(q)
	}
	return q
}

// withOPT adds an OPT record with the given UDP size, DO bit and raw options.
func withOPT(size uint16, do bool, opts ...[]byte) func([]byte) []byte {
	return func(q []byte) []byte {
		var rd []byte
		for _, o := range opts {
			rd = append(rd, o...)
		}
		q = append(q, 0)
		q = binary.BigEndian.AppendUint16(q, typeOPT)
		q = binary.BigEndian.AppendUint16(q, size)
		flags := uint32(0)
		if do {
			flags = 0x8000
		}
		q = binary.BigEndian.AppendUint32(q, flags)
		q = binary.BigEndian.AppendUint16(q, uint16(len(rd)))
		q = append(q, rd...)
		binary.BigEndian.PutUint16(q[10:], 1)
		return q
	}
}

func opt(code uint16, data ...byte) []byte {
	b := binary.BigEndian.AppendUint16(nil, code)
	b = binary.BigEndian.AppendUint16(b, uint16(len(data)))
	return append(b, data...)
}

func soaRR(owner string, ttl, minimum uint32) []byte {
	rd := append(wireName("ns."+owner), wireName("hostmaster."+owner)...)
	for _, v := range []uint32{1, 3600, 600, 86400, minimum} {
		rd = binary.BigEndian.AppendUint32(rd, v)
	}
	return rr(owner, typeSOA, ttl, rd)
}

// mkResp builds a response to q with the given records.
func mkResp(q []byte, rcode int, answers, authority, additional [][]byte) []byte {
	qEnd, _ := questionEnd(q)
	r := append([]byte(nil), q[:qEnd]...)
	r[2] = 0x80 | q[2]&0x01 | 0x00
	r[3] = 0x80 | byte(rcode)
	binary.BigEndian.PutUint16(r[4:], 1)
	binary.BigEndian.PutUint16(r[6:], uint16(len(answers)))
	binary.BigEndian.PutUint16(r[8:], uint16(len(authority)))
	binary.BigEndian.PutUint16(r[10:], uint16(len(additional)))
	for _, l := range [][][]byte{answers, authority, additional} {
		for _, x := range l {
			r = append(r, x...)
		}
	}
	return r
}

func aRR(name string, ttl uint32) []byte { return rr(name, typeA, ttl, []byte{192, 0, 2, 1}) }

func ttlsOf(t *testing.T, m []byte) []uint32 {
	t.Helper()
	rrs, _, ok := recordsAfterQuestion(m)
	if !ok {
		t.Fatalf("unparsable message %x", m)
	}
	var out []uint32
	for _, r := range rrs {
		if r.typ != typeOPT {
			out = append(out, binary.BigEndian.Uint32(m[r.rdataOff-6:]))
		}
	}
	return out
}

func newTestCache(max int, maxTTL int) (*respCache, *time.Time) {
	c := newRespCache(max, maxTTL)
	cur := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return cur }
	return c, &cur
}

func key(t *testing.T, c *respCache, q []byte, tcp bool, client string, ecs bool) string {
	t.Helper()
	k, ok := c.keyFor(q, tcp, netip.MustParseAddr(client), ecs, 24, 56)
	if !ok {
		t.Fatalf("query %x should be cacheable", q)
	}
	return k
}

func TestCacheKeys(t *testing.T) {
	c, _ := newTestCache(10, 3600)
	base := key(t, c, cacheQuery("Example.COM", 1, nil), false, "192.0.2.5", false)
	if base != key(t, c, cacheQuery("example.com", 1, nil), false, "198.51.100.9", false) {
		t.Fatal("case and client must not matter without ECS")
	}
	differ := map[string]string{
		"type":       key(t, c, cacheQuery("example.com", 28, nil), false, "192.0.2.5", false),
		"name":       key(t, c, cacheQuery("example.org", 1, nil), false, "192.0.2.5", false),
		"tcp":        key(t, c, cacheQuery("example.com", 1, nil), true, "192.0.2.5", false),
		"edns":       key(t, c, cacheQuery("example.com", 1, withOPT(1232, false)), false, "192.0.2.5", false),
		"do":         key(t, c, cacheQuery("example.com", 1, withOPT(1232, true)), false, "192.0.2.5", false),
		"size":       key(t, c, cacheQuery("example.com", 1, withOPT(4096, false)), false, "192.0.2.5", false),
		"cd":         key(t, c, cacheQuery("example.com", 1, func(q []byte) []byte { q[3] |= 0x10; return q }), false, "192.0.2.5", false),
		"rd cleared": key(t, c, cacheQuery("example.com", 1, func(q []byte) []byte { q[2] = 0; return q }), false, "192.0.2.5", false),
	}
	seen := map[string]string{base: "base"}
	for n, k := range differ {
		if prev, dup := seen[k]; dup {
			t.Errorf("%s has the same key as %s", n, prev)
		}
		seen[k] = n
	}
	// a client cookie changes nothing; its value is not part of the key
	k1 := key(t, c, cacheQuery("example.com", 1, withOPT(1232, false, opt(optCookie, 1, 2, 3, 4, 5, 6, 7, 8))), false, "192.0.2.5", false)
	k2 := key(t, c, cacheQuery("example.com", 1, withOPT(1232, false, opt(optCookie, 8, 7, 6, 5, 4, 3, 2, 1))), false, "192.0.2.5", false)
	if k1 != k2 || k1 != differ["edns"] {
		t.Fatalf("cookie: %q %q %q", k1, k2, differ["edns"])
	}
	// with ECS the client's network is part of it: the same /24 shares, another one does not
	a := key(t, c, cacheQuery("example.com", 1, nil), false, "192.0.2.5", true)
	b := key(t, c, cacheQuery("example.com", 1, nil), false, "192.0.2.200", true)
	d := key(t, c, cacheQuery("example.com", 1, nil), false, "192.0.3.5", true)
	v6a := key(t, c, cacheQuery("example.com", 1, nil), false, "2001:db8:0:1::5", true)
	v6b := key(t, c, cacheQuery("example.com", 1, nil), false, "2001:db8:0:1:ffff::9", true)
	if a != b || a == d || v6a != v6b || a == v6a {
		t.Fatalf("ecs keys: %q %q %q %q %q", a, b, d, v6a, v6b)
	}
}

func TestCacheBypassed(t *testing.T) {
	c, _ := newTestCache(10, 3600)
	cl := netip.MustParseAddr("192.0.2.5")
	bad := map[string][]byte{
		"a response":    func() []byte { q := cacheQuery("a.example", 1, nil); q[2] |= 0x80; return q }(),
		"opcode UPDATE": func() []byte { q := cacheQuery("a.example", 1, nil); q[2] |= opcodeUpdate << 3; return q }(),
		"AXFR":          cacheQuery("a.example", 252, nil),
		"IXFR":          cacheQuery("a.example", 251, nil),
		"two questions": func() []byte { q := cacheQuery("a.example", 1, nil); q[5] = 2; return q }(),
		"client ECS":    cacheQuery("a.example", 1, withOPT(1232, false, opt(optionECS, 0, 1, 24, 0, 192, 0, 2))),
		"NSID option":   cacheQuery("a.example", 1, withOPT(1232, false, opt(optNSID))),
		"EDE option":    cacheQuery("a.example", 1, withOPT(1232, false, opt(15, 0, 1))),
		"another record": cacheQuery("a.example", 1, func(q []byte) []byte {
			q = append(q, aRR("x.example", 1)...)
			binary.BigEndian.PutUint16(q[10:], 1)
			return q
		}),
		"trailing bytes": append(cacheQuery("a.example", 1, nil), 0, 0),
		"short":          {1, 2, 3},
	}
	for n, q := range bad {
		if k, ok := c.keyFor(q, false, cl, false, 24, 56); ok {
			t.Errorf("%s must not be cached (key %q)", n, k)
		}
	}
}

func TestCacheServesWithAgedTTLs(t *testing.T) {
	c, cur := newTestCache(10, 3600)
	q := cacheQuery("Www.Example.com", 1, nil)
	k := key(t, c, q, false, "192.0.2.5", false)
	resp := mkResp(q, 0, [][]byte{aRR("www.example.com", 300)}, nil, [][]byte{aRR("glue.example.com", 60)})
	c.put(k, resp)
	if c.Len() != 1 {
		t.Fatal("not stored")
	}
	*cur = cur.Add(10 * time.Second)
	q2 := cacheQuery("wWw.eXample.COM", 1, func(q []byte) []byte { binary.BigEndian.PutUint16(q, 0xBEEF); return q })
	got := c.get(k, q2)
	if got == nil {
		t.Fatal("miss")
	}
	if h, _ := parseHeader(got); h.id != 0xBEEF || h.rcode != 0 || h.ancount != 1 || !h.qr {
		t.Fatalf("header %+v", h)
	}
	if name, _, _ := questionOf(got); name != "www.example.com" || string(got[13:16]) != "wWw" {
		t.Fatalf("the client's spelling of the name was not kept: %q", got[12:30])
	}
	if tt := ttlsOf(t, got); len(tt) != 2 || tt[0] != 290 || tt[1] != 50 {
		t.Fatalf("ttls %v", tt)
	}
	if tt := ttlsOf(t, resp); tt[0] != 300 {
		t.Fatal("the stored original was changed")
	}
	// the entry lives as long as its shortest record
	*cur = cur.Add(49 * time.Second)
	if c.get(k, q2) == nil {
		t.Fatal("expired too early")
	}
	*cur = cur.Add(2 * time.Second)
	if c.get(k, q2) != nil || c.Len() != 0 {
		t.Fatal("served after its TTL ran out")
	}
}

func TestCacheMaxTTLCapsStoredAndShown(t *testing.T) {
	c, cur := newTestCache(10, 120)
	q := cacheQuery("big.example", 1, nil)
	k := key(t, c, q, false, "192.0.2.5", false)
	c.put(k, mkResp(q, 0, [][]byte{aRR("big.example", 86400)}, nil, nil))
	if tt := ttlsOf(t, c.get(k, q)); tt[0] != 120 {
		t.Fatalf("a day's TTL must be shown as the cap: %v", tt)
	}
	*cur = cur.Add(121 * time.Second)
	if c.get(k, q) != nil {
		t.Fatal("kept longer than the cap")
	}
}

func TestCacheNegativeAnswers(t *testing.T) {
	c, cur := newTestCache(10, 3600)
	q := cacheQuery("nope.example", 1, nil)
	k := key(t, c, q, false, "192.0.2.5", false)

	c.put(k, mkResp(q, rcodeNXDomain, nil, nil, nil))
	if c.Len() != 0 {
		t.Fatal("an NXDOMAIN without SOA says nothing about how long it holds: not cached")
	}
	c.put(k, mkResp(q, rcodeNXDomain, nil, [][]byte{soaRR("example", 3600, 30)}, nil))
	if c.Len() != 1 {
		t.Fatal("NXDOMAIN with SOA not cached")
	}
	got := c.get(k, q)
	if h, _ := parseHeader(got); h.rcode != rcodeNXDomain {
		t.Fatalf("%+v", h)
	}
	if tt := ttlsOf(t, got); tt[0] != 30 {
		t.Fatalf("the SOA's TTL in a negative answer is its minimum: %v", tt)
	}
	*cur = cur.Add(31 * time.Second)
	if c.get(k, q) != nil {
		t.Fatal("negative answer kept past the SOA minimum")
	}
	// no data (NOERROR, nothing in the answer) is a negative answer too
	c.put(k, mkResp(q, 0, nil, [][]byte{soaRR("example", 20, 600)}, nil))
	if c.Len() != 1 {
		t.Fatal("NODATA not cached")
	}
	*cur = cur.Add(19 * time.Second)
	if c.get(k, q) == nil {
		t.Fatal("NODATA dropped early (its SOA TTL is 20 s)")
	}
	// NOERROR, no answer, no SOA (a referral or a broken server): not cached
	c.put(k+"x", mkResp(q, 0, nil, [][]byte{rr("example", typeNS, 300, wireName("ns.example"))}, nil))
	if c.Len() != 1 {
		t.Fatal("a referral was cached")
	}
}

func TestCacheNeverStores(t *testing.T) {
	c, _ := newTestCache(10, 3600)
	q := cacheQuery("x.example", 1, nil)
	good := func() []byte { return mkResp(q, 0, [][]byte{aRR("x.example", 300)}, nil, nil) }
	cases := map[string][]byte{
		"servfail":  mkResp(q, rcodeServFail, nil, nil, nil),
		"refused":   mkResp(q, rcodeRefused, nil, nil, nil),
		"truncated": func() []byte { r := good(); r[2] |= 0x02; return r }(),
		"a query":   func() []byte { r := good(); r[2] &^= 0x80; return r }(),
		"TSIG":      mkResp(q, 0, [][]byte{aRR("x.example", 300)}, nil, [][]byte{rr("key.", typeTSIG, 0, []byte("sig"))}),
		"ttl zero":  mkResp(q, 0, [][]byte{aRR("x.example", 0)}, nil, nil),
		"ede": mkResp(q, 0, [][]byte{aRR("x.example", 300)}, nil, [][]byte{func() []byte {
			o := opt(15, 0, 3)
			b := []byte{0}
			b = binary.BigEndian.AppendUint16(b, typeOPT)
			b = binary.BigEndian.AppendUint16(b, 1232)
			b = binary.BigEndian.AppendUint32(b, 0)
			b = binary.BigEndian.AppendUint16(b, uint16(len(o)))
			return append(b, o...)
		}()}),
		"garbage": {1, 2, 3, 4},
	}
	for n, r := range cases {
		c.put("k-"+n, r)
		if c.Len() != 0 {
			t.Fatalf("%s was cached", n)
		}
	}
	c.put("ok", good())
	if c.Len() != 1 {
		t.Fatal("the good one was not cached")
	}
}

func TestCacheDropsCookieFromStoredAnswer(t *testing.T) {
	c, _ := newTestCache(10, 3600)
	q := cacheQuery("x.example", 1, withOPT(1232, false, opt(optCookie, 1, 2, 3, 4, 5, 6, 7, 8)))
	k := key(t, c, q, false, "192.0.2.5", false)
	o := append(opt(optCookie, 1, 2, 3, 4, 5, 6, 7, 8, 9, 9, 9, 9, 9, 9, 9, 9), opt(optPadding, 0, 0, 0)...)
	optRR := []byte{0}
	optRR = binary.BigEndian.AppendUint16(optRR, typeOPT)
	optRR = binary.BigEndian.AppendUint16(optRR, 1232)
	optRR = binary.BigEndian.AppendUint32(optRR, 0)
	optRR = binary.BigEndian.AppendUint16(optRR, uint16(len(o)))
	optRR = append(optRR, o...)
	c.put(k, mkResp(q, 0, [][]byte{aRR("x.example", 300)}, nil, [][]byte{optRR}))
	got := c.get(k, q)
	if got == nil {
		t.Fatal("not cached")
	}
	rrs, _, ok := recordsAfterQuestion(got)
	if !ok || len(rrs) != 2 || rrs[1].typ != typeOPT || rrs[1].rdlen != 0 || rrs[1].end != len(got) {
		t.Fatalf("OPT not emptied cleanly: %+v ok=%v", rrs, ok)
	}
	if h, _ := parseHeader(got); h.ancount != 1 || binary.BigEndian.Uint16(got[10:]) != 1 {
		t.Fatal("record counts changed")
	}
}

func TestCacheLRU(t *testing.T) {
	c, _ := newTestCache(3, 3600)
	put := func(n string) {
		q := cacheQuery(n+".example", 1, nil)
		c.put(key(t, c, q, false, "192.0.2.5", false), mkResp(q, 0, [][]byte{aRR(n+".example", 300)}, nil, nil))
	}
	have := func(n string) bool {
		q := cacheQuery(n+".example", 1, nil)
		return c.get(key(t, c, q, false, "192.0.2.5", false), q) != nil
	}
	put("a")
	put("b")
	put("c")
	have("a") // a is now the most recently used
	put("d")  // pushes out the least recently used: b
	if !have("a") || have("b") || !have("c") || !have("d") || c.Len() != 3 || c.Evicted.Load() != 1 {
		t.Fatalf("lru: len %d evicted %d", c.Len(), c.Evicted.Load())
	}
}

func TestCacheConcurrent(t *testing.T) {
	c, _ := newTestCache(50, 3600)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				n := "h" + strconv.Itoa((g*7+i)%80) + ".example"
				q := cacheQuery(n, 1, nil)
				k, _ := c.keyFor(q, false, netip.MustParseAddr("192.0.2.5"), false, 24, 56)
				if c.get(k, q) == nil {
					c.put(k, mkResp(q, 0, [][]byte{aRR(n, 300)}, nil, nil))
				}
			}
		}(g)
	}
	wg.Wait()
	if c.Len() > 50 {
		t.Fatalf("%d entries over the limit of 50", c.Len())
	}
}

func TestCacheConfig(t *testing.T) {
	var d DNSConfig
	if err := d.UnmarshalJSON([]byte(`{}`)); err != nil || !d.Cache || d.CacheEntries != cacheDefaultEntries || d.CacheMaxTTL != cacheDefaultMaxTTL {
		t.Fatalf("defaults: %+v %v", d, err)
	}
	if err := d.UnmarshalJSON([]byte(`{"cache":false,"cache_entries":500,"cache_max_ttl":60}`)); err != nil || d.Cache || d.CacheEntries != 500 || d.CacheMaxTTL != 60 {
		t.Fatalf("explicit: %+v %v", d, err)
	}
	d = defaultDNS()
	for _, bad := range []func(*DNSConfig){
		func(d *DNSConfig) { d.CacheEntries = 99 }, func(d *DNSConfig) { d.CacheEntries = cacheMaxEntries + 1 },
		func(d *DNSConfig) { d.CacheMaxTTL = 0 }, func(d *DNSConfig) { d.CacheMaxTTL = cacheMaxTTLLimit + 1 },
	} {
		x := defaultDNS()
		bad(&x)
		if x.Validate() == nil {
			t.Errorf("accepted %+v", x)
		}
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if NewPool(DNSConfig{}).cache != nil {
		t.Fatal("a zero config must not enable the cache")
	}
	if p := NewPool(defaultDNS()); p.cache == nil || p.cache.max != cacheDefaultEntries {
		t.Fatal("default pool has no cache")
	}
}

// through the frontend: the second identical query never reaches the upstream server
func TestFrontendAnswersFromCache(t *testing.T) {
	up := newFakeDNS(t)
	cfg := testDNSCfg(up.addr)
	cfg.ECS = false // with ECS each client network has its own entries; this test shares one answer between clients
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return p })
	fe.ctx, fe.cancel = context.WithCancel(context.Background())
	t.Cleanup(fe.cancel)
	cl := mustAddr("192.0.2.5")

	q1 := cacheQuery("front.example", 1, nil)
	before := up.hits.Load()
	r1 := fe.resolve(q1, false, cl)
	if h, _ := parseHeader(r1); h.rcode != 0 || h.ancount != 1 || up.hits.Load() != before+1 {
		t.Fatalf("first: %+v upstream %d", h, up.hits.Load()-before)
	}
	q2 := cacheQuery("FRONT.example", 1, func(q []byte) []byte { binary.BigEndian.PutUint16(q, 0x7777); return q })
	r2 := fe.resolve(q2, false, mustAddr("198.51.100.1"))
	if h, _ := parseHeader(r2); h.id != 0x7777 || h.rcode != 0 || h.ancount != 1 || up.hits.Load() != before+1 {
		t.Fatalf("second must come from the cache: %+v upstream %d", h, up.hits.Load()-before)
	}
	// another type is another question
	fe.resolve(cacheQuery("front.example", 28, nil), false, cl)
	if up.hits.Load() != before+2 {
		t.Fatalf("AAAA should have gone upstream: %d", up.hits.Load()-before)
	}
	st := p.cache.stats()
	if !st.On || st.Hits != 1 || st.Misses != 2 || st.Entries != 2 {
		t.Fatalf("stats %+v", st)
	}
	if p.Queries.Load() < 3 || p.Answered.Load() < 3 {
		t.Fatalf("the pool counters must include cache hits: %d/%d", p.Queries.Load(), p.Answered.Load())
	}
	// a hit is still a counted client query
	r := qstats.Query(time.Now().Add(-time.Hour), time.Now(), QFilter{})
	if r.Sums.Total < 3 {
		t.Fatalf("statistics missed a cached answer: %+v", r.Sums)
	}
	// upstream failures are not cached
	up.mode.Store(3)
	q3 := cacheQuery("fail.example", 1, nil)
	fe.resolve(q3, false, cl)
	fe.resolve(q3, false, cl)
	if p.cache.Len() != 2 {
		t.Fatalf("a SERVFAIL was cached: %d entries", p.cache.Len())
	}
}

func TestFrontendCacheOff(t *testing.T) {
	up := newFakeDNS(t)
	cfg := testDNSCfg(up.addr)
	cfg.Cache = false
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return p })
	fe.ctx, fe.cancel = context.WithCancel(context.Background())
	t.Cleanup(fe.cancel)
	q := cacheQuery("off.example", 1, nil)
	before := up.hits.Load()
	fe.resolve(q, false, mustAddr("192.0.2.5"))
	fe.resolve(q, false, mustAddr("192.0.2.5"))
	if up.hits.Load() != before+2 || p.cache.stats().On {
		t.Fatal("with the cache off every query goes upstream")
	}
}

// ANY is cached like any other type (it has a key of its own, separate from A): zone transfers still are not.
func TestCacheKeepsAnyAnswers(t *testing.T) {
	c, _ := newTestCache(10, 3600)
	cl := netip.MustParseAddr("192.0.2.5")
	qa, qany := cacheQuery("a.example", 1, nil), cacheQuery("a.example", 255, nil)
	ka, okA := c.keyFor(qa, false, cl, false, 24, 56)
	kany, okAny := c.keyFor(qany, false, cl, false, 24, 56)
	if !okA || !okAny || ka == kany {
		t.Fatalf("keys: %q %v / %q %v", ka, okA, kany, okAny)
	}
	resp := mkResp(qany, 0, [][]byte{aRR("a.example", 300)}, nil, nil)
	c.put(kany, resp)
	if got := c.get(kany, qany); got == nil {
		t.Fatal("an ANY answer was not served from the cache")
	}
	if c.get(ka, qa) != nil {
		t.Fatal("the ANY answer must not answer an A query")
	}
}

// A big cache is split into shards (one lock each); it still serves, expires and
// stays inside its limit, under concurrent use.
func TestCacheShardedStaysBounded(t *testing.T) {
	c, _ := newTestCache(8192, 3600)
	if len(c.shards) != cacheShards {
		t.Fatalf("a cache of 8192 entries must be sharded, has %d shard(s)", len(c.shards))
	}
	if small, _ := newTestCache(100, 3600); len(small.shards) != 1 {
		t.Fatal("a small cache must stay in one shard so its LRU order is exact")
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 3000; i++ {
				n := "h" + strconv.Itoa(g*3000+i) + ".example"
				q := cacheQuery(n, 1, nil)
				k, _ := c.keyFor(q, false, netip.MustParseAddr("192.0.2.5"), false, 24, 56)
				c.put(k, mkResp(q, 0, [][]byte{aRR(n, 300)}, nil, nil))
				if c.get(k, q) == nil && c.Len() < 100 {
					t.Error("an entry just stored must be found")
				}
			}
		}(g)
	}
	wg.Wait()
	if c.Len() > 8192+cacheShards || c.Len() < 6000 || c.Evicted.Load() == 0 {
		t.Fatalf("entries %d, evicted %d: want near the limit of 8192", c.Len(), c.Evicted.Load())
	}
	before := c.Len()
	if d := c.dropOldest(0.25); d == 0 || c.Len() >= before {
		t.Fatalf("dropOldest removed %d", d)
	}
}
