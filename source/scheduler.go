package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Timing model — like cron, not "every N hours":
//   hourly  → minute of each hour       daily   → HH:MM
//   weekly  → weekday + HH:MM           monthly → day of month + HH:MM
//   once    → a specific local date and time
// After every run, next_run is recomputed from that spec in the server's local
// time zone, so DST changes and month lengths cannot make it drift.

const isoLayout = "2006-01-02T15:04:05Z"

// Schedule is a saved benchmark plus when to run it.
type Schedule struct {
	ID          string
	Name        string
	Server      string
	Protocol    string
	Concurrency string // blank = auto
	Pipeline    string // blank = auto
	Duration    string
	Queries     string
	Qtype       string
	Recurse     bool

	Freq     string // hourly | daily | weekly | monthly | once
	Minute   int    // 0-59
	Hour     int    // 0-23
	Weekday  int    // 0=Sun … 6=Sat
	Monthday int    // 1-31 (capped to the month's length)

	Paused    bool
	NextRun   time.Time
	LastRun   *time.Time
	LastOK    *bool
	LastJobID *string
}

// scheduleJSON is the on-disk and API shape (unchanged from earlier versions).
type scheduleJSON struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Server      string  `json:"server"`
	Protocol    string  `json:"protocol"`
	Concurrency string  `json:"concurrency"`
	Pipeline    string  `json:"pipeline"`
	Duration    string  `json:"duration"`
	Queries     string  `json:"queries"`
	Qtype       string  `json:"qtype"`
	Recurse     bool    `json:"recurse"`
	Freq        string  `json:"freq"`
	Minute      int     `json:"minute"`
	Hour        int     `json:"hour"`
	Weekday     int     `json:"weekday"`
	Monthday    int     `json:"monthday"`
	Paused      bool    `json:"paused"`
	NextRun     string  `json:"next_run"`
	LastRun     *string `json:"last_run"`
	LastOK      *bool   `json:"last_ok"`
	LastJobID   *string `json:"last_job_id"`
}

func (s Schedule) MarshalJSON() ([]byte, error) {
	w := scheduleJSON{
		ID: s.ID, Name: s.Name, Server: s.Server, Protocol: s.Protocol,
		Concurrency: s.Concurrency, Pipeline: s.Pipeline, Duration: s.Duration,
		Queries: s.Queries, Qtype: s.Qtype, Recurse: s.Recurse,
		Freq: s.Freq, Minute: s.Minute, Hour: s.Hour, Weekday: s.Weekday, Monthday: s.Monthday,
		Paused: s.Paused, NextRun: s.NextRun.UTC().Format(isoLayout),
		LastOK: s.LastOK, LastJobID: s.LastJobID,
	}
	if s.LastRun != nil {
		v := s.LastRun.UTC().Format(isoLayout)
		w.LastRun = &v
	}
	return json.Marshal(w)
}

