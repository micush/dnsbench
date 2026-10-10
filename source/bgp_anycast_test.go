package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAnycastBGPStatus(t *testing.T) {
	// each case: the configured neighbors (name -> disabled) and what FRR reports for them
	type nb struct {
		peer     string
		state    string // "" = not known to FRR
		disabled bool
	}
	cases := []struct {
		name  string
		addr  string
		nbrs  []nb
		known bool
		want  string
		kind  string
	}{
		{"established", "10.9.9.9", []nb{{"192.0.2.1", "Established", false}}, true, "ok", ""},
		{"both established", "10.9.9.9", []nb{{"192.0.2.1", "Established", false}, {"192.0.2.2", "Established", false}}, true, "ok", ""},
		{"one of two established", "10.9.9.9", []nb{{"192.0.2.1", "Connect", false}, {"192.0.2.2", "Established", false}}, true, "warn", "partial"},
		{"one of two idle", "10.9.9.9", []nb{{"192.0.2.1", "Idle", false}, {"192.0.2.2", "Established", false}}, true, "warn", "partial"},
		{"one established, one disabled", "10.9.9.9", []nb{{"192.0.2.1", "", true}, {"192.0.2.2", "Established", false}}, true, "warn", "partial"},
		{"connect", "10.9.9.9", []nb{{"192.0.2.1", "Connect", false}}, true, "bad", "down"},
		{"active", "10.9.9.9", []nb{{"192.0.2.1", "Active", false}}, true, "bad", "down"},
		{"opensent", "10.9.9.9", []nb{{"192.0.2.1", "OpenSent", false}}, true, "bad", "down"},
		{"not known to FRR yet", "10.9.9.9", []nb{{"192.0.2.1", "", false}}, true, "bad", "down"},
		{"idle", "10.9.9.9", []nb{{"192.0.2.1", "Idle", false}}, true, "bad", "down"},
		{"all disabled", "10.9.9.9", []nb{{"192.0.2.1", "", true}}, true, "bad", "down"},
		{"none", "10.9.9.9", nil, true, "bad", "none"},
		{"v6 neighbor does not serve v4", "10.9.9.9", []nb{{"2001:db8::1", "Established", false}}, true, "bad", "none"},
		{"v4 neighbor does not serve v6", "2001:db8::1", []nb{{"192.0.2.1", "Established", false}}, true, "bad", "none"},
		{"v6 established", "2001:db8::1", []nb{{"2001:db8::1", "Established", false}}, true, "ok", ""},
		{"v6 fine, v4 neighbor down does not matter", "2001:db8::1", []nb{{"2001:db8::1", "Established", false}, {"192.0.2.1", "Idle", false}}, true, "ok", ""},
		{"bgpd silent", "10.9.9.9", []nb{{"192.0.2.1", "", false}}, false, "bad", "down"},
	}
	for _, c := range cases {
		var nbrs []BGPNeighbor
		var peers []BGPPeer
		for _, n := range c.nbrs {
			nbrs = append(nbrs, BGPNeighbor{Peer: n.peer, RemoteAS: 1, Disabled: n.disabled})
			if n.state != "" {
				af := "ipv4"
				if strings.Contains(n.peer, ":") {
					af = "ipv6"
				}
				peers = append(peers, BGPPeer{Peer: n.peer, AF: af, State: n.state})
			}
		}
		got, why, kind := anycastBGPStatus(c.addr, nbrs, peers, c.known)
		if got != c.want || kind != c.kind {
			t.Errorf("%s: %q/%q, want %q/%q", c.name, got, kind, c.want, c.kind)
		}
		if (got == "ok") != (why == "") {
			t.Errorf("%s: detail %q for %q", c.name, why, got)
		}
	}
}

// The canvas colours a held address by its BGP sessions, and leaves it alone
// when this node does not manage BGP.
func TestMarkAnycastBGPColour(t *testing.T) {
	hookLo(t)
	up := newFakeDNS(t)
	g := defaultGroup()
	g.Interface = "ddgwnone0"
	d := testDNSCfg(up.addr)
	d.ListenPort = freeUDPPort(t)
	g.DNSProxy, g.DNS, g.ExtraVIPs = true, &d, []string{"127.0.0.9"}
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	s := NewSupervisor(context.Background(), dc)
	t.Cleanup(s.StopAll)
	s.refreshPool(dc, true)
	s.mu.Lock()
	s.startGroupLocked(dc.Groups[0])
	s.mu.Unlock()

	oldDir, oldBGP := frrDir, vtyshBGP
	frrDir = t.TempDir()
	t.Cleanup(func() { frrDir, vtyshBGP = oldDir, oldBGP; bgpLive.at = time.Time{} })
	state := ""
	vtyshBGP = func() ([]byte, error) {
		return []byte(`{"ipv4Unicast":{"peers":{"10.0.0.1":{"remoteAs":65001,"state":"` + state + `"}}}}`), nil
	}
	ss := &StatusServer{sup: s}
	look := func(st string) string {
		state = st
		bgpLive.at = time.Time{}
		gs := []CanvasGateway{{GroupID: 1}}
		ss.markAnycast(gs)
		if len(gs[0].Anycast) != 1 || !gs[0].Anycast[0].Up {
			t.Fatalf("%+v", gs[0].Anycast)
		}
		return gs[0].Anycast[0].Status
	}
	if got := look("Established"); got != "" {
		t.Fatalf("BGP not managed here: status %q", got)
	}
	s.mu.Lock()
	s.dc.BGP = &BGPConfig{ASN: 65000, Neighbors: []BGPNeighbor{{Peer: "10.0.0.1", RemoteAS: 65001}}}
	s.mu.Unlock()
	for st, want := range map[string]string{"Established": "ok", "Connect": "bad", "Idle": "bad"} {
		if got := look(st); got != want {
			t.Errorf("%s: %q, want %q", st, got, want)
		}
	}
	// BGP disabled on this node: red, whatever FRR would say
	s.mu.Lock()
	s.dc.BGP.Disabled = true
	s.mu.Unlock()
	if got := look("Established"); got != "bad" {
		t.Errorf("BGP disabled: %q, want bad", got)
	}
}
