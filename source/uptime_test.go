package main

import (
	"strings"
	"testing"
	"time"
)

func TestUpTrackerTransitions(t *testing.T) {
	u := newUpTracker()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	u.pass++
	if i := u.observe("a", "idle", t0); i != nil {
		t.Fatalf("idle has no uptime: %+v", i)
	}
	if i := u.observe("a", "ok", t0); i == nil || !i.Up || i.Failures != 0 || i.Since != t0.Unix() {
		t.Fatalf("first sight up: %+v", i)
	}
	// warn still counts as online and does not restart the clock
	if i := u.observe("a", "warn", t0.Add(time.Hour)); !i.Up || i.Since != t0.Unix() {
		t.Fatalf("warn: %+v", i)
	}
	// a failure restarts it and remembers that one happened
	if i := u.observe("a", "bad", t0.Add(2*time.Hour)); i.Up || i.Failures != 1 || i.Since != t0.Add(2*time.Hour).Unix() {
		t.Fatalf("down: %+v", i)
	}
	if i := u.observe("a", "bad", t0.Add(3*time.Hour)); i.Since != t0.Add(2*time.Hour).Unix() {
		t.Fatalf("down clock moved: %+v", i)
	}
	i := u.observe("a", "ok", t0.Add(4*time.Hour))
	if !i.Up || i.Failures != 1 || i.Since != t0.Add(4*time.Hour).Unix() {
		t.Fatalf("recovered: %+v", i)
	}
	if got := i.Text(t0.Add(4*time.Hour + 90*time.Minute)); got != "online for 1h 30m - 1 failure" {
		t.Fatalf("text %q", got)
	}
	// paused / unknown forgets it
	if u.observe("a", "paused", t0) != nil || u.items["a"] != nil {
		t.Fatal("paused should forget")
	}
	if first := u.observe("b", "bad", t0); first.Up || first.Failures != 1 {
		t.Fatalf("first sight down: %+v", first)
	}
}

func TestUpTrackerAnnotate(t *testing.T) {
	u := newUpTracker()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	mk := func(srv string) []CanvasGateway {
		return []CanvasGateway{{GroupID: 1, Status: "ok", Anycast: []AnycastState{{Addr: "10.9.9.9", Up: true}},
			Servers: []CanvasServer{{Addr: "1.1.1.1", Status: srv, Tests: []CanvasTest{{Name: "x.example", Type: "A", Status: srv}}}}}}
	}
	g := mk("ok")
	u.annotate(g, t0)
	if g[0].Uptime == nil || g[0].Anycast[0].Uptime == nil || g[0].Servers[0].Uptime == nil || g[0].Servers[0].Tests[0].Uptime == nil {
		t.Fatalf("not annotated: %+v", g[0])
	}
	g = mk("bad")
	u.annotate(g, t0.Add(time.Minute))
	if up := g[0].Servers[0].Uptime; up.Up || up.Failures != 1 {
		t.Fatalf("server should be down: %+v", up)
	}
	if !g[0].Uptime.Up || g[0].Uptime.Failures != 0 {
		t.Fatalf("gateway unaffected: %+v", g[0].Uptime)
	}
	// a removed server is forgotten
	g = mk("ok")
	g[0].Servers = nil
	u.annotate(g, t0.Add(2*time.Minute))
	for k := range u.items {
		if strings.Contains(k, "/s/") || strings.Contains(k, "/t/") {
			t.Fatalf("stale item %s", k)
		}
	}
	// withdrawn anycast on a gateway that is not running says nothing
	g = []CanvasGateway{{GroupID: 2, Status: "idle", Anycast: []AnycastState{{Addr: "10.8.8.8"}}}}
	u.annotate(g, t0)
	if g[0].Anycast[0].Uptime != nil || g[0].Uptime != nil {
		t.Fatalf("idle gateway: %+v", g[0])
	}
}

func TestDurText(t *testing.T) {
	for d, want := range map[time.Duration]string{9 * time.Second: "9s", 7*time.Minute + 12*time.Second: "7m 12s", 2*time.Hour + 5*time.Minute: "2h 5m", 76 * time.Hour: "3d 4h"} {
		if got := durText(d); got != want {
			t.Errorf("%v → %q want %q", d, got, want)
		}
	}
}

// The tooltip says how long it has been online and how many failures were seen: "Online for 1m 1s - 0 failures".
func TestUpTextCountsFailures(t *testing.T) {
	u := newUpTracker()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	u.pass++
	text := func(status string, at time.Time, now time.Time) string {
		return u.observe("x", status, at).Text(now)
	}
	if got := text("ok", t0, t0.Add(61*time.Second)); got != "online for 1m 1s - 0 failures" {
		t.Fatalf("%q", got)
	}
	// each time it goes down is one failure; staying down or coming back does not add another
	text("bad", t0.Add(time.Hour), t0.Add(time.Hour))
	text("bad", t0.Add(time.Hour+time.Minute), t0.Add(time.Hour))
	if got := text("ok", t0.Add(2*time.Hour), t0.Add(2*time.Hour+5*time.Second)); got != "online for 5s - 1 failure" {
		t.Fatalf("%q", got)
	}
	text("warn", t0.Add(3*time.Hour), t0.Add(3*time.Hour)) // degraded is still online
	text("bad", t0.Add(4*time.Hour), t0.Add(4*time.Hour))
	if got := text("bad", t0.Add(4*time.Hour+time.Minute), t0.Add(4*time.Hour+30*time.Second)); got != "down for 30s - 2 failures" {
		t.Fatalf("%q", got)
	}
	var none *UpInfo
	if none.Text(t0) != "" {
		t.Fatal("nothing to say for an item with no uptime")
	}
}
