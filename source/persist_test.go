package main

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// swapStats installs the given collectors as the daemon-wide ones for a test.
func swapStats(t *testing.T, q *QStats, h *HostStats) {
	oq, oh := qstats, hoststats
	qstats, hoststats = q, h
	t.Cleanup(func() { qstats, hoststats = oq, oh })
}

func persistFixture(t *testing.T, t0 time.Time) (*QStats, *HostStats, *time.Time, *time.Time) {
	q, cur := qsTestStats(t0)
	c1, c2 := mustAddr("192.0.2.5"), mustAddr("192.0.2.6")
	q.Record(c1, "a.example", 1, false, 0)
	q.Record(c1, "a.example", 28, true, 0)
	q.Record(c2, "typo.example", 1, false, 3)
	q.Record(c2, "typo.example", 1, false, 3)
	q.Record(c2, "corp.example", qtUpdate, false, 5)
	*cur = t0.Add(3 * time.Minute)
	q.Record(c1, "b.example", 1, false, 2)
	h, hcur, rd := fakeHost(t0)
	h.Sample()
	*hcur = t0.Add(10 * time.Second)
	rd.cpuBusy, rd.cpuTotal = 50, 100
	rd.fs = append(rd.fs, fsUsage{Mount: "/var", Device: "sdb1", Type: "xfs", Total: 100, Used: 10, Pct: 10})
	h.Sample()
	*hcur = t0.Add(70 * time.Second)
	rd.cpuBusy, rd.cpuTotal = 80, 200
	h.Sample()
	return q, h, cur, hcur
}

func TestPersistRoundTrip(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 5, 0, time.UTC)
	q, h, cur, hcur := persistFixture(t, t0)
	*hcur = t0.Add(2 * time.Minute)
	q.start = t0.Add(-48 * time.Hour)
	h.start = t0.Add(-72 * time.Hour)
	swapStats(t, q, h)
	dir := t.TempDir()
	if err := savePersisted(dir); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(dir, persistFile)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", st, err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("a temporary file was left: %v", ents)
	}
	from := t0.Add(-time.Hour)
	wantQ := q.Query(from, *cur, QFilter{})
	wantNX := q.Query(from, *cur, QFilter{Rcode: "nxdomain", Client: "192.0.2.6"})
	wantH := h.Query(from, t0.Add(2*time.Minute))

	q2, _ := qsTestStats(t0)
	q2.now = func() time.Time { return *cur }
	q2.start = *cur
	h2, hcur2, _ := fakeHost(t0)
	*hcur2 = t0.Add(2 * time.Minute)
	swapStats(t, q2, h2)
	n, err := loadPersisted(dir)
	if err != nil || n == 0 {
		t.Fatalf("load: %d %v", n, err)
	}
	gotQ := q2.Query(from, *cur, QFilter{})
	gotNX := q2.Query(from, *cur, QFilter{Rcode: "nxdomain", Client: "192.0.2.6"})
	for _, c := range []struct {
		name      string
		got, want *QStatsResult
	}{{"all", gotQ, wantQ}, {"nx client", gotNX, wantNX}} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Fatalf("%s differs after a restart:\n got %+v\nwant %+v", c.name, c.got, c.want)
		}
	}
	if gotQ.Sums.Updates != 1 || gotQ.Sums.UpdFail != 1 || gotQ.Sums.Total != 6 || gotQ.Since != q.start.Unix() {
		t.Fatalf("sums %+v since %d", gotQ.Sums, gotQ.Since)
	}
	if len(gotNX.Domains) == 0 || gotNX.Domains[0].Name != "typo.example" || gotNX.Domains[0].Count != 2 {
		t.Fatalf("the client/domain pairs did not come back: %+v", gotNX.Domains)
	}
	gotH := h2.Query(from, t0.Add(2*time.Minute))
	gotH.Now, wantH.Now = HostNow{}, HostNow{}
	if !reflect.DeepEqual(gotH, wantH) {
		t.Fatalf("host differs after a restart:\n got %+v\nwant %+v", gotH, wantH)
	}
	if len(gotH.FS) != 2 || gotH.FS[1].Mount != "/var" || gotH.Since != h.start.Unix() {
		t.Fatalf("mounts/since: %+v since %d", gotH.FS, gotH.Since)
	}
	// new traffic after the restart adds to the restored minutes
	q2.Record(mustAddr("192.0.2.5"), "a.example", 1, false, 0)
	if r := q2.Query(from, *cur, QFilter{}); r.Sums.Total != 7 {
		t.Fatalf("total after more traffic %d", r.Sums.Total)
	}
}

