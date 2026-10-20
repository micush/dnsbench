package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hookSNMP points the manager at a temporary snmpd.conf and a fake systemctl that records what it is asked.
func hookSNMP(t *testing.T) (calls *[]string, conf string) {
	t.Helper()
	conf = filepath.Join(t.TempDir(), "snmp", "snmpd.conf")
	oc, or_, oi := snmpConfPath, snmpRun, snmpInstalled
	snmpConfPath = conf
	snmpInstalled = func() bool { return true }
	var rec []string
	active := false
	snmpRun = func(args ...string) ([]byte, error) {
		rec = append(rec, strings.Join(args, " "))
		switch args[0] {
		case "restart", "start":
			active = true
		case "disable":
			active = false
		case "is-active":
			if !active {
				return nil, errors.New("inactive")
			}
		}
		return nil, nil
	}
	t.Cleanup(func() { snmpConfPath, snmpRun, snmpInstalled = oc, or_, oi; snmpWarned = snmpWarned.Add(0) })
	return &rec, conf
}

func TestSNMPValidate(t *testing.T) {
	c := SNMPConfig{Communities: []string{" public ", "ro-2", "public", ""}, Location: "  rack 4, row B ", Contact: "noc@example.com"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(c.Communities) != 2 || c.Communities[0] != "public" || c.Communities[1] != "ro-2" || c.Location != "rack 4, row B" {
		t.Fatalf("tidied: %+v", c)
	}
	for _, bad := range []string{"has space", "with#hash", `q"uote`, "tab\tx", "naïve", strings.Repeat("x", 65)} {
		b := SNMPConfig{Communities: []string{bad}}
		if err := b.Validate(); err == nil {
			t.Errorf("community %q should be refused", bad)
		}
	}
	many := SNMPConfig{}
	for i := 0; i < 17; i++ {
		many.Communities = append(many.Communities, "c"+strings.Repeat("x", i))
	}
	if err := many.Validate(); err == nil {
		t.Error("17 communities should be refused")
	}
	for _, bad := range []SNMPConfig{{Location: "two\nlines"}, {Contact: strings.Repeat("x", 256)}} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v should be refused", bad)
		}
	}
}

func TestDaemonConfigDropsAnEmptySNMPBlock(t *testing.T) {
	dc := newDaemonConfig()
	dc.SNMP = &SNMPConfig{Communities: []string{"  "}, Location: " "}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	if dc.SNMP != nil {
		t.Fatalf("an empty snmp block should go away: %+v", dc.SNMP)
	}
}

func TestSNMPConfBody(t *testing.T) {
	b := snmpdConfBody(SNMPConfig{Communities: []string{"public", "ro2"}, Location: "Phoenix DC", Contact: "noc@example.com"})
	for _, w := range []string{snmpHeader, "agentaddress udp:161,udp6:161\n", "rocommunity public\n", "rocommunity6 public\n", "rocommunity ro2\n", "rocommunity6 ro2\n", "sysLocation Phoenix DC\n", "sysContact noc@example.com\n"} {
		if !strings.Contains(b, w) {
			t.Errorf("missing %q in %q", w, b)
		}
	}
	if b2 := snmpdConfBody(SNMPConfig{Communities: []string{"x"}}); strings.Contains(b2, "sysLocation") || strings.Contains(b2, "sysContact") {
		t.Errorf("empty fields must not be written: %q", b2)
	}
}

