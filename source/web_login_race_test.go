package main

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gateAuth makes every Authenticate wait until the gate is opened, so a test can hold many logins "inside PAM".
type gateAuth struct {
	fakeAuth
	gate    chan struct{}
	entered atomic.Int32
}

func (g *gateAuth) Authenticate(service, user, pass string) error {
	g.entered.Add(1)
	<-g.gate
	return g.fakeAuth.Authenticate(service, user, pass)
}

// Parallel guesses used to all pass the lockout check before any failure was counted.
func TestLoginParallelGuessesAreBounded(t *testing.T) {
	e := newWebEnv(t)
	g := &gateAuth{gate: make(chan struct{})}
	e.ws.auth = g
	max := e.ws.policy.Load().MaxFailedLogins

	const n = 20
	var wg sync.WaitGroup
	codes := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- e.do("POST", "/api/login", map[string]string{"username": "alice", "password": "guess"}).code
		}()
	}
	// wait until the requests that may proceed are inside PAM, and the rest have been refused
	deadline := time.Now().Add(5 * time.Second)
	for len(codes) < n-max && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := int(g.entered.Load()); got > max {
		t.Fatalf("%d parallel guesses reached PAM with a limit of %d", got, max)
	}
	close(g.gate)
	wg.Wait()
	close(codes)
	refused := 0
	for c := range codes {
		if c == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused < n-max {
		t.Fatalf("only %d of %d parallel guesses were refused", refused, n)
	}
}

// Failing logins for a name from one address must not lock that name out for everyone else.
func TestLoginLockoutIsNotPerUserName(t *testing.T) {
	e := newWebEnv(t)
	max := e.ws.policy.Load().MaxFailedLogins
	attacker := loginKeys("203.0.113.9", "alice")
	for i := 0; i < max; i++ {
		if _, ok := e.ws.reserve(attacker...); !ok {
			t.Fatalf("attempt %d refused too early", i)
		}
		e.ws.release(true, attacker...)
	}
	if _, locked := e.ws.throttled(attacker...); !locked {
		t.Fatal("the attacker's address and name pair is not locked")
	}
	admin := loginKeys("198.51.100.7", "alice")
	if _, locked := e.ws.throttled(admin...); locked {
		t.Fatal("a failure storm from one address locked the user out for another address")
	}
	if _, ok := e.ws.reserve(admin...); !ok {
		t.Fatal("the real user was refused")
	}
	e.ws.release(false, admin...)
}

func TestLoginFailureTableIsCapped(t *testing.T) {
	e := newWebEnv(t)
	now := time.Now()
	e.ws.mu.Lock()
	for i := 0; i < maxFailKeys; i++ {
		e.ws.fails["ipuser:x|"+itoa(i)] = &failRec{first: now, count: 1}
	}
	e.ws.mu.Unlock()
	keys := loginKeys("192.0.2.1", "newname")
	if _, ok := e.ws.reserve(keys...); !ok {
		t.Fatal("a login was refused because the table is full")
	}
	e.ws.release(true, keys...)
	e.ws.mu.Lock()
	n := len(e.ws.fails)
	e.ws.mu.Unlock()
	if n > maxFailKeys+1 { // the per-address record is always kept
		t.Fatalf("failure table grew to %d", n)
	}
}

func TestLoginQueueFullIsTurnedAway(t *testing.T) {
	e := newWebEnv(t)
	for i := 0; i < maxLoginQueue; i++ {
		e.ws.loginQ <- struct{}{}
	}
	r := e.do("POST", "/api/login", map[string]string{"username": "alice", "password": "pw"})
	if r.code != http.StatusServiceUnavailable || r.hdr.Get("Retry-After") == "" {
		t.Fatalf("expected 503 with the queue full, got %d", r.code)
	}
	if e.auth.authCalls.Load() != 0 {
		t.Fatal("PAM was consulted with the queue full")
	}
}
