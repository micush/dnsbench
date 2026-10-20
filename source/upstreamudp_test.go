package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// udpStub is a UDP server whose answer to each packet is decided by fn (nil result = no answer); it records the
// source address of every packet and how many it saw.
type udpStub struct {
	pc    net.PacketConn
	addr  string
	n     atomic.Int64
	mu    sync.Mutex
	froms map[string]bool
}

func newUDPStub(t *testing.T, fn func(q []byte, reply func([]byte))) *udpStub {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &udpStub{pc: pc, addr: pc.LocalAddr().String(), froms: map[string]bool{}}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			s.n.Add(1)
			s.mu.Lock()
			s.froms[from.String()] = true
			s.mu.Unlock()
			q := append([]byte(nil), buf[:n]...)
			go fn(q, func(r []byte) { pc.WriteTo(r, from) })
		}
	}()
	t.Cleanup(func() { pc.Close(); upstreamSockets.closeAll() })
	return s
}

func answerTo(q []byte) []byte {
	r := append([]byte(nil), q...)
	r[2] |= 0x80
	r[7] = 1 // one answer
	return append(r, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 1)
}

func TestUpstreamUDPSocketIsReused(t *testing.T) {
	st := newUDPStub(t, func(q []byte, reply func([]byte)) { reply(answerTo(q)) })
	for i := 0; i < 8; i++ {
		q := cacheQuery("reuse.example", 1, nil)
		resp, _, err := exchangeOpt(context.Background(), st.addr, q, false, time.Second, false)
		if err != nil || len(resp) < len(q) {
			t.Fatalf("exchange %d: %v", i, err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.froms) != 1 {
		t.Fatalf("8 sequential queries used %d source ports, want 1", len(st.froms))
	}
}

// A late answer for another question that carries the same ID (what a reused socket can receive) is not this
// query's answer.
func TestUpstreamUDPIgnoresAnswerToAnotherQuestion(t *testing.T) {
	st := newUDPStub(t, func(q []byte, reply func([]byte)) {
		other := cacheQuery("other.example", 1, nil)
		other[0], other[1] = q[0], q[1]
		reply(answerTo(other)) // same ID, different question
		time.Sleep(20 * time.Millisecond)
		reply(answerTo(q))
	})
	q := cacheQuery("wanted.example", 1, nil)
	resp, _, err := exchangeOpt(context.Background(), st.addr, q, false, time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	if !sameQuestion(q, resp) {
		t.Fatalf("took the answer to another question: %x", resp)
	}
}

// An error answer without a question section (some servers send FORMERR that way) still ends the wait.
func TestUpstreamUDPAcceptsErrorAnswerWithoutQuestion(t *testing.T) {
	st := newUDPStub(t, func(q []byte, reply func([]byte)) {
		r := append([]byte(nil), q[:12]...)
		r[2] |= 0x80
		r[3] = 1 // FORMERR
		r[4], r[5] = 0, 0
		reply(r)
	})
	resp, _, err := exchangeOpt(context.Background(), st.addr, cacheQuery("x.example", 1, nil), false, time.Second, false)
	if err != nil || resp[3]&0x0f != 1 {
		t.Fatalf("%v %x", err, resp)
	}
}

// A socket that timed out is not put back (its late answer would meet the next query).
func TestUpstreamUDPTimedOutSocketIsNotReused(t *testing.T) {
	var drop atomic.Bool
	drop.Store(true)
	st := newUDPStub(t, func(q []byte, reply func([]byte)) {
		if !drop.Load() {
			reply(answerTo(q))
		}
	})
	if _, _, err := exchangeOpt(context.Background(), st.addr, cacheQuery("t.example", 1, nil), false, 80*time.Millisecond, false); err == nil {
		t.Fatal("expected a timeout")
	}
	drop.Store(false)
	if _, _, err := exchangeOpt(context.Background(), st.addr, cacheQuery("t.example", 1, nil), false, time.Second, false); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.froms) != 2 {
		t.Fatalf("source ports %d, want 2 (the timed-out socket must be discarded)", len(st.froms))
	}
}

// Identical queries that miss the cache while one is already on its way upstream wait for it: one upstream query,
// every client answered with its own ID.
func TestIdenticalMissesShareOneUpstreamQuery(t *testing.T) {
	st := newUDPStub(t, func(q []byte, reply func([]byte)) {
		time.Sleep(150 * time.Millisecond)
		reply(answerTo(q))
	})
	cfg := testDNSCfg(st.addr)
	cfg.ECS = false
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return p })
	fe.ctx, fe.cancel = context.WithCancel(context.Background())
	t.Cleanup(fe.cancel)
	before := st.n.Load()
	const clients = 20
	var wg sync.WaitGroup
	bad := make(chan string, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := cacheQuery("herd.example", 1, nil)
			q[0], q[1] = byte(i+1), byte(0xa0)
			r := fe.resolve(q, false, mustAddr("192.0.2.5"))
			if h, ok := parseHeader(r); !ok || h.id != uint16(i+1)<<8|0xa0 || h.rcode != 0 || h.ancount != 1 {
				bad <- fmt.Sprintf("bad answer: i=%d %x", i, r)
			}
		}(i)
		time.Sleep(2 * time.Millisecond)
	}
	wg.Wait()
	close(bad)
	for b := range bad {
		t.Fatal(b)
	}
	if got := st.n.Load() - before; got != 1 {
		t.Fatalf("%d upstream queries for %d identical clients, want 1", got, clients)
	}
	if len(fe.flight) != 0 {
		t.Fatalf("flight table not emptied: %d", len(fe.flight))
	}
}

