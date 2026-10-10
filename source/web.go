package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"embed"
)

//go:embed webui
var webFS embed.FS

// failDelay slows down each failed login (a var so tests can shorten it).
var failDelay = 400 * time.Millisecond

const (
	sessionCookie  = "__Host-ddgw_session" // the prefix makes a browser take it only if Secure, Path=/ and without Domain
	sessionMaxAge  = 12 * time.Hour
	groupRecheck   = time.Minute
	failWindow     = 10 * time.Minute // join-token failures (cluster)
	lockDuration   = 5 * time.Minute
	maxBody        = 1 << 20
	maxLoginBody   = 4 << 10
	maxUsernameLen = 128
	maxFailKeys    = 50000 // lockout records kept; a flood of made-up user names cannot grow memory past this
	maxLoginQueue  = 32    // login attempts being checked or waiting for PAM at once; more are turned away at once
)

// Authenticator checks passwords and group membership.  The production
// implementation is PAM + the system group database; tests substitute a fake.
type Authenticator interface {
	Authenticate(service, user, password string) error
	InGroup(user, group string) (bool, error)
}

type sysAuth struct{}

func (sysAuth) Authenticate(service, user, password string) error {
	return pamAuthenticate(service, user, password)
}

func (sysAuth) InGroup(username, group string) (bool, error) {
	g, err := user.LookupGroup(group)
	if err != nil {
		return false, err
	}
	u, err := user.Lookup(username)
	if err != nil {
		return false, err
	}
	gids, err := u.GroupIds()
	if err != nil {
		return false, err
	}
	for _, id := range gids {
		if id == g.Gid {
			return true, nil
		}
	}
	return false, nil
}

type session struct {
	user    string
	csrf    string
	created time.Time
	last    time.Time
	checked time.Time // last group-membership check
}

type failRec struct {
	count    int
	inflight int // login attempts that passed the lockout check and have not finished yet
	first    time.Time
	locked   time.Time
}

// WebServer is the HTTPS management GUI.  It runs inside the daemon and calls
// the supervisor directly, so everything the CLI can do is available to it.
type WebServer struct {
	mg       *Mgmt
	confPath string
	status   *StatusServer
	auth     Authenticator
	certs    *CertManager

	policy atomic.Pointer[WebConfig] // live: group, pam_service, session idle

	mu       sync.Mutex
	sessions map[string]*session // by sessKey(cookie)
	stateDir string
	noSave   atomic.Bool
	fails    map[string]*failRec
	loginQ   chan struct{} // bounds the logins waiting for PAM

	applyMu sync.Mutex
	running *WebConfig
	srv     *http.Server
	cancel  context.CancelFunc
}

func NewWebServer(mg *Mgmt, status *StatusServer, auth Authenticator) *WebServer {
	w := &WebServer{
		mg: mg, confPath: mg.confPath, certs: mg.certs, status: status, auth: auth,
		sessions: map[string]*session{}, fails: map[string]*failRec{},
		loginQ: make(chan struct{}, maxLoginQueue),
	}
	d := defaultWeb()
	w.policy.Store(&d)
	w.stateDir = mg.stateDir
	w.loadSessions()
	fn := w.endSessionsFor
	mg.endSessions.Store(&fn)
	return w
}

// endSessionsFor ends every session of user on this node.
func (w *WebServer) endSessionsFor(user string) {
	n := 0
	w.mu.Lock()
	for k, s := range w.sessions {
		if s.user == user {
			delete(w.sessions, k)
			n++
		}
	}
	w.mu.Unlock()
	if n > 0 {
		infof("web: %d session(s) of %q ended (the account changed)", n, user)
		w.saveSessions()
	}
}

// ── lifecycle ────────────────────────────────────────────────────────────────

