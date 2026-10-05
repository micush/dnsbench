package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// The update API. Every route needs a signed-in session like the rest of /api, and the upload is
// a state-changing POST, so the same-origin check in securityHeaders covers it too.

var errUpdatesOff = fmt.Errorf("updates from the web UI are switched off on this server (ALLOW_UPDATES=false)")

// notSafeError is an update that would interrupt something; the operator may still choose it.
type notSafeError struct{ why string }

func (e notSafeError) Error() string { return "not safe to update this server now: " + e.why }

// updateSafe reports whether restarting now would cut a benchmark short.
func (a *App) updateSafe() (bool, string) {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	for _, j := range a.jobs {
		if j.running() {
			return false, "a benchmark is running and would be stopped"
		}
	}
	return true, ""
}

// UpdateUpload stages a release archive.
func (a *App) UpdateUpload(body []byte, by string) (string, error) {
	if !a.updater.enabled {
		return "", errUpdatesOff
	}
	if len(body) == 0 {
		return "", fmt.Errorf("empty upload")
	}
	if len(body) > maxUploadBytes {
		return "", fmt.Errorf("upload too large")
	}
	ver, err := a.updater.Stage(body, version())
	if err != nil {
		return "", fmt.Errorf("rejected: %w", err)
	}
	a.updater.Record(UpdateEvent{Kind: "uploaded", To: ver, By: by,
		Detail: fmt.Sprintf("source v%s staged (running v%s)", ver, version())})
	log.Printf("update: source v%s staged by %q", ver, by)
	return ver, nil
}

// UpdateApply starts building the staged source and restarts into it. The build takes minutes,
// so it runs in the background; progress shows in the status (busy, phase, history). The
// preconditions that can be checked up front are, so the person gets an answer now.
func (a *App) UpdateApply(by string, force bool) (string, error) {
	u := a.updater
	if !u.enabled {
		return "", errUpdatesOff
	}
	target := u.SourceVersion()
	switch {
	case target == "":
		return "", fmt.Errorf("no source tree staged: upload a release archive first")
	case !versionGreater(target, version()):
		return "", fmt.Errorf("the staged source (v%s) is not newer than the running version (v%s)", target, version())
	case u.Busy():
		return "", fmt.Errorf("an update is already in progress on this server")
	}
	if ok, why := a.updateSafe(); !ok && !force {
		return "", notSafeError{why}
	}
	go func() {
		if _, err := u.Apply(context.Background(), by); err != nil {
			log.Printf("update: %v", err)
			return
		}
		a.scheduleRestart(force)
	}()
	return target, nil
}

// restartRecheck is how often an installed update re-asks whether it may restart, and
// restartGrace is how long the HTTP reply gets to go out before it does.
var (
	restartRecheck = 2 * time.Second
	restartGrace   = 1500 * time.Millisecond
)

// scheduleRestart restarts into the installed version, but only once no benchmark is running:
// the check made before the update began is not enough, because the build takes minutes and a
// scheduled run may have started meanwhile. force skips the wait (the operator confirmed it).
func (a *App) scheduleRestart(force bool) {
	u := a.updater
	if u.restartFn == nil {
		log.Printf("update: installed, but this process cannot restart itself; restart dnsbench to run the new version")
		return
	}
	go func() {
		time.Sleep(restartGrace)
		waitUntilSafe(a.updateSafe, force, restartRecheck, func(why string) {
			if why != "" {
				why = "installed; waiting to restart: " + why
			}
			u.SetWaiting(why)
		})
		u.restartFn()
	}()
}

// waitUntilSafe blocks until safe reports true (or force), publishing the reason it is holding
// back through setWaiting and clearing it afterwards.
func waitUntilSafe(safe func() (bool, string), force bool, every time.Duration, setWaiting func(string)) {
	if force {
		return
	}
	defer setWaiting("")
	for {
		ok, why := safe()
		if ok {
			return
		}
		setWaiting(why)
		time.Sleep(every)
	}
}

// updateView is what the page shows.
type updateView struct {
	Enabled       bool          `json:"enabled"`
	Running       string        `json:"running"`
	SourceVersion string        `json:"source_version"`
	Newer         bool          `json:"newer"` // the staged source is newer than the running version
	Busy          bool          `json:"busy"`
	Phase         string        `json:"phase,omitempty"`
	Waiting       string        `json:"waiting,omitempty"`
	Notice        string        `json:"notice,omitempty"` // a rollback that happened at the last start
	Toolchain     string        `json:"toolchain,omitempty"`
	Problems      []string      `json:"problems"` // why an update cannot be started from here
	LastFailed    *UpdateEvent  `json:"last_failed,omitempty"`
	History       []UpdateEvent `json:"history"` // newest first, the most recent statusHistory
	HistoryTotal  int           `json:"history_total"`
}

const statusHistory = 50

func (a *App) UpdateStatus() updateView {
	u := a.updater
	src := u.SourceVersion()
	v := updateView{
		Enabled: u.enabled, Running: version(), SourceVersion: src, Newer: versionGreater(src, version()),
		Busy: u.Busy(), Phase: u.Phase(), Waiting: u.Waiting(), Notice: u.RolledBackNotice(),
		History: u.History(statusHistory), HistoryTotal: u.historyLen(), Problems: []string{},
	}
	if !u.enabled {
		v.Problems = append(v.Problems, errUpdatesOff.Error())
		return v
	}
	if g, err := u.toolchain(); err != nil {
		v.Problems = append(v.Problems, err.Error())
	} else {
		v.Toolchain = g
	}
	if !haveCCompiler() {
		v.Problems = append(v.Problems, "No C compiler on this server (the PAM binding needs gcc and the PAM development headers; re-run install.sh).")
	}
	if exe, err := u.exePath(); err != nil {
		v.Problems = append(v.Problems, err.Error())
	} else if err := canReplace(strings.TrimSuffix(exe, " (deleted)")); err != nil {
		v.Problems = append(v.Problems, err.Error())
	}
	if src != "" {
		v.LastFailed = u.lastFailure()
		if v.LastFailed != nil && v.LastFailed.To != src {
			v.LastFailed = nil
		}
	}
	return v
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (a *App) apiUpdateStatus(w http.ResponseWriter, r *http.Request, _ *session) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": a.UpdateStatus()})
}

func (a *App) apiUpdateUpload(w http.ResponseWriter, r *http.Request, s *session) {
	if !a.updater.enabled {
		jerr(w, http.StatusForbidden, errUpdatesOff.Error())
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxUploadBytes))
	if err != nil {
		jerr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("upload too large (limit %d MB)", maxUploadBytes>>20))
		return
	}
	log.Printf("update: %q uploaded a release archive (%d bytes, from %s)", s.user, len(body), clientIP(r))
	ver, err := a.UpdateUpload(body, s.user)
	if err != nil {
		jerr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"version": ver}})
}

func (a *App) apiUpdateApply(w http.ResponseWriter, r *http.Request, s *session) {
	if !a.updater.enabled {
		jerr(w, http.StatusForbidden, errUpdatesOff.Error())
		return
	}
	args, ok := decodeArgs(w, r)
	if !ok {
		return
	}
	force := args["force"] == "true"
	log.Printf("update: %q asked to update this server (from %s, force=%v)", s.user, clientIP(r), force)
	target, err := a.UpdateApply(s.user, force)
	if err != nil {
		if ns, ok := err.(notSafeError); ok {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": ns.Error(), "needs_confirm": true})
			return
		}
		jerr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"target": target}})
}
