package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAuth: user "alice"/"pw" is in the group, "bob"/"pw" authenticates but is not.
type fakeAuth struct {
	authCalls atomic.Int32
	removed   atomic.Bool // alice removed from the group
}

func (f *fakeAuth) Authenticate(service, user, pass string) error {
	f.authCalls.Add(1)
	if service != "ddgw" {
		return errors.New("wrong service " + service)
	}
	if (user == "alice" || user == "bob") && pass == "pw" {
		return nil
	}
	return errors.New("bad password")
}

func (f *fakeAuth) InGroup(user, group string) (bool, error) {
	if group != "ddgw" {
		return false, errors.New("wrong group")
	}
	return user == "alice" && !f.removed.Load(), nil
}

type webEnv struct {
	t       *testing.T
	ts      *httptest.Server
	ws      *WebServer
	mg      *Mgmt
	auth    *fakeAuth
	conf    string
	reloads atomic.Int32
	client  *http.Client
	csrf    string
}

func newWebEnv(t *testing.T) *webEnv {
	t.Helper()
	failDelay = time.Millisecond
	e := &webEnv{t: t, auth: &fakeAuth{}}
	dir := t.TempDir()
	e.conf = filepath.Join(dir, "ddgw.conf")
	dc := newDaemonConfig()
	dc.Cluster.Enabled = false
	mg, err := NewMgmt(e.conf, filepath.Join(dir, "state"), dc)
	if err != nil {
		t.Fatal(err)
	}
	mg.reloadFn = func() error {
		e.reloads.Add(1)
		nu, err := loadConfig(e.conf)
		if err != nil {
			return err
		}
		mg.OnConfigLoaded(nu)
		return nil
	}
	e.mg = mg
	sup := NewSupervisor(context.Background(), newDaemonConfig())
	st := NewStatusServer("", sup)
	st.mg = mg
	e.ws = NewWebServer(mg, st, e.auth)
	e.ts = httptest.NewServer(e.ws.Handler())
	t.Cleanup(e.ts.Close)
	// A bare client that keeps cookies by hand: the Secure cookie would be
	// dropped by a jar over plain HTTP.
	e.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return e
}

type resp struct {
	code int
	body map[string]any
	raw  []byte
	hdr  http.Header
}

func (e *webEnv) do(method, path string, body any, mods ...func(*http.Request)) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, m := range mods {
		m(req)
	}
	res, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return resp{res.StatusCode, m, raw, res.Header}
}

var cookie string

func (e *webEnv) login(user, pass string) resp {
	r := e.do("POST", "/api/login", map[string]string{"username": user, "password": pass})
	cookie, e.csrf = "", ""
	if r.code == 200 {
		for _, c := range (&http.Response{Header: r.hdr}).Cookies() {
			if c.Name == sessionCookie {
				cookie = c.Name + "=" + c.Value
				if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
					e.t.Fatalf("session cookie flags wrong: %+v", c)
				}
			}
		}
		e.csrf, _ = r.body["csrf"].(string)
	}
	return r
}

func withAuth(e *webEnv, csrf bool) func(*http.Request) {
	return func(r *http.Request) {
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		if csrf {
			r.Header.Set("X-CSRF-Token", e.csrf)
		}
	}
}

func TestLoginRules(t *testing.T) {
	e := newWebEnv(t)
	p := defaultWeb()
	p.MaxFailedLogins = 20 // this test is about the messages, not the lockout
	e.ws.policy.Store(&p)
	for _, c := range []struct{ u, p string }{{"alice", "wrong"}, {"bob", "pw"}, {"nobody", "pw"}, {"", "pw"}} { // stay under the lockout threshold
		r := e.login(c.u, c.p)
		if r.code != http.StatusUnauthorized {
			t.Fatalf("%v: code %d", c, r.code)
		}
		// identical message for every cause
		if !strings.Contains(r.body["error"].(string), "Login failed") || strings.Contains(r.body["error"].(string), "ddgw") {
			t.Fatalf("%v: %v", c, r.body)
		}
		if _, has := r.body["attempts_left"]; has {
			t.Fatalf("%v: attempts left is reported: %v", c, r.body)
		}
	}
	// bob authenticated in PAM but is not in the ddgw group → still refused
	if r := e.login("alice", "pw"); r.code != 200 || r.body["user"] != "alice" || r.body["version"] != version() {
		t.Fatalf("alice login: %d %v", r.code, r.body)
	}
}

