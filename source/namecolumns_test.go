package main

import (
	"context"
	"net"
	"reflect"
	"testing"
)

func TestAddrListAndUniqueLocalAddresses(t *testing.T) {
	ipn := func(s string) net.Addr {
		ip, n, _ := net.ParseCIDR(s)
		n.IP = ip
		return n
	}
	old := nodeIfacesFn
	t.Cleanup(func() { nodeIfacesFn = old })
	nodeIfacesFn = func() []nodeIfaceRaw {
		return []nodeIfaceRaw{
			{Name: "lo", Loopback: true, Addrs: []net.Addr{ipn("127.0.0.1/8"), ipn("::1/128")}},
			{Name: "eth0", Ethernet: true, Addrs: []net.Addr{ipn("192.168.5.5/24"), ipn("fdf5:168:5::5/64"), ipn("2001:db8::5/64"), ipn("fe80::1/64")}},
			{Name: "eth1", Ethernet: true, Addrs: []net.Addr{ipn("fd00::9/64")}}, // only a unique local address
		}
	}
	got := ethernetAddrs()
	if len(got) != 2 || got[0].Name != "eth0" || got[1].Name != "eth1" {
		t.Fatalf("interfaces: %+v", got)
	}
	if !reflect.DeepEqual(got[0].V4, []string{"192.168.5.5/24"}) || !reflect.DeepEqual(got[0].V6, []string{"2001:db8::5/64"}) ||
		!reflect.DeepEqual(got[0].ULA, []string{"fdf5:168:5::5/64"}) {
		t.Fatalf("eth0: %+v (the global ones stay apart from the unique local ones)", got[0])
	}
	want := []string{"192.168.5.5", "2001:db8::5", "fdf5:168:5::5", "fd00::9"}
	if l := addrList(got); !reflect.DeepEqual(l, want) {
		t.Fatalf("addrList %v, want %v (IPv4, then global, then unique local; no prefix lengths)", l, want)
	}
}

func TestNodeNamesByIP(t *testing.T) {
	peers := []PeerView{
		{Addr: "ns1:53854", Hostname: "ns1", Self: true, GwIPs: []string{"192.168.5.5", "fe80::be24:11ff:fe3d:2dd8"}, IPs: []string{"192.168.5.5", "fdf5:168:5::5"}},
		{Addr: "ns2:53854", Hostname: "ns2", GwIPs: []string{"192.168.5.55", "fe80::be24:11ff:fe3d:2dd9"}, IPs: []string{"192.168.5.55"}},
		{Addr: "ns3:53854", Hostname: "ns3", IPs: []string{"192.168.5.12", "fdf5:168:5::12"}}, // a node that does not report its gateway addresses yet
		{Addr: "10.9.9.1:53854", Hostname: "twin", GwIPs: []string{"10.9.9.1"}},
		{Addr: "10.9.9.2:53854", Hostname: "twin", GwIPs: []string{"10.9.9.2"}},
	}
	n := nodeNamesByIP(peers)
	for ip, want := range map[string]string{
		"192.168.5.5": "ns1", "fe80::be24:11ff:fe3d:2dd8": "ns1", "fdf5:168:5::5": "ns1",
		"192.168.5.55": "ns2", "fe80::be24:11ff:fe3d:2dd9": "ns2",
		"192.168.5.12": "ns3", "fdf5:168:5::12": "ns3", // from its interface addresses
		"10.9.9.1": "10.9.9.1:53854", "10.9.9.2": "10.9.9.2:53854", // one host name on two nodes: told apart by address
	} {
		if n[ip] != want {
			t.Errorf("%s: %q, want %q", ip, n[ip], want)
		}
	}
	// the gateway address wins over an interface address when two nodes claim one
	n = nodeNamesByIP([]PeerView{{Addr: "a:1", Hostname: "a", IPs: []string{"10.0.0.7"}}, {Addr: "b:1", Hostname: "b", GwIPs: []string{"10.0.0.7"}}})
	if n["10.0.0.7"] != "b" {
		t.Errorf("gateway address should win: %v", n)
	}
}

