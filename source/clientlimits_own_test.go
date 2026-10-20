package main

import (
	"container/list"
	"strings"
	"testing"
	"time"
)

func TestOwnClientLimit(t *testing.T) {
	// no pool rate at all: only the one client is limited, and a network that is "never limited" does not excuse it
	l := newClientLimits(limCfg(func(d *DNSConfig) {
		d.ClientLimits = []string{"10.1.1.1/32 5"}
		d.ClientExempt = []string{"10.1.0.0/16"}
	}))
	if l == nil {
		t.Fatal("a client limit alone must switch the limiter on")
	}
	t0 := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	c := mustAddr("10.1.1.1")
	for i := 0; i < 10; i++ { // burst: twice the rate, at least 10
		if v := l.check(c, t0); v != admitOK {
			t.Fatalf("query %d inside the burst: %v", i, v)
		}
	}
	if v := l.check(c, t0); v != admitLimited {
		t.Fatalf("the 11th at once must be limited, got %v", v)
	}
	if v := l.check(c, t0.Add(time.Second)); v != admitOK { // 5 a second
		t.Fatal("a second later 5 queries are allowed again")
	}
	for i := 0; i < 1000; i++ {
		if v := l.check(mustAddr("10.1.1.2"), t0); v != admitOK {
			t.Fatal("another client is not limited")
		}
	}
}

func TestOwnClientLimitBeatsPoolRate(t *testing.T) {
	l := newClientLimits(limCfg(func(d *DNSConfig) {
		d.ClientRate = 1000
		d.ClientLimits = []string{"192.0.2.0/24 2"}
	}))
	t0 := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	n := 0
	for l.check(mustAddr("192.0.2.9"), t0) == admitOK && n < 100 {
		n++
	}
	if n != 10 {
		t.Fatalf("its own limit (burst 10) applies, not the pool's: %d", n)
	}
	n = 0
	for l.check(mustAddr("198.51.100.9"), t0) == admitOK && n < 5000 {
		n++
	}
	if n != 2000 {
		t.Fatalf("others keep the pool's rate (burst 2000): %d", n)
	}
}

func TestClientLimitsValidation(t *testing.T) {
	d := limCfg(func(d *DNSConfig) { d.ClientLimits = []string{"10.1.1.1/32 5", " 10.2.0.0/16   200 ", "2001:db8::5 7"} })
	if got := strings.Join(d.ClientLimits, ","); got != "10.1.1.1 5,10.2.0.0/16 200,2001:db8::5 7" {
		t.Errorf("normalised: %s", got)
	}
	for _, bad := range [][]string{{"10.1.1.1"}, {"10.1.1.1 0"}, {"10.1.1.1 x"}, {"nonsense 5"}, {"10.1.1.1 5 6"}, {"10.1.1.1 5", "10.1.1.1/32 9"}, {"10.1.1.1 99999999"}} {
		x := defaultDNS()
		x.Servers = []string{"127.0.0.1:5353"}
		x.Queries = []DNSQuery{{Name: "a.example", Type: "A"}}
		x.ClientLimits = bad
		if err := x.Validate(); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestCacheFlushName(t *testing.T) {
	c := newRespCache(100, 60)
	put := func(key string) {
		sh := c.shards[0]
		e := &cacheEntry{key: key, sh: sh, size: 10}
		if sh.lru == nil {
			sh.lru = list.New()
		}
		e.elem = sh.lru.PushFront(e)
		sh.m[key] = e
		sh.bytes += e.size
	}
	for _, k := range []string{"a.example|1|1|1|512|", "a.example|28|1|1|512|", "a.example|1|1|33|0|10.0.0.0/24", "b.a.example|1|1|1|512|", "ba.example|1|1|1|512|", "a.example.com|1|1|1|512|"} {
		put(k)
	}
	if n := c.flushName("a.example"); n != 3 {
		t.Fatalf("flushed %d, want the 3 entries of a.example (any type or client network)", n)
	}
	if c.Len() != 3 || c.shards[0].bytes != 30 {
		t.Errorf("left: %d entries, %d bytes; subdomains and similar names must stay", c.Len(), c.shards[0].bytes)
	}
	if n := c.flushName("a.example"); n != 0 {
		t.Errorf("again: %d", n)
	}
}

func TestCacheFlushNameChecked(t *testing.T) {
	if n, err := cacheFlushName(" A.Example. "); err != nil || n != "a.example" {
		t.Errorf("%q %v", n, err)
	}
	for _, bad := range []string{"", "10.0.0.1", "a b", "x|y.example", "-oN"} {
		if _, err := cacheFlushName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLookupFull(t *testing.T) {
	for _, bad := range []string{"", "a b", "x;id", "-x", "224.0.0.1"} {
		if _, err := lookupFull(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	r, err := lookupFull("localhost")
	if err != nil || r.Kind != "name" || !strings.Contains(r.Text, "127.0.0.1") {
		t.Errorf("localhost: %v %+v", err, r)
	}
	r, err = lookupFull("127.0.0.1")
	if err != nil || r.Kind != "address" || r.Text == "" {
		t.Errorf("127.0.0.1: %v %+v", err, r)
	}
}
