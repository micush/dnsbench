package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// updateServer is a test server whose updater is switched on and works on a scratch "install".
type updateServer struct {
	*testServer
	hdr      map[string]string
	exe      string
	restarts atomic.Int32
}

func newUpdateServer(t *testing.T) *updateServer {
	t.Helper()
	s := newTestServer(t)
	u := s.app.updater
	u.enabled = true
	dir := t.TempDir()
	us := &updateServer{testServer: s, exe: filepath.Join(dir, "dnsbench")}
	os.WriteFile(us.exe, []byte("OLD"), 0o755)
	u.exePath = func() (string, error) { return us.exe, nil }
	u.build = func(context.Context) (string, error) {
		p := filepath.Join(u.dir, "dnsbench.new")
		return p, os.WriteFile(p, []byte("NEW"), 0o755)
	}
	u.smoke = nil
	u.restartFn = func() { us.restarts.Add(1) }
	oldGrace, oldRecheck := restartGrace, restartRecheck
	restartGrace, restartRecheck = 0, 10*time.Millisecond
	t.Cleanup(func() { restartGrace, restartRecheck = oldGrace, oldRecheck })
	us.hdr = map[string]string{"X-Session-Token": s.apiToken(t)}
	return us
}

func (us *updateServer) upload(t *testing.T, body []byte, extra map[string]string) (*http.Response, string) {
	t.Helper()
	h := map[string]string{"Content-Type": "application/octet-stream"}
	for k, v := range us.hdr {
		h[k] = v
	}
	for k, v := range extra {
		h[k] = v
	}
	return us.do(t, "POST", "/api/update/upload", string(body), h)
}

func (us *updateServer) status(t *testing.T) map[string]any {
	t.Helper()
	resp, body := us.do(t, "GET", "/api/update", "", us.hdr)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, body)
	}
	d, _ := m["data"].(map[string]any)
	if d == nil {
		t.Fatalf("no data: %s", body)
	}
	return d
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestUpdateStatusOnAFreshServer(t *testing.T) {
	us := newUpdateServer(t)
	d := us.status(t)
	if d["enabled"] != true || d["running"] != version() || d["source_version"] != "" || d["newer"] != false || d["busy"] != false {
		t.Errorf("status %v", d)
	}
	if h, _ := d["history"].([]any); h == nil || len(h) != 0 {
		t.Errorf("history should be an empty list, got %v", d["history"])
	}
	if p, _ := d["problems"].([]any); p == nil {
		t.Errorf("problems should be a list, got %v", d["problems"])
	}
}

func TestUploadStagesAReleaseAndRecordsWho(t *testing.T) {
	us := newUpdateServer(t)
	v := nextVersion(t)
	resp, body := us.upload(t, releaseTgz(t, v), nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `"version":"`+v+`"`) {
		t.Fatalf("upload: %d %s", resp.StatusCode, body)
	}
	d := us.status(t)
	if d["source_version"] != v || d["newer"] != true {
		t.Errorf("status after upload: %v", d)
	}
	hist, _ := d["history"].([]any)
	if len(hist) != 1 {
		t.Fatalf("history %v", hist)
	}
	e := hist[0].(map[string]any)
	if e["kind"] != "uploaded" || e["by"] != "alice" || e["to"] != v {
		t.Errorf("event %v", e)
	}
}

