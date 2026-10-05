package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Software updates from the web UI, after the updater in ddgw: a release archive (the
// dnsbench_vN.tgz this project ships) is uploaded, checked and staged under STATE_DIR/update,
// built natively on this host (the PAM binding is cgo, so a binary is only portable to a host
// with the same libpam), started once on a spare port to prove it runs, and swapped in with a
// re-exec. A boot guard restores the previous binary if the new one keeps failing to start.
//
// Unlike ddgw there is one node and no cluster, so there is no "update everyone", no queue and no
// pulling from a peer: upload, then update this server.
//
// Anyone who can sign in can upload source that this host then builds and runs as root. That is
// why ALLOW_UPDATES exists, and why every upload and update is logged with the user's name.

const (
	maxSourceBytes   = 64 << 20 // unpacked
	maxSourceFile    = 16 << 20
	maxSourceFiles   = 2000
	maxUploadBytes   = 32 << 20
	buildTimeout     = 10 * time.Minute
	maxBootAttempts  = 3
	bootConfirmAfter = 60 * time.Second
	maxHistory       = 200
	minGoMinorFloor  = 22 // what install.sh requires; a staged go.mod may ask for more
)

// Overridden by tests: the real build is large and needs libpam; a test program is neither.
var (
	minBinarySize int64 = 1 << 20
	requirePAM          = pamAvailable
	smokeDeadline       = 20 * time.Second
)

// UpdateEvent is one entry of the update history.
type UpdateEvent struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"` // uploaded | applied | failed | rolled-back
	From   string    `json:"from,omitempty"`
	To     string    `json:"to,omitempty"`
	Detail string    `json:"detail,omitempty"`
	By     string    `json:"by,omitempty"`
}

type bootGuard struct {
	State    string    `json:"state"` // applying | ok | rolled-back
	From     string    `json:"from"`
	To       string    `json:"to"`
	Prev     string    `json:"prev"`
	Exe      string    `json:"exe"`
	Attempts int       `json:"attempts"`
	At       time.Time `json:"at"`
}

// Updater owns the staged source tree, the build scratch space, the boot guard and the history.
type Updater struct {
	dir        string // <state>/update
	enabled    bool
	exePath    func() (string, error)
	restartFn  func()                                    // leave the process so main re-execs the installed binary
	build      func(ctx context.Context) (string, error) // the real Build unless a test replaces it
	goCacheDir string                                    // "" = <dir>/gocache; tests share the toolchain's own cache
	smoke      func(bin string) error                    // start the new binary once; tests may skip it

	mu       sync.Mutex
	hist     []UpdateEvent
	updating bool
	phase    string
	waiting  string

	toolMu    sync.Mutex
	toolAt    time.Time
	toolPath  string
	toolErr   error
	toolMinor int
}

func newUpdater(stateDir string, enabled bool) *Updater {
	u := &Updater{dir: filepath.Join(stateDir, "update"), enabled: enabled, exePath: os.Executable}
	u.build = func(ctx context.Context) (string, error) { return u.Build(ctx) }
	u.smoke = func(bin string) error { return smokeTest(bin, u.dir) }
	if b, err := os.ReadFile(filepath.Join(u.dir, "history.json")); err == nil {
		json.Unmarshal(b, &u.hist)
	}
	return u
}

func (u *Updater) sourceDir() string { return filepath.Join(u.dir, "source") }

func (u *Updater) addEventLocked(e UpdateEvent) {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	u.hist = append(u.hist, e)
	if len(u.hist) > maxHistory {
		u.hist = u.hist[len(u.hist)-maxHistory:]
	}
	if err := os.MkdirAll(u.dir, 0o700); err == nil {
		b, _ := json.MarshalIndent(u.hist, "", "  ")
		writeFileAtomic(filepath.Join(u.dir, "history.json"), b, 0o600)
	}
}

