package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/login.html
var loginHTML string

//go:embed web/bench.html
var benchHTML string

//go:embed web/doc.html
var docHTML string

//go:embed web/theme.js
var themeJS string

//go:embed web/ui.css
var uiCSS string

//go:embed web/charts.js
var chartsJS string

//go:embed VERSION
var versionFile string

func version() string { return strings.TrimSpace(versionFile) }

const (
	cookieName   = "dnsbench_session"
	maxBodyBytes = 1 << 20
	keepJobs     = 20 // finished jobs kept for late status lookups
)

// App is the daemon's shared state.
type App struct {
	cfg       *Config
	sessions  *sessionStore
	throttle  *loginThrottle
	schedules *scheduleStore
	pamSvc    string

	jobsMu   sync.Mutex
	jobs     map[string]*Job
	jobOrder []string // creation order, oldest first

	histMu  sync.Mutex
	history []histRun // newest first
}

func newApp(cfg *Config) *App {
	return &App{
		cfg:       cfg,
		sessions:  newSessionStore(),
		throttle:  newLoginThrottle(),
		schedules: newScheduleStore(cfg.SchedulesFile),
		pamSvc:    pamServiceName(cfg.PAMService),
		jobs:      map[string]*Job{},
	}
}

// ── Jobs ─────────────────────────────────────────────────────────────────────

// startJob stops whatever is running (one benchmark at a time, so runs do not
// distort each other) and starts a new job.
func (a *App) startJob(args map[string]string, user string) *Job {
	a.jobsMu.Lock()
	var old []*Job
	for _, j := range a.jobs {
		if j.running() {
			old = append(old, j)
		}
	}
	a.jobsMu.Unlock()
	for _, j := range old {
		j.kill()
	}
	for _, j := range old {
		select {
		case <-j.done:
		case <-time.After(3 * time.Second):
		}
	}

	j := newJob(newUUID(), user, args)
	a.jobsMu.Lock()
	a.jobs[j.ID] = j
	a.jobOrder = append(a.jobOrder, j.ID)
	for len(a.jobOrder) > keepJobs {
		delete(a.jobs, a.jobOrder[0])
		a.jobOrder = a.jobOrder[1:]
	}
	a.jobsMu.Unlock()
	go a.runJob(j)
	return j
}

func (a *App) getJob(id string) *Job {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	return a.jobs[id]
}

// keepAlive extends the sessions of users whose jobs are still running, and
// drops expired sessions, every few minutes.
func (a *App) keepAlive() {
	for {
		time.Sleep(5 * time.Minute)
		a.jobsMu.Lock()
		var users []string
		for _, j := range a.jobs {
			if j.running() {
				users = append(users, j.User)
			}
		}
		a.jobsMu.Unlock()
		for _, u := range users {
			a.sessions.extendFor(u)
		}
		a.sessions.purge()
	}
}

func (a *App) killAll() {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	for _, j := range a.jobs {
		if j.running() {
			j.kill()
		}
	}
}

// ── Routing ──────────────────────────────────────────────────────────────────

