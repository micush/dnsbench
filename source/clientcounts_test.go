package main

import (
	"net/netip"
	"reflect"
	"testing"
	"time"
)

func TestQStatsClientsRunningCount(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s, cur := qsTestStats(t0)
	a, b, c := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2"), netip.MustParseAddr("192.0.2.3")
	s.Record(a, "x.example", 1, false, 0)
	s.Record(b, "x.example", 1, false, 3)
	*cur = t0.Add(10 * time.Minute)
	s.Record(a, "x.example", 1, false, 0)
	s.Record(c, "x.example", 1, false, 0)
	*cur = t0.Add(11 * time.Minute)

	from := t0.Add(-time.Hour)
	r := s.Query(from, *cur, QFilter{})
	at := func(r *QStatsResult, tm time.Time) uint64 { return r.ClientsN[(tm.Unix()-r.Start)/int64(r.Step)] }
	if at(r, t0.Add(-time.Minute)) != 0 || at(r, t0) != 2 || at(r, t0.Add(5*time.Minute)) != 2 || at(r, t0.Add(10*time.Minute)) != 3 {
		t.Fatalf("running count %v", r.ClientsN)
	}
	if last := r.ClientsN[len(r.ClientsN)-1]; int(last) != r.Sums.Clients || r.Sums.Clients != 3 {
		t.Fatalf("the last point %d must be the Clients tile %d (3)", last, r.Sums.Clients)
	}
	f := s.Query(from, *cur, QFilter{Rcode: "nxdomain"})
	if last := f.ClientsN[len(f.ClientsN)-1]; int(last) != f.Sums.Clients || last != 1 {
		t.Fatalf("filtered: last point %d, tile %d, want 1", last, f.Sums.Clients)
	}
}

func TestMergeQStatsClientsUnionAcrossNodes(t *testing.T) {
	mk := func(start int64, total []uint64, news [][]string) *QStatsResult {
		return &QStatsResult{Start: start, Step: 60, From: start, To: start + 180, Total: total, ClientsNew: news}
	}
	a := mk(600, []uint64{1, 1, 1}, [][]string{{"x", "y"}, nil, {"z"}})
	b := mk(660, []uint64{1, 1, 1}, [][]string{{"y"}, {"w", "x"}, nil})
	r := mergeQStats([]clusterPart[QStatsResult]{qpart("A", a), qpart("B", b)}, infosOf("A", "B"))
	if want := []uint64{2, 2, 4, 4}; !reflect.DeepEqual(r.ClientsN, want) {
		t.Fatalf("cluster running count %v, want %v (a client two nodes saw counts once)", r.ClientsN, want)
	}
	if r.Sums.Clients != 4 {
		t.Fatalf("tile %d, want 4", r.Sums.Clients)
	}
}