// Apply makes the listener match cfg: starts, stops or restarts it.  Policy
// fields (group, PAM service, idle timeout) take effect immediately.
func (w *WebServer) Apply(cfg WebConfig) {
	w.applyMu.Lock()
	defer w.applyMu.Unlock()
	c := cfg
	w.policy.Store(&c)

	if w.running != nil && w.running.Listen == cfg.Listen {
		w.certs.Refresh() // certificate changes apply live, no restart
		return
	}
	if w.running != nil {
		w.stopLocked()
		w.saveSessions() // the listener changed: everyone signs in again, and the file says so
	}
	if !pamAvailable {
		errorf("web: GUI not started — this build has no PAM support (build natively with cgo and libpam0g-dev)")
		return
	}
	if err := w.certs.Ensure(); err != nil {
		errorf("web: GUI not started — TLS certificate: %v", err)
		return
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		errorf("web: GUI not started — cannot listen on %s: %v", cfg.Listen, err)
		return
	}
	srv := &http.Server{
		Handler: w.Handler(),
		TLSConfig: &tls.Config{
			GetCertificate: w.certs.GetCertificate,
			MinVersion:     tls.VersionTLS12,
			NextProtos:     []string{"h2", "http/1.1"},
		},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.srv, w.cancel = srv, cancel
	cc := cfg
	w.running = &cc
	go func() {
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorf("web: server stopped: %v", err)
		}
	}()
	go w.janitor(ctx)
	infof("web: GUI listening on https://%s (login: members of group %q via PAM service %q)",
		cfg.Listen, cfg.Group, cfg.PAMService)
	w.sanityWarnings(cfg)
}

func (w *WebServer) sanityWarnings(cfg WebConfig) {
	if _, err := user.LookupGroup(cfg.Group); err != nil {
		warnf("web: group %q does not exist — nobody can log in. Create it and add users: groupadd %s; usermod -aG %s <user>",
			cfg.Group, cfg.Group, cfg.Group)
	}
	if _, err := os.Stat("/etc/pam.d/" + cfg.PAMService); err != nil {
		warnf("web: /etc/pam.d/%s not found — PAM will fall back to its 'other' policy, which usually denies every login (see contrib/pam.d/)", cfg.PAMService)
	}
}

func (w *WebServer) stopLocked() {
	if w.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		w.srv.Shutdown(ctx)
		cancel()
		w.srv.Close()
	}
	if w.cancel != nil {
		w.cancel()
	}
	w.srv, w.cancel, w.running = nil, nil, nil
	w.mu.Lock()
	w.sessions = map[string]*session{} // a change of listener ends every session (the daemon stopping does not: see Stop)
	w.mu.Unlock()
}

func (w *WebServer) Stop() {
	w.applyMu.Lock()
	defer w.applyMu.Unlock()
	w.noSave.Store(true) // the daemon is stopping, not signing anyone out: the file keeps the sessions
	w.stopLocked()
}

func (w *WebServer) janitor(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			idle := w.policy.Load().sessionIdle()
			w.mu.Lock()
			for k, s := range w.sessions {
				if now.Sub(s.last) > idle || now.Sub(s.created) > sessionMaxAge {
					delete(w.sessions, k)
				}
			}
			pol := w.policy.Load()
			for k, f := range w.fails {
				if f.inflight == 0 && now.After(f.locked) && now.Sub(f.first) > pol.failWindow() {
					delete(w.fails, k)
				}
			}
			w.mu.Unlock()
			w.saveSessions() // the idle times move with every request, so it is written once a minute
		}
	}
}

// ── routing ──────────────────────────────────────────────────────────────────

