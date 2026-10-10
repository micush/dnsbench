package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// ── logging helpers (printf style, level controlled at runtime) ─────────────

var logLevel = new(slog.LevelVar)

func setupLogging(level string) {
	logLevel.Set(parseLevel(level))
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warning", "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

func debugf(f string, a ...any) { slog.Debug(fmt.Sprintf(f, a...)) }
func infof(f string, a ...any)  { slog.Info(fmt.Sprintf(f, a...)) }
func warnf(f string, a ...any)  { slog.Warn(fmt.Sprintf(f, a...)) }
func errorf(f string, a ...any) { slog.Error(fmt.Sprintf(f, a...)) }

// ── interface helpers ────────────────────────────────────────────────────────

// ifaceIP4 returns the primary IPv4 address of an interface, or "0.0.0.0".
func ifaceIP4(name string) string {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return "0.0.0.0"
	}
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			return n.IP.To4().String()
		}
	}
	return "0.0.0.0"
}

// ifaceIP6 returns the first non-loopback IPv6 address on the interface,
// preferring link-local, or "::" on failure.  Uses /proc/net/if_inet6:
// addr32hex ifindex prefix_len scope flags ifname
func ifaceIP6(name string) string {
	b, err := os.ReadFile("/proc/net/if_inet6")
	if err != nil {
		return "::"
	}
	var ll, anyAddr string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || f[5] != name || len(f[0]) != 32 {
			continue
		}
		scope, err := strconv.ParseUint(f[3], 16, 8)
		if err != nil || scope == 0xFF {
			continue
		}
		var raw [16]byte
		ok := true
		for i := 0; i < 16; i++ {
			v, err := strconv.ParseUint(f[0][i*2:i*2+2], 16, 8)
			if err != nil {
				ok = false
				break
			}
			raw[i] = byte(v)
		}
		if !ok {
			continue
		}
		addr := netip.AddrFrom16(raw).String()
		if scope == 0x20 {
			ll = addr
		} else if anyAddr == "" {
			anyAddr = addr
		}
	}
	switch {
	case ll != "":
		return ll
	case anyAddr != "":
		return anyAddr
	}
	return "::"
}

func ifaceIP(name string, af AF) (string, bool) {
	if af == afIPv4 {
		ip := ifaceIP4(name)
		return ip, ip != "0.0.0.0"
	}
	ip := ifaceIP6(name)
	return ip, ip != "::"
}

// ── virtual MACs ─────────────────────────────────────────────────────────────

func vmacBytes(groupID, afnID int) [6]byte {
	return [6]byte{dgwOUI[0], dgwOUI[1], dgwOUI[2], byte(groupID), byte(afnID), 0x00}
}

func vmacStr(groupID, afnID int) string {
	b := vmacBytes(groupID, afnID)
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
}

func vmacName(groupID, afnID int) string { return fmt.Sprintf("ddgw%d.%d", groupID, afnID) }

// cmdHook lets a test see, and answer for, the commands the daemon would run (nil in production).
var cmdHook atomic.Pointer[func(name string, args []string) bool]

