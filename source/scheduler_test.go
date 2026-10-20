package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustTask(t *testing.T, s SchedTask) SchedTask {
	t.Helper()
	if s.Name == "" {
		s.Name = "t"
	}
	if s.Action == "" {
		s.Action = "stats_clear"
	}
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	return s
}

func dates(l []time.Time) string {
	var p []string
	for _, x := range l {
		p = append(p, x.Format("01-02 15:04"))
	}
	return strings.Join(p, ",")
}

func TestScheduleThirdSaturday(t *testing.T) {
	task := mustTask(t, SchedTask{Freq: "monthly", Nth: 3, Weekday: 6, At: "12:00", TZ: "UTC"})
	got := dates(task.bases(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC)))
	want := "01-17 12:00,02-21 12:00,03-21 12:00,04-18 12:00,05-16 12:00,06-20 12:00,07-18 12:00,08-15 12:00,09-19 12:00,10-17 12:00,11-21 12:00,12-19 12:00"
	if got != want {
		t.Fatalf("3rd Saturdays:\n got %s\nwant %s", got, want)
	}
	if n := task.next(time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)); n.Format("2006-01-02 15:04") != "2026-10-17 12:00" {
		t.Fatalf("next: %v", n)
	}
}

func TestScheduleKinds(t *testing.T) {
	utc := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		name string
		task SchedTask
		from time.Time
		to   time.Time
		want string
	}{
		{"last day, Feb clamp for the 31st", SchedTask{Freq: "monthly", Day: 31, At: "01:00", TZ: "UTC"}, utc(2026, 1, 1), utc(2026, 4, 1), "01-31 01:00,02-28 01:00,03-31 01:00"},
		{"last day of the month", SchedTask{Freq: "monthly", Day: -1, At: "01:00", TZ: "UTC"}, utc(2028, 2, 1), utc(2028, 3, 5), "02-29 01:00"},
		{"last Friday", SchedTask{Freq: "monthly", Nth: -1, Weekday: 5, At: "23:30", TZ: "UTC"}, utc(2026, 10, 1), utc(2026, 12, 31), "10-30 23:30,11-27 23:30,12-25 23:30"},
		{"every other month", SchedTask{Freq: "monthly", Every: 2, Day: 1, At: "00:00", TZ: "UTC"}, utc(2026, 1, 1), utc(2026, 7, 1), "01-01 00:00,03-01 00:00,05-01 00:00,07-01 00:00"},
		{"weekly Mon and Thu", SchedTask{Freq: "weekly", Days: []int{4, 1}, At: "06:00", TZ: "UTC"}, utc(2026, 10, 12), utc(2026, 10, 23), "10-12 06:00,10-15 06:00,10-19 06:00,10-22 06:00"},
		{"every 2 weeks, Monday", SchedTask{Freq: "weekly", Every: 2, Days: []int{1}, At: "06:00", TZ: "UTC"}, utc(2026, 10, 1), utc(2026, 11, 10), "10-05 06:00,10-19 06:00,11-02 06:00"},
		{"every 3 days", SchedTask{Freq: "daily", Every: 3, At: "02:00", TZ: "UTC"}, utc(2026, 10, 1), utc(2026, 10, 11), "10-01 02:00,10-04 02:00,10-07 02:00,10-10 02:00"},
		{"yearly 3rd Sunday of March", SchedTask{Freq: "yearly", Month: 3, Nth: 3, Weekday: 0, At: "04:00", TZ: "UTC"}, utc(2026, 1, 1), utc(2028, 12, 31), "03-15 04:00,03-21 04:00,03-19 04:00"},
		{"hourly at minute 15", SchedTask{Freq: "hourly", At: "00:15:00"}, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC), "10-01 00:15,10-01 01:15,10-01 02:15"},
		{"every 6 hours", SchedTask{Freq: "hourly", Every: 6, At: "00:00:00"}, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC), "10-01 00:00,10-01 06:00,10-01 12:00"},
	}
	for _, c := range cases {
		task := mustTask(t, c.task)
		bs := task.bases(c.from, c.to)
		for i := range bs {
			bs[i] = bs[i].UTC()
		}
		if got := dates(bs); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestScheduleOffsetStaysInTheWindow(t *testing.T) {
	base := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	if schedOffset("n1", "a", base, 0) != 0 {
		t.Fatal("no window, but an offset")
	}
	seen := map[time.Duration]bool{}
	for _, n := range []string{"n1", "n2", "n3", "n4", "n5", "n6"} {
		o := schedOffset(n, "a", base, 3600)
		if o < 0 || o > time.Hour {
			t.Fatalf("%s: offset %v is outside the window", n, o)
		}
		if o != schedOffset(n, "a", base, 3600) {
			t.Fatal("the offset changed between calls")
		}
		seen[o] = true
	}
	if len(seen) < 4 {
		t.Fatalf("the nodes were not spread over the window: %v", seen)
	}
}

type schedHarness struct {
	s   *Scheduler
	now time.Time
	mu  sync.Mutex
	ran []string
}

func newHarness(t *testing.T, tasks ...SchedTask) *schedHarness {
	h := &schedHarness{now: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)}
	h.s = newScheduler(nil, t.TempDir())
	h.s.now = func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }
	h.s.act = func(action, actor string) (string, error) {
		h.mu.Lock()
		h.ran = append(h.ran, action)
		h.mu.Unlock()
		return "done", nil
	}
	h.s.SetTasks(tasks)
	return h
}

