package main

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Host statistics for Monitor → Host: CPU, memory, disk and network use of the machine ddgw runs
// on, sampled every hostSampleEvery and kept in memory for hostRetain (30 days), per minute, like
// the query statistics.  persist.go saves them with the query statistics.  Everything comes from /proc and
// /sys (and statfs), so there is nothing to install.

const (
	hostSampleEvery = 10 * time.Second
	hostRetain      = 30 * 24 * time.Hour
	hostSlots       = 30 * 24 * 60
	hostMaxFS       = 6 // filesystems whose use is kept over time
)

// hostSlot holds one minute: sums of the samples (so an average can be taken over any span) and peaks.
type hostSlot struct {
	stamp                int64 // minutes since the epoch
	n                    uint16
	cpu, cpuMax          float32
	mem                  float32
	io, ioMax            float32            // busiest disk, percent of the time it was busy
	rx, rxMax, tx, txMax float32            // bytes per second over the counted interfaces
	fs                   [hostMaxFS]float32 // filesystem use in percent, the latest sample; < 0 = none
}

// hostReading is everything one sample reads.
type hostReading struct {
	cpuBusy, cpuTotal   uint64
	memTotal, memAvail  uint64
	swapTotal, swapFree uint64
	load                [3]float64
	cores               int
	ioTicks             map[string]uint64 // per whole disk: milliseconds spent doing I/O
	net                 map[string]netCounters
	fs                  []fsUsage
}

type netCounters struct {
	rx, tx uint64
	speed  int // link speed in Mb/s, 0 = unknown
	up     bool
}

type fsUsage struct {
	Mount  string  `json:"mount"`
	Device string  `json:"device"`
	Type   string  `json:"type"`
	Total  uint64  `json:"total"`
	Used   uint64  `json:"used"`
	Pct    float64 `json:"pct"`
}

// HostStats is the collector.
type HostStats struct {
	mu     sync.Mutex
	start  time.Time
	now    func() time.Time
	read   func() (*hostReading, error)
	slots  [hostSlots]hostSlot
	prev   *hostReading
	prevAt time.Time
	mounts []string // tracked filesystems, in the order they were first seen
	last   HostNow
	run    bool
}

func NewHostStats() *HostStats {
	return &HostStats{start: time.Now(), now: time.Now, read: readHost}
}

// hoststats is the daemon-wide collector.
var hoststats = NewHostStats()

// IfaceNow is one network interface in the current view.
type IfaceNow struct {
	Name    string  `json:"name"`
	Up      bool    `json:"up"`
	Counted bool    `json:"counted"` // part of the totals
	Speed   int     `json:"speed_mbps,omitempty"`
	Rx      float64 `json:"rx_bps"` // bits per second
	Tx      float64 `json:"tx_bps"`
	UtilPct float64 `json:"util_pct"` // the busier direction as a percent of the link speed; -1 = speed unknown
}

// HostNow is the latest sample, for the tiles and tables.
type HostNow struct {
	At        int64      `json:"at"`
	Cores     int        `json:"cores"`
	CPU       float64    `json:"cpu_pct"`
	Load      [3]float64 `json:"load"`
	MemTotal  uint64     `json:"mem_total"`
	MemUsed   uint64     `json:"mem_used"`
	MemPct    float64    `json:"mem_pct"`
	SwapTotal uint64     `json:"swap_total"`
	SwapUsed  uint64     `json:"swap_used"`
	IO        float64    `json:"io_pct"`
	Rx        float64    `json:"rx_bps"` // bits per second, counted interfaces
	Tx        float64    `json:"tx_bps"`
	NetUtil   float64    `json:"net_util_pct"` // busiest counted link, -1 = unknown
	FS        []fsUsage  `json:"fs"`
	Ifaces    []IfaceNow `json:"ifaces"`
}

