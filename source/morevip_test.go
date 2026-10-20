package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestKeepMatching(t *testing.T) {
	m := map[string]uint64{"www.Example.com": 5, "mail.example.com": 3, "other.org": 2, qsOthers: 9}
	got := keepMatching(m, "example")
	if len(got) != 2 || got["www.Example.com"] != 5 || got["mail.example.com"] != 3 {
		t.Errorf("got %v", got)
	}
	if len(keepMatching(m, "")) != 4 {
		t.Error("an empty filter must keep everything")
	}
	if len(keepMatching(m, "others")) != 0 {
		t.Error("(others) must not match")
	}
	f := QFilter{}.withMatch("  10.1. ", "ExAmple")
	if f.CMatch != "10.1." || f.DMatch != "example" {
		t.Errorf("%+v", f)
	}
}

func TestKeepMatchingClientsByHostName(t *testing.T) {
	s := NewQStats()
	s.rdns["10.1.1.1"] = rdnsEntry{names: []string{"Printer-3.office.example"}, at: time.Now()}
	s.rdns["10.1.1.2"] = rdnsEntry{at: time.Now()} // looked up, no name
	m := map[string]uint64{"10.1.1.1": 5, "10.1.1.2": 4, "10.2.2.2": 3, qsOthers: 7}
	// an address-like text never starts lookups (a-f, digits, dots only)
	got := s.keepMatchingClients(m, "10.1.")
	if len(got) != 2 || got["10.1.1.1"] != 5 || got["10.1.1.2"] != 4 {
		t.Errorf("by address: %v", got)
	}
	if len(s.rwork) != 0 {
		t.Errorf("an address-like filter started lookups: %v", s.rwork)
	}
	got = s.keepMatchingClients(map[string]uint64{"10.1.1.1": 5, "10.1.1.2": 4}, "printer-3")
	if len(got) != 1 || got["10.1.1.1"] != 5 {
		t.Errorf("by host name: %v", got)
	}
}

func TestClientNames(t *testing.T) {
	d := defaultDNS()
	d.ClientNames = map[string]string{" 10.1.1.1 ": "  Ann's laptop ", "::ffff:10.1.1.2": "x", "10.1.1.3": "   "}
	if err := d.validateClientNames(); err != nil {
		t.Fatal(err)
	}
	if len(d.ClientNames) != 2 || d.ClientNames["10.1.1.1"] != "Ann's laptop" || d.ClientNames["10.1.1.2"] != "x" {
		t.Errorf("normalised: %v", d.ClientNames)
	}
	for name, m := range map[string]map[string]string{
		"not an address": {"banana": "x"},
		"long":           {"10.1.1.1": strings.Repeat("a", maxClientNameLen+1)},
		"control":        {"10.1.1.1": "a\nb"},
	} {
		e := defaultDNS()
		e.ClientNames = m
		if err := e.validateClientNames(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a name by hand comes first and is found by the filter
	s := NewQStats()
	s.rdns["10.1.1.1"] = rdnsEntry{names: []string{"ann-pc.corp.example"}, at: time.Now()}
	s.SetNames(map[string]string{"10.1.1.1": "Ann's laptop"})
	cl := []NameCount{{Name: "10.1.1.1", Count: 3}}
	s.fillHosts(cl)
	if cl[0].Host != "Ann's laptop" || !cl[0].Manual || len(cl[0].Hosts) != 2 || cl[0].Hosts[1] != "ann-pc.corp.example" {
		t.Errorf("fillHosts: %+v", cl[0])
	}
	got := s.keepMatchingClients(map[string]uint64{"10.1.1.1": 3, "10.1.1.2": 1}, "laptop")
	if len(got) != 1 || got["10.1.1.1"] != 3 {
		t.Errorf("filter by the name: %v", got)
	}
}