func TestUploadRefusals(t *testing.T) {
	us := newUpdateServer(t)
	for name, tc := range map[string]struct {
		body []byte
		want string
	}{
		"not an archive":  {[]byte("hello"), "rejected"},
		"empty":           {nil, "empty upload"},
		"not newer":       {releaseTgz(t, version()), "not newer"},
		"another project": {tgzOf(t, []tent{{name: "x/source/go.mod", body: "module ddgw\n"}, {name: "x/source/main.go", body: "x"}, {name: "x/source/VERSION", body: "99\n"}}), "not a dnsbench source tree"},
		"path traversal":  {tgzOf(t, append(releaseEnts("dnsbench", nextVersion(t)), tent{name: "dnsbench/../../evil", body: "x"})), "escapes"},
	} {
		resp, body := us.upload(t, tc.body, nil)
		if resp.StatusCode != 422 || !strings.Contains(body, tc.want) {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	if us.app.updater.SourceVersion() != "" {
		t.Error("something was staged from a refused upload")
	}
}

func TestUploadOverTheLimitIsRefusedWithoutBeingStored(t *testing.T) {
	us := newUpdateServer(t)
	big := bytes.Repeat([]byte{0x1f}, maxUploadBytes+1024)
	resp, body := us.upload(t, big, nil)
	if resp.StatusCode != 413 || !strings.Contains(body, "too large") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if us.app.updater.SourceVersion() != "" {
		t.Error("staged")
	}
}

func TestCrossOriginUploadAndApplyAreRefused(t *testing.T) {
	us := newUpdateServer(t)
	cross := map[string]string{"Sec-Fetch-Site": "cross-site"}
	resp, _ := us.upload(t, releaseTgz(t, nextVersion(t)), cross)
	if resp.StatusCode != 403 {
		t.Errorf("cross-site upload: %d", resp.StatusCode)
	}
	if us.app.updater.SourceVersion() != "" {
		t.Error("a cross-site upload was staged")
	}
	resp, _ = us.upload(t, releaseTgz(t, nextVersion(t)), map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != 403 {
		t.Errorf("foreign Origin upload: %d", resp.StatusCode)
	}
	us.upload(t, releaseTgz(t, nextVersion(t)), nil)
	h := map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-site"}
	for k, v := range us.hdr {
		h[k] = v
	}
	resp, _ = us.do(t, "POST", "/api/update/apply", "{}", h)
	if resp.StatusCode != 403 {
		t.Errorf("same-site (another port) apply: %d", resp.StatusCode)
	}
	if us.restarts.Load() != 0 || readStr(t, us.exe) != "OLD" {
		t.Error("an update ran from a cross-origin request")
	}
}

func TestUpdatesSwitchedOffRefusesEverythingButStatus(t *testing.T) {
	us := newUpdateServer(t)
	us.app.updater.enabled = false
	for _, p := range []string{"/api/update/upload", "/api/update/apply"} {
		resp, body := us.do(t, "POST", p, "{}", map[string]string{"X-Session-Token": us.hdr["X-Session-Token"], "Content-Type": "application/json"})
		if resp.StatusCode != 403 || !strings.Contains(body, "ALLOW_UPDATES=false") {
			t.Errorf("%s: %d %s", p, resp.StatusCode, body)
		}
	}
	d := us.status(t)
	if d["enabled"] != false {
		t.Errorf("status %v", d)
	}
	if p, _ := d["problems"].([]any); len(p) == 0 || !strings.Contains(p[0].(string), "ALLOW_UPDATES=false") {
		t.Errorf("the page needs to be told why: %v", d["problems"])
	}
	if _, err := us.app.UpdateUpload(releaseTgz(t, nextVersion(t)), "alice"); err == nil {
		t.Error("the app-level upload ignores the switch")
	}
	if _, err := us.app.UpdateApply("alice", true); err == nil {
		t.Error("the app-level apply ignores the switch")
	}
}

func TestApplyEndToEndInstallsThenRestarts(t *testing.T) {
	us := newUpdateServer(t)
	v := nextVersion(t)
	us.upload(t, releaseTgz(t, v), nil)
	resp, body := us.postJSON(t, "/api/update/apply", map[string]any{}, us.hdr)
	if resp.StatusCode != 200 || body["ok"] != true {
		t.Fatalf("apply: %d %v", resp.StatusCode, body)
	}
	waitFor(t, "the restart", func() bool { return us.restarts.Load() == 1 })
	if readStr(t, us.exe) != "NEW" {
		t.Error("not installed before the restart was asked for")
	}
	if us.app.updater.Phase() != "installed, restarting" {
		t.Errorf("phase %q", us.app.updater.Phase())
	}
	time.Sleep(50 * time.Millisecond)
	if us.restarts.Load() != 1 {
		t.Errorf("restarted %d times", us.restarts.Load())
	}
}

func TestApplyWithNothingStagedOrAlreadyRunning(t *testing.T) {
	us := newUpdateServer(t)
	resp, m := us.postJSON(t, "/api/update/apply", map[string]any{}, us.hdr)
	if resp.StatusCode != 409 || m["ok"] != false || !strings.Contains(m["error"].(string), "upload a release archive first") {
		t.Errorf("nothing staged: %d %v", resp.StatusCode, m)
	}
	us.upload(t, releaseTgz(t, nextVersion(t)), nil)
	release := make(chan struct{})
	us.app.updater.build = func(context.Context) (string, error) {
		<-release
		p := filepath.Join(us.app.updater.dir, "dnsbench.new")
		return p, os.WriteFile(p, []byte("NEW"), 0o755)
	}
	if resp, m = us.postJSON(t, "/api/update/apply", map[string]any{}, us.hdr); resp.StatusCode != 200 {
		t.Fatalf("first apply: %v", m)
	}
	waitFor(t, "busy", us.app.updater.Busy)
	if d := us.status(t); d["busy"] != true || d["phase"] != "building" {
		t.Errorf("status while building: %v", d)
	}
	if resp, m = us.postJSON(t, "/api/update/apply", map[string]any{}, us.hdr); resp.StatusCode != 409 || !strings.Contains(m["error"].(string), "already in progress") {
		t.Errorf("second apply: %d %v", resp.StatusCode, m)
	}
	if resp, body := us.upload(t, releaseTgz(t, "999999"), nil); resp.StatusCode != 422 || !strings.Contains(body, "in progress") {
		t.Errorf("upload while building: %d %s", resp.StatusCode, body)
	}
	close(release)
	waitFor(t, "the restart", func() bool { return us.restarts.Load() == 1 })
}

func runningJob(a *App) *Job {
	j := newJob("job-1", "alice", map[string]string{"server": "x"})
	a.jobsMu.Lock()
	a.jobs[j.ID] = j
	a.jobOrder = append(a.jobOrder, j.ID)
	a.jobsMu.Unlock()
	return j
}

func finishJob(j *Job) {
	j.mu.Lock()
	j.status = "done"
	j.mu.Unlock()
}

func TestApplyAsksBeforeStoppingARunningBenchmark(t *testing.T) {
	us := newUpdateServer(t)
	us.upload(t, releaseTgz(t, nextVersion(t)), nil)
	j := runningJob(us.app)
	resp, m := us.postJSON(t, "/api/update/apply", map[string]any{}, us.hdr)
	if resp.StatusCode != 409 || m["needs_confirm"] != true || !strings.HasPrefix(m["error"].(string), "not safe to update") || !strings.Contains(m["error"].(string), "benchmark") {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if us.app.updater.Busy() || readStr(t, us.exe) != "OLD" {
		t.Error("something started although the operator was not asked")
	}
	// confirmed: goes ahead, and restarts without waiting for the benchmark
	resp, m = us.postJSON(t, "/api/update/apply", map[string]any{"force": true}, us.hdr)
	if resp.StatusCode != 200 {
		t.Fatalf("forced apply: %d %v", resp.StatusCode, m)
	}
	waitFor(t, "the forced restart", func() bool { return us.restarts.Load() == 1 })
	finishJob(j)
}

func TestInstalledUpdateWaitsForARunningBenchmarkToFinish(t *testing.T) {
	us := newUpdateServer(t)
	j := runningJob(us.app)
	us.app.scheduleRestart(false)
	waitFor(t, "the waiting message", func() bool { return strings.Contains(us.app.updater.Waiting(), "benchmark") })
	if !strings.HasPrefix(us.app.updater.Waiting(), "installed; waiting to restart:") {
		t.Errorf("waiting %q", us.app.updater.Waiting())
	}
	if d := us.status(t); !strings.Contains(d["waiting"].(string), "benchmark") {
		t.Errorf("the page is not told it is waiting: %v", d["waiting"])
	}
	time.Sleep(60 * time.Millisecond)
	if us.restarts.Load() != 0 {
		t.Fatal("restarted while a benchmark was running")
	}
	finishJob(j)
	waitFor(t, "the restart after the benchmark ended", func() bool { return us.restarts.Load() == 1 })
	waitFor(t, "the waiting message to clear", func() bool { return us.app.updater.Waiting() == "" })
}

func TestWaitUntilSafe(t *testing.T) {
	var calls int
	var told []string
	waitUntilSafe(func() (bool, string) { calls++; return calls >= 3, "busy" }, false, time.Millisecond, func(s string) { told = append(told, s) })
	if calls != 3 || len(told) != 3 || told[0] != "busy" || told[2] != "" {
		t.Errorf("calls %d, told %q", calls, told)
	}
	calls = 0
	waitUntilSafe(func() (bool, string) { calls++; return false, "busy" }, true, time.Millisecond, func(string) { t.Error("told something while forced") })
	if calls != 0 {
		t.Errorf("force still asked %d times", calls)
	}
}

func TestStatusReportsWhatBlocksAnUpdate(t *testing.T) {
	us := newUpdateServer(t)
	us.app.updater.exePath = func() (string, error) { return "/nonexistent-dir/dnsbench", nil }
	d := us.status(t)
	var all []string
	for _, p := range d["problems"].([]any) {
		all = append(all, p.(string))
	}
	if !strings.Contains(strings.Join(all, "\n"), "ReadWritePaths") {
		t.Errorf("the read-only install is not reported: %v", all)
	}
}

func TestStatusShowsWhyTheLastAttemptFailed(t *testing.T) {
	us := newUpdateServer(t)
	v := nextVersion(t)
	us.upload(t, releaseTgz(t, v), nil)
	us.app.updater.build = func(context.Context) (string, error) { return "", io.ErrUnexpectedEOF }
	us.postJSON(t, "/api/update/apply", map[string]any{}, us.hdr)
	waitFor(t, "the failure", func() bool { return us.app.updater.lastFailure() != nil })
	d := us.status(t)
	lf, _ := d["last_failed"].(map[string]any)
	if lf == nil || lf["to"] != v || lf["by"] != "alice" || !strings.Contains(lf["detail"].(string), "unexpected EOF") {
		t.Errorf("last_failed %v", d["last_failed"])
	}
	if d["busy"] != false {
		t.Error("still busy after failing")
	}
	// uploading a fixed release clears it from the page
	n, _ := parseVer(v)
	us.upload(t, releaseTgz(t, strconv.FormatInt(n+1, 10)), nil)
	if d = us.status(t); d["last_failed"] != nil {
		t.Errorf("an old failure is still shown after a new upload: %v", d["last_failed"])
	}
}