func (h *schedHarness) at(tm time.Time) { h.mu.Lock(); h.now = tm; h.mu.Unlock() }
func (h *schedHarness) count() int      { h.mu.Lock(); defer h.mu.Unlock(); return len(h.ran) }

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestSchedulerRunsInsideTheWindowOnceAndCatchesUp(t *testing.T) {
	task := mustTask(t, SchedTask{ID: "a", Freq: "daily", At: "12:00", TZ: "UTC", WindowSec: 1800})
	h := newHarness(t, task)
	h.s.Tick("n1") // first sight: nothing before now is owed
	h.at(time.Date(2026, 10, 17, 11, 59, 0, 0, time.UTC))
	h.s.Tick("n1")
	if h.count() != 0 {
		t.Fatal("ran before its time")
	}
	base := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	runAt := base.Add(schedOffset("n1", "a", base, 1800))
	h.at(runAt.Add(-time.Second))
	h.s.Tick("n1")
	if h.count() != 0 {
		t.Fatal("ran before its own time in the window")
	}
	h.at(runAt.Add(time.Second))
	h.s.Tick("n1")
	waitFor(t, func() bool { return h.count() == 1 })
	h.at(runAt.Add(10 * time.Second))
	h.s.Tick("n1")
	time.Sleep(50 * time.Millisecond)
	if h.count() != 1 {
		t.Fatalf("ran %d times for one occurrence", h.count())
	}
	st := h.s.Status()
	if len(st.Tasks) != 1 || st.Tasks[0].Last == nil || st.Tasks[0].Next != time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("status: %+v", st.Tasks)
	}

	// a node that was off comes back inside the window: it runs once
	h2 := newHarness(t, task)
	h2.s.Tick("n1")
	h2.at(base.Add(1700 * time.Second))
	h2.s.Tick("n1")
	waitFor(t, func() bool { return h2.count() == 1 })

	// back after the window: skipped, and the history says so
	h3 := newHarness(t, task)
	h3.s.Tick("n1")
	h3.at(base.Add(3 * time.Hour))
	h3.s.Tick("n1")
	time.Sleep(50 * time.Millisecond)
	if h3.count() != 0 {
		t.Fatal("ran a missed occurrence long after its window")
	}
	hist := h3.s.Status().History
	if len(hist) != 1 || hist[0].Status != "skipped" {
		t.Fatalf("history: %+v", hist)
	}
}

