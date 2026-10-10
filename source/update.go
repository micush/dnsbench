package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// Software updates, after umiss's managerupdate: a release archive (the
// ddgw_vN.tgz this project ships) is uploaded to any node or pulled from a
// peer, built natively on each node (the PAM binding is cgo, so a binary is
// only portable to a host with the same libpam), and swapped in with a re-exec.
// A boot guard restores the previous binary if the new one keeps failing to
// start.  Admin intent (auto-update everyone / update these nodes) is
// replicated from the primary; history is per node.

const (
	maxSourceBytes   = 64 << 20
	maxSourceFile    = 16 << 20
	maxSourceFiles   = 4000
	maxUploadBytes   = 32 << 20
	buildTimeout     = 10 * time.Minute
	maxBootAttempts  = 3
	bootConfirmAfter = 60 * time.Second
	retryAfterFail   = 10 * time.Minute
	maxHistory       = 500
)

// UpdateIntent is what an admin asked for; replicated from the primary.
type UpdateIntent struct {
	AutoAll bool      `json:"auto_all"`
	Pending []string  `json:"pending"` // node addresses queued for an update
	By      string    `json:"by,omitempty"`
	At      time.Time `json:"at,omitempty"`
}

func (i UpdateIntent) wants(addr string) bool {
	return i.AutoAll || containsStr(i.Pending, addr)
}

// UpdateEvent is one history entry.
type UpdateEvent struct {
	At     time.Time `json:"at"`
	Node   string    `json:"node"`
	Kind   string    `json:"kind"` // uploaded|pulled|queued|cancelled|auto-on|auto-off|applied|failed|rolled-back
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

// Updater owns the source tree, build cache, admin intent and history.
type Updater struct {
	dir     string // <state>/update
	exePath func() (string, error)

	mu         sync.Mutex
	intent     UpdateIntent
	hist       []UpdateEvent
	failedVer  string
	failedAt   time.Time
	updating   bool
	phase      string
	lastDetail string
	waiting    string
}

func NewUpdater(stateDir string) (*Updater, error) {
	u := &Updater{dir: filepath.Join(stateDir, "update"), exePath: os.Executable}
	if err := os.MkdirAll(u.dir, 0o700); err != nil {
		return nil, err
	}
	u.intent.Pending = []string{}
	if b, err := os.ReadFile(filepath.Join(u.dir, "intent.json")); err == nil {
		json.Unmarshal(b, &u.intent)
		if u.intent.Pending == nil {
			u.intent.Pending = []string{}
		}
	} else if os.IsNotExist(err) {
		u.intent.AutoAll = true // a fresh install updates itself; an admin's saved choice is never overridden
	}
	if b, err := os.ReadFile(filepath.Join(u.dir, "history.json")); err == nil {
		json.Unmarshal(b, &u.hist)
	}
	return u, nil
}

func (u *Updater) sourceDir() string { return filepath.Join(u.dir, "source") }

func (u *Updater) saveIntentLocked() {
	b, _ := json.MarshalIndent(u.intent, "", "  ")
	writeAtomic(filepath.Join(u.dir, "intent.json"), b, 0o600)
}

func (u *Updater) addEventLocked(e UpdateEvent) {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	u.hist = append(u.hist, e)
	if len(u.hist) > maxHistory {
		u.hist = u.hist[len(u.hist)-maxHistory:]
	}
	b, _ := json.MarshalIndent(u.hist, "", "  ")
	writeAtomic(filepath.Join(u.dir, "history.json"), b, 0o600)
}

func (u *Updater) Record(e UpdateEvent) {
	u.mu.Lock()
	u.addEventLocked(e)
	u.mu.Unlock()
}

// History returns events newest first (optionally only for one node).
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

func (u *Updater) LastEvent() *UpdateEvent {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.hist) == 0 {
		return nil
	}
	e := u.hist[len(u.hist)-1]
	return &e
}

func (u *Updater) Intent() UpdateIntent {
	u.mu.Lock()
	defer u.mu.Unlock()
	i := u.intent
	i.Pending = append([]string{}, u.intent.Pending...)
	return i
}

// SetIntent replaces the intent (a replica adopting the primary's).
func (u *Updater) SetIntent(i UpdateIntent) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if i.Pending == nil {
		i.Pending = []string{}
	}
	sort.Strings(i.Pending)
	u.intent = i
	u.saveIntentLocked()
}

