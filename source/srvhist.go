package main

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A per-server history of how an upstream server has answered: each minute, how many live queries it answered and
// failed, how many probe queries passed and failed, and the latency of the answers (average and worst).  It is
// kept per server address, not per pool, so it survives a pool being rebuilt (any change to a gateway's DNS
// settings does that) and is what the Topology page's "Statistics…" graphs and `ddgw --server-stats` show.
// Kept 7 days; persist.go saves it with the other statistics (stats.json.gz), so a restart keeps it and shows the
// time the daemon was down as a gap.
//
// The forwarding path pays for it with one atomic add per answer (the latency of one answer in eight is folded in
// under the lock the server already takes for that); a once-a-minute flush turns the counters into a slot.

const (
	srvHistKeep    = 7 * 24 * 60 // minutes of history kept per server
	srvHistMaxSpan = 7 * 24 * time.Hour
)

// srvSlot is one minute of one server.
type srvSlot struct {
	Min     int64 // unix minute the slot covers
	OK      uint32
	Fail    uint32
	PrOK    uint32
	PrFail  uint32
	LatSum  uint64 // microseconds, over LatN answers
	LatN    uint32
	LatMaxU uint32 // microseconds
	Hit     uint32 // gateways: queries answered from the cache
	Up      uint32 // gateways: availability samples that found it working
	Down    uint32 // gateways: … and that found it failing
}

type srvSeries struct {
	ok, fail, prOK, prFail atomic.Uint32 // this minute so far
	hit, up, down          atomic.Uint32 // gateways: cache hits, availability samples
	tick                   atomic.Uint32 // picks the answers whose latency is sampled

	mu     sync.Mutex
	latSum uint64
	latN   uint32
	latMax uint32
	slots  []srvSlot
}

// lat folds one answer's round-trip time into the minute under way.
func (s *srvSeries) lat(d time.Duration) {
	if s == nil {
		return
	}
	us := d.Microseconds()
	if us < 0 {
		us = 0
	}
	if us > 1<<31 {
		us = 1 << 31
	}
	s.mu.Lock()
	s.latSum += uint64(us)
	s.latN++
	if uint32(us) > s.latMax {
		s.latMax = uint32(us)
	}
	s.mu.Unlock()
}

// The nil-safe counters keep tests that build a Server by hand working.
func (s *srvSeries) addOK() {
	if s != nil {
		s.ok.Add(1)
	}
}
func (s *srvSeries) addFail() {
	if s != nil {
		s.fail.Add(1)
	}
}
func (s *srvSeries) addPrOK() {
	if s != nil {
		s.prOK.Add(1)
	}
}
func (s *srvSeries) addPrFail() {
	if s != nil {
		s.prFail.Add(1)
	}
}

func (s *srvSeries) addHit() {
	if s != nil {
		s.hit.Add(1)
	}
}

// avail notes one availability sample of a gateway.
func (s *srvSeries) avail(up bool) {
	if s == nil {
		return
	}
	if up {
		s.up.Add(1)
	} else {
		s.down.Add(1)
	}
}

// sampled says whether this answer's latency should be timed: one in eight, so a front end answering tens of
// thousands of queries a second neither reads the clock nor takes the lock for each of them.
func (s *srvSeries) sampled() bool { return s != nil && s.tick.Add(1)&7 == 0 }

// Keys other than a server's address: one gateway, and one monitored domain on one server.
func gwKey(gid int) string { return "gw:" + strconv.Itoa(gid) }
func domKey(addr, name, typ string) string {
	return "dom:" + addr + "|" + strings.ToLower(strings.TrimSuffix(name, ".")) + "|" + strings.ToUpper(typ)
}

type srvHistory struct {
	mu    sync.Mutex
	m     map[string]*srvSeries
	start sync.Once
}

var srvhist = &srvHistory{m: map[string]*srvSeries{}}

// series returns the history of one server, creating it (and starting the flusher the first time).
func (h *srvHistory) series(addr string) *srvSeries {
	h.start.Do(func() { go h.run() })
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.m[addr]
	if s == nil {
		s = &srvSeries{}
		h.m[addr] = s
	}
	return s
}

// run flushes at every minute boundary for as long as the daemon lives.
func (h *srvHistory) run() {
	for {
		now := time.Now()
		time.Sleep(now.Truncate(time.Minute).Add(time.Minute).Sub(now) + 50*time.Millisecond)
		h.flush(time.Now())
	}
}

