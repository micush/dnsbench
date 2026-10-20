package main

import "testing"

func TestSoftMemLimit(t *testing.T) {
	const gb = 1 << 30
	for _, c := range []struct {
		name         string
		total, limit uint64
		want         int64
	}{
		{"machine only", 8 * gb, 0, 6 * gb},
		{"cgroup smaller", 8 * gb, 2 * gb, 3 * gb / 2},
		{"cgroup larger than the machine", 2 * gb, 8 * gb, 3 * gb / 2},
		{"cgroup only", 0, 4 * gb, 3 * gb},
		{"nothing known", 0, 0, 0},
		{"too small to be useful", 64 << 20, 0, 0},
		{"absurd limit", 0, 1 << 62, 0},
	} {
		if got := softMemLimit(c.total, c.limit); got != c.want {
			t.Errorf("%s: softMemLimit(%d, %d) = %d, want %d", c.name, c.total, c.limit, got, c.want)
		}
	}
}
