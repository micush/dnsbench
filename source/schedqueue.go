package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// One scheduled job at a time per node: a job that falls due while another is running waits its turn (first come, first
// served) and starts when the one before it has finished.  The queue is part of the saved state, so a restart keeps it.

type schedQueued struct {
	ID        string `json:"id"`
	Base      int64  `json:"base"`
	NotBefore int64  `json:"not_before,omitempty"` // a manual run asked for later (staggered over the nodes)
	Actor     string `json:"actor,omitempty"`      // who asked for a manual run
}

func (s *Scheduler) queuedLocked(id string) bool {
	for _, q := range s.st.Queue {
		if q.ID == id {
			return true
		}
	}
	return false
}

// drainLocked starts the first queued job that is due and can still run, when nothing is running.  s.mu is held.
func (s *Scheduler) drainLocked() {
	node := s.node
	if node == "" {
		node = localNodeID()
	}
	now := s.now().Unix()
	for len(s.running) == 0 {
		pick := -1
		for i, q := range s.st.Queue {
			if q.NotBefore <= now {
				pick = i
				break
			}
		}
		if pick < 0 {
			return
		}
		q := s.st.Queue[pick]
		s.st.Queue = append(s.st.Queue[:pick:pick], s.st.Queue[pick+1:]...)
		for _, t := range s.tasks {
			if t.ID == q.ID && !t.Disabled && t.appliesTo(node) {
				actor := "schedule"
				if q.Actor != "" {
					actor = q.Actor
				}
				s.startLocked(t, q.Base, actor)
				break
			}
		}
		s.saveLocked()
	}
}

// RunQueued is a manual "run now" that waits its turn: it starts at once when nothing is running and the delay is 0, otherwise
// it joins the queue (not before the delay has passed).
func (s *Scheduler) RunQueued(id, actor string, delay time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.ID != id {
			continue
		}
		if s.running[id] || s.queuedLocked(id) {
			return errors.New("that task is running or already waiting")
		}
		warnf("scheduler: %q asked to run task %q%s", actor, t.Name, map[bool]string{true: " (after a delay)", false: ""}[delay > 0])
		s.st.Queue = append(s.st.Queue, schedQueued{ID: id, NotBefore: s.now().Add(delay).Unix(), Actor: actor})
		s.saveLocked()
		s.drainLocked()
		return nil
	}
	return errors.New("no such task")
}

// SchedRunAll asks every node the task is for to run it now, one after another schedStagger apart in node-ID order; each node
// runs it when it has finished what it is doing.  The answer says what each node was told.
func (m *Mgmt) SchedRunAll(id, actor string) (map[string]string, error) {
	var task *SchedTask
	m.sched.mu.Lock()
	for i := range m.sched.tasks {
		if m.sched.tasks[i].ID == id {
			t := m.sched.tasks[i]
			task = &t
		}
	}
	m.sched.mu.Unlock()
	if task == nil {
		return nil, errors.New("no such task")
	}
	type target struct {
		id, addr string
		self     bool
	}
	var ts []target
	if m.cl.Enabled() {
		snap := m.cl.Snapshot()
		ts = append(ts, target{snap.NodeID, snap.SelfAddr, true})
		for _, p := range snap.Peers {
			if p.Addr != snap.SelfAddr && !containsStr(snap.Removed, p.Addr) {
				ts = append(ts, target{p.NodeID, p.Addr, false})
			}
		}
	} else {
		ts = append(ts, target{localNodeID(), "this node", true})
	}
	var sel []target
	for _, t := range ts {
		if task.appliesTo(t.id) {
			sel = append(sel, t)
		}
	}
	if len(sel) == 0 {
		return nil, errors.New("the task is not for any node of the cluster")
	}
	sort.Slice(sel, func(i, j int) bool { return sel[i].id < sel[j].id })
	out := map[string]string{}
	for i, t := range sel {
		delay := time.Duration(i) * schedStagger
		var err error
		if t.self {
			err = m.sched.RunQueued(id, actor, delay)
		} else {
			body, _ := json.Marshal(map[string]any{"id": id, "queue": true, "delay_sec": int(delay / time.Second)})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			var resp *proxyResp
			resp, err = m.cl.Relay(ctx, t.addr, proxyReq{User: actor, Method: http.MethodPost, Path: "/api/schedule/run", CT: "application/json", Body: body})
			cancel()
			if err == nil && resp.Status != http.StatusOK {
				err = fmt.Errorf("answered %d", resp.Status)
			}
		}
		if err != nil {
			out[t.addr] = "not started: " + err.Error()
		} else if delay > 0 {
			out[t.addr] = fmt.Sprintf("queued, starts in %d s", int(delay/time.Second))
		} else {
			out[t.addr] = "started"
		}
	}
	return out, nil
}
