package main

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestQStatsCacheHitsInRange(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 30, 0, time.UTC)
	s, cur := qsTestStats(t0)
	for i := 0; i < 3; i++ {
		s.RecordCache(true)
	}
	s.RecordCache(false)
	*cur = t0.Add(10 * time.Minute) // a later minute
	s.RecordCache(true)
	s.RecordCache(false)
	*cur = t0.Add(12 * time.Minute)
	r := s.Query(t0.Add(-time.Hour), *cur, QFilter{})
	if r.Sums.CacheHit != 4 || r.Sums.CacheMis != 2 {
		t.Fatalf("cache sums: %+v", r.Sums)
	}
	// a range that excludes the first minute only sees the later lookups
	r = s.Query(t0.Add(5*time.Minute), *cur, QFilter{})
	if r.Sums.CacheHit != 1 || r.Sums.CacheMis != 1 {
		t.Fatalf("cache sums in a later range: %+v", r.Sums)
	}
}

func TestPersistKeepsCacheCountsAndReadsOlderFile(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 5, 0, time.UTC)
	q, _ := qsTestStats(t0)
	h, _, _ := fakeHost(t0)
	swapStats(t, q, h)
	q.RecordCache(true)
	q.RecordCache(true)
	q.RecordCache(false)
	dir := t.TempDir()
	if err := savePersisted(dir); err != nil {
		t.Fatal(err)
	}
	q2, _ := qsTestStats(t0)
	h2, _, _ := fakeHost(t0)
	swapStats(t, q2, h2)
	if _, err := loadPersisted(dir); err != nil {
		t.Fatal(err)
	}
	if r := q2.Query(t0.Add(-time.Hour), t0.Add(time.Minute), QFilter{}); r.Sums.CacheHit != 2 || r.Sums.CacheMis != 1 {
		t.Fatalf("after reload: %+v", r.Sums)
	}

	// a file written before the cache counters (nine numbers per minute) still loads, with zero cache counts
	old := t.TempDir()
	f, _ := os.Create(filepath.Join(old, persistFile))
	zw := gzip.NewWriter(f)
	m := t0.Unix() / 60
	zw.Write([]byte(`{"k":"hdr","hdr":{"ddgw_stats":1}}` + "\n"))
	zw.Write([]byte(`{"k":"mins","mins":[[` + strconv.FormatInt(m, 10) + `,5,5,0,0,0,0,0,0]]}` + "\n"))
	zw.Close()
	f.Close()
	q3, _ := qsTestStats(t0)
	h3, _, _ := fakeHost(t0)
	swapStats(t, q3, h3)
	if _, err := loadPersisted(old); err != nil {
		t.Fatal(err)
	}
	if r := q3.Query(t0.Add(-time.Hour), t0.Add(time.Minute), QFilter{}); r.Sums.Total != 5 || r.Sums.CacheHit != 0 || r.Sums.CacheMis != 0 {
		t.Fatalf("older file: %+v", r.Sums)
	}
}
