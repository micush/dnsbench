package main

import (
	"context"
	"runtime"
	"sync"
	"time"
)

// Job is one benchmark run. Output lines are appended as the run proceeds and
// are streamed to clients from index 0, so late subscribers see everything.
type Job struct {
	ID        string
	User      string
	Args      map[string]string
	StartedAt string

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when runJob returns

	mu       sync.Mutex
	status   string // pending | running | done | error | killed
	exitCode *int
	lines    []string
}

func newJob(id, user string, args map[string]string) *Job {
	ctx, cancel := context.WithCancel(context.Background())
	return &Job{
		ID: id, User: user, Args: args,
		StartedAt: nowISO(),
		ctx:       ctx, cancel: cancel,
		done:   make(chan struct{}),
		status: "pending",
	}
}

func (j *Job) emit(line string) {
	j.mu.Lock()
	j.lines = append(j.lines, line)
	j.mu.Unlock()
}

func (j *Job) setStatus(s string) {
	j.mu.Lock()
	if j.status == "pending" {
		j.status = s
	}
	j.mu.Unlock()
}

func (j *Job) statusIs(s string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status == s
}

// finish records the terminal state unless the job was killed first.
// A negative exit code means "none".
func (j *Job) finish(status string, exit int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status == "killed" {
		status = "killed"
	}
	j.status = status
	if exit >= 0 {
		j.exitCode = &exit
	}
}

// kill stops the run and marks it killed.
func (j *Job) kill() {
	j.mu.Lock()
	j.status = "killed"
	j.mu.Unlock()
	j.cancel()
}

func (j *Job) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status == "pending" || j.status == "running"
}

// snapshot returns the lines from index from onward plus the current state.
func (j *Job) snapshot(from int) (lines []string, status string, exit *int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if from < len(j.lines) {
		lines = append(lines, j.lines[from:]...)
	}
	status = j.status
	if j.exitCode != nil {
		v := *j.exitCode
		exit = &v
	}
	return
}

func (j *Job) allLines() []string {
	lines, _, _ := j.snapshot(0)
	return lines
}

func cpuThreads() int { return runtime.NumCPU() }

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }
