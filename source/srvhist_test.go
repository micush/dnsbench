package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func newTestHistory() *srvHistory { return &srvHistory{m: map[string]*srvSeries{}} }

// seriesAt makes a series and puts the counters of one minute into it.
func seriesFor(h *srvHistory, addr string) *srvSeries {
	s := &srvSeries{}
	h.m[addr] = s
	return s
}

func TestServerHistoryRecordsAndSummarises(t *testing.T) {
	h := newTestHistory()
	s := seriesFor(h, "192.0.2.1:53")
	base := time.Date(2026, 10, 3, 12, 0, 30, 0, time.UTC)
	// minute 12:00: 100 answers, 1 failure, 5 probes (1 failed), latencies 1 ms and 3 ms
	for i := 0; i < 100; i++ {
		s.addOK()
	}
	s.addFail()
	for i := 0; i < 4; i++ {
		s.addPrOK()
	}
	s.addPrFail()
	s.lat(time.Millisecond)
	s.lat(3 * time.Millisecond)
	h.flush(base.Add(time.Minute)) // at 12:01:30 the 12:00 minute is closed
	// minute 12:01: nothing at all: no slot
	h.flush(base.Add(2 * time.Minute))
	if n := len(s.slots); n != 1 {
		t.Fatalf("%d slots, want 1 (an idle minute leaves a gap)", n)
	}
	r, err := h.Query("192.0.2.1:53", base.Add(-30*time.Minute), base.Add(10*time.Minute))
	if err != nil || !r.Known {
		t.Fatalf("query: %v known=%v", err, r.Known)
	}
	if r.Answered != 100 || r.Failed != 1 || r.ProbesOK != 4 || r.ProbesBad != 1 {
		t.Fatalf("totals: %+v", r)
	}
	if r.AvgMS != 2 || r.MaxMS != 3 {
		t.Fatalf("latency avg %v max %v, want 2 and 3", r.AvgMS, r.MaxMS)
	}
	// a server's loss is its probes' (1 of 5); its failed queries are reported apart (1 of 101)
	if want := 20.0; r.LossPct < want-0.01 || r.LossPct > want+0.01 {
		t.Fatalf("loss %v, want %v", r.LossPct, want)
	}
	if want := 100.0 / 101; r.QueryLossPct < want-0.01 || r.QueryLossPct > want+0.01 {
		t.Fatalf("query loss %v, want %v", r.QueryLossPct, want)
	}
	if r.Step != 60 || len(r.Latency) != len(r.Loss) || len(r.Latency) != len(r.Queries) {
		t.Fatalf("shape: step %d lens %d %d %d", r.Step, len(r.Latency), len(r.Loss), len(r.Queries))
	}
	var have, gaps int
	for i := range r.Latency {
		if r.Latency[i] >= 0 {
			have++
		} else if r.Loss[i] < 0 {
			gaps++
		}
	}
	if have != 1 || gaps != len(r.Latency)-1 {
		t.Fatalf("one bucket has data, the rest are gaps: have %d gaps %d of %d", have, gaps, len(r.Latency))
	}
	// the minute under way is included
	s.addOK()
	if r2, _ := h.Query("192.0.2.1:53", time.Now().Add(-5*time.Minute), time.Now().Add(time.Minute)); r2.Answered != 1 {
		t.Fatalf("the current minute must show: %+v", r2)
	}
}

func TestServerHistorySteps(t *testing.T) {
	h := newTestHistory()
	seriesFor(h, "x")
	now := time.Now()
	for _, c := range []struct {
		span time.Duration
		step int
	}{{time.Hour, 60}, {24 * time.Hour, 300}, {7 * 24 * time.Hour, 1800}, {30 * 24 * time.Hour, 1800}} {
		r, err := h.Query("x", now.Add(-c.span), now)
		if err != nil || r.Step != c.step {
			t.Fatalf("span %v: step %d (%v), want %d", c.span, r.Step, err, c.step)
		}
		if r.To-r.From > int64(7*24*3600) {
			t.Fatalf("a range over 7 days must be cut to 7 days")
		}
	}
	if _, err := h.Query("x", now, now.Add(-time.Hour)); err == nil {
		t.Fatal("a backwards range must be refused")
	}
	if r, _ := h.Query("nobody", now.Add(-time.Hour), now); r.Known {
		t.Fatal("an unknown server is not known")
	}
}