type authed func(w http.ResponseWriter, r *http.Request, s *session)

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", a.handleRoot)
	mux.HandleFunc("GET /theme.js", a.handleThemeJS)
	mux.HandleFunc("GET /ui.css", a.handleUICSS)
	mux.HandleFunc("GET /charts.js", a.handleChartsJS)
	mux.HandleFunc("GET /login", a.handleLoginPage)
	mux.HandleFunc("POST /login", a.handleLoginForm)
	mux.HandleFunc("GET /logout", a.handleLogout)
	mux.HandleFunc("POST /logout", a.handleLogout)
	mux.HandleFunc("GET /bench", a.handleBench)
	mux.HandleFunc("GET /readme", a.docPage("README.md", "ReadMe"))
	mux.HandleFunc("GET /license", a.docPage("LICENSE.txt", "License — GNU General Public License v3"))

	mux.HandleFunc("POST /api/login", a.handleAPILogin)
	mux.HandleFunc("POST /api/logout", a.handleAPILogout)
	mux.HandleFunc("GET /api/jobs", a.auth(a.apiJobs))
	mux.HandleFunc("POST /api/start-job", a.auth(a.apiStartJob))
	mux.HandleFunc("GET /api/job/{id}", a.auth(a.apiJobStatus))
	mux.HandleFunc("GET /api/job/{id}/stream", a.auth(a.apiJobStream))
	mux.HandleFunc("GET /api/job/{id}/output", a.auth(a.apiJobOutput))
	mux.HandleFunc("POST /api/job/{id}/kill", a.auth(a.apiJobKill))

	mux.HandleFunc("GET /api/schedules", a.auth(a.apiSchedulesList))
	mux.HandleFunc("GET /api/scheduler-history", a.auth(a.apiSchedulerHistory))
	mux.HandleFunc("POST /api/schedules/add", a.auth(a.apiSchedulesAdd))
	mux.HandleFunc("POST /api/schedules/update", a.auth(a.apiSchedulesUpdate))
	mux.HandleFunc("POST /api/schedules/delete", a.auth(a.apiSchedulesDelete))
	mux.HandleFunc("POST /api/schedules/pause", a.auth(a.apiSchedulesPause))
	mux.HandleFunc("POST /api/schedules/resume", a.auth(a.apiSchedulesResume))
	mux.HandleFunc("POST /api/schedules/run", a.auth(a.apiSchedulesRun))
	mux.HandleFunc("GET /api/readme-html", a.auth(a.apiDoc("README.md")))
	mux.HandleFunc("GET /api/license-html", a.auth(a.apiDoc("LICENSE.txt")))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	})
	return securityHeaders(mux)
}

// sameOrigin reports whether a state-changing request came from this site.
//
// Browsers say so themselves in Sec-Fetch-Site, which cannot be forged by a
// page: "same-origin" (our own pages) and "none" (typed or bookmarked) pass,
// anything else ("same-site" is another port or subdomain) is refused. Without
// that header (curl, scripts, older browsers) the Origin header is compared with
// the Host instead, and a request carrying neither is allowed: scripts like that
// authenticate with a token header and no browser cookie is involved.
//
// Note that a browser sends "Origin: null" even for our own forms when the
// referrer policy is no-referrer, which is why that policy is not used.
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			jerr(w, http.StatusForbidden, "Cross-origin request refused")
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func jerr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": msg})
}

func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// sessionFrom finds the session from the cookie or the X-Session-Token header.
func (a *App) sessionFrom(r *http.Request) (*session, string) {
	if c, err := r.Cookie(cookieName); err == nil {
		if s := a.sessions.get(c.Value); s != nil {
			return s, c.Value
		}
	}
	if tok := r.Header.Get("X-Session-Token"); tok != "" {
		if s := a.sessions.get(tok); s != nil {
			return s, tok
		}
	}
	return nil, ""
}

func (a *App) auth(h authed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, _ := a.sessionFrom(r)
		if s == nil {
			jerr(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		h(w, r, s)
	}
}

// decodeArgs reads a JSON object body into string values; numbers and booleans
// are converted, so clients may send either form.
func decodeArgs(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	var raw map[string]any
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&raw); err != nil {
		if err == io.EOF {
			return map[string]string{}, true
		}
		jerr(w, http.StatusBadRequest, "Invalid JSON body")
		return nil, false
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = jsonString(v)
	}
	return out, true
}

// ── Pages and login ──────────────────────────────────────────────────────────

func renderLogin(errMsg string) string {
	page := loginHTML
	if errMsg != "" {
		alert := `<div class="alert alert-danger text-center py-2"><span>` + html.EscapeString(errMsg) + `</span></div>`
		page = strings.Replace(page, "<form method=", alert+"<form method=", 1)
	}
	return page
}

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	if s, _ := a.sessionFrom(r); s != nil {
		http.Redirect(w, r, "/bench", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

// handleThemeJS serves the shared theme script; it is needed by the login page.
func (a *App) handleThemeJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	io.WriteString(w, themeJS)
}

func (a *App) handleUICSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	io.WriteString(w, uiCSS)
}

func (a *App) handleChartsJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	io.WriteString(w, chartsJS)
}

func (a *App) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	writeHTML(w, http.StatusOK, renderLogin(""))
}