// Sample takes one reading and folds it into the current minute.
func (h *HostStats) Sample() {
	r, err := h.read()
	if err != nil || r == nil {
		return
	}
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	prev, prevAt := h.prev, h.prevAt
	h.prev, h.prevAt = r, now
	n := HostNow{At: now.Unix(), Cores: r.cores, Load: r.load, MemTotal: r.memTotal, SwapTotal: r.swapTotal, FS: r.fs, NetUtil: -1, Ifaces: []IfaceNow{}}
	if r.memTotal > 0 {
		n.MemUsed = r.memTotal - minU(r.memAvail, r.memTotal)
		n.MemPct = 100 * float64(n.MemUsed) / float64(r.memTotal)
	}
	n.SwapUsed = r.swapTotal - minU(r.swapFree, r.swapTotal)
	var cpu, io, rx, tx float64 = -1, -1, -1, -1
	if prev != nil {
		dt := now.Sub(prevAt).Seconds()
		if dt > 0 {
			if dtot := r.cpuTotal - prev.cpuTotal; r.cpuTotal > prev.cpuTotal && dtot > 0 {
				cpu = clampPct(100 * float64(r.cpuBusy-minU(r.cpuBusy, prev.cpuBusy)) / float64(dtot))
			}
			io = 0
			for d, t := range r.ioTicks {
				if p, ok := prev.ioTicks[d]; ok && t >= p {
					if v := clampPct(100 * float64(t-p) / (dt * 1000)); v > io {
						io = v
					}
				}
			}
			rx, tx = 0, 0
			names := make([]string, 0, len(r.net))
			for k := range r.net {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, name := range names {
				c := r.net[name]
				in := IfaceNow{Name: name, Up: c.up, Counted: netCounted(name), Speed: c.speed, UtilPct: -1}
				if p, ok := prev.net[name]; ok {
					if c.rx >= p.rx {
						in.Rx = 8 * float64(c.rx-p.rx) / dt
					}
					if c.tx >= p.tx {
						in.Tx = 8 * float64(c.tx-p.tx) / dt
					}
				}
				if c.speed > 0 {
					in.UtilPct = clampPct(100 * math.Max(in.Rx, in.Tx) / (float64(c.speed) * 1e6))
				}
				if in.Counted {
					rx += in.Rx / 8
					tx += in.Tx / 8
					if in.UtilPct > n.NetUtil {
						n.NetUtil = in.UtilPct
					}
				}
				n.Ifaces = append(n.Ifaces, in)
			}
		}
	}
	n.CPU, n.IO, n.Rx, n.Tx = cpu, io, rx*8, tx*8
	h.last = n

	m := now.Unix() / 60
	s := &h.slots[m%hostSlots]
	if s.stamp != m {
		*s = hostSlot{stamp: m}
		for i := range s.fs {
			s.fs[i] = -1
		}
	}
	for _, f := range r.fs {
		i := h.fsIndex(f.Mount)
		if i >= 0 {
			s.fs[i] = float32(f.Pct)
		}
	}
	if cpu < 0 { // the first sample has nothing to compare with: only memory and disk space
		s.mem += float32(n.MemPct)
		return
	}
	s.n++
	s.cpu += float32(cpu)
	s.cpuMax = maxF(s.cpuMax, float32(cpu))
	s.mem += float32(n.MemPct)
	s.io += float32(io)
	s.ioMax = maxF(s.ioMax, float32(io))
	s.rx += float32(rx)
	s.rxMax = maxF(s.rxMax, float32(rx))
	s.tx += float32(tx)
	s.txMax = maxF(s.txMax, float32(tx))
}

func (h *HostStats) fsIndex(mount string) int {
	for i, m := range h.mounts {
		if m == mount {
			return i
		}
	}
	if len(h.mounts) < hostMaxFS {
		h.mounts = append(h.mounts, mount)
		return len(h.mounts) - 1
	}
	return -1
}

// netCounted says whether an interface adds to the network totals: not loopback, not ddgw's own
// virtual-MAC links, not container or hypervisor plumbing (their traffic is counted on the real link).
func netCounted(name string) bool {
	for _, p := range []string{"lo", "ddgw", "veth", "docker", "br-", "virbr", "cni", "flannel", "cali", "kube", "tun", "tap"} {
		if name == p || (p != "lo" && strings.HasPrefix(name, p)) {
			return false
		}
	}
	return true
}

func clampPct(v float64) float64 { return math.Max(0, math.Min(100, v)) }
func minU(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
func maxF(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

// HostResult is the answer to a query: one point per Step seconds from Start.  A value below
// zero means no data for that point.
type HostResult struct {
	Start   int64          `json:"start"`
	Step    int            `json:"step"`
	From    int64          `json:"from"`
	To      int64          `json:"to"`
	Since   int64          `json:"since"`
	CPU     []float64      `json:"cpu"`
	CPUMax  []float64      `json:"cpu_max"`
	Mem     []float64      `json:"mem"`
	IO      []float64      `json:"io"`
	IOMax   []float64      `json:"io_max"`
	Rx      []float64      `json:"rx"` // bits per second
	RxMax   []float64      `json:"rx_max"`
	Tx      []float64      `json:"tx"`
	TxMax   []float64      `json:"tx_max"`
	FS      []HostFSSeries `json:"fs"`
	Now     HostNow        `json:"now"`
	Guard   MemGuardInfo   `json:"guard"`             // what the memory guard has dropped
	Cluster *ClusterInfo   `json:"cluster,omitempty"` // set when the numbers are those of several nodes added together
}

// HostFSSeries is the use of one filesystem over time, in percent.
type HostFSSeries struct {
	Mount string    `json:"mount"`
	Pct   []float64 `json:"pct"`
}

// Query returns the series for [from, to), cut to what is kept and aligned to the step.
func (h *HostStats) Query(from, to time.Time) *HostResult {
	now := h.now()
	if to.IsZero() || to.After(now) {
		to = now
	}
	if oldest := now.Add(-hostRetain); from.Before(oldest) {
		from = oldest
	}
	if !from.Before(to) {
		from = to.Add(-time.Hour)
	}
	step := stepFor(to.Sub(from))
	start := from.Unix() / step * step
	end := (to.Unix() + step) / step * step
	n := int((end - start) / step)
	if n > qsMaxPoints {
		n = qsMaxPoints
	}
	mk := func() []float64 {
		s := make([]float64, n)
		for i := range s {
			s[i] = -1
		}
		return s
	}
	r := &HostResult{Start: start, Step: int(step), From: from.Unix(), To: to.Unix(), Since: h.start.Unix(),
		CPU: mk(), CPUMax: mk(), Mem: mk(), IO: mk(), IOMax: mk(), Rx: mk(), RxMax: mk(), Tx: mk(), TxMax: mk(), FS: []HostFSSeries{}}
	h.mu.Lock()
	defer h.mu.Unlock()
	r.Now = h.last
	r.Guard = memguard.Info()
	if r.Now.FS == nil {
		r.Now.FS = []fsUsage{}
	}
	if r.Now.Ifaces == nil {
		r.Now.Ifaces = []IfaceNow{}
	}
	for _, m := range h.mounts {
		r.FS = append(r.FS, HostFSSeries{Mount: m, Pct: mk()})
	}
	for i := 0; i < n; i++ {
		var cnt, memCnt float64
		var cpu, mem, io, rx, tx float64
		var cm, im, rm, tm float64
		fs := make([]float64, len(h.mounts))
		fsSeen := make([]bool, len(h.mounts))
		any := false
		for m := (start + int64(i)*step) / 60; m < (start+int64(i+1)*step)/60; m++ {
			s := &h.slots[m%hostSlots]
			if s.stamp != m {
				continue
			}
			any = true
			for k := range h.mounts {
				if k < hostMaxFS && s.fs[k] >= 0 {
					fs[k], fsSeen[k] = float64(s.fs[k]), true
				}
			}
			if s.n == 0 {
				continue
			}
			cnt += float64(s.n)
			cpu += float64(s.cpu)
			io += float64(s.io)
			rx += float64(s.rx)
			tx += float64(s.tx)
			mem += float64(s.mem)
			memCnt += float64(s.n)
			cm, im = math.Max(cm, float64(s.cpuMax)), math.Max(im, float64(s.ioMax))
			rm, tm = math.Max(rm, float64(s.rxMax)), math.Max(tm, float64(s.txMax))
		}
		if !any {
			continue
		}
		for k := range h.mounts {
			if fsSeen[k] {
				r.FS[k].Pct[i] = round2(fs[k])
			}
		}
		if cnt > 0 {
			r.CPU[i], r.CPUMax[i] = round2(cpu/cnt), round2(cm)
			r.Mem[i] = round2(mem / memCnt)
			r.IO[i], r.IOMax[i] = round2(io/cnt), round2(im)
			r.Rx[i], r.RxMax[i] = round2(8*rx/cnt), round2(8*rm)
			r.Tx[i], r.TxMax[i] = round2(8*tx/cnt), round2(8*tm)
		}
	}
	return r
}

// ── reading /proc ──────────────────────────────────────────────────────────────

func readHost() (*hostReading, error) {
	r := &hostReading{ioTicks: map[string]uint64{}, net: map[string]netCounters{}, cores: runtimeCores()}
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return nil, err
	}
	r.cpuBusy, r.cpuTotal = parseCPUStat(string(b))
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		r.memTotal, r.memAvail, r.swapTotal, r.swapFree = parseMeminfo(string(b))
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		for i := 0; i < 3 && i < len(f); i++ {
			r.load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	}
	if b, err := os.ReadFile("/proc/diskstats"); err == nil {
		for d, t := range parseDiskstats(string(b)) {
			if _, err := os.Stat("/sys/block/" + d); err == nil { // whole disks only, not partitions
				r.ioTicks[d] = t
			}
		}
	}
	if b, err := os.ReadFile("/proc/net/dev"); err == nil {
		for name, c := range parseNetDev(string(b)) {
			base := "/sys/class/net/" + name + "/"
			if _, err := os.Stat(base + "master"); err == nil {
				continue // a bridge or bond port: the bridge or bond carries its traffic
			}
			if sp, err := os.ReadFile(base + "speed"); err == nil {
				if v, err := strconv.Atoi(strings.TrimSpace(string(sp))); err == nil && v > 0 {
					c.speed = v
				}
			}
			if st, err := os.ReadFile(base + "operstate"); err == nil {
				s := strings.TrimSpace(string(st))
				c.up = s == "up" || s == "unknown" // loopback and some virtual links report "unknown"
			}
			r.net[name] = c
		}
	}
	if b, err := os.ReadFile("/proc/mounts"); err == nil {
		r.fs = statMounts(string(b))
	}
	return r, nil
}

func runtimeCores() int {
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		n := 0
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "processor") {
				n++
			}
		}
		if n > 0 {
			return n
		}
	}
	return 1
}

