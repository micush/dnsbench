package main

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func fakeAddrs(cidrs ...string) func(string) ([]net.Addr, error) {
	return func(string) ([]net.Addr, error) {
		var out []net.Addr
		for _, c := range cidrs {
			ip, n, _ := net.ParseCIDR(c)
			n.IP = ip
			out = append(out, n)
		}
		return out, nil
	}
}

func TestOnGatewaySubnet(t *testing.T) {
	old := ifaceAddrsFn
	defer func() { ifaceAddrsFn = old }()
	g := defaultGroup()
	g.VIP4 = "192.168.122.111/24"

	ifaceAddrsFn = fakeAddrs("192.168.122.74/24", "fe80::1/64")
	if ok, why := onGatewaySubnet(g); !ok {
		t.Fatalf("same subnet refused: %s", why)
	}
	ifaceAddrsFn = fakeAddrs("10.5.5.9/24", "fe80::1/64")
	if ok, why := onGatewaySubnet(g); ok || why == "" {
		t.Fatalf("other subnet accepted (%v %q)", ok, why)
	}
	ifaceAddrsFn = func(string) ([]net.Addr, error) { return nil, net.UnknownNetworkError("x") }
	if ok, _ := onGatewaySubnet(g); !ok {
		t.Fatal("a missing interface must be left to the engine's own wait")
	}
	// IPv6-only gateway
	g.VIP4, g.VIP6 = "", "2001:db8:1::1/64"
	ifaceAddrsFn = fakeAddrs("10.5.5.9/24", "fe80::1/64")
	if ok, _ := onGatewaySubnet(g); !ok {
		t.Fatal("link-local only: nothing to compare, must be let through")
	}
	ifaceAddrsFn = fakeAddrs("10.5.5.9/24", "fe80::1/64", "2001:db8:1::74/64")
	if ok, why := onGatewaySubnet(g); !ok {
		t.Fatalf("global address in the VIP prefix refused: %s", why)
	}
	ifaceAddrsFn = fakeAddrs("fe80::1/64", "2001:db8:9::74/64")
	if ok, why := onGatewaySubnet(g); ok || why == "" {
		t.Fatal("global address in another prefix accepted")
	}
	ifaceAddrsFn = fakeAddrs("fd00:1::5/64")
	if ok, _ := onGatewaySubnet(g); ok {
		t.Fatal("a ULA in another prefix accepted")
	}
	// dual stack: every checkable family must match
	g.VIP4 = "10.5.5.1/24"
	ifaceAddrsFn = fakeAddrs("10.5.5.9/24", "2001:db8:9::74/64")
	if ok, _ := onGatewaySubnet(g); ok {
		t.Fatal("v4 matches but v6 does not: must be refused")
	}
	ifaceAddrsFn = fakeAddrs("10.5.5.9/24", "2001:db8:1::74/64")
	if ok, _ := onGatewaySubnet(g); !ok {
		t.Fatal("both families match: refused")
	}
	if !addrsInPrefix(fakeAddrsList(t, "127.0.0.1/8"), netip.MustParsePrefix("127.0.0.9/8")) {
		t.Fatal("loopback config must match its own /8")
	}
}

func fakeAddrsList(t *testing.T, c string) []net.Addr {
	a, _ := fakeAddrs(c)("")
	return a
}

// A node on another subnet does not start the gateway, shows it grey, does not
// count as serving it (so it is no cover for a neighbour's update), and starts
// by itself once it has an address in the subnet.
func TestGatewayHeldBackOffSubnet(t *testing.T) {
	oldF, oldE := ifaceAddrsFn, offnetEvery
	offnetEvery = 20 * time.Millisecond
	ifaceAddrsFn = fakeAddrs("10.5.5.9/24")
	defer func() { ifaceAddrsFn, offnetEvery = oldF, oldE }()

	g := defaultGroup()
	g.Interface = "ddgwnone0"
	g.VIP4 = "192.168.122.111/24"
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	s := NewSupervisor(context.Background(), dc)
	t.Cleanup(s.StopAll)
	s.mu.Lock()
	s.startGroupWhenReadyLocked(g)
	s.mu.Unlock()
	time.Sleep(150 * time.Millisecond)
	if len(s.engineList()) != 0 {
		t.Fatal("started a gateway on the wrong subnet")
	}
	off := s.offnetGroups()
	if off[1] == "" {
		t.Fatal("not reported as off-subnet")
	}
	cg := buildCanvas(dc, nil, nil)
	markOffnet(cg, off)
	if cg[0].Status != "idle" || cg[0].Families[0].Status != "idle" {
		t.Fatalf("canvas: %s %s", cg[0].Status, cg[0].Detail)
	}
	// reported as not serving, so a neighbour's update gate does not count it
	st := gatewayStates(dc, nil)
	if len(st) != 1 || st[0].Serving {
		t.Fatalf("off-subnet node claims to serve: %+v", st)
	}
	if ok, _ := safeToTakeDown([]GwState{{GroupID: 1, Serving: true}}, []peerGateways{{Known: true, Serving: map[int]bool{1: st[0].Serving}}}); ok {
		t.Fatal("update gate counted the off-subnet node as cover")
	}
	// it gets an address in the subnet
	ifaceAddrsFn = fakeAddrs("192.168.122.74/24")
	deadline := time.Now().Add(5 * time.Second)
	for len(s.engineList()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(s.engineList()) == 0 || len(s.offnetGroups()) != 0 {
		t.Fatal("did not start once on the subnet")
	}
}

// Pausing the gateway while it is held back cancels the wait.
func TestOffSubnetWaitCancelled(t *testing.T) {
	oldF, oldE := ifaceAddrsFn, offnetEvery
	offnetEvery = 20 * time.Millisecond
	ifaceAddrsFn = fakeAddrs("10.5.5.9/24")
	defer func() { ifaceAddrsFn, offnetEvery = oldF, oldE }()
	g := defaultGroup()
	g.Interface = "ddgwnone0"
	g.VIP4 = "192.168.122.111/24"
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	s := NewSupervisor(context.Background(), dc)
	t.Cleanup(s.StopAll)
	s.mu.Lock()
	s.startGroupWhenReadyLocked(g)
	s.stopGroupLocked(1)
	s.mu.Unlock()
	ifaceAddrsFn = fakeAddrs("192.168.122.74/24")
	time.Sleep(200 * time.Millisecond)
	if len(s.engineList()) != 0 || len(s.offnetGroups()) != 0 {
		t.Fatal("a removed gateway started anyway")
	}
}
