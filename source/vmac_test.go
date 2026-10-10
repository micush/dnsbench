package main

import (
	"net"
	"os"
	"strings"
	"testing"
)

// The v4 and v6 engines of a group share one macvlan and both call addVmac when the node becomes the controller.
// The second call must not throw away what the first put on it (the IPv4 gateway address once vanished that way).
func TestAddVmacKeepsExistingMacvlanAndItsAddresses(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create a macvlan")
	}
	const group, slot = 199, 7
	name := vmacName(group, slot)
	const parent = "vt199a" // a throwaway veth end to put the macvlan on
	if !runCmd("ip", "link", "add", parent, "type", "veth", "peer", "name", "vt199b") {
		t.Skip("cannot create a veth pair here")
	}
	t.Cleanup(func() { delVmac(group, slot); runCmd("ip", "link", "del", parent) })
	runCmd("ip", "link", "set", parent, "up")
	if !addVmac(parent, group, slot) {
		t.Skip("cannot create a macvlan here")
	}
	if !runCmd("ip", "addr", "add", "10.199.0.1/24", "dev", name, "noprefixroute") {
		t.Skip("cannot add an address here")
	}
	if !addVmac(parent, group, slot) { // what the other address family's engine does a moment later
		t.Fatal("second addVmac failed")
	}
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	addrs, _ := ifc.Addrs()
	found := false
	for _, a := range addrs {
		if a.String() == "10.199.0.1/24" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the address was lost when the macvlan was set up again: %v", addrs)
	}
	if !vmacCurrent(parent, group, slot) {
		t.Fatal("vmacCurrent must see the macvlan as right")
	}
	if vmacCurrent(parent, group, slot+1) {
		t.Fatal("a slot with no macvlan must not look current")
	}
}

// A forwarder's interface must not filter strictly on the reverse path (every query to the VIP arrives on it and
// would be dropped without a trace).  Strict (1) becomes loose (2) on the interface, its parent and the global
// setting; anything else is left alone; a setting that cannot be written is reported.
func TestRelaxReversePath(t *testing.T) {
	old := procIPv4Conf
	defer func() { procIPv4Conf = old }()
	type vals struct{ dev, parent, all string }
	for _, c := range []struct {
		in, want vals
		changed  int
	}{
		{vals{"1", "1", "1"}, vals{"2", "2", "2"}, 3},
		{vals{"1", "2", "2"}, vals{"2", "2", "2"}, 1},
		{vals{"0", "0", "0"}, vals{"0", "0", "0"}, 0},
		{vals{"2", "1", "0"}, vals{"2", "2", "0"}, 1},
	} {
		dir := t.TempDir()
		procIPv4Conf = dir + "/"
		put := func(name, v string) {
			if err := os.MkdirAll(dir+"/"+name, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dir+"/"+name+"/rp_filter", []byte(v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		put("ddgw1.2", c.in.dev)
		put("eth0", c.in.parent)
		put("all", c.in.all)
		changed, failed := relaxReversePath("ddgw1.2", "eth0")
		get := func(n string) string {
			b, _ := os.ReadFile(dir + "/" + n + "/rp_filter")
			return strings.TrimSpace(string(b))
		}
		got := vals{get("ddgw1.2"), get("eth0"), get("all")}
		if got != c.want || len(changed) != c.changed || len(failed) != 0 {
			t.Errorf("in %+v: now %+v changed=%v failed=%v, want %+v (%d changed)", c.in, got, changed, failed, c.want, c.changed)
		}
	}
	// a setting that does not exist or cannot be written is reported, not silently skipped
	if ensureSysctl("/proc/sys/net/ipv4/conf/ddgw-no-such-dev/arp_ignore", "1") {
		t.Error("ensureSysctl claimed success on a path that does not exist")
	}
}
