package main

import (
	"errors"
	"fmt"
	"sync"
)

var schedEditMu sync.Mutex

// onlineUpdateNeedsOneNode says the task is an online update meant for every node while auto-update is on in a cluster:
// that is refused, it is to name exactly one node.
func onlineUpdateNeedsOneNode(t SchedTask, autoUpdate, clustered bool) bool {
	return t.Action == "update_online" && autoUpdate && clustered && len(t.Nodes) != 1
}

// SchedSet adds a task, or replaces the one with the same id (Operate ▸ Scheduler, --schedule-set).  The whole list goes
// through PutConfig, so a replica sends it to the primary like any other shared change.
func (m *Mgmt) SchedSet(t SchedTask, actor string) (SchedTask, error) {
	schedEditMu.Lock()
	defer schedEditMu.Unlock()
	dc, _, err := m.LiveConfig()
	if err != nil {
		return t, err
	}
	if err := t.validate(); err != nil {
		return t, err
	}
	// With auto-update on, one node staging the release is all it takes (every node then updates itself), so the online
	// update is for one node: on each node, each would fetch it and queue its own update, outside the cluster's order.
	if onlineUpdateNeedsOneNode(t, m.upd.Intent().AutoAll, m.cl != nil && len(m.cl.node.Snapshot().Peers) > 0) {
		return t, errors.New("auto-update is on, so the online update runs on one node: choose that node under Runs on, or turn auto-update off")
	}
	replaced := false
	for i := range dc.DNS.Schedule {
		if dc.DNS.Schedule[i].ID == t.ID {
			dc.DNS.Schedule[i], replaced = t, true
		}
	}
	if !replaced {
		dc.DNS.Schedule = append(dc.DNS.Schedule, t)
	}
	verb := "added"
	if replaced {
		verb = "changed"
	}
	if err := m.PutConfig(dc, actor, fmt.Sprintf("Scheduled task %q %s", t.Name, verb)); err != nil {
		return t, err
	}
	return t, nil
}

// SchedDel removes a task.
func (m *Mgmt) SchedDel(id, actor string) error {
	schedEditMu.Lock()
	defer schedEditMu.Unlock()
	dc, _, err := m.LiveConfig()
	if err != nil {
		return err
	}
	var keep []SchedTask
	name := ""
	for _, t := range dc.DNS.Schedule {
		if t.ID == id {
			name = t.Name
			continue
		}
		keep = append(keep, t)
	}
	if name == "" {
		return errors.New("no such task")
	}
	dc.DNS.Schedule = keep
	return m.PutConfig(dc, actor, fmt.Sprintf("Scheduled task %q deleted", name))
}
