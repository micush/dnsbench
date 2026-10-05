package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ── archive helpers ──────────────────────────────────────────────────────────

type tent struct {
	name string
	body string
	typ  byte   // 0 = regular file
	link string // for links
	size int64  // overrides len(body) for the header (the body is then zeros)
}

func tgzOf(t *testing.T, ents []tent) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range ents {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: typ, Linkname: e.link}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
			if e.size > 0 {
				h.Size = e.size
			}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if e.size > 0 {
				if _, err := tw.Write(make([]byte, e.size)); err != nil {
					t.Fatal(err)
				}
			} else if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func releaseEnts(top, ver string) []tent {
	p := top
	if p != "" {
		p += "/"
	}
	return []tent{
		{name: p + "README.md", body: "# readme for v" + ver + "\n"},
		{name: p + "LICENSE.txt", body: "licence for v" + ver + "\n"},
		{name: p + "install.sh", body: "#!/bin/bash\n"},
		{name: p + "source/go.mod", body: "module dnsbench\n\ngo 1.22\n"},
		{name: p + "source/main.go", body: "package main\n\nfunc main() {}\n"},
		{name: p + "source/VERSION", body: ver + "\n"},
	}
}

func releaseTgz(t *testing.T, ver string) []byte { return tgzOf(t, releaseEnts("dnsbench", ver)) }

func nextVersion(t *testing.T) string {
	t.Helper()
	n, err := parseVer(version())
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatInt(n+1, 10)
}

func names(files []srcFile) map[string]string {
	m := map[string]string{}
	for _, f := range files {
		m[f.name] = string(f.data)
	}
	return m
}

// ── archive validation ───────────────────────────────────────────────────────

func TestSourceArchiveIsAcceptedAndTidied(t *testing.T) {
	ents := append(releaseEnts("dnsbench", "9"),
		tent{name: "dnsbench/.git/config", body: "x"},
		tent{name: "dnsbench/dnsbench", body: "binary leftover"},
		tent{name: "dnsbench/source/stray.tmp", body: "x"},
		tent{name: "dnsbench/docs/CHANGELOG.md", body: "x"},
	)
	files, err := parseSourceArchive(tgzOf(t, ents))
	if err != nil {
		t.Fatal(err)
	}
	m := names(files)
	for _, want := range []string{"README.md", "LICENSE.txt", "source/go.mod", "source/main.go", "source/VERSION", "docs/CHANGELOG.md", "install.sh"} {
		if _, ok := m[want]; !ok {
			t.Errorf("%s missing from %v", want, m)
		}
	}
	for _, gone := range []string{".git/config", "dnsbench", "source/stray.tmp"} {
		if _, ok := m[gone]; ok {
			t.Errorf("%s should have been dropped", gone)
		}
	}
}

func TestSourceArchiveWithoutATopDirectory(t *testing.T) {
	files, err := parseSourceArchive(tgzOf(t, releaseEnts("", "9")))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := names(files)["source/VERSION"]; !ok {
		t.Fatalf("got %v", names(files))
	}
}

func TestSourceArchiveAsZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range releaseEnts("dnsbench", "9") {
		w, _ := zw.Create(e.name)
		w.Write([]byte(e.body))
	}
	zw.Close()
	files, err := parseSourceArchive(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if names(files)["source/VERSION"] != "9\n" {
		t.Fatalf("got %v", names(files))
	}
}

