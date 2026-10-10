package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestMoreVIPsValidate(t *testing.T) {
	base := func() GroupConfig {
		g := defaultGroup()
		g.VIP4, g.VIP6 = "10.0.0.1/24", "2001:db8::1/64"
		return g
	}
	g := base()
	g.MoreVIP4 = []string{"10.0.0.2", " 10.0.0.3 "}
	g.MoreVIP6 = []string{"2001:db8::2"}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(g.vipsFor(afIPv4), " "); got != "10.0.0.1/24 10.0.0.2/24 10.0.0.3/24" {
		t.Errorf("vipsFor v4 = %q", got)
	}
	if got := strings.Join(g.vipsFor(afIPv6), " "); got != "2001:db8::1/64 2001:db8::2/64" {
		t.Errorf("vipsFor v6 = %q", got)
	}
	for name, mod := range map[string]func(*GroupConfig){
		"outside the subnet": func(g *GroupConfig) { g.MoreVIP4 = []string{"10.0.1.5"} },
		"the primary":        func(g *GroupConfig) { g.MoreVIP4 = []string{"10.0.0.1"} },
		"twice":              func(g *GroupConfig) { g.MoreVIP4 = []string{"10.0.0.2", "10.0.0.2"} },
		"network address":    func(g *GroupConfig) { g.MoreVIP4 = []string{"10.0.0.0"} },
		"broadcast":          func(g *GroupConfig) { g.MoreVIP4 = []string{"10.0.0.255"} },
		"wrong family":       func(g *GroupConfig) { g.MoreVIP4 = []string{"2001:db8::9"} },
		"v6 in the v4 list":  func(g *GroupConfig) { g.MoreVIP6 = []string{"10.0.0.9"} },
		"not an address":     func(g *GroupConfig) { g.MoreVIP4 = []string{"banana"} },
		"no primary":         func(g *GroupConfig) { g.VIP4 = ""; g.MoreVIP4 = []string{"10.0.0.2"} },
		"too many": func(g *GroupConfig) {
			g.MoreVIP4 = nil
			for i := 2; i < 2+maxMoreVIPs+1; i++ {
				g.MoreVIP4 = append(g.MoreVIP4, "10.0.0."+strconv.Itoa(i))
			}
		},
	} {
		g := base()
		mod(&g)
		if err := g.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// nothing set: the primary only, and the field stays out of the JSON
	g = base()
	if got := g.vipsFor(afIPv4); len(got) != 1 {
		t.Errorf("no extras: %v", got)
	}
}

func TestARPAnsweredForEveryMoreVIP(t *testing.T) {
	e := rmEngine(t, false)
	e.failoverPrimary = 3
	vips := [][4]byte{{192, 0, 2, 9}, {192, 0, 2, 10}, {192, 0, 2, 11}}
	for _, ip := range []string{"192.0.2.9", "192.0.2.10", "192.0.2.11"} {
		out := e.arpAnswerAny(arpRequest(rmCli, "192.0.2.254", ip), rmSelf, vips)
		if out == nil {
			t.Fatalf("no answer for %s", ip)
		}
		if got := [4]byte(out[28:32]); got != parse4(ip) {
			t.Errorf("answer for %s says the address is %v", ip, got)
		}
	}
	if out := e.arpAnswerAny(arpRequest(rmCli, "192.0.2.254", "192.0.2.12"), rmSelf, vips); out != nil {
		t.Error("answered for an address that is not the gateway's")
	}
}

func parse4(s string) [4]byte {
	var a [4]byte
	n, i := 0, 0
	for _, c := range s + "." {
		if c == '.' {
			a[i] = byte(n)
			i++
			n = 0
		} else {
			n = n*10 + int(c-'0')
		}
	}
	return a
}

func TestMoreVIPClashesBetweenGateways(t *testing.T) {
	dc := newDaemonConfig()
	a := defaultGroup()
	a.GroupID, a.VIP4, a.VIP6 = 1, "10.0.0.1/24", ""
	b := defaultGroup()
	b.GroupID, b.VIP4, b.VIP6 = 2, "10.0.0.50/24", ""
	b.MoreVIP4 = []string{"10.0.0.1"}
	dc.Groups = []GroupConfig{a, b}
	if err := dc.Validate(); err == nil || !strings.Contains(err.Error(), "10.0.0.1") {
		t.Fatalf("two gateways with the same address: %v", err)
	}
}
