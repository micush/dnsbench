package main

import (
	"math"
	"os"
	"runtime/debug"
)

// A soft memory limit for Go's garbage collector.  The answer cache, the statistics and the host history are big
// and long-lived, and with the default GOGC=100 the heap may grow to twice what is live before a collection runs.
// That garbage counts as "memory in use" for the memory guard (memguard.go), which would then drop statistics and
// cache entries that were never a problem.  A soft limit makes the collector work harder as the heap nears it
// instead, so the guard sees the real figure.  Like limitProcsToCgroup, this does for memory what newer Go
// versions do not do by themselves: the limit is taken from the cgroup (a container or a unit with MemoryMax=)
// or, without one, from the machine's memory.
//
// It is a soft limit: the collector never uses more than half of the CPU to keep to it, so a heap that really is
// bigger than the limit slows down but is not stopped.  A GOMEMLIMIT set in the environment is left alone.

const (
	memLimitShare = 0.75     // of the cgroup limit or the machine's memory
	memLimitMin   = 64 << 20 // below this a limit would only make the collector spin
)

// softMemLimit is the soft limit for a machine with total bytes of memory and a cgroup limit (0 for none): the
// smaller of the two, scaled down.  It returns 0 when there is nothing sensible to set.
func softMemLimit(total, cgroupLimit uint64) int64 {
	m := total
	if cgroupLimit > 0 && (m == 0 || cgroupLimit < m) {
		m = cgroupLimit
	}
	if m == 0 || m > math.MaxInt64/2 {
		return 0
	}
	l := int64(float64(m) * memLimitShare)
	if l < memLimitMin {
		return 0
	}
	return l
}

// limitMemoryToCgroup applies the soft limit unless GOMEMLIMIT was set by hand; it returns the limit it set, or 0.
func limitMemoryToCgroup() int64 {
	if os.Getenv("GOMEMLIMIT") != "" {
		return 0
	}
	var total uint64
	if b, err := os.ReadFile(procMeminfo); err == nil {
		total, _, _, _ = parseMeminfo(string(b))
	}
	limit, _, _ := cgroupUsage()
	l := softMemLimit(total, limit)
	if l > 0 {
		debug.SetMemoryLimit(l)
	}
	return l
}
