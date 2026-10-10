package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The VIP's macvlan must not take over traffic this node sends into the subnet:
// with the kernel's automatic connected route it did, and a node serving a
// gateway lost contact with the node holding the VIP on its loopback.
func TestAddVIPDoesNotStealOutboundRoute(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	ip := func(a ...string) (string, error) {
		o, err := exec.Command("ip", a...).CombinedOutput()
		return string(o), err
	}
	if _, err := ip("link", "add", "ddgwt0", "type", "bridge"); err != nil {
		t.Skip("cannot create a test interface here")
	}
	defer ip("link", "del", "ddgwt0")
	ip("link", "set", "ddgwt0", "up")
	// the parent as NetworkManager configures it: address without its own route, route at metric 100
	ip("addr", "add", "198.51.100.2/24", "dev", "ddgwt0", "noprefixroute")
	ip("route", "add", "198.51.100.0/24", "dev", "ddgwt0", "proto", "kernel", "scope", "link", "src", "198.51.100.2", "metric", "100")
	if !addVmac("ddgwt0", 99, 1) {
		t.Skip("cannot create a macvlan here")
	}
	defer delVmac(99, 1)
	for i := 0; i < 2; i++ { // twice: re-adding an existing address must also apply
		addVIP(99, 1, "198.51.100.111/24")
	}
	out, err := ip("route", "get", "198.51.100.50")
	if err != nil || !strings.Contains(out, "dev ddgwt0 ") || !strings.Contains(out, "src 198.51.100.2") {
		t.Fatalf("outbound traffic would leave via the VIP: %q (%v)", out, err)
	}
	if a, _ := ip("addr", "show", "ddgw99.1"); !strings.Contains(a, "198.51.100.111/24") {
		t.Fatalf("VIP not on the macvlan: %s", a)
	}
	// and with a parent that has no address in the subnet the VIP's own route is still there
	ip("route", "del", "198.51.100.0/24", "dev", "ddgwt0")
	ip("addr", "del", "198.51.100.2/24", "dev", "ddgwt0")
	if out, _ := ip("route", "get", "198.51.100.50"); !strings.Contains(out, "dev ddgw99.1") {
		t.Fatalf("no route into the subnet left: %q", out)
	}
}
