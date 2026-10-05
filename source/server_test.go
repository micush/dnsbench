package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakePAM accepts exactly alice/secret and records how often it was asked.
func fakePAM(t *testing.T) *atomic.Int32 {
	var calls atomic.Int32
	old, oldGroup := pamAuth, groupMember
	pamAuth = func(service, user, pass string) bool {
		calls.Add(1)
		return user == "alice" && pass == "secret"
	}
	groupMember = func(group, user string) (bool, error) { return user == "alice", nil }
	t.Cleanup(func() { pamAuth, groupMember = old, oldGroup })
	return &calls
}

type testServer struct {
	*httptest.Server
	app    *App
	client *http.Client
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	fakePAM(t)
	a := testApp(t)
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &testServer{Server: srv, app: a, client: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *testServer) do(t *testing.T, method, path, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, s.URL+path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (s *testServer) postJSON(t *testing.T, path string, v any, hdr map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if hdr == nil {
		hdr = map[string]string{}
	}
	hdr["Content-Type"] = "application/json"
	resp, body := s.do(t, "POST", path, string(b), hdr)
	var m map[string]any
	json.Unmarshal([]byte(body), &m)
	return resp, m
}

func (s *testServer) apiToken(t *testing.T) string {
	_, m := s.postJSON(t, "/api/login", map[string]string{"username": "alice", "password": "secret"}, nil)
	tok, _ := m["token"].(string)
	if tok == "" {
		t.Fatalf("no token: %v", m)
	}
	return tok
}

func (s *testServer) formLogin(t *testing.T, user, pass string) (*http.Response, string) {
	form := url.Values{"username": {user}, "password": {pass}}
	return s.do(t, "POST", "/login", form.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

// ── Authentication ───────────────────────────────────────────────────────────

func TestProtectedRoutesRequireLogin(t *testing.T) {
	s := newTestServer(t)
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/jobs"}, {"POST", "/api/start-job"}, {"GET", "/api/job/x"}, {"GET", "/api/job/x/stream"},
		{"GET", "/api/job/x/output"}, {"POST", "/api/job/x/kill"}, {"GET", "/api/schedules"},
		{"GET", "/api/scheduler-history"}, {"POST", "/api/schedules/add"}, {"POST", "/api/schedules/update"},
		{"POST", "/api/schedules/delete"}, {"POST", "/api/schedules/pause"}, {"POST", "/api/schedules/resume"},
		{"POST", "/api/schedules/run"}, {"GET", "/api/readme-html"}, {"GET", "/api/license-html"},
	} {
		resp, body := s.do(t, r.method, r.path, "{}", nil)
		if resp.StatusCode != 401 {
			t.Errorf("%s %s: status %d, body %s", r.method, r.path, resp.StatusCode, body)
		}
	}
	// A bad token is no better than none.
	resp, _ := s.do(t, "GET", "/api/jobs", "", map[string]string{"X-Session-Token": "nope"})
	if resp.StatusCode != 401 {
		t.Fatalf("bad token accepted: %d", resp.StatusCode)
	}
	resp, _ = s.do(t, "GET", "/bench", "", nil)
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/login" {
		t.Fatalf("/bench without session: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = s.do(t, "GET", "/", "", nil)
	if resp.Header.Get("Location") != "/login" {
		t.Fatalf("/ should redirect to /login, got %q", resp.Header.Get("Location"))
	}
}

func TestFormLoginSetsHardenedCookie(t *testing.T) {
	s := newTestServer(t)
	resp, _ := s.formLogin(t, "alice", "secret")
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/bench" {
		t.Fatalf("login: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	var c *http.Cookie
	for _, k := range resp.Cookies() {
		if k.Name == cookieName {
			c = k
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 3600 || len(c.Value) < 40 {
		t.Fatalf("cookie wrong: %+v", c)
	}
	resp, body := s.do(t, "GET", "/bench", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(body, ">alice<") {
		t.Fatalf("/bench after login: %d", resp.StatusCode)
	}
}

func TestCookieSecureWhenTLSOn(t *testing.T) {
	fakePAM(t)
	a := testApp(t)
	a.cfg.NoTLS = false
	form := url.Values{"username": {"alice"}, "password": {"secret"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)
	var c *http.Cookie
	for _, k := range rec.Result().Cookies() {
		if k.Name == cookieName {
			c = k
		}
	}
	if c == nil || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie must be Secure+HttpOnly+SameSite=Strict when TLS is on: %+v", c)
	}
}

func TestCookieNotSecureWhenTLSOff(t *testing.T) {
	s := newTestServer(t)
	s.app.cfg.NoTLS = true
	resp, _ := s.formLogin(t, "alice", "secret")
	for _, k := range resp.Cookies() {
		if k.Name == cookieName && k.Secure {
			t.Fatal("Secure cookie over plain HTTP would never be sent back")
		}
	}
}

func TestWrongPasswordShowsGenericError(t *testing.T) {
	s := newTestServer(t)
	for _, c := range [][2]string{{"alice", "wrong"}, {"nobody", "secret"}, {"", ""}} {
		resp, body := s.formLogin(t, c[0], c[1])
		if resp.StatusCode != 401 || !strings.Contains(body, "Invalid login attempt") {
			t.Errorf("%v: %d", c, resp.StatusCode)
		}
		if len(resp.Cookies()) != 0 {
			t.Errorf("%v: got a cookie", c)
		}
	}
}

func TestAPILoginTokenHeaderAndLogout(t *testing.T) {
	s := newTestServer(t)
	tok := s.apiToken(t)
	hdr := map[string]string{"X-Session-Token": tok}
	if resp, _ := s.do(t, "GET", "/api/jobs", "", hdr); resp.StatusCode != 200 {
		t.Fatalf("token rejected: %d", resp.StatusCode)
	}
	if _, m := s.postJSON(t, "/api/logout", map[string]string{}, hdr); m["ok"] != true {
		t.Fatalf("logout: %v", m)
	}
	if resp, _ := s.do(t, "GET", "/api/jobs", "", hdr); resp.StatusCode != 401 {
		t.Fatal("token still valid after logout")
	}
	resp, m := s.postJSON(t, "/api/login", map[string]string{"username": "alice", "password": "bad"}, nil)
	if resp.StatusCode != 401 || m["ok"] != false || m["token"] != nil {
		t.Fatalf("bad api login: %d %v", resp.StatusCode, m)
	}
	resp, _ = s.do(t, "POST", "/api/login", "{not json", map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != 400 {
		t.Fatalf("malformed JSON: %d", resp.StatusCode)
	}
}

func TestFormLogoutClearsSession(t *testing.T) {
	s := newTestServer(t)
	s.formLogin(t, "alice", "secret")
	resp, _ := s.do(t, "GET", "/logout", "", nil)
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/login" {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp, _ := s.do(t, "GET", "/bench", "", nil); resp.StatusCode != 302 {
		t.Fatal("still logged in after logout")
	}
}

func TestLoginThrottle(t *testing.T) {
	calls := fakePAM(t)
	a := testApp(t)
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	post := func(pw string) int {
		b, _ := json.Marshal(map[string]string{"username": "alice", "password": pw})
		resp, err := http.Post(srv.URL+"/api/login", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := 0; i < 8; i++ {
		if c := post("wrong"); c != 401 {
			t.Fatalf("attempt %d: %d", i, c)
		}
	}
	before := calls.Load()
	if c := post("secret"); c != http.StatusTooManyRequests {
		t.Fatalf("correct password while throttled: %d", c)
	}
	if calls.Load() != before {
		t.Fatal("PAM was consulted while throttled")
	}
	// The window expires.
	a.throttle.mu.Lock()
	for ip := range a.throttle.fails {
		a.throttle.fails[ip] = []time.Time{time.Now().Add(-10 * time.Minute)}
	}
	a.throttle.mu.Unlock()
	if c := post("secret"); c != 200 {
		t.Fatalf("after window: %d", c)
	}
}

func TestSuccessResetsThrottle(t *testing.T) {
	th := newLoginThrottle()
	for i := 0; i < 7; i++ {
		th.fail("1.2.3.4")
	}
	th.success("1.2.3.4")
	th.fail("1.2.3.4")
	if th.blocked("1.2.3.4") {
		t.Fatal("blocked after a success reset the counter")
	}
	if th.blocked("5.6.7.8") {
		t.Fatal("other addresses must be unaffected")
	}
}

func TestSessionExpiryAndKeepAlive(t *testing.T) {
	st := newSessionStore()
	tok := st.create("bob")
	if st.get(tok) == nil || st.get("") != nil || st.get("zzz") != nil {
		t.Fatal("basic get")
	}
	st.mu.Lock()
	st.m[tok].expires = time.Now().Add(-time.Second)
	st.mu.Unlock()
	if st.get(tok) != nil {
		t.Fatal("expired session accepted")
	}
	st.purge()
	if len(st.m) != 0 {
		t.Fatal("purge left an expired session")
	}
	t1, t2 := st.create("bob"), st.create("carol")
	st.mu.Lock()
	st.m[t1].expires = time.Now().Add(time.Minute)
	st.m[t2].expires = time.Now().Add(time.Minute)
	st.mu.Unlock()
	st.extendFor("bob")
	if st.get(t1).expires.Before(time.Now().Add(50*time.Minute)) || st.get(t2).expires.After(time.Now().Add(2*time.Minute)) {
		t.Fatal("extendFor must lengthen only that user's sessions")
	}
}

func TestPAMFailsClosedOnBadInput(t *testing.T) {
	if realPAMAuth("dnsbench", "", "x") || realPAMAuth("dnsbench", "a\x00b", "x") ||
		realPAMAuth("dnsbench", "root", "p\x00w") || realPAMAuth("dnsbench", strings.Repeat("u", 300), "x") {
		t.Fatal("invalid input was accepted")
	}
}

func TestUsernameIsEscapedInPage(t *testing.T) {
	calls := fakePAM(t)
	_ = calls
	old := pamAuth
	pamAuth = func(_, user, _ string) bool { return true }
	defer func() { pamAuth = old }()
	a := testApp(t)
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	evil := `<script>alert(1)</script>__SERVER_CPU_THREADS__`
	tok := a.sessions.create(evil)
	req, _ := http.NewRequest("GET", srv.URL+"/bench", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if bytes.Contains(b, []byte("<script>alert(1)")) {
		t.Fatal("username injected unescaped")
	}
	if bytes.Contains(b, []byte("__SERVER_CPU_THREADS__")) && !bytes.Contains(b, []byte("&lt;script&gt;")) {
		t.Fatal("placeholder not substituted")
	}
}

func TestResponseHeaders(t *testing.T) {
	s := newTestServer(t)
	resp, _ := s.do(t, "GET", "/login", "", nil)
	for k, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Cache-Control": "no-store", "Referrer-Policy": "same-origin"} {
		if resp.Header.Get(k) != want {
			t.Errorf("%s = %q", k, resp.Header.Get(k))
		}
	}
	resp, body := s.do(t, "GET", "/nope", "", nil)
	if resp.StatusCode != 404 || !strings.Contains(body, "not found") {
		t.Fatalf("404: %d %s", resp.StatusCode, body)
	}
	if resp, _ := s.do(t, "GET", "/api/login", "", nil); resp.StatusCode == 200 {
		t.Fatal("GET /api/login should not succeed")
	}
}

func TestRequestBodyLimit(t *testing.T) {
	s := newTestServer(t)
	tok := s.apiToken(t)
	big := `{"server":"x","queries":"` + strings.Repeat("a", 2<<20) + `"}`
	resp, _ := s.do(t, "POST", "/api/start-job", big, map[string]string{"X-Session-Token": tok, "Content-Type": "application/json"})
	if resp.StatusCode != 400 {
		t.Fatalf("oversized body: %d", resp.StatusCode)
	}
}

// ── Jobs over HTTP ───────────────────────────────────────────────────────────

func TestStartJobValidation(t *testing.T) {
	s := newTestServer(t)
	hdr := map[string]string{"X-Session-Token": s.apiToken(t)}
	for _, c := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"server": ""}, "Server address is required"},
		{map[string]any{}, "Server address is required"},
		{map[string]any{"server": "1.2.3.4", "protocol": "doq"}, "DoQ"},
		{map[string]any{"server": "1.2.3.4", "protocol": "smtp"}, "unknown protocol"},
	} {
		resp, m := s.postJSON(t, "/api/start-job", c.body, hdr)
		if resp.StatusCode != 400 || !strings.Contains(m["error"].(string), c.want) {
			t.Errorf("%v: %d %v", c.body, resp.StatusCode, m)
		}
	}
}

func TestJobLifecycleOverHTTP(t *testing.T) {
	s := newTestServer(t)
	udp := startUDP(t, 0, nil, false)
	hdr := map[string]string{"X-Session-Token": s.apiToken(t)}

	// Numbers and booleans are accepted as well as strings.
	resp, m := s.postJSON(t, "/api/start-job", map[string]any{
		"server": udp.addr, "protocol": "udp", "concurrency": 2, "pipeline": 8, "count": 100,
		"recurse": true, "queries": "a.example\nb.example",
	}, hdr)
	if resp.StatusCode != 200 || m["ok"] != true {
		t.Fatalf("start: %d %v", resp.StatusCode, m)
	}
	id := m["job_id"].(string)

	// The live plain-text stream blocks until the job is done and has no colour markers.
	resp, out := s.do(t, "GET", "/api/job/"+id+"/output", "", hdr)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("output: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if strings.Contains(out, "§") || !strings.Contains(out, "200 sent  200 ok   0 errors") || !strings.Contains(out, "Workers    : 2") {
		t.Fatalf("output wrong:\n%s", out)
	}

	resp, body := s.do(t, "GET", "/api/job/"+id, "", hdr)
	var st map[string]any
	json.Unmarshal([]byte(body), &st)
	if st["status"] != "done" || st["exit_code"] != float64(0) || st["started_at"] == "" {
		t.Fatalf("status: %v", st)
	}
	_, body = s.do(t, "GET", "/api/jobs", "", hdr)
	if !strings.Contains(body, id) {
		t.Fatalf("job missing from list: %s", body)
	}
	if resp, _ := s.do(t, "GET", "/api/job/does-not-exist", "", hdr); resp.StatusCode != 404 {
		t.Fatalf("unknown job: %d", resp.StatusCode)
	}
	if resp, _ := s.do(t, "POST", "/api/job/does-not-exist/kill", "", hdr); resp.StatusCode != 404 {
		t.Fatalf("kill unknown: %d", resp.StatusCode)
	}
}

func TestSSEStreamReplaysEverythingAndFinishes(t *testing.T) {
	s := newTestServer(t)
	udp := startUDP(t, 0, nil, false)
	hdr := map[string]string{"X-Session-Token": s.apiToken(t)}
	_, m := s.postJSON(t, "/api/start-job", map[string]any{"server": udp.addr, "count": 50, "concurrency": 1, "queries": "a.example"}, hdr)
	id := m["job_id"].(string)

	// Subscribe late, after the job has finished: every line must still arrive.
	time.Sleep(500 * time.Millisecond)
	req, _ := http.NewRequest("GET", s.URL+"/api/job/"+id+"/stream", nil)
	req.Header.Set("X-Session-Token", hdr["X-Session-Token"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var lines []string
	var done map[string]any
	for sc.Scan() {
		txt := sc.Text()
		if !strings.HasPrefix(txt, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(txt[6:]), &ev); err != nil {
			t.Fatalf("bad event %q: %v", txt, err)
		}
		if l, ok := ev["line"].(string); ok {
			lines = append(lines, l)
		}
		if ev["done"] == true {
			done = ev
		}
	}
	if done == nil || done["status"] != "done" || done["exit_code"] != float64(0) {
		t.Fatalf("no proper done event: %v", done)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Protocol   : UDP") || !strings.Contains(joined, "50 sent  50 ok") || !strings.Contains(joined, "§hdr§") {
		t.Fatalf("stream missing lines or markers:\n%s", joined)
	}
}

func TestKillOverHTTP(t *testing.T) {
	s := newTestServer(t)
	udp := startUDP(t, 0, nil, false)
	hdr := map[string]string{"X-Session-Token": s.apiToken(t)}
	_, m := s.postJSON(t, "/api/start-job", map[string]any{"server": udp.addr, "duration": "60s"}, hdr)
	id := m["job_id"].(string)
	time.Sleep(300 * time.Millisecond)
	if _, k := s.postJSON(t, "/api/job/"+id+"/kill", map[string]any{}, hdr); k["ok"] != true {
		t.Fatalf("kill: %v", k)
	}
	j := s.app.getJob(id)
	select {
	case <-j.done:
	case <-time.After(5 * time.Second):
		t.Fatal("job not stopped")
	}
	_, body := s.do(t, "GET", "/api/job/"+id, "", hdr)
	if !strings.Contains(body, `"killed"`) {
		t.Fatalf("status after kill: %s", body)
	}
}

func TestOldJobsAreEvicted(t *testing.T) {
	a := testApp(t)
	udp := startUDP(t, 0, nil, false)
	var first string
	for i := 0; i < keepJobs+5; i++ {
		j := a.startJob(args("server", udp.addr, "count", "1", "concurrency", "1"), "u")
		if i == 0 {
			first = j.ID
		}
		<-j.done
	}
	if a.getJob(first) != nil {
		t.Fatal("oldest job kept forever")
	}
	a.jobsMu.Lock()
	n := len(a.jobs)
	a.jobsMu.Unlock()
	if n != keepJobs {
		t.Fatalf("%d jobs retained, want %d", n, keepJobs)
	}
}

// ── Schedules over HTTP ──────────────────────────────────────────────────────

func TestScheduleAPILifecycle(t *testing.T) {
	s := newTestServer(t)
	udp := startUDP(t, 0, nil, false)
	hdr := map[string]string{"X-Session-Token": s.apiToken(t)}

	resp, m := s.postJSON(t, "/api/schedules/add", map[string]any{"server": ""}, hdr)
	if resp.StatusCode != 400 {
		t.Fatalf("empty server: %d", resp.StatusCode)
	}
	resp, m = s.postJSON(t, "/api/schedules/add", map[string]any{"server": udp.addr, "freq": "fortnightly"}, hdr)
	if resp.StatusCode != 400 {
		t.Fatalf("bad freq: %d %v", resp.StatusCode, m)
	}
	resp, m = s.postJSON(t, "/api/schedules/add", map[string]any{
		"name": "nightly", "server": udp.addr, "protocol": "udp", "concurrency": "2", "pipeline": "4",
		"duration": "", "queries": "a.example", "qtype": "A", "recurse": "true",
		"freq": "daily", "hour": 3, "minute": 15,
	}, hdr)
	if resp.StatusCode != 200 || m["ok"] != true {
		t.Fatalf("add: %d %v", resp.StatusCode, m)
	}
	id := m["id"].(string)

	list := func() []map[string]any {
		_, body := s.do(t, "GET", "/api/schedules", "", hdr)
		var out struct {
			Schedules []map[string]any `json:"schedules"`
		}
		json.Unmarshal([]byte(body), &out)
		return out.Schedules
	}
	l := list()
	if len(l) != 1 || l[0]["name"] != "nightly" || l[0]["hour"] != float64(3) || l[0]["minute"] != float64(15) || l[0]["paused"] != false {
		t.Fatalf("list: %v", l)
	}
	nr, err := time.Parse(isoLayout, l[0]["next_run"].(string))
	if err != nil || !nr.After(time.Now()) || nr.In(time.Local).Hour() != 3 || nr.In(time.Local).Minute() != 15 {
		t.Fatalf("next_run %v (%v)", l[0]["next_run"], err)
	}

	if _, u := s.postJSON(t, "/api/schedules/update", map[string]any{"id": id, "name": "renamed", "hour": "5"}, hdr); u["ok"] != true {
		t.Fatalf("update: %v", u)
	}
	l = list()
	if l[0]["name"] != "renamed" || l[0]["hour"] != float64(5) || l[0]["server"] != udp.addr {
		t.Fatalf("after update: %v", l[0])
	}
	if resp, _ := s.postJSON(t, "/api/schedules/update", map[string]any{"id": id, "hour": 99}, hdr); resp.StatusCode != 400 {
		t.Fatalf("hour 99 accepted: %d", resp.StatusCode)
	}
	if resp, _ := s.postJSON(t, "/api/schedules/update", map[string]any{"id": "nope"}, hdr); resp.StatusCode != 404 {
		t.Fatalf("unknown id: %d", resp.StatusCode)
	}

	s.postJSON(t, "/api/schedules/pause", map[string]any{"id": id}, hdr)
	if list()[0]["paused"] != true {
		t.Fatal("pause")
	}
	s.postJSON(t, "/api/schedules/resume", map[string]any{"id": id}, hdr)
	if list()[0]["paused"] != false {
		t.Fatal("resume")
	}

	// Run now: starts a job, and its result lands in the shared history.
	_, r := s.postJSON(t, "/api/schedules/run", map[string]any{"id": id}, hdr)
	jid, _ := r["job_id"].(string)
	if jid == "" {
		t.Fatalf("run: %v", r)
	}
	var runs []map[string]any
	for i := 0; i < 100 && len(runs) == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		_, body := s.do(t, "GET", "/api/scheduler-history", "", hdr)
		var h struct {
			Runs []map[string]any `json:"runs"`
		}
		json.Unmarshal([]byte(body), &h)
		runs = h.Runs
	}
	if len(runs) != 1 || runs[0]["job_id"] != jid || runs[0]["status"] != "done" || runs[0]["schedule_name"] != "renamed" {
		t.Fatalf("history: %v", runs)
	}
	args := runs[0]["args"].(map[string]any)
	if args["concurrency"] != "2" || args["server"] != udp.addr {
		t.Fatalf("history args: %v", args)
	}
	if ls, ok := runs[0]["lines"].([]any); !ok || len(ls) < 10 {
		t.Fatalf("history lines: %v", runs[0]["lines"])
	}
	for i := 0; i < 100 && list()[0]["last_run"] == nil; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	l = list()
	if l[0]["last_run"] == nil || l[0]["last_ok"] != true || l[0]["last_job_id"] != jid {
		t.Fatalf("last-run fields not recorded: %v", l[0])
	}

	if resp, _ := s.postJSON(t, "/api/schedules/delete", map[string]any{"id": id}, hdr); resp.StatusCode != 200 || len(list()) != 0 {
		t.Fatal("delete")
	}
	if resp, _ := s.postJSON(t, "/api/schedules/delete", map[string]any{"id": id}, hdr); resp.StatusCode != 404 {
		t.Fatal("double delete")
	}
	if resp, _ := s.postJSON(t, "/api/schedules/run", map[string]any{"id": id}, hdr); resp.StatusCode != 404 {
		t.Fatal("run deleted")
	}
}

func TestOneShotScheduleRemovesItselfAfterRunning(t *testing.T) {
	s := newTestServer(t)
	udp := startUDP(t, 0, nil, false)
	hdr := map[string]string{"X-Session-Token": s.apiToken(t)}
	run := time.Now().Add(2 * time.Hour).Format("2006-01-02T15:04")
	_, m := s.postJSON(t, "/api/schedules/add", map[string]any{"server": udp.addr, "freq": "once", "run_at": run,
		"duration": "", "concurrency": "1", "queries": "a.example"}, hdr)
	id := m["id"].(string)
	sc, _ := s.app.schedules.get(id)
	if d := sc.NextRun.Sub(time.Now()); d < 119*time.Minute || d > 121*time.Minute {
		t.Fatalf("run_at not honoured: %v", d)
	}
	s.postJSON(t, "/api/schedules/run", map[string]any{"id": id}, hdr)
	for i := 0; i < 100; i++ {
		if _, ok := s.app.schedules.get(id); !ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("one-shot schedule still present after it ran")
}

func TestSchedulesSurviveRestart(t *testing.T) {
	s := newTestServer(t)
	hdr := map[string]string{"X-Session-Token": s.apiToken(t)}
	s.postJSON(t, "/api/schedules/add", map[string]any{"name": "keep", "server": "192.0.2.1", "freq": "weekly", "weekday": 2, "hour": 4}, hdr)
	again := newScheduleStore(s.app.cfg.SchedulesFile)
	again.load()
	l := again.list()
	if len(l) != 1 || l[0].Name != "keep" || l[0].Weekday != 2 || l[0].Hour != 4 {
		t.Fatalf("after reload: %+v", l)
	}
}

// ── Docs pages ───────────────────────────────────────────────────────────────

func TestDocPagesRenderAndEscape(t *testing.T) {
	s := newTestServer(t)
	hdr := map[string]string{"X-Session-Token": s.apiToken(t)}
	_, body := s.do(t, "GET", "/api/readme-html", "", hdr)
	var m map[string]any
	json.Unmarshal([]byte(body), &m)
	if m["ok"] != true || m["html"] == "" {
		t.Fatalf("readme: %s", body)
	}
	resp, page := s.do(t, "GET", "/license", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(page, "GNU GENERAL PUBLIC LICENSE") {
		t.Fatalf("license page: %d", resp.StatusCode)
	}
	resp, page = s.do(t, "GET", "/readme", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(page, "<h3") {
		t.Fatalf("readme page: %d", resp.StatusCode)
	}
}

func TestCrossOriginPostsAreRefused(t *testing.T) {
	s := newTestServer(t)
	tok := s.apiToken(t)
	host := strings.TrimPrefix(s.URL, "http://")
	for origin, want := range map[string]int{
		"https://evil.example": 403, "http://" + host + ".evil.example": 403, "null": 403,
		"http://" + host: 200, "": 200,
	} {
		hdr := map[string]string{"X-Session-Token": tok, "Content-Type": "application/json"}
		if origin != "" {
			hdr["Origin"] = origin
		}
		resp, _ := s.do(t, "POST", "/api/schedules/delete", `{"id":"none"}`, hdr)
		got := resp.StatusCode
		if want == 200 {
			want = 404 // allowed through; the id simply does not exist
		}
		if got != want {
			t.Errorf("Origin %q: %d, want %d", origin, got, want)
		}
	}
	// A cross-site login attempt is refused before PAM is consulted.
	resp, _ := s.do(t, "POST", "/login", "username=alice&password=secret",
		map[string]string{"Origin": "https://evil.example", "Content-Type": "application/x-www-form-urlencoded"})
	if resp.StatusCode != 403 {
		t.Errorf("cross-site login: %d", resp.StatusCode)
	}
	// Reads are never blocked by Origin.
	resp, _ = s.do(t, "GET", "/api/jobs", "", map[string]string{"X-Session-Token": tok, "Origin": "https://other.example"})
	if resp.StatusCode != 200 {
		t.Errorf("GET with foreign Origin: %d", resp.StatusCode)
	}
}

// What real browsers send. A same-origin POST from our own login form carries
// "Origin: null" under a no-referrer policy; that must not be refused when the
// browser also says the request is same-origin. This broke browser login once.
func TestBrowserFetchMetadataIsHonoured(t *testing.T) {
	s := newTestServer(t)
	for _, c := range []struct {
		name, site, origin string
		want               int
	}{
		{"same-origin with null Origin", "same-origin", "null", 200},
		{"same-origin with matching Origin", "same-origin", "http://" + strings.TrimPrefix(s.URL, "http://"), 200},
		{"user-initiated navigation", "none", "", 200},
		{"cross-site", "cross-site", "https://evil.example", 403},
		{"cross-site with a spoofed matching Origin", "cross-site", "http://" + strings.TrimPrefix(s.URL, "http://"), 403},
		{"same-site but another origin", "same-site", "http://other." + strings.TrimPrefix(s.URL, "http://"), 403},
		{"no metadata, null Origin", "", "null", 403},
	} {
		hdr := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
		if c.site != "" {
			hdr["Sec-Fetch-Site"] = c.site
		}
		if c.origin != "" {
			hdr["Origin"] = c.origin
		}
		resp, _ := s.do(t, "POST", "/login", "username=alice&password=secret", hdr)
		got := resp.StatusCode
		if c.want == 200 {
			if got == 403 {
				t.Errorf("%s: refused", c.name)
			}
		} else if got != 403 {
			t.Errorf("%s: %d, want 403", c.name, got)
		}
	}
}

// ── Login group ──────────────────────────────────────────────────────────────

func TestLoginRequiresGroupMembership(t *testing.T) {
	fakePAM(t) // PAM accepts exactly alice/secret
	var asked []string
	member, lookupErr := false, error(nil)
	groupMember = func(group, user string) (bool, error) {
		asked = append(asked, group+"/"+user)
		return member, lookupErr
	}
	a := testApp(t)
	a.cfg.LoginGroup = "staff"
	srv := httptest.NewServer(a.routes())
	defer srv.Close()

	api := func(user, pw string) (int, string) {
		b, _ := json.Marshal(map[string]string{"username": user, "password": pw})
		resp, err := http.Post(srv.URL+"/api/login", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		msg, _ := m["error"].(string)
		return resp.StatusCode, msg
	}

	// Right password, not in the group: refused, with the same message as a bad password.
	code, notMemberMsg := api("alice", "secret")
	if code != 401 || notMemberMsg != "Invalid login attempt" {
		t.Fatalf("non-member: %d %q", code, notMemberMsg)
	}
	if len(asked) != 1 || asked[0] != "staff/alice" {
		t.Fatalf("group check called with %v, want the configured group", asked)
	}
	_, badPassMsg := api("alice", "wrong")
	if badPassMsg != notMemberMsg {
		t.Fatalf("messages differ, which would reveal membership: %q vs %q", badPassMsg, notMemberMsg)
	}
	// A wrong password never reaches the group check.
	if len(asked) != 1 {
		t.Fatalf("group consulted after a failed password: %v", asked)
	}

	// A lookup failure (group missing, directory down) refuses everyone.
	member, lookupErr = true, nil
	lookupErr = errors.New("cannot look up group \"staff\"")
	member = false
	if code, _ := api("alice", "secret"); code != 401 {
		t.Fatalf("lookup error: %d", code)
	}

	// A member gets in, by API and by the form.
	member, lookupErr = true, nil
	if code, _ := api("alice", "secret"); code != 200 {
		t.Fatalf("member via API: %d", code)
	}
	form := url.Values{"username": {"alice"}, "password": {"secret"}}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.PostForm(srv.URL+"/login", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/bench" {
		t.Fatalf("member via form: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// And a non-member through the form sees the generic error page and gets no cookie.
	member = false
	resp, err = client.PostForm(srv.URL+"/login", form)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 401 || !strings.Contains(string(page), "Invalid login attempt") || len(resp.Cookies()) != 0 {
		t.Fatalf("non-member via form: %d cookies=%d", resp.StatusCode, len(resp.Cookies()))
	}
}

func TestNonMemberAttemptsCountTowardTheThrottle(t *testing.T) {
	fakePAM(t)
	groupMember = func(string, string) (bool, error) { return false, nil }
	a := testApp(t)
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	post := func() int {
		b, _ := json.Marshal(map[string]string{"username": "alice", "password": "secret"})
		resp, err := http.Post(srv.URL+"/api/login", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := 0; i < 8; i++ {
		if c := post(); c != 401 {
			t.Fatalf("attempt %d: %d", i, c)
		}
	}
	if c := post(); c != http.StatusTooManyRequests {
		t.Fatalf("9th attempt: %d, want 429", c)
	}
}

func TestPageServesThemeScriptWithoutLogin(t *testing.T) {
	s := newTestServer(t)
	resp, body := s.do(t, "GET", "/theme.js", "", nil)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("theme.js: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, want := range []string{"prefers-color-scheme", "dnsbench-theme", "toggleTheme", "data-theme"} {
		if !strings.Contains(body, want) {
			t.Errorf("theme.js lacks %q", want)
		}
	}
}
