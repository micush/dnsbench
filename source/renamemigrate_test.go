package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateOldInstall(t *testing.T) {
	root := t.TempDir()
	w := func(p, c string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldS, newS := filepath.Join(root, "var-lib-ddgw"), filepath.Join(root, "var-lib-dnsdir")
	savedA := []string{oldStateDir, oldRunDir, oldPAMFile, newPAMFile, oldNMFile, newNMFile, groupFile, oldFRRBackup, newFRRBackup}
	t.Cleanup(func() {
		oldStateDir, oldRunDir, oldPAMFile, newPAMFile, oldNMFile, newNMFile, groupFile, oldFRRBackup, newFRRBackup = savedA[0], savedA[1], savedA[2], savedA[3], savedA[4], savedA[5], savedA[6], savedA[7], savedA[8]
	})
	savedRun, savedList := renameHostRun, listLinkNames
	t.Cleanup(func() { renameHostRun, listLinkNames = savedRun, savedList })
	oldStateDir, oldRunDir = oldS, filepath.Join(root, "run-ddgw")
	oldPAMFile, newPAMFile = filepath.Join(root, "pam", "ddgw"), filepath.Join(root, "pam", "dnsdir")
	oldNMFile, newNMFile = filepath.Join(root, "nm", "90-ddgw-unmanaged.conf"), filepath.Join(root, "nm", "90-dnsdir-unmanaged.conf")
	groupFile = filepath.Join(root, "group")
	oldFRRBackup, newFRRBackup = filepath.Join(root, "frr", "frr.conf.pre-ddgw"), filepath.Join(root, "frr", "frr.conf.pre-dnsdir")

	w(filepath.Join(oldS, "ddgw.conf"), `{"web":{"group":"ddgw","pam_service":"ddgw","tls_cert":"/var/lib/ddgw/ddgw-web.crt"}}`)
	w(filepath.Join(oldS, "ddgw-web.crt"), "CRT")
	w(filepath.Join(oldS, "ddgw-web.key"), "KEY")
	w(filepath.Join(oldS, "cluster.json"), "{}")
	w(filepath.Join(oldS, "updates", "guard.json"), `{"Prev":"/var/lib/ddgw/updates/ddgw.prev"}`)
	w(oldPAMFile, "auth")
	w(oldNMFile, "unmanaged-devices=interface-name:ddgw*\n")
	w(oldFRRBackup, "! old\n")
	w(groupFile, "root:x:0:\nddgw:x:900:alice\n")
	var ran []string
	renameHostRun = func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil
	}
	listLinkNames = func() []string { return []string{"lo", "eth0", "ddgw1.1", "ddgwt0", "dnsdir1.1"} }

	did, err := migrateOldInstall(newS)
	if err != nil || len(did) == 0 {
		t.Fatalf("did=%v err=%v", did, err)
	}
	for _, f := range []string{"dnsdir.conf", "dnsdir-web.crt", "dnsdir-web.key", "cluster.json", "updates/guard.json"} {
		if !fileExists(filepath.Join(newS, f)) {
			t.Errorf("%s must be in the new directory", f)
		}
	}
	if fileExists(oldS) {
		t.Error("the emptied old directory must be removed")
	}
	cb, _ := os.ReadFile(filepath.Join(newS, "dnsdir.conf"))
	if strings.Contains(string(cb), "ddgw") || !strings.Contains(string(cb), `"group":"dnsdir"`) || !strings.Contains(string(cb), `"pam_service":"dnsdir"`) || !strings.Contains(string(cb), "/var/lib/dnsdir/dnsdir-web.crt") {
		t.Errorf("the config must name the new group, PAM service and paths: %s", cb)
	}
	gb, _ := os.ReadFile(filepath.Join(newS, "updates", "guard.json"))
	if strings.Contains(string(gb), "ddgw") {
		t.Errorf("the update guard must name the new paths: %s", gb)
	}
	if !fileExists(newPAMFile) || fileExists(oldPAMFile) || !fileExists(newNMFile) || fileExists(oldNMFile) || !fileExists(newFRRBackup) || fileExists(oldFRRBackup) {
		t.Error("PAM, NetworkManager and FRR files must be renamed")
	}
	nm, _ := os.ReadFile(newNMFile)
	if !strings.Contains(string(nm), "dnsdir*") {
		t.Errorf("NetworkManager must ignore dnsdir*: %s", nm)
	}
	joined := strings.Join(ran, "|")
	if !strings.Contains(joined, "groupmod -n dnsdir ddgw") || !strings.Contains(joined, "ip link del ddgw1.1") {
		t.Errorf("group rename and leftover link removal expected, ran: %s", joined)
	}
	if strings.Contains(joined, "ddgwt0") || strings.Contains(joined, "dnsdir1.1") {
		t.Errorf("only ddgw<group>.<slot> links may be removed: %s", joined)
	}

	// a second start does nothing
	ran = nil
	if did, err := migrateOldInstall(newS); err != nil || len(did) != 0 || len(ran) != 0 {
		t.Fatalf("second run must be a no-op: %v %v %v", did, err, ran)
	}
}

func TestMigrateOldInstallNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	saved := []string{oldStateDir, oldRunDir, oldPAMFile, newPAMFile, oldNMFile, newNMFile, groupFile, oldFRRBackup, newFRRBackup}
	savedRun, savedList := renameHostRun, listLinkNames
	t.Cleanup(func() {
		oldStateDir, oldRunDir, oldPAMFile, newPAMFile, oldNMFile, newNMFile, groupFile, oldFRRBackup, newFRRBackup = saved[0], saved[1], saved[2], saved[3], saved[4], saved[5], saved[6], saved[7], saved[8]
		renameHostRun, listLinkNames = savedRun, savedList
	})
	renameHostRun = func(string, ...string) error { return nil }
	listLinkNames = func() []string { return nil }
	oldRunDir, groupFile = filepath.Join(root, "run"), filepath.Join(root, "group")
	oldPAMFile, newPAMFile = filepath.Join(root, "p1"), filepath.Join(root, "p2")
	oldNMFile, newNMFile = filepath.Join(root, "n1"), filepath.Join(root, "n2")
	oldFRRBackup, newFRRBackup = filepath.Join(root, "f1"), filepath.Join(root, "f2")
	oldStateDir = filepath.Join(root, "old")
	newS := filepath.Join(root, "new")
	os.MkdirAll(oldStateDir, 0o755)
	os.MkdirAll(newS, 0o755)
	os.WriteFile(filepath.Join(oldStateDir, "ddgw.conf"), []byte("OLD"), 0o600)
	os.WriteFile(filepath.Join(newS, "dnsdir.conf"), []byte("NEW"), 0o600)
	if _, err := migrateOldInstall(newS); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(newS, "dnsdir.conf")); string(b) != "NEW" {
		t.Fatalf("an existing dnsdir config must never be replaced, got %q", b)
	}
}