func TestSchedulerOnlyRunsWhereTheTaskIs(t *testing.T) {
	task := mustTask(t, SchedTask{ID: "b", Freq: "daily", At: "12:00", TZ: "UTC", Nodes: []string{"ab02"}})
	h := newHarness(t, task)
	h.s.Tick("n1")
	h.at(time.Date(2026, 10, 17, 12, 0, 5, 0, time.UTC))
	h.s.Tick("n1")
	time.Sleep(50 * time.Millisecond)
	if h.count() != 0 {
		t.Fatal("ran on a node that is not one of the task's nodes")
	}
}

func TestSchedulerShellCommand(t *testing.T) {
	h := newHarness(t)
	ok := mustTask(t, SchedTask{ID: "s1", Name: "echo", Action: "shell", Command: "echo hello; echo oops >&2", Freq: "daily"})
	slow := mustTask(t, SchedTask{ID: "s2", Name: "slow", Action: "shell", Command: "sleep 30", TimeoutSec: 1, Freq: "daily"})
	bad := mustTask(t, SchedTask{ID: "s3", Name: "bad", Action: "shell", Command: "exit 3", Freq: "daily"})
	h.s.SetTasks([]SchedTask{ok, slow, bad})
	idle := func() bool {
		for _, r := range h.s.Status().History {
			if r.Status == "running" {
				return false
			}
		}
		return true
	}
	for _, id := range []string{"s1", "s2", "s3"} { // one at a time
		if err := h.s.RunNow(id, "test"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, idle)
	}
	by := map[string]SchedRun{}
	for _, r := range h.s.Status().History {
		by[r.Task] = r
	}
	if by["s1"].Status != "ok" || !strings.Contains(by["s1"].Output, "hello") || !strings.Contains(by["s1"].Output, "oops") {
		t.Errorf("echo: %+v", by["s1"])
	}
	if by["s2"].Status != "failed" || !strings.Contains(by["s2"].Output, "stopped after") {
		t.Errorf("a command over its limit: %+v", by["s2"])
	}
	if by["s3"].Status != "failed" {
		t.Errorf("exit 3: %+v", by["s3"])
	}
}

func TestSchedulerHistorySurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	a := newScheduler(nil, dir)
	a.act = func(string, string) (string, error) { return "x", nil }
	a.SetTasks([]SchedTask{mustTask(t, SchedTask{ID: "h", Freq: "daily"})})
	if err := a.RunNow("h", "test"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { h := a.Status().History; return len(h) == 1 && h[0].Status == "ok" })
	b := newScheduler(nil, dir)
	if h := b.Status().History; len(h) != 1 || h[0].Task != "h" {
		t.Fatalf("history after a restart: %+v", h)
	}
}

func TestScheduleValidation(t *testing.T) {
	bad := []SchedTask{
		{Name: "", Action: "stats_clear", Freq: "daily"},
		{Name: "x", Action: "rm -rf", Freq: "daily"},
		{Name: "x", Action: "shell", Freq: "daily"},
		{Name: "x", Action: "stats_clear", Freq: "fortnightly"},
		{Name: "x", Action: "stats_clear", Freq: "daily", At: "25:00"},
		{Name: "x", Action: "stats_clear", Freq: "weekly"},
		{Name: "x", Action: "stats_clear", Freq: "weekly", Days: []int{7}},
		{Name: "x", Action: "stats_clear", Freq: "monthly", Day: 32},
		{Name: "x", Action: "stats_clear", Freq: "monthly", Nth: 5, Weekday: 1},
		{Name: "x", Action: "stats_clear", Freq: "yearly"},
		{Name: "x", Action: "stats_clear", Freq: "daily", WindowSec: -1},
		{Name: "x", Action: "stats_clear", Freq: "daily", Every: 400},
	}
	for i, b := range bad {
		if err := b.validate(); err == nil {
			t.Errorf("case %d accepted: %+v", i, b)
		}
	}
	l := []SchedTask{{Name: "A", Action: "stats_clear", Freq: "daily"}, {Name: "a", Action: "stats_clear", Freq: "daily"}}
	if err := validateSchedule(l); err == nil {
		t.Error("two tasks with one name accepted")
	}
	// a copy compares equal after validation, whatever the other frequencies' fields held
	x := SchedTask{Name: "x", Action: "stats_clear", Freq: "daily", Days: []int{1}, Day: 5, Month: 3, Command: "ls"}
	if err := x.validate(); err != nil || x.Days != nil || x.Day != 0 || x.Month != 0 || x.Command != "" {
		t.Errorf("fields of other kinds kept: %+v %v", x, err)
	}
}

