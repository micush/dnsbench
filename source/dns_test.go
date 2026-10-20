package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDNS is a UDP+TCP DNS server on loopback with tunable behaviour.
type fakeDNS struct {
	addr  string
	delay atomic.Int64 // ns
	mode  atomic.Int32 // 0 ok, 1 NXDOMAIN, 2 drop, 3 SERVFAIL
	hits  atomic.Int64
	lastQ atomic.Value // the last question name asked (a string)
	only  atomic.Bool  // answer only the probe names (a.example, b.example): a server that cannot resolve anything else
	pc    net.PacketConn
	tl    net.Listener
}

func reply(q []byte, rcode int, answers int) []byte {
	r := errorResponse(q, rcode)
	if answers > 0 {
		binary.BigEndian.PutUint16(r[6:], uint16(answers))
		// one A record pointing back at the question name
		r = append(r, 0xC0, 0x0C, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 1)
	}
	return r
}

func (f *fakeDNS) answer(q []byte) []byte {
	f.hits.Add(1)
	if n, _, ok := questionOf(q); ok {
		f.lastQ.Store(n)
	}
	if d := f.delay.Load(); d > 0 {
		time.Sleep(time.Duration(d))
	}
	if f.only.Load() {
		if n, _, _ := questionOf(q); n != "a.example" && n != "b.example" {
			return nil
		}
	}
	switch f.mode.Load() {
	case 1:
		return reply(q, 3, 0)
	case 2:
		return nil
	case 3:
		return reply(q, rcodeServFail, 0)
	}
	return reply(q, 0, 1)
}

func newFakeDNS(t *testing.T) *fakeDNS {
	t.Helper()
	// the same port for UDP and TCP; another process may already hold the TCP side of the number, so retry
	var pc net.PacketConn
	var tl net.Listener
	for try := 0; ; try++ {
		var err error
		if pc, err = net.ListenPacket("udp4", "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
		port := pc.LocalAddr().(*net.UDPAddr).Port
		if tl, err = net.Listen("tcp4", net.JoinHostPort("127.0.0.1", itoa(port))); err == nil {
			break
		}
		pc.Close()
		if try >= 50 {
			t.Fatal(err)
		}
	}
	f := &fakeDNS{addr: pc.LocalAddr().String(), pc: pc, tl: tl}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := append([]byte(nil), buf[:n]...)
			go func() {
				if r := f.answer(q); r != nil {
					pc.WriteTo(r, from)
				}
			}()
		}
	}()
	go func() {
		for {
			c, err := tl.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var lb [2]byte
				if _, err := io.ReadFull(c, lb[:]); err != nil {
					return
				}
				q := make([]byte, binary.BigEndian.Uint16(lb[:]))
				io.ReadFull(c, q)
				if r := f.answer(q); r != nil {
					out := append([]byte{byte(len(r) >> 8), byte(len(r))}, r...)
					c.Write(out)
				}
			}()
		}
	}()
	t.Cleanup(func() { pc.Close(); tl.Close() })
	return f
}

func testDNSCfg(servers ...string) DNSConfig {
	d := defaultDNS()
	d.Servers = servers
	d.Queries = []DNSQuery{{Name: "a.example", Type: "A"}, {Name: "b.example", Type: "AAAA"}}
	d.ProbeTimeoutMS = 300
	d.QueryTimeoutMS = 300
	d.FailThreshold = 1
	d.LatencyAlpha = 0.5
	return d
}

func rankedAddrs(p *Pool) []string {
	var out []string
	for _, s := range p.Ranked() {
		out = append(out, s.Addr)
	}
	return out
}

func TestRankedFastestFirst(t *testing.T) {
	slow, mid, fast := newFakeDNS(t), newFakeDNS(t), newFakeDNS(t)
	slow.delay.Store(int64(80 * time.Millisecond))
	mid.delay.Store(int64(30 * time.Millisecond))
	p := NewPool(testDNSCfg(slow.addr, mid.addr, fast.addr))
	p.ProbeNow(context.Background())
	got := rankedAddrs(p)
	want := []string{fast.addr, mid.addr, slow.addr}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("ranking = %v, want %v", got, want)
	}

	// Traffic goes to the fastest server only.
	q, _ := buildQuery(0x1234, "www.example", "A")
	for i := 0; i < 5; i++ {
		resp, err := p.Forward(context.Background(), q, false)
		if err != nil {
			t.Fatal(err)
		}
		if h, _ := parseHeader(resp); h.id != 0x1234 || h.ancount != 1 {
			t.Fatalf("bad response %+v", h)
		}
	}
	if slow.hits.Load() != 2 || mid.hits.Load() != 2 { // probe queries only
		t.Fatalf("slow/mid got forwarded traffic: slow=%d mid=%d", slow.hits.Load(), mid.hits.Load())
	}
	if fast.hits.Load() != 2+5 {
		t.Fatalf("fast hits = %d, want 7", fast.hits.Load())
	}
}

