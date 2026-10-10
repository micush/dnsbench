package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Do virtual MACs work on this network?  A virtual MAC needs two things the network may not give: frames sent from it must
// leave the node, and frames addressed to it must arrive.  A VMware port group that is not promiscuous, a cloud network, a
// switch port that allows one MAC: any of them makes a gateway that looks fine (the router even holds the virtual MAC in
// its ARP table, because the announcements got out) unreachable, because the traffic addressed to the virtual MAC never
// comes back.  The test asks the router a question from a throwaway virtual-MAC interface and listens on that interface
// for the answer, which is a unicast frame to the virtual MAC, the same way client traffic comes:
//
//  1. Control: the same question from the real MAC.  A router that does not answer it proves nothing, so then the result is
//     "inconclusive", never "not delivered".
//  2. Test: the question from the virtual MAC.  No answer, with the control answered, is "not delivered".
//
// Nothing in a neighbour's cache is touched: the IPv4 question is an ARP probe (sender 0.0.0.0, RFC 5227) and the IPv6 one
// comes from a throwaway link-local address.  The test never changes a setting: it reports, and a person applies it.

// Verdicts.
const (
	vmacDelivered    = "delivered"
	vmacNotDelivered = "not-delivered"
	vmacInconclusive = "inconclusive"
	vmacOff          = "off" // real MAC addresses are already on for the gateway
)

// VmacFamily is the result for one address family.
type VmacFamily struct {
	AF        string `json:"af"`
	Responder string `json:"responder,omitempty"`
	Verdict   string `json:"verdict"`
	Detail    string `json:"detail"`
}

// VmacResult is the last test of one gateway on this node.
type VmacResult struct {
	GroupID  int          `json:"group_id"`
	Verdict  string       `json:"verdict"`
	Detail   string       `json:"detail"`
	At       int64        `json:"at"` // unix seconds
	Families []VmacFamily `json:"families,omitempty"`
}

var vmacResults = struct {
	sync.Mutex
	m map[int]VmacResult
}{m: map[int]VmacResult{}}

func vmacResultFor(gid int) (VmacResult, bool) {
	vmacResults.Lock()
	defer vmacResults.Unlock()
	r, ok := vmacResults.m[gid]
	return r, ok
}

func setVmacResult(r VmacResult) {
	vmacResults.Lock()
	vmacResults.m[r.GroupID] = r
	vmacResults.Unlock()
}

// vmacAutoOff stops the test that runs when a gateway starts (set by the unit tests, which must not create interfaces).
var vmacAutoOff atomic.Bool

// vmacTestMu lets one test run at a time: the throwaway interface has one name per gateway.
var vmacTestMu sync.Mutex

// timing: three tries of 700 ms for each question, so a lost frame does not decide the result.
var (
	vmacTries = 3
	vmacWait  = 700 * time.Millisecond
)

// testMAC is the MAC of the throwaway interface: the gateway's virtual-MAC pattern with a last byte the real slots
// (always 0) never have, so it cannot clash with one.
func vmacTestMAC(groupID int) [6]byte {
	m := vmacBytes(groupID, 0xfe)
	m[5] = 0x7e
	return m
}

func vmacTestName(groupID int) string { return fmt.Sprintf("ddgwt%d", groupID) }

// ── what to ask ──────────────────────────────────────────────────────────────

// File locations (replaceable in tests).
var (
	procNetRoute  = "/proc/net/route"
	procNetARP    = "/proc/net/arp"
	procNetRoute6 = "/proc/net/ipv6_route"
)

// parseRoute4 lists the default gateways of iface from /proc/net/route text.
func parseRoute4(text, iface string) []netip.Addr {
	var out []netip.Addr
	for i, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if i == 0 || len(f) < 8 || f[0] != iface || f[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&0x2 == 0 { // RTF_GATEWAY
			continue
		}
		v, err := strconv.ParseUint(f[2], 16, 32)
		if err != nil || v == 0 {
			continue
		}
		out = append(out, netip.AddrFrom4([4]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}))
	}
	return out
}

// parseARP lists the complete neighbour entries of iface from /proc/net/arp text.
func parseARP(text, iface string) []netip.Addr {
	var out []netip.Addr
	for i, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if i == 0 || len(f) < 6 || f[5] != iface || f[2] != "0x2" {
			continue
		}
		if a, err := netip.ParseAddr(f[0]); err == nil && a.Is4() {
			out = append(out, a)
		}
	}
	return out
}

