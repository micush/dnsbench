package main

import (
	"sync"
	"time"
)

const sessionTTL = time.Hour

// session is a logged-in browser or API client.
type session struct {
	user    string
	expires time.Time
}

// sessionStore keeps sessions in memory; they do not survive a restart.
type sessionStore struct {
	mu sync.Mutex
	m  map[string]*session
}

func newSessionStore() *sessionStore { return &sessionStore{m: map[string]*session{}} }

func (s *sessionStore) create(user string) string {
	tok := newToken(32)
	s.mu.Lock()
	s.m[tok] = &session{user: user, expires: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	return tok
}

func (s *sessionStore) get(tok string) *session {
	if tok == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.m[tok]
	if sess == nil || !sess.expires.After(time.Now()) {
		return nil
	}
	cp := *sess
	return &cp
}

func (s *sessionStore) delete(tok string) {
	s.mu.Lock()
	delete(s.m, tok)
	s.mu.Unlock()
}

// extendFor pushes the expiry of every session of user out by sessionTTL.
func (s *sessionStore) extendFor(user string) {
	exp := time.Now().Add(sessionTTL)
	s.mu.Lock()
	for _, sess := range s.m {
		if sess.user == user {
			sess.expires = exp
		}
	}
	s.mu.Unlock()
}

// purge drops expired sessions.
func (s *sessionStore) purge() {
	now := time.Now()
	s.mu.Lock()
	for k, sess := range s.m {
		if !sess.expires.After(now) {
			delete(s.m, k)
		}
	}
	s.mu.Unlock()
}

// loginThrottle slows password guessing: after maxFails failures from one
// address inside the window, further attempts are refused until it passes.
type loginThrottle struct {
	mu       sync.Mutex
	fails    map[string][]time.Time
	maxFails int
	window   time.Duration
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{fails: map[string][]time.Time{}, maxFails: 8, window: 5 * time.Minute}
}

func (t *loginThrottle) prune(ip string, now time.Time) []time.Time {
	list := t.fails[ip]
	keep := list[:0]
	for _, ts := range list {
		if now.Sub(ts) < t.window {
			keep = append(keep, ts)
		}
	}
	if len(keep) == 0 {
		delete(t.fails, ip)
		return nil
	}
	t.fails[ip] = keep
	return keep
}

func (t *loginThrottle) blocked(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.prune(ip, time.Now())) >= t.maxFails
}

func (t *loginThrottle) fail(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.prune(ip, now)
	t.fails[ip] = append(t.fails[ip], now)
}

func (t *loginThrottle) success(ip string) {
	t.mu.Lock()
	delete(t.fails, ip)
	t.mu.Unlock()
}