// parseCPUStat reads the aggregate "cpu" line: busy = everything but idle and iowait.
func parseCPUStat(s string) (busy, total uint64) {
	for _, l := range strings.Split(s, "\n") {
		f := strings.Fields(l)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var v [10]uint64
		for i := 1; i < len(f) && i <= 10; i++ {
			v[i-1], _ = strconv.ParseUint(f[i], 10, 64)
		}
		// user nice system idle iowait irq softirq steal (guest is already inside user)
		for i := 0; i < 8; i++ {
			total += v[i]
		}
		return total - v[3] - v[4], total
	}
	return 0, 0
}

func parseMeminfo(s string) (total, avail, swapTotal, swapFree uint64) {
	sc := bufio.NewScanner(strings.NewReader(s))
	var free, buffers, cached uint64
	haveAvail := false
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, _ := strconv.ParseUint(f[0], 10, 64)
		n *= 1024
		switch k {
		case "MemTotal":
			total = n
		case "MemAvailable":
			avail, haveAvail = n, true
		case "MemFree":
			free = n
		case "Buffers":
			buffers = n
		case "Cached":
			cached = n
		case "SwapTotal":
			swapTotal = n
		case "SwapFree":
			swapFree = n
		}
	}
	if !haveAvail { // kernels before 3.14
		avail = free + buffers + cached
	}
	return
}

