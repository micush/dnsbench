package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// keyForRef is the cache key function as it was before the key was built without allocating; keyBytes must give
// exactly the same key (or the same refusal) for every query.
func keyForRef(q []byte, tcp bool, client netip.Addr, ecs bool, p4, p6 int) (string, bool) {
	if len(q) < 12 || q[2]&0x80 != 0 || (q[2]>>3)&0x0F != 0 || binary.BigEndian.Uint16(q[4:]) != 1 ||
		binary.BigEndian.Uint16(q[6:]) != 0 || binary.BigEndian.Uint16(q[8:]) != 0 {
		return "", false
	}
	name, qt, ok := questionOf(q)
	if !ok || qt == 251 || qt == 252 {
		return "", false
	}
	qEnd, ok := questionEnd(q)
	if !ok {
		return "", false
	}
	qclass := binary.BigEndian.Uint16(q[qEnd-2:])
	flags := int(q[2] & 0x01)
	flags |= int(q[3]&0x30) >> 3
	size := 0
	if !tcp {
		size = 512
	}
	rrs, _, ok := recordsAfterQuestion(q)
	if !ok || len(rrs) > 1 {
		return "", false
	}
	if len(rrs) == 1 {
		rr := rrs[0]
		if rr.typ != typeOPT {
			return "", false
		}
		if !optionsOnly(q[rr.rdataOff:rr.end], optCookie) {
			return "", false
		}
		flags |= 1 << 4
		if ttl := binary.BigEndian.Uint32(q[rr.rdataOff-6:]); ttl&0x8000 != 0 {
			flags |= 1 << 3
		}
		if !tcp {
			if size = int(rr.class); size < 512 {
				size = 512
			}
		}
	}
	if tcp {
		flags |= 1 << 5
	}
	nw := ""
	if ecs && ecsUsable(client) {
		a := client.Unmap()
		bits := p6
		if a.Is4() {
			bits = p4
		}
		if pf, err := a.Prefix(bits); err == nil {
			nw = pf.String()
		}
	}
	return name + "|" + itoa(int(qt)) + "|" + itoa(int(qclass)) + "|" + itoa(flags) + "|" + itoa(size) + "|" + nw, true
}

func TestKeyBytesMatchesTheReferenceKey(t *testing.T) {
	c := newRespCache(100, 60)
	rng := rand.New(rand.NewPCG(1, 2))
	cookie := []byte{0, 10, 0, 8, 1, 2, 3, 4, 5, 6, 7, 8}
	padding := []byte{0, 12, 0, 2, 0, 0}
	var corpus [][]byte
	for _, name := range []string{"example.com", "WwW.ExAmPlE.CoM", ".", "a.b.c.d.e.f.g.example", strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + ".example"} {
		for _, qt := range []uint16{1, 28, 255, 251, 252, 65} {
			corpus = append(corpus, cacheQuery(name, qt, nil),
				cacheQuery(name, qt, withOPT(1232, true)), cacheQuery(name, qt, withOPT(100, false)),
				cacheQuery(name, qt, withOPT(4096, false, cookie)), cacheQuery(name, qt, withOPT(4096, true, padding)))
		}
	}
	base := cacheQuery("fuzz.example", 1, withOPT(1232, true, cookie))
	for i := 0; i < 3000; i++ {
		q := append([]byte(nil), base...)
		for k := rng.IntN(4); k >= 0; k-- {
			q[rng.IntN(len(q))] = byte(rng.IntN(256))
		}
		if rng.IntN(3) == 0 {
			q = q[:rng.IntN(len(q)+1)]
		}
		corpus = append(corpus, q)
	}
	clients := []netip.Addr{netip.MustParseAddr("192.0.2.9"), netip.MustParseAddr("2001:db8::1"), {}}
	for _, q := range corpus {
		for _, tcp := range []bool{false, true} {
			for _, ecs := range []bool{false, true} {
				for _, cl := range clients {
					wk, wok := keyForRef(q, tcp, cl, ecs, 24, 56)
					gk, gok := c.keyFor(q, tcp, cl, ecs, 24, 56)
					if wk != gk || wok != gok {
						t.Fatalf("query %x tcp=%v ecs=%v client=%v:\n want %q %v\n  got %q %v", q, tcp, ecs, cl, wk, wok, gk, gok)
					}
				}
			}
		}
	}
}

func TestCacheHitAllocatesNoKey(t *testing.T) {
	c := newRespCache(1000, 60)
	q := cacheQuery("alloc.example", 1, withOPT(1232, true))
	var qi qinfo
	parseQuestion(q, &qi)
	var kb [384]byte
	key, ok := c.keyBytes(kb[:0], q, &qi, false, netip.Addr{}, false, 24, 56)
	if !ok {
		t.Fatal("not cacheable")
	}
	c.put(string(key), mkResp(q, 0, [][]byte{aRR("alloc.example", 300)}, nil, nil))
	qEnd := qi.nameEnd + 4
	if c.getBytes(key, q, qEnd) == nil {
		t.Fatal("miss")
	}
	keyAllocs := testing.AllocsPerRun(200, func() {
		var qi qinfo
		parseQuestion(q, &qi)
		var kb [384]byte
		if _, ok := c.keyBytes(kb[:0], q, &qi, false, netip.Addr{}, false, 24, 56); !ok {
			t.Fatal("not cacheable")
		}
	})
	if keyAllocs != 0 {
		t.Fatalf("building the key allocates %v times", keyAllocs)
	}
	// the lookup itself costs only the copy of the answer
	if a := testing.AllocsPerRun(200, func() { _ = c.getBytes(key, q, qEnd) }); a > 1 {
		t.Fatalf("a hit allocates %v times, want 1 (the answer)", a)
	}
}

