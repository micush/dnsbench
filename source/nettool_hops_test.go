package main

import (
	"testing"
	"time"
)

// The traceroute keeps its table as data and fills in each hop's reverse name in the background.
func TestTraceHopsCarryNames(t *testing.T) {
	j := &NetJob{Kind: "trace", State: "running"}
	m := &mtrRaw{j: j, hops: map[int]*mtrHop{}, target: "127.0.0.1"}
	if _, err := m.Write([]byte("x 0 0\nh 0 127.0.0.1\np 0 1500 0\n")); err != nil {
		t.Fatal(err)
	}
	netJobs.mu.Lock()
	if len(j.Hops) != 1 || j.Hops[0].Addr != "127.0.0.1" || j.Hops[0].Sent != 1 {
		netJobs.mu.Unlock()
		t.Fatalf("hops: %+v", j.Hops)
	}
	netJobs.mu.Unlock()
	for i := 0; i < 50; i++ {
		netJobs.mu.Lock()
		n := j.Hops[0].Name
		netJobs.mu.Unlock()
		if n != "" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Skip("this machine has no reverse name for 127.0.0.1")
}
