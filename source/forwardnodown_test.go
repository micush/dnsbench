package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Only the probes take a server out of service; queries that time out on it do not.
func TestForwardFailuresNeverMarkAServerDown(t *testing.T) {
	odd, good := newFakeDNS(t), newFakeDNS(t)
	odd.only.Store(true)
	good.delay.Store(int64(5 * time.Millisecond))
	cfg := testDNSCfg(odd.addr, good.addr)
	cfg.FailThreshold = 1
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	for i := 0; i < 8; i++ {
		q, _ := buildQuery(uint16(i+1), fmt.Sprintf("n%d.external.example", i), "A")
		p.avoid = nameAvoid{}
		if _, err := p.Forward(context.Background(), q, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range p.servers {
		s.mu.Lock()
		h, fc := s.healthy, s.failCount
		s.mu.Unlock()
		if s.Addr == odd.addr && (!h || fc == 0) {
			t.Fatalf("healthy=%v failures counted=%d: timeouts must be counted but not mark the server down", h, fc)
		}
	}
	// the probe is what takes a dead server down
	odd.only.Store(false)
	odd.mode.Store(2)
	p.ProbeNow(context.Background())
	for _, s := range p.servers {
		if s.Addr == odd.addr {
			s.mu.Lock()
			h := s.healthy
			s.mu.Unlock()
			if h {
				t.Fatal("a server that fails its probe stayed in service")
			}
		}
	}
}