func (u *Updater) SetAuto(on bool, by, self string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.intent.AutoAll == on {
		return
	}
	u.intent.AutoAll, u.intent.By, u.intent.At = on, by, time.Now().UTC()
	u.saveIntentLocked()
	kind := "auto-off"
	if on {
		kind = "auto-on"
	}
	infof("auto-update %s by %s", map[bool]string{true: "enabled", false: "disabled"}[on], by)
	u.addEventLocked(UpdateEvent{Node: self, Kind: kind, By: by})
}

func (u *Updater) Push(nodes []string, by string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, n := range nodes {
		if !containsStr(u.intent.Pending, n) {
			u.intent.Pending = append(u.intent.Pending, n)
			u.addEventLocked(UpdateEvent{Node: n, Kind: "queued", By: by})
		}
	}
	sort.Strings(u.intent.Pending)
	u.intent.By, u.intent.At = by, time.Now().UTC()
	u.saveIntentLocked()
}

func (u *Updater) Cancel(nodes []string, by string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	kept := []string{}
	for _, p := range u.intent.Pending {
		if containsStr(nodes, p) {
			u.addEventLocked(UpdateEvent{Node: p, Kind: "cancelled", By: by})
			continue
		}
		kept = append(kept, p)
	}
	u.intent.Pending = kept
	u.saveIntentLocked()
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

func (u *Updater) Phase() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.phase
}

// SetWaiting records why this node is holding back a queued update ("" = not waiting).
func (u *Updater) SetWaiting(why string) {
	u.mu.Lock()
	changed := u.waiting != why
	u.waiting = why
	u.mu.Unlock()
	if changed && why != "" {
		infof("update: holding back — %s", why)
	}
}

func (u *Updater) Waiting() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.waiting
}

func (u *Updater) Busy() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.updating
}

// FailedFor returns the target version that last failed (within the retry
// window), or "".
func (u *Updater) FailedFor() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.failedVer != "" && time.Since(u.failedAt) < retryAfterFail {
		return u.failedVer
	}
	return ""
}

func (u *Updater) noteFailure(self, from, to, detail string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.failedVer, u.failedAt, u.lastDetail = to, time.Now(), detail
	u.addEventLocked(UpdateEvent{Node: self, Kind: "failed", From: from, To: to, Detail: detail})
	warnf("update: v%s → v%s failed: %s", from, to, detail)
}

func (u *Updater) clearFailure() {
	u.mu.Lock()
	u.failedVer = ""
	u.mu.Unlock()
}

// ── source archive handling ──────────────────────────────────────────────────

var versionFileRe = regexp.MustCompile(`^[0-9]+\n?$`)

type srcFile struct {
	name string
	data []byte
	mode fs.FileMode
}

// cleanArchiveName normalises an entry name and rejects anything that could
// escape the destination.
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

// finishSource strips a single common top directory, drops VCS/binary
// leftovers and checks the tree really is a ddgw source tree.
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
		if name == ".git" || strings.HasPrefix(name, ".git/") || name == "ddgw" || strings.HasSuffix(name, ".tmp") {
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
	gm := get("source/go.mod")
	if gm == nil || !bytes.Contains(gm, []byte("module ddgw")) {
		return nil, errors.New("not a ddgw source tree (source/go.mod with \"module ddgw\" not found in the archive; trees before v44 had the Go files at the top and cannot be used by this version)")
	}
	if get("source/main.go") == nil {
		return nil, errors.New("not a ddgw source tree (source/main.go missing)")
	}
	v := get("source/VERSION")
	if v == nil || !versionFileRe.Match(v) {
		return nil, errors.New("source/VERSION missing or not a plain integer")
	}
	return out, nil
}