// runCmd runs a command without a shell; failures are only logged at debug.
func runCmd(name string, args ...string) bool {
	if h := cmdHook.Load(); h != nil {
		return (*h)(name, args)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		debugf("cmd failed: %s %s — %v %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		return false
	}
	return true
}

// vmacCurrent reports whether the group's macvlan for this slot already exists with the right MAC on the
// right parent interface, so it can be kept as it is.
func vmacCurrent(iface string, groupID, afnID int) bool {
	name := vmacName(groupID, afnID)
	ifc, err := net.InterfaceByName(name)
	if err != nil || ifc.HardwareAddr.String() != vmacStr(groupID, afnID) {
		return false
	}
	parent, err := net.InterfaceByName(iface)
	if err != nil {
		return false
	}
	b, err := os.ReadFile("/sys/class/net/" + name + "/iflink") // the ifindex of the link it sits on
	return err == nil && strings.TrimSpace(string(b)) == strconv.Itoa(parent.Index)
}

// addVmac makes sure the slot's macvlan exists.  The v4 and v6 engines of a group share it, and both call this when
// the node becomes the controller a moment apart: deleting and re-creating it each time would throw away the
// addresses the first one had just put on it (the IPv4 gateway address vanished that way and the node stopped
// answering for it), so one that is already right is only brought up.
func addVmac(iface string, groupID, afnID int) bool {
	name := vmacName(groupID, afnID)
	if vmacCurrent(iface, groupID, afnID) {
		runCmd("ip", "link", "set", name, "up")
		return true
	}
	runCmd("ip", "link", "del", name)
	ok := runCmd("ip", "link", "add", "link", iface, "name", name,
		"address", vmacStr(groupID, afnID), "type", "macvlan", "mode", "bridge")
	if ok {
		runCmd("ip", "link", "set", name, "up")
	}
	return ok
}

func delVmac(groupID, afnID int) { runCmd("ip", "link", "del", vmacName(groupID, afnID)) }

// vipRouteMetric is the metric of the on-link route the VIP's macvlan gets.  It
// is deliberately worse than any route the parent interface has for the same
// subnet (DHCP/NetworkManager use 100, static configuration 0).
const vipRouteMetric = "1024"

// delVIP takes the VIP (and the route addVIP added for it) off the slot's macvlan, for an engine that is no longer
// the controller while the other family's engine still uses the same macvlan.
func delVIP(groupID, afnID int, vip string) {
	dev := vmacName(groupID, afnID)
	runCmd("ip", "addr", "del", vip, "dev", dev)
	p, err := netip.ParsePrefix(vip)
	if err != nil {
		return
	}
	fam := "-4"
	if p.Addr().Is6() {
		fam = "-6"
	}
	runCmd("ip", fam, "route", "del", p.Masked().String(), "dev", dev, "metric", vipRouteMetric)
}

// addVIP puts the VIP on the group's macvlan WITHOUT the automatic connected
// route.  With the route the kernel adds, the macvlan competes with the parent
// interface for every packet this node sends into the subnet — the node's own
// traffic (cluster, DNS to servers, ping) then leaves from the VIP address, and
// a peer that holds the VIP on its loopback takes the reply for itself, so the
// two nodes could no longer reach each other.  The route is added back at a
// metric that only matters when the parent has no address in the subnet.
func addVIP(groupID, afnID int, vip string) {
	dev := vmacName(groupID, afnID)
	runCmd("ip", "addr", "del", vip, "dev", dev) // so the flags below apply to an existing address too
	if !runCmd("ip", "addr", "add", vip, "dev", dev, "noprefixroute") {
		runCmd("ip", "addr", "replace", vip, "dev", dev) // an ip(8) without noprefixroute
		return
	}
	p, err := netip.ParsePrefix(vip)
	if err != nil {
		return
	}
	fam := "-4"
	if p.Addr().Is6() {
		fam = "-6"
	}
	runCmd("ip", fam, "route", "replace", p.Masked().String(), "dev", dev, "metric", vipRouteMetric)
}

// hostCIDR turns "10.0.0.1/24" into "10.0.0.1/32" (or /128 for IPv6).
func hostCIDR(vip string) string {
	p, err := netip.ParsePrefix(vip)
	if err != nil {
		return vip
	}
	return netip.PrefixFrom(p.Addr(), p.Addr().BitLen()).String()
}

func vipAddr(vip string) (netip.Addr, error) {
	p, err := netip.ParsePrefix(vip)
	if err != nil {
		return netip.Addr{}, err
	}
	return p.Addr(), nil
}

// procIPv4Conf is where the per-interface IPv4 settings live (replaceable in tests).
var procIPv4Conf = "/proc/sys/net/ipv4/conf/"

// ensureSysctl sets a kernel setting and reads it back; false means it did not take (a container that may not write
// sysctls, say), which the caller reports.
func ensureSysctl(path, val string) bool {
	setSysctl(path, val)
	b, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(b)) == val
}