func TestCacheByteBudget(t *testing.T) {
	c := newRespCache(100, 60) // one shard: a budget of 100 * cacheBytesPerEntry
	big := func(i int) ([]byte, []byte) {
		name := fmt.Sprintf("big%d.example", i)
		q := cacheQuery(name, 16, nil)
		txt := rr(name, 16, 300, append([]byte{255}, make([]byte, 255)...))
		var ans [][]byte
		for k := 0; k < 70; k++ { // about 19 KB
			ans = append(ans, txt)
		}
		return q, mkResp(q, 0, ans, nil, nil)
	}
	for i := 0; i < 100; i++ {
		q, r := big(i)
		k, _ := c.keyFor(q, true, netip.Addr{}, false, 24, 56)
		c.put(k, r)
	}
	budget := 100 * cacheBytesPerEntry
	if c.Bytes() > budget {
		t.Fatalf("the cache holds %d bytes, budget %d", c.Bytes(), budget)
	}
	if c.Len() >= 100 || c.Evicted.Load() == 0 {
		t.Fatalf("large answers did not push anything out: %d entries, %d evicted", c.Len(), c.Evicted.Load())
	}
	// the bookkeeping stays right when entries are dropped by the memory guard
	c.dropOldest(1.0)
	if c.Len() != 0 || c.Bytes() != 0 {
		t.Fatalf("after dropping everything: %d entries, %d bytes", c.Len(), c.Bytes())
	}
	// one answer larger than the whole budget of a one-entry cache is not kept
	c1 := newRespCache(1, 60)
	q, r := big(1)
	k, _ := c1.keyFor(q, true, netip.Addr{}, false, 24, 56)
	c1.put(k, r)
	if c1.Len() != 0 {
		t.Fatal("an answer bigger than the budget was cached")
	}
	// ordinary answers are not affected
	q2 := cacheQuery("small.example", 1, nil)
	k2, _ := c1.keyFor(q2, true, netip.Addr{}, false, 24, 56)
	c1.put(k2, mkResp(q2, 0, [][]byte{aRR("small.example", 300)}, nil, nil))
	if c1.Len() != 1 {
		t.Fatal("an ordinary answer was refused")
	}
}

func nxNoSOA(q []byte) []byte {
	r := append([]byte(nil), q...)
	r[2] |= 0x80
	r[3] = r[3]&0xf0 | rcodeNXDomain
	return r
}

func herdFrontend(t *testing.T, st *udpStub) *DNSFrontend {
	cfg := testDNSCfg(st.addr)
	cfg.ECS = false
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return p })
	fe.ctx, fe.cancel = context.WithCancel(context.Background())
	t.Cleanup(fe.cancel)
	return fe
}