// ExtractSource validates and stages an uploaded/pulled archive as the source
// tree, replacing the previous one.  It returns the new VERSION.
func (u *Updater) ExtractSource(body []byte) (string, error) {
	files, err := parseSourceArchive(body)
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(u.dir, "source.new")
	os.RemoveAll(tmp)
	for _, f := range files {
		dst := filepath.Join(tmp, filepath.FromSlash(f.name))
		if rel, err := filepath.Rel(tmp, dst); err != nil || strings.HasPrefix(rel, "..") {
			os.RemoveAll(tmp)
			return "", fmt.Errorf("unsafe path %q", f.name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
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
	u.clearFailure()
	return u.SourceVersion(), nil
}

// SourceTarball packs the staged source tree (top-level "ddgw/") for a peer.
func (u *Updater) SourceTarball() ([]byte, string, error) {
	ver := u.SourceVersion()
	if ver == "" {
		return nil, "", errors.New("no source tree staged on this node")
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	root := u.sourceDir()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: "ddgw/" + filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0)}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	tw.Close()
	gz.Close()
	return buf.Bytes(), ver, nil
}

// sourceFingerprint hashes the staged tree (names and contents).
func (u *Updater) sourceFingerprint() (string, error) {
	h := sha256.New()
	root := u.sourceDir()
	var names []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			names = append(names, rel)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	sort.Strings(names)
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(root, n))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ── build ────────────────────────────────────────────────────────────────────

var goVersionRe = regexp.MustCompile(`go version go1\.(\d+)`)

func goUsable(bin string) bool {
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return false
	}
	m := goVersionRe.FindStringSubmatch(string(out))
	if m == nil {
		return false
	}
	n, _ := strconv.Atoi(m[1])
	return n >= 24
}

func (u *Updater) findGo() (string, error) {
	cands := []string{}
	if p, err := exec.LookPath("go"); err == nil {
		cands = append(cands, p)
	}
	cands = append(cands, "/usr/local/go/bin/go", "/usr/lib/go/bin/go", "/usr/lib/golang/bin/go", "/snap/bin/go") // /snap/bin is not on a service's PATH
	if m, _ := filepath.Glob("/usr/lib/go-*/bin/go"); len(m) > 0 {
		sort.Sort(sort.Reverse(sort.StringSlice(m)))
		cands = append(cands, m...)
	}
	cands = append(cands, "/usr/local/share/ddgw/go/bin/go")
	for _, c := range cands {
		if goUsable(c) {
			return c, nil
		}
	}
	return "", errors.New("no Go toolchain >= 1.24 found on this node (install Go, or re-run install.sh which keeps one under /usr/local/share/ddgw/go)")
}

// Build compiles the staged source natively and returns the binary path.
func (u *Updater) Build(ctx context.Context) (bin, version string, err error) {
	version = u.SourceVersion()
	if version == "" {
		return "", "", errors.New("no source tree staged on this node")
	}
	fp, err := u.sourceFingerprint()
	if err != nil {
		return "", "", err
	}
	cache := filepath.Join(u.dir, "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return "", "", err
	}
	out := filepath.Join(cache, fmt.Sprintf("ddgw-v%s-%s", version, fp[:12]))
	if checkBuilt(out, version) == nil {
		return out, version, nil
	}
	goBin, err := u.findGo()
	if err != nil {
		return "", "", err
	}
	if _, e1 := exec.LookPath("gcc"); e1 != nil {
		if _, e2 := exec.LookPath("cc"); e2 != nil {
			return "", "", errors.New("no C compiler on this node (the PAM binding needs gcc and the PAM development headers)")
		}
	}
	bctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	tmp := out + ".tmp"
	os.Remove(tmp)
	cmd := exec.CommandContext(bctx, goBin, "build", "-trimpath", "-ldflags=-s -w", "-o", tmp, ".")
	cmd.Dir = filepath.Join(u.sourceDir(), "source") // the Go module (the archive also holds docs/, install.sh, contrib/)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1", "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod",
		"GOCACHE="+filepath.Join(u.dir, "gocache"), "GOPATH="+filepath.Join(u.dir, "gopath"), "HOME="+u.dir)
	if outp, err := cmd.CombinedOutput(); err != nil {
		os.Remove(tmp)
		msg := strings.TrimSpace(string(outp))
		if len(msg) > 1500 {
			msg = "…" + msg[len(msg)-1500:]
		}
		return "", "", fmt.Errorf("go build failed: %v\n%s", err, msg)
	}
	if err := checkBuilt(tmp, version); err != nil {
		os.Remove(tmp)
		return "", "", err
	}
	if err := os.Rename(tmp, out); err != nil {
		return "", "", err
	}
	// keep the cache small: only the newest build stays
	if ents, err := os.ReadDir(cache); err == nil {
		for _, e := range ents {
			if filepath.Join(cache, e.Name()) != out {
				os.Remove(filepath.Join(cache, e.Name()))
			}
		}
	}
	return out, version, nil
}

