package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// strainLog writes a log line when a node's CPU, memory or fullest disk goes over hostStrainPct (the node's shape on the
// drawing turns amber) and another when it is back at or under it.  It remembers which of the three are over per node, so a
// reading that merely moves (CPU 91% then 93%) is not logged again.
type strainLog struct {
	mu  sync.Mutex
	was map[string]string // node key -> the kinds over the limit now ("CPU,disk"), absent when none
}

func strainKinds(l HostLoad) string {
	var k []string
	for _, s := range l.Strained(false) {
		k = append(k, strings.Fields(s)[0])
	}
	return strings.Join(k, ",")
}

// note records one reading of a node and returns what to log ("" for nothing) and whether it is a warning.
func (s *strainLog) note(key, name string, l HostLoad) (string, bool) {
	now := strainKinds(l)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.was == nil {
		s.was = map[string]string{}
	}
	before := s.was[key]
	if now == before {
		return "", false
	}
	if now == "" {
		delete(s.was, key)
		cpu := "n/a"
		if l.CPU >= 0 {
			cpu = fmt.Sprintf("%.0f%%", l.CPU)
		}
		return fmt.Sprintf("host load: node %s is back at %.0f%% or below (CPU %s, memory %.0f%%, disk %.0f%%)", name, hostStrainPct, cpu, l.Mem, l.Disk), false
	}
	s.was[key] = now
	what := strings.Join(l.Strained(true), ", ")
	if before == "" {
		return fmt.Sprintf("host load: node %s is over %.0f%%: %s; it shows amber until all are back at %.0f%% or below", name, hostStrainPct, what, hostStrainPct), true
	}
	return fmt.Sprintf("host load: node %s is now over %.0f%%: %s", name, hostStrainPct, what), true
}

// logStrain checks this node and, in a cluster, every reachable member that reported its load.
func (c *Cluster) logStrain() {
	emit := func(msg string, warn bool) {
		if msg == "" {
			return
		}
		if warn {
			warnf("%s", msg)
		} else {
			infof("%s", msg)
		}
	}
	self, _ := os.Hostname()
	var view ClusterView
	if c.Enabled() {
		view = c.View()
	}
	infos := map[string]*HostLoad{}
	c.mu.Lock()
	for addr, pi := range c.info {
		if pi != nil && pi.Msg.Host != nil {
			h := *pi.Msg.Host
			infos[addr] = &h
		}
	}
	c.mu.Unlock()
	for _, p := range view.Peers {
		if p.Self {
			if p.Hostname != "" {
				self = p.Hostname
			}
			continue
		}
		if h := infos[p.Addr]; h != nil && p.Reachable { // an unreachable node keeps its last state: no "cleared" for silence
			name := p.Hostname
			if name == "" {
				name = p.Addr
			}
			emit(c.strain.note(p.Addr, name, *h))
		}
	}
	if l, ok := hostLoadFn(); ok {
		emit(c.strain.note("", self, l))
	}
}