func TestNameMembersPutsTheNodeNameOnTheRows(t *testing.T) {
	gs := []GatewayGroup{{GroupID: 1, AF: "v4", Members: []GatewayMember{{IP: "192.168.5.5"}, {IP: "192.168.5.99"}}},
		{GroupID: 1, AF: "v6", Members: []GatewayMember{{IP: "fe80::be24:11ff:fe3d:2dd8"}}}}
	nameMembers(gs, map[string]string{"192.168.5.5": "ns1", "fe80::be24:11ff:fe3d:2dd8": "ns1"})
	if gs[0].Members[0].Name != "ns1" || gs[0].Members[1].Name != "" || gs[1].Members[0].Name != "ns1" {
		t.Fatalf("%+v", gs)
	}
}

// The two nodes of a cluster tell each other their addresses, so each can name the other's gateway address.
func TestPeersReportTheirAddresses(t *testing.T) {
	a, b := twoNodeCluster(t)
	a.mg.gwIPsFn = func() []string { return []string{"10.1.1.1", "fe80::1"} }
	b.mg.gwIPsFn = func() []string { return []string{"10.1.1.2", "fe80::2"} }
	a.sync()
	b.sync()
	v := a.mg.cl.View()
	if len(v.Peers) != 2 {
		t.Fatalf("peers %+v", v.Peers)
	}
	self, other := v.Peers[0], v.Peers[1]
	if !self.Self || !reflect.DeepEqual(self.GwIPs, []string{"10.1.1.1", "fe80::1"}) {
		t.Fatalf("self: %+v", self)
	}
	if !reflect.DeepEqual(other.GwIPs, []string{"10.1.1.2", "fe80::2"}) {
		t.Fatalf("the other node's gateway addresses: %+v", other)
	}
	if len(self.IPs) == 0 || !reflect.DeepEqual(self.IPs, other.IPs) { // both nodes of the test are on this one machine
		t.Fatalf("addresses: self %v other %v", self.IPs, other.IPs)
	}
	names := nodeNamesByIP(v.Peers)
	if names["10.1.1.2"] == "" || names["10.1.1.2"] != names["fe80::2"] || names["10.1.1.1"] == names["10.1.1.2"] {
		t.Fatalf("names %v", names)
	}
}

// The DNS page's servers carry the names given on the Topology page, for the shared pool and for a gateway's own.
func TestDNSStatusServersCarryTheirNames(t *testing.T) {
	shared := newUDPStub(t, func(q []byte, reply func([]byte)) { reply(answerTo(q)) })
	own := newUDPStub(t, func(q []byte, reply func([]byte)) { reply(answerTo(q)) })
	sc, oc := testDNSCfg(shared.addr), testDNSCfg(own.addr)
	sc.ServerNames = map[string]string{shared.addr: "shared-ns"}
	oc.ServerNames = map[string]string{own.addr: "own-ns"}
	mk := func(id int, vip string, d *DNSConfig) GroupConfig {
		g := defaultGroup()
		g.GroupID, g.VIP4, g.DNSProxy, g.DNS = id, vip, true, d
		return g
	}
	dc := newDaemonConfig()
	dc.DNS = sc
	dc.Groups = []GroupConfig{mk(1, "10.1.0.1/24", &oc), mk(2, "10.2.0.1/24", nil)}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	s := NewSupervisor(context.Background(), dc)
	s.refreshPool(dc, true)
	t.Cleanup(func() { s.StopAll() })
	st := NewStatusServer("", s)
	res := st.dnsStatus()
	pools := res["data"].(map[string]any)["pools"].([]map[string]any)
	got := map[int]string{}
	for _, p := range pools {
		for _, sv := range p["servers"].([]ServerStat) {
			got[p["key"].(int)] = sv.Name
		}
	}
	if got[0] != "shared-ns" || got[1] != "own-ns" {
		t.Fatalf("names by pool: %v", got)
	}
	// a server without a name has none
	sc.ServerNames = nil
	if n := s.serverNames(0)[shared.addr]; n != "shared-ns" {
		t.Fatalf("the running configuration still names it: %q", n)
	}
}