// parseDiskstats returns the milliseconds each device spent doing I/O (the 13th field).
func parseDiskstats(s string) map[string]uint64 {
	out := map[string]uint64{}
	for _, l := range strings.Split(s, "\n") {
		f := strings.Fields(l)
		if len(f) < 13 {
			continue
		}
		name := f[2]
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
			continue
		}
		t, err := strconv.ParseUint(f[12], 10, 64)
		if err != nil {
			continue
		}
		out[name] = t
	}
	return out
}

func parseNetDev(s string) map[string]netCounters {
	out := map[string]netCounters{}
	for _, l := range strings.Split(s, "\n") {
		name, rest, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 16 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		out[strings.TrimSpace(name)] = netCounters{rx: rx, tx: tx}
	}
	return out
}

var realFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true, "f2fs": true, "vfat": true,
	"ntfs": true, "ntfs3": true, "exfat": true, "reiserfs": true, "jfs": true, "ufs": true}

// statMounts lists the disk filesystems (one per device) with their use, largest first.
func statMounts(mounts string) []fsUsage {
	var out []fsUsage
	seen := map[string]bool{}
	for _, l := range strings.Split(mounts, "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || !realFS[f[2]] || seen[f[0]] {
			continue
		}
		mp := strings.ReplaceAll(f[1], "\\040", " ")
		var st syscall.Statfs_t
		if err := syscall.Statfs(mp, &st); err != nil || st.Blocks == 0 {
			continue
		}
		seen[f[0]] = true
		bs := uint64(st.Bsize)
		total := st.Blocks * bs
		used := (st.Blocks - st.Bfree) * bs
		avail := st.Bavail * bs
		// like df: percent of what a normal user can use
		pct := 0.0
		if used+avail > 0 {
			pct = 100 * float64(used) / float64(used+avail)
		}
		out = append(out, fsUsage{Mount: mp, Device: filepath.Base(f[0]), Type: f[2], Total: total, Used: used, Pct: math.Round(pct*10) / 10})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	if len(out) > hostMaxFS {
		out = out[:hostMaxFS]
	}
	return out
}