func TestAPIRequiresSession(t *testing.T) {
	e := newWebEnv(t)
	cookie = ""
	for _, p := range []string{"/api/session", "/api/gateways", "/api/neighbors", "/api/dns", "/api/config"} {
		if r := e.do("GET", p, nil); r.code != 401 {
			t.Fatalf("GET %s without session = %d", p, r.code)
		}
	}
	if r := e.do("POST", "/api/assert-agc", map[string]any{}); r.code != 401 {
		t.Fatalf("assert-agc without session = %d", r.code)
	}
	if r := e.do("GET", "/api/session", nil, func(r *http.Request) { r.Header.Set("Cookie", sessionCookie+"=forged") }); r.code != 401 {
		t.Fatal("forged cookie accepted")
	}
}

func TestCLIParityEndpoints(t *testing.T) {
	e := newWebEnv(t)
	e.login("alice", "pw")
	for _, p := range []string{"/api/session", "/api/gateways", "/api/neighbors", "/api/dns", "/api/config"} {
		if r := e.do("GET", p, nil, withAuth(e, false)); r.code != 200 {
			t.Fatalf("GET %s = %d %s", p, r.code, r.raw)
		}
	}
	// no engines running → assert-agc reports it like the CLI
	r := e.do("POST", "/api/assert-agc", map[string]any{}, withAuth(e, true))
	if r.code != 200 || r.body["ok"] != false {
		t.Fatalf("assert-agc: %d %v", r.code, r.body)
	}
	// no gateway, no DNS proxy
	r = e.do("GET", "/api/dns", nil, withAuth(e, false))
	if r.body["ok"] != false || !strings.Contains(r.body["error"].(string), "no gateway") {
		t.Fatalf("dns: %v", r.body)
	}
}

func TestCSRFAndOrigin(t *testing.T) {
	e := newWebEnv(t)
	e.login("alice", "pw")
	body := map[string]any{}
	if r := e.do("POST", "/api/assert-agc", body, withAuth(e, false)); r.code != 403 {
		t.Fatalf("missing CSRF token accepted: %d", r.code)
	}
	if r := e.do("POST", "/api/assert-agc", body, withAuth(e, false), func(r *http.Request) { r.Header.Set("X-CSRF-Token", "wrong") }); r.code != 403 {
		t.Fatalf("wrong CSRF token accepted: %d", r.code)
	}
	if r := e.do("POST", "/api/assert-agc", body, withAuth(e, true), func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }); r.code != 403 {
		t.Fatalf("cross-origin accepted: %d", r.code)
	}
	if r := e.do("POST", "/api/assert-agc", body, withAuth(e, true), func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }); r.code != 415 {
		t.Fatalf("non-JSON content type accepted: %d", r.code)
	}
	if r := e.do("POST", "/api/assert-agc", body, withAuth(e, true), func(r *http.Request) { r.Header.Set("Origin", e.ts.URL) }); r.code != 200 {
		t.Fatalf("same-origin refused: %d", r.code)
	}
	if r := e.do("PUT", "/api/config", map[string]any{"config": map[string]any{}}, withAuth(e, false)); r.code != 403 {
		t.Fatalf("config PUT without CSRF: %d", r.code)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	e := newWebEnv(t)
	e.login("alice", "pw")
	old := cookie
	if r := e.do("POST", "/api/logout", map[string]any{}, withAuth(e, true)); r.code != 200 {
		t.Fatal("logout failed")
	}
	cookie = old
	if r := e.do("GET", "/api/session", nil, withAuth(e, false)); r.code != 401 {
		t.Fatal("session survived logout")
	}
}

func TestSessionIdleTimeoutAndGroupRevocation(t *testing.T) {
	e := newWebEnv(t)
	e.login("alice", "pw")
	e.ws.mu.Lock()
	for _, s := range e.ws.sessions {
		s.last = time.Now().Add(-time.Hour)
	}
	e.ws.mu.Unlock()
	if r := e.do("GET", "/api/session", nil, withAuth(e, false)); r.code != 401 {
		t.Fatal("idle session not expired")
	}

	e.login("alice", "pw")
	e.auth.removed.Store(true) // alice leaves the ddgw group
	e.ws.mu.Lock()
	for _, s := range e.ws.sessions {
		s.checked = time.Now().Add(-time.Hour) // force the periodic re-check
	}
	e.ws.mu.Unlock()
	if r := e.do("GET", "/api/session", nil, withAuth(e, false)); r.code != 401 {
		t.Fatal("session survived group removal")
	}
}

