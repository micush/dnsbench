package main

import "math"

// hist is a fixed-size log-linear latency histogram. Values are recorded in
// microseconds; each power of two is split into 64 linear buckets, which keeps
// the relative error under ~1% for any run length with O(1) memory. Min, max
// and mean are tracked exactly.
const (
	histShift   = 46 // float64 bits kept: 11 exponent + 6 mantissa
	histBuckets = 42 * 64
)

var histBase = int(math.Float64bits(1.0) >> histShift)

type hist struct {
	counts [histBuckets]uint64
	n      uint64
	sumUs  float64
	minUs  float64
	maxUs  float64
}

func (h *hist) recordNs(ns int64) {
	us := float64(ns) / 1000.0
	if us < 1 {
		us = 1
	}
	idx := int(math.Float64bits(us)>>histShift) - histBase
	if idx >= histBuckets {
		idx = histBuckets - 1
	}
	h.counts[idx]++
	if h.n == 0 || us < h.minUs {
		h.minUs = us
	}
	if us > h.maxUs {
		h.maxUs = us
	}
	h.n++
	h.sumUs += us
}

func (h *hist) merge(o *hist) {
	if o.n == 0 {
		return
	}
	for i, c := range o.counts {
		if c != 0 {
			h.counts[i] += c
		}
	}
	if h.n == 0 || o.minUs < h.minUs {
		h.minUs = o.minUs
	}
	if o.maxUs > h.maxUs {
		h.maxUs = o.maxUs
	}
	h.n += o.n
	h.sumUs += o.sumUs
}

func (h *hist) reset() { *h = hist{} }

// bucketMidUs is the midpoint of bucket idx in microseconds.
func bucketMidUs(idx int) float64 {
	lo := math.Float64frombits(uint64(idx+histBase) << histShift)
	hi := math.Float64frombits(uint64(idx+1+histBase) << histShift)
	return (lo + hi) / 2
}

// percentileMs returns the p-th percentile (0-100) in milliseconds.
func (h *hist) percentileMs(p float64) float64 {
	if h.n == 0 {
		return 0
	}
	rank := uint64(math.Round(p/100*float64(h.n-1))) + 1
	var cum uint64
	for i, c := range h.counts {
		cum += c
		if cum >= rank {
			v := bucketMidUs(i)
			if v < h.minUs {
				v = h.minUs
			}
			if v > h.maxUs {
				v = h.maxUs
			}
			return v / 1000
		}
	}
	return h.maxUs / 1000
}

func (h *hist) meanMs() float64 {
	if h.n == 0 {
		return 0
	}
	return h.sumUs / float64(h.n) / 1000
}
func (h *hist) minMs() float64 { return h.minUs / 1000 }
func (h *hist) maxMs() float64 { return h.maxUs / 1000 }
