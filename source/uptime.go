package main

import (
	"fmt"
	"sync"
	"time"
)

// Uptime for the topology: how long each gateway, anycast address, DNS server and domain test
// has been online since it last failed.  The picture is rebuilt for every request, so the
// times live here, in memory: a sampler looks at the picture every few seconds (even with no
// browser open) and notes when an item flips between working and failing.  A restart of ddgw
// starts the counting again.

const upSampleEvery = 2 * time.Second

// UpInfo says how long an item has been in its current state.
type UpInfo struct {
	Up    bool  `json:"up"`    // working now (green or yellow); false = failing (red)
	Since int64 `json:"since"` // unix seconds the current state began
	// Failures is how many times it has failed since ddgw began counting (it starts at 1 when first seen failing).
	Failures int `json:"failures"`
}

type upItem struct {
	info UpInfo
	seen uint64
}

type upTracker struct {
	mu    sync.Mutex
	items map[string]*upItem
	pass  uint64
}

func newUpTracker() *upTracker { return &upTracker{items: map[string]*upItem{}} }

// observe records the status of one item and returns its info; nil when the status says nothing
// about working or failing (grey, paused), which also forgets the item.
func (u *upTracker) observe(key, status string, now time.Time) *UpInfo {
	up := status == "ok" || status == "warn"
	if !up && status != "bad" {
		delete(u.items, key)
		return nil
	}
	it := u.items[key]
	switch {
	case it == nil:
		it = &upItem{info: UpInfo{Up: up, Since: now.Unix()}}
		if !up {
			it.info.Failures = 1
		}
		u.items[key] = it
	case it.info.Up != up:
		f := it.info.Failures
		if !up {
			f++ // a new failure; coming back does not count again
		}
		it.info = UpInfo{Up: up, Since: now.Unix(), Failures: f}
	}
	it.seen = u.pass
	c := it.info
	return &c
}

// annotate fills the Uptime fields of the groups and forgets items that are gone.
func (u *upTracker) annotate(groups []CanvasGateway, now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.pass++
	for i := range groups {
		g := &groups[i]
		gk := fmt.Sprintf("g%d", g.GroupID)
		g.Uptime = u.observe(gk, g.Status, now)
		running := g.Status != "idle" && g.Status != "paused"
		for j := range g.Anycast {
			a := &g.Anycast[j]
			st := "idle"
			if running {
				st = "bad"
				if a.Up {
					st = "ok"
					if a.Status == "warn" || a.Status == "bad" {
						st = a.Status
					}
				}
			}
			a.Uptime = u.observe(gk+"/a/"+a.Addr, st, now)
		}
		for j := range g.Servers {
			s := &g.Servers[j]
			s.Uptime = u.observe(gk+"/s/"+s.Addr, s.Status, now)
			for k := range s.Tests {
				t := &s.Tests[k]
				t.Uptime = u.observe(gk+"/t/"+s.Addr+"/"+t.Name+"/"+t.Type, t.Status, now)
			}
		}
	}
	for k, it := range u.items {
		if it.seen != u.pass {
			delete(u.items, k)
		}
	}
}

// durText writes a span as "3d 4h", "2h 5m", "7m 12s" or "9s".
func durText(d time.Duration) string {
	s := int64(d / time.Second)
	if s < 0 {
		s = 0
	}
	days, h, m, sec := s/86400, s/3600%24, s/60%60, s%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

// Text is the sentence the CLI and the tooltips use.
func (u *UpInfo) Text(now time.Time) string {
	if u == nil {
		return ""
	}
	d := durText(now.Sub(time.Unix(u.Since, 0)))
	n := fmt.Sprintf("%d failures", u.Failures)
	if u.Failures == 1 {
		n = "1 failure"
	}
	if !u.Up {
		return "down for " + d + " - " + n
	}
	return "online for " + d + " - " + n
}