func TestLoginThrottle(t *testing.T) {
	e := newWebEnv(t)
	max := defaultWeb().MaxFailedLogins // 3 attempts, 1 minute window, 15 minute lockout
	for i := 1; i < max; i++ {
		r := e.login("alice", "nope")
		// a guesser is not told how many tries it has left, nor what the policy is, nor which group is needed
		msg, _ := r.body["error"].(string)
		if _, has := r.body["attempts_left"]; has || r.code != 401 || strings.ContainsAny(msg, "0123456789") ||
			strings.Contains(msg, "left") || strings.Contains(msg, "ddgw") || strings.Contains(msg, "lockout") {
			t.Fatalf("attempt %d of %d leaks something: %d %v", i, max, r.code, r.body)
		}
	}
	// the attempt that reaches the limit already reports the lockout
	r := e.login("alice", "nope")
	if ra, _ := r.body["retry_after"].(float64); r.code != http.StatusTooManyRequests || ra < 899 || ra > 901 {
		t.Fatalf("expected a 15 minute lockout on the last failure, got %d %v", r.code, r.body)
	}
	before := e.auth.authCalls.Load()
	r = e.login("alice", "pw") // correct password, but locked out
	if r.code != http.StatusTooManyRequests || r.hdr.Get("Retry-After") == "" {
		t.Fatalf("expected 429 lockout, got %d", r.code)
	}
	if e.auth.authCalls.Load() != before {
		t.Fatal("PAM was consulted while locked out")
	}
	// the login page can ask whether this address is locked
	st := e.do("GET", "/api/login/state", nil, withAuth(e, false))
	if st.code != 200 || st.body["locked"] != true {
		t.Fatalf("login state: %d %v", st.code, st.body)
	}
	for _, k := range []string{"max_failures", "window_minutes", "lockout_minutes"} {
		if _, has := st.body[k]; has {
			t.Fatalf("the login page state tells the policy (%s): %v", k, st.body)
		}
	}
	// not locked: only that is said, to an address that has not failed
	if fresh := e.do("GET", "/api/login/state", nil, withAuth(e, false)); fresh.body["locked"] == nil {
		t.Fatalf("no locked field: %v", fresh.body)
	}
}

func TestLoginThrottleUsesConfiguredPolicy(t *testing.T) {
	e := newWebEnv(t)
	p := defaultWeb()
	p.MaxFailedLogins, p.LockoutMinutes = 1, 2
	e.ws.policy.Store(&p)
	r := e.login("bob", "nope")
	if ra, _ := r.body["retry_after"].(float64); r.code != http.StatusTooManyRequests || ra < 119 || ra > 121 {
		t.Fatalf("one allowed failure, two minute lockout: %d %v", r.code, r.body)
	}
}

func TestSecurityHeadersAndStatic(t *testing.T) {
	e := newWebEnv(t)
	cookie = ""
	res, err := http.Get(e.ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	page, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !bytes.Contains(page, []byte("/app.js")) {
		t.Fatalf("index: %d", res.StatusCode)
	}
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("weak CSP: %s", csp)
	}
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if res.Header.Get(h) == "" {
			t.Fatalf("missing %s", h)
		}
	}
	if bytes.Contains(page, []byte("<script>")) || bytes.Contains(page, []byte(" onclick=")) {
		t.Fatal("inline script in index.html would violate the CSP")
	}
	for _, p := range []string{"/app.js", "/style.css"} {
		r, _ := http.Get(e.ts.URL + p)
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 || len(b) < 500 {
			t.Fatalf("%s: %d len=%d", p, r.StatusCode, len(b))
		}
	}
	// the stylesheet must follow the system theme
	css, _ := http.Get(e.ts.URL + "/style.css")
	cb, _ := io.ReadAll(css.Body)
	css.Body.Close()
	if !bytes.Contains(cb, []byte("prefers-color-scheme: dark")) || !bytes.Contains(cb, []byte("color-scheme: light dark")) {
		t.Fatal("stylesheet does not follow the system colour scheme")
	}
	if r, _ := http.Get(e.ts.URL + "/etc/passwd"); r.StatusCode != 404 {
		t.Fatalf("unexpected route: %d", r.StatusCode)
	}
}

