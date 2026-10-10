package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newTestUpdater(t *testing.T) *Updater {
	u, err := NewUpdater(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func good(ver string) []srcFile {
	return []srcFile{
		{"ddgw/source/go.mod", []byte("module ddgw\n\ngo 1.24\n"), 0o644},
		{"ddgw/source/main.go", []byte("package main\nfunc main(){}\n"), 0o644},
		{"ddgw/source/VERSION", []byte(ver + "\n"), 0o644},
		{"ddgw/source/webui/app.js", []byte("//"), 0o644},
	}
}

func TestUpdateExtractAndTarballRoundTrip(t *testing.T) {
	u := newTestUpdater(t)
	if u.SourceVersion() != "" {
		t.Fatal("no source yet")
	}
	v, err := u.ExtractSource(makeTgz(t, good("7")))
	if err != nil || v != "7" || u.SourceVersion() != "7" {
		t.Fatalf("extract: %v %q", err, v)
	}
	if _, err := os.Stat(filepath.Join(u.sourceDir(), "source", "webui", "app.js")); err != nil {
		t.Fatal("subdirectories must be kept")
	}
	tb, tv, err := u.SourceTarball()
	if err != nil || tv != "7" {
		t.Fatal(err)
	}
	u2 := newTestUpdater(t)
	if v, err := u2.ExtractSource(tb); err != nil || v != "7" {
		t.Fatalf("a peer must be able to extract what we serve: %v", err)
	}
	// a newer upload replaces the tree (no stale files)
	f := good("8")
	f = f[:3]
	if _, err := u.ExtractSource(makeTgz(t, f)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(u.sourceDir(), "webui")); err == nil {
		t.Error("files of the previous release must not linger")
	}
}

func TestUpdateZipAccepted(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range good("9") {
		w, _ := zw.Create(f.name)
		w.Write(f.data)
	}
	zw.Close()
	u := newTestUpdater(t)
	if v, err := u.ExtractSource(buf.Bytes()); err != nil || v != "9" {
		t.Fatalf("zip: %v %q", err, v)
	}
}

func TestUpdateArchiveSafety(t *testing.T) {
	u := newTestUpdater(t)
	bad := func(name string, files []srcFile, want string) {
		t.Helper()
		_, err := u.ExtractSource(makeTgz(t, files))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err=%v, want %q", name, err, want)
		}
	}
	base := good("5")
	bad("path traversal", append(append([]srcFile{}, base...), srcFile{"ddgw/../../etc/evil", []byte("x"), 0o644}), "escapes")
	bad("absolute path", append(append([]srcFile{}, base...), srcFile{"/etc/evil", []byte("x"), 0o644}), "absolute")
	bad("backslash", append(append([]srcFile{}, base...), srcFile{"ddgw\\evil", []byte("x"), 0o644}), "unsafe")
	bad("no go.mod", base[1:], "not a ddgw source tree")
	bad("wrong module", []srcFile{{"go.mod", []byte("module other\n"), 0o644}, base[1], base[2]}, "not a ddgw source tree")
	bad("no main", []srcFile{base[0], base[2]}, "main.go missing")
	bad("bad version", []srcFile{base[0], base[1], {"ddgw/source/VERSION", []byte("v5"), 0o644}}, "VERSION")
	bad("semver", []srcFile{base[0], base[1], {"ddgw/source/VERSION", []byte("1.2.3"), 0o644}}, "VERSION")
	// symlink entries are refused
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "ddgw/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	tw.Close()
	gz.Close()
	if _, err := u.ExtractSource(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("symlink: %v", err)
	}
	if _, err := u.ExtractSource([]byte("hello")); err == nil {
		t.Error("garbage upload")
	}
	// size caps
	huge := append(append([]srcFile{}, base...), srcFile{"ddgw/big", bytes.Repeat([]byte("a"), maxSourceFile+1), 0o644})
	bad("huge file", huge, "too large")
	if u.SourceVersion() != "" {
		t.Error("a rejected upload must not leave a staged tree behind")
	}
}

func TestUpdateIntentPersistsAndDecides(t *testing.T) {
	dir := t.TempDir()
	u, _ := NewUpdater(dir)
	u.Push([]string{"b:1", "a:1"}, "alice")
	u.SetAuto(true, "alice", "a:1")
	u2, _ := NewUpdater(dir)
	i := u2.Intent()
	if !i.AutoAll || len(i.Pending) != 2 || i.Pending[0] != "a:1" {
		t.Fatalf("intent not persisted: %+v", i)
	}
	u2.Cancel([]string{"a:1"}, "alice")
	if u2.Intent().wants("zzz") != true { // auto-all wants everyone
		t.Fatal("auto-all wants every node")
	}
	u2.SetAuto(false, "alice", "a:1")
	if u2.Intent().wants("a:1") || !u2.Intent().wants("b:1") {
		t.Fatalf("wants: %+v", u2.Intent())
	}
	h := u2.History(0)
	if len(h) < 4 || h[0].Kind != "auto-off" {
		t.Fatalf("history: %+v", h)
	}
	if !versionGreater("10", "9") || versionGreater("9", "10") || versionGreater("x", "1") {
		t.Error("versionGreater must compare integers")
	}
}

// realTree copies the whole repository (the Go module is source/) into an archive
// layout with source/VERSION set to ver.
func realTree(t *testing.T, ver string) []srcFile {
	t.Helper()
	var files []srcFile
	err := filepath.WalkDir("..", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "ddgw" || strings.HasSuffix(p, ".tgz") || strings.HasSuffix(p, ".tmp") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("..", p)
		rel = filepath.ToSlash(rel)
		if rel == "source/VERSION" {
			b = []byte(ver + "\n")
		}
		files = append(files, srcFile{"ddgw/" + rel, b, 0o644})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestUpdateBuildApplyAndRollbackGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the daemon")
	}
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("no gcc")
	}
	u := newTestUpdater(t)
	if _, err := u.findGo(); err != nil {
		t.Skip(err)
	}
	next := itoa(mustInt(version()) + 1)
	if _, err := u.ExtractSource(makeTgz(t, realTree(t, next))); err != nil {
		t.Fatal(err)
	}
	// stand in for the running executable
	exe := filepath.Join(t.TempDir(), "ddgw")
	os.WriteFile(exe, []byte("#!/bin/sh\necho old\n"), 0o755)
	u.exePath = func() (string, error) { return exe, nil }

	got, err := u.Apply(context.Background(), "self:1", "tester")
	if err != nil || got != next {
		t.Fatalf("apply: %v %q", err, got)
	}
	out, err := exec.Command(exe, "--version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "ddgw v"+next {
		t.Fatalf("the executable must now be the new build: %q %v", out, err)
	}
	prev, _ := os.ReadFile(filepath.Join(u.dir, "ddgw.prev"))
	if !strings.Contains(string(prev), "echo old") {
		t.Fatal("the previous binary must be kept for rollback")
	}
	if u.Busy() == false {
		t.Error("stays busy until the restart")
	}
	// the new process claims it is the target version for guard purposes
	g, _ := u.readGuard()
	if g.State != "applying" || g.To != next {
		t.Fatalf("guard: %+v", g)
	}
	// simulate: the new version runs as `next` and crashes repeatedly
	oldEmbedded := embeddedVersion
	embeddedVersion = next
	defer func() { embeddedVersion = oldEmbedded }()
	for i := 1; i <= maxBootAttempts; i++ {
		if u.GuardOnStart() {
			t.Fatalf("attempt %d must still be allowed", i)
		}
	}
	if !u.GuardOnStart() {
		t.Fatal("after too many starts without confirmation the previous binary must be restored")
	}
	restored, _ := os.ReadFile(exe)
	if !strings.Contains(string(restored), "echo old") {
		t.Fatal("rollback must put the previous binary back")
	}
	if ev := u.LastEvent(); ev == nil || ev.Kind != "rolled-back" {
		t.Fatalf("history: %+v", ev)
	}
	if u.GuardOnStart() {
		t.Fatal("a rolled-back guard must not trigger again")
	}
	// not newer -> refused
	embeddedVersion = next + "0"
	u.updating = false
	if _, err := u.Apply(context.Background(), "s", "t"); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Fatalf("applying a non-newer version: %v", err)
	}
}

func TestUpdateGuardConfirm(t *testing.T) {
	u := newTestUpdater(t)
	u.writeGuard(bootGuard{State: "applying", From: "1", To: version(), At: timeNow()})
	if u.GuardOnStart() {
		t.Fatal("first start is fine")
	}
	u.GuardConfirm("self:1")
	g, _ := u.readGuard()
	if g.State != "ok" {
		t.Fatalf("confirmed guard: %+v", g)
	}
	if ev := u.LastEvent(); ev == nil || ev.Kind != "applied" {
		t.Fatalf("history: %+v", ev)
	}
}

// A relative --state-dir used to leak into GOPATH ("must be absolute path")
// and break the native build; paths are made absolute up front.
func TestNewMgmtMakesPathsAbsolute(t *testing.T) {
	d := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(d); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	dc := newDaemonConfig()
	mg, err := NewMgmt("ddgw.conf", "state", dc)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(mg.stateDir) || !filepath.IsAbs(mg.confPath) {
		t.Fatalf("paths must be absolute: %q %q", mg.stateDir, mg.confPath)
	}
}
