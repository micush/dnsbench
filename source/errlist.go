package main

import (
	"sort"
	"sync"
)

// The full error list of a run: every distinct (domain, reason) pair with how many times it
// happened. Counting pairs instead of storing each failure keeps a run that fails hundreds of
// thousands of times small, and loses nothing a reader could use.
const (
	maxErrDistinct = 5000 // distinct pairs kept per run; further ones are only counted in Other
	maxLocalErrs   = 2000 // the same limit for one worker's private tally
	keepErrLists   = 100  // runs whose error list the server remembers
)

type errKey struct{ domain, reason string }

// errTally counts errors per (domain, reason), up to a limit of distinct pairs.
type errTally struct {
	m     map[errKey]uint64
	other uint64 // errors whose pair did not fit
}

func (t *errTally) add(k errKey, n uint64, limit int) {
	if t.m == nil {
		t.m = map[errKey]uint64{}
	}
	if _, seen := t.m[k]; seen || len(t.m) < limit {
		t.m[k] += n
		return
	}
	t.other += n
}

func (t *errTally) merge(o *errTally, limit int) {
	for k, n := range o.m {
		t.add(k, n, limit)
	}
	t.other += o.other
}

func (t *errTally) reset() {
	clear(t.m)
	t.other = 0
}

// errRow is one line of a run's error list.
type errRow struct {
	Domain string `json:"domain"`
	Reason string `json:"reason"`
	Count  uint64 `json:"count"`
}

// errList is a finished run's error list, most frequent first.
type errList struct {
	Rows  []errRow `json:"errors"`
	Other uint64   `json:"other"` // errors beyond the distinct-pair limit, not listed
	Total uint64   `json:"total"`
}

func (t *errTally) list() *errList {
	l := &errList{Rows: make([]errRow, 0, len(t.m)), Other: t.other, Total: t.other}
	for k, n := range t.m {
		l.Rows = append(l.Rows, errRow{k.domain, k.reason, n})
		l.Total += n
	}
	sort.Slice(l.Rows, func(i, j int) bool {
		a, b := l.Rows[i], l.Rows[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Domain != b.Domain {
			return a.Domain < b.Domain
		}
		return a.Reason < b.Reason
	})
	return l
}

// errStore remembers the error lists of the most recent runs.
type errStore struct {
	mu    sync.Mutex
	lists map[string]*errList
	order []string // oldest first
}

func (s *errStore) put(id string, l *errList) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lists == nil {
		s.lists = map[string]*errList{}
	}
	if _, had := s.lists[id]; !had {
		s.order = append(s.order, id)
	}
	s.lists[id] = l
	for len(s.order) > keepErrLists {
		delete(s.lists, s.order[0])
		s.order = s.order[1:]
	}
}

func (s *errStore) get(id string) *errList {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lists[id]
}
