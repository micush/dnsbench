package main

import (
	"testing"
	"time"
)

// In a cache of several shards a hit moves its entry to the front of the LRU order at most once a second, so a
// hit on a popular name does not need the shard's write lock; a cache of one shard keeps the exact order.
func TestCacheLRUBumpIsThrottled(t *testing.T) {
	c, clock := newTestCache(cacheShardMin*2, 3600)
	if len(c.shards) < 2 {
		t.Fatalf("want several shards, got %d", len(c.shards))
	}
	q := cacheQuery("bump.example.com", 1, nil)
	k := key(t, c, q, false, "192.0.2.5", false)
	c.put(k, mkResp(q, 0, [][]byte{aRR("bump.example.com", 3000)}, nil, nil))
	sh := c.shardFor(k)
	// a second entry in the same shard goes in front of the first
	var other string
	for i := 0; ; i++ {
		n := "o" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + ".example.com"
		oq := cacheQuery(n, 1, nil)
		ok := key(t, c, oq, false, "192.0.2.5", false)
		if c.shardFor(ok) == sh {
			c.put(ok, mkResp(oq, 0, [][]byte{aRR(n, 3000)}, nil, nil))
			other = ok
			break
		}
	}
	front := func() string { return sh.lru.Front().Value.(*cacheEntry).key }
	if front() != other {
		t.Fatal("the newest entry should be in front")
	}
	if c.get(k, q) == nil {
		t.Fatal("miss")
	}
	if front() != other {
		t.Fatal("a hit within a second of the store must not reorder a multi-shard cache")
	}
	*clock = clock.Add(2 * time.Second)
	if c.get(k, q) == nil {
		t.Fatal("miss")
	}
	if front() != k {
		t.Fatal("a hit after the throttle time must move the entry to the front")
	}
}

func TestCacheSingleShardKeepsExactOrder(t *testing.T) {
	c, _ := newTestCache(10, 3600)
	if len(c.shards) != 1 {
		t.Fatalf("want one shard, got %d", len(c.shards))
	}
	q1, q2 := cacheQuery("a.example.com", 1, nil), cacheQuery("b.example.com", 1, nil)
	k1, k2 := key(t, c, q1, false, "192.0.2.5", false), key(t, c, q2, false, "192.0.2.5", false)
	c.put(k1, mkResp(q1, 0, [][]byte{aRR("a.example.com", 3000)}, nil, nil))
	c.put(k2, mkResp(q2, 0, [][]byte{aRR("b.example.com", 3000)}, nil, nil))
	if c.get(k1, q1) == nil {
		t.Fatal("miss")
	}
	if got := c.shards[0].lru.Front().Value.(*cacheEntry).key; got != k1 {
		t.Fatal("a hit must move the entry to the front at once in a one-shard cache")
	}
}