func TestSNMPEnablesWhenACommunityIsSetAndDisablesWhenEmptied(t *testing.T) {
	calls, conf := hookSNMP(t)
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := "rocommunity theirs localhost\n"
	if err := os.WriteFile(conf, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	// empty and never taken over: left strictly alone
	applySNMP(SNMPConfig{})
	if len(*calls) != 0 {
		t.Fatalf("an snmpd that was never ours must not be touched: %v", *calls)
	}
	// a community: the file is written, the original kept, snmpd enabled and restarted
	cfg := SNMPConfig{Communities: []string{"public"}, Location: "lab"}
	applySNMP(cfg)
	got, _ := os.ReadFile(conf)
	if string(got) != snmpdConfBody(cfg) {
		t.Fatalf("conf: %q", got)
	}
	if b, _ := os.ReadFile(conf + snmpBackupSuffix); string(b) != orig {
		t.Fatalf("the original file was not kept: %q", b)
	}
	if st, _ := os.Stat(conf); st.Mode().Perm() != 0o600 {
		t.Errorf("the community strings are secrets: mode %v", st.Mode().Perm())
	}
	if strings.Join(*calls, "|") != "enable snmpd|restart snmpd" {
		t.Fatalf("calls: %v", *calls)
	}
	// the same settings again: nothing is restarted
	*calls = nil
	applySNMP(cfg)
	if strings.Join(*calls, "|") != "enable snmpd|is-active --quiet snmpd" {
		t.Fatalf("an unchanged file must not restart snmpd: %v", *calls)
	}
	// another community: restarted
	*calls = nil
	applySNMP(SNMPConfig{Communities: []string{"public", "second"}, Location: "lab"})
	if !strings.Contains(strings.Join(*calls, "|"), "restart snmpd") {
		t.Fatalf("a changed file must restart snmpd: %v", *calls)
	}
	// emptied: snmpd is switched off and the original file is back
	*calls = nil
	applySNMP(SNMPConfig{Location: "lab"})
	if strings.Join(*calls, "|") != "disable --now snmpd" {
		t.Fatalf("calls: %v", *calls)
	}
	if b, _ := os.ReadFile(conf); string(b) != orig {
		t.Fatalf("the original file should be back: %q", b)
	}
	if _, err := os.Stat(conf + snmpBackupSuffix); err == nil {
		t.Error("the backup should have been moved back")
	}
	// and a second empty apply does nothing (the file is not ours any more)
	*calls = nil
	applySNMP(SNMPConfig{})
	if len(*calls) != 0 {
		t.Fatalf("calls: %v", *calls)
	}
}

func TestSNMPWithoutAnOriginalFileRemovesOurs(t *testing.T) {
	_, conf := hookSNMP(t)
	applySNMP(SNMPConfig{Communities: []string{"public"}})
	if _, err := os.Stat(conf + snmpBackupSuffix); err == nil {
		t.Fatal("there was nothing to keep")
	}
	applySNMP(SNMPConfig{})
	if _, err := os.Stat(conf); err == nil {
		t.Fatal("our file should be removed when the communities are emptied and there was no original")
	}
}

func TestSNMPNotInstalledDoesNothing(t *testing.T) {
	calls, conf := hookSNMP(t)
	snmpInstalled = func() bool { return false }
	applySNMP(SNMPConfig{Communities: []string{"public"}})
	if _, err := os.Stat(conf); err == nil || len(*calls) != 0 {
		t.Fatalf("with no snmpd nothing is written or run: %v", *calls)
	}
	st := snmpStatus(SNMPConfig{Communities: []string{"public"}})
	if st["installed"] != false || st["enabled"] != true {
		t.Fatalf("%v", st)
	}
}

func TestSNMPCommunitiesAreRedactedInSupportBundles(t *testing.T) {
	v, n := redactJSON(map[string]any{"ntp": map[string]any{"snmp_communities": []any{"public", "x"}, "snmp_contact": "noc"}, "snmp": map[string]any{"location": "lab"}})
	m := v.(map[string]any)["ntp"].(map[string]any)
	if m["snmp_communities"] != tshootRedacted || m["snmp_contact"] != "noc" || n != 1 {
		t.Fatalf("%v (%d)", m, n)
	}
}

func TestSNMPConfigHistoryHidesTheStrings(t *testing.T) {
	a, b := newDaemonConfig(), newDaemonConfig()
	b.DNS.SNMPCommunities = []string{"verysecret"}
	b.SNMP = &SNMPConfig{Location: "lab"}
	var text string
	for _, s := range configSections(a, b) {
		text += s.Label + ": " + s.Detail + "\n"
	}
	if !strings.Contains(text, "SNMP switched on with 1 community string(s)") || !strings.Contains(text, "location changed") || strings.Contains(text, "verysecret") {
		t.Fatalf("%q", text)
	}
}

func TestSNMPCommunitiesAndContactAreSharedLocationIsNot(t *testing.T) {
	a := newDaemonConfig()
	a.DNS.SNMPCommunities = []string{"public", "ro2"}
	a.DNS.SNMPContact = "noc@example.com"
	a.SNMP = &SNMPConfig{Location: "rack A"}
	sh := sharedOf(a)
	if len(sh.DNS.SNMPCommunities) != 2 || sh.DNS.SNMPContact != "noc@example.com" {
		t.Fatalf("not replicated: %+v", sh.DNS)
	}
	b := newDaemonConfig()
	b.SNMP = &SNMPConfig{Location: "rack B"}
	if _, err := mergeShared(b, sh, nil); err != nil {
		t.Fatal(err)
	}
	got := b.snmpSettings()
	if len(got.Communities) != 2 || got.Contact != "noc@example.com" || got.Location != "rack B" {
		t.Fatalf("%+v", got)
	}
	// emptying on the primary empties the replica
	a.DNS.SNMPCommunities = nil
	if _, err := mergeShared(b, sharedOf(a), nil); err != nil {
		t.Fatal(err)
	}
	if b.snmpSettings().enabled() {
		t.Fatal("communities should be gone")
	}
}

func TestSharedSNMPCommunitiesAreValidated(t *testing.T) {
	dc := newDaemonConfig()
	dc.DNS.SNMPCommunities = []string{"bad one"}
	if err := dc.Validate(); err == nil {
		t.Fatal("a community with a space should be refused")
	}
}

func TestSNMPStatusCanBeReadOnAnotherNode(t *testing.T) {
	if err := proxyAllowed("GET", "/api/snmp"); err != nil {
		t.Fatalf("the SNMP status must be relayable to the node picked in the top bar: %v", err)
	}
}
