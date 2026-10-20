package main

import (
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Go 1.24 sizes its scheduler by the machine's CPU count and ignores a cgroup CPU quota.  In a container (an LXC
// container with a CPU limit, a systemd unit with CPUQuota=, Docker --cpus) that means dozens of threads
// sharing, say, two CPUs' worth of time: the kernel throttles the whole group in bursts, which costs throughput
// and shows up as latency.  limitProcsToCgroup lowers GOMAXPROCS to the quota, as a newer Go does by itself.

// parseCPUMax reads cgroup v2 "cpu.max" ("max 100000" or "200000 100000") and returns the CPUs it allows.
func parseCPUMax(s string) (float64, bool) {
	f := strings.Fields(s)
	if len(f) < 1 || f[0] == "max" {
		return 0, false
	}
	quota, err := strconv.ParseFloat(f[0], 64)
	period := 100000.0
	if len(f) > 1 {
		if p, perr := strconv.ParseFloat(f[1], 64); perr == nil {
			period = p
		}
	}
	if err != nil || quota <= 0 || period <= 0 {
		return 0, false
	}
	return quota / period, true
}

// cgroupCPUs is the CPU quota of this process's cgroup, if there is one (v2, then v1).
func cgroupCPUs(read func(string) (string, bool)) (float64, bool) {
	if s, ok := read("/sys/fs/cgroup/cpu.max"); ok {
		return parseCPUMax(s)
	}
	q, ok1 := read("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	p, ok2 := read("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if ok1 && ok2 {
		qv := strings.TrimSpace(q)
		if qv == "-1" {
			return 0, false
		}
		return parseCPUMax(qv + " " + strings.TrimSpace(p))
	}
	return 0, false
}

func readSmallFile(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// procsFor is the GOMAXPROCS to use for a quota of cpus on a machine with have CPUs: the quota rounded up, never
// more than there are, never less than one.
func procsFor(cpus float64, have int) int {
	n := int(math.Ceil(cpus))
	if n < 1 {
		n = 1
	}
	if n > have {
		n = have
	}
	return n
}

// limitProcsToCgroup applies the quota unless GOMAXPROCS was set by hand; it returns the CPUs it limited to, or 0.
func limitProcsToCgroup() (quota float64, procs int) {
	if os.Getenv("GOMAXPROCS") != "" {
		return 0, 0
	}
	q, ok := cgroupCPUs(readSmallFile)
	if !ok {
		return 0, 0
	}
	have := runtime.GOMAXPROCS(0)
	n := procsFor(q, have)
	if n >= have {
		return q, 0
	}
	runtime.GOMAXPROCS(n)
	return q, n
}
