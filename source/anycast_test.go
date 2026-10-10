package main

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
)

func TestNormalizeAnycast(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.53": "203.0.113.53", "203.0.113.53/32": "203.0.113.53", " 2001:DB8::53 ": "2001:db8::53",
		"2001:db8::53/128": "2001:db8::53", "::ffff:203.0.113.53": "203.0.113.53",
	} {
		got, err := normalizeAnycast(in)
		if err != nil || got != want {
			t.Errorf("%q → %q, %v (want %q)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "x", "203.0.113.0/24", "2001:db8::/64", "127.0.0.1", "::1", "0.0.0.0", "224.0.0.1", "fe80::1", "ff02::1", "1.2.3"} {
		if got, err := normalizeAnycast(bad); err == nil {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
}

func TestAnycastConfigRules(t *testing.T) {
	g := defaultGroup()
	g.ExtraVIPs = []string{"203.0.113.53/32", "2001:db8:53::1"}
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := dc.Groups[0].ExtraVIPs; got[0] != "203.0.113.53" || got[1] != "2001:db8:53::1" {
		t.Fatalf("not normalised: %v", got)
	}
	// the same anycast address on two gateways is how an anycast service is run; equal to a shared address, or listed twice, is refused
	g2 := defaultGroup()
	g2.GroupID, g2.VIP4, g2.ExtraVIPs = 2, "10.9.0.1/24", []string{"203.0.113.53"}
	dc.Groups = append(dc.Groups, g2)
	if err := dc.Validate(); err != nil {
		t.Fatalf("the same anycast address on two gateways must be allowed: %v", err)
	}
	g2.ExtraVIPs = []string{"10.0.0.1"} // = group 1's vip4
	dc.Groups[1] = g2
	if err := dc.Validate(); err == nil {
		t.Fatal("anycast address equal to a shared address accepted")
	}
	// the list is applied in place: it never restarts the gateway (election)
	a, b := defaultGroup(), defaultGroup()
	b.ExtraVIPs = []string{"203.0.113.53"}
	if restartDiffers(&a, &b) {
		t.Fatal("changing the anycast list restarts the gateway")
	}
	if !sameList(nil, []string{}) || sameList(a.ExtraVIPs, b.ExtraVIPs) {
		t.Fatal("sameList")
	}
}

func TestAnycastSharedAcrossCluster(t *testing.T) {
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{defaultGroup()}
	plain := sharedOf(dc).hash()
	dc.Groups[0].ExtraVIPs = []string{}
	if sharedOf(dc).hash() != plain {
		t.Fatal("an empty list changes the cluster hash (rolling updates would disagree)")
	}
	dc.Groups[0].ExtraVIPs = []string{"203.0.113.53"}
	sh := sharedOf(dc)
	if sh.hash() == plain {
		t.Fatal("anycast addresses are not part of the shared config")
	}
	rep := newDaemonConfig()
	rep.Groups = []GroupConfig{defaultGroup()}
	changed, err := mergeShared(rep, sh, nil)
	if err != nil || !changed || len(rep.Groups[0].ExtraVIPs) != 1 || rep.Groups[0].ExtraVIPs[0] != "203.0.113.53" {
		t.Fatalf("replica did not receive it: %v %v %v", changed, err, rep.Groups[0].ExtraVIPs)
	}
}

func TestAnycastCanvasEdit(t *testing.T) {
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{defaultGroup()}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: 1, Anycast: "203.0.113.53, 2001:db8:53::1"}); err != nil {
		t.Fatal(err)
	}
	if len(dc.Groups[0].ExtraVIPs) != 2 {
		t.Fatalf("got %v", dc.Groups[0].ExtraVIPs)
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: 1, Anycast: "10.0.0.0/24"}); err == nil {
		t.Fatal("a subnet was accepted")
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: 1, Anycast: "-"}); err != nil || len(dc.Groups[0].ExtraVIPs) != 0 {
		t.Fatalf("not cleared: %v %v", err, dc.Groups[0].ExtraVIPs)
	}
}

// fakeLo records what the anycast manager does to lo.
type fakeLo struct {
	mu    sync.Mutex
	on    map[string]bool
	adds  int
	dels  int
	failA bool
}

func hookLo(t *testing.T) *fakeLo {
	resetAnycast()
	t.Cleanup(resetAnycast)
	f := &fakeLo{on: map[string]bool{}}
	oa, od := anycastAddFn, anycastDelFn
	anycastAddFn = func(a string) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failA {
			return false
		}
		f.on[a] = true
		f.adds++
		return true
	}
	anycastDelFn = func(a string) { f.mu.Lock(); delete(f.on, a); f.dels++; f.mu.Unlock() }
	t.Cleanup(func() { anycastAddFn, anycastDelFn = oa, od })
	return f
}

