package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The memory guard.  The statistics, the host history and the answer cache all live in memory, and the
// statistics and host history grow up to 30 days of data.  Every memGuardEvery the guard looks at how much
// memory the machine is using (the same figure as Monitor ▸ Host: what programs hold, caches excluded; inside a
// container with a memory limit, the container's own use against its limit, whichever is higher).  At or above
// the limit (85 % by default) it drops the oldest data: each round removes the oldest tenth of the time span the
// statistics and the host history cover, and a tenth of the answer cache (the least recently used entries), gives
// the memory back to the system and measures again, up to memGuardRounds rounds per check, until use is below
// the limit.  If it is still above, the next check carries on.  Newer data always outlives older data, and the newest hour is never dropped (it is tiny, and a machine that
// is short of memory for another reason should not lose its recent history as well).
//
// The limit can be changed with the environment variable DDGW_MEMORY_LIMIT_PERCENT (1-99) of the daemon.

const (
	memGuardEvery  = 30 * time.Second
	memGuardLimit  = 85.0
	memGuardStep   = 0.10      // of the time span covered (statistics, host) or of the entries (cache) per round
	memGuardRounds = 5         // rounds per check
	memGuardKeep   = time.Hour // the newest hour of statistics and host history is never dropped
	memGuardSettle = time.Second
	memGuardEnv    = "DDGW_MEMORY_LIMIT_PERCENT"
)

// MemGuardInfo is what the Host page and --host say about the guard.
type MemGuardInfo struct {
	Limit    float64 `json:"limit_pct"`
	Trims    int     `json:"trims"` // checks that had to drop data
	Last     int64   `json:"last"`  // when the last one happened
	LastNote string  `json:"last_note"`
}

type memGuard struct {
	mu     sync.Mutex
	limit  float64
	usage  func() (float64, error) // percent of memory in use
	caches func() []*respCache
	now    func() time.Time
	free   func() // hand freed memory back to the system
	settle time.Duration
	info   MemGuardInfo
}

// memguard is the daemon-wide guard.
var memguard = newMemGuard()

func newMemGuard() *memGuard {
	g := &memGuard{limit: memGuardLimit, usage: memUsagePercent, now: time.Now, free: debug.FreeOSMemory, settle: memGuardSettle,
		caches: func() []*respCache { return nil }}
	if v := strings.TrimSpace(os.Getenv(memGuardEnv)); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 1 && n <= 99 {
			g.limit = n
		} else {
			warnf("memory guard: %s=%q ignored (want a number from 1 to 99)", memGuardEnv, v)
		}
	}
	g.info.Limit = g.limit
	return g
}

func (g *memGuard) Info() MemGuardInfo {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.info
}

// Run checks until stop is closed.
func (g *memGuard) Run(stop <-chan struct{}) {
	t := time.NewTicker(memGuardEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			g.check()
		}
	}
}

// trimmed is what one round removed.
type trimmed struct {
	statSlots, statMinutes, hostMinutes, cacheEntries int
}

func (t *trimmed) add(o trimmed) {
	t.statSlots += o.statSlots
	t.statMinutes += o.statMinutes
	t.hostMinutes += o.hostMinutes
	t.cacheEntries += o.cacheEntries
}

func (t trimmed) any() bool { return t.statSlots+t.statMinutes+t.hostMinutes+t.cacheEntries > 0 }

// check measures once and, at or above the limit, trims.  It reports whether it dropped anything.
func (g *memGuard) check() bool {
	pct, err := g.usage()
	if err != nil || pct < g.limit {
		return false
	}
	start := pct
	var total trimmed
	rounds := 0
	var oldest time.Time
	for ; rounds < memGuardRounds && pct >= g.limit; rounds++ {
		r, o := g.trimRound()
		if !r.any() {
			break
		}
		if oldest.IsZero() {
			oldest = o
		}
		total.add(r)
		g.free()
		time.Sleep(g.settle)
		if p, err := g.usage(); err == nil {
			pct = p
		}
	}
	if !total.any() {
		warnf("memory guard: memory use is %.1f%% (limit %.0f%%), but there is no statistics, host history or cache left to drop", start, g.limit)
		return false
	}
	note := fmt.Sprintf("memory was %.1f%% (limit %.0f%%): dropped the oldest history (%d statistics minutes and %d top-list slots, %d host minutes) and %d cached answers; now %.1f%%",
		start, g.limit, total.statMinutes, total.statSlots, total.hostMinutes, total.cacheEntries, pct)
	warnf("memory guard: %s", note)
	g.mu.Lock()
	g.info.Trims++
	g.info.Last = g.now().Unix()
	g.info.LastNote = note
	g.mu.Unlock()
	return true
}

// trimRound drops the oldest tenth of the history and of the cache.  The second result is where the
// history started before the round.
func (g *memGuard) trimRound() (trimmed, time.Time) {
	var r trimmed
	now := g.now()
	oldest := qstats.oldest()
	if h := hoststats.oldest(); !h.IsZero() && (oldest.IsZero() || h.Before(oldest)) {
		oldest = h
	}
	if !oldest.IsZero() {
		span := now.Sub(oldest)
		cut := time.Duration(float64(span) * memGuardStep)
		if cut < time.Minute {
			cut = time.Minute
		}
		before := oldest.Add(cut).Truncate(time.Hour).Add(time.Hour) // up to a whole hour: the top lists are kept in hours
		if floor := now.Add(-memGuardKeep); before.After(floor) {
			before = floor
		}
		if before.After(oldest) {
			r.statSlots, r.statMinutes = qstats.dropBefore(before)
			r.hostMinutes = hoststats.dropBefore(before)
		}
	}
	for _, c := range g.caches() {
		if c != nil {
			r.cacheEntries += c.dropOldest(memGuardStep)
		}
	}
	return r, oldest
}

