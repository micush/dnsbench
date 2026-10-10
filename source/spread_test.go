package main

import (
	"context"
	"testing"
	"time"
)

func spreadServers(n int) []*Server {
	var out []*Server
	for i := 0; i < n; i++ {
		out = append(out, &Server{Addr: "s" + itoa(i)})
	}
	return out
}

func names(ss []*Server) string {
	var b []byte
	for _, s := range ss {
		b = append(b, s.Addr...)
		b = append(b, ' ')
	}
	return string(b)
}

func TestSpreadOrderBand(t *testing.T) {
	ss := spreadServers(5)
	lat := []float64{10, 11, 12.5, 13, 30}
	// 12.5 is 25% above 10, so only s0 and s1 are in a 20% band; the rest stay behind in rank order.
	for turn, want := range []string{"s0 s1 s2 s3 s4 ", "s1 s0 s2 s3 s4 ", "s0 s1 s2 s3 s4 ", "s1 s0 s2 s3 s4 "} {
		if got := names(spreadOrder(ss, lat, 20, uint64(turn))); got != want {
			t.Fatalf("turn %d: %q, want %q", turn, got, want)
		}
	}
	// a wider band takes s2 and s3 in, s4 (30 ms) is still a fallback
	ss = spreadServers(5)
	for turn, want := range []string{"s0 s1 s2 s3 s4 ", "s1 s2 s3 s0 s4 ", "s2 s3 s0 s1 s4 ", "s3 s0 s1 s2 s4 ", "s0 s1 s2 s3 s4 "} {
		if got := names(spreadOrder(ss, lat, 30, uint64(turn))); got != want {
			t.Fatalf("band 30 turn %d: %q, want %q", turn, got, want)
		}
	}
	// the limit itself is inside the band (10 * 1.5 = 15)
	ss = spreadServers(5)
	if got := names(spreadOrder(ss[:2], []float64{10, 15}, 50, 0)); got != "s0 s1 " {
		t.Fatalf("edge of the band: %q", got)
	}
	// nobody near the fastest: the order is untouched, whatever the turn
	for turn := uint64(0); turn < 4; turn++ {
		if got := names(spreadOrder(ss[:3], []float64{1, 50, 60}, 20, turn)); got != "s0 s1 s2 " {
			t.Fatalf("lone fastest server: %q", got)
		}
	}
	// the input slice is not modified
	if names(ss) != "s0 s1 s2 s3 s4 " {
		t.Fatalf("input reordered: %s", names(ss))
	}
	// turn counters far beyond the band size still land in range
	if got := names(spreadOrder(ss, lat, 30, 1<<63+5)); len(got) != len("s0 s1 s2 s3 s4 ") {
		t.Fatalf("huge turn: %q", got)
	}
}

func setLatency(p *Pool, ms ...float64) {
	for i, s := range p.servers {
		s.mu.Lock()
		s.healthy, s.ewma = true, ms[i]
		s.mu.Unlock()
	}
	p.rankGen.Add(1) // the forwarding path reuses its ranking until a server goes up or down
}

func TestCandidatesSpreadOffKeepsFastestFirst(t *testing.T) {
	cfg := testDNSCfg("127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3")
	cfg.Spread = false
	p := NewPool(cfg)
	setLatency(p, 10, 11, 12)
	for i := 0; i < 6; i++ {
		if c := p.Candidates(); c[0].Addr != "127.0.0.1:1" || len(c) != 3 {
			t.Fatalf("spread off changed the order: %s", names(c))
		}
	}
}

func TestCandidatesSpreadTakesTurns(t *testing.T) {
	cfg := testDNSCfg("127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3", "127.0.0.1:4")
	cfg.Spread = true
	p := NewPool(cfg)
	setLatency(p, 10, 11, 12, 80)
	first := map[string]int{}
	for i := 0; i < 30; i++ {
		c := p.Candidates()
		if len(c) != 4 || c[3].Addr != "127.0.0.1:4" {
			t.Fatalf("the slow server must stay last: %s", names(c))
		}
		first[c[0].Addr]++
	}
	if first["127.0.0.1:1"] != 10 || first["127.0.0.1:2"] != 10 || first["127.0.0.1:3"] != 10 || first["127.0.0.1:4"] != 0 {
		t.Fatalf("turns = %v", first)
	}
	// an unhealthy server is not a candidate at all, the other two share the load
	p.servers[1].mu.Lock()
	p.servers[1].healthy = false
	p.servers[1].mu.Unlock()
	p.rankGen.Add(1) // what the probe and the forwarding path do when a server goes down
	first = map[string]int{}
	for i := 0; i < 20; i++ {
		first[p.Candidates()[0].Addr]++
	}
	if first["127.0.0.1:1"] != 10 || first["127.0.0.1:3"] != 10 {
		t.Fatalf("after one went down: %v", first)
	}
}