func TestLatencyReordersAfterChange(t *testing.T) {
	a, b := newFakeDNS(t), newFakeDNS(t)
	b.delay.Store(int64(40 * time.Millisecond))
	p := NewPool(testDNSCfg(a.addr, b.addr))
	p.ProbeNow(context.Background())
	if r := rankedAddrs(p); r[0] != a.addr {
		t.Fatalf("want a first: %v", r)
	}
	// a degrades, b speeds up; a few probe rounds later the order flips.
	a.delay.Store(int64(120 * time.Millisecond))
	b.delay.Store(0)
	for i := 0; i < 4; i++ {
		p.ProbeNow(context.Background())
	}
	if r := rankedAddrs(p); r[0] != b.addr {
		t.Fatalf("want b first after change: %v", r)
	}
}

func TestHealthGating(t *testing.T) {
	good, nx, dead, partial := newFakeDNS(t), newFakeDNS(t), newFakeDNS(t), newFakeDNS(t)
	nx.mode.Store(1)   // answers NXDOMAIN to the probe → not eligible
	dead.mode.Store(2) // never answers
	_ = partial
	p := NewPool(testDNSCfg(good.addr, nx.addr, dead.addr))
	p.ProbeNow(context.Background())
	if r := rankedAddrs(p); len(r) != 1 || r[0] != good.addr {
		t.Fatalf("only the good server should be eligible, got %v", r)
	}
	for _, s := range p.Snapshot() {
		if s.Addr != good.addr && (s.Healthy || s.LastError == "") {
			t.Fatalf("bad server %s not reported: %+v", s.Addr, s)
		}
	}

	// nx recovers → joins the pool on the next round.
	nx.mode.Store(0)
	p.ProbeNow(context.Background())
	if len(rankedAddrs(p)) != 2 {
		t.Fatalf("recovered server not re-admitted: %v", rankedAddrs(p))
	}
}

func TestForwardFailoverAndMarkDown(t *testing.T) {
	fast, slow := newFakeDNS(t), newFakeDNS(t)
	slow.delay.Store(int64(20 * time.Millisecond))
	p := NewPool(testDNSCfg(fast.addr, slow.addr))
	p.ProbeNow(context.Background())

	fast.mode.Store(2) // fast server dies between probes
	q, _ := buildQuery(7, "x.example", "A")
	resp, err := p.Forward(context.Background(), q, false)
	if err != nil {
		t.Fatalf("expected failover to slow server: %v", err)
	}
	if h, _ := parseHeader(resp); h.ancount != 1 {
		t.Fatal("no answer from fallback")
	}
	// the failed query does not demote it; the next probe does (fail_threshold=1)
	if r := rankedAddrs(p); len(r) != 2 {
		t.Fatalf("a failed query took the server out of service: %v", r)
	}
	p.ProbeNow(context.Background())
	if r := rankedAddrs(p); len(r) != 1 || r[0] != slow.addr {
		t.Fatalf("dead server still ranked: %v", r)
	}
}

func TestServfailTriesNextServer(t *testing.T) {
	a, b := newFakeDNS(t), newFakeDNS(t)
	b.delay.Store(int64(20 * time.Millisecond))
	p := NewPool(testDNSCfg(a.addr, b.addr))
	p.ProbeNow(context.Background())
	a.mode.Store(3)
	q, _ := buildQuery(9, "y.example", "A")
	resp, err := p.Forward(context.Background(), q, false)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := parseHeader(resp); h.rcode != 0 || h.ancount != 1 {
		t.Fatalf("expected b's good answer, got %+v", h)
	}
}

