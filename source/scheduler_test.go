package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withLocal(t *testing.T, loc *time.Location) {
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
}

func at(loc *time.Location, y int, m time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, m, d, h, mi, s, 0, loc)
}

func TestNextRunDailyHourly(t *testing.T) {
	loc := time.FixedZone("T", -7*3600)
	withLocal(t, loc)
	daily := &Schedule{Freq: "daily", Hour: 14, Minute: 30}
	if got, want := nextRunAfter(at(loc, 2026, 3, 10, 14, 29, 59), daily), at(loc, 2026, 3, 10, 14, 30, 0); !got.Equal(want) {
		t.Errorf("before: %v want %v", got, want)
	}
	if got, want := nextRunAfter(at(loc, 2026, 3, 10, 14, 30, 0), daily), at(loc, 2026, 3, 11, 14, 30, 0); !got.Equal(want) {
		t.Errorf("exactly at: %v want %v", got, want)
	}
	hourly := &Schedule{Freq: "hourly", Minute: 15}
	if got, want := nextRunAfter(at(loc, 2026, 3, 10, 10, 10, 0), hourly), at(loc, 2026, 3, 10, 10, 15, 0); !got.Equal(want) {
		t.Errorf("hourly before: %v want %v", got, want)
	}
	if got, want := nextRunAfter(at(loc, 2026, 3, 10, 10, 15, 0), hourly), at(loc, 2026, 3, 10, 11, 15, 0); !got.Equal(want) {
		t.Errorf("hourly after: %v want %v", got, want)
	}
	// "14:30 daily" means local 14:30, not 14:30 UTC.
	if got := nextRunAfter(at(loc, 2026, 3, 10, 9, 0, 0), daily); got.UTC().Hour() != 21 || got.UTC().Minute() != 30 {
		t.Errorf("not local time: %v", got.UTC())
	}
}

func TestNextRunWeekly(t *testing.T) {
	loc := time.FixedZone("T", 5*3600+1800)
	withLocal(t, loc)
	wed := &Schedule{Freq: "weekly", Weekday: 3, Hour: 9}
	// 2026-03-10 is a Tuesday.
	if got, want := nextRunAfter(at(loc, 2026, 3, 10, 12, 0, 0), wed), at(loc, 2026, 3, 11, 9, 0, 0); !got.Equal(want) {
		t.Errorf("tue: %v want %v", got, want)
	}
	if got, want := nextRunAfter(at(loc, 2026, 3, 11, 8, 0, 0), wed), at(loc, 2026, 3, 11, 9, 0, 0); !got.Equal(want) {
		t.Errorf("wed early: %v want %v", got, want)
	}
	if got, want := nextRunAfter(at(loc, 2026, 3, 11, 9, 0, 0), wed), at(loc, 2026, 3, 18, 9, 0, 0); !got.Equal(want) {
		t.Errorf("wed exactly: %v want %v", got, want)
	}
	sun := &Schedule{Freq: "weekly", Weekday: 0, Hour: 1}
	if got := nextRunAfter(at(loc, 2026, 3, 11, 9, 0, 0), sun); got.In(loc).Weekday() != time.Sunday {
		t.Errorf("sunday: %v", got)
	}
}

func TestNextRunMonthlyClampsToMonthLength(t *testing.T) {
	loc := time.UTC
	withLocal(t, loc)
	m31 := &Schedule{Freq: "monthly", Monthday: 31, Hour: 6}
	cases := []struct{ now, want time.Time }{
		{at(loc, 2026, 1, 15, 0, 0, 0), at(loc, 2026, 1, 31, 6, 0, 0)},
		{at(loc, 2026, 1, 31, 12, 0, 0), at(loc, 2026, 2, 28, 6, 0, 0)}, // 2026 is not a leap year
		{at(loc, 2028, 1, 31, 12, 0, 0), at(loc, 2028, 2, 29, 6, 0, 0)}, // 2028 is
		{at(loc, 2026, 12, 31, 12, 0, 0), at(loc, 2027, 1, 31, 6, 0, 0)},
		{at(loc, 2026, 4, 30, 5, 0, 0), at(loc, 2026, 4, 30, 6, 0, 0)}, // April has 30 days
	}
	for _, c := range cases {
		if got := nextRunAfter(c.now, m31); !got.Equal(c.want) {
			t.Errorf("from %v: %v want %v", c.now, got, c.want)
		}
	}
}

func TestNextRunAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database")
	}
	withLocal(t, loc)
	daily := &Schedule{Freq: "daily", Hour: 2, Minute: 30}
	// 2026-03-08 is the spring-forward day: 02:30 does not exist that day.
	now := at(loc, 2026, 3, 7, 3, 0, 0)
	for i := 0; i < 5; i++ {
		next := nextRunAfter(now, daily)
		if !next.After(now) || next.Sub(now) > 26*time.Hour {
			t.Fatalf("run %d: now %v next %v", i, now, next)
		}
		now = next
	}
	// Local wall-clock time is kept across the change (never drifts by an hour).
	hourly := &Schedule{Freq: "hourly", Minute: 0}
	n := nextRunAfter(at(loc, 2026, 11, 1, 0, 30, 0), hourly) // fall-back day
	for i := 0; i < 5; i++ {
		nn := nextRunAfter(n, hourly)
		if nn.Sub(n) != time.Hour {
			t.Fatalf("hourly gap %v between %v and %v", nn.Sub(n), n, nn)
		}
		n = nn
	}
}

// What an earlier release wrote to schedules.json.
const legacySchedules = `[{"id":"abc","name":"nightly","server":"10.0.0.1","protocol":"udp","concurrency":"","pipeline":"","duration":"30s","queries":"a.com\nb.com","qtype":"A","recurse":true,"freq":"daily","minute":30,"hour":2,"weekday":1,"monthday":1,"paused":false,"next_run":"2026-03-11T09:30:00Z","last_run":"2026-03-10T09:30:00Z","last_ok":null,"last_job_id":"j1"},{"id":"once1","name":"","server":"x","freq":"once","next_run":"2030-01-01T00:00:00Z"}]`

func TestScheduleFileCompatibleWithEarlierReleases(t *testing.T) {
	file := filepath.Join(t.TempDir(), "schedules.json")
	os.WriteFile(file, []byte(legacySchedules), 0o644)
	st := newScheduleStore(file)
	st.load()
	list := st.list()
	if len(list) != 2 {
		t.Fatalf("loaded %d", len(list))
	}
	s, _ := st.get("abc")
	if s.Name != "nightly" || s.Concurrency != "" || s.Minute != 30 || s.Hour != 2 || s.LastJobID == nil || *s.LastJobID != "j1" ||
		s.LastRun == nil || !s.NextRun.Equal(time.Date(2026, 3, 11, 9, 30, 0, 0, time.UTC)) || !s.Recurse || s.Queries != "a.com\nb.com" {
		t.Fatalf("bad parse: %+v", s)
	}
	o, _ := st.get("once1")
	if o.Protocol != "udp" || o.Duration != "30s" || o.Qtype != "ANY" || !o.Recurse {
		t.Fatalf("defaults not applied: %+v", o)
	}
	// Saving writes the same keys back.
	st.mu.Lock()
	st.saveLocked()
	st.mu.Unlock()
	data, _ := os.ReadFile(file)
	var back []map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "name", "server", "protocol", "concurrency", "pipeline", "duration", "queries", "qtype", "recurse",
		"freq", "minute", "hour", "weekday", "monthday", "paused", "next_run", "last_run", "last_ok", "last_job_id"} {
		if _, ok := back[0][key]; !ok {
			t.Errorf("key %q missing from saved JSON", key)
		}
	}
	if back[0]["next_run"] != "2026-03-11T09:30:00Z" {
		t.Errorf("next_run = %v", back[0]["next_run"])
	}
}

func TestMissingNextRunIsComputedOnLoad(t *testing.T) {
	file := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(file, []byte(`[{"id":"x","server":"s","freq":"hourly","minute":5}]`), 0o644)
	st := newScheduleStore(file)
	st.load()
	s, _ := st.get("x")
	if !s.NextRun.After(time.Now()) || s.NextRun.Sub(time.Now()) > time.Hour+time.Minute {
		t.Fatalf("next run %v", s.NextRun)
	}
}

