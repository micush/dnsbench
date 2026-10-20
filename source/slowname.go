package main

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// A server that times out on one name is not asked that name first again for a while.  Health probes use a few fixed
// names, so a server can pass them and still never answer some others (a name whose authoritative servers it
// cannot reach in time): without this every client asking such a name waits out the query timeout
// on that server before the next one answers, each time it asks.  The server stays in service for every other name,
// and is still tried for this one, after the others.

const (
	avoidFor  = time.Minute
	avoidKeep = 8192 // entries kept; past that the expired ones go, then all
)

type nameAvoid struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func avoidKey(server, name string, qtype uint16) string {
	return server + "|" + strings.ToLower(name) + "|" + strconv.Itoa(int(qtype))
}

func (a *nameAvoid) mark(server, name string, qtype uint16) {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m == nil {
		a.m = map[string]time.Time{}
	}
	if len(a.m) >= avoidKeep {
		for k, t := range a.m {
			if now.After(t) {
				delete(a.m, k)
			}
		}
		if len(a.m) >= avoidKeep {
			a.m = map[string]time.Time{}
		}
	}
	a.m[avoidKey(server, name, qtype)] = now.Add(avoidFor)
}

// order puts the servers that recently timed out on this name last (otherwise in the same order).
func (a *nameAvoid) order(ranked []*Server, name string, qtype uint16) []*Server {
	if len(ranked) < 2 {
		return ranked
	}
	a.mu.Lock()
	if len(a.m) == 0 {
		a.mu.Unlock()
		return ranked
	}
	now := time.Now()
	var bad []bool
	for i, s := range ranked {
		if t, ok := a.m[avoidKey(s.Addr, name, qtype)]; ok && now.Before(t) {
			if bad == nil {
				bad = make([]bool, len(ranked))
			}
			bad[i] = true
		}
	}
	a.mu.Unlock()
	if bad == nil {
		return ranked
	}
	out := make([]*Server, 0, len(ranked))
	for i, s := range ranked {
		if !bad[i] {
			out = append(out, s)
		}
	}
	for i, s := range ranked {
		if bad[i] {
			out = append(out, s)
		}
	}
	return out
}
