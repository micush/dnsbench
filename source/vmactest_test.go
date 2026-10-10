package main

import (
	"net/netip"
	"strings"
	"testing"
)

func TestParseVmacRoutes(t *testing.T) {
	r4 := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n" +
		"eth0\t00000000\t0100A8C0\t0003\t0\t0\t100\t00000000\n" +
		"eth0\t0000A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\n" +
		"eth1\t00000000\t0100000A\t0003\t0\t0\t100\t00000000\n"
	if g := parseRoute4(r4, "eth0"); len(g) != 1 || g[0] != netip.MustParseAddr("192.168.0.1") {
		t.Fatalf("route4 = %v", g)
	}
	arp := "IP address       HW type     Flags       HW address            Mask     Device\n" +
		"192.168.0.1      0x1         0x2         00:11:22:33:44:55     *        eth0\n" +
		"192.168.0.9      0x1         0x0         00:00:00:00:00:00     *        eth0\n" +
		"10.0.0.1         0x1         0x2         00:11:22:33:44:66     *        eth1\n"
	if g := parseARP(arp, "eth0"); len(g) != 1 || g[0] != netip.MustParseAddr("192.168.0.1") {
		t.Fatalf("arp = %v", g)
	}
	z := strings.Repeat("0", 32)
	r6 := z + " 00 " + z + " 00 fe800000000000000000000000000001 00000064 00000001 00000000 00000003 eth0\n" +
		z + " 00 " + z + " 00 " + z + " ffffffff 00000001 00000000 00200200 lo\n"
	if g := parseRoute6(r6, "eth0"); len(g) != 1 || g[0] != netip.MustParseAddr("fe80::1") {
		t.Fatalf("route6 = %v", g)
	}
}

func TestVmacReplyMatchers(t *testing.T) {
	dst := [6]byte{2, 0, 0, 0, 0, 0x7e}
	arp := make([]byte, 42)
	copy(arp[0:6], dst[:])
	arp[12], arp[13], arp[21] = 0x08, 0x06, 2
	copy(arp[28:32], []byte{192, 168, 0, 1})
	if !isARPReplyTo(arp, dst, [4]byte{192, 168, 0, 1}) {
		t.Fatal("a reply to the test MAC must match")
	}
	if isARPReplyTo(arp, [6]byte{1}, [4]byte{192, 168, 0, 1}) || isARPReplyTo(arp, dst, [4]byte{192, 168, 0, 2}) || isARPReplyTo(arp[:30], dst, [4]byte{192, 168, 0, 1}) {
		t.Fatal("another MAC, another sender or a short frame must not match")
	}
	arp[21] = 1 // a request
	if isARPReplyTo(arp, dst, [4]byte{192, 168, 0, 1}) {
		t.Fatal("a request is not a reply")
	}
	from := [16]byte{0xfe, 0x80, 15: 1}
	na := make([]byte, 86)
	copy(na[0:6], dst[:])
	na[12], na[13], na[20], na[54] = 0x86, 0xdd, 58, 136
	copy(na[22:38], from[:])
	if !isNAReplyTo(na, dst, from) {
		t.Fatal("an advertisement to the test MAC must match")
	}
	na[54] = 135
	if isNAReplyTo(na, dst, from) {
		t.Fatal("a solicitation is not an advertisement")
	}
	if ll := eui64LinkLocal([6]byte{2, 0, 0, 1, 2, 3}); ll[8] != 0 || ll[11] != 0xff || ll[12] != 0xfe || ll[15] != 3 {
		t.Fatalf("eui64 = %x", ll)
	}
}

func TestVmacVerdicts(t *testing.T) {
	for _, c := range []struct {
		control, test bool
		want          string
	}{{false, false, vmacInconclusive}, {false, true, vmacInconclusive}, {true, true, vmacDelivered}, {true, false, vmacNotDelivered}} {
		if v, d := vmacJudge(c.control, c.test); v != c.want || d == "" {
			t.Errorf("judge(%v,%v) = %s", c.control, c.test, v)
		}
	}
	f := func(af, v string) VmacFamily { return VmacFamily{AF: af, Verdict: v, Detail: v} }
	if v, _ := combineVmac(nil); v != vmacInconclusive {
		t.Error("nothing to test is inconclusive")
	}
	if v, d := combineVmac([]VmacFamily{f("v4", vmacDelivered), f("v6", vmacNotDelivered)}); v != vmacNotDelivered || !strings.Contains(d, "IPv6") {
		t.Errorf("combine = %s %s", v, d)
	}
	if v, _ := combineVmac([]VmacFamily{f("v4", vmacDelivered), f("v6", vmacInconclusive)}); v != vmacDelivered {
		t.Error("one delivered family and one unknown is delivered")
	}
}