func (w *WebServer) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "webui")
	static := http.FileServerFS(sub)
	mux.Handle("GET /{$}", static)
	mux.Handle("GET /app.js", static)
	mux.Handle("GET /help.js", static)
	mux.Handle("GET /style.css", static)

	mux.HandleFunc("POST /api/login", w.handleLogin)
	mux.HandleFunc("GET /api/login/state", w.handleLoginState)
	mux.HandleFunc("POST /api/logout", w.authed(w.handleLogout))
	mux.HandleFunc("GET /api/session", w.authed(w.handleSession))
	mux.HandleFunc("GET /api/gateways", w.authed(w.handleGateways))
	mux.HandleFunc("GET /api/neighbors", w.authed(w.handleNeighbors))
	mux.HandleFunc("GET /api/dns", w.authed(w.handleDNS))
	mux.HandleFunc("GET /api/canvas", w.authed(func(rw http.ResponseWriter, r *http.Request, s *session) {
		groups := w.status.canvasGroups()
		if r.URL.Query().Get("own") == "" { // ?own=1: this node's own view, as asked by another node or by the sidebar
			w.status.viaServing(r.Context(), groups)
		}
		writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "data": groups})
	}))
	mux.HandleFunc("POST /api/assert-agc", w.authed(w.handleAssertAGC))
	mux.HandleFunc("GET /api/config", w.authed(w.handleGetConfig))
	mux.HandleFunc("PUT /api/config", w.authed(w.handlePutConfig))
	// raw uploads (release archive) are relayed too, so no JSON content-type rule here
	mux.HandleFunc("/api/proxy", w.authedCT(w.handleProxy, false))
	w.mgmtRoutes(mux)
	mux.HandleFunc("/api/", func(rw http.ResponseWriter, r *http.Request) {
		jsonError(rw, http.StatusNotFound, "not found")
	})
	return secure(mux)
}

// secure sets defensive headers on every response.  Scripts and styles are
// same-origin files only (no inline code), which the CSP enforces.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		h := rw.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; "+
			"img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(rw, r)
	})
}

type ctxKey struct{}

// authed requires a valid session and, for state-changing methods, a matching
// CSRF token, a JSON content type and a same-origin Origin header.
func (w *WebServer) authed(h func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return w.authedCT(h, true)
}

// authedCT is authed with the content-type rule optional: the release-archive
// upload sends raw bytes (the CSRF token and same-origin checks still apply).
func (w *WebServer) authedCT(h func(http.ResponseWriter, *http.Request, *session), requireJSON bool) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		// a request relayed by another cluster node: it was authenticated by the
		// signed cluster channel and the user was authenticated where they logged
		// in.  The context value can only be set in-process.
		if ps, ok := r.Context().Value(proxyCtxKey{}).(*session); ok {
			h(rw, r, ps)
			return
		}
		s := w.lookup(r)
		if s == nil {
			jsonError(rw, http.StatusUnauthorized, "not logged in")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !sameOrigin(r) {
				jsonError(rw, http.StatusForbidden, "cross-origin request refused")
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.csrf)) != 1 {
				jsonError(rw, http.StatusForbidden, "missing or invalid CSRF token")
				return
			}
			if requireJSON && r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				jsonError(rw, http.StatusUnsupportedMediaType, "content type must be application/json")
				return
			}
		}
		h(rw, r, s)
	}
}

func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // non-browser client; CSRF token still required
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

// lookup returns the live session for the request cookie, refreshing its idle
// timer, or nil.  Group membership is re-checked periodically so removing a
// user from the group ends their session.
func (w *WebServer) lookup(r *http.Request) *session {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	pol := w.policy.Load()
	now := time.Now()
	w.mu.Lock()
	key := sessKey(c.Value)
	s := w.sessions[key]
	if s == nil {
		w.mu.Unlock()
		return nil
	}
	if now.Sub(s.last) > pol.sessionIdle() || now.Sub(s.created) > sessionMaxAge {
		delete(w.sessions, key)
		w.mu.Unlock()
		return nil
	}
	s.last = now
	recheck := now.Sub(s.checked) > groupRecheck
	uname := s.user
	w.mu.Unlock()

	if recheck {
		if ok, err := w.auth.InGroup(uname, pol.Group); err != nil || !ok {
			warnf("web: session for %q ended — no longer in group %q", uname, pol.Group)
			w.mu.Lock()
			delete(w.sessions, key)
			w.mu.Unlock()
			return nil
		}
		w.mu.Lock()
		s.checked = now
		w.mu.Unlock()
	}
	return s
}

