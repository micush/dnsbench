package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bgpEnv is a web environment whose FRR is a fake: saving BGP settings starts the (asynchronous) apply, which must
// not touch the real /etc/frr nor still be running when the next test swaps the fake out.
func bgpEnv(t *testing.T) *webEnv {
	t.Helper()
	fakeFRR(t, nil)
	e := newWebEnv(t)
	t.Cleanup(func() { // runs before fakeFRR's own cleanup puts the real values back
		for i := 0; i < 400; i++ {
			e.mg.bgp.mu.Lock()
			busy := e.mg.bgp.running
			e.mg.bgp.mu.Unlock()
			if !busy {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	return e
}

// A router ID is meaningless without an AS: the settings refuse it, and a stored one next to a cleared AS is dropped
// rather than stopping the config from loading.
func TestBGPRouterIDNeedsASN(t *testing.T) {
	e := bgpEnv(t)
	err := e.mg.BGPSet(BGPConfig{RouterID: "192.0.2.10"}, "t")
	if err == nil || !strings.Contains(err.Error(), "router id needs a local AS") {
		t.Fatalf("a router id without an AS was accepted: %v", err)
	}
	if err := e.mg.BGPSet(BGPConfig{ASN: 64512, RouterID: "192.0.2.10"}, "t"); err != nil {
		t.Fatal(err)
	}
	// clearing the AS together with the router id is what the page and `--asn off` send
	if err := e.mg.BGPSet(BGPConfig{}, "t"); err != nil {
		t.Fatal(err)
	}
	dc, _, _ := e.mg.LiveConfig()
	if dc.BGP != nil && (dc.BGP.RouterID != "" || dc.BGP.ASN != 0) {
		t.Fatalf("settings after clearing the AS: %+v", dc.BGP)
	}
	// an old file with a router id and no AS still validates; the router id goes
	old := BGPConfig{RouterID: "192.0.2.10", Disabled: true}
	if err := old.Validate(); err != nil || old.RouterID != "" || old.Disabled {
		t.Fatalf("old config not normalised: %+v %v", old, err)
	}
}

func TestRenderFRRDisabledNeighborAndProcess(t *testing.T) {
	b := &BGPConfig{ASN: 64512, Neighbors: []BGPNeighbor{
		{Peer: "192.0.2.1", RemoteAS: 64500, Disabled: true},
		{Peer: "192.0.2.2", RemoteAS: 64500},
	}}
	conf := renderFRR(b, []string{"203.0.113.53"}, nil, "h")
	if !strings.Contains(conf, " neighbor 192.0.2.1 shutdown\n") || strings.Contains(conf, " neighbor 192.0.2.2 shutdown") {
		t.Fatalf("only the disabled neighbor is shut down:\n%s", conf)
	}
	if !strings.Contains(conf, " neighbor 192.0.2.1 remote-as 64500\n") {
		t.Fatalf("the disabled neighbor must stay configured:\n%s", conf)
	}
	b.Disabled = true
	if conf := renderFRR(b, nil, nil, "h"); strings.Contains(conf, "router bgp") {
		t.Fatalf("a disabled process must leave no BGP section:\n%s", conf)
	}
	if b.Active() || !b.Configured() {
		t.Fatal("a disabled process is configured but not active")
	}
}

func TestBGPOperate(t *testing.T) {
	e := bgpEnv(t)
	if err := e.mg.BGPOperate(BGPOperateArgs{Enabled: false}, "t"); err == nil {
		t.Fatal("disabling BGP without an AS should be refused")
	}
	set := BGPConfig{ASN: 64512, RouterID: "192.0.2.10", Neighbors: []BGPNeighbor{
		{Peer: "192.0.2.1", RemoteAS: 64500}, {Peer: "2001:db8::1", RemoteAS: 64500}}}
	if err := e.mg.BGPSet(set, "t"); err != nil {
		t.Fatal(err)
	}
	get := func() *BGPConfig { dc, _, _ := e.mg.LiveConfig(); return dc.BGP }

	if err := e.mg.BGPOperate(BGPOperateArgs{Peer: "2001:DB8::1", Enabled: false}, "t"); err != nil {
		t.Fatal(err)
	}
	if c := get(); c.Neighbors[0].Disabled || !c.Neighbors[1].Disabled || c.Disabled || !c.Active() {
		t.Fatalf("after disabling one neighbor: %+v", c)
	}
	if err := e.mg.BGPOperate(BGPOperateArgs{Peer: "192.0.2.99", Enabled: false}, "t"); err == nil {
		t.Fatal("an unknown neighbor was accepted")
	}
	if err := e.mg.BGPOperate(BGPOperateArgs{Peer: "x", Enabled: false}, "t"); err == nil {
		t.Fatal("a bad address was accepted")
	}

	// editing the settings does not switch anything back on (the page sends the flags it last saw, or none)
	edit := *get()
	edit.Keepalive = 5
	edit.Neighbors = []BGPNeighbor{{Peer: "192.0.2.1", RemoteAS: 64500}, {Peer: "2001:db8::1", RemoteAS: 64500, Disabled: false}, {Peer: "192.0.2.3", RemoteAS: 64501}}
	if err := e.mg.BGPSet(edit, "t"); err != nil {
		t.Fatal(err)
	}
	if c := get(); !c.Neighbors[1].Disabled || c.Neighbors[0].Disabled || c.Neighbors[2].Disabled || c.Keepalive != 5 {
		t.Fatalf("settings edit changed the neighbors' on/off state: %+v", c.Neighbors)
	}

	if err := e.mg.BGPOperate(BGPOperateArgs{Enabled: false}, "t"); err != nil {
		t.Fatal(err)
	}
	c := get()
	if !c.Disabled || c.Active() || c.ASN != 64512 || len(c.Neighbors) != 3 || c.RouterID != "192.0.2.10" {
		t.Fatalf("a disabled process keeps its settings: %+v", c)
	}
	edit = *c
	edit.Disabled = false // the settings path must not be able to switch it on
	if err := e.mg.BGPSet(edit, "t"); err != nil {
		t.Fatal(err)
	}
	if !get().Disabled {
		t.Fatal("the settings edit switched BGP back on")
	}
	if err := e.mg.BGPOperate(BGPOperateArgs{Enabled: true}, "t"); err != nil {
		t.Fatal(err)
	}
	if c := get(); c.Disabled || !c.Active() || !c.Neighbors[1].Disabled {
		t.Fatalf("enabled again: %+v", c)
	}
	// clearing the AS forgets the process-level flag
	if err := e.mg.BGPOperate(BGPOperateArgs{Enabled: false}, "t"); err != nil {
		t.Fatal(err)
	}
	if err := e.mg.BGPSet(BGPConfig{}, "t"); err != nil {
		t.Fatal(err)
	}
	if err := e.mg.BGPSet(BGPConfig{ASN: 64512}, "t"); err != nil {
		t.Fatal(err)
	}
	if get().Disabled {
		t.Fatal("a fresh AS must start enabled")
	}
}

func TestBGPOperateAPIAndStatus(t *testing.T) {
	e := bgpEnv(t)
	e.login("alice", "pw")
	if r := e.do("PUT", "/api/bgp", BGPConfig{ASN: 64512, Neighbors: []BGPNeighbor{{Peer: "192.0.2.1", RemoteAS: 64500}}}, withAuth(e, true)); r.code != 200 {
		t.Fatalf("set: %d %s", r.code, r.raw)
	}
	r := e.do("POST", "/api/bgp/operate", BGPOperateArgs{Peer: "192.0.2.1", Enabled: false}, withAuth(e, true))
	if r.code != 200 {
		t.Fatalf("operate: %d %s", r.code, r.raw)
	}
	if !strings.Contains(string(r.raw), `"disabled":true`) {
		t.Fatalf("the reply does not show the neighbor as disabled: %s", r.raw)
	}
	if r := e.do("POST", "/api/bgp/operate", BGPOperateArgs{Enabled: false}, withAuth(e, true)); r.code != 200 || !strings.Contains(string(r.raw), `"disabled":true`) {
		t.Fatalf("operate process: %d %s", r.code, r.raw)
	}
	if r := e.do("POST", "/api/bgp/operate", BGPOperateArgs{Peer: "198.51.100.1", Enabled: true}, withAuth(e, true)); r.code == 200 {
		t.Fatal("an unknown neighbor was accepted over the API")
	}
	// a router id without an AS is refused over the API too
	if r := e.do("PUT", "/api/bgp", BGPConfig{RouterID: "192.0.2.10"}, withAuth(e, true)); r.code == 200 {
		t.Fatal("router id without AS accepted over the API")
	}
	if err := proxyAllowed("POST", "/api/bgp/operate"); err != nil {
		t.Fatalf("the operate call must be relayable to another node: %v", err)
	}
}

func TestNodeEthernetAddrs(t *testing.T) {
	ipn := func(s string) net.Addr {
		ip, n, _ := net.ParseCIDR(s)
		n.IP = ip
		return n
	}
	old := nodeIfacesFn
	t.Cleanup(func() { nodeIfacesFn = old })
	nodeIfacesFn = func() []nodeIfaceRaw {
		return []nodeIfaceRaw{
			{Name: "lo", Loopback: true, Ethernet: false, Addrs: []net.Addr{ipn("127.0.0.1/8")}},
			{Name: "veth1", Ethernet: false, Addrs: []net.Addr{ipn("10.9.9.1/24")}},
			{Name: "eth1", Ethernet: true, Addrs: []net.Addr{ipn("fe80::1/64")}},
			{Name: "eth0", Ethernet: true, Addrs: []net.Addr{
				ipn("fe80::1234/64"), ipn("fd00::5/64"), ipn("2001:db8::5/64"), ipn("10.0.0.5/24"), ipn("169.254.1.1/16"), ipn("3fff::9/48")}},
		}
	}
	got := ethernetAddrs()
	if len(got) != 1 || got[0].Name != "eth0" {
		t.Fatalf("only an Ethernet interface with a usable address is listed: %+v", got)
	}
	if strings.Join(got[0].V4, ",") != "10.0.0.5/24" || strings.Join(got[0].V6, ",") != "2001:db8::5/64,3fff::9/48" {
		t.Fatalf("addresses: %+v (link-local, ULA and 169.254 must be left out)", got[0])
	}
}

// A made-up /sys/class/net: which interfaces count as Ethernet.
func TestIsEthernetIfaceKinds(t *testing.T) {
	root := t.TempDir()
	cls, virt := filepath.Join(root, "class")+"/", filepath.Join(root, "virtual")+"/"
	oldC, oldV := sysClassNet, sysVirtualNet
	sysClassNet, sysVirtualNet = cls, virt
	t.Cleanup(func() { sysClassNet, sysVirtualNet = oldC, oldV })
	mk := func(name, typ string, virtual bool, devtype string, subdirs ...string) {
		d := filepath.Join(cls, name)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "type"), []byte(typ+"\n"), 0o644)
		uev := "INTERFACE=" + name + "\n"
		if devtype != "" {
			uev += "DEVTYPE=" + devtype + "\n"
		}
		os.WriteFile(filepath.Join(d, "uevent"), []byte(uev), 0o644)
		for _, s := range subdirs {
			os.MkdirAll(filepath.Join(d, s), 0o755)
		}
		if virtual {
			os.MkdirAll(filepath.Join(virt, name), 0o755)
		}
	}
	mk("ens18", "1", false, "")           // a physical (or virtio) card
	mk("eth0", "1", true, "veth")         // a container's network card
	mk("vmbr0", "1", true, "bridge")      // a bridge
	mk("bond0", "1", true, "bond")        // a bond
	mk("eth0.5", "1", true, "vlan")       // a VLAN
	mk("br9", "1", true, "", "bridge")    // a bridge seen only by its directory
	mk("bondx", "1", true, "", "bonding") // a bond seen only by its directory
	mk("mv0", "1", true, "macvlan")       // someone's macvlan
	mk("ddgw1.1", "1", true, "macvlan")   // ddgw's own
	mk("ddgwlike", "1", false, "")        // anything named ddgw* is ddgw's
	mk("vxlan0", "1", true, "vxlan")      // an overlay
	mk("tap0", "1", true, "")             // a tap
	mk("dummy0", "1", true, "")           // a dummy
	mk("tun0", "65534", true, "")         // not Ethernet at all
	mk("lo", "772", true, "")             // loopback
	want := map[string]bool{"ens18": true, "eth0": true, "vmbr0": true, "bond0": true, "eth0.5": true, "br9": true, "bondx": true}
	for _, n := range []string{"ens18", "eth0", "vmbr0", "bond0", "eth0.5", "br9", "bondx", "mv0", "ddgw1.1", "ddgwlike", "vxlan0", "tap0", "dummy0", "tun0", "lo", "missing"} {
		if got := isEthernetIface(n); got != want[n] {
			t.Errorf("%s: Ethernet = %v, want %v", n, got, want[n])
		}
	}
}

// When no interface of a kind that counts as Ethernet has an address, the others (not loopback, not ddgw's) are listed.
func TestEthernetAddrsFallsBackToOtherInterfaces(t *testing.T) {
	ipn := func(s string) net.Addr {
		ip, n, _ := net.ParseCIDR(s)
		n.IP = ip
		return n
	}
	old := nodeIfacesFn
	t.Cleanup(func() { nodeIfacesFn = old })
	nodeIfacesFn = func() []nodeIfaceRaw {
		return []nodeIfaceRaw{
			{Name: "lo", Loopback: true, Addrs: []net.Addr{ipn("127.0.0.1/8")}},
			{Name: "ddgw1.1", Addrs: []net.Addr{ipn("10.0.0.56/24")}},
			{Name: "eth0", Ethernet: true, Addrs: []net.Addr{ipn("fe80::1/64")}}, // Ethernet, but nothing usable
			{Name: "odd0", Addrs: []net.Addr{ipn("10.5.5.5/24"), ipn("2001:db8::5/64")}},
		}
	}
	got := ethernetAddrs()
	if len(got) != 1 || got[0].Name != "odd0" || strings.Join(got[0].V4, ",") != "10.5.5.5/24" || strings.Join(got[0].V6, ",") != "2001:db8::5/64" {
		t.Fatalf("fallback: %+v (ddgw's own interface and the loopback must stay out)", got)
	}
}
