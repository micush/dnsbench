package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// guardRig has a statistics collector, a host collector and a cache holding 30 days of data, and a guard
// whose memory reading is scripted.
type guardRig struct {
	g     *memGuard
	q     *QStats
	h     *HostStats
	c     *respCache
	now   time.Time
	reads []float64
	nread int
	freed int
}

func newGuardRig(t *testing.T, reads ...float64) *guardRig {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	r := &guardRig{now: now, reads: reads}
	q, qcur := qsTestStats(now.Add(-30 * 24 * time.Hour))
	h, hcur, rd := fakeHost(now.Add(-30 * 24 * time.Hour))
	cl := netip.MustParseAddr("192.0.2.5")
	// one query and one host sample every 6 hours for 30 days
	for i := 0; i < 30*4; i++ {
		ts := now.Add(-30 * 24 * time.Hour).Add(time.Duration(i) * 6 * time.Hour)
		*qcur, *hcur = ts, ts
		q.Record(cl, "n"+strconv.Itoa(i)+".example", 1, false, 0)
		rd.cpuTotal += 100 // a sample only counts once there is a CPU delta to compare with
		rd.cpuBusy += 10
		h.Sample()
	}
	*qcur, *hcur = now, now
	c, _ := newTestCache(1000, 3600)
	c.now = func() time.Time { return now }
	for i := 0; i < 100; i++ {
		qq := cacheQuery("c"+strconv.Itoa(i)+".example", 1, nil)
		k, _ := c.keyFor(qq, false, cl, false, 24, 56)
		c.put(k, mkResp(qq, 0, [][]byte{aRR("c"+strconv.Itoa(i)+".example", 3000)}, nil, nil))
	}
	r.q, r.h, r.c = q, h, c
	r.g = &memGuard{limit: 85, now: func() time.Time { return now }, free: func() { r.freed++ }, settle: 0,
		caches: func() []*respCache { return []*respCache{c} }}
	r.g.info.Limit = 85
	r.g.usage = func() (float64, error) {
		v := r.reads[len(r.reads)-1]
		if r.nread < len(r.reads) {
			v = r.reads[r.nread]
		}
		r.nread++
		return v, nil
	}
	swapStats(t, q, h)
	return r
}

func TestMemGuardDoesNothingBelowTheLimit(t *testing.T) {
	r := newGuardRig(t, 84.9)
	before := r.q.oldest()
	if r.g.check() || r.q.oldest() != before || r.c.Len() != 100 || r.freed != 0 || r.g.Info().Trims != 0 {
		t.Fatal("trimmed below the limit")
	}
}

func TestMemGuardTrimsOldestFirstUntilUnderTheLimit(t *testing.T) {
	// 90 % at the check, 88 % after round 1, 86 % after round 2, 84 % after round 3: three rounds
	r := newGuardRig(t, 90, 88, 86, 84)
	oldest0, hOldest0 := r.q.oldest(), r.h.oldest()
	if oldest0.IsZero() || hOldest0.IsZero() {
		t.Fatal("no data in the rig")
	}
	if !r.g.check() {
		t.Fatal("did not trim at 90%")
	}
	if r.freed != 3 {
		t.Fatalf("rounds %d, want 3 (it must stop once under the limit)", r.freed)
	}
	o := r.q.oldest()
	span := r.now.Sub(oldest0)
	if !o.After(oldest0) || o.Sub(oldest0) < span/5 || o.Sub(oldest0) > span/2 {
		t.Fatalf("three rounds of a tenth should drop roughly 27%% of %v, the oldest moved by %v", span, o.Sub(oldest0))
	}
	if !r.h.oldest().After(hOldest0) {
		t.Fatal("host history not trimmed")
	}
	if r.c.Len() >= 100 || r.c.Len() < 70 {
		t.Fatalf("cache has %d entries after 3 rounds of a tenth (want about 73)", r.c.Len())
	}
	// the newest data is untouched
	res := r.q.Query(r.now.Add(-24*time.Hour), r.now, QFilter{})
	if res.Sums.Total == 0 {
		t.Fatal("the newest data was dropped")
	}
	old := r.q.Query(oldest0, oldest0.Add(24*time.Hour), QFilter{})
	if old.Sums.Total != 0 || len(old.Domains) != 0 {
		t.Fatalf("the oldest day is still there: %+v", old.Sums)
	}
	info := r.g.Info()
	if info.Trims != 1 || info.Last != r.now.Unix() || info.LastNote == "" || info.Limit != 85 {
		t.Fatalf("info %+v", info)
	}
	// the least recently used cache entries went first: touch c99, trim, c99 survives while c0 is gone
	qq := cacheQuery("c99.example", 1, nil)
	k, _ := r.c.keyFor(qq, false, netip.MustParseAddr("192.0.2.5"), false, 24, 56)
	if r.c.get(k, qq) == nil {
		t.Fatal("c99 should still be cached")
	}
	q0 := cacheQuery("c0.example", 1, nil)
	k0, _ := r.c.keyFor(q0, false, netip.MustParseAddr("192.0.2.5"), false, 24, 56)
	if r.c.get(k0, q0) != nil {
		t.Fatal("the least recently used entry should have been dropped first")
	}
}