// ── helpers ──────────────────────────────────────────────────────────────────

func writeJSON(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	json.NewEncoder(rw).Encode(v)
}

func jsonError(rw http.ResponseWriter, code int, msg string) {
	writeJSON(rw, code, map[string]any{"ok": false, "error": msg})
}

func readJSON(rw http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, limit))
	if err != nil {
		jsonError(rw, http.StatusRequestEntityTooLarge, "request body too large")
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		jsonError(rw, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func randToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// ── login / logout ───────────────────────────────────────────────────────────

func validUsername(u string) bool {
	if u == "" || len(u) > maxUsernameLen {
		return false
	}
	for _, c := range u {
		if unicode.IsControl(c) || c == ':' || c == '/' {
			return false
		}
	}
	return true
}

// loginKeys are what a login attempt is counted against: the address, and the address together with the user
// name.  There is deliberately no key for the user name alone: that would let anyone who can reach the page lock
// a known administrator out by failing a few logins for that name.
func loginKeys(ip, user string) []string {
	return []string{"ip:" + ip, "ipuser:" + ip + "|" + strings.ToLower(user)}
}

// failRecLocked returns the record of key, started afresh when its window has run out; nil when it does not
// exist and none may be created (w.mu held).  Records of a user name seen from an address are not created once
// maxFailKeys are held, after the expired ones are swept; the per-address record always is (an address cannot be
// invented the way a user name can).
func (w *WebServer) failRecLocked(k string, now time.Time, pol *WebConfig, create bool) *failRec {
	f := w.fails[k]
	if f != nil {
		if f.inflight == 0 && now.After(f.locked) && now.Sub(f.first) > pol.failWindow() {
			f.count, f.first = 0, now
		}
		return f
	}
	if !create {
		return nil
	}
	if len(w.fails) >= maxFailKeys {
		for kk, ff := range w.fails {
			if ff.inflight == 0 && now.After(ff.locked) && now.Sub(ff.first) > pol.failWindow() {
				delete(w.fails, kk)
			}
		}
		if len(w.fails) >= maxFailKeys && strings.HasPrefix(k, "ipuser:") {
			return nil
		}
	}
	f = &failRec{first: now}
	w.fails[k] = f
	return f
}

// throttled reports whether any of keys is currently locked out.
func (w *WebServer) throttled(keys ...string) (time.Duration, bool) {
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, k := range keys {
		if f := w.fails[k]; f != nil && now.Before(f.locked) {
			return f.locked.Sub(now), true
		}
	}
	return 0, false
}

// reserve claims one login attempt on every key before PAM is asked.  The lockout used to be checked first and
// the failure recorded only after PAM answered, so any number of parallel requests all passed the check and got
// a guess each.  Now the failures counted so far plus the attempts under way may not reach the limit: a request
// that would exceed it is refused (ok false, with how long to wait) without PAM being consulted.  A granted
// reservation is given back with release, which is also where a failure is counted.
func (w *WebServer) reserve(keys ...string) (wait time.Duration, ok bool) {
	pol := w.policy.Load()
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	var got []*failRec
	for _, k := range keys {
		f := w.failRecLocked(k, now, pol, true)
		if f == nil {
			continue
		}
		if now.Before(f.locked) {
			wait = f.locked.Sub(now)
		} else if f.count+f.inflight >= pol.MaxFailedLogins {
			wait = time.Second // attempts already under way may use up the allowance: try again shortly
		}
		if wait > 0 {
			for _, g := range got {
				g.inflight--
			}
			return wait, false
		}
		f.inflight++
		got = append(got, f)
	}
	return 0, true
}

// release ends a reservation made by reserve; failed counts the attempt as a failed login on every key and
// reports, if one of them is now locked out, for how long.  The number of attempts still allowed is deliberately
// not reported to anyone: it would tell a guesser how many tries it has.
func (w *WebServer) release(failed bool, keys ...string) (lockedFor time.Duration) {
	pol := w.policy.Load()
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, k := range keys {
		f := w.fails[k]
		if f == nil {
			continue
		}
		if f.inflight > 0 {
			f.inflight--
		}
		if !failed {
			continue
		}
		if now.Sub(f.first) > pol.failWindow() && !now.Before(f.locked) {
			f.count, f.first = 0, now
		}
		f.count++
		if f.count >= pol.MaxFailedLogins {
			f.locked = now.Add(pol.lockout())
			f.count = 0
			f.first = now
		}
		if now.Before(f.locked) {
			if d := f.locked.Sub(now); d > lockedFor {
				lockedFor = d
			}
		}
	}
	return lockedFor
}

// loginState tells the login page whether the caller's address is locked out.
func (w *WebServer) handleLoginState(rw http.ResponseWriter, r *http.Request) {
	wait, locked := w.throttled("ip:" + clientIP(r))
	// Only whether this address is locked and for how long: the thresholds of the policy are not
	// told to someone who is not logged in.
	out := map[string]any{"ok": true, "locked": locked}
	if locked {
		out["retry_after"] = int(wait.Seconds()) + 1
	}
	writeJSON(rw, http.StatusOK, out)
}

func lockoutError(rw http.ResponseWriter, wait time.Duration) {
	secs := int(wait.Seconds()) + 1
	rw.Header().Set("Retry-After", itoa(secs))
	writeJSON(rw, http.StatusTooManyRequests, map[string]any{
		"ok": false, "error": "Too many failed attempts.", "retry_after": secs})
}

func (w *WebServer) clearFailures(keys ...string) {
	w.mu.Lock()
	for _, k := range keys {
		if f := w.fails[k]; f != nil && f.inflight == 0 {
			delete(w.fails, k)
		} else if f != nil {
			f.count = 0
		}
	}
	w.mu.Unlock()
}

func (w *WebServer) handleLogin(rw http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		jsonError(rw, http.StatusForbidden, "cross-origin request refused")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(rw, r, maxLoginBody, &req) {
		return
	}
	ip := clientIP(r)
	keys := loginKeys(ip, req.Username)
	select {
	case w.loginQ <- struct{}{}:
		defer func() { <-w.loginQ }()
	default:
		rw.Header().Set("Retry-After", "2")
		jsonError(rw, http.StatusServiceUnavailable, "Too many logins at once; try again in a moment.")
		return
	}
	if wait, ok := w.reserve(keys...); !ok {
		lockoutError(rw, wait)
		return
	}
	pol := w.policy.Load()

	var reason string
	switch {
	case !validUsername(req.Username) || req.Password == "":
		reason = "malformed credentials"
	default:
		if err := w.auth.Authenticate(pol.PAMService, req.Username, req.Password); err != nil {
			reason = "authentication failed: " + err.Error()
		} else if ok, err := w.auth.InGroup(req.Username, pol.Group); err != nil {
			reason = "group lookup failed: " + err.Error()
		} else if !ok {
			reason = "not a member of group " + pol.Group
		}
	}
	if reason != "" {
		lockedFor := w.release(true, keys...)
		warnf("web: login failed for %q from %s: %s", req.Username, ip, reason)
		time.Sleep(failDelay)
		if lockedFor > 0 {
			warnf("web: locking out %q from %s for %s after repeated failed logins", req.Username, ip, lockedFor.Round(time.Second))
			lockoutError(rw, lockedFor)
			return
		}
		// One message for every cause: don't reveal which part was wrong, which group is needed or
		// how many attempts are left.
		writeJSON(rw, http.StatusUnauthorized, map[string]any{"ok": false,
			"error": "Login failed: wrong username or password, or not authorized."})
		return
	}
	w.release(false, keys...)
	w.clearFailures(keys...)

	now := time.Now()
	s := &session{user: req.Username, csrf: randToken(), created: now, last: now, checked: now}
	tok := randToken()
	w.mu.Lock()
	w.sessions[sessKey(tok)] = s
	w.mu.Unlock()
	w.saveSessions()
	http.SetCookie(rw, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionMaxAge.Seconds()),
	})
	infof("web: %q logged in from %s", req.Username, ip)
	writeJSON(rw, http.StatusOK, w.sessionInfo(s))
}