func TestSourceArchiveRefusals(t *testing.T) {
	base := func(mod func([]tent) []tent) []byte { return tgzOf(t, mod(releaseEnts("dnsbench", "9"))) }
	replace := func(name, body string) func([]tent) []tent {
		return func(e []tent) []tent {
			for i := range e {
				if strings.HasSuffix(e[i].name, name) {
					e[i].body = body
				}
			}
			return e
		}
	}
	drop := func(name string) func([]tent) []tent {
		return func(e []tent) []tent {
			var out []tent
			for _, x := range e {
				if !strings.HasSuffix(x.name, name) {
					out = append(out, x)
				}
			}
			return out
		}
	}
	add := func(x tent) func([]tent) []tent { return func(e []tent) []tent { return append(e, x) } }
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"no go.mod", base(drop("source/go.mod")), "not a dnsbench source tree"},
		{"another project's module", base(replace("go.mod", "module ddgw\n")), "not a dnsbench source tree"},
		{"a module that merely starts with the name", base(replace("go.mod", "module dnsbenchx\n")), "not a dnsbench source tree"},
		{"no main.go", base(drop("source/main.go")), "main.go missing"},
		{"no VERSION", base(drop("source/VERSION")), "VERSION missing"},
		{"VERSION with a v", base(replace("VERSION", "v9\n")), "VERSION missing or not a plain integer"},
		{"VERSION with a dot", base(replace("VERSION", "9.1\n")), "VERSION missing or not a plain integer"},
		{"path traversal", base(add(tent{name: "dnsbench/../../etc/cron.d/x", body: "x"})), "escapes"},
		{"absolute path", base(add(tent{name: "/etc/passwd", body: "x"})), "absolute path"},
		{"backslash in a name", base(add(tent{name: `dnsbench\..\x`, body: "x"})), "unsafe path"},
		{"symlink", base(add(tent{name: "dnsbench/source/link", typ: tar.TypeSymlink, link: "/etc/shadow"})), "not a regular file"},
		{"hard link", base(add(tent{name: "dnsbench/source/hl", typ: tar.TypeLink, link: "source/main.go"})), "not a regular file"},
		{"device node", base(add(tent{name: "dnsbench/source/dev", typ: tar.TypeChar})), "not a regular file"},
		{"duplicate file", base(add(tent{name: "dnsbench/source/main.go", body: "other"})), "duplicate file"},
		{"empty archive", tgzOf(t, nil), "archive is empty"},
		{"not an archive", []byte("just some text, not an archive"), "not a .tgz"},
		{"truncated gzip", releaseTgz(t, "9")[:40], ""},
	}
	for _, c := range cases {
		_, err := parseSourceArchive(c.body)
		if err == nil {
			t.Errorf("%s: accepted", c.name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.want)
		}
	}
}

func TestSourceArchiveSizeLimits(t *testing.T) {
	big := append(releaseEnts("dnsbench", "9"), tent{name: "dnsbench/docs/huge.bin", size: maxSourceFile + 1})
	if _, err := parseSourceArchive(tgzOf(t, big)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("one huge file: %v", err)
	}
	var many []tent
	for i := 0; i < 5; i++ { // each file is allowed, together they are not
		many = append(many, tent{name: "dnsbench/docs/part" + strconv.Itoa(i), size: maxSourceFile - 1})
	}
	if _, err := parseSourceArchive(tgzOf(t, append(releaseEnts("dnsbench", "9"), many...))); err == nil || !strings.Contains(err.Error(), "too large when unpacked") {
		t.Errorf("total size: %v", err)
	}
	ents := releaseEnts("dnsbench", "9")
	for i := 0; i < maxSourceFiles; i++ {
		ents = append(ents, tent{name: "dnsbench/docs/f" + strconv.Itoa(i), body: "x"})
	}
	if _, err := parseSourceArchive(tgzOf(t, ents)); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("file count: %v", err)
	}
}

// ── staging ──────────────────────────────────────────────────────────────────