func TestConfigGetPutValidateAndSave(t *testing.T) {
	e := newWebEnv(t)
	e.login("alice", "pw")

	r := e.do("GET", "/api/config", nil, withAuth(e, false))
	if r.body["exists"] != false || r.body["path"] != e.conf {
		t.Fatalf("get: %v", r.body)
	}
	cfg := r.body["config"].(map[string]any)
	if g, _ := cfg["groups"].([]any); len(g) == 0 { // no file yet means no gateways; give the test one to edit
		b, _ := json.Marshal(defaultGroup())
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		cfg["groups"] = []any{m}
	}

	// invalid: bad VIP → 422, nothing written
	cfg["groups"].([]any)[0].(map[string]any)["vip4"] = "not-an-ip"
	r = e.do("PUT", "/api/config", map[string]any{"config": cfg}, withAuth(e, true))
	if r.code != 422 || !strings.Contains(r.body["error"].(string), "vip4") {
		t.Fatalf("invalid config: %d %v", r.code, r.body)
	}
	if _, err := os.Stat(e.conf); err == nil {
		t.Fatal("invalid config was written")
	}

	// unknown field (typo) rejected
	cfg["groups"].([]any)[0].(map[string]any)["vip4"] = "10.9.0.1/24"
	cfg["groups"].([]any)[0].(map[string]any)["prio"] = 5
	if r = e.do("PUT", "/api/config", map[string]any{"config": cfg}, withAuth(e, true)); r.code != 400 {
		t.Fatalf("unknown field: %d %v", r.code, r.body)
	}
	delete(cfg["groups"].([]any)[0].(map[string]any), "prio")

	// an explicit empty groups list means "no gateways", not the default group
	empty := map[string]any{"log_level": "info", "groups": []any{}}
	if r = e.do("PUT", "/api/config?dry_run=1", map[string]any{"config": empty}, withAuth(e, true)); r.code != 200 {
		t.Fatalf("zero groups: %d %v", r.code, r.body)
	}
	if g, _ := r.body["config"].(map[string]any)["groups"].([]any); len(g) != 0 {
		t.Fatalf("zero groups became %v", g)
	}

	// dry run: valid, not written, not applied
	if r = e.do("PUT", "/api/config?dry_run=1", map[string]any{"config": cfg}, withAuth(e, true)); r.code != 200 {
		t.Fatalf("dry run: %d %v", r.code, r.body)
	}
	if _, err := os.Stat(e.conf); err == nil || e.reloads.Load() != 0 {
		t.Fatal("dry run wrote or applied")
	}

	// real save: written 0600, applied via reload, GET round-trips
	cfg["groups"].([]any)[0].(map[string]any)["dns_proxy"] = true
	cfg["dns"] = map[string]any{"servers": []any{"10.0.0.53"}, "queries": []any{map[string]any{"name": "example.com", "type": "a"}}}
	r = e.do("PUT", "/api/config", map[string]any{"config": cfg}, withAuth(e, true))
	if r.code != 200 {
		t.Fatalf("save: %d %v", r.code, r.body)
	}
	if e.reloads.Load() != 1 {
		t.Fatalf("reload calls = %d", e.reloads.Load())
	}
	st, err := os.Stat(e.conf)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("saved file: %v %v", err, st)
	}
	dc, err := loadConfig(e.conf)
	if err != nil || dc.Groups[0].VIP4 != "10.9.0.1/24" || !dc.Groups[0].DNSProxy ||
		dc.DNS.Servers[0] != "10.0.0.53:53" || dc.DNS.Queries[0].Type != "A" {
		t.Fatalf("saved content wrong: %v %+v", err, dc)
	}
	r = e.do("GET", "/api/config", nil, withAuth(e, false))
	if r.body["exists"] != true {
		t.Fatal("exists flag not updated")
	}

	// an existing file keeps its mode
	os.Chmod(e.conf, 0o640)
	e.do("PUT", "/api/config", map[string]any{"config": cfg}, withAuth(e, true))
	if st, _ := os.Stat(e.conf); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode not preserved: %v", st.Mode())
	}
}