// relaxReversePath makes reverse-path filtering loose (2) wherever it is strict (1) for a forwarder's virtual-MAC
// interface: the interface itself, its parent and the global setting.  The kernel uses the larger of the
// interface's value and the global one, so 2 on the interface alone is enough, and the other two are changed
// only because an administrator reading `sysctl` would otherwise find a strict value that looks like the cause.
// It returns what it changed ("all", "eth0", …) and what it could not.
func relaxReversePath(dev, parent string) (changed, failed []string) {
	for _, name := range []string{dev, parent, "all"} {
		path := procIPv4Conf + name + "/rp_filter"
		b, err := os.ReadFile(path)
		if err != nil || strings.TrimSpace(string(b)) != "1" {
			continue
		}
		if ensureSysctl(path, "2") {
			changed = append(changed, name)
		} else {
			failed = append(failed, name)
		}
	}
	return
}

// linkLocalFor is the address a forwarder's virtual-MAC interface carries (see prepareV4Input): link-local, unique
// per group and slot, and never used for anything else.
func linkLocalFor(groupID, afnID int) string {
	return fmt.Sprintf("169.254.%d.%d/32", 1+(groupID-1+200)%200, 1+(afnID-1+253)%253)
}

// prepareV4Input makes the interface of a virtual MAC accept the queries that arrive on it.  The kernel's
// reverse-path check has a rule that bites here: an interface with no IPv4 address fails it whatever its rp_filter
// is (0 aside, and the global value counts too), and a forwarder's interface has none, because the VIP is on lo and
// only the controller holds it on the interface.  So a forwarder's interface gets a link-local /32 (no route, never
// advertised: arp_ignore is on), and strict rp_filter is made loose (see relaxReversePath).
func prepareV4Input(groupID, afnID int, forwarder bool, parent string) (changed, failed []string) {
	dev := vmacName(groupID, afnID)
	if forwarder {
		runCmd("ip", "addr", "replace", linkLocalFor(groupID, afnID), "dev", dev, "noprefixroute")
	}
	return relaxReversePath(dev, parent)
}

func setSysctl(path, val string) {
	if err := os.WriteFile(path, []byte(val), 0o644); err != nil {
		debugf("sysctl %s=%s failed: %v", path, val, err)
	}
}

// ifaceMAC is the hardware address of an interface (zero if it cannot be read).
func ifaceMAC(iface string) (m [6]byte) {
	if ifc, err := net.InterfaceByName(iface); err == nil && len(ifc.HardwareAddr) == 6 {
		copy(m[:], ifc.HardwareAddr)
	}
	return
}

// ── raw frames ───────────────────────────────────────────────────────────────

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// openPacketSocket opens an AF_PACKET socket bound to iface.  proto is the
// EtherType to receive (0 = send-only).  recvTimeout > 0 sets SO_RCVTIMEO.
func openPacketSocket(iface string, proto uint16, recvTimeout time.Duration) (int, error) {
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return -1, err
	}
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, int(htons(proto)))
	if err != nil {
		return -1, err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrLinklayer{Protocol: htons(proto), Ifindex: ifc.Index}); err != nil {
		syscall.Close(fd)
		return -1, err
	}
	if recvTimeout > 0 {
		tv := syscall.NsecToTimeval(recvTimeout.Nanoseconds())
		if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
			syscall.Close(fd)
			return -1, err
		}
	}
	return fd, nil
}