// ── what the collectors give up ──

// oldest is the start of the oldest data held, zero when there is none.
func (s *QStats) oldest() time.Time {
	s.flush()
	s.mu.Lock()
	defer s.mu.Unlock()
	var min int64
	note := func(m int64) {
		if m > 0 && (min == 0 || m < min) {
			min = m
		}
	}
	for i := range s.mins {
		if s.mins[i].total > 0 {
			note(s.mins[i].stamp)
		}
	}
	for i := range s.fine {
		if s.fine[i].stamp > 0 {
			note(s.fine[i].stamp * qsFineSpan)
		}
	}
	for i := range s.coarse {
		if s.coarse[i].stamp > 0 {
			note(s.coarse[i].stamp * qsCoarseSpan)
		}
	}
	if min == 0 {
		return time.Time{}
	}
	return time.Unix(min*60, 0)
}

// dropBefore forgets everything older than t: the top lists that end before it and the minute counters.
func (s *QStats) dropBefore(t time.Time) (slots, minutes int) {
	m := t.Unix() / 60
	s.flush()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.mins {
		if s.mins[i].stamp > 0 && s.mins[i].stamp < m {
			if s.mins[i].total > 0 {
				minutes++
			}
			s.mins[i] = qsMin{}
		}
	}
	for i := range s.fine {
		if s.fine[i].stamp > 0 && (s.fine[i].stamp+1)*qsFineSpan <= m {
			s.fine[i] = qsTop{}
			slots++
		}
	}
	for i := range s.coarse {
		if s.coarse[i].stamp > 0 && (s.coarse[i].stamp+1)*qsCoarseSpan <= m {
			s.coarse[i] = qsTop{}
			slots++
		}
	}
	return
}

func (h *HostStats) oldest() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	var min int64
	for i := range h.slots {
		if s := &h.slots[i]; s.n > 0 && s.stamp > 0 && (min == 0 || s.stamp < min) {
			min = s.stamp
		}
	}
	if min == 0 {
		return time.Time{}
	}
	return time.Unix(min*60, 0)
}

func (h *HostStats) dropBefore(t time.Time) (minutes int) {
	m := t.Unix() / 60
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.slots {
		if h.slots[i].n > 0 && h.slots[i].stamp < m {
			h.slots[i] = hostSlot{}
			minutes++
		}
	}
	return
}

// dropOldest removes a fraction of the entries, least recently used first (at least one if there are any).
func (c *respCache) dropOldest(frac float64) int {
	dropped := 0
	for _, sh := range c.shards { // the same fraction from every shard
		sh.mu.Lock()
		n := int(float64(len(sh.m))*frac + 0.999)
		for i := 0; i < n && sh.lru.Len() > 0; i++ {
			sh.drop(sh.lru.Back().Value.(*cacheEntry))
			dropped++
		}
		sh.mu.Unlock()
	}
	return dropped
}

// ── how much memory is in use ──

// cgroup files; variables so a test can point them elsewhere
var (
	cgroupV2Max     = "/sys/fs/cgroup/memory.max"
	cgroupV2Current = "/sys/fs/cgroup/memory.current"
	cgroupV2Stat    = "/sys/fs/cgroup/memory.stat"
	cgroupV1Limit   = "/sys/fs/cgroup/memory/memory.limit_in_bytes"
	cgroupV1Usage   = "/sys/fs/cgroup/memory/memory.usage_in_bytes"
	cgroupV1Stat    = "/sys/fs/cgroup/memory/memory.stat"
	procMeminfo     = "/proc/meminfo"
)

func readUint(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return n, err == nil
}

func statValue(path, key string) uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == key {
			n, _ := strconv.ParseUint(f[1], 10, 64)
			return n
		}
	}
	return 0
}

// cgroupUsage returns the memory limit of the container the daemon runs in, and what it uses (caches that can
// be dropped excluded), when there is a limit.
func cgroupUsage() (limit, used uint64, ok bool) {
	if l, k := readUint(cgroupV2Max); k { // "max" does not parse: no limit
		if cur, k := readUint(cgroupV2Current); k {
			in := statValue(cgroupV2Stat, "inactive_file")
			if in > cur {
				in = cur
			}
			return l, cur - in, true
		}
	}
	if l, k := readUint(cgroupV1Limit); k && l < 1<<60 { // an unlimited v1 group shows a huge number
		if cur, k := readUint(cgroupV1Usage); k {
			in := statValue(cgroupV1Stat, "total_inactive_file")
			if in > cur {
				in = cur
			}
			return l, cur - in, true
		}
	}
	return 0, 0, false
}

// memUsagePercent is the share of memory in use, the larger of the machine's and the container's.
func memUsagePercent() (float64, error) {
	b, err := os.ReadFile(procMeminfo)
	if err != nil {
		return 0, err
	}
	total, avail, _, _ := parseMeminfo(string(b))
	if total == 0 {
		return 0, fmt.Errorf("no MemTotal in %s", procMeminfo)
	}
	pct := 100 * float64(total-minU(avail, total)) / float64(total)
	if limit, used, ok := cgroupUsage(); ok && limit > 0 {
		if c := 100 * float64(used) / float64(limit); c > pct {
			pct = c
		}
	}
	return pct, nil
}