func (s *Schedule) UnmarshalJSON(b []byte) error {
	w := scheduleJSON{ // defaults for keys missing from older files
		Server: "127.0.0.1", Protocol: "udp", Concurrency: "10", Pipeline: "8",
		Duration: "30s", Qtype: "ANY", Recurse: true,
		Freq: "daily", Hour: 2, Weekday: 1, Monthday: 1,
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*s = Schedule{
		ID: w.ID, Name: w.Name, Server: w.Server, Protocol: w.Protocol,
		Concurrency: w.Concurrency, Pipeline: w.Pipeline, Duration: w.Duration,
		Queries: w.Queries, Qtype: w.Qtype, Recurse: w.Recurse,
		Freq: w.Freq, Minute: w.Minute, Hour: w.Hour, Weekday: w.Weekday, Monthday: w.Monthday,
		Paused: w.Paused, LastOK: w.LastOK, LastJobID: w.LastJobID,
	}
	if t, err := time.Parse(isoLayout, w.NextRun); err == nil {
		s.NextRun = t
	}
	if w.LastRun != nil {
		if t, err := time.Parse(isoLayout, *w.LastRun); err == nil {
			s.LastRun = &t
		}
	}
	return nil
}

// ── Next-run computation ─────────────────────────────────────────────────────

func daysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// nextRunAfter returns the first time strictly after now at which s fires, in
// the server's local time zone. For "once" (and unknown) frequencies it returns
// an hour from now; callers set one-shot times directly.
func nextRunAfter(now time.Time, s *Schedule) time.Time {
	loc := time.Local
	n := now.In(loc)
	y, mo, d := n.Date()
	switch s.Freq {
	case "hourly":
		// Candidates come from two directions: wall-clock times for the
		// surrounding local hours (right across a skipped hour or a
		// half-hour shift) and whole absolute hours from this one (right when
		// a local hour repeats). Take the earliest match strictly after now.
		var best time.Time
		consider := func(c time.Time) {
			if c.Minute() == s.Minute && c.After(n) && (best.IsZero() || c.Before(best)) {
				best = c
			}
		}
		base := time.Date(y, mo, d, n.Hour(), s.Minute, 0, 0, loc)
		for k := -1; k <= 3; k++ {
			consider(time.Date(y, mo, d, n.Hour()+k, s.Minute, 0, 0, loc))
			consider(base.Add(time.Duration(k) * time.Hour))
		}
		if best.IsZero() {
			best = base.Add(3 * time.Hour)
		}
		return best
	case "daily":
		c := time.Date(y, mo, d, s.Hour, s.Minute, 0, 0, loc)
		if !c.After(n) {
			c = time.Date(y, mo, d+1, s.Hour, s.Minute, 0, 0, loc)
		}
		return c
	case "weekly":
		ahead := (s.Weekday - int(n.Weekday()) + 7) % 7
		c := time.Date(y, mo, d+ahead, s.Hour, s.Minute, 0, 0, loc)
		if !c.After(n) {
			c = time.Date(y, mo, d+ahead+7, s.Hour, s.Minute, 0, 0, loc)
		}
		return c
	case "monthly":
		at := func(year int, month time.Month) time.Time {
			// time.Date normalises month overflow (13 → January next year).
			ref := time.Date(year, month, 1, 0, 0, 0, 0, loc)
			day := s.Monthday
			if dm := daysIn(ref.Year(), ref.Month()); day > dm {
				day = dm
			}
			return time.Date(ref.Year(), ref.Month(), day, s.Hour, s.Minute, 0, 0, loc)
		}
		c := at(y, mo)
		if !c.After(n) {
			c = at(y, mo+1)
		}
		return c
	}
	return now.Add(time.Hour)
}

// parseLocalTime reads "YYYY-MM-DDTHH:MM[:SS]" as server-local time.
func parseLocalTime(s string) (time.Time, bool) {
	s = strings.TrimSuffix(strings.TrimSpace(s), "Z")
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func (s *Schedule) validate() error {
	switch s.Freq {
	case "hourly", "daily", "weekly", "monthly", "once":
	default:
		return fmt.Errorf("unknown frequency %q", s.Freq)
	}
	if s.Minute < 0 || s.Minute > 59 || s.Hour < 0 || s.Hour > 23 ||
		s.Weekday < 0 || s.Weekday > 6 || s.Monthday < 1 || s.Monthday > 31 {
		return fmt.Errorf("time of day out of range")
	}
	return nil
}

// ── Store ────────────────────────────────────────────────────────────────────

type scheduleStore struct {
	mu   sync.Mutex
	m    map[string]*Schedule
	file string
}

func newScheduleStore(file string) *scheduleStore {
	return &scheduleStore{m: map[string]*Schedule{}, file: file}
}

func (st *scheduleStore) load() {
	data, err := os.ReadFile(st.file)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("schedules: %s not present yet (first run)", st.file)
		} else {
			log.Printf("schedules: cannot read %s: %v", st.file, err)
		}
		return
	}
	var list []Schedule
	if err := json.Unmarshal(data, &list); err != nil {
		log.Printf("schedules: %s is not valid JSON: %v", st.file, err)
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	for i := range list {
		s := list[i]
		if s.ID == "" {
			continue
		}
		if s.NextRun.IsZero() {
			s.NextRun = nextRunAfter(now, &s)
		}
		st.m[s.ID] = &s
	}
	log.Printf("schedules: loaded %d from %s", len(st.m), st.file)
}

// saveLocked writes the file; st.mu must be held.
func (st *scheduleStore) saveLocked() {
	list := make([]Schedule, 0, len(st.m))
	for _, s := range st.m {
		list = append(list, *s)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	data, err := json.Marshal(list)
	if err != nil {
		log.Printf("schedules: cannot encode: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(st.file), 0o755); err != nil {
		log.Printf("schedules: cannot create %s: %v", filepath.Dir(st.file), err)
		return
	}
	if err := writeFileAtomic(st.file, data, 0o644); err != nil {
		log.Printf("schedules: cannot write %s: %v", st.file, err)
	}
}

func (st *scheduleStore) list() []Schedule {
	st.mu.Lock()
	defer st.mu.Unlock()
	list := make([]Schedule, 0, len(st.m))
	for _, s := range st.m {
		list = append(list, *s)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Name != list[j].Name {
			return list[i].Name < list[j].Name
		}
		return list[i].ID < list[j].ID
	})
	return list
}

func (st *scheduleStore) get(id string) (Schedule, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.m[id]
	if !ok {
		return Schedule{}, false
	}
	return *s, true
}

// update applies fn to the schedule with the given id and saves. It reports
// whether the id exists.
func (st *scheduleStore) update(id string, fn func(*Schedule)) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.m[id]
	if !ok {
		return false
	}
	fn(s)
	st.saveLocked()
	return true
}

func (st *scheduleStore) add(s Schedule) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.m[s.ID] = &s
	st.saveLocked()
}