func TestSpreadSharesRealQueries(t *testing.T) {
	a, b, c, slow := newFakeDNS(t), newFakeDNS(t), newFakeDNS(t), newFakeDNS(t)
	for _, f := range []*fakeDNS{a, b, c} {
		f.delay.Store(int64(20 * time.Millisecond))
	}
	slow.delay.Store(int64(120 * time.Millisecond))
	cfg := testDNSCfg(a.addr, b.addr, c.addr, slow.addr)
	cfg.Spread = true
	cfg.SpreadBand = 60
	cfg.QueryTimeoutMS = 1000
	cfg.ProbeTimeoutMS = 1000
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	q, _ := buildQuery(0x4242, "www.example", "A")
	for i := 0; i < 30; i++ {
		resp, err := p.Forward(context.Background(), q, false)
		if err != nil {
			t.Fatal(err)
		}
		if h, _ := parseHeader(resp); h.ancount != 1 {
			t.Fatalf("bad answer %+v", h)
		}
	}
	// 2 probe queries each plus 10 turns each; the slow server saw only its probes
	for _, f := range []*fakeDNS{a, b, c} {
		if n := f.hits.Load(); n < 2+8 || n > 2+12 {
			t.Fatalf("a fast server got %d queries, want about 12", n)
		}
	}
	if n := slow.hits.Load(); n != 2 {
		t.Fatalf("the slow server got forwarded traffic: %d", n)
	}
}

func TestSpreadFailsOverInsideBand(t *testing.T) {
	a, b := newFakeDNS(t), newFakeDNS(t)
	cfg := testDNSCfg(a.addr, b.addr)
	cfg.Spread = true
	cfg.SpreadBand = 1000 // both are surely within it
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	if len(p.Candidates()) != 2 {
		t.Fatal("both servers should be eligible")
	}
	a.mode.Store(2) // a stops answering between probes
	q, _ := buildQuery(5, "x.example", "A")
	for i := 0; i < 6; i++ { // whichever turn it is, the client gets b's answer
		resp, err := p.Forward(context.Background(), q, false)
		if err != nil {
			t.Fatalf("query %d: %v", i, err)
		}
		if h, _ := parseHeader(resp); h.ancount != 1 {
			t.Fatalf("query %d: no answer", i)
		}
	}
	p.ProbeNow(context.Background()) // only the probe takes it out
	if r := rankedAddrs(p); len(r) != 1 || r[0] != b.addr {
		t.Fatalf("the dead server is still eligible: %v", r)
	}
	// a SERVFAIL from the one whose turn it is hands over as well
	b.mode.Store(0)
	a.mode.Store(3)
	p.ProbeNow(context.Background()) // a is not healthy while it SERVFAILs the probes
	if len(rankedAddrs(p)) != 1 {
		t.Fatalf("servfailing server should be out: %v", rankedAddrs(p))
	}
}