func TestMarkVmacDegradesOnlyAHealthyGateway(t *testing.T) {
	setVmacResult(VmacResult{GroupID: 71, Verdict: vmacNotDelivered, Detail: "x"})
	setVmacResult(VmacResult{GroupID: 72, Verdict: vmacDelivered, Detail: "y"})
	setVmacResult(VmacResult{GroupID: 73, Verdict: vmacOff})
	gs := []CanvasGateway{{GroupID: 71, Status: "ok"}, {GroupID: 72, Status: "ok"}, {GroupID: 73, Status: "ok"}, {GroupID: 71, Status: "bad", Detail: "down"}, {GroupID: 74, Status: "ok"}}
	markVmac(gs, nil)
	if gs[0].Status != "warn" || !strings.Contains(gs[0].Detail, "real MAC") || gs[0].Vmac == nil {
		t.Fatalf("not delivered must make a healthy gateway amber: %+v", gs[0])
	}
	if gs[1].Status != "ok" || gs[1].Vmac == nil || gs[2].Vmac != nil || gs[4].Vmac != nil {
		t.Fatal("delivered keeps it green; off and untested show nothing")
	}
	if gs[3].Status != "bad" || gs[3].Detail != "down" {
		t.Fatal("a gateway that is already bad stays as it is")
	}
}

func TestCanvasSetRealMACs(t *testing.T) {
	dc := &DaemonConfig{Groups: []GroupConfig{{GroupID: 1, Interface: "eth0", VIP4: "10.0.0.1/24"}}}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: 1, RealMACs: "on"}); err != nil || !dc.Groups[0].RealMACs {
		t.Fatalf("on: %v %v", err, dc.Groups[0].RealMACs)
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: 1, RealMACs: "off"}); err != nil || dc.Groups[0].RealMACs {
		t.Fatalf("off: %v", err)
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: 1, RealMACs: "maybe"}); err == nil {
		t.Fatal("bad value must be refused")
	}
}

func TestMarkVmacIgnoresTheResultOnceRealMACsAreOn(t *testing.T) {
	setVmacResult(VmacResult{GroupID: 75, Verdict: vmacNotDelivered, Detail: "x"})
	gs := []CanvasGateway{{GroupID: 75, Status: "ok", Detail: "up"}}
	markVmac(gs, map[int]bool{75: true})
	if gs[0].Status != "ok" || gs[0].Vmac != nil || gs[0].Detail != "up" {
		t.Fatalf("a stale not-delivered must not keep the gateway amber: %+v", gs[0])
	}
}

// An anycast address on two gateways is listed once, as its best gateway has it: a paused gateway listed first must not
// hide that the other one announces it.
func TestAllAnycastStatesSharedAddress(t *testing.T) {
	resetAnycast()
	t.Cleanup(resetAnycast)
	s := &Supervisor{anycast: map[int]*anycastSet{}, dc: &DaemonConfig{Groups: []GroupConfig{
		{GroupID: 1, Paused: true, ExtraVIPs: []string{"10.250.250.250"}},
		{GroupID: 2, ExtraVIPs: []string{"10.250.250.250", "10.250.250.251"}},
	}}}
	a := newAnycastSet(2, []string{"10.250.250.250"}, func() *Pool { return nil }, func() int { return 0 })
	s.anycast[2] = a
	anycastReg.Lock()
	anycastReg.claims["10.250.250.250"] = map[int]*anycastClaim{2: {want: true}}
	anycastReg.onLo["10.250.250.250"] = true
	anycastReg.Unlock()
	got := map[string]AnycastState{}
	for _, st := range s.AllAnycastStates() {
		got[st.Addr] = st
	}
	if len(got) != 2 || !got["10.250.250.250"].Up || got["10.250.250.250"].Reason == "gateway paused" {
		t.Fatalf("the announced address is shown as the paused gateway has it: %+v", got)
	}
	if st := got["10.250.250.251"]; st.Up || st.Reason == "" {
		t.Fatalf("an address nobody announces: %+v", st)
	}
}

// A gateway this node was removed from does not list its anycast addresses: they are not this node's.
func TestAllAnycastStatesSkipsAGatewayThisNodeWasRemovedFrom(t *testing.T) {
	resetAnycast()
	t.Cleanup(resetAnycast)
	s := &Supervisor{anycast: map[int]*anycastSet{}, dc: &DaemonConfig{Groups: []GroupConfig{
		{GroupID: 1, Paused: true, ExcludedHere: true, ExtraVIPs: []string{"10.250.250.250", "fddd::250"}},
		{GroupID: 2, Paused: true, ExtraVIPs: []string{"10.250.251.1"}},
	}}}
	got := s.AllAnycastStates()
	if len(got) != 1 || got[0].Addr != "10.250.251.1" {
		t.Fatalf("addresses of a gateway this node is not part of are listed: %+v", got)
	}
}
