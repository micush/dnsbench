package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Sessions survive a restart of the daemon (an upgrade, a reboot), so nobody has to sign in again.
// They are kept in <state-dir>/sessions.json (0600).  Only a SHA-256 of each cookie value is stored, so
// the file cannot be used to take a session over.  The idle timeout and the 12 hour limit still apply
// to a restored session, and the group is re-checked on its first use.

const sessionsFile = "sessions.json"

// sessKey is what a cookie value is filed under, in memory and on disk.
func sessKey(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

type sessionRec struct {
	Key     string `json:"k"`
	User    string `json:"u"`
	CSRF    string `json:"c"`
	Created int64  `json:"cr"`
	Last    int64  `json:"l"`
}

// loadSessions restores the sessions saved by the last run; whatever has run out is dropped.
func (w *WebServer) loadSessions() {
	if w.stateDir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(w.stateDir, sessionsFile))
	if err != nil {
		return
	}
	var recs []sessionRec
	if json.Unmarshal(b, &recs) != nil {
		return
	}
	now := time.Now()
	n := 0
	w.mu.Lock()
	for _, r := range recs {
		created, last := time.Unix(r.Created, 0), time.Unix(r.Last, 0)
		if r.Key == "" || r.User == "" || r.CSRF == "" || now.Sub(created) > sessionMaxAge || now.Sub(last) > w.policy.Load().sessionIdle() {
			continue
		}
		// checked is left at zero: the group is looked at again with the first request
		w.sessions[r.Key] = &session{user: r.User, csrf: r.CSRF, created: created, last: last}
		n++
	}
	w.mu.Unlock()
	if n > 0 {
		infof("web: %d session(s) restored from the last run", n)
	}
}

// saveSessions writes the live sessions.  A failure is only logged: the sessions then end with the process, as before.
func (w *WebServer) saveSessions() {
	if w.stateDir == "" || w.noSave.Load() {
		return
	}
	w.mu.Lock()
	recs := make([]sessionRec, 0, len(w.sessions))
	for k, s := range w.sessions {
		recs = append(recs, sessionRec{Key: k, User: s.user, CSRF: s.csrf, Created: s.created.Unix(), Last: s.last.Unix()})
	}
	w.mu.Unlock()
	b, _ := json.Marshal(recs)
	final := filepath.Join(w.stateDir, sessionsFile)
	if len(recs) == 0 {
		os.Remove(final)
		return
	}
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		if err = os.Rename(tmp, final); err != nil {
			os.Remove(tmp)
			warnf("web: cannot save the sessions: %v", err)
		}
	} else {
		warnf("web: cannot save the sessions: %v", err)
	}
}