// Run samples until stop is closed.  Only one runner is started per collector.
func (h *HostStats) Run(stop <-chan struct{}) {
	h.mu.Lock()
	if h.run {
		h.mu.Unlock()
		return
	}
	h.run = true
	h.mu.Unlock()
	defer func() { h.mu.Lock(); h.run = false; h.mu.Unlock() }()
	h.Sample()
	t := time.NewTicker(hostSampleEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			h.Sample()
		}
	}
}

// hostStrainPct is how full the CPU, the memory or a disk may get before the node's shape on the Topology drawing turns yellow (degraded).
const hostStrainPct = 85.0

// HostLoad is what a node shows of its own load to the drawing: the CPU (the last 10-second sample, -1 until there are two
// samples), the memory in use and the fullest of the tracked filesystems, in percent.
type HostLoad struct {
	CPU       float64 `json:"cpu_pct"`
	Mem       float64 `json:"mem_pct"`
	Disk      float64 `json:"disk_pct"`
	DiskMount string  `json:"disk_mount,omitempty"`
}

// Load is the latest sample as a HostLoad; false before the first sample.
func (h *HostStats) Load() (HostLoad, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.last.At == 0 {
		return HostLoad{}, false
	}
	l := HostLoad{CPU: h.last.CPU, Mem: h.last.MemPct, Disk: -1}
	for _, f := range h.last.FS {
		if f.Pct > l.Disk {
			l.Disk, l.DiskMount = f.Pct, f.Mount
		}
	}
	return l, true
}

// hostLoadFn is where the daemon reads its own load (tests replace it).
var hostLoadFn = func() (HostLoad, bool) { return hoststats.Load() }

// Strained lists what is over hostStrainPct: "CPU 91%", "memory 87%", "disk 88% (/var)" (mount named when withMount).
func (l HostLoad) Strained(withMount bool) []string {
	var out []string
	if l.CPU > hostStrainPct {
		out = append(out, fmt.Sprintf("CPU %.0f%%", l.CPU))
	}
	if l.Mem > hostStrainPct {
		out = append(out, fmt.Sprintf("memory %.0f%%", l.Mem))
	}
	if l.Disk > hostStrainPct {
		if withMount && l.DiskMount != "" {
			out = append(out, fmt.Sprintf("disk %.0f%% (%s)", l.Disk, l.DiskMount))
		} else {
			out = append(out, fmt.Sprintf("disk %.0f%%", l.Disk))
		}
	}
	return out
}

// hostRange reads --host-range / ?from= like the statistics do.
func hostRange(from, to string) (time.Time, time.Time, error) { return qstatsRange(from, to) }