func TestScheduleDescribe(t *testing.T) {
	task := mustTask(t, SchedTask{Freq: "monthly", Nth: 3, Weekday: 6, At: "12:00", WindowSec: 5400})
	if got := task.describe(); got != "every month, the 3rd Saturday at 12:00 UTC, within 1h 30m" {
		t.Fatalf("%q", got)
	}
}

func TestScheduleSurvivesConfigSave(t *testing.T) {
	dc := newDaemonConfig()
	dc.DNS.Schedule = []SchedTask{{Name: "n", Action: "stats_clear", Freq: "daily", At: "03:00"}}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	if dc.DNS.Schedule[0].ID == "" {
		t.Fatal("no id given")
	}
	sh := sharedOf(dc)
	if len(sh.DNS.Schedule) != 1 {
		t.Fatal("the schedule is not replicated")
	}
	h1 := sh.hash()
	dc.DNS.Schedule[0].Name = "changed"
	if sharedOf(dc).hash() == h1 {
		t.Fatal("a change of the schedule does not change the shared hash")
	}
	c := cloneDNS(dc.DNS)
	c = ownPoolFrom(dc.DNS)
	if c.Schedule != nil || dc.DNS.Schedule == nil {
		t.Fatal("a gateway's own pool must not carry the schedule")
	}
	if dc.DNS.live().Schedule != nil {
		t.Fatal("the live pool settings must not carry the schedule (a change would restart the pool)")
	}
}