func (f *fakeLo) has(a string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.on[a] }

func freeUDPPort(t *testing.T) int {
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	return l.LocalAddr().(*net.UDPAddr).Port
}

// The address is on lo only while a DNS server answers, is renewed while it
// does, and is taken off when the pool goes down, the gateway stops or ddgw stops.
func TestAnycastFollowsDNSHealth(t *testing.T) {
	lo := hookLo(t)
	up := newFakeDNS(t)
	p := NewPool(testDNSCfg(up.addr))
	p.ProbeNow(context.Background())
	port := freeUDPPort(t)
	const addr = "127.0.0.9" // loopback so the test can bind it; the real config refuses loopback
	a := newAnycastSet(1, []string{addr}, func() *Pool { return p }, func() int { return port })
	a.step()
	if !lo.has(addr) || !a.state()[0].Up {
		t.Fatalf("not announced while healthy: %+v", a.state())
	}
	a.step()
	if lo.adds < 2 {
		t.Fatal("the address lifetime is not renewed")
	}
	// the answer comes from the anycast address itself
	q, _ := buildQuery(0x1234, "any.example", "A")
	resp, _, err := exchange(context.Background(), net.JoinHostPort(addr, itoa(port)), q, false, 2e9)
	if err != nil {
		t.Skipf("cannot query the anycast listener here: %v", err)
	}
	if h, _ := parseHeader(resp); h.id != 0x1234 || h.ancount != 1 {
		t.Fatalf("bad answer from the anycast address: %+v", h)
	}

	up.mode.Store(2) // stops answering
	p.ProbeNow(context.Background())
	a.step()
	if lo.has(addr) || a.state()[0].Up || a.state()[0].Reason == "" {
		t.Fatalf("still announced with no healthy DNS server: %+v", a.state())
	}
	adds := lo.adds
	a.step()
	if lo.adds != adds {
		t.Fatal("renewed while unhealthy")
	}

	up.mode.Store(0)
	p.ProbeNow(context.Background())
	a.step()
	if !lo.has(addr) {
		t.Fatal("not announced again after recovery")
	}

	lo.mu.Lock()
	lo.failA = true
	lo.mu.Unlock()
	a.step()
	if a.state()[0].Up || a.state()[0].Reason == "" {
		t.Fatalf("reported up although lo refused it: %+v", a.state())
	}
	lo.mu.Lock()
	lo.failA = false
	lo.mu.Unlock()
	a.step()

	a.cancel = func() {}
	a.done = make(chan struct{})
	close(a.done)
	a.stop()
	if lo.has(addr) {
		t.Fatal("still on lo after stop")
	}
	c, err := net.ListenPacket("udp4", net.JoinHostPort(addr, itoa(port)))
	if err != nil {
		t.Fatalf("listener not closed on stop: %v", err)
	}
	c.Close()
}

// The supervisor holds the addresses with the gateway and drops them when it stops.
func TestSupervisorHoldsAnycastWithGateway(t *testing.T) {
	lo := hookLo(t)
	up := newFakeDNS(t)
	g := defaultGroup()
	g.Interface = "ddgwnone0"
	d := testDNSCfg(up.addr)
	d.ListenPort = freeUDPPort(t)
	g.DNSProxy, g.DNS, g.ExtraVIPs = true, &d, []string{"127.0.0.9"}
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	s := NewSupervisor(context.Background(), dc)
	t.Cleanup(s.StopAll)
	s.refreshPool(dc, true)
	s.mu.Lock()
	s.startGroupLocked(dc.Groups[0])
	s.mu.Unlock()
	if !lo.has("127.0.0.9") {
		t.Fatalf("not held after the gateway started: %+v", s.AnycastStates(1))
	}
	s.mu.Lock()
	s.stopGroupLocked(1)
	s.mu.Unlock()
	if lo.has("127.0.0.9") || s.AnycastStates(1) != nil {
		t.Fatal("still held after the gateway stopped")
	}
	// a paused gateway holds nothing
	g.Paused = true
	s.mu.Lock()
	s.startGroupLocked(g)
	s.mu.Unlock()
	if lo.has("127.0.0.9") {
		t.Fatal("a paused gateway holds its anycast address")
	}
}