// parseRoute6 lists the default gateways of iface from /proc/net/ipv6_route text.
func parseRoute6(text, iface string) []netip.Addr {
	var out []netip.Addr
	for _, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if len(f) < 10 || f[9] != iface || f[0] != strings.Repeat("0", 32) || f[1] != "00" {
			continue
		}
		flags, err := strconv.ParseUint(f[8], 16, 32)
		if err != nil || flags&0x2 == 0 || f[4] == strings.Repeat("0", 32) {
			continue
		}
		var b [16]byte
		ok := true
		for i := 0; i < 16; i++ {
			v, err := strconv.ParseUint(f[4][2*i:2*i+2], 16, 8)
			if err != nil {
				ok = false
				break
			}
			b[i] = byte(v)
		}
		if ok {
			out = append(out, netip.AddrFrom16(b))
		}
	}
	return out
}

func readFileFn(p string) string { b, _ := os.ReadFile(p); return string(b) }

// responders are the neighbours to ask on iface: the default gateways first, then other complete neighbours of the same
// family, never an address of this node or one of the gateway's own.
func vmacResponders(iface string, v6 bool, skip []netip.Addr) []netip.Addr {
	var l []netip.Addr
	if v6 {
		l = parseRoute6(readFileFn(procNetRoute6), iface)
	} else {
		l = append(parseRoute4(readFileFn(procNetRoute), iface), parseARP(readFileFn(procNetARP), iface)...)
	}
	seen := map[netip.Addr]bool{}
	for _, s := range skip {
		seen[s] = true
	}
	var out []netip.Addr
	for _, a := range l {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	if len(out) > 2 {
		out = out[:2]
	}
	return out
}

// ── the questions and the answers ────────────────────────────────────────────

// isARPReplyTo: an ARP reply from "from", Ethernet-addressed to dst.
func isARPReplyTo(f []byte, dst [6]byte, from [4]byte) bool {
	return len(f) >= 42 && bytes.Equal(f[0:6], dst[:]) && f[12] == 0x08 && f[13] == 0x06 &&
		f[20] == 0 && f[21] == 2 && bytes.Equal(f[28:32], from[:])
}

// isNAReplyTo: a neighbor advertisement from "from", Ethernet-addressed to dst.
func isNAReplyTo(f []byte, dst [6]byte, from [16]byte) bool {
	return len(f) >= 62 && bytes.Equal(f[0:6], dst[:]) && f[12] == 0x86 && f[13] == 0xdd &&
		f[20] == 58 && bytes.Equal(f[22:38], from[:]) && f[54] == 136
}

// eui64LinkLocal is the link-local address a MAC gives itself.
func eui64LinkLocal(m [6]byte) [16]byte {
	return [16]byte{0xfe, 0x80, 0, 0, 0, 0, 0, 0, m[0] ^ 0x02, m[1], m[2], 0xff, 0xfe, m[3], m[4], m[5]}
}

// linkLocalOf is the link-local address an interface holds (its EUI-64 one when it cannot be read).
func linkLocalOf(ifc *net.Interface) [16]byte {
	if addrs, err := ifc.Addrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() == nil && n.IP.IsLinkLocalUnicast() {
				var b [16]byte
				copy(b[:], n.IP.To16())
				return b
			}
		}
	}
	var m [6]byte
	copy(m[:], ifc.HardwareAddr)
	return eui64LinkLocal(m)
}