func TestServerHistoryKeepsSevenDays(t *testing.T) {
	h := newTestHistory()
	s := seriesFor(h, "x")
	now := time.Date(2026, 10, 10, 0, 0, 30, 0, time.UTC)
	old := now.Add(-8 * 24 * time.Hour)
	s.slots = []srvSlot{{Min: old.Unix() / 60, OK: 1}, {Min: now.Unix()/60 - 5, OK: 2}}
	s.addOK()
	h.flush(now)
	if len(s.slots) != 2 || s.slots[0].OK != 2 {
		t.Fatalf("the 8-day-old slot must go, the recent one stay and the new minute be added: %+v", s.slots)
	}
	// a server with nothing in 7 days has an empty history (the series stays: a paused server still holds it)
	h2 := newTestHistory()
	seriesFor(h2, "gone").slots = []srvSlot{{Min: old.Unix() / 60, OK: 1}}
	h2.flush(now)
	if r, _ := h2.Query("gone", now.Add(-time.Hour), now.Add(time.Hour)); r.Known || len(h2.m["gone"].slots) != 0 {
		t.Fatalf("a server with no data in 7 days must show nothing: %+v", r)
	}
}

func TestServerHistoryNilSafeAndShared(t *testing.T) {
	var s *srvSeries
	s.addOK()
	s.addFail()
	s.addPrOK()
	s.addPrFail()
	s.lat(time.Millisecond) // a hand-made Server has no history: none of these may panic
	if srvhist.series("shared:53") != srvhist.series("shared:53") {
		t.Fatal("two pools listing one address must share its history")
	}
}