// Record appends an event to the history.
func (u *Updater) Record(e UpdateEvent) {
	u.mu.Lock()
	u.addEventLocked(e)
	u.mu.Unlock()
}

// History returns events newest first; limit <= 0 means all of them.
func (u *Updater) History(limit int) []UpdateEvent {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]UpdateEvent, 0, len(u.hist))
	for i := len(u.hist) - 1; i >= 0; i-- {
		out = append(out, u.hist[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (u *Updater) historyLen() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.hist)
}

func (u *Updater) Phase() string   { u.mu.Lock(); defer u.mu.Unlock(); return u.phase }
func (u *Updater) Busy() bool      { u.mu.Lock(); defer u.mu.Unlock(); return u.updating }
func (u *Updater) Waiting() string { u.mu.Lock(); defer u.mu.Unlock(); return u.waiting }

// SetWaiting records why the installed update is not restarting yet ("" = not waiting).
func (u *Updater) SetWaiting(why string) {
	u.mu.Lock()
	changed := u.waiting != why
	u.waiting = why
	u.mu.Unlock()
	if changed && why != "" {
		log.Printf("update: holding back the restart: %s", why)
	}
}

// lastFailure is the newest history entry when it is a failed attempt, so the page can say why
// the staged version is not installed. It is forgotten as soon as anything newer happens.
func (u *Updater) lastFailure() *UpdateEvent {
	u.mu.Lock()
	defer u.mu.Unlock()
	if n := len(u.hist); n > 0 && u.hist[n-1].Kind == "failed" {
		e := u.hist[n-1]
		return &e
	}
	return nil
}

func (u *Updater) noteFailure(from, to, detail, by string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.addEventLocked(UpdateEvent{Kind: "failed", From: from, To: to, Detail: detail, By: by})
	log.Printf("update: v%s -> v%s failed: %s", from, to, detail)
}

// ── versions ─────────────────────────────────────────────────────────────────

func parseVer(v string) (int64, error) {
	v = strings.TrimSpace(v)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != v {
		return 0, fmt.Errorf("version %q is not a plain integer", v)
	}
	return n, nil
}

func versionGreater(a, b string) bool {
	x, e1 := parseVer(a)
	y, e2 := parseVer(b)
	return e1 == nil && e2 == nil && x > y
}

// SourceVersion is the VERSION of the staged source tree ("" if none).
func (u *Updater) SourceVersion() string {
	b, err := os.ReadFile(filepath.Join(u.sourceDir(), "source", "VERSION"))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(b))
	if _, err := parseVer(v); err != nil {
		return ""
	}
	return v
}

// ── source archive handling ──────────────────────────────────────────────────

var (
	versionFileRe = regexp.MustCompile(`^[0-9]+\n?$`)
	moduleLineRe  = regexp.MustCompile(`(?m)^module\s+dnsbench\s*$`)
	goDirectiveRe = regexp.MustCompile(`(?m)^go\s+1\.(\d+)`)
)

type srcFile struct {
	name string
	data []byte
	mode fs.FileMode
}

// cleanArchiveName normalises an entry name and rejects anything that could escape the
// directory it is unpacked into.
func cleanArchiveName(raw string) (string, error) {
	if raw == "" || strings.ContainsAny(raw, "\x00\\") {
		return "", fmt.Errorf("unsafe path %q in archive", raw)
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("absolute path %q in archive", raw)
	}
	c := path.Clean(raw)
	if c == "." {
		return "", nil
	}
	for _, p := range strings.Split(c, "/") {
		if p == ".." {
			return "", fmt.Errorf("path %q in archive escapes the source directory", raw)
		}
	}
	return c, nil
}

type srcCollector struct {
	files []srcFile
	total int64
}

func (c *srcCollector) add(name string, data []byte, mode fs.FileMode) error {
	if len(c.files) >= maxSourceFiles {
		return fmt.Errorf("archive has more than %d files", maxSourceFiles)
	}
	c.total += int64(len(data))
	if c.total > maxSourceBytes {
		return errors.New("archive is too large when unpacked")
	}
	c.files = append(c.files, srcFile{name, data, mode})
	return nil
}