func (w *WebServer) handleLogout(rw http.ResponseWriter, r *http.Request, s *session) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		w.mu.Lock()
		delete(w.sessions, sessKey(c.Value))
		w.mu.Unlock()
		w.saveSessions()
	}
	http.SetCookie(rw, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	infof("web: %q logged out", s.user)
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *WebServer) sessionInfo(s *session) map[string]any {
	host, _ := os.Hostname()
	return map[string]any{
		"ok": true, "user": s.user, "csrf": s.csrf, "version": version(),
		"hostname": host, "config_path": w.confPath,
		"idle_minutes": w.policy.Load().SessionIdleMinutes,
	}
}

func (w *WebServer) handleSession(rw http.ResponseWriter, r *http.Request, s *session) {
	writeJSON(rw, http.StatusOK, w.sessionInfo(s))
}

// ── read-only views (CLI: --show-gateways / --show-neighbors / --show-dns) ──

func (w *WebServer) handleGateways(rw http.ResponseWriter, r *http.Request, s *session) {
	gs := buildGateways(w.status.snapshot())
	nameMembers(gs, w.status.ipNames())
	nameGateways(gs, w.status.sup.config())
	if r.URL.Query().Get("own") == "" { // ?own=1: only what this node runs, as asked by another node
		gs = w.status.gatewaysVia(r.Context(), gs)
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "data": gs})
}

