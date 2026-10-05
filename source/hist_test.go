package main

import (
	"math"
	"math/rand"
	"sort"
	"testing"
)

func TestHistPercentilesCloseToExact(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var h hist
	var vals []float64
	for i := 0; i < 200000; i++ {
		ms := math.Exp(rng.NormFloat64()*0.8) * 0.5 // log-normal around 0.5 ms
		vals = append(vals, ms)
		h.recordNs(int64(ms * 1e6))
	}
	sort.Float64s(vals)
	for _, p := range []float64{50, 95, 99} {
		exact := vals[int(math.Round(p/100*float64(len(vals)-1)))]
		got := h.percentileMs(p)
		if math.Abs(got-exact)/exact > 0.02 {
			t.Errorf("p%.0f = %.4f, exact %.4f", p, got, exact)
		}
	}
	if math.Abs(h.minMs()-vals[0])/vals[0] > 1e-3 || math.Abs(h.maxMs()-vals[len(vals)-1])/vals[len(vals)-1] > 1e-3 {
		t.Errorf("min/max off: %v %v", h.minMs(), h.maxMs())
	}
}

func TestHistMergeAndEmpty(t *testing.T) {
	var a, b, empty hist
	if empty.percentileMs(99) != 0 || empty.meanMs() != 0 {
		t.Fatal("empty histogram should report zeros")
	}
	for i := 1; i <= 100; i++ {
		a.recordNs(int64(i) * 1000)
		b.recordNs(int64(i+100) * 1000)
	}
	a.merge(&b)
	a.merge(&empty)
	if a.n != 200 || a.minMs() != 0.001 || a.maxMs() != 0.2 {
		t.Fatalf("merge: n=%d min=%v max=%v", a.n, a.minMs(), a.maxMs())
	}
	if m := a.meanMs(); math.Abs(m-0.1005) > 1e-9 {
		t.Fatalf("mean %v", m)
	}
}

func TestHistClampsExtremes(t *testing.T) {
	var h hist
	h.recordNs(0)
	h.recordNs(1 << 62)
	if h.n != 2 {
		t.Fatal("lost samples")
	}
}
