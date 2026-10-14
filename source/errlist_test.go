package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestErrTallyCountsSortsAndCaps(t *testing.T) {
	var t1 errTally
	t1.add(errKey{"a.example", "timeout"}, 1, 2)
	t1.add(errKey{"b.example", "SERVFAIL"}, 1, 2)
	t1.add(errKey{"b.example", "SERVFAIL"}, 4, 2) // a pair already kept still counts when full
	t1.add(errKey{"c.example", "timeout"}, 3, 2)  // a new pair past the limit only counts as other
	l := t1.list()
	if l.Total != 9 || l.Other != 3 || len(l.Rows) != 2 {
		t.Fatalf("%+v", l)
	}
	if l.Rows[0] != (errRow{"b.example", "SERVFAIL", 5}) || l.Rows[1] != (errRow{"a.example", "timeout", 1}) {
		t.Fatalf("order: %+v", l.Rows)
	}
	var t2 errTally
	t2.merge(&t1, 10)
	if t2.list().Total != 9 {
		t.Fatalf("merge lost errors: %+v", t2.list())
	}
	t1.reset()
	if len(t1.m) != 0 || t1.other != 0 {
		t.Fatal("reset")
	}
}

func TestErrStoreKeepsOnlyTheLastRuns(t *testing.T) {
	var s errStore
	for i := 0; i < keepErrLists+5; i++ {
		s.put(fmt.Sprint("job", i), &errList{Total: uint64(i)})
	}
	if s.get("job0") != nil || s.get("job4") != nil {
		t.Fatal("old lists were not dropped")
	}
	if l := s.get(fmt.Sprint("job", keepErrLists+4)); l == nil || l.Total != uint64(keepErrLists+4) {
		t.Fatalf("newest missing: %+v", l)
	}
}

func finishedJob(t *testing.T, a *App, kv ...string) *Job {
	t.Helper()
	j := a.startJob(args(kv...), "test")
	select {
	case <-j.done:
	case <-time.After(30 * time.Second):
		j.kill()
		t.Fatal("job did not finish")
	}
	return j
}

func TestRunKeepsTheFullErrorList(t *testing.T) {
	srv := startUDP(t, 3, nil, false)
	a := testApp(t)
	j := finishedJob(t, a, "server", srv.addr, "concurrency", "1", "pipeline", "4", "count", "50", "queries", "a.example\nb.example\nc.example")
	l := a.errLists.get(j.ID)
	if l == nil || l.Total != 50 || l.Other != 0 {
		t.Fatalf("%+v", l)
	}
	seen := map[string]bool{}
	var sum uint64
	for _, r := range l.Rows {
		if r.Reason != "NXDOMAIN" {
			t.Errorf("reason %q", r.Reason)
		}
		seen[r.Domain] = true
		sum += r.Count
	}
	if sum != 50 || len(seen) != 3 {
		t.Fatalf("sum=%d domains=%v rows=%+v", sum, seen, l.Rows)
	}
}

func TestRunWithoutErrorsHasAnEmptyList(t *testing.T) {
	srv := startUDP(t, 0, nil, false)
	a := testApp(t)
	j := finishedJob(t, a, "server", srv.addr, "concurrency", "1", "pipeline", "4", "count", "20")
	if l := a.errLists.get(j.ID); l == nil || l.Total != 0 || len(l.Rows) != 0 {
		t.Fatalf("%+v", l)
	}
}

func TestJobErrorsEndpoint(t *testing.T) {
	srv := startUDP(t, 3, nil, false)
	s := newTestServer(t)
	hdr := map[string]string{"X-Session-Token": s.apiToken(t)}
	j := finishedJob(t, s.app, "server", srv.addr, "concurrency", "1", "pipeline", "4", "count", "10", "queries", "only.example")
	resp, body := s.do(t, "GET", "/api/job/"+j.ID+"/errors", "", hdr)
	if resp.StatusCode != 200 || !strings.Contains(body, `"domain":"only.example"`) || !strings.Contains(body, `"count":10`) || !strings.Contains(body, `"total":10`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := s.do(t, "GET", "/api/job/nope/errors", "", hdr); resp.StatusCode != 404 {
		t.Fatalf("unknown job: %d", resp.StatusCode)
	}
}
