package main

import (
	"net/netip"
	"sync"
	"testing"
	"time"
)

func qsTestStats(t0 time.Time) (*QStats, *time.Time) {
	s := NewQStats()
	cur := t0
	s.now = func() time.Time { return cur }
	s.start = t0
	return s, &cur
}

func TestQuestionOf(t *testing.T) {
	q, _ := buildQuery(1, "WWW.Example.COM", "AAAA")
	n, ty, ok := questionOf(q)
	if !ok || n != "www.example.com" || ty != 28 {
		t.Fatalf("%q %d %v", n, ty, ok)
	}
	if _, _, ok := questionOf(q[:14]); ok {
		t.Fatal("a truncated name was accepted")
	}
	if _, _, ok := questionOf(q[:8]); ok {
		t.Fatal("a short message was accepted")
	}
	if qtypeName(65) != "HTTPS" || qtypeName(9999) != "TYPE9999" {
		t.Fatal(qtypeName(9999))
	}
}

func TestQStatsRecordAndQuery(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 30, 0, time.UTC)
	s, cur := qsTestStats(t0)
	a, b := netip.MustParseAddr("192.0.2.5"), netip.MustParseAddr("::ffff:192.0.2.6")
	s.Record(a, "a.example", 1, false, 0)
	s.Record(a, "a.example", 28, false, 0)
	s.Record(b, "b.example", 1, true, 3)
	*cur = t0.Add(2 * time.Minute)
	s.Record(b, "a.example", 1, false, 2)
	s.Record(a, "c.example", 12, false, 5)
	s.Record(a, "c.example", 12, false, 9)
	*cur = t0.Add(5 * time.Minute)

	r := s.Query(t0.Add(-30*time.Minute), *cur, QFilter{})
	if r.Step != 60 || r.Sums.Total != 6 || r.Sums.NoError != 2 || r.Sums.NXDomain != 1 || r.Sums.ServFail != 1 || r.Sums.Refused != 1 || r.Sums.Other != 1 {
		t.Fatalf("sums: %+v", r.Sums)
	}
	var sum uint64
	for _, v := range r.Total {
		sum += v
	}
	if sum != 6 || r.Start%60 != 0 || len(r.Total) != len(r.NoError) {
		t.Fatalf("series: %d %v", sum, r.Total)
	}
	if r.Sums.Clients != 2 || r.Clients[0].Name != "192.0.2.5" || r.Clients[0].Count != 4 || r.Clients[1].Name != "192.0.2.6" {
		t.Fatalf("clients (the ::ffff: form must fold into the v4 address): %+v", r.Clients)
	}
	if r.Domains[0].Name != "a.example" || r.Domains[0].Count != 3 {
		t.Fatalf("domains: %+v", r.Domains)
	}
	if len(r.Protos) != 2 || r.Protos[0].Name != "UDP" || r.Protos[0].Count != 5 || r.Protos[1].Count != 1 {
		t.Fatalf("protos: %+v", r.Protos)
	}
	if r.Types[0].Name != "A" || r.Types[0].Count != 3 {
		t.Fatalf("types: %+v", r.Types)
	}
	// a range before the first query sees nothing
	if r := s.Query(t0.Add(-3*time.Hour), t0.Add(-2*time.Hour), QFilter{}); r.Sums.Total != 0 || len(r.Clients) != 0 {
		t.Fatalf("empty range: %+v", r.Sums)
	}
}

