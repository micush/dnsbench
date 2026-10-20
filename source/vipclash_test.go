package main

import (
	"net/netip"
	"strings"
	"testing"
)

func TestVIPCannotBeANodeAddress(t *testing.T) {
	var own netip.Addr
	for a := range nodeAddrOwners(nil) {
		own = a
		break
	}
	if !own.IsValid() {
		t.Skip("this machine has no non-loopback address")
	}
	bits := 24
	if own.Is6() {
		bits = 64
	}
	vip := netip.PrefixFrom(own, bits).String()
	mk := func(g GroupConfig) *DaemonConfig { dc := newDaemonConfig(); dc.Groups = []GroupConfig{g}; return dc }
	g := defaultGroup()
	g.VIP4, g.VIP6 = "", ""
	if own.Is4() {
		g.VIP4 = vip
	} else {
		g.VIP6 = vip
	}
	err := checkVIPsNotNodes(mk(defaultGroup()), mk(g), nil)
	if err == nil || !strings.Contains(err.Error(), own.String()) {
		t.Fatalf("a node's own address was accepted as a shared address: %v", err)
	}
	// already in the saved configuration: still saveable, so it can be fixed
	if err := checkVIPsNotNodes(mk(g), mk(g), nil); err != nil {
		t.Fatalf("an existing address blocked a save: %v", err)
	}
	// anycast addresses too
	h := defaultGroup()
	h.ExtraVIPs = []string{own.String()}
	if err := checkVIPsNotNodes(mk(defaultGroup()), mk(h), nil); err == nil {
		t.Fatal("anycast address equal to a node address accepted")
	}
}