func TestStageStagesAReleaseAndReplacesThePreviousOne(t *testing.T) {
	u := newUpdater(t.TempDir(), true)
	if u.SourceVersion() != "" {
		t.Fatal("something staged on a fresh updater")
	}
	v1 := nextVersion(t)
	got, err := u.Stage(releaseTgz(t, v1), version())
	if err != nil || got != v1 || u.SourceVersion() != v1 {
		t.Fatalf("stage: %q %v (source %q)", got, err, u.SourceVersion())
	}
	n, _ := parseVer(v1)
	v2 := strconv.FormatInt(n+1, 10)
	if _, err := u.Stage(releaseTgz(t, v2), version()); err != nil || u.SourceVersion() != v2 {
		t.Fatalf("restage: %v (source %q)", err, u.SourceVersion())
	}
	if _, err := os.Stat(filepath.Join(u.dir, "source.old")); err == nil {
		t.Error("source.old left behind")
	}
	if _, err := os.Stat(filepath.Join(u.dir, "source.new")); err == nil {
		t.Error("source.new left behind")
	}
	// nothing in the staged tree is writable by group or others
	filepath.Walk(u.sourceDir(), func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().Perm()&0o022 != 0 {
			t.Errorf("%s is group/world writable (%v)", p, fi.Mode().Perm())
		}
		return nil
	})
}

func TestStageRefusesWhatIsNotNewer(t *testing.T) {
	u := newUpdater(t.TempDir(), true)
	for _, v := range []string{version(), "1"} {
		_, err := u.Stage(releaseTgz(t, v), version())
		if err == nil || !strings.Contains(err.Error(), "not newer") || !strings.Contains(err.Error(), "--allow-downgrade") {
			t.Errorf("v%s: %v", v, err)
		}
	}
	if u.SourceVersion() != "" {
		t.Error("an archive that was refused is staged")
	}
}

func TestStageKeepsThePreviousTreeWhenTheNewArchiveIsBad(t *testing.T) {
	u := newUpdater(t.TempDir(), true)
	v := nextVersion(t)
	if _, err := u.Stage(releaseTgz(t, v), version()); err != nil {
		t.Fatal(err)
	}
	bad := tgzOf(t, append(releaseEnts("dnsbench", "99"), tent{name: "dnsbench/../x", body: "x"}))
	if _, err := u.Stage(bad, version()); err == nil {
		t.Fatal("bad archive accepted")
	}
	if u.SourceVersion() != v {
		t.Errorf("staged tree changed to %q", u.SourceVersion())
	}
}

// ── apply ────────────────────────────────────────────────────────────────────

type fakeHost struct {
	u      *Updater
	exe    string
	docDir string
	builds atomic.Int32
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	dir := t.TempDir()
	h := &fakeHost{u: newUpdater(filepath.Join(dir, "state"), true)}
	h.docDir = filepath.Join(dir, "opt")
	os.MkdirAll(h.docDir, 0o755)
	h.exe = filepath.Join(h.docDir, "dnsbench")
	os.WriteFile(h.exe, []byte("OLD"), 0o755)
	h.u.exePath = func() (string, error) { return h.exe, nil }
	h.u.build = func(ctx context.Context) (string, error) {
		h.builds.Add(1)
		p := filepath.Join(h.u.dir, "dnsbench.new")
		return p, os.WriteFile(p, []byte("NEW"), 0o755)
	}
	h.u.smoke = nil
	return h
}

func (h *fakeHost) stage(t *testing.T) string {
	t.Helper()
	v := nextVersion(t)
	if _, err := h.u.Stage(releaseTgz(t, v), version()); err != nil {
		t.Fatal(err)
	}
	return v
}