func TestSpreadConfig(t *testing.T) {
	d := defaultDNS()
	if !d.Spread || d.SpreadBand != 20 {
		t.Fatalf("defaults: spread=%v band=%d", d.Spread, d.SpreadBand)
	}
	if err := d.UnmarshalJSON([]byte(`{"servers":["10.0.0.53"],"queries":["example.com"],"spread":true,"spread_band":35}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(); err != nil || !d.Spread || d.SpreadBand != 35 {
		t.Fatalf("parsed: %+v err=%v", d, err)
	}
	// a config that does not mention it keeps the defaults
	var e DNSConfig
	if err := e.UnmarshalJSON([]byte(`{"servers":["10.0.0.53"]}`)); err != nil || !e.Spread || e.SpreadBand != 20 {
		t.Fatalf("omitted: %+v err=%v", e, err)
	}
	// switching it off in the file is honoured, and a config that never knew the key now spreads
	var f DNSConfig
	if err := f.UnmarshalJSON([]byte(`{"servers":["10.0.0.53"],"spread":false}`)); err != nil || f.Spread {
		t.Fatalf("spread:false: %+v err=%v", f, err)
	}
	for _, bad := range []int{0, -5, 1001} {
		d.SpreadBand = bad
		if d.Validate() == nil {
			t.Fatalf("spread_band %d accepted", bad)
		}
	}
	d.SpreadBand = 1000
	if err := d.Validate(); err != nil {
		t.Fatalf("1000 is the largest allowed: %v", err)
	}
}

// The servers in the band take turns in address order, so the share each gets stays even although their
// latencies jitter and swap places between queries.
func TestSpreadEvenWhileLatenciesJitter(t *testing.T) {
	cfg := testDNSCfg("127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3")
	cfg.Spread = true
	p := NewPool(cfg)
	orders := [][]float64{{10, 11, 12}, {12, 10, 11}, {11, 12, 10}, {10, 12, 11}, {12, 11, 10}, {11, 10, 12}}
	first := map[string]int{}
	for i := 0; i < 60; i++ {
		setLatency(p, orders[i%len(orders)]...)
		first[p.Candidates()[0].Addr]++
	}
	for _, a := range []string{"127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3"} {
		if first[a] != 20 {
			t.Fatalf("shares = %v, want 20 each", first)
		}
	}
}

// A server that leaves the band for a while and comes back gets one turn, not a burst to catch up, and the
// others keep sharing evenly meanwhile.
func TestSpreadMembershipChanges(t *testing.T) {
	ss := spreadServers(3)
	got := map[string]int{}
	for i := 0; i < 60; i++ {
		lat := []float64{10, 11, 12}
		if i < 20 { // s2 is far too slow for the first 20 queries
			lat[2] = 50
		}
		got[spreadOrder(ss, lat, 20, uint64(i))[0].Addr]++
	}
	if got["s0"] < 22 || got["s0"] > 24 || got["s1"] < 22 || got["s1"] > 24 || got["s2"] < 13 || got["s2"] > 14 {
		t.Fatalf("shares = %v", got)
	}
}

// The canvas marks the servers that take turns (the GUI draws their line blue); a server that is slower than the
// band, one that is down and every server with spread off are not marked.
func TestCanvasMarksServersInBand(t *testing.T) {
	a, b, slow, down := newFakeDNS(t), newFakeDNS(t), newFakeDNS(t), newFakeDNS(t)
	for _, f := range []*fakeDNS{a, b} {
		f.delay.Store(int64(20 * time.Millisecond))
	}
	slow.delay.Store(int64(400 * time.Millisecond))
	down.mode.Store(2)
	for _, spread := range []bool{true, false} {
		d := testDNSCfg(a.addr, b.addr, slow.addr, down.addr)
		d.Spread = spread
		d.SpreadBand = 150 // wide, so scheduling jitter on a loaded machine cannot push one of the two equal servers out of it; the slow one is 20 times slower
		d.ProbeTimeoutMS, d.QueryTimeoutMS = 1000, 1000
		g := defaultGroup()
		g.DNSProxy, g.DNS = true, &d
		dc := newDaemonConfig()
		dc.Groups = []GroupConfig{g}
		s := NewSupervisor(context.Background(), dc)
		s.refreshPool(dc, true)
		rows := []SnapshotRow{{GroupID: 1, AF: "v4", Local: true, State: "active", DNSUp: true}}
		cv := buildCanvas(dc, rows, s.poolList())
		s.StopAll()
		got := map[string]bool{}
		for _, sv := range cv[0].Servers {
			got[sv.Addr] = sv.InBand
		}
		want := map[string]bool{a.addr: spread, b.addr: spread, slow.addr: false, down.addr: false}
		for addr, w := range want {
			if got[addr] != w {
				t.Fatalf("spread=%v: in_band of %s = %v, want %v (all: %v)", spread, addr, got[addr], w, got)
			}
		}
	}
}

func TestSnapshotInBandLoneFastest(t *testing.T) {
	cfg := testDNSCfg("127.0.0.1:1", "127.0.0.1:2")
	cfg.Spread = true
	p := NewPool(cfg)
	setLatency(p, 10, 80)
	st := p.Snapshot()
	if !st[0].InBand || st[1].InBand {
		t.Fatalf("a lone fastest server is the one taking the queries: %+v", st)
	}
}

// The forwarding path reuses its ranking for a short while, but a server going down is seen at once and a later
// latency change is seen once the snapshot has aged out.
func TestRankingSnapshotReusedAndRefreshed(t *testing.T) {
	cfg := testDNSCfg("127.0.0.1:1", "127.0.0.1:2")
	cfg.Spread = false
	p := NewPool(cfg)
	setLatency(p, 10, 20)
	first := p.rankedSnap()
	if again := p.rankedSnap(); again != first {
		t.Fatal("a fresh ranking must be reused, not rebuilt for every query")
	}
	if first.list[0].Addr != "127.0.0.1:1" {
		t.Fatalf("order: %s", names(first.list))
	}
	// a latency change alone shows up only after rankMaxAge
	p.servers[0].mu.Lock()
	p.servers[0].ewma = 50
	p.servers[0].mu.Unlock()
	if p.rankedSnap() != first {
		t.Fatal("rebuilt before it aged out")
	}
	time.Sleep(rankMaxAge + 30*time.Millisecond)
	if c := p.Candidates(); c[0].Addr != "127.0.0.1:2" {
		t.Fatalf("after the snapshot aged out the faster server must lead: %s", names(c))
	}
	// a server going down is seen immediately, through the generation
	p.servers[1].mu.Lock()
	p.servers[1].healthy = false
	p.servers[1].mu.Unlock()
	p.rankGen.Add(1)
	if c := p.Candidates(); len(c) != 1 || c[0].Addr != "127.0.0.1:1" {
		t.Fatalf("a down server must leave the ranking at once: %s", names(c))
	}
}

// BenchmarkRanking compares what every forwarded query used to do (rank the servers under each one's lock) with
// the snapshot: go test -run xxx -bench Ranking -cpu 2,8
func BenchmarkRanking(b *testing.B) {
	p := NewPool(testDNSCfg("127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3", "127.0.0.1:4", "127.0.0.1:5", "127.0.0.1:6"))
	setLatency(p, 1, 2, 130, 131, 177, 316)
	b.Run("ranked-every-time", func(b *testing.B) {
		b.SetParallelism(16)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = p.Ranked()
			}
		})
	})
	b.Run("snapshot", func(b *testing.B) {
		b.SetParallelism(16)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = p.Candidates()
			}
		})
	})
}