// take returns the counters of the minute under way (and clears them when reset is set).
func (s *srvSeries) take(reset bool) srvSlot {
	var sl srvSlot
	if reset {
		sl.OK, sl.Fail, sl.PrOK, sl.PrFail = s.ok.Swap(0), s.fail.Swap(0), s.prOK.Swap(0), s.prFail.Swap(0)
		sl.Hit, sl.Up, sl.Down = s.hit.Swap(0), s.up.Swap(0), s.down.Swap(0)
	} else {
		sl.OK, sl.Fail, sl.PrOK, sl.PrFail = s.ok.Load(), s.fail.Load(), s.prOK.Load(), s.prFail.Load()
		sl.Hit, sl.Up, sl.Down = s.hit.Load(), s.up.Load(), s.down.Load()
	}
	s.mu.Lock()
	sl.LatSum, sl.LatN, sl.LatMaxU = s.latSum, s.latN, s.latMax
	if reset {
		s.latSum, s.latN, s.latMax = 0, 0, 0
	}
	s.mu.Unlock()
	return sl
}

func (sl srvSlot) empty() bool {
	return sl.OK == 0 && sl.Fail == 0 && sl.PrOK == 0 && sl.PrFail == 0 && sl.LatN == 0 && sl.Hit == 0 && sl.Up == 0 && sl.Down == 0
}

// flush closes the minute that has just ended: every server that did anything in it gets a slot.
func (h *srvHistory) flush(now time.Time) {
	min := now.Unix()/60 - 1
	h.mu.Lock()
	all := make(map[string]*srvSeries, len(h.m))
	for k, v := range h.m {
		all[k] = v
	}
	h.mu.Unlock()
	for _, s := range all {
		sl := s.take(true)
		s.mu.Lock()
		if !sl.empty() {
			sl.Min = min
			s.slots = append(s.slots, sl)
		}
		if n := len(s.slots); n > 0 && s.slots[0].Min < min-srvHistKeep {
			i := sort.Search(n, func(i int) bool { return s.slots[i].Min >= min-srvHistKeep })
			s.slots = append([]srvSlot(nil), s.slots[i:]...)
		}
		s.mu.Unlock()
	}
}

