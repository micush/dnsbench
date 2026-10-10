package main

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func cachePut(t *testing.T, c *respCache, name string, ttl uint32) (string, []byte) {
	t.Helper()
	q := cacheQuery(name, 1, nil)
	k := key(t, c, q, false, "192.0.2.5", false)
	c.put(k, mkResp(q, 0, [][]byte{aRR(name, ttl)}, nil, nil))
	return k, q
}

// A pool rebuilt because its servers changed keeps the cache; changing the cache itself starts a new one.
func TestCacheSurvivesAPoolRebuild(t *testing.T) {
	a, b := newFakeDNS(t), newFakeDNS(t)
	d := testDNSCfg(a.addr)
	g := defaultGroup()
	g.Interface, g.DNSProxy = "ddgwnone0", true
	dc := newDaemonConfig()
	dc.DNS = d
	dc.Groups = []GroupConfig{g}
	s := NewSupervisor(context.Background(), dc)
	t.Cleanup(s.StopAll)
	s.refreshPool(dc, true)
	p1 := s.poolFor(1)
	k, q := cachePut(t, p1.cache, "www.example.com", 300)

	// a server is added: a new pool, the same cache with its entry
	nu := *dc
	nu.DNS = testDNSCfg(a.addr, b.addr)
	s.refreshPool(&nu, true)
	p2 := s.poolFor(1)
	if p2 == p1 {
		t.Fatal("adding a server must build a new pool")
	}
	if p2.cache != p1.cache || p2.cache.get(k, q) == nil {
		t.Fatal("the cache did not follow the rebuilt pool")
	}

	// a server is removed (or paused: the same to the pool): still there
	nu2 := *dc
	nu2.DNS = testDNSCfg(b.addr)
	s.refreshPool(&nu2, true)
	if p3 := s.poolFor(1); p3 == p2 || p3.cache != p1.cache {
		t.Fatal("removing a server must keep the cache")
	}

	// a different cache size, TTL limit or client-subnet setting: empty
	for name, mut := range map[string]func(*DNSConfig){
		"size":  func(c *DNSConfig) { c.CacheEntries = 500 },
		"ttl":   func(c *DNSConfig) { c.CacheMaxTTL = 60 },
		"ecs":   func(c *DNSConfig) { c.ECSPrefix4 = 16 },
		"ecsOn": func(c *DNSConfig) { c.ECS = false },
	} {
		n := *dc
		n.DNS = testDNSCfg(b.addr)
		mut(&n.DNS)
		old := s.poolFor(1).cache
		s.refreshPool(&n, true)
		if c := s.poolFor(1).cache; c == old || c.Len() != 0 {
			t.Fatalf("%s changed but the cache was kept", name)
		}
		s.refreshPool(&nu2, true) // back, for the next one (a new cache again)
		cachePut(t, s.poolFor(1).cache, "www.example.com", 300)
	}

	// switched off and on again: empty
	off := *dc
	off.DNS = testDNSCfg(b.addr)
	off.DNS.Cache = false
	s.refreshPool(&off, true)
	if s.poolFor(1).cache != nil {
		t.Fatal("cache off must mean no cache")
	}
	s.refreshPool(&nu2, true)
	if s.poolFor(1).cache == nil || s.poolFor(1).cache.Len() != 0 {
		t.Fatal("on again must start empty")
	}
}