// With every UDP worker busy on a slow upstream, further queries still proceed at once (a goroutine each) instead
// of queueing behind them: 150 queries against an upstream that takes a second all finish in about that time.
func TestSlowUpstreamDoesNotQueueQueriesBehindTheWorkers(t *testing.T) {
	st := newUDPStub(t, func(q []byte, reply func([]byte)) {
		time.Sleep(time.Second)
		reply(answerTo(q))
	})
	cfg := testDNSCfg(st.addr)
	cfg.Cache = false
	cfg.ProbeTimeoutMS, cfg.QueryTimeoutMS = 5000, 5000
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	l, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	fe := NewDNSFrontend(mustAddr("127.0.0.1"), port, func() *Pool { return p })
	if err := fe.Start(); err != nil {
		t.Skipf("cannot bind: %v", err)
	}
	defer fe.Stop()
	srv := net.JoinHostPort("127.0.0.1", itoa(port))
	const n = 150
	var wg sync.WaitGroup
	var failed atomic.Int64
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := cacheQuery("slow.example", 1, nil)
			q[0], q[1] = byte(i>>8), byte(i)
			resp, _, err := exchange(context.Background(), srv, q, false, 6*time.Second)
			if err != nil {
				failed.Add(1)
				return
			}
			if h, _ := parseHeader(resp); h.rcode != 0 {
				failed.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if failed.Load() != 0 {
		t.Fatalf("%d of %d queries failed", failed.Load(), n)
	}
	if d := time.Since(start); d > 2500*time.Millisecond { // queued behind 64 workers it would be three rounds of a second
		t.Fatalf("%d queries took %v: they queued behind busy workers", n, d)
	}
}

// Another process holding the port without SO_REUSEPORT still makes Start fail, as before.
func TestFrontendStartFailsWhenPortIsTaken(t *testing.T) {
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.LocalAddr().(*net.UDPAddr).Port
	p := NewPool(testDNSCfg("127.0.0.1:9"))
	fe := NewDNSFrontend(mustAddr("127.0.0.1"), port, func() *Pool { return p })
	if err := fe.Start(); err == nil {
		fe.Stop()
		t.Fatal("Start succeeded on a port another socket holds")
	}
}

// The idle sockets live in shards, but a socket given in one is found by a caller that starts in another, and the
// per-upstream cap still holds across all of them.
func TestUpstreamSocketsShardsShareAndCap(t *testing.T) {
	u := newUDPUpstreams()
	mk := func() *net.UDPConn {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	defer u.closeAll()
	c1 := mk()
	u.give("up:53", c1)
	if got := u.take("up:53"); got != c1 {
		t.Fatalf("a socket given must be found by the next caller, whichever shard it starts in (got %v)", got)
	}
	if u.take("up:53") != nil || u.take("other:53") != nil {
		t.Fatal("nothing left to take")
	}
	for i := 0; i < udpIdleMax+40; i++ {
		u.give("up:53", mk())
	}
	n := 0
	for u.take("up:53") != nil {
		n++
	}
	if n == 0 || n > udpIdleMax {
		t.Fatalf("kept %d idle sockets, want between 1 and %d", n, udpIdleMax)
	}
}

// Many goroutines taking and giving at once (run with -race) never hand one socket to two callers.
func TestUpstreamSocketsConcurrent(t *testing.T) {
	u := newUDPUpstreams()
	defer u.closeAll()
	var mu sync.Mutex
	inUse := map[*net.UDPConn]bool{}
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				c := u.take("up:53")
				if c == nil {
					var err error
					if c, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}); err != nil {
						t.Error(err)
						return
					}
				}
				mu.Lock()
				if inUse[c] {
					mu.Unlock()
					t.Error("one socket handed to two callers")
					return
				}
				inUse[c] = true
				mu.Unlock()
				mu.Lock()
				delete(inUse, c)
				mu.Unlock()
				u.give("up:53", c)
			}
		}()
	}
	wg.Wait()
}

// The upstream must not see the client's own transaction ID, and the client must get its ID back.
func TestUpstreamSeesRandomIDAndClientGetsItsOwnBack(t *testing.T) {
	var mu sync.Mutex
	seen := map[uint16]bool{}
	st := newUDPStub(t, func(q []byte, reply func([]byte)) {
		mu.Lock()
		seen[uint16(q[0])<<8|uint16(q[1])] = true
		mu.Unlock()
		reply(answerTo(q))
	})
	const n = 64
	for i := 0; i < n; i++ {
		q := cacheQuery("id.example", 1, nil)
		q[0], q[1] = 0x12, 0x34
		resp, _, err := exchangeOpt(context.Background(), st.addr, q, false, time.Second, false)
		if err != nil {
			t.Fatal(err)
		}
		if resp[0] != 0x12 || resp[1] != 0x34 {
			t.Fatalf("the client's ID did not come back: %x", resp[:2])
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < n/2 {
		t.Fatalf("the upstream saw only %d different IDs in %d queries", len(seen), n)
	}
	if seen[0x1234] && len(seen) < 2 {
		t.Fatal("the client's ID went upstream unchanged")
	}
}

// A busy socket is retired after udpMaxUses exchanges, so its source port does not live for ever.
func TestUpstreamSocketIsRetiredAfterManyUses(t *testing.T) {
	st := newUDPStub(t, func(q []byte, reply func([]byte)) { reply(answerTo(q)) })
	for i := 0; i < udpMaxUses+10; i++ {
		if _, _, err := exchangeOpt(context.Background(), st.addr, cacheQuery("rot.example", 1, nil), false, time.Second, false); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.froms) < 2 {
		t.Fatalf("one source port carried %d exchanges", udpMaxUses+10)
	}
}