func TestMemGuardRoundsAreCappedPerCheck(t *testing.T) {
	r := newGuardRig(t, 95) // never gets better
	if !r.g.check() || r.freed != memGuardRounds {
		t.Fatalf("rounds %d, want %d", r.freed, memGuardRounds)
	}
	first := r.q.oldest()
	if !r.g.check() || !r.q.oldest().After(first) {
		t.Fatal("the next check must carry on")
	}
}

func TestMemGuardNothingLeftToDrop(t *testing.T) {
	q, _ := qsTestStats(time.Now())
	h, _, _ := fakeHost(time.Now())
	swapStats(t, q, h)
	freed := 0
	g := &memGuard{limit: 85, now: time.Now, free: func() { freed++ }, caches: func() []*respCache { return nil },
		usage: func() (float64, error) { return 99, nil }}
	if g.check() || freed != 0 || g.Info().Trims != 0 {
		t.Fatal("with nothing to drop it must not claim a trim")
	}
}

func TestMemGuardWhileCollectorsAreBusy(t *testing.T) {
	r := newGuardRig(t, 95)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				r.q.Record(netip.MustParseAddr("192.0.2.9"), "busy"+strconv.Itoa(n%50)+".example", 1, false, 0)
				r.q.Query(r.now.Add(-time.Hour), r.now, QFilter{})
				r.h.Query(r.now.Add(-time.Hour), r.now)
				qq := cacheQuery("x"+strconv.Itoa(n%30)+".example", 1, nil)
				if k, ok := r.c.keyFor(qq, false, netip.MustParseAddr("192.0.2.9"), false, 24, 56); ok && r.c.get(k, qq) == nil {
					r.c.put(k, mkResp(qq, 0, [][]byte{aRR("x.example", 300)}, nil, nil))
				}
			}
		}(i)
	}
	for i := 0; i < 3; i++ {
		r.g.check()
	}
	close(stop)
	wg.Wait()
}