func TestCacheExportAndLoad(t *testing.T) {
	src, srcNow := newTestCache(100, 3600)
	for i := 0; i < 5; i++ {
		cachePut(t, src, "n"+strconv.Itoa(i)+".example.com", 300)
	}
	cachePut(t, src, "short.example.com", 3) // would expire within the warm-start margin
	*srcNow = srcNow.Add(2 * time.Second)
	ex := src.export(100, 1<<20, cacheWarmMinLeft)
	if len(ex) != 5 {
		t.Fatalf("exported %d entries, want the 5 with time left", len(ex))
	}
	if ex[0].Age != 2000 {
		t.Fatalf("age %d ms, want 2000", ex[0].Age)
	}
	if got := src.export(2, 1<<20, cacheWarmMinLeft); len(got) != 2 {
		t.Fatalf("count cap: %d", len(got))
	}
	if got := src.export(100, 1, cacheWarmMinLeft); len(got) != 0 {
		t.Fatalf("byte cap: %d", len(got))
	}

	dst, dstNow := newTestCache(100, 3600)
	if n := dst.load(ex); n != 5 || dst.Len() != 5 {
		t.Fatalf("loaded %d, holds %d", n, dst.Len())
	}
	// the TTL goes on counting from the age it had: 300 s entry, 2 s old there, 10 s more here
	*dstNow = dstNow.Add(10 * time.Second)
	q := cacheQuery("n0.example.com", 1, nil)
	k := key(t, dst, q, false, "192.0.2.5", false)
	got := dst.get(k, q)
	if got == nil {
		t.Fatal("a loaded entry is not served")
	}
	if tt := ttlsOf(t, got); len(tt) != 1 || tt[0] != 288 {
		t.Fatalf("ttl %v, want 288", tt)
	}
	*dstNow = dstNow.Add(300 * time.Second)
	if dst.get(k, q) != nil {
		t.Fatal("served after its TTL ran out")
	}

	// an entry this node has is never replaced, and a full cache takes nothing more
	full, _ := newTestCache(100, 3600)
	k0, _ := cachePut(t, full, "n0.example.com", 100)
	if n := full.load(ex); n != 4 {
		t.Fatalf("loaded %d on top of an existing entry, want 4", n)
	}
	if tt := ttlsOf(t, full.get(k0, cacheQuery("n0.example.com", 1, nil))); tt[0] != 100 {
		t.Fatalf("an entry of this node was replaced: %v", tt)
	}
	tiny, _ := newTestCache(100, 3600)
	tiny.shards[0].max = 2
	if n := tiny.load(ex); n != 2 || tiny.Len() != 2 {
		t.Fatalf("a full shard took %d", n)
	}
	// a taken-over entry is the first to go when clients fill the cache
	lru, _ := newTestCache(100, 3600)
	lru.shards[0].max = 3
	lru.load(ex[:2])
	cachePut(t, lru, "x1.example.com", 300)
	cachePut(t, lru, "x2.example.com", 300)
	if lru.Len() != 3 {
		t.Fatalf("holds %d", lru.Len())
	}

	// rubbish is skipped, not stored: no key, a message that is too short or not an answer, a negative or an expired age
	bad := []cacheWireEntry{{Key: "", Msg: ex[0].Msg}, {Key: "k", Msg: []byte{1, 2, 3}}, {Key: "k2", Msg: make([]byte, 40)},
		{Key: ex[0].Key, Msg: ex[0].Msg, Age: -5}, {Key: ex[1].Key, Msg: ex[1].Msg, Age: 3600 * 1000}}
	chk, _ := newTestCache(100, 3600)
	if n := chk.load(bad); n != 0 || chk.Len() != 0 {
		t.Fatalf("rubbish loaded: %d", n)
	}
	if n := chk.load(append(bad, ex[2])); n != 1 {
		t.Fatalf("one good entry among rubbish: %d", n)
	}
}

// A node that has just started fills its cache from a peer through the real cluster call.
func TestWarmStartFromAPeer(t *testing.T) {
	a, b := twoNodeCluster(t)
	d := testDNSCfg("127.0.0.1:5353")
	pa, pb := NewPool(d), NewPool(d)
	a.mg.poolOf = func(gid int) *Pool {
		if gid == 1 {
			return pa
		}
		return nil
	}
	b.mg.poolOf = func(gid int) *Pool {
		if gid == 1 {
			return pb
		}
		return nil
	}
	for i := 0; i < 50; i++ {
		cachePut(t, pa.cache, "w"+strconv.Itoa(i)+".example.com", 600)
	}
	ctx := context.Background()
	if !b.mg.warmOne(ctx, 1) {
		t.Fatal("nothing taken from the peer")
	}
	if pb.cache.Len() != 50 {
		t.Fatalf("B holds %d entries, want 50", pb.cache.Len())
	}
	q := cacheQuery("w7.example.com", 1, nil)
	if pb.cache.get(key(t, pb.cache, q, false, "192.0.2.5", false), q) == nil {
		t.Fatal("a taken-over entry is not served")
	}
	// a peer that is cold too, or a gateway it does not have: nothing, and no error
	if (&Mgmt{poolOf: func(int) *Pool { return nil }}).cacheExport(1).Entries != nil {
		t.Fatal("no pool must export nothing")
	}
	if a.mg.warmOne(ctx, 1) != true {
		t.Fatal("a node whose peer has the same entries has nothing more to take, which still counts as warm")
	}

	// other cache settings on the peer: its entries are not used
	d2 := d
	d2.CacheMaxTTL = 60
	pc := NewPool(d2)
	b.mg.poolOf = func(int) *Pool { return pc }
	if b.mg.warmOne(ctx, 1) || pc.cache.Len() != 0 {
		t.Fatal("entries of a cache with other settings must not be taken")
	}
	// a gateway with the cache off asks nobody
	d3 := d
	d3.Cache = false
	b.mg.poolOf = func(int) *Pool { return NewPool(d3) }
	if !b.mg.warmOne(ctx, 1) {
		t.Fatal("a pool without a cache has nothing to fill")
	}
}