// Editing the list through a reload changes the addresses in place: no restart
// of the gateway, the new address appears, the removed one goes.
func TestReloadChangesAnycastInPlace(t *testing.T) {
	lo := hookLo(t)
	up := newFakeDNS(t)
	g := defaultGroup()
	g.Interface = "ddgwnone0"
	d := testDNSCfg(up.addr)
	d.ListenPort = freeUDPPort(t)
	g.DNSProxy, g.DNS, g.ExtraVIPs = true, &d, []string{"127.0.0.9"}
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	s := NewSupervisor(context.Background(), dc)
	t.Cleanup(s.StopAll)
	s.refreshPool(dc, true)
	s.mu.Lock()
	s.startGroupLocked(dc.Groups[0])
	s.mu.Unlock()
	eng := s.engineList()
	nu := newDaemonConfig()
	g2 := g
	g2.ExtraVIPs = []string{"127.0.0.10"}
	nu.Groups = []GroupConfig{g2}
	s.Reload(nu)
	if lo.has("127.0.0.9") || !lo.has("127.0.0.10") {
		t.Fatalf("addresses not swapped: %v", lo.on)
	}
	now := s.engineList()
	if len(now) != len(eng) || (len(eng) > 0 && now[0] != eng[0]) {
		t.Fatal("the gateway was restarted by an anycast change")
	}
}

func TestAnycastCanvasAddDel(t *testing.T) {
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{defaultGroup()}
	ed := func(action, addr string) error {
		_, err := applyCanvasEdit(dc, canvasEdit{Action: action, Kind: "anycast", Group: 1, Address: addr})
		return err
	}
	if err := ed("add", "203.0.113.53"); err != nil {
		t.Fatal(err)
	}
	if err := ed("add", "2001:db8:53::1/128"); err != nil {
		t.Fatal(err)
	}
	if err := ed("add", "203.0.113.53"); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := ed("add", "10.0.0.0/8"); err == nil {
		t.Fatal("subnet accepted")
	}
	if err := ed("add", ""); err == nil {
		t.Fatal("empty address accepted")
	}
	if got := dc.Groups[0].ExtraVIPs; len(got) != 2 || got[1] != "2001:db8:53::1" {
		t.Fatalf("got %v", got)
	}
	if err := ed("del", "203.0.113.99"); err == nil {
		t.Fatal("deleting an unknown address succeeded")
	}
	if err := ed("del", "203.0.113.53"); err != nil || len(dc.Groups[0].ExtraVIPs) != 1 {
		t.Fatalf("not removed: %v %v", err, dc.Groups[0].ExtraVIPs)
	}
}

// Pausing an anycast address withdraws it (on this node, or on every node) and resuming announces it again.
func TestAnycastPause(t *testing.T) {
	lo := hookLo(t)
	up := newFakeDNS(t)
	p := NewPool(testDNSCfg(up.addr))
	p.ProbeNow(context.Background())
	port := freeUDPPort(t)
	const addr = "127.0.0.9"
	a := newAnycastSet(1, []string{addr}, func() *Pool { return p }, func() int { return port })
	scope := ""
	a.paused = func() map[string]string {
		if scope == "" {
			return nil
		}
		return map[string]string{addr: scope}
	}
	a.step()
	if !lo.has(addr) {
		t.Fatal("not announced")
	}
	scope = "node"
	a.step()
	if lo.has(addr) || a.state()[0].Up || a.state()[0].Reason != pausedHereWhy {
		t.Fatalf("paused on this node but announced: %+v", a.state())
	}
	scope = "all"
	a.step()
	if lo.has(addr) || a.state()[0].Reason != pausedAllWhy {
		t.Fatalf("paused on all nodes but announced: %+v", a.state())
	}
	scope = ""
	a.step()
	if !lo.has(addr) || !a.state()[0].Up {
		t.Fatalf("not announced after resume: %+v", a.state())
	}
	a.cancel = func() {}
	a.done = make(chan struct{})
	close(a.done)
	a.stop()
}

