package main

import (
	"testing"
	"time"
)

func TestParseProc(t *testing.T) {
	busy, total := parseCPUStat("cpu  100 10 50 800 40 5 5 0 0 0\ncpu0 1 1 1 1 1 1 1 0 0 0\n")
	if total != 1010 || busy != 1010-800-40 {
		t.Fatalf("cpu %d/%d", busy, total)
	}
	mt, ma, st, sf := parseMeminfo("MemTotal:        8000 kB\nMemFree:         1000 kB\nMemAvailable:    4000 kB\nSwapTotal:       2000 kB\nSwapFree:        1500 kB\n")
	if mt != 8000*1024 || ma != 4000*1024 || st != 2000*1024 || sf != 1500*1024 {
		t.Fatalf("mem %d %d %d %d", mt, ma, st, sf)
	}
	if _, a, _, _ := parseMeminfo("MemTotal: 100 kB\nMemFree: 10 kB\nBuffers: 5 kB\nCached: 15 kB\n"); a != 30*1024 {
		t.Fatalf("old kernel available %d", a)
	}
	d := parseDiskstats("   8       0 sda 1 2 3 4 5 6 7 8 9 1234 11\n   8       1 sda1 1 2 3 4 5 6 7 8 9 99 11\n   7       0 loop0 1 2 3 4 5 6 7 8 9 5 11\n")
	if d["sda"] != 1234 || d["sda1"] != 99 || len(d) != 2 {
		t.Fatalf("disk %v", d)
	}
	n := parseNetDev("Inter-|   Receive |  Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n  eth0: 1000 1 0 0 0 0 0 0 2000 2 0 0 0 0 0 0\n    lo: 5 1 0 0 0 0 0 0 5 1 0 0 0 0 0 0\n")
	if n["eth0"].rx != 1000 || n["eth0"].tx != 2000 || len(n) != 2 {
		t.Fatalf("net %v", n)
	}
	for name, want := range map[string]bool{"eth0": true, "ens3": true, "bond0": true, "lo": false, "ddgw1.1": false, "veth12": false, "docker0": false, "br-ab12": false, "virbr0": false} {
		if netCounted(name) != want {
			t.Errorf("netCounted(%s) != %v", name, want)
		}
	}
	if fs := statMounts("/dev/x / ext4 rw 0 0\ntmpfs /run tmpfs rw 0 0\nproc /proc proc rw 0 0\n"); len(fs) > 1 {
		t.Fatalf("only disk filesystems: %+v", fs)
	}
}

func fakeHost(t0 time.Time) (*HostStats, *time.Time, *hostReading) {
	cur := t0
	rd := &hostReading{cores: 2, memTotal: 1000, memAvail: 600, ioTicks: map[string]uint64{"sda": 0}, net: map[string]netCounters{"eth0": {speed: 1000, up: true}, "lo": {up: true}, "ddgw1.1": {}},
		fs: []fsUsage{{Mount: "/", Device: "sda1", Type: "ext4", Total: 1000, Used: 400, Pct: 40}}}
	h := NewHostStats()
	h.now = func() time.Time { return cur }
	h.read = func() (*hostReading, error) {
		c := *rd
		c.ioTicks, c.net = map[string]uint64{}, map[string]netCounters{}
		for k, v := range rd.ioTicks {
			c.ioTicks[k] = v
		}
		for k, v := range rd.net {
			c.net[k] = v
		}
		return &c, nil
	}
	return h, &cur, rd
}