// A real pool feeds the history: probes and live answers of a fake upstream show up, and a SERVFAIL counts as a loss.
func TestPoolFeedsServerHistory(t *testing.T) {
	up := newFakeDNS(t)
	p := NewPool(testDNSCfg(up.addr))
	p.ProbeNow(context.Background())
	q, _ := buildQuery(7, "hist.example", "A")
	for i := 0; i < 20; i++ {
		if _, err := p.Forward(context.Background(), q, false); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := srvhist.Query(up.addr, time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if r.Answered < 20 || r.ProbesOK < 1 || r.LossPct != 0 || !r.Known {
		t.Fatalf("history after traffic: %+v", r)
	}
	up.mode.Store(3) // SERVFAIL
	cfg2 := testDNSCfg(up.addr)
	cfg2.Cache = false
	p2 := NewPool(cfg2)
	p2.ProbeNow(context.Background())
	before := r.Failed + r.ProbesBad
	p2.Forward(context.Background(), q, false)
	r2, _ := srvhist.Query(up.addr, time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if r2.Failed+r2.ProbesBad <= before || r2.LossPct <= 0 {
		t.Fatalf("a server answering SERVFAIL must show loss: %+v", r2)
	}
}

func TestServerHistoryPersistRoundTrip(t *testing.T) {
	h := newTestHistory()
	s := seriesFor(h, "192.0.2.9:53")
	base := time.Now().Add(-10 * time.Minute)
	for i := 0; i < 3; i++ {
		for j := 0; j <= i; j++ {
			s.addOK()
		}
		s.addFail()
		s.addPrOK()
		s.addPrFail()
		s.lat(time.Duration(i+1) * time.Millisecond)
		h.flush(base.Add(time.Duration(i+1) * time.Minute))
	}
	// an old minute beyond retention is dropped
	s.slots = append([]srvSlot{{Min: time.Now().Unix()/60 - srvHistKeep - 5, OK: 1}}, s.slots...)
	var recs []pRec
	if err := h.persistTo(func(r pRec) error { recs = append(recs, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].K != "srv" || len(recs[0].SrvMin) != 3 {
		t.Fatalf("records %+v", recs)
	}
	h2 := newTestHistory()
	h2.start.Do(func() {}) // no flusher in the test
	if n := h2.restore(recs[0]); n != 3 {
		t.Fatalf("restored %d, want 3", n)
	}
	if n := h2.restore(recs[0]); n != 0 {
		t.Fatalf("restoring twice added %d", n)
	}
	a, b := h.m["192.0.2.9:53"].slots[1:], h2.m["192.0.2.9:53"].slots
	if len(a) != len(b) {
		t.Fatalf("%d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("slot %d: %+v != %+v", i, a[i], b[i])
		}
	}
}

// The history rides in stats.json.gz: save, forget, load.
func TestServerHistoryInStatsFile(t *testing.T) {
	old := srvhist
	defer func() { srvhist = old }()
	srvhist = newTestHistory()
	srvhist.start.Do(func() {})
	s := seriesFor(srvhist, "192.0.2.77:53")
	s.slots = []srvSlot{{Min: time.Now().Unix()/60 - 5, OK: 7, Fail: 1, LatSum: 9000, LatN: 3, LatMaxU: 5000}}
	dir := t.TempDir()
	if err := savePersisted(dir); err != nil {
		t.Fatal(err)
	}
	srvhist = newTestHistory()
	srvhist.start.Do(func() {})
	if _, err := loadPersisted(dir); err != nil {
		t.Fatal(err)
	}
	got := srvhist.m["192.0.2.77:53"]
	if got == nil || len(got.slots) != 1 || got.slots[0].OK != 7 || got.slots[0].LatMaxU != 5000 {
		t.Fatalf("not restored: %+v", got)
	}
}

// A gateway's series: client answers and errors, cache hits, availability samples; a domain's: probes only.
func TestGatewayAndDomainHistory(t *testing.T) {
	h := newTestHistory()
	gw := seriesFor(h, gwKey(7))
	for i := 0; i < 90; i++ {
		gw.addOK()
	}
	for i := 0; i < 10; i++ {
		gw.addFail()
	}
	for i := 0; i < 60; i++ {
		gw.addHit()
	}
	for i := 0; i < 27; i++ {
		gw.avail(true)
	}
	for i := 0; i < 3; i++ {
		gw.avail(false)
	}
	gw.lat(2 * time.Millisecond)
	dk := domKey("192.0.2.1:53", "Example.COM.", "a")
	if dk != "dom:192.0.2.1:53|example.com|A" {
		t.Fatalf("domain key %q", dk)
	}
	dm := seriesFor(h, dk)
	dm.addPrOK()
	dm.addPrOK()
	dm.addPrFail()
	dm.lat(4 * time.Millisecond)
	base := time.Now()
	h.flush(base.Add(time.Minute))
	r, err := h.Query(gwKey(7), base.Add(-10*time.Minute), base.Add(5*time.Minute))
	if err != nil || !r.Known || r.Kind != "gateway" {
		t.Fatalf("gateway: %+v %v", r, err)
	}
	if r.Answered != 90 || r.Failed != 10 || r.Hits != 60 || r.LossPct != 10 || r.AvailPct != 90 || r.AvgMS != 2 {
		t.Fatalf("gateway totals: %+v", r)
	}
	var qps, hit, av float64 = -1, -1, -1
	for i := range r.QPS {
		if r.QPS[i] >= 0 {
			qps, hit, av = r.QPS[i], r.HitQPS[i], r.Avail[i]
		}
	}
	if qps <= 0 || hit <= 0 || hit >= qps || av != 90 {
		t.Fatalf("buckets: qps %v hit %v avail %v", qps, hit, av)
	}
	d, _ := h.Query(dk, base.Add(-10*time.Minute), base.Add(5*time.Minute))
	if !d.Known || d.Kind != "domain" || d.ProbesOK != 2 || d.ProbesBad != 1 || d.LossPct < 33 || d.LossPct > 34 {
		t.Fatalf("domain: %+v", d)
	}
	if u, _ := h.Query("192.0.2.200:53", base.Add(-time.Minute), base.Add(time.Minute)); u.Known || u.Kind != "server" || u.AvailPct != -1 {
		t.Fatalf("unknown key: %+v", u)
	}
	// the new counters survive the stats file
	var recs []pRec
	h.persistTo(func(r pRec) error { recs = append(recs, r); return nil })
	h2 := newTestHistory()
	h2.start.Do(func() {})
	for _, rc := range recs {
		h2.restore(rc)
	}
	g2, _ := h2.Query(gwKey(7), base.Add(-10*time.Minute), base.Add(5*time.Minute))
	if g2.Hits != 60 || g2.AvailPct != 90 {
		t.Fatalf("restored gateway: %+v", g2)
	}
}

// The front end feeds its gateway's series: answers, SERVFAILs and cache hits.
func TestFrontendFeedsGatewayHistory(t *testing.T) {
	up := newFakeDNS(t)
	cfg := testDNSCfg(up.addr)
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	f := NewDNSFrontend(netip.MustParseAddr("127.0.0.1"), 0, func() *Pool { return p })
	f.ctx, f.cancel = context.WithCancel(context.Background())
	defer f.cancel()
	s := &srvSeries{}
	f.gw = s
	q, _ := buildQuery(9, "gw.example", "A")
	client := netip.MustParseAddr("127.0.0.1")
	for i := 0; i < 16; i++ {
		f.resolve(q, false, client)
	}
	sl := s.take(false)
	if sl.OK != 16 || sl.Fail != 0 {
		t.Fatalf("answers not counted: %+v", sl)
	}
	if p.cache != nil && sl.Hit < 10 {
		t.Fatalf("cache hits not counted: %+v", sl)
	}
	if sl.LatN != 2 { // one answer in eight is timed
		t.Fatalf("latency samples: %d", sl.LatN)
	}
}

// Two nodes that each saw traffic in different minutes make one graph with no hole; averages are weighted by the samples,
// the worst is the worst of both, counts add up, and a range that does not line up is refused.
func TestGatewayHistoryMergesNodes(t *testing.T) {
	ha, hb := newTestHistory(), newTestHistory()
	a, b := seriesFor(ha, gwKey(7)), seriesFor(hb, gwKey(7))
	now := time.Now()
	for i := 0; i < 10; i++ {
		a.addOK()
	}
	a.lat(2 * time.Millisecond)
	ha.flush(now.Add(-4 * time.Minute)) // node A answered in an earlier minute …
	for i := 0; i < 30; i++ {
		b.addOK()
	}
	b.addFail()
	b.lat(4 * time.Millisecond)
	b.lat(8 * time.Millisecond)
	b.lat(12 * time.Millisecond)
	hb.flush(now.Add(-2 * time.Minute)) // … node B in a later one
	from, to := now.Add(-10*time.Minute), now
	ra, _ := ha.Raw(gwKey(7), from, to)
	rb, _ := hb.Raw(gwKey(7), from, to)
	alone := ra.Stats()
	gaps := func(r ServerStatsResult) int {
		n := 0
		for _, q := range r.Queries {
			if q < 0 {
				n++
			}
		}
		return n
	}
	if !ra.merge(rb) {
		t.Fatal("same range refused")
	}
	both := ra.Stats()
	if gaps(both) >= gaps(alone) || gaps(both) != len(both.Queries)-2 {
		t.Fatalf("holes: alone %d, together %d of %d", gaps(alone), gaps(both), len(both.Queries))
	}
	if both.Answered != 40 || both.Failed != 1 || !both.Known {
		t.Fatalf("totals: %+v", both)
	}
	if both.AvgMS != 6.5 || both.MaxMS != 12 { // (2+4+8+12)/4 and the worst
		t.Fatalf("latency: avg %v max %v", both.AvgMS, both.MaxMS)
	}
	other, _ := hb.Raw(gwKey(7), from.Add(-5*time.Hour), to)
	if ra.merge(other) {
		t.Fatal("a different range was merged")
	}
	if o, _ := hb.Raw(gwKey(8), from, to); ra.merge(o) {
		t.Fatal("a different key was merged")
	}
}

// The peer call answers only for gateways, with this node's own buckets.
func TestClusterHistHandler(t *testing.T) {
	c := &Cluster{}
	srvhist.series(gwKey(9991)).addOK()
	now := time.Now()
	ask := func(key string) (int, srvRaw) {
		b, _ := json.Marshal(histAsk{Key: key, From: now.Add(-time.Hour).Unix(), To: now.Unix()})
		rw := httptest.NewRecorder()
		c.handleHist(rw, httptest.NewRequest("POST", "/cluster/hist", bytes.NewReader(b)), ClusterPeer{}, b)
		var r srvRaw
		json.Unmarshal(rw.Body.Bytes(), &r)
		return rw.Code, r
	}
	if code, r := ask(gwKey(9991)); code != 200 || !r.Known || len(r.B) == 0 || r.Key != gwKey(9991) {
		t.Fatalf("gateway: %d %+v", code, r)
	}
	for _, k := range []string{"192.0.2.1:53", "dom:192.0.2.1:53|x|A", ""} {
		if code, _ := ask(k); code != http.StatusBadRequest {
			t.Errorf("%q answered (%d)", k, code)
		}
	}
}