// persistTo emits one record per server (its closed minutes, oldest first).
func (h *srvHistory) persistTo(emit func(pRec) error) error {
	h.mu.Lock()
	all := make(map[string]*srvSeries, len(h.m))
	for k, v := range h.m {
		all[k] = v
	}
	h.mu.Unlock()
	addrs := make([]string, 0, len(all))
	for a := range all {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	oldest := time.Now().Unix()/60 - srvHistKeep
	for _, a := range addrs {
		s := all[a]
		s.mu.Lock()
		rows := make([][9]uint64, 0, len(s.slots))
		for _, sl := range s.slots {
			if sl.Min <= oldest {
				continue
			}
			rows = append(rows, [9]uint64{uint64(sl.Min), uint64(sl.OK)<<32 | uint64(sl.Fail), uint64(sl.PrOK)<<32 | uint64(sl.PrFail), sl.LatSum, uint64(sl.LatN), uint64(sl.LatMaxU), uint64(sl.Hit), uint64(sl.Up), uint64(sl.Down)})
		}
		s.mu.Unlock()
		if len(rows) == 0 {
			continue
		}
		if err := emit(pRec{K: "srv", Srv: a, SrvMin: rows}); err != nil {
			return err
		}
	}
	return nil
}

// restore reads one server's saved minutes back (before traffic arrives); minutes already present are kept.
func (h *srvHistory) restore(r pRec) int {
	if r.Srv == "" || len(r.SrvMin) == 0 {
		return 0
	}
	s := h.series(r.Srv)
	now := time.Now().Unix() / 60
	s.mu.Lock()
	defer s.mu.Unlock()
	have := map[int64]bool{}
	for _, sl := range s.slots {
		have[sl.Min] = true
	}
	n := 0
	for _, v := range r.SrvMin {
		m := int64(v[0])
		if m <= now-srvHistKeep || m > now || have[m] {
			continue
		}
		s.slots = append(s.slots, srvSlot{Min: m, OK: uint32(v[1] >> 32), Fail: uint32(v[1]), PrOK: uint32(v[2] >> 32), PrFail: uint32(v[2]), LatSum: v[3], LatN: uint32(v[4]), LatMaxU: uint32(v[5]), Hit: uint32(v[6]), Up: uint32(v[7]), Down: uint32(v[8])})
		n++
	}
	sort.Slice(s.slots, func(i, j int) bool { return s.slots[i].Min < s.slots[j].Min })
	return n
}

// ServerStatsResult is what the Topology page and --server-stats show: equal-length series over buckets of Step seconds
// starting at Start; a value below 0 is "no data in that bucket".
type ServerStatsResult struct {
	Addr      string    `json:"addr"`
	Known     bool      `json:"known"` // the daemon has recorded something for this server
	From      int64     `json:"from"`
	To        int64     `json:"to"`
	Start     int64     `json:"start"`
	Step      int       `json:"step"`
	Latency   []float64 `json:"latency"`              // average ms
	LatMax    []float64 `json:"latency_max"`          // worst ms
	Loss      []float64 `json:"loss"`                 // servers: % of the probe queries that failed; gateways: of the client queries; domains: of the probes
	QueryLoss []float64 `json:"query_loss,omitempty"` // servers: % of the client queries sent to it that failed (timeouts, SERVFAIL, REFUSED)
	Queries   []float64 `json:"queries"`              // live queries answered or failed
	QPS       []float64 `json:"qps"`                  // gateways: queries per second
	HitQPS    []float64 `json:"hit_qps"`              // gateways: of which answered from the cache
	Avail     []float64 `json:"avail"`                // gateways: % of the availability samples that found it working
	// over the whole range
	Answered     uint64  `json:"answered"`
	Failed       uint64  `json:"failed"`
	ProbesOK     uint64  `json:"probes_ok"`
	ProbesBad    uint64  `json:"probes_failed"`
	AvgMS        float64 `json:"avg_ms"`
	MaxMS        float64 `json:"max_ms"`
	LossPct      float64 `json:"loss_pct"`
	QueryLossPct float64 `json:"query_loss_pct,omitempty"` // servers: over the whole range, as QueryLoss
	Hits         uint64  `json:"hits"`                     // gateways: answered from the cache
	AvailPct     float64 `json:"avail_pct"`                // gateways: availability over the range, -1 when never sampled
	Kind         string  `json:"kind"`                     // server | gateway | domain
	Nodes        int     `json:"nodes"`                    // how many nodes' history this is (a gateway's is every reachable node's)
	NodesOff     int     `json:"nodes_off"`                // cluster nodes that could not be asked
}

// srvAcc is the sum of what one history key recorded over a bucket (or a whole range).
type srvAcc struct {
	OK     uint64 `json:"ok"`
	Fail   uint64 `json:"fail"`
	PrOK   uint64 `json:"pr_ok"`
	PrFail uint64 `json:"pr_fail"`
	Hit    uint64 `json:"hit"`
	Up     uint64 `json:"up"`
	Down   uint64 `json:"down"`
	LatSum uint64 `json:"lat_sum"`
	LatN   uint32 `json:"lat_n"`
	LatMax uint32 `json:"lat_max"`
}

func (a *srvAcc) add(o srvAcc) {
	a.OK += o.OK
	a.Fail += o.Fail
	a.PrOK += o.PrOK
	a.PrFail += o.PrFail
	a.Hit += o.Hit
	a.Up += o.Up
	a.Down += o.Down
	a.LatSum += o.LatSum
	a.LatN += o.LatN
	if o.LatMax > a.LatMax {
		a.LatMax = o.LatMax
	}
}

// srvRaw is the unprocessed buckets of one key over a range. Nodes of a cluster exchange these (summing buckets is exact;
// summing averages is not) to draw one gateway graph from every node's history.
type srvRaw struct {
	Key   string   `json:"key"`
	From  int64    `json:"from"`
	To    int64    `json:"to"`
	Start int64    `json:"start"`
	Step  int      `json:"step"`
	Known bool     `json:"known"`
	B     []srvAcc `json:"b"`
}

// srvRange clamps [from, to] to the retention and chooses the bucket size.
func srvRange(from, to time.Time) (time.Time, time.Time, int, int64, int) {
	if to.Sub(from) > srvHistMaxSpan {
		from = to.Add(-srvHistMaxSpan)
	}
	span := to.Sub(from)
	step := 60
	switch {
	case span > 2*24*time.Hour:
		step = 1800
	case span > 2*time.Hour:
		step = 300
	}
	start := from.Unix() / int64(step) * int64(step)
	return from, to, step, start, int((to.Unix()-start)/int64(step)) + 1
}

// Raw sums one key's minutes into the buckets of [from, to] (at most 7 days).
func (h *srvHistory) Raw(key string, from, to time.Time) (srvRaw, error) {
	if !to.After(from) {
		return srvRaw{}, errors.New("the end of the range is before its start")
	}
	from, to, step, start, n := srvRange(from, to)
	r := srvRaw{Key: key, From: from.Unix(), To: to.Unix(), Start: start, Step: step, B: make([]srvAcc, n)}
	h.mu.Lock()
	s := h.m[key]
	h.mu.Unlock()
	if s == nil {
		return r, nil
	}
	s.mu.Lock()
	slots := append([]srvSlot(nil), s.slots...)
	s.mu.Unlock()
	slots = append(slots, func() srvSlot { c := s.take(false); c.Min = time.Now().Unix() / 60; return c }())
	for _, sl := range slots {
		if !sl.empty() {
			r.Known = true
			break
		}
	}
	for _, sl := range slots {
		t := sl.Min * 60
		if t < start || t > to.Unix() || sl.empty() {
			continue
		}
		r.B[int((t-start)/int64(step))].add(srvAcc{OK: uint64(sl.OK), Fail: uint64(sl.Fail), PrOK: uint64(sl.PrOK), PrFail: uint64(sl.PrFail),
			Hit: uint64(sl.Hit), Up: uint64(sl.Up), Down: uint64(sl.Down), LatSum: sl.LatSum, LatN: sl.LatN, LatMax: sl.LatMaxU})
	}
	return r, nil
}

// mergeRaw adds another node's buckets of the same key and range to r; it reports false (and changes nothing) when the
// other one covers a different range.
func (r *srvRaw) merge(o srvRaw) bool {
	if o.Key != r.Key || o.Start != r.Start || o.Step != r.Step || len(o.B) != len(r.B) {
		return false
	}
	for i := range r.B {
		r.B[i].add(o.B[i])
	}
	r.Known = r.Known || o.Known
	return true
}

// Stats turns buckets into the series and totals the Topology page and --server-stats show.
func (r srvRaw) Stats() ServerStatsResult {
	n, step := len(r.B), r.Step
	out := ServerStatsResult{Addr: r.Key, Known: r.Known, From: r.From, To: r.To, Start: r.Start, Step: step,
		Latency: make([]float64, n), LatMax: make([]float64, n), Loss: make([]float64, n), Queries: make([]float64, n),
		QPS: make([]float64, n), HitQPS: make([]float64, n), Avail: make([]float64, n), AvailPct: -1, Kind: kindOf(r.Key), Nodes: 1}
	var total srvAcc
	srv := out.Kind == "server"
	loss := func(a srvAcc) float64 {
		if srv { // a server's loss is its probes': client queries that time out on it say little about its health
			if t := a.PrOK + a.PrFail; t > 0 {
				return float64(a.PrFail) * 100 / float64(t)
			}
			return -1
		}
		if t := a.OK + a.Fail + a.PrOK + a.PrFail; t > 0 {
			return float64(a.Fail+a.PrFail) * 100 / float64(t)
		}
		return -1
	}
	queryLoss := func(a srvAcc) float64 {
		if t := a.OK + a.Fail; t > 0 {
			return float64(a.Fail) * 100 / float64(t)
		}
		return -1
	}
	if srv {
		out.QueryLoss = make([]float64, n)
	}
	for i, b := range r.B {
		total.add(b)
		out.Latency[i], out.LatMax[i], out.Loss[i], out.Queries[i], out.QPS[i], out.HitQPS[i], out.Avail[i] = -1, -1, loss(b), -1, -1, -1, -1
		if b.Up+b.Down > 0 {
			out.Avail[i] = round2(float64(b.Up) * 100 / float64(b.Up+b.Down))
		}
		if b.LatN > 0 {
			out.Latency[i] = round2(float64(b.LatSum) / float64(b.LatN) / 1000)
			out.LatMax[i] = round2(float64(b.LatMax) / 1000)
		}
		if b.OK+b.Fail > 0 {
			out.Queries[i] = float64(b.OK + b.Fail)
			out.QPS[i] = round2(float64(b.OK+b.Fail) / float64(step))
			out.HitQPS[i] = round2(float64(b.Hit) / float64(step))
		}
		if out.Loss[i] >= 0 {
			out.Loss[i] = round2(out.Loss[i])
		}
		if srv {
			out.QueryLoss[i] = queryLoss(b)
			if out.QueryLoss[i] >= 0 {
				out.QueryLoss[i] = round2(out.QueryLoss[i])
			}
		}
	}
	out.Answered, out.Failed, out.ProbesOK, out.ProbesBad = total.OK, total.Fail, total.PrOK, total.PrFail
	if total.LatN > 0 {
		out.AvgMS = round2(float64(total.LatSum) / float64(total.LatN) / 1000)
		out.MaxMS = round2(float64(total.LatMax) / 1000)
	}
	if l := loss(total); l >= 0 {
		out.LossPct = round2(l)
	}
	if srv {
		if l := queryLoss(total); l >= 0 {
			out.QueryLossPct = round2(l)
		}
	}
	out.Hits = total.Hit
	if total.Up+total.Down > 0 {
		out.AvailPct = round2(float64(total.Up) * 100 / float64(total.Up+total.Down))
	}
	return out
}

// Query summarises one key over [from, to] (at most 7 days), from this node's history.
func (h *srvHistory) Query(addr string, from, to time.Time) (ServerStatsResult, error) {
	r, err := h.Raw(addr, from, to)
	if err != nil {
		return ServerStatsResult{}, err
	}
	return r.Stats(), nil
}

// kindOf says what a history key stands for.
func kindOf(key string) string {
	switch {
	case strings.HasPrefix(key, "gw:"):
		return "gateway"
	case strings.HasPrefix(key, "dom:"):
		return "domain"
	}
	return "server"
}