func (st *scheduleStore) remove(id string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.m[id]; !ok {
		return false
	}
	delete(st.m, id)
	st.saveLocked()
	return true
}

// dueNow returns the schedules that should fire now and advances their
// next_run in the same critical section, so the next tick cannot fire them
// again while the benchmark is still running. One-shot schedules are left
// alone; they are removed when their run completes.
func (st *scheduleStore) dueNow(now time.Time) []Schedule {
	st.mu.Lock()
	defer st.mu.Unlock()
	var due []Schedule
	for _, s := range st.m {
		if s.Paused || s.NextRun.After(now) {
			continue
		}
		due = append(due, *s)
		if s.Freq != "once" {
			s.NextRun = nextRunAfter(now, s)
		} else {
			s.NextRun = now.AddDate(100, 0, 0) // fired; removed on completion
		}
	}
	if len(due) > 0 {
		st.saveLocked()
	}
	return due
}

// ── Running schedules ────────────────────────────────────────────────────────

// histRun is one completed scheduled run, as served to the browser.
type histRun struct {
	JobID        string            `json:"job_id"`
	ScheduleID   string            `json:"schedule_id"`
	ScheduleName string            `json:"schedule_name"`
	Args         map[string]string `json:"args"`
	Started      string            `json:"started"`
	Status       string            `json:"status"`
	Lines        []string          `json:"lines"`
}

const historyCap = 500

func (a *App) schedulerLoop() {
	for {
		time.Sleep(30 * time.Second)
		for _, s := range a.schedules.dueNow(time.Now()) {
			a.runSchedule(s)
		}
	}
}

// runSchedule starts s now, records the result when it completes, and returns
// the job id. It is used by the timer loop and the "Run now" button alike.
func (a *App) runSchedule(s Schedule) string {
	args := map[string]string{
		"server": s.Server, "protocol": s.Protocol,
		"concurrency": s.Concurrency, "pipeline": s.Pipeline,
		"duration": s.Duration, "queries": s.Queries, "qtype": s.Qtype,
		"recurse": strconv.FormatBool(s.Recurse),
	}
	job := a.startJob(args, "scheduler:"+s.ID)
	started := nowISO()

	go func() {
		<-job.done
		lines := job.allLines()
		_, status, _ := job.snapshot(len(lines))

		// Record what "auto" resolved to rather than the blank saved value.
		shown := map[string]string{}
		for k, v := range args {
			shown[k] = v
		}
		if p, _, err := parseParams(args, cpuThreads()); err == nil {
			shown["concurrency"] = strconv.Itoa(p.workers)
			if p.protocol == "udp" {
				shown["pipeline"] = strconv.Itoa(p.window)
			}
		}
		a.histMu.Lock()
		a.history = append([]histRun{{
			JobID: job.ID, ScheduleID: s.ID, ScheduleName: s.Name, Args: shown,
			Started: started, Status: status, Lines: lines,
		}}, a.history...)
		if len(a.history) > historyCap {
			a.history = a.history[:historyCap]
		}
		a.histMu.Unlock()

		now := time.Now()
		ok := status == "done"
		if s.Freq == "once" {
			a.schedules.remove(s.ID)
		} else {
			a.schedules.update(s.ID, func(cur *Schedule) {
				cur.LastRun = &now
				cur.LastOK = &ok
				id := job.ID
				cur.LastJobID = &id
				cur.NextRun = nextRunAfter(now, cur)
			})
		}
		log.Printf("scheduler: %s -> job %s (%s)", s.ID, job.ID, status)
	}()
	return job.ID
}
