package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The daemon's log goes to stderr (journald under systemd) and, so that the GUI and
// `ddgw --log` can show and filter all of it, also to a size-rotated file in the state
// directory: ddgw.log, plus ddgw.log.1 and ddgw.log.2 (the two previous files).

const (
	logFileName    = "ddgw.log"
	logFileMax     = 2 << 20 // rotate when the current file reaches this size
	logFileKeep    = 2       // rotated files kept
	logReadMax     = 5000    // most lines one GUI request returns
	logReadDefault = 1000
)

// rotatingFile is an io.Writer that appends to path and rotates it by size.
type rotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func openRotatingFile(path string, max int64, keep int) (*rotatingFile, error) {
	r := &rotatingFile{path: path, max: max, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return len(p), nil
	}
	if r.size+int64(len(p)) > r.max && r.size > 0 {
		r.f.Close()
		for i := r.keep; i >= 1; i-- {
			from := r.path
			if i > 1 {
				from = fmt.Sprintf("%s.%d", r.path, i-1)
			}
			_ = os.Rename(from, fmt.Sprintf("%s.%d", r.path, i))
		}
		if err := r.open(); err != nil {
			r.f = nil
			return len(p), nil // logging must never break the daemon
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	if err != nil {
		return len(p), nil
	}
	return n, nil
}

// logDir is where ReadLog looks; set by startLogFile.
var logDir string

// startLogFile makes the default logger write to stderr and to the log file in dir.
// Without a writable dir it leaves logging on stderr only.
func startLogFile(dir string) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		warnf("log file: cannot create %s (%v); logging to stderr only", dir, err)
		return
	}
	rf, err := openRotatingFile(filepath.Join(dir, logFileName), logFileMax, logFileKeep)
	if err != nil {
		warnf("log file: cannot open it in %s (%v); logging to stderr only", dir, err)
		return
	}
	logDir = dir
	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, rf), &slog.HandlerOptions{Level: logLevel})))
}

// LogQuery selects log lines.  Zero values mean "no filter"; Limit 0 means all.
type LogQuery struct {
	Level string        // minimum level: debug, info, warn, error
	Text  string        // every word must appear (case-insensitive)
	Since time.Duration // only lines newer than now-Since
	Limit int           // newest N matching lines
}

// LogLine is one parsed log line.
type LogLine struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
	Raw   string `json:"raw"`
}

// LogResult is the answer to a query.
type LogResult struct {
	Lines     []LogLine `json:"lines"`
	Matched   int       `json:"matched"`   // lines that matched before Limit cut them
	Truncated bool      `json:"truncated"` // Limit cut older matching lines
	Scanned   int       `json:"scanned"`   // lines read in all log files
	Oldest    string    `json:"oldest"`    // time of the oldest line kept on disk
	File      string    `json:"file"`
}

var logLineRe = regexp.MustCompile(`^time=(\S+) level=(\w+) msg=(.*)$`)

func levelRank(l string) int {
	switch strings.ToUpper(l) {
	case "DEBUG":
		return 0
	case "WARN", "WARNING":
		return 2
	case "ERROR":
		return 3
	}
	return 1
}

// parseLogLine splits a slog text line; a line that is not in that format (a panic
// trace, say) is kept as an INFO-level line of its own.
func parseLogLine(raw string) LogLine {
	m := logLineRe.FindStringSubmatch(raw)
	if m == nil {
		return LogLine{Level: "INFO", Msg: raw, Raw: raw}
	}
	msg := m[3]
	if u, err := strconv.Unquote(msg); err == nil {
		msg = u
	}
	return LogLine{Time: m[1], Level: strings.ToUpper(m[2]), Msg: msg, Raw: raw}
}

// ReadLog returns the lines of the log files that match q, oldest first.
func ReadLog(q LogQuery) (*LogResult, error) {
	if logDir == "" {
		return nil, fmt.Errorf("the log file is not available on this node (logging to stderr only)")
	}
	base := filepath.Join(logDir, logFileName)
	res := &LogResult{Lines: []LogLine{}, File: base}
	words := strings.Fields(strings.ToLower(q.Text))
	min := 0 // no level given: every line, debug included
	if q.Level != "" {
		min = levelRank(q.Level)
	}
	var cutoff time.Time
	if q.Since > 0 {
		cutoff = time.Now().Add(-q.Since)
	}
	var keep []LogLine
	paths := []string{}
	for i := logFileKeep; i >= 1; i-- {
		paths = append(paths, fmt.Sprintf("%s.%d", base, i))
	}
	paths = append(paths, base)
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			raw := sc.Text()
			if raw == "" {
				continue
			}
			res.Scanned++
			ln := parseLogLine(raw)
			if res.Oldest == "" && ln.Time != "" {
				res.Oldest = ln.Time
			}
			if levelRank(ln.Level) < min {
				continue
			}
			if !cutoff.IsZero() {
				if t, err := time.Parse(time.RFC3339Nano, ln.Time); err == nil && t.Before(cutoff) {
					continue
				}
			}
			if len(words) > 0 {
				low := strings.ToLower(ln.Raw)
				ok := true
				for _, w := range words {
					if !strings.Contains(low, w) {
						ok = false
						break
					}
				}
				if !ok {
					continue
				}
			}
			res.Matched++
			keep = append(keep, ln)
			if q.Limit > 0 && len(keep) > q.Limit {
				keep = keep[1:]
				res.Truncated = true
			}
		}
		f.Close()
	}
	if keep != nil {
		res.Lines = keep
	}
	return res, nil
}

// parseSince reads "15m", "6h", "2d" or plain seconds; empty or "all" is no limit.
func parseSince(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "all" || s == "0" {
		return 0, nil
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return time.Duration(n) * time.Second, nil
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is not a time span: use 15m, 6h, 2d or seconds", s)
	}
	return d, nil
}

// logQueryFromArgs builds a query from the string arguments of the API and CLI.
func logQueryFromArgs(level, text, since, n string) (LogQuery, error) {
	q := LogQuery{Level: level, Text: text}
	d, err := parseSince(since)
	if err != nil {
		return q, err
	}
	q.Since = d
	if n == "" {
		q.Limit = logReadDefault
	} else {
		v, err := strconv.Atoi(n)
		if err != nil || v < 0 {
			return q, fmt.Errorf("%q is not a line count", n)
		}
		q.Limit = v
	}
	return q, nil
}