func readStr(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApplyInstallsKeepsThePreviousBinaryAndArmsTheGuard(t *testing.T) {
	h := newFakeHost(t)
	os.WriteFile(filepath.Join(h.docDir, "README.md"), []byte("old readme"), 0o644)
	v := h.stage(t)
	got, err := h.u.Apply(context.Background(), "alice")
	if err != nil || got != v {
		t.Fatalf("apply: %q %v", got, err)
	}
	if readStr(t, h.exe) != "NEW" {
		t.Error("the new binary was not installed")
	}
	if fi, _ := os.Stat(h.exe); fi.Mode().Perm() != 0o755 {
		t.Errorf("installed mode %v", fi.Mode().Perm())
	}
	if readStr(t, filepath.Join(h.u.dir, "dnsbench.prev")) != "OLD" {
		t.Error("the previous binary was not kept for rollback")
	}
	if g, ok := h.u.readGuard(); !ok || g.State != "applying" || g.To != v || g.From != version() || g.Exe != h.exe {
		t.Errorf("guard %+v", g)
	}
	if !strings.Contains(readStr(t, filepath.Join(h.docDir, "README.md")), "readme for v"+v) || !strings.Contains(readStr(t, filepath.Join(h.docDir, "LICENSE.txt")), "licence for v"+v) {
		t.Error("the docs next to the binary were not refreshed")
	}
	if !h.u.Busy() || h.u.Phase() != "installed, restarting" {
		t.Errorf("busy=%v phase=%q", h.u.Busy(), h.u.Phase())
	}
	if _, err := os.Stat(filepath.Join(h.u.dir, "dnsbench.new")); err == nil {
		t.Error("the build output was left behind")
	}
	if left, _ := filepath.Glob(filepath.Join(h.docDir, "*.tmp")); len(left) > 0 {
		t.Errorf("temp files left next to the binary: %v", left)
	}
}

func TestApplyFailuresChangeNothingAndAreRecorded(t *testing.T) {
	t.Run("build fails", func(t *testing.T) {
		h := newFakeHost(t)
		h.stage(t)
		h.u.build = func(context.Context) (string, error) { return "", errors.New("go build failed: boom") }
		if _, err := h.u.Apply(context.Background(), "alice"); err == nil {
			t.Fatal("no error")
		}
		checkUntouched(t, h, "boom")
	})
	t.Run("the new build does not start", func(t *testing.T) {
		h := newFakeHost(t)
		h.stage(t)
		h.u.smoke = func(string) error { return errors.New("the new build exited right after starting") }
		if _, err := h.u.Apply(context.Background(), "alice"); err == nil {
			t.Fatal("no error")
		}
		checkUntouched(t, h, "exited right after starting")
	})
	t.Run("the binary cannot be replaced, found before building", func(t *testing.T) {
		h := newFakeHost(t)
		h.stage(t)
		h.u.exePath = func() (string, error) { return "/nonexistent-dir/dnsbench", nil }
		_, err := h.u.Apply(context.Background(), "alice")
		if err == nil || !strings.Contains(err.Error(), "install.sh") {
			t.Fatalf("error %v should say to run install.sh", err)
		}
		if h.builds.Load() != 0 {
			t.Error("a build was started although the binary cannot be replaced")
		}
		if lf := h.u.lastFailure(); lf == nil {
			t.Error("not recorded")
		}
	})
	t.Run("nothing staged", func(t *testing.T) {
		h := newFakeHost(t)
		if _, err := h.u.Apply(context.Background(), "alice"); err == nil || !strings.Contains(err.Error(), "no source tree staged") {
			t.Fatalf("%v", err)
		}
		if h.u.Busy() {
			t.Error("still busy after an error")
		}
	})
}

func checkUntouched(t *testing.T, h *fakeHost, wantDetail string) {
	t.Helper()
	if readStr(t, h.exe) != "OLD" {
		t.Error("the running binary was replaced")
	}
	if _, ok := h.u.readGuard(); ok {
		t.Error("a boot guard was written for an update that never installed")
	}
	if h.u.Busy() {
		t.Error("still busy after a failure")
	}
	lf := h.u.lastFailure()
	if lf == nil || !strings.Contains(lf.Detail, wantDetail) || lf.By != "alice" {
		t.Errorf("failure not recorded properly: %+v", lf)
	}
}

func TestApplyRefusesToRunTwiceAtOnce(t *testing.T) {
	h := newFakeHost(t)
	h.stage(t)
	release := make(chan struct{})
	h.u.build = func(context.Context) (string, error) {
		<-release
		p := filepath.Join(h.u.dir, "dnsbench.new")
		return p, os.WriteFile(p, []byte("NEW"), 0o755)
	}
	errc := make(chan error, 1)
	go func() { _, err := h.u.Apply(context.Background(), "alice"); errc <- err }()
	deadline := time.Now().Add(2 * time.Second)
	for !h.u.Busy() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := h.u.Apply(context.Background(), "bob"); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("second apply: %v", err)
	}
	if _, err := h.u.Stage(releaseTgz(t, "99999"), version()); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Errorf("stage during a build: %v", err)
	}
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestApplyRefusesAStagedVersionThatIsNotNewer(t *testing.T) {
	h := newFakeHost(t)
	// stage by hand, as if the running binary had been replaced since (by install.sh, say)
	h.stage(t)
	vf := filepath.Join(h.u.sourceDir(), "source", "VERSION")
	os.WriteFile(vf, []byte(version()+"\n"), 0o644)
	if _, err := h.u.Apply(context.Background(), "alice"); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Fatalf("%v", err)
	}
	if h.builds.Load() != 0 {
		t.Error("built something that is not newer")
	}
}

