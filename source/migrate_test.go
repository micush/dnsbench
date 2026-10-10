package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyConfig(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "etc-liras", "ddgw.conf")
	dst := filepath.Join(root, "var-lib-ddgw", "ddgw.conf")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Dir(legacy), 0o755))
	must(os.WriteFile(legacy, []byte(`{"groups":[{"group_id":3,"vip4":"10.3.0.1/24"}]}`), 0o640))
	must(os.WriteFile(filepath.Join(filepath.Dir(legacy), "ddgw-web.crt"), []byte("CRT"), 0o644))
	must(os.WriteFile(filepath.Join(filepath.Dir(legacy), "ddgw-web.key"), []byte("KEY"), 0o600))

	moved, err := migrateLegacyConfig(dst, legacy)
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v", moved, err)
	}
	dc, err := loadConfig(dst)
	if err != nil || len(dc.Groups) != 1 || dc.Groups[0].GroupID != 3 {
		t.Fatalf("the migrated config must load: %v %+v", err, dc)
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0o640 {
		t.Fatalf("the config keeps its mode, got %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(dst)); st.Mode().Perm() != 0o700 {
		t.Fatalf("the new directory is private, got %v", st.Mode().Perm())
	}
	for _, f := range []string{"ddgw-web.crt", "ddgw-web.key"} {
		if b, err := os.ReadFile(filepath.Join(filepath.Dir(dst), f)); err != nil || len(b) == 0 {
			t.Fatalf("%s must come along: %v", f, err)
		}
		if st, _ := os.Stat(filepath.Join(filepath.Dir(dst), f)); st.Mode().Perm() != 0o600 {
			t.Fatalf("%s must be private, got %v", f, st.Mode().Perm())
		}
	}
	if _, err := os.Stat(legacy); err == nil {
		t.Fatal("the old file must not stay in place (it would be used twice)")
	}
	for _, f := range []string{"ddgw-web.crt", "ddgw-web.key"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(legacy), f)); err == nil {
			t.Fatalf("%s must be moved, not copied", f)
		}
	}
	if _, err := os.Stat(filepath.Dir(legacy)); err == nil {
		t.Fatal("nothing may be left behind: the old directory must be gone once it is empty")
	}
	must(os.MkdirAll(filepath.Dir(legacy), 0o755))

	// a second run does nothing, and never overwrites
	must(os.WriteFile(legacy, []byte(`{"groups":[]}`), 0o600))
	if moved, err := migrateLegacyConfig(dst, legacy); err != nil || moved {
		t.Fatalf("existing config must win: moved=%v err=%v", moved, err)
	}
	if dc, _ := loadConfig(dst); len(dc.Groups) != 1 {
		t.Fatal("existing config was overwritten")
	}
	// nothing to migrate is not an error
	if moved, err := migrateLegacyConfig(filepath.Join(root, "other", "ddgw.conf"), filepath.Join(root, "missing.conf")); err != nil || moved {
		t.Fatalf("no legacy file: moved=%v err=%v", moved, err)
	}
}

func TestSaveCreatesPrivateDirectory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "new", "ddgw.conf")
	if err := newDaemonConfig().save(p); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Dir(p)); st.Mode().Perm() != 0o700 {
		t.Fatalf("a directory created for the config must be private, got %v", st.Mode().Perm())
	}
}

func TestDefaultLocations(t *testing.T) {
	if confPath != "/var/lib/ddgw/ddgw.conf" || filepath.Dir(confPath) != defaultStateDir {
		t.Fatalf("the config lives in the state directory: %s vs %s", confPath, defaultStateDir)
	}
	if statusSocket != "/run/ddgw/ddgw.sock" {
		t.Fatalf("socket: %s", statusSocket)
	}
}

func TestMissingConfigMeansNoGateways(t *testing.T) {
	dc, err := loadConfig(filepath.Join(t.TempDir(), "nope", "ddgw.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if dc.Groups == nil || len(dc.Groups) != 0 {
		t.Fatalf("a missing config must not invent a gateway: %+v", dc.Groups)
	}
	if dc.DNS.ListenPort != 53 || dc.Web.Listen == "" {
		t.Fatalf("defaults missing: %+v", dc.Web)
	}
}

func TestLegacyConfigPathIsRedirected(t *testing.T) {
	for _, in := range []string{"/etc/liras/ddgw.conf", "/etc/liras//ddgw.conf", "/etc/liras/../liras/ddgw.conf"} {
		if got := canonicalConf(in); got != confPath {
			t.Errorf("%q -> %q, want %q", in, got, confPath)
		}
	}
	for _, in := range []string{confPath, "./ddgw.conf", "/tmp/x/ddgw.conf"} {
		if got := canonicalConf(in); got != in {
			t.Errorf("%q must be left alone, got %q", in, got)
		}
	}
}
