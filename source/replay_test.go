package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"testing"
	"time"
)

// signedAt is a hello from ip with the timestamp stamp (Unix milliseconds), signed with the engine's key.
func signedAt(t *testing.T, e *Engine, ip string, stamp int64) []byte {
	t.Helper()
	pkt := helloFrom(t, e, ip, 100, 1)
	binary.BigEndian.PutUint64(pkt[47:], uint64(stamp))
	mac := hmac.New(sha256.New, e.cfg.keyBytes())
	mac.Write(pkt[:headerSize])
	return mac.Sum(append([]byte(nil), pkt[:headerSize]...))
}

func TestReplayedPacketIsRefused(t *testing.T) {
	e := newTestEngine()
	now := wireNow().UnixMilli()
	p1 := signedAt(t, e, "10.0.0.9", now)
	e.mu.Lock()
	ok1 := e.replayLocked(mustParse(t, e, p1))
	ok2 := e.replayLocked(mustParse(t, e, p1))                                // the same packet again
	ok3 := e.replayLocked(mustParse(t, e, signedAt(t, e, "10.0.0.9", now-1))) // an older one
	ok4 := e.replayLocked(mustParse(t, e, signedAt(t, e, "10.0.0.9", now+1))) // a newer one
	e.mu.Unlock()
	if !ok1 || ok2 || ok3 || !ok4 {
		t.Fatalf("first=%v replay=%v older=%v newer=%v", ok1, ok2, ok3, ok4)
	}
}

func mustParse(t *testing.T, e *Engine, b []byte) *Packet {
	t.Helper()
	p, err := parsePacket(b, e.cfg.keyBytes())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReplayPerSender(t *testing.T) {
	e := newTestEngine()
	now := wireNow().UnixMilli()
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.replayLocked(mustParse(t, e, signedAt(t, e, "10.0.0.9", now))) {
		t.Fatal("first sender refused")
	}
	// another sender's clock may be behind the first one's
	if !e.replayLocked(mustParse(t, e, signedAt(t, e, "10.0.0.8", now-5000))) {
		t.Fatal("a second sender with an older stamp was refused")
	}
}

func TestStaleAndFutureStampsAreRefused(t *testing.T) {
	e := newTestEngine()
	now := wireNow()
	for name, st := range map[string]int64{
		"a recording from a minute ago": now.Add(-time.Minute).UnixMilli(),
		"a clock a minute ahead":        now.Add(time.Minute).UnixMilli(),
		"the zero stamp":                0,
	} {
		e.mu.Lock()
		ok := e.replayLocked(mustParse(t, e, signedAt(t, e, "10.0.0.9", st)))
		e.mu.Unlock()
		if ok {
			t.Errorf("%s was accepted", name)
		}
	}
	e.mu.Lock()
	ok := e.replayLocked(mustParse(t, e, signedAt(t, e, "10.0.0.9", now.Add(-20*time.Second).UnixMilli())))
	e.mu.Unlock()
	if !ok {
		t.Error("a packet 20 s old was refused (the limit is 30 s)")
	}
}

// A refused packet changes nothing: no peer is learnt from a replay.
func TestReplayLearnsNoPeer(t *testing.T) {
	e := newTestEngine()
	old := signedAt(t, e, "10.0.0.9", wireNow().Add(-time.Hour).UnixMilli())
	e.mu.Lock()
	e.onPacketLocked(old, nil)
	n := len(e.peers)
	e.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d peers learnt from a stale packet", n)
	}
	fresh := signedAt(t, e, "10.0.0.9", wireNow().UnixMilli())
	e.mu.Lock()
	e.onPacketLocked(fresh, nil)
	n = len(e.peers)
	e.onPacketLocked(fresh, nil)
	e.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d peers after a fresh packet", n)
	}
}

// The table of senders stays bounded.
func TestReplayTableBounded(t *testing.T) {
	e := newTestEngine()
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := 0; i < replayTableMax*2; i++ {
		ip := "10." + itoa(i>>16&255) + "." + itoa(i>>8&255) + "." + itoa(i&255)
		e.replayLocked(&Packet{SenderIP: ip, Stamp: wireNow().UnixMilli()})
	}
	if len(e.stamps) > replayTableMax {
		t.Fatalf("%d senders kept", len(e.stamps))
	}
}