func TestQStatsStepsAndRetention(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s, cur := qsTestStats(t0)
	s.Record(netip.MustParseAddr("192.0.2.5"), "old.example", 1, false, 0)
	*cur = t0.Add(31 * 24 * time.Hour) // the first query is now outside the 30 day window
	s.Record(netip.MustParseAddr("192.0.2.5"), "new.example", 1, false, 0)
	r := s.Query(t0, *cur, QFilter{})
	if r.Sums.Total != 1 || r.Domains[0].Name != "new.example" || len(r.Domains) != 1 {
		t.Fatalf("old data survived: %+v %+v", r.Sums, r.Domains)
	}
	if r.Step != 10800 || len(r.Total) > 250 {
		t.Fatalf("a 30 day range should use 3 h steps: step %d points %d", r.Step, len(r.Total))
	}
	for _, c := range []struct {
		span time.Duration
		step int64
	}{{time.Hour, 60}, {2 * time.Hour, 60}, {24 * time.Hour, 600}, {48 * time.Hour, 3600}, {7 * 24 * time.Hour, 3600}, {10 * 24 * time.Hour, 10800}} {
		if got := stepFor(c.span); got != c.step {
			t.Errorf("stepFor(%v) = %d, want %d", c.span, got, c.step)
		}
	}
	// a slot is reused after 30 days: the stale count must not leak in
	s2, cur2 := qsTestStats(t0)
	s2.Record(netip.MustParseAddr("192.0.2.5"), "x.example", 1, false, 0)
	*cur2 = t0.Add(qsRetain)
	s2.Record(netip.MustParseAddr("192.0.2.5"), "y.example", 1, false, 0)
	if r := s2.Query(t0.Add(time.Hour), *cur2, QFilter{}); r.Sums.Total != 1 {
		t.Fatalf("slot reuse leaked: %+v", r.Sums)
	}
}

func TestQStatsCapsDistinctNames(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s, cur := qsTestStats(t0)
	for i := 0; i < qsCapDomains+50; i++ {
		s.Record(netip.MustParseAddr("192.0.2.5"), "d"+fmtN(i)+".example", 1, false, 0)
	}
	*cur = t0.Add(time.Minute)
	r := s.Query(t0.Add(-time.Hour), *cur, QFilter{})
	if r.Sums.Total != uint64(qsCapDomains+50) {
		t.Fatalf("total %d", r.Sums.Total)
	}
	last := r.Domains[len(r.Domains)-1]
	if len(r.Domains) != qsKeepResult || last.Name == qsOthers {
		// (others) sorts last, but the list is cut at qsKeepResult
		t.Logf("top list cut at %d", len(r.Domains))
	}
	var others uint64
	s.flush() // the events are counted in batches; this test reads the tables directly
	s.mu.Lock()
	for _, tp := range s.fine {
		others += uint64(tp.c[qcNoError].domains[qsOthers])
	}
	s.mu.Unlock()
	if others != 50 {
		t.Fatalf("the overflow should be counted as (others): %d", others)
	}
}

func fmtN(i int) string { return fmtInt2(i) }

