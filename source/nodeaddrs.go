package main

import (
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
)

// NodeIface is one Ethernet interface of a node with the addresses the Topology drawing shows in the node's tooltip:
// its IPv4 addresses and its IPv6 global unicast (GUA) addresses, each with its prefix length.
type NodeIface struct {
	Name string   `json:"name"`
	V4   []string `json:"v4,omitempty"`
	V6   []string `json:"v6,omitempty"`
	// ULA is the interface's unique local IPv6 addresses (fc00::/7).  The tooltip does not show them (it is for the
	// global ones); the Node IP column of Monitor ▸ Cluster does, with the rest.
	ULA []string `json:"ula,omitempty"`
}

var (
	gua6 = netip.MustParsePrefix("2000::/3")
	ula6 = netip.MustParsePrefix("fc00::/7")
)

// addrList is every address of the interfaces without the prefix lengths: IPv4, then IPv6 global, then unique local,
// each once.  It is what identifies a node by address (the Node IP column and the lookup of a gateway member's node).
func addrList(ifs []NodeIface) []string {
	var out []string
	seen := map[string]bool{}
	add := func(l []string) {
		for _, a := range l {
			if p, err := netip.ParsePrefix(a); err == nil {
				a = p.Addr().String()
			}
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	for _, i := range ifs {
		add(i.V4)
	}
	for _, i := range ifs {
		add(i.V6)
	}
	for _, i := range ifs {
		add(i.ULA)
	}
	return out
}

// nodeIfacesFn lists the node's interfaces (replaceable in tests).
var nodeIfacesFn = func() []nodeIfaceRaw {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []nodeIfaceRaw
	for _, ifc := range ifs {
		r := nodeIfaceRaw{Name: ifc.Name, Loopback: ifc.Flags&net.FlagLoopback != 0, Ethernet: isEthernetIface(ifc.Name)}
		r.Addrs, _ = ifc.Addrs()
		out = append(out, r)
	}
	return out
}

type nodeIfaceRaw struct {
	Name     string
	Loopback bool
	Ethernet bool
	Addrs    []net.Addr
}

// sysfs locations (variables so a test can point them at a made-up tree).
var (
	sysClassNet   = "/sys/class/net/"
	sysVirtualNet = "/sys/devices/virtual/net/"
)

// ethernetDevTypes are the kinds of virtual link that are an Ethernet interface of the host (or of the container
// it runs in) and not a plumbing detail: a bridge, a bond, a VLAN, and a veth, which is how a container's own
// "eth0" appears (the host's side of a veth has no address, so it is not listed anyway).
var ethernetDevTypes = map[string]bool{"bridge": true, "bond": true, "vlan": true, "veth": true}

// devType is the DEVTYPE line of an interface's uevent file ("" when there is none).
func devType(name string) string {
	b, err := os.ReadFile(sysClassNet + name + "/uevent")
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "DEVTYPE="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// isEthernetIface says whether name is an Ethernet interface of this node: ARPHRD_ETHER, and either a physical
// device or a virtual link of one of the kinds in ethernetDevTypes (a bridge, a bond, a VLAN, a veth: the network
// card of a container is one).  Other virtual links (macvlan such as ddgw's own ddgwN.M, vxlan, tap, dummy) are
// left out.
func isEthernetIface(name string) bool {
	base := sysClassNet + name + "/"
	if b, err := os.ReadFile(base + "type"); err != nil || strings.TrimSpace(string(b)) != "1" {
		return false
	}
	if strings.HasPrefix(name, "ddgw") {
		return false
	}
	if _, err := os.Stat(sysVirtualNet + name); err != nil {
		return true // a physical device
	}
	if ethernetDevTypes[devType(name)] {
		return true
	}
	for _, kind := range []string{"bridge", "bonding"} {
		if _, err := os.Stat(base + kind); err == nil {
			return true
		}
	}
	return false
}

// ethernetAddrs lists the Ethernet interfaces that hold an IPv4 address or an IPv6 GUA, sorted by name.  When no
// interface of a kind counted as Ethernet has one (a virtual link of a kind not known, say), every other interface
// that is not the loopback or ddgw's own holds the addresses instead, so the tooltip shows what the node has rather
// than nothing.
func ethernetAddrs() []NodeIface {
	raw := nodeIfacesFn()
	if out := collectAddrs(raw, func(r nodeIfaceRaw) bool { return r.Ethernet }); len(out) > 0 {
		return out
	}
	return collectAddrs(raw, func(r nodeIfaceRaw) bool { return !strings.HasPrefix(r.Name, "ddgw") })
}

// collectAddrs lists the interfaces chosen by keep (never the loopback) that hold an IPv4 address or an IPv6 GUA.
func collectAddrs(raw []nodeIfaceRaw, keep func(nodeIfaceRaw) bool) []NodeIface {
	var out []NodeIface
	for _, r := range raw {
		if r.Loopback || !keep(r) {
			continue
		}
		ni := NodeIface{Name: r.Name}
		for _, a := range r.Addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(n.IP)
			if !ok {
				continue
			}
			ones, _ := n.Mask.Size()
			ip = ip.Unmap()
			pfx := netip.PrefixFrom(ip, ones).String()
			switch {
			case ip.Is4():
				if !ip.IsLinkLocalUnicast() && !ip.IsLoopback() && !ip.IsUnspecified() {
					ni.V4 = append(ni.V4, pfx)
				}
			case gua6.Contains(ip):
				ni.V6 = append(ni.V6, pfx)
			case ula6.Contains(ip):
				ni.ULA = append(ni.ULA, pfx)
			}
		}
		if len(ni.V4)+len(ni.V6)+len(ni.ULA) > 0 {
			sort.Strings(ni.V4)
			sort.Strings(ni.V6)
			sort.Strings(ni.ULA)
			out = append(out, ni)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
