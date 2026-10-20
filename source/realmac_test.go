package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	rmSelf = [6]byte{0x02, 0x00, 0x00, 0xc0, 0xff, 0xee} // this node's real MAC
	rmPeer = [6]byte{0x02, 0x00, 0x00, 0xaa, 0xbb, 0x03} // the node with slot 3
	rmCli  = [6]byte{0x00, 0x1b, 0x17, 0x00, 0x01, 0x14} // the firewall
)

// rmEngine is a controller in a group with two other nodes (slots 2 and 3), in the mode asked for.
func rmEngine(t *testing.T, real bool) *Engine {
	t.Helper()
	cfg := defaultGroup()
	cfg.Interface, cfg.RealMACs = "dgwtest0", real
	cfg.VIP4, cfg.VIP6 = "192.0.2.9/24", "2001:db8::9/64"
	cfg.LBMethod = lbFailover
	e := NewEngine(cfg, afIPv4, func() DNSConfig { return defaultDNS() }, func() *Pool { return nil })
	e.running, e.myIP, e.afnID, e.state = true, "192.0.2.1", 1, stateActive
	now := time.Now()
	e.peers = map[string]*Peer{
		"192.0.2.2": {IP: "192.0.2.2", AfnID: 2, Priority: 100, Weight: 100, LastSeen: now, FirstSeen: now},
		"192.0.2.3": {IP: "192.0.2.3", AfnID: 3, Priority: 100, Weight: 100, LastSeen: now, FirstSeen: now},
	}
	oldM := ifaceMACFn
	ifaceMACFn = func(string) [6]byte { return rmSelf }
	t.Cleanup(func() { ifaceMACFn = oldM; e.Stop() })
	return e
}

func arpRequest(senderMAC [6]byte, senderIP, target string) []byte {
	return buildARPRequest(senderMAC, [4]byte(net.ParseIP(senderIP).To4()), [4]byte(net.ParseIP(target).To4()))
}

func replyMAC(t *testing.T, f []byte) (payload, ethSrc, ethDst [6]byte) {
	t.Helper()
	if len(f) != 42 || f[12] != 8 || f[13] != 6 || f[21] != 2 {
		t.Fatalf("not an ARP reply: %x", f)
	}
	return [6]byte(f[22:28]), [6]byte(f[6:12]), [6]byte(f[0:6])
}

// By default the answer names the slot's virtual MAC, as it always did.
func TestARPAnswerIsAVirtualMACByDefault(t *testing.T) {
	e := rmEngine(t, false)
	e.failoverPrimary = 3
	out := e.arpAnswer(arpRequest(rmCli, "192.0.2.254", "192.0.2.9"), rmSelf, [4]byte{192, 0, 2, 9})
	pay, src, dst := replyMAC(t, out)
	if pay != vmacBytes(1, 3) || src != rmSelf || dst != rmCli {
		t.Fatalf("payload %x eth src %x dst %x", pay, src, dst)
	}
	// and it does not learn MACs
	if len(e.peerMACs) != 0 {
		t.Fatalf("learned %v by default", e.peerMACs)
	}
}

func TestRealMACAnswerNamesTheNodesOwnMAC(t *testing.T) {
	e := rmEngine(t, true)
	e.failoverPrimary = 3
	vip := [4]byte{192, 0, 2, 9}
	req := arpRequest(rmCli, "192.0.2.254", "192.0.2.9")

	// slot 3's MAC is not known yet: the controller answers with its own (always right: it holds the VIP too)
	pay, src, _ := replyMAC(t, e.arpAnswer(req, rmSelf, vip))
	if pay != rmSelf || src != rmSelf {
		t.Fatalf("unknown MAC: payload %x", pay)
	}
	// the node sends an ARP of its own (a request for something else): its real MAC is learned
	e.arpAnswer(arpRequest(rmPeer, "192.0.2.3", "192.0.2.77"), rmSelf, vip)
	pay, src, dst := replyMAC(t, e.arpAnswer(req, rmSelf, vip))
	if pay != rmPeer || src != rmSelf || dst != rmCli {
		t.Fatalf("learned MAC: payload %x eth src %x dst %x (the answer is sent from this node's MAC, naming the other's)", pay, src, dst)
	}
	// slot 1 is this node's own: its own real MAC
	e.failoverPrimary = 1
	e.afnID = 1
	if pay, _, _ := replyMAC(t, e.arpAnswer(req, rmSelf, vip)); pay != rmSelf {
		t.Fatalf("own slot: %x", pay)
	}
	// a node that has expired is not pointed at
	e.afnID = 1
	e.failoverPrimary = 3
	e.peers["192.0.2.3"].LastSeen = time.Now().Add(-time.Hour)
	pay, _, _ = replyMAC(t, e.arpAnswer(req, rmSelf, vip))
	if pay == rmPeer {
		t.Fatalf("an expired node was answered with")
	}
}