func TestCorruptScheduleFileIsIgnored(t *testing.T) {
	file := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(file, []byte(`{not json`), 0o644)
	st := newScheduleStore(file)
	st.load()
	if len(st.list()) != 0 {
		t.Fatal("loaded something from garbage")
	}
}

func TestDueNowAdvancesAndOnceFiresOnce(t *testing.T) {
	st := newScheduleStore(filepath.Join(t.TempDir(), "s.json"))
	now := time.Now()
	st.add(Schedule{ID: "d", Freq: "daily", Hour: 3, NextRun: now.Add(-time.Minute)})
	st.add(Schedule{ID: "o", Freq: "once", NextRun: now.Add(-time.Minute)})
	st.add(Schedule{ID: "p", Freq: "daily", Paused: true, NextRun: now.Add(-time.Minute)})
	st.add(Schedule{ID: "f", Freq: "daily", NextRun: now.Add(time.Hour)})
	due := st.dueNow(now)
	if len(due) != 2 {
		t.Fatalf("due %d, want 2", len(due))
	}
	if again := st.dueNow(now.Add(time.Second)); len(again) != 0 {
		t.Fatalf("fired again while still running: %d", len(again))
	}
	d, _ := st.get("d")
	if !d.NextRun.After(now) {
		t.Fatal("daily schedule not advanced")
	}
}

func TestParseLocalTime(t *testing.T) {
	loc := time.FixedZone("T", -3600)
	withLocal(t, loc)
	for _, in := range []string{"2026-05-06T07:08", "2026-05-06T07:08:00", "2026-05-06T07:08Z"} {
		got, ok := parseLocalTime(in)
		if !ok || !got.Equal(time.Date(2026, 5, 6, 7, 8, 0, 0, loc)) {
			t.Errorf("%q -> %v %v", in, got, ok)
		}
	}
	if _, ok := parseLocalTime("tomorrow"); ok {
		t.Error("garbage accepted")
	}
}

func TestScheduleValidate(t *testing.T) {
	good := Schedule{Freq: "weekly", Minute: 59, Hour: 23, Weekday: 6, Monthday: 31}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Schedule{
		{Freq: "yearly", Monthday: 1}, {Freq: "daily", Hour: 24, Monthday: 1}, {Freq: "daily", Minute: -1, Monthday: 1},
		{Freq: "weekly", Weekday: 7, Monthday: 1}, {Freq: "monthly", Monthday: 0},
	} {
		if err := bad.validate(); err == nil || strings.TrimSpace(err.Error()) == "" {
			t.Errorf("%+v accepted", bad)
		}
	}
}

// Whatever the schedule and whatever the clock does, the next run must be
// strictly in the future; otherwise the scheduler could fire in a tight loop.
func TestNextRunIsAlwaysStrictlyAfterNow(t *testing.T) {
	zones := []string{"UTC", "America/New_York", "Europe/London", "Australia/Lord_Howe", "Asia/Kolkata", "America/Sao_Paulo"}
	for _, z := range zones {
		loc, err := time.LoadLocation(z)
		if err != nil {
			t.Skip("no tz database")
		}
		withLocal(t, loc)
		scheds := []*Schedule{
			{Freq: "hourly", Minute: 0}, {Freq: "hourly", Minute: 30}, {Freq: "daily", Hour: 1, Minute: 30},
			{Freq: "daily", Hour: 2, Minute: 30}, {Freq: "weekly", Weekday: 0, Hour: 2, Minute: 15},
			{Freq: "monthly", Monthday: 31, Hour: 2, Minute: 30},
		}
		for _, s := range scheds {
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for i := 0; i < 24*400; i++ { // every 15 minutes for ~ a year
				now = now.Add(15 * time.Minute)
				next := nextRunAfter(now, s)
				if !next.After(now) {
					t.Fatalf("%s %+v: next %v is not after %v", z, s, next, now)
				}
				limit := 32 * 24 * time.Hour
				if s.Freq == "hourly" {
					limit = 2 * time.Hour
				}
				if next.Sub(now) > limit {
					t.Fatalf("%s %+v: next %v is too far from %v", z, s, next, now)
				}
			}
		}
	}
}