// authenticate checks the credentials (with throttling) and creates a session.
// On failure it returns the message to show and the HTTP status to use.
func (a *App) authenticate(r *http.Request, user, pass string) (token, msg string, status int) {
	ip := clientIP(r)
	if a.throttle.blocked(ip) {
		return "", "Too many failed attempts. Try again in a few minutes.", http.StatusTooManyRequests
	}
	if !pamAuth(a.pamSvc, user, pass) {
		a.throttle.fail(ip)
		log.Printf("login: failed for %q from %s", user, ip)
		return "", "Invalid login attempt", http.StatusUnauthorized
	}
	// Right password, but only members of the login group may use dnsbench. This
	// runs after PAM so a wrong password never reveals who is in the group, the
	// user sees the same message as for a bad password, and any error (such as
	// the group not existing) refuses the login.
	if ok, err := groupMember(a.cfg.LoginGroup, user); !ok {
		a.throttle.fail(ip)
		if err != nil {
			log.Printf("login: refused %q from %s: %v", user, ip, err)
		} else {
			log.Printf("login: refused %q from %s: not a member of group %q", user, ip, a.cfg.LoginGroup)
		}
		return "", "Invalid login attempt", http.StatusUnauthorized
	}
	a.throttle.success(ip)
	log.Printf("login: %q from %s", user, ip)
	return a.sessions.create(user), "", http.StatusOK
}

func (a *App) setSessionCookie(w http.ResponseWriter, tok string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: !a.cfg.NoTLS,
	})
}

func (a *App) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		writeHTML(w, http.StatusBadRequest, renderLogin("Invalid request"))
		return
	}
	user := strings.TrimSpace(r.PostForm.Get("username"))
	tok, msg, status := a.authenticate(r, user, r.PostForm.Get("password"))
	if tok == "" {
		writeHTML(w, status, renderLogin(msg))
		return
	}
	a.setSessionCookie(w, tok, int(sessionTTL.Seconds()))
	http.Redirect(w, r, "/bench", http.StatusFound)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if _, tok := a.sessionFrom(r); tok != "" {
		a.sessions.delete(tok)
	}
	a.setSessionCookie(w, "", -1)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (a *App) handleBench(w http.ResponseWriter, r *http.Request) {
	s, _ := a.sessionFrom(r)
	if s == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	page := strings.NewReplacer(
		"__USER__", html.EscapeString(s.user),
		"__SERVER_CPU_THREADS__", strconv.Itoa(cpuThreads()),
	).Replace(benchHTML)
	writeHTML(w, http.StatusOK, page)
}

func (a *App) docPage(file, title string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := strings.NewReplacer("__TITLE__", html.EscapeString(title), "__BODY__", docBody(file)).Replace(docHTML)
		writeHTML(w, http.StatusOK, page)
	}
}

func (a *App) apiDoc(file string) authed {
	return func(w http.ResponseWriter, r *http.Request, _ *session) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "html": docBody(file)})
	}
}

// ── Auth API ─────────────────────────────────────────────────────────────────