func TestScheduleSetAndDeleteThroughMgmt(t *testing.T) {
	mg := newPowerMgmt(t)
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{}
	if err := dc.save(mg.confPath); err != nil {
		t.Fatal(err)
	}
	task, err := mg.SchedSet(SchedTask{Name: "nightly", Action: "stats_clear", Freq: "daily", At: "03:00"}, "test")
	if err != nil || task.ID == "" {
		t.Fatalf("add: %+v %v", task, err)
	}
	got, _, _ := mg.LiveConfig()
	if len(got.DNS.Schedule) != 1 || got.DNS.Schedule[0].Name != "nightly" {
		t.Fatalf("not saved: %+v", got.DNS.Schedule)
	}
	task.At = "04:30"
	if _, err := mg.SchedSet(task, "test"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = mg.LiveConfig()
	if len(got.DNS.Schedule) != 1 || got.DNS.Schedule[0].At != "04:30:00" {
		t.Fatalf("not replaced: %+v", got.DNS.Schedule)
	}
	if _, err := mg.SchedSet(SchedTask{Name: "NIGHTLY", Action: "stats_clear", Freq: "daily"}, "test"); err == nil {
		t.Fatal("a second task with the same name was accepted")
	}
	if err := mg.SchedDel("nope", "test"); err == nil {
		t.Fatal("deleting a task that is not there did not fail")
	}
	if err := mg.SchedDel(task.ID, "test"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = mg.LiveConfig()
	if len(got.DNS.Schedule) != 0 {
		t.Fatalf("not deleted: %+v", got.DNS.Schedule)
	}
}

func TestMultipleOrdinalsAndWeekdaysWithAWindow(t *testing.T) {
	utc := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	task := mustTask(t, SchedTask{Freq: "monthly", Nths: []int{3, 2}, Weekdays: []int{6, 0}, At: "01:00", Until: "10:00", TZ: "UTC"})
	if task.WindowSec != 9*3600 {
		t.Fatalf("window %d", task.WindowSec)
	}
	var got []string
	for _, b := range task.bases(utc(2026, 11, 1), utc(2026, 11, 30).Add(24*time.Hour-time.Second)) {
		got = append(got, b.Format("01-02 15:04"))
	}
	// November 2026: Sundays 1,8,15,22,29 and Saturdays 7,14,21,28 → the candidates are Sun 8, Sat 14, Sun 15, Sat 21,
	// but the task runs once a month, on the first of them.
	if want := "11-08 01:00"; strings.Join(got, ",") != want {
		t.Fatalf("got %v want %s", got, want)
	}
	if d := task.describe(); d != "every month, the 2nd or 3rd Sunday or Saturday between 01:00 and 10:00 UTC" && !strings.Contains(d, "between 01:00 and 10:00") {
		t.Fatal(d)
	}
	old := mustTask(t, SchedTask{Freq: "monthly", Nth: 3, Weekday: 6, At: "12:00"})
	if len(old.Nths) != 1 || old.Nth != 0 || old.Weekdays[0] != 6 {
		t.Fatalf("not folded: %+v", old)
	}
	if err := (&SchedTask{Name: "x", Action: "stats_clear", Freq: "monthly", Nths: []int{2}}).validate(); err == nil {
		t.Fatal("weekday required")
	}
	wrap := mustTask(t, SchedTask{Freq: "daily", At: "22:00", Until: "02:00"})
	if wrap.WindowSec != 4*3600 {
		t.Fatalf("wrap %d", wrap.WindowSec)
	}
}

func TestSchedEnvHasTheUsualPaths(t *testing.T) {
	env := schedEnv([]string{"PATH=/opt/x/bin", "LANG=C"}, "job")
	m := map[string]string{}
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i > 0 {
			m[e[:i]] = e[i+1:]
		}
	}
	for _, p := range []string{"/opt/x/bin", "/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
		if !strings.Contains(":"+m["PATH"]+":", ":"+p+":") {
			t.Errorf("PATH %q lacks %s", m["PATH"], p)
		}
	}
	if m["HOME"] != "/root" || m["LANG"] != "C" {
		t.Errorf("env %v", m)
	}
	if got := schedEnv(nil, "job"); len(got) < 2 {
		t.Errorf("empty base: %v", got)
	}
}

func TestOverlappingTasksAreStaggered(t *testing.T) {
	s := newScheduler(nil, t.TempDir())
	var l []SchedTask
	for i, n := range []string{"a", "b", "c", "d", "e", "f"} {
		x := mustTask(t, SchedTask{ID: "aa0" + string(rune('0'+i)), Name: n, Action: "stats_clear", Freq: "daily", At: "01:00", Until: "02:00"})
		l = append(l, x)
	}
	fixed := mustTask(t, SchedTask{ID: "aa10", Name: "fixed", Action: "stats_clear", Freq: "daily", At: "01:30"})
	l = append(l, fixed)
	s.SetTasks(l)
	base := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	for _, node := range []string{"ab01", "ab02"} {
		s.mu.Lock()
		var at []int64
		for _, x := range l {
			b := base
			if x.WindowSec == 0 {
				b = base.Add(30 * time.Minute)
			}
			at = append(at, b.Add(s.runOffset(node, x, b)).Unix())
			if again := b.Add(s.runOffset(node, x, b)).Unix(); again != at[len(at)-1] {
				t.Fatal("not stable")
			}
			if o := s.runOffset(node, x, b); x.WindowSec > 0 && (o < 0 || o > time.Duration(x.WindowSec)*time.Second) {
				t.Fatalf("outside the window: %v", o)
			}
		}
		s.mu.Unlock()
		for i := range at {
			for j := i + 1; j < len(at); j++ {
				if d := at[i] - at[j]; d < int64(schedStagger/time.Second) && d > -int64(schedStagger/time.Second) {
					t.Errorf("node %s: tasks %d and %d start %d s apart (under the stagger)", node, i, j, d)
				}
			}
		}
	}
}

func TestSchedulerRunsOneJobAtATime(t *testing.T) {
	a := mustTask(t, SchedTask{ID: "a1", Name: "a", Action: "stats_clear", Freq: "daily", At: "12:00"})
	b := mustTask(t, SchedTask{ID: "a2", Name: "b", Action: "node_pause", Freq: "daily", At: "12:00"})
	h := newHarness(t, a, b)
	release := make(chan struct{})
	h.s.act = func(action, actor string) (string, error) {
		h.mu.Lock()
		h.ran = append(h.ran, action)
		h.mu.Unlock()
		if action == "stats_clear" {
			<-release
		}
		return "done", nil
	}
	h.s.Tick("n1")
	h.at(time.Date(2026, 10, 17, 12, 0, 2, 0, time.UTC))
	h.s.Tick("n1")
	waitFor(t, func() bool { return h.count() == 1 })
	time.Sleep(100 * time.Millisecond)
	if h.count() != 1 {
		t.Fatal("the second job started while the first was running")
	}
	if st := h.s.Status(); !st.Tasks[1].Queued {
		t.Fatalf("the second job should show as waiting: %+v", st.Tasks[1])
	}
	if err := h.s.RunNow("a2", "me"); err == nil {
		t.Fatal("Run now must not start a second job")
	}
	h.s.Tick("n1") // still running: still waits
	time.Sleep(50 * time.Millisecond)
	if h.count() != 1 {
		t.Fatal("started during the first job")
	}
	close(release)
	waitFor(t, func() bool { return h.count() == 2 })
	waitFor(t, func() bool {
		for _, r := range h.s.Status().History {
			if r.Status == "running" {
				return false
			}
		}
		return true
	})
	if h.s.Status().Tasks[1].Queued {
		t.Fatal("still queued after it ran")
	}
}

func TestEachDayRunsOnEveryMatchingDay(t *testing.T) {
	utc := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	task := mustTask(t, SchedTask{Freq: "monthly", Nths: []int{2, 3}, Weekdays: []int{6, 0}, At: "01:00", EachDay: true})
	var got []string
	for _, b := range task.bases(utc(2026, 10, 1), utc(2026, 10, 31).Add(24*time.Hour-time.Second)) {
		got = append(got, b.Format("01-02"))
	}
	if want := "10-10,10-11,10-17,10-18"; strings.Join(got, ",") != want {
		t.Fatalf("got %v want %s", got, want)
	}
	if d := task.describe(); !strings.Contains(d, "each of the 2nd or 3rd Sunday or Saturdays") {
		t.Fatal(d)
	}
	first := mustTask(t, SchedTask{Freq: "monthly", Nths: []int{2, 3}, Weekdays: []int{6, 0}, At: "01:00"})
	if d := first.describe(); !strings.Contains(d, "the first of the 2nd or 3rd") {
		t.Fatal(d)
	}
	plain := mustTask(t, SchedTask{Freq: "daily", At: "01:00", EachDay: true})
	if plain.EachDay {
		t.Fatal("each_day kept on a daily task")
	}
}

func TestOnceRunsOnlyOnItsDate(t *testing.T) {
	utc := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	task := mustTask(t, SchedTask{Freq: "once", Date: "2026-11-14", At: "03:30", Until: "05:00", Every: 1, Days: []int{1}})
	var got []string
	for _, b := range task.bases(utc(2026, 1, 1), utc(2027, 12, 31)) {
		got = append(got, b.Format("2006-01-02 15:04"))
	}
	if strings.Join(got, ",") != "2026-11-14 03:30" {
		t.Fatalf("got %v", got)
	}
	if task.WindowSec != 90*60 || task.Every != 0 || len(task.Days) != 0 {
		t.Fatalf("%+v", task)
	}
	if d := task.describe(); d != "once on 2026-11-14 between 03:30 and 05:00 UTC" {
		t.Fatal(d)
	}
	if n := task.next(utc(2026, 11, 14).Add(4 * time.Hour)); !n.IsZero() {
		t.Fatalf("runs again at %v", n)
	}
	if err := (&SchedTask{Name: "x", Action: "stats_clear", Freq: "once"}).validate(); err == nil {
		t.Fatal("date required")
	}
	if err := (&SchedTask{Name: "x", Action: "stats_clear", Freq: "once", Date: "2026-13-40"}).validate(); err == nil {
		t.Fatal("bad date accepted")
	}
	daily := mustTask(t, SchedTask{Freq: "daily", Date: "2026-11-14"})
	if daily.Date != "" {
		t.Fatal("date kept on a daily task")
	}
}

// An hourly task can have a To minute: the window is from the At minute to it, never more than 59 minutes, and the
// next hour when To is earlier than At.
func TestHourlyToMinute(t *testing.T) {
	a := mustTask(t, SchedTask{Freq: "hourly", At: "00:10:00", Until: "00:40"})
	if a.WindowSec != 30*60 || a.Until != "00:40" {
		t.Fatalf("10 to 40: window %d until %q", a.WindowSec, a.Until)
	}
	b := mustTask(t, SchedTask{Freq: "hourly", At: "00:50:00", Until: "00:05"})
	if b.WindowSec != 15*60 {
		t.Fatalf("50 to 05 wraps to the next hour: window %d", b.WindowSec)
	}
	c := mustTask(t, SchedTask{Freq: "hourly", At: "00:00:00", Until: "07:59"}) // only the minute counts
	if c.WindowSec != 59*60 || c.Until != "00:59" {
		t.Fatalf("59 minutes: window %d until %q", c.WindowSec, c.Until)
	}
	if !strings.Contains(a.describe(), "between minute 10 and minute 40") {
		t.Fatalf("describe: %q", a.describe())
	}
	d := SchedTask{ID: "x", Name: "n", Action: "stats_clear", Freq: "hourly", At: "00:10:00", Until: "00:10"}
	if err := d.validate(); err == nil {
		t.Fatal("To equal to At on an hourly task must be refused (it would be a 60 minute window)")
	}
}

// PickDay: every node runs the task once a month, on one of the matching days, and the nodes do not all pick the same.
func TestPickDayPerNode(t *testing.T) {
	task := mustTask(t, SchedTask{ID: "pk1", Freq: "monthly", Nths: []int{1, 2, 3, 4}, Weekdays: []int{0, 6}, At: "09:00", Until: "12:00", PickDay: true})
	from, to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)
	all := map[string]bool{}
	for _, b := range task.bases(from, to) {
		all[b.Format("2006-01-02")] = true
	}
	days := map[string]bool{}
	for _, node := range []string{"n1", "n2", "n3", "n4", "n5", "n6", "n7", "n8"} {
		perMonth := map[string]int{}
		for _, b := range task.basesFor(node, from, to) {
			if !all[b.Format("2006-01-02")] {
				t.Fatalf("%s: %v is not one of the matching days", node, b)
			}
			perMonth[b.Format("2006-01")]++
			days[b.Format("2006-01-02")] = true
		}
		if len(perMonth) != 6 {
			t.Fatalf("%s: runs in %d months, want 6: %v", node, len(perMonth), perMonth)
		}
		for m, n := range perMonth {
			if n != 1 {
				t.Fatalf("%s: %d runs in %s, want one", node, n, m)
			}
		}
		again := task.basesFor(node, from, to)
		first := task.basesFor(node, from, to)
		if len(again) != len(first) || again[0] != first[0] {
			t.Fatal("the choice is not stable")
		}
	}
	if len(days) < 6 {
		t.Fatalf("8 nodes over 6 months picked only %d different days", len(days))
	}
	if n := task.nextFor("n1", time.Date(2026, 10, 17, 20, 0, 0, 0, time.UTC)); n.IsZero() || !all[n.Format("2006-01-02")] {
		t.Fatalf("nextFor: %v", n)
	}
	if !strings.Contains(task.describe(), "one day per node out of the 1st, 2nd, 3rd or 4th Sunday or Saturdays") {
		t.Fatalf("describe: %q", task.describe())
	}
	both := SchedTask{ID: "x", Name: "n", Action: "stats_clear", Freq: "monthly", Nths: []int{1}, Weekdays: []int{0, 6}, EachDay: true, PickDay: true}
	if err := both.validate(); err == nil {
		t.Fatal("each_day and pick_day together must be refused")
	}
	daily := mustTask(t, SchedTask{Freq: "daily", PickDay: true})
	if daily.PickDay {
		t.Fatal("pick_day kept on a daily task")
	}
}