func (w *WebServer) handleNeighbors(rw http.ResponseWriter, r *http.Request, s *session) {
	rows := w.status.snapshot()
	if r.URL.Query().Get("all") == "1" { // the Nodes page lists this node too
		sortRows(rows)
		writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "data": rows})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "data": peersOnly(rows)})
}

func (w *WebServer) handleDNS(rw http.ResponseWriter, r *http.Request, s *session) {
	resp := w.status.dnsStatus()
	if r.URL.Query().Get("own") == "" { // ?own=1: only what this node runs, as asked by another node
		resp = w.status.dnsVia(r.Context(), resp)
	}
	writeJSON(rw, http.StatusOK, resp)
}

// ── actions (CLI: --assert-agc) ──────────────────────────────────────────────

func (w *WebServer) handleAssertAGC(rw http.ResponseWriter, r *http.Request, s *session) {
	warnf("web: %q asserted AGC on this node (from %s)", s.user, clientIP(r))
	writeJSON(rw, http.StatusOK, w.status.assertAGC())
}

// ── configuration (CLI: --show-config / --configure) ────────────────────────

func (w *WebServer) handleGetConfig(rw http.ResponseWriter, r *http.Request, s *session) {
	_, statErr := os.Stat(w.confPath)
	dc, err := loadConfig(w.confPath)
	if err != nil {
		jsonError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"ok": true, "path": w.confPath, "exists": statErr == nil, "config": dc,
	})
}

func (w *WebServer) handlePutConfig(rw http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Config json.RawMessage `json:"config"`
		Note   string          `json:"note"`
	}
	if !readJSON(rw, r, maxBody, &req) {
		return
	}
	if len(req.Config) == 0 {
		jsonError(rw, http.StatusBadRequest, "missing \"config\"")
		return
	}
	dc := newDaemonConfig()
	if err := json.Unmarshal(req.Config, dc); err != nil {
		jsonError(rw, http.StatusBadRequest, "invalid config: "+err.Error())
		return
	}
	if err := dc.Validate(); err != nil {
		jsonError(rw, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if r.URL.Query().Get("dry_run") == "1" {
		writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "dry_run": true, "config": dc})
		return
	}

	infof("web: %q saving %s (from %s)", s.user, w.confPath, clientIP(r))
	if err := w.mg.PutConfig(dc, s.user, req.Note); err != nil {
		jsonError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "config": dc})
}