func TestRealMACLearningIsCareful(t *testing.T) {
	e := rmEngine(t, true)
	vip := [4]byte{192, 0, 2, 9}
	e.arpAnswer(arpRequest(rmPeer, "192.0.2.99", "192.0.2.77"), rmSelf, vip)                         // not a node of the group
	e.arpAnswer(arpRequest(rmPeer, "0.0.0.0", "192.0.2.77"), rmSelf, vip)                            // a probe
	e.arpAnswer(arpRequest([6]byte{0x01, 0, 0x5e, 0, 0, 1}, "192.0.2.3", "192.0.2.77"), rmSelf, vip) // a multicast source
	e.arpAnswer(arpRequest([6]byte{}, "192.0.2.3", "192.0.2.77"), rmSelf, vip)
	if len(e.peerMACs) != 0 {
		t.Fatalf("learned from what it should not: %v", e.peerMACs)
	}
	e.arpAnswer(arpRequest(rmPeer, "192.0.2.3", "192.0.2.77"), rmSelf, vip)
	if en := e.peerMACs["192.0.2.3"]; en == nil || en.mac != rmPeer {
		t.Fatalf("a node's own ARP was not learned: %v", e.peerMACs)
	}
}

func v6frame(srcMAC [6]byte, src, dst string, next byte, l4 []byte) []byte {
	return eth([]byte{0x33, 0x33, 0, 0, 0, 1}, srcMAC[:], 0x86dd, ip6(src, dst, next, l4))
}

func TestRealMACAnswerForIPv6(t *testing.T) {
	e := rmEngine(t, true)
	e.af = afIPv6
	e.myIP = "fe80::1"
	e.peers = map[string]*Peer{"fe80::3": {IP: "fe80::3", AfnID: 3, LastSeen: time.Now(), FirstSeen: time.Now()}}
	e.failoverPrimary = 3
	vip6 := [16]byte(net.ParseIP("2001:db8::9").To16())
	ns := append([]byte{135, 0, 0, 0, 0, 0, 0, 0}, vip6[:]...)
	solicit := eth([]byte{0x33, 0x33, 0xff, 0, 0, 9}, rmCli[:], 0x86dd, ip6("fe80::fe", "ff02::1:ff00:9", 58, ns))

	// any IPv6 frame from the node's address teaches its MAC (here a hello)
	out := e.nsAnswer(v6frame(rmPeer, "fe80::3", "ff02::1", 17, udp(1, 2, nil)), rmSelf, vip6)
	if out != nil || e.peerMACs["fe80::3"] == nil || e.peerMACs["fe80::3"].mac != rmPeer {
		t.Fatalf("not learned from an IPv6 frame: %v", e.peerMACs)
	}
	f := e.nsAnswer(solicit, rmSelf, vip6)
	if f == nil {
		t.Fatal("no answer to a solicitation of the VIP")
	}
	// Ethernet: to the solicitor, from this node; the target link-layer option: the other node's real MAC
	if [6]byte(f[0:6]) != rmCli || [6]byte(f[6:12]) != rmSelf {
		t.Fatalf("eth %x > %x", f[6:12], f[0:6])
	}
	if !bytes.Contains(f[14+40:], rmPeer[:]) || bytes.Contains(f[14+40:], rmSelf[:]) {
		t.Fatalf("the advertised MAC is not the node's: %x", f[14+40:])
	}
	// the virtual-MAC default is unchanged
	e2 := rmEngine(t, false)
	e2.af = afIPv6
	e2.myIP = "fe80::1"
	e2.failoverPrimary = 3
	e2.peers = e.peers
	f = e2.nsAnswer(solicit, rmSelf, vip6)
	vm3 := vmacBytes(1, 3)
	if f == nil || !bytes.Contains(f[14+40:], vm3[:]) {
		t.Fatalf("default mode did not advertise the virtual MAC")
	}
}