func TestPersistMissingStaleAndDamaged(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 5, 0, time.UTC)
	dir := t.TempDir()
	q0, h0, _, _ := persistFixture(t, t0)
	swapStats(t, q0, h0)
	if n, err := loadPersisted(dir); n != 0 || err != nil {
		t.Fatalf("a missing file is not an error: %d %v", n, err)
	}
	if n, err := loadPersisted(""); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	if err := savePersisted(dir); err != nil {
		t.Fatal(err)
	}

	// 40 days later everything is past the retention and is left out
	late := t0.Add(40 * 24 * time.Hour)
	q1, _ := qsTestStats(late)
	h1, _, _ := fakeHost(late)
	swapStats(t, q1, h1)
	if _, err := loadPersisted(dir); err != nil {
		t.Fatal(err)
	}
	if r := q1.Query(late.Add(-time.Hour), late, QFilter{}); r.Sums.Total != 0 || len(r.Clients) != 0 {
		t.Fatalf("stale data was loaded: %+v", r.Sums)
	}

	// a file cut short keeps what came before and says so
	raw, _ := os.ReadFile(filepath.Join(dir, persistFile))
	os.WriteFile(filepath.Join(dir, persistFile), raw[:len(raw)/2], 0o600)
	q2, _ := qsTestStats(t0)
	h2, _, _ := fakeHost(t0)
	swapStats(t, q2, h2)
	if _, err := loadPersisted(dir); err == nil {
		t.Fatal("a truncated file must be reported")
	}

	// not gzip, a wrong format and a missing header are refused without loading anything
	for name, body := range map[string]string{"text": "hello", "format": `{"k":"hdr","hdr":{"ddgw_stats":99}}` + "\n", "nohdr": `{"k":"mins","mins":[[1,1,1,0,0,0,0,0,0]]}` + "\n"} {
		f, _ := os.Create(filepath.Join(dir, persistFile))
		if name == "text" {
			f.WriteString(body)
		} else {
			zw := gzip.NewWriter(f)
			zw.Write([]byte(body))
			zw.Close()
		}
		f.Close()
		q3, _ := qsTestStats(t0)
		swapStats(t, q3, h2)
		if n, err := loadPersisted(dir); err == nil || n != 0 {
			t.Fatalf("%s: n=%d err=%v", name, n, err)
		}
	}
}

func TestPersistLoopSavesAtStop(t *testing.T) {
	t0 := time.Now()
	q, h, _, _ := persistFixture(t, t0)
	q.now = time.Now
	swapStats(t, q, h)
	dir := filepath.Join(t.TempDir(), "state") // does not exist yet
	stop := make(chan struct{})
	final := startPersistence(dir, stop)
	close(stop)
	final()
	raw, err := os.ReadFile(filepath.Join(dir, persistFile))
	if err != nil || len(raw) == 0 {
		t.Fatalf("no file after the final save: %v", err)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v", st.Mode().Perm())
	}
	if !strings.Contains(persistFile, ".gz") {
		t.Fatal(persistFile)
	}
}

// a file written before updates were a kind of their own has one kind fewer per top-list record; it must still load
func TestPersistLoadsFileWithFewerKinds(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 5, 0, time.UTC)
	q, _ := qsTestStats(t0)
	h, _, _ := fakeHost(t0)
	swapStats(t, q, h)
	dir := t.TempDir()
	f, _ := os.Create(filepath.Join(dir, persistFile))
	zw := gzip.NewWriter(f)
	m := t0.Unix() / 60 / qsFineSpan
	zw.Write([]byte(`{"k":"hdr","hdr":{"ddgw_stats":1}}` + "\n"))
	zw.Write([]byte(`{"k":"fine","s":` + strconv.FormatInt(m, 10) + `,"top":[{"c":{"192.0.2.5":2},"d":{"a.example":2},"t":{"A":2},"p":{"192.0.2.5":{"a.example":2}},"np":1,"u":2},{},{},{},{}]}` + "\n"))
	zw.Close()
	f.Close()
	if n, err := loadPersisted(dir); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if r := q.Query(t0.Add(-time.Hour), t0, QFilter{Rcode: "noerror"}); len(r.Domains) != 1 || r.Domains[0].Count != 2 {
		t.Fatalf("%+v", r.Domains)
	}
}