func TestHostSampleAndQuery(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	h, cur, rd := fakeHost(t0)
	h.Sample() // primes the counters
	for i := 1; i <= 6; i++ {
		*cur = t0.Add(time.Duration(i) * 10 * time.Second)
		rd.cpuTotal += 200
		rd.cpuBusy += 50          // 25 %
		rd.ioTicks["sda"] += 2500 // 25 % of 10 s
		c := rd.net["eth0"]
		c.rx += 1_250_000 // 1 Mb/s over 10 s
		c.tx += 125_000
		rd.net["eth0"] = c
		l := rd.net["lo"]
		l.rx += 9e9
		rd.net["lo"] = l
		h.Sample()
	}
	*cur = t0.Add(70 * time.Second)
	r := h.Query(t0.Add(-time.Hour), *cur)
	last := -1
	for i, v := range r.CPU {
		if v >= 0 {
			last = i
		}
	}
	if last < 0 {
		t.Fatal("no cpu points")
	}
	if r.Now.CPU != 25 || r.Now.MemPct != 40 || r.Now.IO != 25 || r.Now.Rx != 1_000_000 || r.Now.Tx != 100_000 {
		t.Fatalf("now: %+v", r.Now)
	}
	if r.Now.NetUtil != 0.1 {
		t.Fatalf("net util %v", r.Now.NetUtil)
	}
	var cnt int
	for _, in := range r.Now.Ifaces {
		if in.Counted {
			cnt++
		}
	}
	if cnt != 1 {
		t.Fatalf("only eth0 counts: %+v", r.Now.Ifaces)
	}
	var cpuSum float64
	var pts int
	for i, v := range r.CPU {
		if v >= 0 {
			cpuSum += v
			pts++
			if r.CPUMax[i] < v {
				t.Fatalf("peak below average at %d", i)
			}
		}
	}
	if pts == 0 || cpuSum/float64(pts) != 25 {
		t.Fatalf("cpu series %v", r.CPU[last])
	}
	if r.Mem[last] != 40 || r.IO[last] != 25 || r.Rx[last] != 1_000_000 || r.Tx[last] != 100_000 {
		t.Fatalf("series at %d: mem %v io %v rx %v tx %v", last, r.Mem[last], r.IO[last], r.Rx[last], r.Tx[last])
	}
	if len(r.FS) != 1 || r.FS[0].Mount != "/" || r.FS[0].Pct[last] != 40 {
		t.Fatalf("fs %+v", r.FS)
	}
	// a gap stays -1, and a counter that went backwards does not give a huge rate
	if r.CPU[0] != -1 {
		t.Fatalf("empty minute should be -1: %v", r.CPU[0])
	}
	rd.cpuTotal = 10
	c := rd.net["eth0"]
	c.rx = 1
	rd.net["eth0"] = c
	*cur = cur.Add(10 * time.Second)
	h.Sample()
	if n := h.last; n.Rx != 0 || n.CPU != -1 {
		t.Fatalf("after reset: %+v", n)
	}
}

func TestHostRetentionAndSteps(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	h, cur, rd := fakeHost(t0)
	h.Sample()
	*cur = t0.Add(10 * time.Second)
	rd.cpuTotal, rd.cpuBusy = 100, 10
	h.Sample()
	*cur = t0.Add(40 * 24 * time.Hour)
	r := h.Query(t0, *cur)
	if r.Step != 10800 || r.From != cur.Add(-hostRetain).Unix() {
		t.Fatalf("step %d from %d", r.Step, r.From)
	}
	for _, v := range r.CPU {
		if v >= 0 {
			t.Fatal("data older than 30 days must be gone")
		}
	}
	if len(r.CPU) > qsMaxPoints {
		t.Fatalf("too many points: %d", len(r.CPU))
	}
}

func TestHostMountsTracked(t *testing.T) {
	h := NewHostStats()
	for i := 0; i < hostMaxFS+3; i++ {
		h.fsIndex("/m" + fmtN(i))
	}
	if len(h.mounts) != hostMaxFS || h.fsIndex("/m"+fmtN(hostMaxFS+1)) != -1 || h.fsIndex("/m0") != 0 {
		t.Fatalf("mounts %v", h.mounts)
	}
}

// The load a node shows its cluster: CPU, memory and the fullest disk; "over the limit" means strictly more than 85%.
func TestHostLoadStrain(t *testing.T) {
	h, now, rd := fakeHost(time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC))
	if _, ok := h.Load(); ok {
		t.Fatal("a load before any sample")
	}
	rd.memAvail = 100 // 90% of the memory in use
	rd.fs = []fsUsage{{Mount: "/", Total: 100, Used: 40, Pct: 40}, {Mount: "/var", Total: 50, Used: 45, Pct: 90}}
	h.Sample()
	*now = now.Add(10 * time.Second)
	rd.cpuBusy, rd.cpuTotal = 95, 100 // 95% busy over the interval
	h.Sample()
	l, ok := h.Load()
	if !ok || l.Mem != 90 || l.Disk != 90 || l.DiskMount != "/var" || l.CPU < 94 || l.CPU > 96 {
		t.Fatalf("load: %+v %v", l, ok)
	}
	if got := l.Strained(true); len(got) != 3 || got[0] != "CPU 95%" || got[1] != "memory 90%" || got[2] != "disk 90% (/var)" {
		t.Fatalf("strained: %v", got)
	}
	if got := l.Strained(false); got[2] != "disk 90%" {
		t.Fatalf("short form: %v", got)
	}
	for _, c := range []struct {
		l    HostLoad
		want int
	}{{HostLoad{CPU: 85, Mem: 85, Disk: 85}, 0}, {HostLoad{CPU: 85.1, Mem: 10, Disk: 10}, 1}, {HostLoad{CPU: -1, Mem: 86, Disk: -1}, 1}, {HostLoad{CPU: -1, Mem: -1, Disk: -1}, 0}} {
		if got := c.l.Strained(true); len(got) != c.want {
			t.Errorf("%+v: %v", c.l, got)
		}
	}
}
