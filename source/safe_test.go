package main

import "testing"

func TestRecoverPanicHolds(t *testing.T) {
	before := panicCount.Load()
	func() {
		defer recoverPanic("a test")
		panic("boom")
	}()
	if panicCount.Load() != before+1 {
		t.Fatal("the panic was not counted")
	}
	func() { // no panic: nothing is counted
		defer recoverPanic("a test")
	}()
	if panicCount.Load() != before+1 {
		t.Fatal("a call without a panic was counted")
	}
}

// A panic while a gateway packet is handled drops the packet; the daemon and the engine's lock survive.
func TestEngineSurvivesPanicInPacket(t *testing.T) {
	e := newTestEngine()
	e.peers = nil // a nil map: learning the new peer panics
	pkt := helloFrom(t, e, "10.0.0.9", 100, 1)
	before := panicCount.Load()
	e.handlePacket(pkt, &fakeAddr{})
	if panicCount.Load() != before+1 {
		t.Fatal("the panic did not happen or was not caught")
	}
	if !e.mu.TryLock() {
		t.Fatal("the engine lock was left held")
	}
	e.mu.Unlock()
}

type fakeAddr struct{}

func (*fakeAddr) Network() string { return "udp" }
func (*fakeAddr) String() string  { return "10.0.0.9:4729" }