// ── boot guard ───────────────────────────────────────────────────────────────

func armGuard(h *fakeHost, to string) {
	prev := filepath.Join(h.u.dir, "dnsbench.prev")
	os.MkdirAll(h.u.dir, 0o700)
	os.WriteFile(prev, []byte("PREVIOUS"), 0o755)
	os.WriteFile(h.exe, []byte("BROKEN"), 0o755)
	h.u.writeGuard(bootGuard{State: "applying", From: "1", To: to, Prev: prev, Exe: h.exe})
}

func TestGuardRestoresThePreviousBinaryAfterRepeatedFailedStarts(t *testing.T) {
	h := newFakeHost(t)
	armGuard(h, version()) // the binary now running is the one that was installed
	for i := 1; i <= maxBootAttempts; i++ {
		if h.u.GuardOnStart() {
			t.Fatalf("rolled back on start %d, before the limit", i)
		}
		if g, _ := h.u.readGuard(); g.Attempts != i {
			t.Fatalf("attempts after start %d: %d", i, g.Attempts)
		}
	}
	if readStr(t, h.exe) != "BROKEN" {
		t.Fatal("restored too early")
	}
	if !h.u.GuardOnStart() {
		t.Fatal("no rollback after the limit")
	}
	if readStr(t, h.exe) != "PREVIOUS" {
		t.Error("the previous binary was not restored")
	}
	if g, _ := h.u.readGuard(); g.State != "rolled-back" {
		t.Errorf("state %q", g.State)
	}
	hist := h.u.History(0)
	if len(hist) != 1 || hist[0].Kind != "rolled-back" {
		t.Errorf("history %+v", hist)
	}
	// the process that started after the rollback runs the old version: it must say so, and must not roll back again
	if h.u.GuardOnStart() {
		t.Error("rolled back twice")
	}
}

func TestGuardIgnoresAnUpdateThatIsNotTheRunningBinary(t *testing.T) {
	h := newFakeHost(t)
	armGuard(h, "999999") // something else is running now, e.g. install.sh put another version in place
	if h.u.GuardOnStart() {
		t.Fatal("rolled back a binary that is not the one the guard was armed for")
	}
	if g, _ := h.u.readGuard(); g.State != "ok" {
		t.Errorf("state %q", g.State)
	}
	if readStr(t, h.exe) != "BROKEN" {
		t.Error("touched the binary")
	}
}

func TestGuardConfirmRecordsASuccessfulUpdateOnce(t *testing.T) {
	h := newFakeHost(t)
	armGuard(h, version())
	h.u.GuardOnStart()
	h.u.GuardConfirm()
	h.u.GuardConfirm()
	hist := h.u.History(0)
	if len(hist) != 1 || hist[0].Kind != "applied" || hist[0].To != version() || hist[0].From != "1" {
		t.Fatalf("history %+v", hist)
	}
	if g, _ := h.u.readGuard(); g.State != "ok" {
		t.Errorf("state %q", g.State)
	}
	if h.u.GuardOnStart() {
		t.Error("a confirmed update was rolled back")
	}
}