// With auto-update on, the online update task is for one node of a cluster; alone, or with auto-update off, it is not asked.
func TestOnlineUpdateNeedsOneNode(t *testing.T) {
	every := SchedTask{Action: "update_online"}
	one := SchedTask{Action: "update_online", Nodes: []string{"a"}}
	two := SchedTask{Action: "update_online", Nodes: []string{"a", "b"}}
	other := SchedTask{Action: "stats_clear"}
	for _, c := range []struct {
		t       SchedTask
		auto    bool
		cluster bool
		want    bool
	}{
		{every, true, true, true}, {two, true, true, true}, {one, true, true, false},
		{every, false, true, false}, {every, true, false, false}, {other, true, true, false},
	} {
		if got := onlineUpdateNeedsOneNode(c.t, c.auto, c.cluster); got != c.want {
			t.Errorf("%+v auto=%v cluster=%v: got %v", c.t, c.auto, c.cluster, got)
		}
	}
	mg := newPowerMgmt(t)
	if st := mg.sched.Status(); st.AutoUpdate != mg.upd.Intent().AutoAll {
		t.Errorf("status says auto-update %v, the intent %v", st.AutoUpdate, mg.upd.Intent().AutoAll)
	}
}

func TestMonthsLimitATask(t *testing.T) {
	task := mustTask(t, SchedTask{ID: "mo1", Freq: "monthly", Nths: []int{2, 3}, Weekdays: []int{0, 6}, At: "01:00", Until: "06:00", EachDay: true, Months: []int{10, 4, 4, 5, 6, 7, 8, 9}})
	if want := []int{4, 5, 6, 7, 8, 9, 10}; fmt.Sprint(task.Months) != fmt.Sprint(want) {
		t.Fatalf("months %v, want %v", task.Months, want)
	}
	if d := task.describe(); !strings.Contains(d, "April to October") {
		t.Fatalf("describe %q lacks the months", d)
	}
	n := 0
	for _, b := range task.bases(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		if m := b.Month(); m < 4 || m > 10 {
			t.Fatalf("runs in %v", b)
		}
		n++
	}
	if n != 7*4 {
		t.Fatalf("%d runs, want 28 (4 days a month for 7 months)", n)
	}
	all := mustTask(t, SchedTask{ID: "mo2", Freq: "daily", At: "01:00", Months: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}})
	if all.Months != nil {
		t.Fatalf("all twelve should clear: %v", all.Months)
	}
	if one := mustTask(t, SchedTask{ID: "mo3", Freq: "hourly", At: "00:05", Months: []int{3}}); one.Months != nil {
		t.Fatalf("hourly keeps months: %v", one.Months)
	}
	bad := SchedTask{ID: "mo4", Freq: "daily", At: "01:00", Months: []int{13}}
	if bad.validate() == nil {
		t.Fatal("month 13 accepted")
	}
}