func readLimited(r io.Reader, limit int64, name string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("file %q in archive is too large", name)
	}
	return b, nil
}

func parseSourceArchive(body []byte) ([]srcFile, error) {
	var c srcCollector
	var err error
	switch {
	case len(body) > 4 && bytes.HasPrefix(body, []byte("PK\x03\x04")):
		err = collectZip(body, &c)
	case len(body) > 2 && body[0] == 0x1f && body[1] == 0x8b:
		err = collectTgz(body, &c)
	default:
		err = errors.New("not a .tgz/.tar.gz or .zip archive")
	}
	if err != nil {
		return nil, err
	}
	return finishSource(c.files)
}

func collectTgz(body []byte, c *srcCollector) error {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return err
	}
	tr := tar.NewReader(io.LimitReader(gz, maxSourceBytes*2))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading archive: %w", err)
		}
		switch h.Typeflag {
		case tar.TypeDir, tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		case tar.TypeReg:
		default:
			return fmt.Errorf("archive entry %q is not a regular file (links and devices are refused)", h.Name)
		}
		name, err := cleanArchiveName(h.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		if h.Size > maxSourceFile {
			return fmt.Errorf("file %q in archive is too large", h.Name)
		}
		data, err := readLimited(tr, maxSourceFile, h.Name)
		if err != nil {
			return err
		}
		if err := c.add(name, data, fs.FileMode(h.Mode)&0o755|0o644); err != nil {
			return err
		}
	}
}