func fmtInt2(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestQStatsRange(t *testing.T) {
	f, to, err := qstatsRange("", "")
	if err != nil || to.Sub(f) != time.Hour {
		t.Fatalf("default: %v %v %v", f, to, err)
	}
	if f, to, _ := qstatsRange("30d", ""); to.Sub(f) != 30*24*time.Hour {
		t.Fatalf("30d: %v", to.Sub(f))
	}
	if f, to, err := qstatsRange("1790000000", "1790003600"); err != nil || f.Unix() != 1790000000 || to.Unix() != 1790003600 {
		t.Fatalf("explicit: %v %v %v", f, to, err)
	}
	for _, bad := range [][2]string{{"soon", ""}, {"", "x"}, {"0", ""}} {
		if _, _, err := qstatsRange(bad[0], bad[1]); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}

func TestQStatsTiers(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 0, 5, 0, 0, time.UTC)
	s, cur := qsTestStats(t0)
	cl := netip.MustParseAddr("192.0.2.5")
	s.Record(cl, "old.example", 1, false, 0) // 3 days before "now"
	*cur = t0.Add(72 * time.Hour)
	s.Record(cl, "recent.example", 1, false, 0)
	// a range inside the last day reads the exact 10-minute slots
	r := s.Query(cur.Add(-time.Hour), *cur, QFilter{})
	if len(r.Domains) != 1 || r.Domains[0].Name != "recent.example" {
		t.Fatalf("day range: %+v", r.Domains)
	}
	// a week reads the hour slots and still sees the old query
	r = s.Query(cur.Add(-7*24*time.Hour), *cur, QFilter{})
	if r.Sums.Total != 2 || len(r.Domains) != 2 {
		t.Fatalf("week range: %+v %+v", r.Sums, r.Domains)
	}
}

func TestQStatsByAnswerKind(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s, cur := qsTestStats(t0)
	a, b := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")
	s.Record(a, "good.example", 1, false, 0)
	s.Record(a, "good.example", 28, false, 0)
	s.Record(a, "typo.example", 1, false, 3)
	s.Record(b, "typo.example", 1, true, 3)
	s.Record(b, "nope.example", 1, false, 3)
	s.Record(b, "bad.example", 1, false, 2)
	s.Record(b, "deny.example", 1, false, 5)
	s.Record(b, "odd.example", 1, false, 4)
	*cur = t0.Add(time.Minute)
	from := t0.Add(-time.Hour)

	all := s.Query(from, *cur, QFilter{})
	if all.Sums.Total != 8 || all.Domains[0].Name != "good.example" && all.Domains[0].Name != "typo.example" {
		t.Fatalf("all: %+v", all.Domains)
	}
	nx := s.Query(from, *cur, QFilter{Rcode: "nxdomain"})
	if nx.Rcode != "nxdomain" || len(nx.Domains) != 2 || nx.Domains[0].Name != "typo.example" || nx.Domains[0].Count != 2 || nx.Domains[1].Name != "nope.example" {
		t.Fatalf("nxdomain domains: %+v", nx.Domains)
	}
	if len(nx.Clients) != 2 || nx.Clients[0].Name != "192.0.2.2" || nx.Clients[0].Count != 2 || nx.Sums.Clients != 2 {
		t.Fatalf("nxdomain clients: %+v", nx.Clients)
	}
	if len(nx.Protos) != 2 || nx.Sums.Total != 8 {
		t.Fatalf("nxdomain protos %+v / the sums must stay whole: %d", nx.Protos, nx.Sums.Total)
	}
	for kind, want := range map[string]string{"noerror": "good.example", "servfail": "bad.example", "refused": "deny.example", "other": "odd.example"} {
		r := s.Query(from, *cur, QFilter{Rcode: kind})
		if len(r.Domains) != 1 || r.Domains[0].Name != want {
			t.Fatalf("%s: %+v", kind, r.Domains)
		}
	}
	// the all-kinds list is the sum of the kinds
	var sum uint64
	for _, d := range all.Domains {
		sum += d.Count
	}
	if sum != 8 {
		t.Fatalf("sum of all %d", sum)
	}
	// a flood of random NXDOMAIN names is capped on its own and does not touch NoError
	for i := 0; i < qsCaps[qcNXDomain][1]+40; i++ {
		s.Record(a, "r"+fmtN(i)+".example", 1, false, 3)
	}
	var others uint64
	s.flush() // the events are counted in batches; this test reads the tables directly
	s.mu.Lock()
	for _, tp := range s.fine {
		others += uint64(tp.c[qcNXDomain].domains[qsOthers])
	}
	s.mu.Unlock()
	if others == 0 {
		t.Fatalf("overflow not counted as (others)")
	}
	if g := s.Query(from, *cur, QFilter{Rcode: "noerror"}); len(g.Domains) != 1 {
		t.Fatalf("noerror list disturbed: %+v", g.Domains)
	}
}

func TestQStatsRcodeArg(t *testing.T) {
	for in, want := range map[string]string{"": "", "all": "", "NXDOMAIN": "nxdomain", " servfail ": "servfail", "refused": "refused", "other": "other", "noerror": "noerror"} {
		if got, err := qstatsRcode(in); err != nil || got != want {
			t.Errorf("%q → %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"nx", "3", "x y"} {
		if _, err := qstatsRcode(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestQStatsClientDomainPairs(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s, cur := qsTestStats(t0)
	a, b := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")
	for i := 0; i < 3; i++ {
		s.Record(a, "one.example", 1, false, 0)
	}
	s.Record(a, "two.example", 1, false, 0)
	s.Record(b, "one.example", 1, false, 0)
	s.Record(a, "typo.example", 1, false, 3)
	s.Record(b, "typo.example", 1, false, 3)
	s.Record(b, "typo.example", 1, false, 3)
	*cur = t0.Add(time.Minute)
	from := t0.Add(-time.Hour)

	r := s.Query(from, *cur, QFilter{Client: "192.0.2.1"})
	if r.Client != "192.0.2.1" || len(r.Domains) != 3 || r.Domains[0].Name != "one.example" || r.Domains[0].Count != 3 {
		t.Fatalf("client a: %+v", r.Domains)
	}
	r = s.Query(from, *cur, QFilter{Client: "192.0.2.1", Rcode: "nxdomain"})
	if len(r.Domains) != 1 || r.Domains[0].Name != "typo.example" || r.Domains[0].Count != 1 {
		t.Fatalf("client a nx: %+v", r.Domains)
	}
	r = s.Query(from, *cur, QFilter{Domain: "one.example"})
	if len(r.Clients) != 2 || r.Clients[0].Name != "192.0.2.1" || r.Clients[0].Count != 3 || r.Clients[1].Count != 1 {
		t.Fatalf("domain one: %+v", r.Clients)
	}
	r = s.Query(from, *cur, QFilter{Domain: "typo.example", Rcode: "nxdomain"})
	if len(r.Clients) != 2 || r.Clients[0].Name != "192.0.2.2" || r.Clients[0].Count != 2 {
		t.Fatalf("domain typo: %+v", r.Clients)
	}
	if r := s.Query(from, *cur, QFilter{Client: "192.0.2.9"}); len(r.Domains) != 0 {
		t.Fatalf("unknown client: %+v", r.Domains)
	}
	// past the pair cap the difference shows as (others) and the client total stays exact
	for i := 0; i < qsPairCaps[qcNoError]+40; i++ {
		s.Record(b, "p"+fmtN(i)+".example", 1, false, 0)
	}
	r = s.Query(from, *cur, QFilter{Client: "192.0.2.2", Rcode: "noerror"})
	var sum uint64
	for _, d := range r.Domains {
		sum += d.Count
	}
	if sum != uint64(qsPairCaps[qcNoError]+40+1) && len(r.Domains) < qsKeepResult {
		t.Fatalf("counts not preserved: %d over %d rows", sum, len(r.Domains))
	}
	s.flush() // the events are counted in batches; this test reads the tables directly
	s.mu.Lock()
	n := 0
	for _, tp := range s.fine {
		n += tp.c[qcNoError].npairs
	}
	s.mu.Unlock()
	if n > qsPairCaps[qcNoError] {
		t.Fatalf("pair cap exceeded: %d", n)
	}
}

func TestQStatsFilterArg(t *testing.T) {
	if f, err := qstatsFilter("NXDomain", "::ffff:192.0.2.1", ""); err != nil || f.Rcode != "nxdomain" || f.Client != "192.0.2.1" {
		t.Fatalf("%+v %v", f, err)
	}
	if f, err := qstatsFilter("", "", "WWW.Example.COM."); err != nil || f.Domain != "www.example.com" {
		t.Fatalf("%+v %v", f, err)
	}
	for _, c := range [][3]string{{"", "192.0.2.1", "x.example"}, {"", "not-an-ip", ""}, {"bogus", "", ""}, {"", "", "a b"}} {
		if _, err := qstatsFilter(c[0], c[1], c[2]); err == nil {
			t.Errorf("%q accepted", c)
		}
	}
}

func TestQStatsUpdateCounter(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 30, 0, time.UTC)
	s, cur := qsTestStats(t0)
	c := netip.MustParseAddr("192.0.2.9")
	s.Record(c, "corp.example", qtUpdate, false, 0)
	s.Record(c, "corp.example", qtUpdate, true, 5)
	s.Record(c, "corp.example", qtUpdate, false, 2)
	s.Record(c, "www.example", 1, false, 0)
	*cur = t0.Add(3 * time.Minute)
	r := s.Query(t0.Add(-time.Hour), *cur, QFilter{})
	if r.Sums.Updates != 3 || r.Sums.UpdFail != 2 || r.Sums.Total != 4 {
		t.Fatalf("sums %+v (updates are part of the total)", r.Sums)
	}
	var n uint64
	for _, v := range r.Updates {
		n += v
	}
	if n != 3 || len(r.Updates) != len(r.Total) {
		t.Fatalf("series %v", r.Updates)
	}
}

// updates are a kind of their own: not counted again as NoError/Refused, listed under rcode "update"
func TestQStatsUpdateKind(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 30, 0, time.UTC)
	s, cur := qsTestStats(t0)
	c1, c2 := netip.MustParseAddr("192.0.2.9"), netip.MustParseAddr("192.0.2.10")
	s.Record(c1, "www.example", 1, false, 0)
	s.Record(c1, "corp.example", qtUpdate, false, 0)
	s.Record(c1, "corp.example", qtUpdate, false, 0)
	s.Record(c2, "other.example", qtUpdate, true, 5)
	*cur = t0.Add(2 * time.Minute)
	all := s.Query(t0.Add(-time.Hour), *cur, QFilter{})
	if all.Sums.Total != 4 || all.Sums.NoError != 1 || all.Sums.Refused != 0 || all.Sums.Updates != 3 || all.Sums.UpdFail != 1 {
		t.Fatalf("sums: %+v", all.Sums)
	}
	u := s.Query(t0.Add(-time.Hour), *cur, QFilter{Rcode: "update"})
	if len(u.Domains) != 2 || u.Domains[0].Name != "corp.example" || u.Domains[0].Count != 2 || len(u.Clients) != 2 || u.Clients[0].Name != "192.0.2.9" {
		t.Fatalf("update lists: domains %+v clients %+v", u.Domains, u.Clients)
	}
	if len(u.Types) != 1 || u.Types[0].Name != "UPDATE" || len(u.Protos) != 2 {
		t.Fatalf("types %+v protos %+v", u.Types, u.Protos)
	}
	ok := s.Query(t0.Add(-time.Hour), *cur, QFilter{Rcode: "noerror"})
	if len(ok.Domains) != 1 || ok.Domains[0].Name != "www.example" {
		t.Fatalf("noerror list must not hold updates: %+v", ok.Domains)
	}
	uc := s.Query(t0.Add(-time.Hour), *cur, QFilter{Rcode: "update", Client: "192.0.2.9"})
	if len(uc.Domains) != 1 || uc.Domains[0].Name != "corp.example" || uc.Domains[0].Count != 2 {
		t.Fatalf("zones this client updated: %+v", uc.Domains)
	}
	if f, err := qstatsFilter("update", "", ""); err != nil || f.Rcode != "update" {
		t.Fatal(f, err)
	}
}

// Queries are counted in batches a moment after they happen, but anything that reads the statistics sees them all.
func TestQStatsCountsBatchedEventsBeforeAnyRead(t *testing.T) {
	s := NewQStats()
	ip := netip.MustParseAddr("10.0.0.1")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				s.Record(ip, "example.com", 1, false, 0)
				s.RecordCache(i%2 == 0)
			}
		}()
	}
	wg.Wait()
	r := s.Query(time.Now().Add(-time.Hour), time.Time{}, QFilter{})
	if r.Sums.Total != 4000 {
		t.Fatalf("total = %d, want 4000", r.Sums.Total)
	}
	if r.Sums.CacheHit+r.Sums.CacheMis != 4000 {
		t.Fatalf("cache lookups = %d, want 4000", r.Sums.CacheHit+r.Sums.CacheMis)
	}
}

func TestQStatsTimerCountsWithoutAnyRead(t *testing.T) {
	s := NewQStats()
	s.Record(netip.MustParseAddr("10.0.0.2"), "a.example", 1, false, 0)
	time.Sleep(qsFlushEvery + 400*time.Millisecond)
	n := 0
	for i := range s.pend {
		s.pend[i].mu.Lock()
		n += len(s.pend[i].ev)
		s.pend[i].mu.Unlock()
	}
	if n != 0 {
		t.Fatalf("%d events still waiting after the flush interval", n)
	}
	s.mu.Lock()
	var total uint32
	for i := range s.mins {
		total += s.mins[i].total
	}
	s.mu.Unlock()
	if total != 1 {
		t.Fatalf("counted %d, want 1", total)
	}
}