// The controller asks for the MACs it does not know, not too often, and refreshes the ones it does.
func TestControllerAsksForTheRealMACs(t *testing.T) {
	e := rmEngine(t, true)
	var mu sync.Mutex
	var sent [][]byte
	old := rawSend
	rawSend = func(iface string, f []byte) error {
		mu.Lock()
		sent = append(sent, append([]byte(nil), f...))
		mu.Unlock()
		return nil
	}
	defer func() { rawSend = old }()

	e.resolvePeerMACsLocked()
	mu.Lock()
	n := len(sent)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("%d requests for two nodes", n)
	}
	asked := map[string]bool{}
	for _, f := range sent {
		if f[12] != 8 || f[13] != 6 || f[21] != 1 || [6]byte(f[6:12]) != rmSelf || [4]byte(f[28:32]) != [4]byte{192, 0, 2, 1} {
			t.Fatalf("not an ARP request from this node: %x", f)
		}
		asked[netip.AddrFrom4([4]byte(f[38:42])).String()] = true
	}
	if !asked["192.0.2.2"] || !asked["192.0.2.3"] {
		t.Fatalf("asked %v", asked)
	}
	e.resolvePeerMACsLocked() // at once again: no
	mu.Lock()
	if len(sent) != 2 {
		t.Fatalf("asked again at once: %d", len(sent))
	}
	mu.Unlock()
	// one is learned: after the retry time only the other is asked again
	e.notePeerMACLocked("192.0.2.3", rmPeer)
	for _, en := range e.peerMACs {
		en.asked = time.Now().Add(-time.Minute)
	}
	e.peerMACs["192.0.2.3"].asked = time.Now()
	e.resolvePeerMACsLocked()
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 3 || netip.AddrFrom4([4]byte(sent[2][38:42])).String() != "192.0.2.2" {
		t.Fatalf("the retry: %d requests", len(sent))
	}
	// a forwarder does not ask, and the default mode never does
	e.state = stateForward
	e.peerMACs["192.0.2.2"].asked = time.Time{}
	e.resolvePeerMACsLocked()
	if len(sent) != 3 {
		t.Fatalf("a forwarder asked")
	}
	d := rmEngine(t, false)
	d.resolvePeerMACsLocked()
	if len(sent) != 3 {
		t.Fatalf("default mode asked")
	}
}

func TestControllerAsksForIPv6MACs(t *testing.T) {
	e := rmEngine(t, true)
	e.af = afIPv6
	e.myIP = "fe80::1"
	e.peers = map[string]*Peer{"fe80::3": {IP: "fe80::3", AfnID: 3, LastSeen: time.Now(), FirstSeen: time.Now()}}
	var got [][]byte
	old := rawSend
	rawSend = func(iface string, f []byte) error { got = append(got, f); return nil }
	defer func() { rawSend = old }()
	e.resolvePeerMACsLocked()
	if len(got) != 1 {
		t.Fatalf("%d solicitations", len(got))
	}
	f := got[0]
	if f[12] != 0x86 || f[13] != 0xdd || f[14+6] != 58 || f[14+40] != 135 || !bytes.Equal(f[14+40+8:14+40+24], net.ParseIP("fe80::3").To16()) {
		t.Fatalf("not a neighbor solicitation for fe80::3: %x", f)
	}
}

// In real-MAC mode no virtual MAC is created, the VIP is on lo on every node (the controller too), and the controller
// announces the VIP at its own MAC; taking a slot over means announcing it again, not creating anything.
func TestRealMACModeCreatesNoVirtualMACs(t *testing.T) {
	log := recordCmds(t)
	var mu sync.Mutex
	var frames [][]byte
	old := rawSend
	rawSend = func(iface string, f []byte) error {
		mu.Lock()
		frames = append(frames, append([]byte(nil), f...))
		mu.Unlock()
		return nil
	}
	defer func() { rawSend = old }()
	oldV := addVmacFn
	claimed := 0
	addVmacFn = func(string, int, int) bool { claimed++; return true }
	defer func() { addVmacFn = oldV }()

	e := rmEngine(t, true)
	e.state = stateForward
	e.setupVmacsLocked()
	e.state = stateActive
	e.setupVmacsLocked()
	e.takeoverAFNLocked(3)
	lines := log.snapshot()
	t.Logf("commands: %v", lines)
	if idx(lines, 0, "link add") >= 0 || idx(lines, 0, "macvlan-for") >= 0 || claimed != 0 || len(e.takeover) != 0 {
		t.Fatalf("a virtual MAC was set up: %v (claimed %d, takeover %v)", lines, claimed, e.takeover)
	}
	if idx(lines, 0, "addr replace", "192.0.2.9/32", "dev lo") < 0 {
		t.Fatalf("the VIP is not on lo: %v", lines)
	}
	if idx(lines, 0, "addr del", "dev lo") >= 0 {
		t.Fatalf("the controller took the VIP off lo: %v", lines)
	}
	if !e.dnsLoAdded {
		t.Fatal("not recorded as on lo")
	}
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	garp := 0
	for _, f := range frames {
		if len(f) == 42 && f[21] == 2 && [6]byte(f[22:28]) == rmSelf && [4]byte(f[28:32]) == [4]byte{192, 0, 2, 9} {
			garp++
		}
	}
	if garp < 2 { // on becoming the controller, and again for the slot taken over
		t.Fatalf("%d announcements of the VIP at this node's MAC among %d frames", garp, len(frames))
	}
	// stopping it puts the VIP off lo
	log.reset()
	e.teardownDNSLocked()
	if idx(log.snapshot(), 0, "addr del", "192.0.2.9/32", "dev lo") < 0 || e.dnsLoAdded {
		t.Fatalf("teardown: %v", log.snapshot())
	}
}