// An answer that cannot be cached (an NXDOMAIN with no SOA) used to be fetched again by every waiting client.
func TestWaitersShareAnUncacheableAnswer(t *testing.T) {
	st := newUDPStub(t, func(q []byte, reply func([]byte)) {
		if name, _, _ := questionOf(q); strings.HasPrefix(name, "herd") {
			time.Sleep(150 * time.Millisecond)
			reply(nxNoSOA(q))
			return
		}
		reply(answerTo(q))
	})
	fe := herdFrontend(t, st)
	before := st.n.Load()
	const clients = 20
	var wg sync.WaitGroup
	bad := make(chan string, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := cacheQuery("HeRd.example", 1, nil)
			q[0], q[1] = byte(i+1), 0xb0
			if i%2 == 1 {
				q = cacheQuery("herd.EXAMPLE", 1, nil) // another spelling: it must come back as sent
				q[0], q[1] = byte(i+1), 0xb0
			}
			r := fe.resolve(q, false, mustAddr("192.0.2.5"))
			h, ok := parseHeader(r)
			if !ok || h.id != uint16(i+1)<<8|0xb0 || h.rcode != rcodeNXDomain || !sameQuestionExact(q, r) {
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

func sameQuestionExact(q, r []byte) bool {
	end, ok := questionEnd(q)
	return ok && len(r) >= end && string(q[12:end]) == string(r[12:end])
}

// When the upstream does not answer, the clients waiting for the same question fail with the first one instead of
// each trying the upstream again.
func TestWaitersShareAFailure(t *testing.T) {
	st := newUDPStub(t, func(q []byte, reply func([]byte)) {
		if name, _, _ := questionOf(q); strings.HasPrefix(name, "dead") {
			return // never answered
		}
		reply(answerTo(q))
	})
	fe := herdFrontend(t, st)
	before := st.n.Load()
	const clients = 15
	var wg sync.WaitGroup
	var servfail atomic.Int32
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := cacheQuery("dead.example", 1, nil)
			q[0], q[1] = byte(i+1), 0xc0
			r := fe.resolve(q, false, mustAddr("192.0.2.5"))
			if h, ok := parseHeader(r); ok && h.rcode == rcodeServFail && h.id == uint16(i+1)<<8|0xc0 {
				servfail.Add(1)
			}
		}(i)
		time.Sleep(time.Millisecond)
	}
	wg.Wait()
	if servfail.Load() != clients {
		t.Fatalf("%d of %d clients got SERVFAIL with their own ID", servfail.Load(), clients)
	}
	if got := st.n.Load() - before; got != 1 {
		t.Fatalf("%d upstream attempts for %d clients asking the same dead question, want 1", got, clients)
	}
}

func startFrontend(t *testing.T, st *udpStub) (*DNSFrontend, string) {
	t.Helper()
	cfg := testDNSCfg(st.addr)
	cfg.Cache = false
	cfg.ECS = false
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	l, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	fe := NewDNSFrontend(mustAddr("127.0.0.1"), port, func() *Pool { return p })
	if err := fe.Start(); err != nil {
		t.Skipf("cannot bind: %v", err)
	}
	t.Cleanup(fe.Stop)
	return fe, net.JoinHostPort("127.0.0.1", itoa(port))
}

// Query buffers are reused between packets: under load every client must still get the answer to its own question.
func TestPooledQueryBuffersNeverMixQueriesUp(t *testing.T) {
	st := newUDPStub(t, func(q []byte, reply func([]byte)) {
		time.Sleep(time.Duration(rand.IntN(3)) * time.Millisecond)
		reply(answerTo(q))
	})
	_, srv := startFrontend(t, st)
	const n = 600
	var wg sync.WaitGroup
	var bad atomic.Int32
	gate := make(chan struct{}, 48) // a burst of hundreds at once overruns the stub's own socket buffer, which is not what is tested
	for i := 0; i < n; i++ {
		wg.Add(1)
		gate <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-gate }()
			q := cacheQuery(fmt.Sprintf("n%d.pool.example", i), 1, nil)
			q[0], q[1] = byte(i>>8), byte(i)
			resp, _, err := exchange(context.Background(), srv, q, false, 5*time.Second)
			switch {
			case err != nil:
				bad.Add(1)
				t.Logf("i=%d error: %v", i, err)
			case !sameQuestion(q, resp):
				bad.Add(1)
				t.Logf("i=%d wrong question: q=%x resp=%x", i, q, resp)
			case binary.BigEndian.Uint16(resp) != uint16(i):
				bad.Add(1)
				t.Logf("i=%d wrong id: %x", i, resp[:2])
			}
		}(i)
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d of %d clients got another client's answer", bad.Load(), n)
	}
}

// Many answers on one TCP connection come back framed correctly from the pooled write buffer.
func TestTCPAnswersFromPooledBuffer(t *testing.T) {
	st := newUDPStub(t, func(q []byte, reply func([]byte)) { reply(answerTo(q)) })
	_, srv := startFrontend(t, st)
	c, err := net.Dial("tcp", srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < 60; i++ {
		q := cacheQuery(fmt.Sprintf("t%d.tcp.example", i), 1, nil)
		q[0], q[1] = 0x77, byte(i)
		msg := append([]byte{byte(len(q) >> 8), byte(len(q))}, q...)
		c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Write(msg); err != nil {
			t.Fatal(err)
		}
		var lb [2]byte
		if _, err := readFull(c, lb[:]); err != nil {
			t.Fatal(err)
		}
		resp := make([]byte, int(lb[0])<<8|int(lb[1]))
		if _, err := readFull(c, resp); err != nil {
			t.Fatal(err)
		}
		if resp[0] != 0x77 || resp[1] != byte(i) || !sameQuestion(q, resp) {
			t.Fatalf("answer %d is wrong: %x", i, resp)
		}
	}
}

func readFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := c.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// BenchmarkCacheHit compares the lookup of a cached answer the old way (parse the question and the records,
// build the key as a string, look it up, work out the question end of the stored answer) with the current one.
func BenchmarkCacheHit(b *testing.B) {
	c := newRespCache(1000, 60)
	q := cacheQuery("bench.example.com", 1, withOPT(1232, true))
	k, _ := c.keyFor(q, false, netip.Addr{}, false, 24, 56)
	c.put(k, mkResp(q, 0, [][]byte{aRR("bench.example.com", 300)}, nil, nil))
	b.Run("string key", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			key, _ := keyForRef(q, false, netip.Addr{}, false, 24, 56)
			_ = c.get(key, q)
		}
	})
	b.Run("bytes key", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var qi qinfo
			parseQuestion(q, &qi)
			var kb [384]byte
			key, _ := c.keyBytes(kb[:0], q, &qi, false, netip.Addr{}, false, 24, 56)
			_ = c.getBytes(key, q, qi.nameEnd+4)
		}
	})
}