func (a *App) handleAPILogin(w http.ResponseWriter, r *http.Request) {
	args, ok := decodeArgs(w, r)
	if !ok {
		return
	}
	tok, msg, status := a.authenticate(r, strings.TrimSpace(args["username"]), args["password"])
	if tok == "" {
		jerr(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": tok})
}

func (a *App) handleAPILogout(w http.ResponseWriter, r *http.Request) {
	if _, tok := a.sessionFrom(r); tok != "" {
		a.sessions.delete(tok)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Job API ──────────────────────────────────────────────────────────────────

func (a *App) apiJobs(w http.ResponseWriter, r *http.Request, _ *session) {
	a.jobsMu.Lock()
	out := map[string]any{}
	for id, j := range a.jobs {
		_, st, _ := j.snapshot(1 << 30)
		out[id] = map[string]any{"status": st, "started_at": j.StartedAt}
	}
	a.jobsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "jobs": out})
}

func (a *App) apiStartJob(w http.ResponseWriter, r *http.Request, s *session) {
	args, ok := decodeArgs(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(args["server"]) == "" {
		jerr(w, http.StatusBadRequest, "Server address is required")
		return
	}
	if _, _, err := parseParams(args, cpuThreads()); err != nil {
		jerr(w, http.StatusBadRequest, err.Error())
		return
	}
	j := a.startJob(args, s.user)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job_id": j.ID})
}

func exitJSON(exit *int) any {
	if exit == nil {
		return nil
	}
	return *exit
}

func (a *App) apiJobStatus(w http.ResponseWriter, r *http.Request, _ *session) {
	j := a.getJob(r.PathValue("id"))
	if j == nil {
		jerr(w, http.StatusNotFound, "Job not found")
		return
	}
	_, st, exit := j.snapshot(1 << 30)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "status": st, "started_at": j.StartedAt, "exit_code": exitJSON(exit),
	})
}

func (a *App) apiJobKill(w http.ResponseWriter, r *http.Request, _ *session) {
	j := a.getJob(r.PathValue("id"))
	if j == nil {
		jerr(w, http.StatusNotFound, "Job not found")
		return
	}
	j.kill()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// follow calls emit for every output line as it appears, until the job
// finishes or the client goes away. It returns the final status and exit code.
func follow(r *http.Request, j *Job, emit func(line string) error) (status string, exit *int, err error) {
	sent := 0
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		lines, st, ex := j.snapshot(sent)
		for _, l := range lines {
			if err := emit(l); err != nil {
				return st, ex, err
			}
			sent++
		}
		if st != "pending" && st != "running" {
			// One more look: lines may have been added just before the status flipped.
			lines, st, ex = j.snapshot(sent)
			for _, l := range lines {
				if err := emit(l); err != nil {
					return st, ex, err
				}
			}
			return st, ex, nil
		}
		select {
		case <-r.Context().Done():
			return st, ex, r.Context().Err()
		case <-tick.C:
		}
	}
}

func (a *App) apiJobStream(w http.ResponseWriter, r *http.Request, _ *session) {
	j := a.getJob(r.PathValue("id"))
	if j == nil {
		jerr(w, http.StatusNotFound, "Job not found")
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		jerr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	status, exit, err := follow(r, j, func(line string) error {
		b, _ := json.Marshal(line)
		if _, err := fmt.Fprintf(w, "data: {\"line\":%s}\n\n", b); err != nil {
			return err
		}
		fl.Flush()
		return nil
	})
	if err != nil {
		return
	}
	done, _ := json.Marshal(map[string]any{"done": true, "status": status, "exit_code": exitJSON(exit)})
	fmt.Fprintf(w, "data: %s\n\n", done)
	fl.Flush()
}

func (a *App) apiJobOutput(w http.ResponseWriter, r *http.Request, _ *session) {
	j := a.getJob(r.PathValue("id"))
	if j == nil {
		jerr(w, http.StatusNotFound, "Job not found")
		return
	}
	fl, _ := w.(http.Flusher)
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	follow(r, j, func(line string) error {
		if _, err := io.WriteString(w, stripTags(line)+"\n"); err != nil {
			return err
		}
		if fl != nil {
			fl.Flush()
		}
		return nil
	})
}

// ── Schedule API ─────────────────────────────────────────────────────────────

func (a *App) apiSchedulerHistory(w http.ResponseWriter, r *http.Request, _ *session) {
	a.histMu.Lock()
	runs := append([]histRun{}, a.history...)
	a.histMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runs": runs})
}

func (a *App) apiSchedulesList(w http.ResponseWriter, r *http.Request, _ *session) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "schedules": a.schedules.list()})
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

// applyTiming copies timing fields from args into s where present.
func applyTiming(s *Schedule, args map[string]string) {
	if v, ok := args["freq"]; ok {
		s.Freq = v
	}
	for key, dst := range map[string]*int{"minute": &s.Minute, "hour": &s.Hour, "weekday": &s.Weekday, "monthday": &s.Monthday} {
		if v, ok := args[key]; ok {
			*dst = atoiOr(v, *dst)
		}
	}
}