// The canvas edit, the cluster split between "this node" and "all nodes", and what deleting an address does to its pause.
func TestAnycastPauseConfig(t *testing.T) {
	dc := newDaemonConfig()
	g := defaultGroup()
	g.ExtraVIPs = []string{"203.0.113.53", "203.0.113.54"}
	dc.Groups = []GroupConfig{g}
	edit := func(action, scope, addr string) (string, error) {
		return applyCanvasEdit(dc, canvasEdit{Action: action, Kind: "anycast", Group: 1, Address: addr, Scope: scope})
	}
	if _, err := edit("pause", "", "203.0.113.53"); err == nil {
		t.Fatal("pause without a scope accepted")
	}
	if _, err := edit("pause", "node", "203.0.113.99"); err == nil {
		t.Fatal("pausing an address the gateway does not have")
	}
	if _, err := edit("pause", "node", "203.0.113.53"); err != nil {
		t.Fatal(err)
	}
	if _, err := edit("pause", "all", "203.0.113.54"); err != nil {
		t.Fatal(err)
	}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	got := dc.Groups[0].pausedAnycast()
	if got["203.0.113.53"] != "node" || got["203.0.113.54"] != "all" {
		t.Fatalf("paused: %v", got)
	}
	// only "all" is shared: a replica gets that one and keeps its own list
	sh := sharedOf(dc)
	if len(sh.Groups[0].PausedVIPs) != 1 || sh.Groups[0].PausedVIPs[0] != "203.0.113.54" {
		t.Fatalf("shared: %+v", sh.Groups[0].PausedVIPs)
	}
	rep := newDaemonConfig()
	rg := defaultGroup()
	rg.PausedVIPsHere = []string{"203.0.113.54"} // its own pause of something else is not touched by a merge
	rep.Groups = []GroupConfig{rg}
	if _, err := mergeShared(rep, sh, nil); err != nil {
		t.Fatal(err)
	}
	if len(rep.Groups[0].PausedVIPs) != 1 || len(rep.Groups[0].PausedVIPsHere) != 1 {
		t.Fatalf("replica: all=%v here=%v", rep.Groups[0].PausedVIPs, rep.Groups[0].PausedVIPsHere)
	}
	// the summary wording, and a hash that does not move for a cluster with nothing paused
	if m, _ := edit("resume", "node", "203.0.113.53"); !strings.Contains(m, "resumed on this node") {
		t.Fatalf("resume: %q", m)
	}
	if m, _ := edit("resume", "node", "203.0.113.53"); !strings.Contains(m, "not paused") {
		t.Fatalf("resume again: %q", m)
	}
	// deleting an address drops its pause
	if _, err := edit("del", "", "203.0.113.54"); err != nil {
		t.Fatal(err)
	}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(dc.Groups[0].PausedVIPs) != 0 || len(dc.Groups[0].pausedAnycast()) != 0 {
		t.Fatalf("pause outlived its address: %v", dc.Groups[0].PausedVIPs)
	}
	plain := newDaemonConfig()
	plain.Groups = []GroupConfig{defaultGroup()}
	h0 := sharedOf(plain).hash()
	plain.Groups[0].PausedVIPs = []string{}
	if sharedOf(plain).hash() != h0 {
		t.Fatal("an empty paused list changes the cluster hash")
	}
}

// The same anycast address on two gateways: one listener, on lo while ANY of them can answer, answered from the first
// gateway that can.
func TestAnycastSharedByTwoGateways(t *testing.T) {
	lo := hookLo(t)
	f1, f2 := newFakeDNS(t), newFakeDNS(t)
	p1, p2 := NewPool(testDNSCfg(f1.addr)), NewPool(testDNSCfg(f2.addr))
	p1.ProbeNow(context.Background())
	p2.ProbeNow(context.Background())
	port := freeUDPPort(t)
	const addr = "127.0.0.9"
	a := newAnycastSet(1, []string{addr}, func() *Pool { return p1 }, func() int { return port })
	b := newAnycastSet(2, []string{addr}, func() *Pool { return p2 }, func() int { return port })
	a.step()
	b.step()
	if !lo.has(addr) || !a.state()[0].Up || !b.state()[0].Up || a.state()[0].Carried != nil {
		t.Fatalf("both healthy: %+v %+v", a.state(), b.state())
	}
	if anycastPool(addr) != p1 {
		t.Fatal("the lowest healthy gateway answers")
	}
	// gateway 1 loses its DNS: the address stays, gateway 2 answers, and gateway 1 says who keeps it up
	f1.mode.Store(2)
	p1.ProbeNow(context.Background())
	a.step()
	b.step()
	if !lo.has(addr) || !a.state()[0].Up || len(a.state()[0].Carried) != 1 || a.state()[0].Carried[0] != 2 || a.state()[0].Reason == "" {
		t.Fatalf("one gateway down, the other up: %+v", a.state())
	}
	if anycastPool(addr) != p2 {
		t.Fatal("the healthy gateway must answer")
	}
	// gateway 2 pauses the address too: now nobody can answer and it is withdrawn
	b.paused = func() map[string]string { return map[string]string{addr: "node"} }
	b.step()
	a.step()
	if lo.has(addr) || a.state()[0].Up || b.state()[0].Up {
		t.Fatalf("nobody can answer but it is announced: %+v %+v", a.state(), b.state())
	}
	// back
	b.paused = nil
	b.step()
	if !lo.has(addr) {
		t.Fatal("not announced again")
	}
	// the gateway that carries it stops: the address goes with the last one
	a.cancel, a.done = func() {}, make(chan struct{})
	close(a.done)
	a.stop()
	if !lo.has(addr) {
		t.Fatal("gateway 1 stopping must not withdraw what gateway 2 still serves")
	}
	b.cancel, b.done = func() {}, make(chan struct{})
	close(b.done)
	b.stop()
	if lo.has(addr) {
		t.Fatal("the last gateway stopped but the address is still announced")
	}
}
