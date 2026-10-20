package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSSDGPOProblem(t *testing.T) {
	dir := t.TempDir()
	sssdConfFile, sssdConfDir = filepath.Join(dir, "sssd.conf"), filepath.Join(dir, "conf.d")
	os.MkdirAll(sssdConfDir, 0o755)
	if p := sssdGPOProblem("svc"); p != "" {
		t.Fatalf("no sssd.conf: %q", p)
	}
	write := func(s string) { os.WriteFile(sssdConfFile, []byte(s), 0o600) }
	write("[sssd]\ndomains = ex.com\n[domain/ex.com]\nid_provider = ad\naccess_provider = ad\n")
	if p := sssdGPOProblem("svc"); !strings.Contains(p, "domain/ex.com") || !strings.Contains(p, "+svc") {
		t.Fatalf("AD, unmapped: %q", p)
	}
	write("[domain/ex.com]\nid_provider = ad\nad_gpo_access_control = Permissive\n")
	if p := sssdGPOProblem("svc"); p != "" {
		t.Fatalf("permissive: %q", p)
	}
	write("[domain/ex.com]\nid_provider = ad\nad_gpo_map_interactive = +other, +svc\n")
	if p := sssdGPOProblem("svc"); p != "" {
		t.Fatalf("mapped: %q", p)
	}
	write("[domain/ex.com]\nid_provider = ldap\n")
	if p := sssdGPOProblem("svc"); p != "" {
		t.Fatalf("not AD: %q", p)
	}
	write("[domain/ex.com]\nid_provider = ad\n")
	os.WriteFile(filepath.Join(sssdConfDir, "x.conf"), []byte("[domain/ex.com]\nad_gpo_map_service = +svc\n"), 0o600)
	if p := sssdGPOProblem("svc"); p != "" {
		t.Fatalf("mapped in a snippet: %q", p)
	}
}

func TestFixSSSD(t *testing.T) {
	dir := t.TempDir()
	sssdConfFile, sssdConfDir = filepath.Join(dir, "sssd.conf"), filepath.Join(dir, "conf.d")
	os.MkdirAll(sssdConfDir, 0o755)
	procUIDMap = filepath.Join(dir, "no-uid-map")
	restarts := 0
	sssdRestart = func(bool) error { restarts++; return nil }
	orig := "[sssd]\ndomains = ex.com\n\n[domain/ex.com]\nid_provider = ad\naccess_provider = ad\n\n[pam]\n"
	os.WriteFile(sssdConfFile, []byte(orig), 0o600)
	msg, err := fixSSSD("svc")
	if err != nil || !strings.Contains(msg, "restarted sssd") || restarts != 1 {
		t.Fatalf("fix: %q %v %d", msg, err, restarts)
	}
	b, _ := os.ReadFile(sssdConfFile)
	if !strings.Contains(string(b), "[domain/ex.com]\nad_gpo_map_interactive = +svc\nid_provider = ad") || !strings.HasSuffix(string(b), "[pam]\n") {
		t.Fatalf("conf: %q", b)
	}
	if bk, _ := os.ReadFile(sssdConfFile + ".svc.bak"); string(bk) != orig {
		t.Fatalf("backup: %q", bk)
	}
	if st, _ := os.Stat(sssdConfFile); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if msg, err = fixSSSD("svc"); msg != "" || err != nil || restarts != 1 {
		t.Fatalf("second run must do nothing: %q %v %d", msg, err, restarts)
	}
	// a second daemon appends to the line the first one made; an existing line gets the item added
	if msg, err = fixSSSD("other"); err != nil || restarts != 2 {
		t.Fatalf("other: %q %v", msg, err)
	}
	b, _ = os.ReadFile(sssdConfFile)
	if !strings.Contains(string(b), "ad_gpo_map_interactive = +svc, +other\n") {
		t.Fatalf("append: %q", b)
	}
	// a domain that exists only in a snippet is not edited
	os.WriteFile(sssdConfFile, []byte("[sssd]\n"), 0o600)
	os.WriteFile(filepath.Join(sssdConfDir, "d.conf"), []byte("[domain/sn.com]\nid_provider = ad\n"), 0o600)
	if msg, err = fixSSSD("svc"); err == nil || restarts != 2 {
		t.Fatalf("snippet: %q %v", msg, err)
	}
}

func TestFixSSSDIDRangeInAContainer(t *testing.T) {
	dir := t.TempDir()
	sssdConfFile, sssdConfDir = filepath.Join(dir, "sssd.conf"), filepath.Join(dir, "conf.d")
	procUIDMap = filepath.Join(dir, "uid_map")
	resets := []bool{}
	sssdRestart = func(reset bool) error { resets = append(resets, reset); return nil }
	conf := "[sssd]\n[domain/ex.com]\nid_provider = ad\nad_gpo_access_control = permissive\n"
	os.WriteFile(sssdConfFile, []byte(conf), 0o600)

	// a host: the map is the whole range, nothing to do
	os.WriteFile(procUIDMap, []byte("         0          0 4294967295\n"), 0o644)
	if msg, err := fixSSSD("svc"); msg != "" || err != nil || len(resets) != 0 {
		t.Fatalf("host: %q %v %v", msg, err, resets)
	}
	// a container with IDs 0-65535: the range is set, sssd's cache is cleared
	os.WriteFile(procUIDMap, []byte("         0     100000      65536\n"), 0o644)
	msg, err := fixSSSD("svc")
	if err != nil || !strings.Contains(msg, "ID range 10000-60000") || !strings.Contains(msg, "cache cleared") || len(resets) != 1 || !resets[0] {
		t.Fatalf("container: %q %v %v", msg, err, resets)
	}
	b, _ := os.ReadFile(sssdConfFile)
	if !strings.Contains(string(b), "[domain/ex.com]\nldap_idmap_range_min = 10000\nldap_idmap_range_max = 60000\nldap_idmap_range_size = 50000\nid_provider = ad") {
		t.Fatalf("conf: %q", b)
	}
	if msg, err = fixSSSD("svc"); msg != "" || err != nil || len(resets) != 1 {
		t.Fatalf("second run must do nothing: %q %v %v", msg, err, resets)
	}
	// a domain with a range of its own is left alone
	os.WriteFile(sssdConfFile, []byte("[domain/ex.com]\nid_provider = ad\nldap_idmap_range_min = 500\nad_gpo_access_control = disabled\n"), 0o600)
	if msg, err = fixSSSD("svc"); msg != "" || err != nil || len(resets) != 1 {
		t.Fatalf("own range: %q %v %v", msg, err, resets)
	}
	// a container too small for the range: warn, change nothing
	os.WriteFile(procUIDMap, []byte("0 100000 30000\n"), 0o644)
	os.WriteFile(sssdConfFile, []byte("[domain/ex.com]\nid_provider = ad\nad_gpo_access_control = disabled\n"), 0o600)
	if msg, err = fixSSSD("svc"); msg != "" || err == nil || !strings.Contains(err.Error(), "up to 29999") || len(resets) != 1 {
		t.Fatalf("small map: %q %v %v", msg, err, resets)
	}
	// ID mapping off: the IDs come from AD itself
	os.WriteFile(procUIDMap, []byte("0 100000 65536\n"), 0o644)
	os.WriteFile(sssdConfFile, []byte("[domain/ex.com]\nid_provider = ad\nldap_id_mapping = False\nad_gpo_access_control = disabled\n"), 0o600)
	if msg, err = fixSSSD("svc"); msg != "" || err != nil || len(resets) != 1 {
		t.Fatalf("no id mapping: %q %v %v", msg, err, resets)
	}
}