func TestRolledBackNoticeOnlyWhileThePreviousVersionRuns(t *testing.T) {
	h := newFakeHost(t)
	h.u.writeGuard(bootGuard{State: "rolled-back", From: version(), To: "999"})
	if n := h.u.RolledBackNotice(); !strings.Contains(n, "v999 was rolled back to v"+version()) {
		t.Errorf("notice %q", n)
	}
	h.u.writeGuard(bootGuard{State: "rolled-back", From: "1", To: "999"}) // an older rollback
	if n := h.u.RolledBackNotice(); n != "" {
		t.Errorf("stale notice %q", n)
	}
}

// ── history ──────────────────────────────────────────────────────────────────

func TestHistoryIsKeptAcrossRestartsAndCapped(t *testing.T) {
	dir := t.TempDir()
	u := newUpdater(dir, true)
	for i := 0; i < maxHistory+25; i++ {
		u.Record(UpdateEvent{Kind: "uploaded", To: strconv.Itoa(i)})
	}
	u2 := newUpdater(dir, true)
	h := u2.History(0)
	if len(h) != maxHistory || h[0].To != strconv.Itoa(maxHistory+24) {
		t.Fatalf("len %d, newest %q", len(h), h[0].To)
	}
	if got := u2.History(3); len(got) != 3 {
		t.Errorf("limit: %d", len(got))
	}
	fi, err := os.Stat(filepath.Join(dir, "update", "history.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("history.json: %v %v", fi, err)
	}
}

func TestLastFailureIsForgottenOnceSomethingNewerHappens(t *testing.T) {
	u := newUpdater(t.TempDir(), true)
	if u.lastFailure() != nil {
		t.Fatal("failure on a fresh updater")
	}
	u.noteFailure("1", "2", "boom", "alice")
	if lf := u.lastFailure(); lf == nil || lf.To != "2" {
		t.Fatalf("%+v", lf)
	}
	u.Record(UpdateEvent{Kind: "uploaded", To: "3"})
	if u.lastFailure() != nil {
		t.Error("an old failure is still reported after a newer upload")
	}
}

// ── canReplace, versions ─────────────────────────────────────────────────────

func TestCanReplace(t *testing.T) {
	dir := t.TempDir()
	if err := canReplace(filepath.Join(dir, "dnsbench")); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("the probe left %v behind", left)
	}
	err := canReplace("/nonexistent-dir/dnsbench")
	if err == nil || !strings.Contains(err.Error(), "ReadWritePaths") || !strings.Contains(err.Error(), "/nonexistent-dir") {
		t.Errorf("error %v", err)
	}
}

func TestVersionComparison(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"10", "9", true}, {"9", "10", false}, {"9", "9", false}, {"", "9", false}, {"9", "", false}, {"09", "8", false}, {"v9", "8", false}} {
		if got := versionGreater(c.a, c.b); got != c.want {
			t.Errorf("versionGreater(%q,%q)=%v", c.a, c.b, got)
		}
	}
}

// ── a real toolchain ─────────────────────────────────────────────────────────

func goEnvCache(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		t.Skip("no go toolchain on PATH")
	}
	return strings.TrimSpace(string(out))
}

// realBuilder stages a tiny program as the "source tree" so the real Build runs the real
// toolchain; the full dnsbench source takes a minute to build and needs libpam.
func realBuilder(t *testing.T, ver, goMod, mainGo string) *Updater {
	t.Helper()
	oldPAM, oldSize := requirePAM, minBinarySize
	requirePAM, minBinarySize = false, 1
	t.Cleanup(func() { requirePAM, minBinarySize = oldPAM, oldSize })
	u := newUpdater(t.TempDir(), true)
	u.goCacheDir = goEnvCache(t)
	src := filepath.Join(u.sourceDir(), "source")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "go.mod"), []byte(goMod), 0o644)
	os.WriteFile(filepath.Join(src, "main.go"), []byte(mainGo), 0o644)
	os.WriteFile(filepath.Join(src, "VERSION"), []byte(ver+"\n"), 0o644)
	return u
}

const tinyMod = "module dnsbench\n\ngo 1.22\n"

