package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestVmacLive runs the real test against a veth pair whose far end sits in a network namespace acting as the router.
// Needs root, iproute2 and DDGW_LIVE_VMAC=1; skipped otherwise (it creates interfaces).
func TestVmacLive(t *testing.T) {
	if os.Getenv("DDGW_LIVE_VMAC") != "1" || os.Geteuid() != 0 {
		t.Skip("set DDGW_LIVE_VMAC=1 as root to run")
	}
	sh := func(cmd string) {
		t.Helper()
		if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", cmd, err, out)
		}
	}
	_, err6 := os.Stat("/proc/net/if_inet6")
	v6 := err6 == nil // this host has IPv6
	sh("ip netns del lvrt 2>/dev/null; ip link del lvp0 2>/dev/null; true")
	sh("ip netns add lvrt && ip link add lvp0 type veth peer name lvr0 && ip link set lvr0 netns lvrt")
	defer exec.Command("sh", "-c", "ip netns del lvrt; ip link del lvp0").Run()
	sh("ip netns exec lvrt sh -c 'ip link set lo up; ip link set lvr0 up; ip addr add 10.77.0.1/24 dev lvr0'")
	if v6 {
		sh("ip netns exec lvrt ip -6 addr add fd77::1/64 dev lvr0 nodad")
	}
	sh("ip link set lvp0 up; ip addr add 10.77.0.2/24 dev lvp0; ip route add default via 10.77.0.1 dev lvp0 metric 5")
	if v6 {
		sh("ip -6 addr add fd77::2/64 dev lvp0 nodad; ip -6 route add default via fd77::1 dev lvp0 metric 5")
	}
	sh("sleep 2; ping -c1 -W1 10.77.0.1 >/dev/null; ping -6 -c1 -W1 fd77::1 >/dev/null; true")
	gc := GroupConfig{GroupID: 9, Interface: "lvp0", VIP4: "10.77.0.50/24"}
	nfam := 1
	if v6 {
		gc.VIP6, nfam = "fd77::50/64", 2
	}

	r := vmacTestGroup(gc)
	t.Logf("open network: %s — %s", r.Verdict, r.Detail)
	if r.Verdict != vmacDelivered {
		t.Fatalf("with an open network the verdict must be delivered, got %+v", r)
	}
	m := vmacTestMAC(9)
	mac := fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", m[0], m[1], m[2], m[3], m[4], m[5])
	sh("ip netns exec lvrt nft add table arp t && ip netns exec lvrt nft 'add chain arp t o { type filter hook output priority 0; }' && ip netns exec lvrt nft add rule arp t o arp daddr ether " + mac + " drop")
	r = vmacTestGroup(gc)
	t.Logf("the router's ARP replies to the virtual MAC dropped: %s — %s", r.Verdict, r.Detail)
	if r.Verdict != vmacNotDelivered || len(r.Families) != nfam {
		t.Fatalf("with the router's ARP replies to the virtual MAC dropped the verdict must be not-delivered, got %+v", r)
	}
	sh("ip netns exec lvrt sh -c 'ip link set lvr0 down'")
	r = vmacTestGroup(gc)
	t.Logf("router silent: %s — %s", r.Verdict, r.Detail)
	if r.Verdict != vmacInconclusive {
		t.Fatalf("with a silent router the verdict must be inconclusive, got %+v", r)
	}
	if out, _ := exec.Command("ip", "-o", "link").Output(); strings.Contains(string(out), "ddgwt9") {
		t.Fatal("the throwaway interface was left behind")
	}
	gc.RealMACs = true
	if r := vmacTestGroup(gc); r.Verdict != vmacOff {
		t.Fatalf("real MACs already on must say off, got %+v", r)
	}
}