func TestMemUsageReadsMeminfoAndCgroup(t *testing.T) {
	dir := t.TempDir()
	w := func(name, body string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	oldV2m, oldV2c, oldV2s, oldV1l, oldV1u, oldV1s, oldMI := cgroupV2Max, cgroupV2Current, cgroupV2Stat, cgroupV1Limit, cgroupV1Usage, cgroupV1Stat, procMeminfo
	t.Cleanup(func() {
		cgroupV2Max, cgroupV2Current, cgroupV2Stat, cgroupV1Limit, cgroupV1Usage, cgroupV1Stat, procMeminfo = oldV2m, oldV2c, oldV2s, oldV1l, oldV1u, oldV1s, oldMI
	})
	procMeminfo = w("meminfo", "MemTotal: 1000 kB\nMemFree: 100 kB\nMemAvailable: 400 kB\n")
	cgroupV2Max, cgroupV2Current, cgroupV2Stat = filepath.Join(dir, "none1"), filepath.Join(dir, "none2"), filepath.Join(dir, "none3")
	cgroupV1Limit, cgroupV1Usage, cgroupV1Stat = filepath.Join(dir, "none4"), filepath.Join(dir, "none5"), filepath.Join(dir, "none6")
	if p, err := memUsagePercent(); err != nil || p < 59.9 || p > 60.1 {
		t.Fatalf("host only: %v %v", p, err)
	}
	// a container limited to 100 MB that uses 90 MB of which 20 MB is inactive file cache: 70 %, below the host's 60 %? no, above
	cgroupV2Max = w("memory.max", "104857600\n")
	cgroupV2Current = w("memory.current", "94371840\n")
	cgroupV2Stat = w("memory.stat", "anon 1\ninactive_file 20971520\nactive_file 5\n")
	if p, _ := memUsagePercent(); p < 69.9 || p > 70.1 {
		t.Fatalf("cgroup v2: %v", p)
	}
	cgroupV2Max = w("memory.max2", "max\n") // no limit: the host figure again
	if p, _ := memUsagePercent(); p < 59.9 || p > 60.1 {
		t.Fatalf("unlimited cgroup: %v", p)
	}
	cgroupV1Limit, cgroupV1Usage, cgroupV1Stat = w("l", "100000\n"), w("u", "95000\n"), w("s", "total_inactive_file 5000\n")
	if p, _ := memUsagePercent(); p < 89.9 || p > 90.1 {
		t.Fatalf("cgroup v1: %v", p)
	}
	cgroupV1Limit = w("l2", "9223372036854771712\n") // v1 "unlimited"
	if p, _ := memUsagePercent(); p < 59.9 || p > 60.1 {
		t.Fatalf("v1 unlimited: %v", p)
	}
	procMeminfo = w("bad", "nothing here\n")
	if _, err := memUsagePercent(); err == nil {
		t.Fatal("no MemTotal must be an error")
	}
}

func TestMemGuardLimitFromEnvironment(t *testing.T) {
	t.Setenv(memGuardEnv, "70")
	if g := newMemGuard(); g.limit != 70 || g.Info().Limit != 70 {
		t.Fatalf("limit %v", g.limit)
	}
	for _, bad := range []string{"0", "100", "abc", "-5"} {
		t.Setenv(memGuardEnv, bad)
		if g := newMemGuard(); g.limit != memGuardLimit {
			t.Fatalf("%q must be ignored, got %v", bad, g.limit)
		}
	}
	t.Setenv(memGuardEnv, "")
	if g := newMemGuard(); g.limit != 85 {
		t.Fatal("default must be 85")
	}
}

// the newest hour is never dropped, however high memory use stays
func TestMemGuardKeepsTheNewestHour(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	q, qcur := qsTestStats(now.Add(-50 * time.Minute))
	h, hcur, rd := fakeHost(now.Add(-50 * time.Minute))
	for i := 0; i < 50; i++ {
		ts := now.Add(-time.Duration(50-i) * time.Minute)
		*qcur, *hcur = ts, ts
		q.Record(netip.MustParseAddr("192.0.2.5"), "recent.example", 1, false, 0)
		rd.cpuTotal += 100
		rd.cpuBusy += 10
		h.Sample()
	}
	*qcur, *hcur = now, now
	swapStats(t, q, h)
	g := &memGuard{limit: 85, now: func() time.Time { return now }, free: func() {}, caches: func() []*respCache { return nil },
		usage: func() (float64, error) { return 99, nil }}
	if g.check() {
		t.Fatal("it dropped history younger than an hour")
	}
	if r := q.Query(now.Add(-time.Hour), now, QFilter{}); r.Sums.Total != 50 || len(r.Domains) != 1 {
		t.Fatalf("recent statistics lost: %+v", r.Sums)
	}
	if r := h.Query(now.Add(-time.Hour), now); r.CPU[len(r.CPU)-2] < 0 && r.CPU[len(r.CPU)-3] < 0 {
		t.Fatal("recent host history lost")
	}
	// the same data, two hours old, goes: only what is older than the newest hour
	later := now.Add(70 * time.Minute)
	g.now = func() time.Time { return later }
	*qcur, *hcur = later, later
	if !g.check() {
		t.Fatal("history older than an hour should go at 99%")
	}
	if r := q.Query(now.Add(-time.Hour), later, QFilter{}); r.Sums.Total >= 50 {
		t.Fatalf("nothing dropped: %+v", r.Sums)
	}
}