// The default is untouched: a controller still has the VIP on its macvlan and off lo, and creates the MACs.
func TestDefaultModeStillUsesVirtualMACs(t *testing.T) {
	log := recordCmds(t)
	oldV, oldC := addVmacFn, claimVmacFn
	vm := func(_ string, group, slot int) bool { runCmd("macvlan-for", vmacName(group, slot)); return true }
	addVmacFn, claimVmacFn = vm, vm
	defer func() { addVmacFn, claimVmacFn = oldV, oldC }()
	e := rmEngine(t, false)
	e.setupVmacsLocked()
	lines := log.snapshot()
	if idx(lines, 0, "macvlan-for", vmacName(1, 1)) < 0 || idx(lines, 0, "addr add", "dev "+vmacName(1, 1)) < 0 {
		t.Fatalf("the virtual-MAC setup did not happen: %v", lines)
	}
}

func TestRealMACSettingInConfigAndCluster(t *testing.T) {
	g := defaultGroup()
	g.GroupID, g.VIP4 = 1, "192.0.2.9/24"
	b, _ := json.Marshal(g)
	if strings.Contains(string(b), "real_macs") {
		t.Fatalf("the default is written to the file: %s", b)
	}
	g.RealMACs = true
	b, _ = json.Marshal(g)
	if !strings.Contains(string(b), `"real_macs":true`) {
		t.Fatalf("not written when on: %s", b)
	}
	// a change of it restarts the gateway; it is not a live setting
	a, c := defaultGroup(), defaultGroup()
	c.RealMACs = true
	if !restartDiffers(&a, &c) || liveDiffers(&a, &c) {
		t.Fatalf("restart %v live %v", restartDiffers(&a, &c), liveDiffers(&a, &c))
	}
	// shared by every node
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	sc := sharedOf(dc)
	if !sc.Groups[0].RealMACs {
		t.Fatal("not in the shared settings")
	}
	var gc GroupConfig
	applySharedGroup(&gc, sc.Groups[0])
	if !gc.RealMACs {
		t.Fatal("not applied from the shared settings")
	}
	sc.Groups[0].RealMACs = false
	b, _ = json.Marshal(sc.Groups[0])
	if strings.Contains(string(b), "real_macs") {
		t.Fatal("the shared form carries the default (the hash of a cluster that never used it would change)")
	}
}

// The Gateways page shows the real MAC in real-MAC mode and the virtual MAC otherwise.
func TestGatewayViewShowsTheRealMAC(t *testing.T) {
	rows := []SnapshotRow{
		{GroupID: 1, AF: "v4", PeerIP: "192.0.2.1", AfnID: 1, Local: true, State: "active", RealMACs: true, MAC: "02:00:00:c0:ff:ee", VIP4: "192.0.2.9/24"},
		{GroupID: 1, AF: "v4", PeerIP: "192.0.2.3", AfnID: 3, State: "forward", RealMACs: true, MAC: "02:00:00:aa:bb:03"},
		{GroupID: 1, AF: "v4", PeerIP: "192.0.2.2", AfnID: 2, State: "forward", RealMACs: true},
	}
	gs := buildGateways(rows)
	got := map[string]string{}
	for _, m := range gs[0].Members {
		got[m.IP] = m.VMAC
	}
	if got["192.0.2.1"] != "02:00:00:c0:ff:ee" || got["192.0.2.3"] != "02:00:00:aa:bb:03" || got["192.0.2.2"] != "" {
		t.Fatalf("%v", got)
	}
	rows2 := []SnapshotRow{{GroupID: 1, AF: "v4", PeerIP: "192.0.2.3", AfnID: 3, State: "forward"}}
	if m := buildGateways(rows2)[0].Members[0]; m.VMAC != vmacStr(1, 3) {
		t.Fatalf("default: %q", m.VMAC)
	}
}

func TestNewGatewaysStartWithRealMACs(t *testing.T) {
	if !newGatewayGroup().RealMACs || !newDaemonConfig().Groups[0].RealMACs {
		t.Fatal("a gateway created now must start with real MAC addresses")
	}
	// a group in a file that does not say it keeps the old way
	var g GroupConfig
	if err := json.Unmarshal([]byte(`{"group_id":1,"interface":"eth0","vip4":"10.0.0.1/24"}`), &g); err != nil || g.RealMACs {
		t.Fatalf("an existing group changed: %v %v", err, g.RealMACs)
	}
	var dc DaemonConfig
	if err := json.Unmarshal([]byte(`{"groups":[{"group_id":1,"interface":"eth0","vip4":"10.0.0.1/24"}]}`), &dc); err != nil || dc.Groups[0].RealMACs {
		t.Fatalf("an existing config changed: %v", err)
	}
}