func TestNoHealthyServers(t *testing.T) {
	dead := newFakeDNS(t)
	dead.mode.Store(2)
	p := NewPool(testDNSCfg(dead.addr))
	p.ProbeNow(context.Background())
	q, _ := buildQuery(1, "z.example", "A")
	if _, err := p.Forward(context.Background(), q, false); err != errNoServers {
		t.Fatalf("err = %v", err)
	}
	sf := errorResponse(q, rcodeServFail)
	if h, _ := parseHeader(sf); h.rcode != rcodeServFail || h.id != 1 || !h.qr || h.qdcount != 1 {
		t.Fatalf("bad SERVFAIL %+v", h)
	}
}

func TestFrontendUDPAndTCP(t *testing.T) {
	up := newFakeDNS(t)
	cfg := testDNSCfg(up.addr)
	cfg.Cache = false // this test is about SERVFAIL when every upstream is down; a cached answer would hide it
	p := NewPool(cfg)
	p.ProbeNow(context.Background())

	// pick a free port
	l, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	fe := NewDNSFrontend(mustAddr("127.0.0.1"), port, func() *Pool { return p })
	if err := fe.Start(); err != nil {
		t.Skipf("cannot bind: %v", err)
	}
	defer fe.Stop()

	q, _ := buildQuery(0xBEEF, "front.example", "A")
	srv := net.JoinHostPort("127.0.0.1", itoa(port))

	resp, _, err := exchange(context.Background(), srv, q, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := parseHeader(resp); h.id != 0xBEEF || h.ancount != 1 {
		t.Fatalf("udp: %+v", h)
	}
	resp, _, err = exchange(context.Background(), srv, q, true, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := parseHeader(resp); h.id != 0xBEEF || h.ancount != 1 {
		t.Fatalf("tcp: %+v", h)
	}

	// all upstreams down → client gets SERVFAIL immediately, not a timeout
	up.mode.Store(2)
	p.ProbeNow(context.Background())
	start := time.Now()
	resp, _, err = exchange(context.Background(), srv, q, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := parseHeader(resp); h.rcode != rcodeServFail {
		t.Fatalf("want SERVFAIL, got %+v", h)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("SERVFAIL was not immediate")
	}
}

func TestConfigParsing(t *testing.T) {
	var d DNSConfig
	err := d.UnmarshalJSON([]byte(`{"servers":["10.0.0.53","[2001:db8::53]:5353"],
		"queries":["example.com", "example.com aaaa", {"name":"x.org","type":"mx"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if d.Servers[0] != "10.0.0.53:53" || d.Servers[1] != "[2001:db8::53]:5353" {
		t.Fatalf("servers: %v", d.Servers)
	}
	if d.Queries[1].Type != "AAAA" || d.Queries[2].Type != "MX" || d.ListenPort != 53 {
		t.Fatalf("queries/defaults: %+v", d)
	}
	if err := d.UnmarshalJSON([]byte(`{"serverz":[]}`)); err == nil {
		t.Fatal("typo'd key accepted")
	}
	d2 := defaultDNS()
	d2.Servers = []string{"example.com"}
	if d2.Validate() == nil {
		t.Fatal("hostname server accepted")
	}
}

func TestLogDomainFlips(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)
	a := TestStat{Name: "a.example", Type: "A", OK: true, MS: 1}
	b := TestStat{Name: "b.example", Type: "A", OK: true, MS: 1}
	bad := TestStat{Name: "b.example", Type: "A", Error: "rcode 2"}
	logDomainFlips("s1", nil, []TestStat{a, b}) // first round, all fine: silent
	logDomainFlips("s1", []TestStat{a, b}, []TestStat{a, b})
	if buf.Len() != 0 {
		t.Fatalf("steady state logged: %s", buf.String())
	}
	logDomainFlips("s1", []TestStat{a, b}, []TestStat{a, bad})
	if !strings.Contains(buf.String(), "domain b.example (A) fails: rcode 2") || strings.Contains(buf.String(), "a.example") {
		t.Fatalf("failure: %s", buf.String())
	}
	buf.Reset()
	logDomainFlips("s1", []TestStat{a, bad}, []TestStat{a, bad}) // still failing: silent
	logDomainFlips("s1", []TestStat{a, bad}, []TestStat{a, b})
	if !strings.Contains(buf.String(), "domain b.example (A) answers again") || strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("recovery: %s", buf.String())
	}
	buf.Reset()
	logDomainFlips("s1", nil, []TestStat{a, bad}) // first round with a failure
	if !strings.Contains(buf.String(), "fails") {
		t.Fatalf("first-round failure not logged: %s", buf.String())
	}
}