func collectZip(body []byte, c *srcCollector) error {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() {
			return fmt.Errorf("archive entry %q is not a regular file (links and devices are refused)", f.Name)
		}
		name, err := cleanArchiveName(f.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		if f.UncompressedSize64 > maxSourceFile {
			return fmt.Errorf("file %q in archive is too large", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		data, err := readLimited(rc, maxSourceFile, f.Name)
		rc.Close()
		if err != nil {
			return err
		}
		if err := c.add(name, data, f.Mode()&0o755|0o644); err != nil {
			return err
		}
	}
	return nil
}

// finishSource strips a single common top directory, drops version-control and binary leftovers
// and checks the tree really is a dnsbench source tree.
func finishSource(files []srcFile) ([]srcFile, error) {
	if len(files) == 0 {
		return nil, errors.New("archive is empty")
	}
	top := ""
	same := true
	for i, f := range files {
		p, _, found := strings.Cut(f.name, "/")
		if !found {
			same = false
			break
		}
		if i == 0 {
			top = p
		} else if p != top {
			same = false
			break
		}
	}
	var out []srcFile
	seen := map[string]bool{}
	for _, f := range files {
		name := f.name
		if same {
			name = strings.TrimPrefix(name, top+"/")
		}
		if name == ".git" || strings.HasPrefix(name, ".git/") || name == "dnsbench" || strings.HasSuffix(name, ".tmp") {
			continue
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate file %q in archive", name)
		}
		seen[name] = true
		out = append(out, srcFile{name, f.data, f.mode})
	}
	get := func(n string) []byte {
		for _, f := range out {
			if f.name == n {
				return f.data
			}
		}
		return nil
	}
	if gm := get("source/go.mod"); gm == nil || !moduleLineRe.Match(gm) {
		return nil, errors.New(`not a dnsbench source tree (source/go.mod with "module dnsbench" not found in the archive)`)
	}
	if get("source/main.go") == nil {
		return nil, errors.New("not a dnsbench source tree (source/main.go missing)")
	}
	if v := get("source/VERSION"); v == nil || !versionFileRe.Match(v) {
		return nil, errors.New("source/VERSION missing or not a plain integer")
	}
	return out, nil
}

// Stage validates an uploaded archive and stages it as the source tree, replacing the previous
// one. It returns the new VERSION. An archive that is not newer than running is refused: this
// page only moves forward (install.sh --allow-downgrade is the way back).
func (u *Updater) Stage(body []byte, running string) (string, error) {
	u.mu.Lock()
	busy := u.updating
	u.mu.Unlock()
	if busy {
		return "", errors.New("an update is in progress on this server; wait for it to finish")
	}
	files, err := parseSourceArchive(body)
	if err != nil {
		return "", err
	}
	var ver string
	for _, f := range files {
		if f.name == "source/VERSION" {
			ver = strings.TrimSpace(string(f.data))
		}
	}
	if !versionGreater(ver, running) {
		return "", fmt.Errorf("this archive is v%s, which is not newer than the running v%s (to go back to an older release, run its install.sh with --allow-downgrade)", ver, running)
	}
	if err := os.MkdirAll(u.dir, 0o700); err != nil {
		return "", err
	}
	tmp := filepath.Join(u.dir, "source.new")
	os.RemoveAll(tmp)
	for _, f := range files {
		dst := filepath.Join(tmp, filepath.FromSlash(f.name))
		if rel, err := filepath.Rel(tmp, dst); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			os.RemoveAll(tmp)
			return "", fmt.Errorf("unsafe path %q", f.name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			os.RemoveAll(tmp)
			return "", err
		}
		if err := os.WriteFile(dst, f.data, f.mode); err != nil {
			os.RemoveAll(tmp)
			return "", err
		}
	}
	old := filepath.Join(u.dir, "source.old")
	os.RemoveAll(old)
	if _, err := os.Stat(u.sourceDir()); err == nil {
		if err := os.Rename(u.sourceDir(), old); err != nil {
			os.RemoveAll(tmp)
			return "", err
		}
	}
	if err := os.Rename(tmp, u.sourceDir()); err != nil {
		os.Rename(old, u.sourceDir())
		return "", err
	}
	os.RemoveAll(old)
	return u.SourceVersion(), nil
}

// ── build ────────────────────────────────────────────────────────────────────

var goVersionRe = regexp.MustCompile(`go version go1\.(\d+)`)

func goUsable(bin string, minMinor int) bool {
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return false
	}
	m := goVersionRe.FindStringSubmatch(string(out))
	if m == nil {
		return false
	}
	n, _ := strconv.Atoi(m[1])
	return n >= minMinor
}

// goMinor is the Go minor version the staged source asks for in go.mod (never below what
// install.sh requires), so a release that needs a newer toolchain says so plainly.
func (u *Updater) goMinor() int {
	minor := minGoMinorFloor
	if b, err := os.ReadFile(filepath.Join(u.sourceDir(), "source", "go.mod")); err == nil {
		if m := goDirectiveRe.FindSubmatch(b); m != nil {
			if n, _ := strconv.Atoi(string(m[1])); n > minor {
				minor = n
			}
		}
	}
	return minor
}

func findGo(minMinor int) (string, error) {
	var cands []string
	if p, err := exec.LookPath("go"); err == nil {
		cands = append(cands, p)
	}
	cands = append(cands, "/usr/local/go/bin/go", "/usr/bin/go", "/usr/lib/go/bin/go", "/usr/lib/golang/bin/go")
	if m, _ := filepath.Glob("/usr/lib/go-*/bin/go"); len(m) > 0 {
		sort.Sort(sort.Reverse(sort.StringSlice(m)))
		cands = append(cands, m...)
	}
	for _, c := range cands {
		if goUsable(c, minMinor) {
			return c, nil
		}
	}
	return "", fmt.Errorf("no Go toolchain 1.%d or newer found on this server (install Go, or re-run install.sh)", minMinor)
}

// toolchain is findGo for the current staged source, remembered for 30 s because the page asks
// every couple of seconds and each candidate costs a process start.
func (u *Updater) toolchain() (string, error) {
	minor := u.goMinor()
	u.toolMu.Lock()
	defer u.toolMu.Unlock()
	if u.toolMinor == minor && time.Since(u.toolAt) < 30*time.Second {
		return u.toolPath, u.toolErr
	}
	u.toolPath, u.toolErr = findGo(minor)
	u.toolAt, u.toolMinor = time.Now(), minor
	return u.toolPath, u.toolErr
}

func haveCCompiler() bool {
	if _, err := exec.LookPath("gcc"); err == nil {
		return true
	}
	_, err := exec.LookPath("cc")
	return err == nil
}

// buildEnv is the environment install.sh builds with: no network, no toolchain download, and
// scratch space under the update directory (the service's own /tmp is private).
func (u *Updater) buildEnv(tmp string) []string {
	cache := u.goCacheDir
	if cache == "" {
		cache = filepath.Join(u.dir, "gocache")
	}
	return append(os.Environ(), "CGO_ENABLED=1", "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOPROXY=off",
		"GOCACHE="+cache, "GOPATH="+filepath.Join(u.dir, "gopath"), "HOME="+u.dir, "TMPDIR="+tmp)
}

// Build compiles the staged source natively, checks the result and returns the binary's path.
func (u *Updater) Build(ctx context.Context) (string, error) {
	version := u.SourceVersion()
	if version == "" {
		return "", errors.New("no source tree staged on this server")
	}
	goBin, err := u.toolchain()
	if err != nil {
		return "", err
	}
	if !haveCCompiler() {
		return "", errors.New("no C compiler on this server (the PAM binding needs gcc and the PAM development headers; re-run install.sh)")
	}
	out := filepath.Join(u.dir, "dnsbench.new")
	os.Remove(out)
	tmp := filepath.Join(u.dir, "tmp")
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	bctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(bctx, goBin, "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", out, ".")
	cmd.Dir = filepath.Join(u.sourceDir(), "source") // the Go module; the archive also holds docs/, install.sh, contrib/
	cmd.Env = u.buildEnv(tmp)
	if outp, err := cmd.CombinedOutput(); err != nil {
		os.Remove(out)
		msg := strings.TrimSpace(string(outp))
		if len(msg) > 1500 {
			msg = "…" + msg[len(msg)-1500:]
		}
		return "", fmt.Errorf("go build failed: %v\n%s", err, msg)
	}
	if err := checkBuilt(out, version); err != nil {
		os.Remove(out)
		return "", err
	}
	return out, nil
}

// checkBuilt verifies a built binary runs, reports the expected version and (like the running
// one) has PAM linked in. It is run with an empty environment so nothing from the service's
// configuration can change what it prints.
func checkBuilt(bin, version string) error {
	st, err := os.Stat(bin)
	if err != nil || st.Size() < minBinarySize {
		return errors.New("built binary missing or implausibly small")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	b, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("built binary does not run: %w", err)
	}
	if got := strings.TrimSpace(string(b)); got != "dnsbench "+version {
		return fmt.Errorf("built binary reports %q, expected %q", got, "dnsbench "+version)
	}
	if requirePAM {
		data, err := os.ReadFile(bin)
		if err != nil || !bytes.Contains(data, []byte("libpam.so")) {
			return errors.New("built binary has no PAM support (is the PAM development package installed?); nobody could sign in to it")
		}
	}
	return nil
}

// smokeTest starts the new binary on a spare loopback port with a scratch state directory and
// waits for its login page. A build that compiles but cannot start is found here, before the
// running binary is replaced, rather than after a restart.
func smokeTest(bin, dir string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := os.MkdirTemp(dir, "smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(st)
	var out bytes.Buffer
	cmd := exec.Command(bin, "--no-tls", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--state-dir", st)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("the new build cannot be started: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stop := func() { cmd.Process.Kill(); <-exited }
	tail := func() string {
		s := strings.TrimSpace(out.String())
		if len(s) > 800 {
			s = "…" + s[len(s)-800:]
		}
		return s
	}
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.After(smokeDeadline)
	for {
		select {
		case err := <-exited:
			return fmt.Errorf("the new build exited right after starting (%v):\n%s", err, tail())
		case <-deadline:
			stop()
			return fmt.Errorf("the new build did not answer on its login page within %s:\n%s", smokeDeadline, tail())
		case <-time.After(150 * time.Millisecond):
		}
		resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/login")
		if err != nil {
			continue
		}
		code := resp.StatusCode
		resp.Body.Close()
		stop()
		if code != http.StatusOK {
			return fmt.Errorf("the new build answered its login page with HTTP %d:\n%s", code, tail())
		}
		return nil
	}
}

// ── apply / boot guard ───────────────────────────────────────────────────────

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// canReplace says whether the running binary can be replaced: the directory it lives in must be
// writable by this process. Under the shipped systemd unit that needs ReadWritePaths=/opt/dnsbench,
// which units installed before this feature lack.
func canReplace(exe string) error {
	f, err := os.CreateTemp(filepath.Dir(exe), ".dnsbench-write-test-")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %v (the systemd unit installed here mounts it read-only; run install.sh from this release once, which adds ReadWritePaths=-/opt/dnsbench, and updates from the web UI will work from then on)", filepath.Dir(exe), unwrapPathError(err))
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

func unwrapPathError(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// relabel gives the new binary the SELinux type install.sh gives it, when SELinux is enforcing.
// A file renamed into place otherwise keeps the type of its scratch location. Best effort.
func relabel(bin string) {
	ge, err := exec.LookPath("getenforce")
	if err != nil {
		return
	}
	if out, err := exec.Command(ge).Output(); err != nil || strings.TrimSpace(string(out)) != "Enforcing" {
		return
	}
	cc, err := exec.LookPath("chcon")
	if err != nil {
		return
	}
	if err := exec.Command(cc, "-t", "bin_t", bin).Run(); err != nil {
		log.Printf("update: could not set the SELinux label on %s: %v", bin, err)
	}
}

// refreshDocs copies README.md and LICENSE.txt from the staged tree next to the binary, where
// the in-app ReadMe and License pages read them (install.sh does the same). Best effort.
func (u *Updater) refreshDocs(dir string) {
	for _, name := range []string{"README.md", "LICENSE.txt"} {
		b, err := os.ReadFile(filepath.Join(u.sourceDir(), name))
		if err != nil {
			continue
		}
		if err := writeFileAtomic(filepath.Join(dir, name), b, 0o644); err != nil {
			log.Printf("update: could not refresh %s: %v", name, err)
		}
	}
}

func (u *Updater) guardPath() string { return filepath.Join(u.dir, "guard.json") }

func (u *Updater) readGuard() (bootGuard, bool) {
	var g bootGuard
	b, err := os.ReadFile(u.guardPath())
	if err != nil || json.Unmarshal(b, &g) != nil {
		return g, false
	}
	return g, true
}

func (u *Updater) writeGuard(g bootGuard) {
	os.MkdirAll(u.dir, 0o700)
	b, _ := json.MarshalIndent(g, "", "  ")
	writeFileAtomic(u.guardPath(), b, 0o600)
}

func (u *Updater) setPhase(p string) {
	u.mu.Lock()
	u.phase = p
	u.mu.Unlock()
}

// Apply builds the staged source, proves the build starts, and replaces the running executable
// with it. The caller restarts the process afterwards. It returns the new version.
func (u *Updater) Apply(ctx context.Context, by string) (string, error) {
	u.mu.Lock()
	if u.updating {
		u.mu.Unlock()
		return "", errors.New("an update is already in progress on this server")
	}
	u.updating, u.phase = true, "building"
	u.mu.Unlock()
	done := false
	defer func() {
		if !done {
			u.mu.Lock()
			u.updating, u.phase = false, ""
			u.mu.Unlock()
		}
	}()

	from, target := version(), u.SourceVersion()
	if target == "" {
		return "", errors.New("no source tree staged: upload a release archive first")
	}
	if !versionGreater(target, from) {
		return "", fmt.Errorf("the staged source (v%s) is not newer than the running version (v%s)", target, from)
	}
	exe, err := u.exePath()
	if err != nil {
		u.noteFailure(from, target, err.Error(), by)
		return "", err
	}
	if err := canReplace(exe); err != nil { // before the long build, not after it
		u.noteFailure(from, target, err.Error(), by)
		return "", err
	}
	log.Printf("update: building v%s (running v%s), requested by %q", target, from, by)
	bin, err := u.build(ctx)
	if err != nil {
		u.noteFailure(from, target, err.Error(), by)
		return "", err
	}
	defer os.Remove(bin)
	u.setPhase("testing the new build")
	if u.smoke != nil {
		if err := u.smoke(bin); err != nil {
			u.noteFailure(from, target, err.Error(), by)
			return "", err
		}
	}
	u.setPhase("installing")
	prev := filepath.Join(u.dir, "dnsbench.prev")
	if err := copyFile(exe, prev, 0o755); err != nil {
		u.noteFailure(from, target, "cannot save the current binary: "+err.Error(), by)
		return "", fmt.Errorf("cannot save the current binary for rollback: %w", err)
	}
	u.writeGuard(bootGuard{State: "applying", From: from, To: target, Prev: prev, Exe: exe, At: time.Now().UTC()})
	if err := copyFile(bin, exe, 0o755); err != nil {
		os.Remove(u.guardPath())
		u.noteFailure(from, target, "cannot replace "+exe+": "+err.Error(), by)
		return "", fmt.Errorf("cannot replace %s: %w", exe, err)
	}
	relabel(exe)
	u.refreshDocs(filepath.Dir(exe))
	u.mu.Lock()
	u.phase = "installed, restarting"
	u.mu.Unlock()
	done = true // stays "updating" until the process re-execs
	log.Printf("update: v%s installed at %s, restarting", target, exe)
	return target, nil
}

// GuardOnStart is called as early as possible after start. It returns true when the previous
// binary has been restored and the caller must re-exec it.
func (u *Updater) GuardOnStart() (rolledBack bool) {
	g, ok := u.readGuard()
	if !ok || g.State != "applying" {
		return false
	}
	if g.To != version() { // a different binary is running than the update expected
		g.State = "ok"
		u.writeGuard(g)
		return false
	}
	g.Attempts++
	u.writeGuard(g)
	if g.Attempts <= maxBootAttempts {
		return false
	}
	log.Printf("update: v%s did not stay up (%d starts), restoring v%s", g.To, g.Attempts-1, g.From)
	if err := copyFile(g.Prev, g.Exe, 0o755); err != nil {
		log.Printf("update: rollback failed: %v", err)
		return false
	}
	g.State = "rolled-back"
	u.writeGuard(g)
	u.Record(UpdateEvent{Kind: "rolled-back", From: g.To, To: g.From,
		Detail: fmt.Sprintf("v%s failed to stay running (%d starts)", g.To, g.Attempts-1)})
	return true
}

// GuardConfirm marks a freshly applied update as good once it has run long enough.
func (u *Updater) GuardConfirm() {
	g, ok := u.readGuard()
	if !ok || g.State != "applying" || g.To != version() {
		return
	}
	g.State = "ok"
	u.writeGuard(g)
	u.Record(UpdateEvent{Kind: "applied", From: g.From, To: g.To})
	log.Printf("update: v%s confirmed (up for %s)", g.To, bootConfirmAfter)
}

// RolledBackNotice reports a rollback that happened at the last start.
func (u *Updater) RolledBackNotice() string {
	g, ok := u.readGuard()
	if ok && g.State == "rolled-back" && g.From == version() {
		return fmt.Sprintf("v%s was rolled back to v%s after failing to stay up", g.To, g.From)
	}
	return ""
}
