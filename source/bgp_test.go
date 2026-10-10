package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBGPValidate(t *testing.T) {
	ok := BGPConfig{ASN: 64512, LegacyEnabled: true, LegacyBFD: true, RouterID: " 192.0.2.10 ", Neighbors: []BGPNeighbor{
		{Peer: "192.0.2.1", RemoteAS: 64500, Description: "core", Password: "s3cret!", LegacyBFD: true},
		{Peer: " 2001:DB8::1 ", RemoteAS: 4200000000},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if ok.RouterID != "192.0.2.10" || ok.Neighbors[1].Peer != "2001:db8::1" {
		t.Fatalf("not normalised: %+v", ok)
	}
	if ok.LegacyEnabled || ok.LegacyBFD || ok.Neighbors[0].LegacyBFD {
		t.Fatalf("v41 settings not dropped: %+v", ok)
	}
	bad := map[string]BGPConfig{
		"router id v6":          {ASN: 1, RouterID: "2001:db8::1"},
		"router id text":        {ASN: 1, RouterID: "x"},
		"neighbor name":         {ASN: 1, Neighbors: []BGPNeighbor{{Peer: "router.example", RemoteAS: 2}}},
		"neighbor loopback":     {ASN: 1, Neighbors: []BGPNeighbor{{Peer: "127.0.0.1", RemoteAS: 2}}},
		"neighbor link-local":   {ASN: 1, Neighbors: []BGPNeighbor{{Peer: "fe80::1", RemoteAS: 2}}},
		"neighbor no AS":        {ASN: 1, Neighbors: []BGPNeighbor{{Peer: "192.0.2.1"}}},
		"duplicate":             {ASN: 1, Neighbors: []BGPNeighbor{{Peer: "192.0.2.1", RemoteAS: 2}, {Peer: "192.0.2.1", RemoteAS: 3}}},
		"password with space":   {ASN: 1, Neighbors: []BGPNeighbor{{Peer: "192.0.2.1", RemoteAS: 2, Password: "a b"}}},
		"password injection":    {ASN: 1, Neighbors: []BGPNeighbor{{Peer: "192.0.2.1", RemoteAS: 2, Password: "a\nrouter bgp 1"}}},
		"description injection": {ASN: 1, Neighbors: []BGPNeighbor{{Peer: "192.0.2.1", RemoteAS: 2, Description: "x\nip route 0.0.0.0/0 Null0"}}},
		"description too long":  {ASN: 1, Neighbors: []BGPNeighbor{{Peer: "192.0.2.1", RemoteAS: 2, Description: strings.Repeat("x", 61)}}},
	}
	for name, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// no AS: BGP is off, neighbors are kept
	off := BGPConfig{}
	if err := off.Validate(); err != nil || off.Neighbors == nil {
		t.Fatalf("empty config: %v %v", err, off.Neighbors)
	}
}

func TestRenderFRR(t *testing.T) {
	b := &BGPConfig{ASN: 64512, RouterID: "192.0.2.10", Neighbors: []BGPNeighbor{
		{Peer: "192.0.2.1", RemoteAS: 64500, Description: "core", Password: "pw"},
		{Peer: "2001:db8::1", RemoteAS: 64500},
	}}
	conf := renderFRR(b, []string{"203.0.113.53"}, []string{"2001:db8:53::1"}, "dns1")
	for _, want := range []string{
		"hostname dns1\n",
		"ip prefix-list DDGW-ANYCAST-V4 seq 10 permit 203.0.113.53/32\n",
		"ip prefix-list DDGW-ANYCAST-V4 seq 1000 deny any\n",
		"ipv6 prefix-list DDGW-ANYCAST-V6 seq 10 permit 2001:db8:53::1/128\n",
		"route-map DDGW-IN deny 10\n",
		"router bgp 64512\n no bgp default ipv4-unicast\n bgp router-id 192.0.2.10\n",
		" neighbor 192.0.2.1 remote-as 64500\n neighbor 192.0.2.1 description core\n neighbor 192.0.2.1 password pw\n neighbor 192.0.2.1 bfd\n",
		" address-family ipv4 unicast\n  network 203.0.113.53/32\n  neighbor 192.0.2.1 activate\n  neighbor 192.0.2.1 route-map DDGW-IN in\n  neighbor 192.0.2.1 route-map DDGW-OUT-V4 out\n",
		" address-family ipv6 unicast\n  network 2001:db8:53::1/128\n  neighbor 2001:db8::1 activate\n  neighbor 2001:db8::1 route-map DDGW-IN in\n  neighbor 2001:db8::1 route-map DDGW-OUT-V6 out\n",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("missing %q in:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "neighbor 192.0.2.1 route-map DDGW-OUT-V6") || strings.Contains(conf, "ipv4 unicast\n  network 2001") {
		t.Errorf("families mixed up:\n%s", conf)
	}
	// BFD is on for every neighbor
	if !strings.Contains(conf, " neighbor 2001:db8::1 bfd\n") || !strings.Contains(conf, " neighbor 192.0.2.1 bfd\n") {
		t.Errorf("BFD not on for every neighbor:\n%s", conf)
	}
	// off: header only, no BGP at all
	for _, off := range []*BGPConfig{nil, {}, {Neighbors: []BGPNeighbor{{Peer: "192.0.2.1", RemoteAS: 2}}}} {
		if c := renderFRR(off, []string{"203.0.113.53"}, nil, "x"); strings.Contains(c, "router bgp") || strings.Contains(c, "203.0.113.53") {
			t.Errorf("BGP config rendered for %+v:\n%s", off, c)
		}
	}
	if safeHostname("a b;c") != "" || safeHostname("dns-1.example") == "" {
		t.Error("hostname check")
	}
}

func TestSetDaemon(t *testing.T) {
	in := "zebra=yes\nbgpd=no\nospfd=no\n"
	if got := setDaemon(in, "bgpd", true); got != "zebra=yes\nbgpd=yes\nospfd=no\n" {
		t.Fatalf("%q", got)
	}
	if got := setDaemon("zebra=yes", "bgpd", true); got != "zebra=yes\nbgpd=yes\n" {
		t.Fatalf("%q", got)
	}
	if got := setDaemon("bgpd=yes\nbgpd_options=\"  -A 127.0.0.1\"\n", "bgpd", false); got != "bgpd=no\nbgpd_options=\"  -A 127.0.0.1\"\n" {
		t.Fatalf("option line touched: %q", got)
	}
}

func TestAnycastAddressesCollected(t *testing.T) {
	dc := newDaemonConfig()
	a, b := defaultGroup(), defaultGroup()
	b.GroupID, b.VIP4 = 2, "10.9.0.1/24"
	a.ExtraVIPs = []string{"203.0.113.9", "2001:db8::9"}
	b.ExtraVIPs = []string{"203.0.113.5", "203.0.113.9"}
	dc.Groups = []GroupConfig{a, b}
	v4, v6 := anycastAddresses(dc)
	if strings.Join(v4, ",") != "203.0.113.5,203.0.113.9" || strings.Join(v6, ",") != "2001:db8::9" {
		t.Fatalf("%v %v", v4, v6)
	}
}

type frrCall struct{ name, args string }

func fakeFRR(t *testing.T, fail map[string]bool) (*[]frrCall, string) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "daemons"), []byte("zebra=yes\nbgpd=no\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "frr.conf"), []byte("! old\n"), 0o640)
	oldDir, oldRun, oldReload := frrDir, frrRun, frrCanReload
	frrDir = dir
	frrCanReload = func() bool { return true }
	var calls []frrCall
	frrRun = func(name string, args ...string) ([]byte, error) {
		c := frrCall{name, strings.Join(args, " ")}
		calls = append(calls, c)
		if fail[c.name+" "+c.args] {
			return []byte("boom"), os.ErrInvalid
		}
		return nil, nil
	}
	t.Cleanup(func() { frrDir, frrRun, frrCanReload = oldDir, oldRun, oldReload })
	return &calls, dir
}

func bgpDC(enabled bool) *DaemonConfig {
	dc := newDaemonConfig()
	dc.Groups[0].ExtraVIPs = []string{"203.0.113.53"}
	asn := uint32(0) // no AS: BGP is off
	if enabled {
		asn = 64512
	}
	dc.BGP = &BGPConfig{ASN: asn, Neighbors: []BGPNeighbor{{Peer: "192.0.2.1", RemoteAS: 64500}}}
	return dc
}

func TestBGPApplyLifecycle(t *testing.T) {
	calls, dir := fakeFRR(t, nil)
	m := NewBGPManager(t.TempDir())

	// never enabled: touches nothing
	if r := m.applyNow(bgpDC(false)); !r.OK || len(*calls) != 0 {
		t.Fatalf("off and unmanaged did something: %+v %v", r, *calls)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "frr.conf")); string(b) != "! old\n" {
		t.Fatal("frr.conf touched while BGP was never enabled")
	}

	// enable: conf written, bgpd switched on, service enabled and restarted
	if r := m.applyNow(bgpDC(true)); !r.OK {
		t.Fatalf("%+v", r)
	}
	conf, _ := os.ReadFile(filepath.Join(dir, "frr.conf"))
	dm, _ := os.ReadFile(filepath.Join(dir, "daemons"))
	if !strings.Contains(string(conf), "router bgp 64512") || !strings.Contains(string(conf), "network 203.0.113.53/32") || !strings.Contains(string(dm), "bgpd=yes") || !strings.Contains(string(dm), "bfdd=yes") {
		t.Fatalf("not applied:\n%s\n%s", conf, dm)
	}
	got := ""
	for _, c := range *calls {
		got += c.name + " " + c.args + ";"
	}
	if got != "systemctl enable frr;systemctl reset-failed frr;systemctl restart frr;" {
		t.Fatalf("calls: %s", got)
	}
	if _, err := os.Stat(m.markerPath()); err != nil {
		t.Fatal("no marker after enabling")
	}

	// same config again: only a liveness check
	*calls = nil
	if r := m.applyNow(bgpDC(true)); !r.OK {
		t.Fatal(r)
	}
	if len(*calls) != 1 || (*calls)[0].args != "is-active --quiet frr" {
		t.Fatalf("unchanged config did more than check: %v", *calls)
	}

	// a changed neighbor: reload, not restart
	*calls = nil
	dc := bgpDC(true)
	dc.BGP.Neighbors = append(dc.BGP.Neighbors, BGPNeighbor{Peer: "192.0.2.2", RemoteAS: 64500})
	if r := m.applyNow(dc); !r.OK || len(*calls) != 1 || (*calls)[0].args != "reload frr" {
		t.Fatalf("%+v %v", r, *calls)
	}

	// turn off: BGP section gone, bgpd=no, restart, marker removed
	*calls = nil
	if r := m.applyNow(bgpDC(false)); !r.OK {
		t.Fatal(r)
	}
	conf, _ = os.ReadFile(filepath.Join(dir, "frr.conf"))
	dm, _ = os.ReadFile(filepath.Join(dir, "daemons"))
	if strings.Contains(string(conf), "router bgp") || !strings.Contains(string(dm), "bgpd=no") {
		t.Fatalf("not turned off:\n%s\n%s", conf, dm)
	}
	if _, err := os.Stat(m.markerPath()); err == nil {
		t.Fatal("marker kept after turning off")
	}
}

func TestBGPApplyProblems(t *testing.T) {
	// FRR not installed: reported, nothing created
	oldDir := frrDir
	frrDir = filepath.Join(t.TempDir(), "nope")
	defer func() { frrDir = oldDir }()
	m := NewBGPManager(t.TempDir())
	if r := m.applyNow(bgpDC(true)); r.OK || !strings.Contains(r.Detail, "not installed") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(frrDir); err == nil {
		t.Fatal("created the FRR directory")
	}
	frrDir = oldDir

	// reload fails: falls back to a restart
	calls, _ := fakeFRR(t, map[string]bool{"systemctl reload frr": true})
	m = NewBGPManager(t.TempDir())
	m.applyNow(bgpDC(true))
	*calls = nil
	dc := bgpDC(true)
	dc.BGP.ASN = 64513
	if r := m.applyNow(dc); !r.OK || len(*calls) != 3 || (*calls)[2].args != "restart frr" {
		t.Fatalf("%+v %v", r, *calls)
	}

	// restart fails: reported as a failure
	_, _ = fakeFRR(t, map[string]bool{"systemctl restart frr": true})
	m = NewBGPManager(t.TempDir())
	if r := m.applyNow(bgpDC(true)); r.OK {
		t.Fatalf("a failed restart was reported as ok: %+v", r)
	}
}

func TestParseBGPSummary(t *testing.T) {
	raw := `{"ipv4Unicast":{"routerId":"1.1.1.1","as":64512,"peers":{
	  "192.0.2.1":{"remoteAs":64500,"state":"Established","peerUptime":"00:01:02","pfxRcd":0,"pfxSnt":1},
	  "192.0.2.2":{"remoteAs":64500,"state":"Active","peerUptime":"never","pfxRcd":0,"pfxSnt":0}}},
	  "ipv6Unicast":{"peers":{"2001:db8::1":{"remoteAs":64500,"state":"Idle","pfxSnt":0}}}}`
	ps, err := parseBGPSummary([]byte(raw))
	if err != nil || len(ps) != 3 {
		t.Fatalf("%v %v", err, ps)
	}
	if ps[0].Peer != "192.0.2.1" || ps[0].State != "Established" || ps[0].Uptime != "00:01:02" || ps[0].Sent != 1 {
		t.Fatalf("%+v", ps[0])
	}
	if ps[1].Uptime != "" || ps[2].AF != "ipv6" {
		t.Fatalf("%+v", ps)
	}
	if _, err := parseBGPSummary([]byte("not json")); err == nil {
		t.Fatal("garbage accepted")
	}
	if ps, err := parseBGPSummary([]byte("{}")); err != nil || len(ps) != 0 {
		t.Fatal("empty summary")
	}
}

// The config file only has a "bgp" key once BGP was used, so a config that never
// touched it stays readable by older versions (they reject unknown keys).
func TestBGPKeyOmittedWhenUnused(t *testing.T) {
	b, _ := json.Marshal(newDaemonConfig())
	if strings.Contains(string(b), `"bgp"`) {
		t.Fatalf("bgp key present by default: %s", b)
	}
	dc := newDaemonConfig()
	dc.BGP = &BGPConfig{ASN: 1}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(dc)
	var back DaemonConfig
	if err := json.Unmarshal(b, &back); err != nil || back.BGP == nil || back.BGP.ASN != 1 {
		t.Fatalf("round trip: %v %+v", err, back.BGP)
	}
	// the shared (cluster) config never carries it
	if strings.Contains(string(bgpJSON(sharedOf(dc))), "bgp") {
		t.Fatal("BGP settings leak into the shared config")
	}
}

func bgpJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestBGPActiveAndLegacyJSON(t *testing.T) {
	if (*BGPConfig)(nil).Active() || (&BGPConfig{}).Active() || !(&BGPConfig{ASN: 1}).Active() {
		t.Fatal("Active() must follow the AS number")
	}
	// a config written by v41 still loads, and the old keys are gone after a save
	var c BGPConfig
	old := `{"enabled":true,"asn":64512,"bfd":true,"neighbors":[{"peer":"192.0.2.1","remote_as":64500,"bfd":true}]}`
	dec := json.NewDecoder(strings.NewReader(old))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(c)
	if strings.Contains(string(out), "bfd") || strings.Contains(string(out), "enabled") {
		t.Fatalf("v41 keys written again: %s", out)
	}
}

func TestParseBFDPeers(t *testing.T) {
	raw := `[{"multihop":false,"peer":"192.0.2.1","status":"up","uptime":120},{"peer":"2001:DB8::1","status":"down"},{"peer":"::ffff:192.0.2.9","status":"init"}]`
	got, err := parseBFDPeers([]byte(raw))
	if err != nil || got["192.0.2.1"] != "up" || got["2001:db8::1"] != "down" || got["192.0.2.9"] != "init" || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := parseBFDPeers([]byte("not json")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestBGPApplyWithoutReloadScript(t *testing.T) {
	calls, dir := fakeFRR(t, nil)
	m := NewBGPManager(t.TempDir())
	if r := m.applyNow(bgpDC(true)); !r.OK {
		t.Fatal(r)
	}
	// the host's own config was kept before ddgw replaced it
	if b, _ := os.ReadFile(filepath.Join(dir, "frr.conf.pre-ddgw")); string(b) != "! old\n" {
		t.Fatalf("no backup of the foreign frr.conf: %q", b)
	}
	// no frr-reload.py: a changed config restarts once, no reload attempt,
	frrCanReload = func() bool { return false }
	*calls = nil
	dc := bgpDC(true)
	dc.BGP.ASN = 64513
	r := m.applyNow(dc)
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	for _, c := range *calls {
		if c.args == "reload frr" {
			t.Fatalf("tried a reload that cannot work: %v", *calls)
		}
	}
	// a second apply must not overwrite the backup with ddgw's own file
	if b, _ := os.ReadFile(filepath.Join(dir, "frr.conf.pre-ddgw")); string(b) != "! old\n" {
		t.Fatalf("backup replaced: %q", b)
	}
}

func TestFRRJournalTail(t *testing.T) {
	oldRun := frrRun
	defer func() { frrRun = oldRun }()
	frrRun = func(name string, args ...string) ([]byte, error) {
		return []byte("noise vty[3]@> enable\nreal one\n\nreal two\nvty[9] again\n"), nil
	}
	if got := frrJournalTail(); got != " [journal: real one | real two]" {
		t.Fatalf("%q", got)
	}
}

func TestBGPMultihopAndTimers(t *testing.T) {
	b := &BGPConfig{ASN: 65001, Neighbors: []BGPNeighbor{
		{Peer: "10.0.1.5", RemoteAS: 64512, Multihop: 2},
		{Peer: "10.0.1.6", RemoteAS: 64512, Multihop: 1},
		{Peer: "10.0.1.7", RemoteAS: 65001, Multihop: 2}, // iBGP: ignored
		{Peer: "10.0.1.8", RemoteAS: 64512},
	}}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	if b.Neighbors[1].Multihop != 0 {
		t.Fatal("multihop 1 should mean off")
	}
	out := renderFRR(b, []string{"203.0.113.53"}, nil, "h")
	if !strings.Contains(out, " timers bgp 3 9\n") {
		t.Fatal("timers missing:\n" + out)
	}
	if strings.Count(out, "ebgp-multihop") != 1 || !strings.Contains(out, "neighbor 10.0.1.5 ebgp-multihop 2\n") {
		t.Fatal("multihop lines wrong:\n" + out)
	}
	bad := &BGPConfig{ASN: 1, Neighbors: []BGPNeighbor{{Peer: "10.0.1.5", RemoteAS: 2, Multihop: 300}}}
	if bad.Validate() == nil {
		t.Fatal("multihop 300 accepted")
	}
}

func TestBGPTimerSettings(t *testing.T) {
	b := &BGPConfig{ASN: 65001, Neighbors: []BGPNeighbor{}}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(renderFRR(b, nil, nil, "h"), " timers bgp 3 9\n") {
		t.Fatal("defaults not 3/9")
	}
	b.Keepalive, b.Hold = 10, 30
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(renderFRR(b, nil, nil, "h"), " timers bgp 10 30\n") {
		t.Fatal("custom timers not written")
	}
	for _, bad := range []BGPConfig{{ASN: 1, Keepalive: 9, Hold: 9}, {ASN: 1, Hold: 2}, {ASN: 1, Keepalive: 12}, {ASN: 1, Keepalive: -1}} {
		bad := bad
		if bad.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestRenderFRRASPrepend(t *testing.T) {
	b := &BGPConfig{ASN: 4215123199, Neighbors: []BGPNeighbor{{Peer: "192.0.2.1", RemoteAS: 64500}}}
	if conf := renderFRR(b, []string{"203.0.113.53"}, []string{"2001:db8:53::1"}, "h"); strings.Contains(conf, "as-path prepend") {
		t.Fatalf("prepend without being asked:\n%s", conf)
	}
	b.ASPrepend = true
	conf := renderFRR(b, []string{"203.0.113.53"}, []string{"2001:db8:53::1"}, "h")
	want := " set as-path prepend 4215123199 4215123199 4215123199\n"
	if strings.Count(conf, want) != 2 || !strings.Contains(conf, "match ip address prefix-list DDGW-ANYCAST-V4\n"+want) || !strings.Contains(conf, "match ipv6 address prefix-list DDGW-ANYCAST-V6\n"+want) {
		t.Fatalf("both outbound route-maps must prepend three times:\n%s", conf)
	}
	if strings.Contains(strings.SplitN(conf, "route-map DDGW-IN", 2)[1], "prepend") {
		t.Fatal("the inbound route-map must not change")
	}
}
