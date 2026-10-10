package main

import (
	"encoding/json"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

func newTestEngine() *Engine {
	cfg := defaultGroup()
	cfg.Interface = "dgwtest0" // does not exist: ip(8) calls fail quietly
	e := NewEngine(cfg, afIPv4, func() DNSConfig { return defaultDNS() }, func() *Pool { return nil })
	e.running = true
	e.myIP = "10.0.0.5"
	return e
}

func helloFrom(t *testing.T, e *Engine, ip string, pri, afn int) []byte {
	t.Helper()
	g := e.cfg
	g.Priority = pri
	b, err := buildPacket(&g, pktHello, afIPv4, afn, 0, ip)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A node that hears an existing AGC while in SPEAK joins as an AFN, even if
// its own priority is higher (the case that once left it stuck in SPEAK).
func TestJoinExistingAGCEvenWithHigherPriority(t *testing.T) {
	for _, pri := range []int{50, 200} {
		e := newTestEngine()
		e.cfg.Priority = 100
		e.state = stateSpeak
		pkt := helloFrom(t, e, "10.0.0.9", pri, 1)
		e.mu.Lock()
		e.onPacketLocked(pkt, nil)
		st, afn, agc := e.state, e.afnID, e.agcIP
		e.mu.Unlock()
		if st != stateForward || afn < 2 || agc != "10.0.0.9" {
			t.Fatalf("peer pri %d: state=%v afn=%d agc=%q", pri, st, afn, agc)
		}
	}
}

func TestLoneNodeWinsElection(t *testing.T) {
	e := newTestEngine()
	e.state = stateSpeak
	e.mu.Lock()
	e.runElectionLocked()
	st, afn := e.state, e.afnID
	e.mu.Unlock()
	if st != stateActive || afn != 1 {
		t.Fatalf("state=%v afn=%d", st, afn)
	}
	e.Stop()
}

// helloFlags is helloFrom with explicit header flags (bit0 preempt, bit2 leaving).
func helloFlags(t *testing.T, e *Engine, ip string, pri, afn int, preempt bool, flags uint8) []byte {
	t.Helper()
	g := e.cfg
	g.Priority, g.Preempt = pri, preempt
	b, err := buildPacket(&g, pktHello, afIPv4, afn, flags, ip)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newController(t *testing.T) *Engine {
	e := newTestEngine()
	e.cfg.Priority = 100
	e.state = stateSpeak
	e.mu.Lock()
	e.runElectionLocked() // becomes AGC
	e.mu.Unlock()
	if e.state != stateActive {
		t.Fatal("not the controller")
	}
	return e
}

// A controller yields to a higher-ranked peer that is itself a controller (two at
// once must settle on one) or that asked to preempt — but not to a node that has
// merely restarted and is still joining: pulling the role straight back on its
// first hello left the group with no controller and moved the virtual MAC twice.
func TestAGCYieldsOnlyToControllerOrPreemptingPeer(t *testing.T) {
	cases := []struct {
		name    string
		afn     int
		preempt bool
		yields  bool
	}{
		{"joining node, higher priority", 0, false, false},
		{"forwarder, higher priority", 7, false, false},
		{"higher priority with preempt", 7, true, true},
		{"another controller", 1, false, true},
	}
	// a controller that took over keeps its old slot (here 3): the flag says it is one
	{
		e := newController(t)
		e.mu.Lock()
		e.onPacketLocked(helloFlags(t, e, "10.0.0.9", 200, 3, false, flagController), nil)
		st := e.state
		e.mu.Unlock()
		if st == stateActive {
			t.Error("did not yield to a controller holding slot 3")
		}
		e.Stop()
	}
	for _, c := range cases {
		e := newController(t)
		e.mu.Lock()
		e.onPacketLocked(helloFlags(t, e, "10.0.0.9", 200, c.afn, c.preempt, 0), nil)
		st := e.state
		e.mu.Unlock()
		if (st != stateActive) != c.yields {
			t.Errorf("%s: state=%v, yields want %v", c.name, st, c.yields)
		}
		e.Stop()
	}
	// same priority, greater IP as text (the restart case in the field)
	e := newController(t)
	e.mu.Lock()
	e.onPacketLocked(helloFlags(t, e, "10.0.0.9", 100, 0, false, 0), nil)
	st := e.state
	e.mu.Unlock()
	if st != stateActive {
		t.Fatalf("a restarting node with a greater IP took the role: %v", st)
	}
	e.Stop()
}

func TestPickAFNMethods(t *testing.T) {
	e := newTestEngine()
	e.afnID = 1
	e.peers["10.0.0.6"] = &Peer{IP: "10.0.0.6", AfnID: 2, Weight: 300, LastSeen: time.Now()}
	e.peers["10.0.0.7"] = &Peer{IP: "10.0.0.7", AfnID: 3, Weight: 100, LastSeen: time.Now()}

	e.cfg.LBMethod = lbRoundRobin
	seen := map[int]int{}
	for i := 0; i < 9; i++ {
		seen[e.PickAFN("10.1.1.1")]++
	}
	if seen[1] != 3 || seen[2] != 3 || seen[3] != 3 {
		t.Fatalf("roundrobin uneven: %v", seen)
	}

	e.cfg.LBMethod = lbHostPinned
	a, b := e.PickAFN("10.1.1.4"), e.PickAFN("10.1.1.4")
	if a != b {
		t.Fatal("hostpinned not sticky")
	}

	e.cfg.LBMethod = lbWeighted
	e.cfg.Weight = 100
	seen = map[int]int{}
	for i := 0; i < 500; i++ {
		seen[e.PickAFN("10.1.1.1")]++
	}
	if seen[2] != 300 || seen[1] != 100 || seen[3] != 100 {
		t.Fatalf("weighted distribution wrong: %v", seen)
	}

	e.cfg.LBMethod = lbFailover
	if e.PickAFN("x") != 1 {
		t.Fatal("failover primary should be lowest slot")
	}
	delete(e.peers, "10.0.0.6")
	e.afnID = 2
	if got := e.PickAFN("x"); got != 2 {
		t.Fatalf("failover after loss = %d", got)
	}
}

func TestNAChecksumAgainstKnownVector(t *testing.T) {
	// Checksum of a frame must verify to zero when recomputed with the field included.
	vip := mustAddr("2001:db8::1").As16()
	dst := mustAddr("fe80::2").As16()
	f := buildNA([6]byte{1, 2, 3, 4, 5, 6}, [6]byte{9, 9, 9, 9, 9, 9}, vmacBytes(1, 2), vip, dst, 0x60000000)
	body := f[14+40:]
	if icmp6Checksum(vip, dst, body) != 0 {
		t.Fatal("NA checksum does not verify")
	}
}

func TestConfigRoundTripAndValidation(t *testing.T) {
	dc := newDaemonConfig()
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	dc.DNS.Servers = []string{"127.0.0.1"}
	if dc.Validate() == nil {
		t.Fatal("a server without probe queries should be rejected")
	}
	dc.DNS.Queries = []DNSQuery{{Name: "example.com", Type: "A"}}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	var back DaemonConfig
	b, _ := jsonMarshal(dc)
	if err := back.UnmarshalJSON(b); err != nil {
		t.Fatal(err)
	}
	if back.Groups[0].LBMethod != lbRoundRobin || !back.Groups[0].DNSProxy || back.DNS.Servers[0] != "127.0.0.1:53" {
		t.Fatalf("round trip lost data: %+v", back)
	}
	// "dns_proxy" is no longer a switch: an old file that turned it off still serves DNS
	off := []byte(`{"groups":[{"vip4":"10.9.9.1/24","dns_proxy":false}]}`)
	var offDC DaemonConfig
	if err := offDC.UnmarshalJSON(off); err != nil || offDC.Validate() != nil || !offDC.Groups[0].DNSProxy {
		t.Fatalf("dns_proxy:false must be accepted and forced on: %v %+v", err, offDC.Groups)
	}
	old := []byte(`{"groups":[{"vip":"10.9.9.1/24"}]}`)
	var oldDC DaemonConfig
	if err := oldDC.UnmarshalJSON(old); err != nil || oldDC.Groups[0].VIP4 != "10.9.9.1/24" {
		t.Fatalf("legacy vip key: %v %+v", err, oldDC.Groups)
	}
}

func jsonMarshal(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }

// When the controller says it is leaving, the best of the others becomes the
// controller at once — not a hold time later — and takes over the leaver's
// virtual MAC, which clients have cached for the address.
func TestControllerResignHandsOverAndTakesItsMAC(t *testing.T) {
	var took []int
	old := addVmacFn
	addVmacFn = func(_ string, _, slot int) bool { took = append(took, slot); return true }
	defer func() { addVmacFn = old }()

	e := newTestEngine()
	e.cfg.Priority = 100
	e.state = stateSpeak
	// an existing controller (slot 1) is heard: this node joins as a forwarder
	e.mu.Lock()
	e.onPacketLocked(helloFrom(t, e, "10.0.0.9", 100, 1), nil)
	st, slot := e.state, e.afnID
	e.mu.Unlock()
	if st != stateForward || slot < 2 {
		t.Fatalf("did not join as a forwarder: %v slot=%d", st, slot)
	}
	// the controller resigns
	g := e.cfg
	resign, err := buildPacket(&g, pktResign, afIPv4, 1, 0, "10.0.0.9")
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.onPacketLocked(resign, nil)
	st, agc := e.state, e.agcIP
	e.mu.Unlock()
	if st != stateActive || agc != e.myIP {
		t.Fatalf("not the controller after the resign: state=%v agc=%q", st, agc)
	}
	if len(took) != 1 || took[0] != 1 {
		t.Fatalf("did not take over the old controller's slot 1: %v", took)
	}
	e.Stop()
}

// A forwarder's hello gone quiet (the old path) also hands the controller's MAC over.
func TestControllerLossByTimeoutTakesItsMAC(t *testing.T) {
	var took []int
	old := addVmacFn
	addVmacFn = func(_ string, _, slot int) bool { took = append(took, slot); return true }
	defer func() { addVmacFn = old }()

	e := newTestEngine()
	e.state = stateSpeak
	e.mu.Lock()
	e.onPacketLocked(helloFrom(t, e, "10.0.0.9", 100, 1), nil)
	e.peers["10.0.0.9"].LastSeen = time.Now().Add(-time.Minute)
	e.reapPeersLocked()
	st := e.state
	e.mu.Unlock()
	if st != stateActive || len(took) != 1 || took[0] != 1 {
		t.Fatalf("state=%v took=%v", st, took)
	}
	e.Stop()
}

// A stopping controller tells the group and goes quiet; waitUntilSafe holds a
// finished update back until it is safe to go down.
func TestWaitUntilSafe(t *testing.T) {
	n := 0
	var reasons []string
	waitUntilSafe(func() (bool, string) {
		n++
		return n >= 3, "the other member is still recovering"
	}, false, time.Millisecond, func(s string) { reasons = append(reasons, s) })
	if n != 3 {
		t.Fatalf("asked %d times", n)
	}
	if len(reasons) != 3 || reasons[0] == "" || reasons[1] == "" || reasons[2] != "" {
		t.Fatalf("waiting reasons: %q", reasons)
	}
	called := false
	waitUntilSafe(func() (bool, string) { called = true; return false, "x" }, true, time.Millisecond, func(string) {})
	if called {
		t.Fatal("force must not wait")
	}
}

type capConn struct {
	net.PacketConn
	mu   sync.Mutex
	sent [][]byte
}

func (c *capConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	c.mu.Lock()
	c.sent = append(c.sent, append([]byte(nil), b...))
	c.mu.Unlock()
	return len(b), nil
}
func (c *capConn) Close() error { return nil }

func TestStoppingControllerResignsAndGoesQuiet(t *testing.T) {
	old := leaveGrace
	leaveGrace = 5 * time.Millisecond
	defer func() { leaveGrace = old }()

	e := newTestEngine()
	e.cfg.Neighbors = []string{"10.0.0.9"} // unicast mode
	cc := &capConn{}
	e.conn = cc
	e.state = stateSpeak
	e.mu.Lock()
	e.runElectionLocked() // lone node: becomes the controller
	e.mu.Unlock()
	e.Stop()
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if len(cc.sent) != 2 { // the resign, said twice (multicast is not reliable)
		t.Fatalf("expected the resign twice and nothing else, got %d packets", len(cc.sent))
	}
	pkt, err := parsePacket(cc.sent[0], e.cfg.keyBytes())
	if err != nil || pkt.Type != pktResign {
		t.Fatalf("not a resign: %+v %v", pkt, err)
	}
	// and a forwarder that stops says nothing (the controller would read it as a request to step down)
	f := newTestEngine()
	f.cfg.Neighbors = []string{"10.0.0.9"}
	fc := &capConn{}
	f.conn = fc
	f.state = stateForward
	f.Stop()
	if len(fc.sent) != 0 {
		t.Fatalf("a stopping forwarder sent %d packet(s)", len(fc.sent))
	}
}

// A forwarder that says it is leaving has its slot covered by the controller at
// once, not a hold time later.
func TestLeavingForwarderIsCoveredAtOnce(t *testing.T) {
	var took []int
	old := addVmacFn
	addVmacFn = func(_ string, _, slot int) bool { took = append(took, slot); return true }
	defer func() { addVmacFn = old }()

	var announced []int
	oa := announceVmacFn
	announceVmacFn = func(_ string, _, slot int, _ string) { announced = append(announced, slot) }
	defer func() { announceVmacFn = oa }()

	e := newController(t)
	e.mu.Lock()
	e.onPacketLocked(helloFlags(t, e, "10.0.0.9", 100, 2, false, 0), nil) // a forwarder in slot 2
	if len(took) != 0 || e.peers["10.0.0.9"] == nil {
		e.mu.Unlock()
		t.Fatalf("setup: took=%v", took)
	}
	e.onPacketLocked(helloFlags(t, e, "10.0.0.9", 100, 2, false, flagLeaving), nil)
	gone := e.peers["10.0.0.9"] == nil
	e.mu.Unlock()
	if !gone || len(took) != 1 || took[0] != 2 {
		t.Fatalf("peer gone=%v, slots taken over=%v", gone, took)
	}
	// the network must learn the MAC moved, or the switch keeps sending it to the old port
	if len(announced) != 1 || announced[0] != 2 {
		t.Fatalf("virtual MAC announced for slots %v", announced)
	}
	e.Stop()
}

// A stopping forwarder announces it (twice); one that never got a slot says nothing.
func TestStoppingForwarderAnnouncesLeave(t *testing.T) {
	old := leaveGrace
	leaveGrace = 5 * time.Millisecond
	defer func() { leaveGrace = old }()

	e := newTestEngine()
	e.cfg.Neighbors = []string{"10.0.0.9"}
	cc := &capConn{}
	e.conn = cc
	e.state, e.afnID = stateForward, 2
	e.Stop()
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if len(cc.sent) != 2 {
		t.Fatalf("expected two leave hellos, got %d", len(cc.sent))
	}
	pkt, err := parsePacket(cc.sent[0], e.cfg.keyBytes())
	if err != nil || pkt.Type != pktHello || pkt.Flags&flagLeaving == 0 || pkt.AfnID != 2 {
		t.Fatalf("not a leave hello: %+v %v", pkt, err)
	}
	f := newTestEngine()
	f.cfg.Neighbors = []string{"10.0.0.9"}
	fc := &capConn{}
	f.conn = fc
	f.state = stateStandby
	f.Stop()
	if len(fc.sent) != 0 {
		t.Fatalf("a standby node announced %d packet(s)", len(fc.sent))
	}
}

// A node that is starting up and hears a controller that took over from an old one
// (so holds slot 2, not 1) joins as a forwarder instead of winning the election on
// the IP tie-break — the flap that doubled the outage of every restart.
func TestJoinerRecognisesTakenOverController(t *testing.T) {
	e := newTestEngine()
	e.cfg.Priority = 100
	e.state = stateSpeak
	e.mu.Lock()
	e.onPacketLocked(helloFlags(t, e, "10.0.0.2", 100, 2, false, flagController), nil) // lower IP, slot 2, controller
	st, agc := e.state, e.agcIP
	e.mu.Unlock()
	if st != stateForward || agc != "10.0.0.2" {
		t.Fatalf("state=%v agc=%q: a node with the greater IP took the role from a sitting controller", st, agc)
	}
	e.Stop()
}

// A node that has announced it is leaving does not react to what it hears next.
func TestLeavingNodeIgnoresPackets(t *testing.T) {
	e := newController(t)
	e.mu.Lock()
	e.leaving = true
	e.onPacketLocked(helloFlags(t, e, "10.0.0.9", 200, 3, false, flagController), nil) // would make it yield
	st, peers := e.state, len(e.peers)
	e.mu.Unlock()
	if st != stateActive || peers != 0 {
		t.Fatalf("a leaving controller reacted: state=%v peers=%d", st, peers)
	}
	e.mu.Lock()
	e.leaving = false
	e.mu.Unlock()
	e.Stop()
}

// A peer's row shows the state its hellos imply, not "active" for everyone.
func TestSnapshotShowsPeerStates(t *testing.T) {
	e := newTestEngine()
	e.cfg.HoldMS = 999
	now := time.Now()
	e.mu.Lock()
	e.peers = map[string]*Peer{
		"10.0.0.2": {IP: "10.0.0.2", AfnID: 2, Controller: true, LastSeen: now},
		"10.0.0.3": {IP: "10.0.0.3", AfnID: 3, LastSeen: now},
		"10.0.0.4": {IP: "10.0.0.4", AfnID: 0, LastSeen: now},
		"10.0.0.5": {IP: "10.0.0.5", AfnID: 4, LastSeen: now.Add(-time.Minute)},
		"10.0.0.6": {IP: "10.0.0.6", AfnID: 1, Controller: true, LastSeen: now.Add(-time.Minute)},
	}
	e.mu.Unlock()
	want := map[string]string{"10.0.0.2": "active", "10.0.0.3": "forward", "10.0.0.4": "standby", "10.0.0.5": "expired", "10.0.0.6": "expired"}
	for _, r := range e.snapshot() {
		if r.Local {
			continue
		}
		if r.State != want[r.PeerIP] {
			t.Errorf("%s: state %q, want %q", r.PeerIP, r.State, want[r.PeerIP])
		}
		delete(want, r.PeerIP)
	}
	if len(want) != 0 {
		t.Fatalf("rows missing: %v", want)
	}
}

// The same, from real hellos: the controller flag makes a peer "active", a forwarder slot makes it "forward".
func TestSnapshotPeerStatesFromHellos(t *testing.T) {
	e := newTestEngine()
	e.state = stateStandby
	send := func(ip string, afn int, flags uint8) {
		g := e.cfg
		g.Priority = 100
		b, err := buildPacket(&g, pktHello, afIPv4, afn, flags, ip)
		if err != nil {
			t.Fatal(err)
		}
		e.mu.Lock()
		e.onPacketLocked(b, nil)
		e.mu.Unlock()
	}
	send("10.0.0.9", 2, flagController) // an AGC that holds slot 2
	send("10.0.0.7", 3, 0)              // a forwarder
	got := map[string]string{}
	for _, r := range e.snapshot() {
		got[r.PeerIP] = r.State
	}
	if got["10.0.0.9"] != "active" || got["10.0.0.7"] != "forward" {
		t.Fatalf("states from hellos: %v", got)
	}
}