func tinyMain(ver string) string {
	return `package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("dnsbench ` + ver + `")
		return
	}
	time.Sleep(time.Hour)
}
`
}

func TestBuildCompilesAndChecksTheBinaryWithTheRealToolchain(t *testing.T) {
	u := realBuilder(t, "99", tinyMod, tinyMain("99"))
	bin, err := u.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(bin); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("binary %v %v", fi, err)
	}
	if left, _ := os.ReadDir(filepath.Join(u.dir, "tmp")); len(left) != 0 {
		t.Errorf("scratch space left behind: %v", left)
	}
}

func TestBuildRefusesABinaryThatReportsAnotherVersion(t *testing.T) {
	u := realBuilder(t, "98", tinyMod, tinyMain("99")) // VERSION says 98, the program says 99
	if _, err := u.Build(context.Background()); err == nil || !strings.Contains(err.Error(), `reports "dnsbench 99", expected "dnsbench 98"`) {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(u.dir, "dnsbench.new")); err == nil {
		t.Error("the rejected binary was left in place")
	}
}

func TestBuildReportsCompilerErrors(t *testing.T) {
	u := realBuilder(t, "99", tinyMod, "package main\n\nfunc main() { thisDoesNotExist() }\n")
	_, err := u.Build(context.Background())
	if err == nil || !strings.Contains(err.Error(), "go build failed") || !strings.Contains(err.Error(), "thisDoesNotExist") {
		t.Fatalf("%v", err)
	}
}

func TestBuildNeedsPAMWhenTheRunningBinaryHasIt(t *testing.T) {
	u := realBuilder(t, "99", tinyMod, tinyMain("99"))
	requirePAM = true // restored by realBuilder's cleanup
	if _, err := u.Build(context.Background()); err == nil || !strings.Contains(err.Error(), "no PAM support") {
		t.Fatalf("%v", err)
	}
}

func TestAReleaseThatNeedsANewerGoSaysSo(t *testing.T) {
	u := realBuilder(t, "99", "module dnsbench\n\ngo 1.99\n", tinyMain("99"))
	if m := u.goMinor(); m != 99 {
		t.Fatalf("goMinor %d", m)
	}
	_, err := u.Build(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no Go toolchain 1.99 or newer") {
		t.Fatalf("%v", err)
	}
	// and never asks for less than install.sh does
	u2 := realBuilder(t, "99", "module dnsbench\n\ngo 1.10\n", tinyMain("99"))
	if m := u2.goMinor(); m != minGoMinorFloor {
		t.Errorf("goMinor %d", m)
	}
}

// ── the smoke test ───────────────────────────────────────────────────────────

func buildProg(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module tiny\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644)
	out := filepath.Join(dir, "tiny")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=-mod=readonly", "GOPROXY=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build a test program: %v\n%s", err, b)
	}
	return out
}

func serveProg(status string) string {
	return `package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := ""
	for i, a := range os.Args {
		if a == "--port" {
			port = os.Args[i+1]
		}
	}
	fmt.Println("tiny server starting")
	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(` + status + `) })
	http.ListenAndServe("127.0.0.1:"+port, nil)
}
`
}

func TestSmokeTest(t *testing.T) {
	old := smokeDeadline
	smokeDeadline = 2 * time.Second
	t.Cleanup(func() { smokeDeadline = old })
	dir := t.TempDir()

	if err := smokeTest(buildProg(t, serveProg("200")), dir); err != nil {
		t.Errorf("a build that serves its login page failed the smoke test: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "smoke-*")); len(left) != 0 {
		t.Errorf("scratch state left behind: %v", left)
	}
	if err := smokeTest(buildProg(t, serveProg("500")), dir); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("HTTP 500: %v", err)
	}
	if err := smokeTest(buildProg(t, "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc main() { fmt.Println(\"cannot open the database\"); os.Exit(3) }\n"), dir); err == nil ||
		!strings.Contains(err.Error(), "exited right after starting") || !strings.Contains(err.Error(), "cannot open the database") {
		t.Errorf("early exit should be reported with its output: %v", err)
	}
	if err := smokeTest(buildProg(t, "package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(time.Hour) }\n"), dir); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("never listens: %v", err)
	}
	if err := smokeTest(filepath.Join(dir, "does-not-exist"), dir); err == nil {
		t.Error("a missing binary passed")
	}
}