func sendRawFrame(iface string, frame []byte) error {
	fd, err := openPacketSocket(iface, 0, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	_, err = syscall.Write(fd, frame)
	return err
}

// buildARPProbe is an ARP probe (RFC 5227): broadcast, sender address 0.0.0.0, so
// no host updates its cache from it, but the Ethernet source is the virtual MAC —
// which is what makes a switch or bridge learn that the MAC now sits on this port.
func buildARPProbe(mac [6]byte, target [4]byte) []byte {
	bcast := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	f := append([]byte{}, bcast...)
	f = append(f, mac[:]...)
	f = append(f, 0x08, 0x06)
	f = append(f, 0x00, 0x01, 0x08, 0x00, 6, 4, 0x00, 0x01) // Ethernet/IPv4, request
	f = append(f, mac[:]...)
	f = append(f, 0, 0, 0, 0)
	f = append(f, make([]byte, 6)...)
	f = append(f, target[:]...)
	return f
}

// buildDADNS is an IPv6 duplicate-address-detection Neighbor Solicitation for
// target: source ::, so no host updates a neighbour entry from it, with the
// virtual MAC as Ethernet source (see buildARPProbe).
func buildDADNS(mac [6]byte, target [16]byte) []byte {
	var src [16]byte // ::
	dst := [16]byte{0xff, 0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0xff, target[13], target[14], target[15]}
	body := []byte{135, 0, 0, 0, 0, 0, 0, 0}
	body = append(body, target[:]...)
	ck := icmp6Checksum(src, dst, body)
	body[2], body[3] = byte(ck>>8), byte(ck)
	f := []byte{0x33, 0x33, 0xff, target[13], target[14], target[15]}
	f = append(f, mac[:]...)
	f = append(f, 0x86, 0xdd)
	f = append(f, 0x60, 0, 0, 0, byte(len(body)>>8), byte(len(body)), 58, 255)
	f = append(f, src[:]...)
	f = append(f, dst[:]...)
	return append(f, body...)
}

// announceVmac makes the network learn that a virtual MAC is now on this node's
// port.  A switch (or a Linux bridge under a hypervisor) keeps a MAC on the port
// it was last seen on until something transmits from it elsewhere — so when a
// node covers for a departed one, or a returning node brings its MAC back, the
// traffic for that MAC would go on to the old port and be lost until the entry
// aged out or the first reply came from the new owner.  It changes no neighbour
// cache on any host.
func announceVmac(iface string, groupID, afnID int, vip string) {
	ip, err := vipAddr(vip)
	if err != nil {
		return
	}
	mac := vmacBytes(groupID, afnID)
	var frame []byte
	if ip.Is4() {
		frame = buildARPProbe(mac, ip.As4())
	} else {
		frame = buildDADNS(mac, ip.As16())
	}
	for i := 0; i < 2; i++ { // a lost frame would cost the whole benefit
		if err := sendRawFrame(iface, frame); err != nil {
			warnf("announcing virtual MAC %s failed: %v", vmacStr(groupID, afnID), err)
			return
		}
	}
}

func sendGratuitousARP(iface string, groupID, afnID int, vip4 string) {
	sendGratuitousARPMAC(iface, vmacBytes(groupID, afnID), vip4)
}

// sendGratuitousARPMAC broadcasts "vip4 is at mac" (a gratuitous ARP reply).
func sendGratuitousARPMAC(iface string, mac [6]byte, vip4 string) {
	ip, err := vipAddr(vip4)
	if err != nil || !ip.Is4() {
		return
	}
	ip4 := ip.As4()
	bcast := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	frame := append([]byte{}, bcast...)
	frame = append(frame, mac[:]...)
	frame = append(frame, 0x08, 0x06)
	frame = append(frame, 0x00, 0x01, 0x08, 0x00, 6, 4, 0x00, 0x02) // Ethernet/IPv4, reply
	frame = append(frame, mac[:]...)
	frame = append(frame, ip4[:]...)
	frame = append(frame, bcast...)
	frame = append(frame, ip4[:]...)
	if err := rawSend(iface, frame); err != nil {
		warnf("gratuitous ARP failed: %v", err)
	}
}

// icmp6Checksum computes the ICMPv6 checksum over the pseudo header + body.
func icmp6Checksum(src, dst [16]byte, body []byte) uint16 {
	raw := make([]byte, 0, 40+len(body)+1)
	raw = append(raw, src[:]...)
	raw = append(raw, dst[:]...)
	l := len(body)
	raw = append(raw, byte(l>>24), byte(l>>16), byte(l>>8), byte(l))
	raw = append(raw, 0, 0, 0, 58)
	raw = append(raw, body...)
	if len(raw)%2 == 1 {
		raw = append(raw, 0)
	}
	var s uint32
	for i := 0; i < len(raw); i += 2 {
		s += uint32(raw[i])<<8 | uint32(raw[i+1])
	}
	s = (s >> 16) + (s & 0xFFFF)
	s += s >> 16
	return ^uint16(s)
}

// buildNA builds a full Ethernet+IPv6+ICMPv6 Neighbor Advertisement for target
// (the VIP), advertising vmac, from src → dst, flags = NA flag word.  ethSrc is the Ethernet source: the
// advertised MAC itself for an NA sent from its own interface, but the sender's own MAC for an answer on behalf
// of a slot (see ifaceMAC).
func buildNA(ethDst, ethSrc, vmac [6]byte, target, dst [16]byte, naFlags uint32) []byte {
	body := []byte{136, 0, 0, 0,
		byte(naFlags >> 24), byte(naFlags >> 16), byte(naFlags >> 8), byte(naFlags)}
	body = append(body, target[:]...)
	body = append(body, 2, 1) // option: target link-layer address
	body = append(body, vmac[:]...)
	ck := icmp6Checksum(target, dst, body)
	body[2], body[3] = byte(ck>>8), byte(ck)

	frame := make([]byte, 0, 14+40+len(body))
	frame = append(frame, ethDst[:]...)
	frame = append(frame, ethSrc[:]...)
	frame = append(frame, 0x86, 0xdd)
	frame = append(frame, 0x60, 0, 0, 0, byte(len(body)>>8), byte(len(body)), 58, 255)
	frame = append(frame, target[:]...)
	frame = append(frame, dst[:]...)
	frame = append(frame, body...)
	return frame
}

func sendUnsolicitedNA(iface string, groupID, afnID int, vip6 string) {
	sendUnsolicitedNAMAC(iface, vmacBytes(groupID, afnID), vip6)
}

// sendUnsolicitedNAMAC announces "vip6 is at vm" to all nodes (an unsolicited neighbor advertisement).
func sendUnsolicitedNAMAC(iface string, vm [6]byte, vip6 string) {
	ip, err := vipAddr(vip6)
	if err != nil || !ip.Is6() {
		return
	}
	allNodes := netip.MustParseAddr("ff02::1").As16()
	// Override=1, Solicited=0; Ethernet dst = IPv6 all-nodes multicast MAC.
	frame := buildNA([6]byte{0x33, 0x33, 0, 0, 0, 0x01}, vm, vm,
		ip.As16(), allNodes, 0x20000000)
	if err := rawSend(iface, frame); err != nil {
		warnf("unsolicited NA failed: %v", err)
	}
}

// buildARPRequest asks who has target (an ARP request from sender): used to learn a peer's real MAC address.
func buildARPRequest(senderMAC [6]byte, senderIP, target [4]byte) []byte {
	out := make([]byte, 0, 42)
	out = append(out, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
	out = append(out, senderMAC[:]...)
	out = append(out, 0x08, 0x06, 0x00, 0x01, 0x08, 0x00, 6, 4, 0x00, 0x01)
	out = append(out, senderMAC[:]...)
	out = append(out, senderIP[:]...)
	out = append(out, make([]byte, 6)...)
	return append(out, target[:]...)
}

// buildNSFor is a neighbor solicitation for target from src (the solicited-node multicast address of the target), with
// the sender's MAC as the source link-layer address: used to learn a peer's real MAC address.
func buildNSFor(senderMAC [6]byte, src, target [16]byte) []byte {
	dst := [16]byte{0xff, 0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0xff, target[13], target[14], target[15]}
	body := []byte{135, 0, 0, 0, 0, 0, 0, 0}
	body = append(body, target[:]...)
	body = append(body, 1, 1) // option: source link-layer address
	body = append(body, senderMAC[:]...)
	ck := icmp6Checksum(src, dst, body)
	body[2], body[3] = byte(ck>>8), byte(ck)
	f := []byte{0x33, 0x33, 0xff, target[13], target[14], target[15]}
	f = append(f, senderMAC[:]...)
	f = append(f, 0x86, 0xdd)
	f = append(f, 0x60, 0, 0, 0, byte(len(body)>>8), byte(len(body)), 58, 255)
	f = append(f, src[:]...)
	f = append(f, dst[:]...)
	return append(f, body...)
}