func TestWebConfigValidation(t *testing.T) {
	ok := defaultWeb()
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if ok.Listen != ":53853" || ok.Group != "ddgw" || ok.PAMService != "ddgw" || ok.MaxFailedLogins != 3 || ok.FailedLoginWindowMinutes != 1 || ok.LockoutMinutes != 15 {
		t.Fatalf("defaults wrong: %+v", ok)
	}
	for name, mod := range map[string]func(*WebConfig){
		"bad listen":   func(w *WebConfig) { w.Listen = "53853" },
		"bad port":     func(w *WebConfig) { w.Listen = ":99999" },
		"cert w/o key": func(w *WebConfig) { w.CertFile = "/x.crt" },
		"empty group":  func(w *WebConfig) { w.Group = "" },
		"zero idle":    func(w *WebConfig) { w.SessionIdleMinutes = 0 },
		"zero tries":   func(w *WebConfig) { w.MaxFailedLogins = 0 },
		"zero window":  func(w *WebConfig) { w.FailedLoginWindowMinutes = 0 },
		"zero lockout": func(w *WebConfig) { w.LockoutMinutes = 0 },
	} {
		w := defaultWeb()
		mod(&w)
		if w.Validate() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// the old "enabled" key still loads (the GUI is always on) and is dropped
	var legacy WebConfig
	if err := json.Unmarshal([]byte(`{"enabled": false, "listen": ":1234"}`), &legacy); err != nil {
		t.Fatalf("old config with web.enabled must still load: %v", err)
	}
	if legacy.Listen != ":1234" || legacy.LegacyEnabled != nil {
		t.Fatalf("legacy load: %+v", legacy)
	}
	if b, _ := json.Marshal(legacy); strings.Contains(string(b), `"enabled"`) {
		t.Fatalf("enabled must not be written back: %s", b)
	}
}

// End to end over real TLS with a self-signed certificate generated into the
// config directory.  Skipped where PAM is unavailable (non-cgo builds).
func TestHTTPSListenerSelfSigned(t *testing.T) {
	if !pamAvailable {
		t.Skip("built without PAM")
	}
	e := newWebEnv(t)
	cfg := defaultWeb()
	cfg.Listen = "127.0.0.1:0"
	// pick a free port first, since Apply takes an address
	ln, err := newLocalListener()
	if err != nil {
		t.Skip(err)
	}
	cfg.Listen = ln
	e.ws.Apply(cfg)
	defer e.ws.Stop()

	if _, err := os.Stat(filepath.Join(filepath.Dir(e.conf), "ddgw-web.key")); err != nil {
		t.Fatalf("self-signed key not stored: %v", err)
	}
	if st, _ := os.Stat(filepath.Join(filepath.Dir(e.conf), "ddgw-web.key")); st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", st.Mode())
	}
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: 5 * time.Second}
	res, err := c.Get("https://" + cfg.Listen + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || res.TLS == nil || res.TLS.Version < tls.VersionTLS12 {
		t.Fatalf("https: %d tls=%v", res.StatusCode, res.TLS)
	}
	// plain HTTP to the TLS port must not serve the app
	if r, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + cfg.Listen + "/"); err == nil && r.StatusCode == 200 {
		t.Fatal("served over plain HTTP")
	}
}

func newLocalListener() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

func TestSessionsSurviveARestart(t *testing.T) {
	e := newWebEnv(t)
	if r := e.login("alice", "pw"); r.code != 200 {
		t.Fatalf("login: %d", r.code)
	}
	tok := strings.TrimPrefix(cookie, sessionCookie+"=")
	if tok == "" {
		t.Fatal("no session cookie")
	}
	f := filepath.Join(e.ws.stateDir, sessionsFile)
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), tok) {
		t.Fatal("the cookie value itself must not be written to disk")
	}
	if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o600 {
		t.Fatalf("sessions file mode %v", fi.Mode())
	}
	// a new process: a fresh WebServer over the same state dir
	ws2 := NewWebServer(e.mg, nil, e.auth)
	r := httptest.NewRequest("GET", "/api/session", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	if ws2.lookup(r) == nil {
		t.Fatal("the session did not survive the restart")
	}
	// a stopping daemon leaves the file alone
	ws2.Stop()
	if _, err := os.Stat(f); err != nil {
		t.Fatal("Stop removed the saved sessions")
	}
	// logout removes it
	e.do("POST", "/api/logout", nil, withAuth(e, true))
	if _, err := os.Stat(f); err == nil {
		t.Fatal("no sessions left but the file is still there")
	}
}
