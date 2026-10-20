package main

import (
	"testing"
	"time"
)

// Stop must come back even when a query never finishes.
func TestFrontendStopDoesNotWaitForeverForAQuery(t *testing.T) {
	old := dnsStopWait
	dnsStopWait = 200 * time.Millisecond
	defer func() { dnsStopWait = old }()
	fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return nil })
	if err := fe.Start(); err != nil {
		t.Skip("cannot listen here:", err)
	}
	fe.wg.Add(1) // a query that is stuck
	defer fe.wg.Done()
	t0 := time.Now()
	fe.Stop()
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("Stop took %v", d)
	}
	if fe.Listening() {
		t.Fatal("still listening")
	}
}
