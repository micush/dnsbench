package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSchedulerUpdateOnlineWhenAlreadyNewest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			w.Write([]byte(`{"tag_name":"v1"}`))
			return
		}
		t.Errorf("nothing else may be fetched, got %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	oa, oh := ghAPI, ghHost
	ghAPI, ghHost = srv.URL, srv.URL
	defer func() { ghAPI, ghHost = oa, oh }()
	mg := newPowerMgmt(t)
	out, err := mg.sched.builtin("update_online", "test")
	if err != nil || !strings.Contains(out, "up to date") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestSchedulerUpdateOnlineCannotReach(t *testing.T) {
	oa := ghAPI
	ghAPI = "http://127.0.0.1:1"
	defer func() { ghAPI = oa }()
	mg := newPowerMgmt(t)
	if _, err := mg.sched.builtin("update_online", "test"); err == nil {
		t.Fatal("an unreachable GitHub must fail the task")
	}
}

func TestSchedulerRebootsTheHost(t *testing.T) {
	pc := fakePower(t)
	od := schedActionDelay
	schedActionDelay = 1
	defer func() { schedActionDelay = od }()
	mg := newPowerMgmt(t)
	out, err := mg.sched.builtin("node_reboot", "test")
	if err != nil || !strings.Contains(out, "reboot") {
		t.Fatalf("%q %v", out, err)
	}
	calls := waitCalls(t, pc, 1)
	if len(calls) == 0 || !strings.Contains(strings.Join(calls, " "), "reboot") {
		t.Fatalf("calls %v", calls)
	}
}

func TestSchedulerRunOnAllNodesQueuesAndStaggers(t *testing.T) {
	h := newHarness(t, mustTask(t, SchedTask{ID: "a1", Name: "a", Action: "stats_clear", Freq: "daily", At: "12:00"}),
		mustTask(t, SchedTask{ID: "a2", Name: "b", Action: "node_pause", Freq: "daily", At: "12:00"}))
	h.s.Tick("n1")
	// a delayed manual run is not started before its time
	if err := h.s.RunQueued("a1", "me", 60*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if h.count() != 0 {
		t.Fatal("started before its delay")
	}
	if err := h.s.RunQueued("a1", "me", 0); err == nil {
		t.Fatal("a task already waiting must not be queued twice")
	}
	if !h.s.Status().Tasks[0].Queued {
		t.Fatal("not shown as waiting")
	}
	h.at(h.now.Add(61 * time.Second))
	h.s.Tick("n1")
	waitFor(t, func() bool { return h.count() == 1 })
	// an immediate one runs at once when idle
	waitFor(t, func() bool { return !h.s.Status().Tasks[0].Running })
	if err := h.s.RunQueued("a2", "me", 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return h.count() == 2 })
	waitFor(t, func() bool {
		for _, r := range h.s.Status().History {
			if r.Status == "running" {
				return false
			}
		}
		return true
	})
}

func TestSchedRunAllOnAMemberlessNode(t *testing.T) {
	mg := newPowerMgmt(t)
	mg.sched.SetTasks([]SchedTask{mustTask(t, SchedTask{ID: "a1", Name: "a", Action: "shell", Command: "true", Freq: "daily"})})
	out, err := mg.SchedRunAll("a1", "me")
	if err != nil || len(out) != 1 {
		t.Fatalf("%v %v", out, err)
	}
	for _, v := range out {
		if v != "started" {
			t.Fatalf("%q", v)
		}
	}
	if _, err := mg.SchedRunAll("nope", "me"); err == nil {
		t.Fatal("unknown task")
	}
	waitFor(t, func() bool {
		for _, r := range mg.sched.Status().History {
			if r.Status == "running" {
				return false
			}
		}
		return len(mg.sched.Status().History) > 0
	})
}

func TestReleaseNameSaysItOnce(t *testing.T) {
	if got := releaseName("v80", "80"); got != "v80" {
		t.Fatalf("same tag and version: %q", got)
	}
	if got := releaseName("v80-rc1", "80"); got != "v80-rc1 (v80)" {
		t.Fatalf("different: %q", got)
	}
}
