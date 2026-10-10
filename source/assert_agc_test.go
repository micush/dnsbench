package main

import (
	"net"
	"sync"
	"testing"
)

// mesh wires engines together in memory: what one sends (to the neighbours of unicast mode, or to a single node) is
// queued, and pump hands it to the engine it is addressed to.
type mesh struct {
	mu      sync.Mutex
	queue   []meshPkt
	engines map[string]*Engine
	logTo   *cmdLog // when set, what is sent is written there too, in order with the commands
}
type meshPkt struct {
	to   string
	data []byte
}
type meshConn struct {
	net.PacketConn
	m *mesh
}

func (c *meshConn) WriteTo(b []byte, a net.Addr) (int, error) {
	if ua, ok := a.(*net.UDPAddr); ok {
		c.m.mu.Lock()
		c.m.queue = append(c.m.queue, meshPkt{ua.IP.String(), append([]byte(nil), b...)})
		lg := c.m.logTo
		c.m.mu.Unlock()
		if lg != nil {
			if len(b) > 0 {
				name := map[PktType]string{pktHello: "hello", pktResign: "resign", pktCoup: "coup"}[PktType(b[3])]
				lg.add("send " + name + " to " + ua.IP.String())
			}
		}
	}
	return len(b), nil
}
func (c *meshConn) Close() error { return nil }

func newMesh(t *testing.T, ips ...string) (*mesh, map[string]*Engine) {
	t.Helper()
	// the macvlans: nothing is really created (as in production a macvlan that exists is left alone), but what is asked
	// shows in the command log of a test that has one
	oldV, oldC := addVmacFn, claimVmacFn
	vm := func(_ string, group, slot int) bool { runCmd("macvlan-for", vmacName(group, slot)); return true }
	addVmacFn, claimVmacFn = vm, vm
	t.Cleanup(func() { addVmacFn, claimVmacFn = oldV, oldC })
	m := &mesh{engines: map[string]*Engine{}}
	for _, ip := range ips {
		cfg := defaultGroup()
		cfg.Interface = "dgwtest0"
		cfg.Neighbors = ips
		cfg.HelloMS, cfg.HoldMS = 60000, 180000 // nothing fires by itself: the test sends every hello
		e := NewEngine(cfg, afIPv4, func() DNSConfig { return defaultDNS() }, func() *Pool { return nil })
		e.running, e.myIP, e.conn = true, ip, &meshConn{m: m}
		m.engines[ip] = e
		t.Cleanup(e.Stop)
	}
	return m, m.engines
}

// pump delivers queued packets until none are left.
func (m *mesh) pump(t *testing.T) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		m.mu.Lock()
		q := m.queue
		m.queue = nil
		m.mu.Unlock()
		if len(q) == 0 {
			return
		}
		for _, p := range q {
			if e := m.engines[p.to]; e != nil {
				e.mu.Lock()
				e.onPacketLocked(p.data, nil)
				e.mu.Unlock()
			}
		}
	}
	t.Fatal("packets never stopped")
}

// round: every engine says hello once, in the order given, and what that causes is delivered.
func (m *mesh) round(t *testing.T, order ...string) {
	t.Helper()
	for _, ip := range order {
		e := m.engines[ip]
		e.mu.Lock()
		gen := e.helloGen
		e.mu.Unlock()
		e.sendHello(gen)
		m.pump(t)
	}
}

func steadyThree(t *testing.T) (*mesh, *Engine, *Engine, *Engine) {
	m, es := newMesh(t, "10.0.0.204", "10.0.0.203", "10.0.0.202")
	a, b, c := es["10.0.0.204"], es["10.0.0.203"], es["10.0.0.202"]
	a.mu.Lock()
	a.state = stateSpeak
	a.runElectionLocked() // alone: the controller
	a.mu.Unlock()
	for _, e := range []*Engine{b, c} {
		e.mu.Lock()
		e.state = stateSpeak
		e.mu.Unlock()
	}
	for i := 0; i < 3; i++ {
		m.round(t, "10.0.0.204", "10.0.0.203", "10.0.0.202")
	}
	for _, e := range []*Engine{a, b, c} {
		e.mu.Lock()
		st, agc := e.state, e.agcIP
		e.mu.Unlock()
		want := stateForward
		if e == a {
			want = stateActive
		}
		if st != want || agc != "10.0.0.204" {
			t.Fatalf("%s did not settle: state %v, controller %q", e.myIP, st, agc)
		}
	}
	return m, a, b, c
}

// "Make this node the gateway controller" on a node that ranks lower than the sitting controller (equal priority, a
// smaller address).  It used to do nothing: the node lost its own election, and the controller stepped down and then won
// its election again.
func TestAssertAGCFromALowerRankedNode(t *testing.T) {
	m, a, b, c := steadyThree(t)
	c.mu.Lock()
	msg := c.assertAGCLocked()
	c.mu.Unlock()
	t.Log(msg)
	m.pump(t)
	check := func(when string, who ...*Engine) {
		t.Helper()
		for _, e := range who {
			e.mu.Lock()
			st, agc := e.state, e.agcIP
			e.mu.Unlock()
			want := stateForward
			if e == c {
				want = stateActive
			}
			if st != want || agc != "10.0.0.202" {
				t.Fatalf("%s, %s: state %v, controller %q (want %v, 10.0.0.202)", when, e.myIP, st, agc, want)
			}
		}
	}
	// at once the two that took part agree; the third learns of the new controller from its first hello
	check("right after", a, c)
	for i := 0; i < 6; i++ {
		m.round(t, "10.0.0.204", "10.0.0.203", "10.0.0.202")
	}
	check("after six rounds of hellos", a, b, c)
}

// The request is lost (the first RESIGN never arrives): the node keeps asking while the controller still says it is one.
func TestAssertAGCSurvivesALostResign(t *testing.T) {
	m, a, _, c := steadyThree(t)
	c.mu.Lock()
	c.assertAGCLocked()
	c.mu.Unlock()
	m.mu.Lock()
	m.queue = nil // the RESIGN and everything else sent so far is lost
	m.mu.Unlock()
	for i := 0; i < 8; i++ {
		m.round(t, "10.0.0.204", "10.0.0.203", "10.0.0.202")
	}
	for _, e := range []*Engine{a, c} {
		e.mu.Lock()
		st, agc := e.state, e.agcIP
		e.mu.Unlock()
		want := stateForward
		if e == c {
			want = stateActive
		}
		if st != want || agc != "10.0.0.202" {
			t.Fatalf("%s: state %v, controller %q", e.myIP, st, agc)
		}
	}
}

// A node that is already the controller is left alone, and the controller asking again changes nothing.
func TestAssertAGCOnTheControllerIsANoOp(t *testing.T) {
	m, a, _, _ := steadyThree(t)
	a.mu.Lock()
	msg := a.assertAGCLocked()
	a.mu.Unlock()
	m.pump(t)
	m.round(t, "10.0.0.204", "10.0.0.203", "10.0.0.202")
	a.mu.Lock()
	st := a.state
	a.mu.Unlock()
	if st != stateActive {
		t.Fatalf("the controller stepped down: %v (%s)", st, msg)
	}
}