// ask sends frame on sendIface up to vmacTries times and reports whether match saw an answer on listenIface.
func vmacAsk(sendIface string, frame []byte, listenIface string, match func([]byte) bool) (bool, error) {
	hit := make(chan struct{}, 1)
	h, err := startCapture(listenIface, 512, func(_ time.Time, d []byte) {
		if match(d) {
			select {
			case hit <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		return false, err
	}
	defer h.stop()
	for i := 0; i < vmacTries; i++ {
		if err := rawSend(sendIface, frame); err != nil {
			return false, err
		}
		select {
		case <-hit:
			return true, nil
		case <-time.After(vmacWait):
		}
	}
	return false, nil
}

// judge turns the two answers into a verdict.
func vmacJudge(controlOK, testOK bool) (string, string) {
	switch {
	case !controlOK:
		return vmacInconclusive, "the router did not answer the control question from the real MAC, so nothing can be said"
	case testOK:
		return vmacDelivered, "the router's answer reached the virtual MAC"
	}
	return vmacNotDelivered, "the router answers the real MAC but its answer to the virtual MAC never arrived: the network drops frames to or from virtual MACs"
}

// combineVmac folds the families' results into the gateway's: any "not delivered" wins, then "delivered", else "inconclusive".
func combineVmac(fs []VmacFamily) (string, string) {
	if len(fs) == 0 {
		return vmacInconclusive, "the gateway has no address to test"
	}
	verdict := vmacInconclusive
	for _, f := range fs {
		switch {
		case f.Verdict == vmacNotDelivered:
			verdict = vmacNotDelivered
		case f.Verdict == vmacDelivered && verdict != vmacNotDelivered:
			verdict = vmacDelivered
		}
	}
	var parts []string
	for _, f := range fs {
		name := "IPv4"
		if f.AF == "v6" {
			name = "IPv6"
		}
		parts = append(parts, name+": "+f.Detail)
	}
	return verdict, strings.Join(parts, "; ")
}

// testFamily asks the router(s) of one family; parent is the gateway's interface, test the throwaway one.
func vmacTestFamily(parent, test string, v6 bool, skip []netip.Addr) VmacFamily {
	f := VmacFamily{AF: "v4"}
	if v6 {
		f.AF = "v6"
	}
	rs := vmacResponders(parent, v6, skip)
	if len(rs) == 0 {
		f.Verdict, f.Detail = vmacInconclusive, "no router or neighbour found on "+parent+" to ask"
		return f
	}
	pif, err := net.InterfaceByName(parent)
	if err != nil {
		f.Verdict, f.Detail = vmacInconclusive, err.Error()
		return f
	}
	real := ifaceMAC(parent)
	tmac := ifaceMAC(test)
	best := VmacFamily{AF: f.AF, Verdict: vmacInconclusive}
	for _, r := range rs {
		var frameReal, frameTest []byte
		var matchReal, matchTest func([]byte) bool
		if v6 {
			t := r.As16()
			frameReal = buildNSFor(real, linkLocalOf(pif), t)
			frameTest = buildNSFor(tmac, eui64LinkLocal(tmac), t)
			matchReal = func(d []byte) bool { return isNAReplyTo(d, real, t) }
			matchTest = func(d []byte) bool { return isNAReplyTo(d, tmac, t) }
		} else {
			t := r.As4()
			frameReal, frameTest = buildARPProbe(real, t), buildARPProbe(tmac, t)
			matchReal = func(d []byte) bool { return isARPReplyTo(d, real, t) }
			matchTest = func(d []byte) bool { return isARPReplyTo(d, tmac, t) }
		}
		ctl, err := vmacAsk(parent, frameReal, parent, matchReal)
		if err != nil {
			f.Verdict, f.Detail = vmacInconclusive, "cannot send or listen: "+err.Error()
			return f
		}
		tst := false
		if ctl {
			if tst, err = vmacAsk(test, frameTest, test, matchTest); err != nil {
				f.Verdict, f.Detail = vmacInconclusive, "cannot send or listen: "+err.Error()
				return f
			}
		}
		v, d := vmacJudge(ctl, tst)
		cur := VmacFamily{AF: f.AF, Responder: r.String(), Verdict: v, Detail: d + " (" + r.String() + ")"}
		if rank := map[string]int{vmacInconclusive: 0, vmacDelivered: 1, vmacNotDelivered: 2}; rank[v] > rank[best.Verdict] || best.Responder == "" {
			best = cur
		}
		if v == vmacDelivered {
			break // one router that gets it through is enough
		}
	}
	return best
}

// VmacTest runs the test for one gateway of this node and keeps the result.
func vmacTestGroup(gc GroupConfig) VmacResult {
	vmacTestMu.Lock()
	defer vmacTestMu.Unlock()
	res := VmacResult{GroupID: gc.GroupID, At: time.Now().Unix()}
	finish := func(v, d string) VmacResult {
		res.Verdict, res.Detail = v, d
		setVmacResult(res)
		return res
	}
	if gc.RealMACs {
		return finish(vmacOff, "real MAC addresses are on for this gateway: there are no virtual MACs to test")
	}
	if _, err := net.InterfaceByName(gc.Interface); err != nil {
		return finish(vmacInconclusive, "the interface "+gc.Interface+" is not on this node")
	}
	name, mac := vmacTestName(gc.GroupID), vmacTestMAC(gc.GroupID)
	runCmd("ip", "link", "del", name) // a leftover of a test that was cut short
	if !runCmd("ip", "link", "add", "link", gc.Interface, "name", name,
		"address", fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5]), "type", "macvlan", "mode", "bridge") {
		return finish(vmacInconclusive, "could not create a test interface on "+gc.Interface+" (needs root and the ip command)")
	}
	defer runCmd("ip", "link", "del", name)
	setSysctl("/proc/sys/net/ipv6/conf/"+name+"/disable_ipv6", "1") // no router solicitations or MLD reports of its own
	runCmd("ip", "link", "set", name, "up")
	for i := 0; i < 20; i++ { // the interface comes up in a moment
		if ifc, err := net.InterfaceByName(name); err == nil && ifc.Flags&net.FlagUp != 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	var skip []netip.Addr
	if ifc, err := net.InterfaceByName(gc.Interface); err == nil {
		if addrs, err := ifc.Addrs(); err == nil {
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok {
					if ip, ok := netip.AddrFromSlice(n.IP); ok {
						skip = append(skip, ip.Unmap())
					}
				}
			}
		}
	}
	for _, v := range []string{gc.VIP4, gc.VIP6} {
		if p, err := netip.ParsePrefix(v); err == nil {
			skip = append(skip, p.Addr())
		}
	}
	if gc.VIP4 != "" {
		res.Families = append(res.Families, vmacTestFamily(gc.Interface, name, false, skip))
	}
	if gc.VIP6 != "" {
		res.Families = append(res.Families, vmacTestFamily(gc.Interface, name, true, skip))
	}
	sort.SliceStable(res.Families, func(i, j int) bool { return res.Families[i].AF < res.Families[j].AF })
	v, d := combineVmac(res.Families)
	if v == vmacNotDelivered {
		infof("Group %d: virtual MACs are not delivered on %s (%s)", gc.GroupID, gc.Interface, d)
	}
	return finish(v, d)
}

// errNoGroup is for a group number that does not exist.
var errNoGroup = errors.New("there is no gateway with that group number (see --canvas)")

// VmacTest runs the test for one gateway (group > 0) or all of them on this node.
func (m *Mgmt) VmacTest(group int) ([]VmacResult, error) {
	dc, _, err := m.LiveConfig()
	if err != nil {
		return nil, err
	}
	var out []VmacResult
	for _, g := range dc.Groups {
		if group != 0 && g.GroupID != group {
			continue
		}
		out = append(out, vmacTestGroup(g))
	}
	if len(out) == 0 {
		if group != 0 {
			return nil, errNoGroup
		}
		return nil, errors.New("there is no gateway to test")
	}
	return out, nil
}

// markVmac puts the last test result on each gateway of the picture; a "not delivered" makes a healthy gateway degraded,
// because clients cannot reach it through this node.
func markVmac(groups []CanvasGateway, real map[int]bool) {
	for i := range groups {
		if real[groups[i].GroupID] { // the result belongs to the old mode: with real MACs on there is nothing to warn about
			continue
		}
		r, ok := vmacResultFor(groups[i].GroupID)
		if !ok || r.Verdict == vmacOff {
			continue
		}
		rr := r
		groups[i].Vmac = &rr
		if r.Verdict == vmacNotDelivered && !groups[i].Paused && (groups[i].Status == "ok") {
			groups[i].Status = "warn"
			groups[i].Detail = "Virtual MACs: NOT delivered on this network — " + r.Detail + ". Use real MAC addresses for this gateway."
		}
	}
}

// vmacAtStart tests a gateway when it starts here, in the background: it never holds the start up.
func vmacAtStart(gc GroupConfig) {
	if vmacAutoOff.Load() || gc.RealMACs || gc.Paused {
		return
	}
	go func() {
		time.Sleep(3 * time.Second) // let the interface settle
		vmacTestGroup(gc)
	}()
}
