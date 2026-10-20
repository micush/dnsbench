package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// schedActionDelay lets the history entry be written before the host goes down (shortened in tests).
var schedActionDelay = 1500 * time.Millisecond

// Two built-in scheduler actions that act on the host or the daemon's own version.

// updateOnline looks for the newest release online (the same lookup and repository as the Upgrade page's "Fetch from
// online"), and when it is newer than the version running here it stages it and queues this node for the update.  The
// queue is the normal one, so the node waits until its gateways stay served, behind the canary, and is rolled back
// if the new version does not stay up.  Nothing is downloaded when this node already runs the newest.
func (s *Scheduler) updateOnline(actor string) (string, error) {
	m := s.m
	ctx, cancel := context.WithTimeout(context.Background(), 2*fetchTimeout)
	defer cancel()
	repo := m.upd.Repo()
	tag, err := latestTag(ctx, repo)
	if err != nil {
		return "", err
	}
	run := version()
	if n := strings.TrimPrefix(tag, "v"); !versionGreater(n, run) {
		if _, perr := parseVer(n); perr == nil {
			return fmt.Sprintf("up to date: running v%s, newest release %s", run, tag), nil
		}
	}
	staged := m.upd.SourceVersion()
	if !versionGreater(staged, run) || versionGreater(strings.TrimPrefix(tag, "v"), staged) {
		body, got, err := fetchRelease(ctx, repo)
		if err != nil {
			return "", err
		}
		if staged, err = m.UpdateUpload(body, actor); err != nil {
			return "", fmt.Errorf("%s: %w", got, err)
		}
	}
	if !versionGreater(staged, run) {
		return fmt.Sprintf("up to date: running v%s, newest release %s", run, releaseName(tag, staged)), nil
	}
	if err := m.UpdatePush([]string{m.cl.selfAddrForEvents()}, actor); err != nil {
		return "", fmt.Errorf("v%s is staged but could not be queued: %w", staged, err)
	}
	return fmt.Sprintf("%s staged; this node is queued to update (running v%s)", releaseName(tag, staged), run), nil
}

// releaseName is the release as one name: its tag (v80), and the version inside it only when that is a different
// number, so the line does not say v80 twice.
func releaseName(tag, staged string) string {
	if strings.TrimPrefix(tag, "v") == staged {
		return "v" + staged
	}
	return tag + " (v" + staged + ")"
}

// rebootHost restarts the host, but only once that is safe (no gateway would lose its last serving member), like an update.
func (s *Scheduler) rebootHost(actor string) (string, error) {
	m := s.m
	go func() {
		time.Sleep(schedActionDelay) // let the history entry be written first
		waitUntilSafe(m.updateSafe, false, restartRecheck, func(string) {})
		if _, err := m.Power(PowerReq{Action: "restart", Force: true}, actor); err != nil {
			errorf("scheduler: reboot: %v", err)
		}
	}()
	return "rebooting the host once it is safe to go down", nil
}
