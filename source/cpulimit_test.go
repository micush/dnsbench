package main

import "testing"

func TestParseCPUMax(t *testing.T) {
	for _, c := range []struct {
		in string
		v  float64
		ok bool
	}{
		{"max 100000\n", 0, false}, {"200000 100000\n", 2, true}, {"150000 100000", 1.5, true},
		{"50000", 0.5, true}, {"", 0, false}, {"x y", 0, false}, {"0 100000", 0, false},
	} {
		v, ok := parseCPUMax(c.in)
		if ok != c.ok || v != c.v {
			t.Errorf("%q: %v %v, want %v %v", c.in, v, ok, c.v, c.ok)
		}
	}
}

func TestCgroupCPUsV1AndV2(t *testing.T) {
	files := map[string]string{}
	read := func(p string) (string, bool) { s, ok := files[p]; return s, ok }
	if _, ok := cgroupCPUs(read); ok {
		t.Fatal("no files, no quota")
	}
	files["/sys/fs/cgroup/cpu/cpu.cfs_quota_us"] = "300000\n"
	files["/sys/fs/cgroup/cpu/cpu.cfs_period_us"] = "100000\n"
	if v, ok := cgroupCPUs(read); !ok || v != 3 {
		t.Fatalf("v1: %v %v", v, ok)
	}
	files["/sys/fs/cgroup/cpu/cpu.cfs_quota_us"] = "-1\n"
	if _, ok := cgroupCPUs(read); ok {
		t.Fatal("v1 unlimited")
	}
	files["/sys/fs/cgroup/cpu.max"] = "100000 100000\n" // v2 wins
	if v, ok := cgroupCPUs(read); !ok || v != 1 {
		t.Fatalf("v2: %v %v", v, ok)
	}
}

func TestProcsFor(t *testing.T) {
	if procsFor(1.5, 32) != 2 || procsFor(0.2, 32) != 1 || procsFor(8, 4) != 4 || procsFor(2, 2) != 2 {
		t.Fatal("procsFor")
	}
}