func TestSmokeTestRunsTheBinaryWithoutTheServicesEnvironment(t *testing.T) {
	t.Setenv("LOGIN_GROUP", "should-not-be-seen")
	src := `package main

import (
	"net/http"
	"os"
)

func main() {
	port := ""
	for i, a := range os.Args {
		if a == "--port" {
			port = os.Args[i+1]
		}
	}
	code := 200
	if os.Getenv("LOGIN_GROUP") != "" {
		code = 500
	}
	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
	http.ListenAndServe("127.0.0.1:"+port, nil)
}
`
	if err := smokeTest(buildProg(t, src), t.TempDir()); err != nil {
		t.Errorf("the service's environment leaked into the smoke test: %v", err)
	}
}

// The boot guard has to count a start before anything that can fail. A start that dies earlier than
// the guard runs is never counted, so a bad update crash-loops forever instead of being rolled back.
func TestGuardAtStartCountsEveryStartAndRestoresOnTheFourth(t *testing.T) {
	h := newFakeHost(t)
	armGuard(h, version())
	stateDir := filepath.Dir(h.u.dir)
	var restarted []string
	restart := func(p string) error { restarted = append(restarted, p); return nil }
	for i := 1; i <= maxBootAttempts; i++ {
		if guardAtStart(stateDir, h.exe, restart) {
			t.Fatalf("restored on start %d, before the limit", i)
		}
	}
	if len(restarted) != 0 || readStr(t, h.exe) != "BROKEN" {
		t.Fatal("restored too early")
	}
	if !guardAtStart(stateDir, h.exe, restart) {
		t.Fatal("no rollback after the limit")
	}
	if len(restarted) != 1 || restarted[0] != h.exe || readStr(t, h.exe) != "PREVIOUS" {
		t.Errorf("restarted %v, binary %q", restarted, readStr(t, h.exe))
	}
	// nothing armed: starting is not touched at all
	h2 := newFakeHost(t)
	if guardAtStart(filepath.Dir(h2.u.dir), h2.exe, restart) || len(restarted) != 1 {
		t.Error("guardAtStart did something with no update pending")
	}
}

// main() must call the guard before run() and also when the configuration is rejected, the two
// ways a release can fail before it serves anything. (The end-to-end rollback test is the real check;
// this keeps the order from being changed by accident.)
func TestMainCountsStartsBeforeAnythingThatCanFail(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func main()")
	j := strings.Index(src, "func selfPath()")
	if i < 0 || j < 0 {
		t.Fatal("main.go changed shape; update this test")
	}
	main := src[i:j]
	if strings.Count(main, "guardAtStart(") != 2 {
		t.Errorf("main() should call guardAtStart on the config-error path and before run(): %d calls", strings.Count(main, "guardAtStart("))
	}
	if g, r := strings.Index(main, "guardAtStart(cfg.StateDir"), strings.Index(main, "run(cfg)"); g < 0 || r < 0 || g > r {
		t.Error("guardAtStart(cfg.StateDir ...) must come before run(cfg)")
	}
	if e, g := strings.Index(main, "os.Exit(2)"), strings.Index(main, "guardAtStart("); g < 0 || e < g {
		t.Error("the config-error path must count the start before it exits")
	}
	if strings.Contains(src[j:], "GuardOnStart()") && !strings.Contains(src[j:], "func guardAtStart") {
		t.Error("run() calls GuardOnStart too late")
	}
	if strings.Count(src, "GuardOnStart()") != 1 {
		t.Errorf("GuardOnStart should be called from exactly one place (guardAtStart), found %d", strings.Count(src, "GuardOnStart()"))
	}
}