// checkBuilt verifies a built binary runs, reports the expected version and
// (like the running one) has PAM linked in.
func checkBuilt(bin, version string) error {
	st, err := os.Stat(bin)
	if err != nil || st.Size() < 1<<20 {
		return errors.New("built binary missing or implausibly small")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return fmt.Errorf("built binary does not run: %w", err)
	}
	if got := strings.TrimSpace(string(b)); got != "ddgw v"+version {
		return fmt.Errorf("built binary reports %q, expected %q", got, "ddgw v"+version)
	}
	if pamAvailable {
		data, err := os.ReadFile(bin)
		if err != nil || !bytes.Contains(data, []byte("libpam.so")) {
			return errors.New("built binary has no PAM support (is the PAM development package installed?)")
		}
	}
	return nil
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
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
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
	b, _ := json.MarshalIndent(g, "", "  ")
	writeAtomic(u.guardPath(), b, 0o600)
}

// Apply builds the staged source and replaces the running executable with it.
// The caller restarts the process afterwards.  Returns the new version.
func (u *Updater) Apply(ctx context.Context, self, by string) (string, error) {
	u.mu.Lock()
	if u.updating {
		u.mu.Unlock()
		return "", errors.New("an update is already in progress on this node")
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

	from := version()
	target := u.SourceVersion()
	if target == "" {
		return "", errors.New("no source tree staged: upload a release archive first")
	}
	if !versionGreater(target, from) {
		return "", fmt.Errorf("the staged source (v%s) is not newer than the running version (v%s)", target, from)
	}
	infof("update: building v%s (running v%s) — requested by %s", target, from, by)
	bin, _, err := u.Build(ctx)
	if err != nil {
		u.noteFailure(self, from, target, err.Error())
		return "", err
	}
	exe, err := u.exePath()
	if err != nil {
		u.noteFailure(self, from, target, err.Error())
		return "", err
	}
	prev := filepath.Join(u.dir, "ddgw.prev")
	if err := copyFile(exe, prev, 0o755); err != nil {
		u.noteFailure(self, from, target, "cannot save the current binary: "+err.Error())
		return "", fmt.Errorf("cannot save the current binary for rollback: %w", err)
	}
	u.writeGuard(bootGuard{State: "applying", From: from, To: target, Prev: prev, Exe: exe, At: time.Now().UTC()})
	if err := copyFile(bin, exe, 0o755); err != nil {
		os.Remove(u.guardPath())
		u.noteFailure(self, from, target, "cannot replace "+exe+": "+err.Error())
		return "", fmt.Errorf("cannot replace %s: %w", exe, err)
	}
	u.clearFailure()
	u.mu.Lock()
	u.phase = "installed, restarting"
	u.mu.Unlock()
	done = true // stays "updating" until the process re-execs
	infof("update: v%s installed at %s — restarting", target, exe)
	return target, nil
}

// GuardOnStart is called as early as possible after start.  It returns true
// when the previous binary has been restored and the caller must re-exec it.
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
	warnf("update: v%s did not stay up (%d starts) — restoring v%s", g.To, g.Attempts-1, g.From)
	if err := copyFile(g.Prev, g.Exe, 0o755); err != nil {
		errorf("update: rollback failed: %v", err)
		return false
	}
	g.State = "rolled-back"
	u.writeGuard(g)
	u.Record(UpdateEvent{Node: "local", Kind: "rolled-back", From: g.To, To: g.From,
		Detail: fmt.Sprintf("v%s failed to stay running (%d starts)", g.To, g.Attempts-1)})
	return true
}

// GuardConfirm marks a freshly applied update as good once it has run long
// enough.
func (u *Updater) GuardConfirm(self string) {
	g, ok := u.readGuard()
	if !ok || g.State != "applying" || g.To != version() {
		return
	}
	g.State = "ok"
	u.writeGuard(g)
	u.Record(UpdateEvent{Node: self, Kind: "applied", From: g.From, To: g.To})
	u.mu.Lock()
	u.updating = false
	u.mu.Unlock()
	infof("update: v%s confirmed (up for %s)", g.To, bootConfirmAfter)
}

// RolledBackNotice reports a rollback that happened at the last start.
func (u *Updater) RolledBackNotice() string {
	g, ok := u.readGuard()
	if ok && g.State == "rolled-back" && g.From == version() {
		return fmt.Sprintf("v%s was rolled back to v%s after failing to stay up", g.To, g.From)
	}
	return ""
}
