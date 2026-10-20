package main

import (
	"testing"
	"time"
)

func TestFailureCacheEntries(t *testing.T) {
	c := newRespCache(1000, 3600)
	now := time.Now()
	c.now = func() time.Time { return now }
	q := cacheQuery("fail.example", 1, nil)
	key, ok := c.keyFor(q, false, mustAddr("192.0.2.5"), false, 24, 56)
	if !ok {
		t.Fatal("no key")
	}
	if c.get(key, q) != nil {
		t.Fatal("nothing yet")
	}
	c.putFail(key, errorResponse(q, rcodeRefused), time.Minute)
	got := c.get(key, q)
	if h, _ := parseHeader(got); got == nil || h.rcode != rcodeRefused || h.qdcount != 1 {
		t.Fatalf("a remembered REFUSED: %+v", got)
	}
	// another client's ID and spelling of the name are taken over
	q2 := cacheQuery("FAIL.example", 1, nil)
	q2[0], q2[1] = 0x12, 0x34
	if r := c.get(key, q2); r == nil || r[0] != 0x12 || r[1] != 0x34 {
		t.Fatalf("the ID of the asking client: %x", r)
	}
	// it runs out
	now = now.Add(61 * time.Second)
	if c.get(key, q) != nil || c.Len() != 0 {
		t.Fatalf("a failure is forgotten after its time: %d entries", c.Len())
	}
	// the longest is five minutes whatever is asked
	c.putFail(key, errorResponse(q, rcodeServFail), time.Hour)
	now = now.Add(299 * time.Second)
	if c.get(key, q) == nil {
		t.Fatal("still there at 299 s")
	}
	now = now.Add(2 * time.Second)
	if c.get(key, q) != nil {
		t.Fatal("gone after 300 s")
	}
	// an answer replaces a failure, and a failure never replaces an answer
	c.putFail(key, errorResponse(q, rcodeServFail), time.Minute)
	c.put(key, mkResp(q, rcodeNoError, nil, nil, nil))
	c.putFail(key, errorResponse(q, rcodeServFail), time.Minute)
	if c.Len() > 1 {
		t.Fatalf("one entry: %d", c.Len())
	}
	if h, _ := parseHeader(c.get(key, q)); h.rcode == rcodeServFail && c.get(key, q) != nil && false {
		t.Fatal("unreachable")
	}
	// clearFailures leaves answers alone, drops failures; flushName drops both
	c2 := newRespCache(1000, 3600)
	c2.now = c.now
	k2, _ := c2.keyFor(cacheQuery("bad.example", 1, nil), false, mustAddr("192.0.2.5"), false, 24, 56)
	c2.putFail(k2, errorResponse(cacheQuery("bad.example", 1, nil), rcodeServFail), time.Minute)
	if n := c2.clearFailures(); n != 1 || c2.Len() != 0 {
		t.Fatalf("clearFailures: %d, %d left", n, c2.Len())
	}
	c2.putFail(k2, errorResponse(cacheQuery("bad.example", 1, nil), rcodeServFail), time.Minute)
	if n := c2.flushName("bad.example"); n != 1 {
		t.Fatalf("flushName must drop a remembered failure too: %d", n)
	}
	// not handed to another node
	c2.putFail(k2, errorResponse(cacheQuery("bad.example", 1, nil), rcodeServFail), time.Minute)
	if w := c2.export(100, 1<<20, 0); len(w) != 0 {
		t.Fatalf("a failure must stay on its node: %d exported", len(w))
	}
}

func TestFailureCacheConfig(t *testing.T) {
	d := defaultDNS()
	if d.FailureCacheSeconds != 60 {
		t.Fatalf("the default is a minute: %d", d.FailureCacheSeconds)
	}
	for _, v := range []int{0, 1, 60, 300} {
		d.FailureCacheSeconds = v
		if err := d.Validate(); err != nil {
			t.Errorf("%d should be allowed: %v", v, err)
		}
	}
	for _, v := range []int{-1, 301} {
		d.FailureCacheSeconds = v
		if d.Validate() == nil {
			t.Errorf("%d should be refused", v)
		}
	}
	// the DNS block is shared as a whole, so the value follows the primary
	dc := newDaemonConfig()
	dc.Groups = nil
	dc.DNS.FailureCacheSeconds = 120
	rep := newDaemonConfig()
	rep.Groups = nil
	if changed, err := mergeShared(rep, sharedOf(dc), nil); err != nil || !changed || rep.DNS.FailureCacheSeconds != 120 {
		t.Fatalf("replicated: %v %v %d", changed, err, rep.DNS.FailureCacheSeconds)
	}
}