func (a *App) apiSchedulesAdd(w http.ResponseWriter, r *http.Request, _ *session) {
	args, ok := decodeArgs(w, r)
	if !ok {
		return
	}
	server := strings.TrimSpace(args["server"])
	if server == "" {
		jerr(w, http.StatusBadRequest, "server is required")
		return
	}
	get := func(k, def string) string {
		if v, ok := args[k]; ok {
			return v
		}
		return def
	}
	s := Schedule{
		ID: newUUID(), Name: args["name"], Server: server,
		Protocol: get("protocol", "udp"), Concurrency: get("concurrency", "10"),
		Pipeline: get("pipeline", "8"), Duration: get("duration", "30s"),
		Queries: args["queries"], Qtype: get("qtype", "ANY"),
		Recurse: get("recurse", "true") != "false",
		Freq:    "daily", Hour: 2, Weekday: 1, Monthday: 1,
	}
	applyTiming(&s, args)
	if err := s.validate(); err != nil {
		jerr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Freq == "once" {
		t, ok := parseLocalTime(args["run_at"])
		if !ok {
			t = time.Now().Add(time.Hour)
		}
		s.NextRun = t
	} else {
		s.NextRun = nextRunAfter(time.Now(), &s)
	}
	a.schedules.add(s)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": s.ID})
}

func (a *App) apiSchedulesUpdate(w http.ResponseWriter, r *http.Request, _ *session) {
	args, ok := decodeArgs(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(args["id"])
	cur, found := a.schedules.get(id)
	if !found {
		jerr(w, http.StatusNotFound, "Not found")
		return
	}
	next := cur
	set := func(k string, dst *string) {
		if v, ok := args[k]; ok {
			*dst = v
		}
	}
	set("name", &next.Name)
	set("server", &next.Server)
	set("protocol", &next.Protocol)
	set("concurrency", &next.Concurrency)
	set("pipeline", &next.Pipeline)
	set("duration", &next.Duration)
	set("queries", &next.Queries)
	set("qtype", &next.Qtype)
	if v, ok := args["recurse"]; ok {
		next.Recurse = v != "false"
	}
	applyTiming(&next, args)
	if err := next.validate(); err != nil {
		jerr(w, http.StatusBadRequest, err.Error())
		return
	}
	if next.Freq == "once" {
		if t, ok := parseLocalTime(args["run_at"]); ok {
			next.NextRun = t
		}
	} else {
		next.NextRun = nextRunAfter(time.Now(), &next)
	}
	a.schedules.update(id, func(s *Schedule) {
		// Keep run-state fields that may have changed since we copied it.
		next.LastRun, next.LastOK, next.LastJobID, next.Paused = s.LastRun, s.LastOK, s.LastJobID, s.Paused
		*s = next
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Schedule updated"})
}

func (a *App) apiSchedulesDelete(w http.ResponseWriter, r *http.Request, _ *session) {
	args, ok := decodeArgs(w, r)
	if !ok {
		return
	}
	if !a.schedules.remove(strings.TrimSpace(args["id"])) {
		jerr(w, http.StatusNotFound, "Not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) apiSchedulesPause(w http.ResponseWriter, r *http.Request, _ *session) {
	args, ok := decodeArgs(w, r)
	if !ok {
		return
	}
	if !a.schedules.update(strings.TrimSpace(args["id"]), func(s *Schedule) { s.Paused = true }) {
		jerr(w, http.StatusNotFound, "Not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) apiSchedulesResume(w http.ResponseWriter, r *http.Request, _ *session) {
	args, ok := decodeArgs(w, r)
	if !ok {
		return
	}
	if !a.schedules.update(strings.TrimSpace(args["id"]), func(s *Schedule) {
		s.Paused = false
		if s.Freq != "once" {
			s.NextRun = nextRunAfter(time.Now(), s)
		}
	}) {
		jerr(w, http.StatusNotFound, "Not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) apiSchedulesRun(w http.ResponseWriter, r *http.Request, _ *session) {
	args, ok := decodeArgs(w, r)
	if !ok {
		return
	}
	s, found := a.schedules.get(strings.TrimSpace(args["id"]))
	if !found {
		jerr(w, http.StatusNotFound, "Not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job_id": a.runSchedule(s)})
}
