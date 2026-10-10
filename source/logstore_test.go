package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotatingFile(t *testing.T) {
	dir := t.TempDir()
	rf, err := openRotatingFile(filepath.Join(dir, "x.log"), 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		fmt.Fprintf(rf, "line %02d %s\n", i, strings.Repeat("z", 20))
	}
	for _, n := range []string{"x.log", "x.log.1", "x.log.2"} {
		if st, err := os.Stat(filepath.Join(dir, n)); err != nil || st.Size() > 100 {
			t.Fatalf("%s: %v %v", n, st, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x.log.3")); err == nil {
		t.Fatal("kept more rotated files than asked")
	}
}

func TestParseLogLine(t *testing.T) {
	l := parseLogLine(`time=2026-10-01T15:48:26.431-07:00 level=WARN msg="dns: server 1.2.3.4:53 is DOWN: x"`)
	if l.Level != "WARN" || l.Msg != "dns: server 1.2.3.4:53 is DOWN: x" || l.Time != "2026-10-01T15:48:26.431-07:00" {
		t.Fatalf("%+v", l)
	}
	if l := parseLogLine("panic: boom"); l.Level != "INFO" || l.Msg != "panic: boom" {
		t.Fatalf("%+v", l)
	}
}

func TestReadLogFilters(t *testing.T) {
	dir := t.TempDir()
	old := logDir
	logDir = dir
	defer func() { logDir = old }()
	now := time.Now()
	ln := func(ago time.Duration, lvl, msg string) string {
		return fmt.Sprintf("time=%s level=%s msg=%q\n", now.Add(-ago).Format(time.RFC3339Nano), lvl, msg)
	}
	// the older file holds the older lines: they must come first
	os.WriteFile(filepath.Join(dir, logFileName+".1"), []byte(ln(3*time.Hour, "INFO", "dns: server A is UP")+ln(2*time.Hour, "WARN", "dns: server A is DOWN")), 0o600)
	os.WriteFile(filepath.Join(dir, logFileName), []byte(ln(10*time.Minute, "DEBUG", "probe of A failed")+ln(5*time.Minute, "INFO", "dns: server A is UP")+ln(time.Minute, "ERROR", "web: boom")+"stray continuation\n"), 0o600)

	r, err := ReadLog(LogQuery{})
	if err != nil || len(r.Lines) != 6 || r.Scanned != 6 || r.Lines[0].Msg != "dns: server A is UP" || r.Lines[4].Msg != "web: boom" {
		t.Fatalf("all: %v %+v", err, r)
	}
	if r, _ = ReadLog(LogQuery{Level: "warn"}); len(r.Lines) != 2 {
		t.Fatalf("warn+: %d %+v", len(r.Lines), r.Lines)
	}
	if r, _ = ReadLog(LogQuery{Text: "SERVER a up"}); len(r.Lines) != 2 {
		t.Fatalf("text (all words, any case): %+v", r.Lines)
	}
	if r, _ = ReadLog(LogQuery{Since: 30 * time.Minute}); len(r.Lines) != 4 { // 3 timed lines + the stray one without a time
		t.Fatalf("since: %d %+v", len(r.Lines), r.Lines)
	}
	r, _ = ReadLog(LogQuery{Limit: 2})
	if len(r.Lines) != 2 || !r.Truncated || r.Matched != 6 || r.Lines[1].Msg != "stray continuation" {
		t.Fatalf("limit keeps the newest: %+v", r)
	}
	logDir = ""
	if _, err := ReadLog(LogQuery{}); err == nil {
		t.Fatal("no log dir should be an error, not an empty log")
	}
}

func TestLogQueryArgs(t *testing.T) {
	q, err := logQueryFromArgs("warn", "x y", "2d", "")
	if err != nil || q.Since != 48*time.Hour || q.Limit != logReadDefault || q.Text != "x y" {
		t.Fatalf("%+v %v", q, err)
	}
	if q, _ = logQueryFromArgs("", "", "all", "0"); q.Since != 0 || q.Limit != 0 {
		t.Fatalf("%+v", q)
	}
	for _, bad := range [][4]string{{"", "", "soon", ""}, {"", "", "", "-1"}, {"", "", "", "abc"}} {
		if _, err := logQueryFromArgs(bad[0], bad[1], bad[2], bad[3]); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}
