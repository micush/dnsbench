package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestStrainLogTransitions(t *testing.T) {
	var s strainLog
	say := func(l HostLoad) (string, bool) { return s.note("k", "ns1", l) }
	if m, _ := say(HostLoad{CPU: 10, Mem: 40, Disk: 50}); m != "" {
		t.Fatalf("quiet: %q", m)
	}
	if m, _ := say(HostLoad{CPU: 85, Mem: 85, Disk: 85}); m != "" {
		t.Fatalf("85%% is not over: %q", m)
	}
	m, warn := say(HostLoad{CPU: 91, Mem: 40, Disk: 88, DiskMount: "/var"})
	if !warn || !strings.Contains(m, "node ns1 is over 85%: CPU 91%, disk 88% (/var)") || !strings.Contains(m, "amber") {
		t.Fatalf("over: %q", m)
	}
	if m, _ := say(HostLoad{CPU: 95, Mem: 40, Disk: 89, DiskMount: "/var"}); m != "" {
		t.Fatalf("a moving reading is not logged again: %q", m)
	}
	m, warn = say(HostLoad{CPU: 95, Mem: 90, Disk: 89, DiskMount: "/var"})
	if !warn || !strings.Contains(m, "now over 85%") || !strings.Contains(m, "memory 90%") {
		t.Fatalf("another one over: %q", m)
	}
	m, warn = say(HostLoad{CPU: 20, Mem: 41, Disk: 50})
	if warn || !strings.Contains(m, "back at 85% or below") || !strings.Contains(m, "CPU 20%") {
		t.Fatalf("cleared: %q", m)
	}
	if m, _ := say(HostLoad{CPU: 20, Mem: 41, Disk: 50}); m != "" {
		t.Fatalf("still clear: %q", m)
	}
}

// A cluster node (this one or another) going over the limit is logged by the cluster loop, and again when it clears.
func TestClusterLogsHostStrain(t *testing.T) {
	failDelay = 0
	old, oldLog := hostLoadFn, slog.Default()
	t.Cleanup(func() { hostLoadFn = old; slog.SetDefault(oldLog) })
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	load := HostLoad{CPU: 10, Mem: 40, Disk: 50, DiskMount: "/"}
	hostLoadFn = func() (HostLoad, bool) { return load, true }
	a, b := newTNode(t, "A"), newTNode(t, "B")
	a.join2(t, b)
	a.sync()
	a.mg.cl.logStrain()
	if strings.Contains(buf.String(), "host load") {
		t.Fatalf("quiet cluster logged: %s", buf.String())
	}
	load = HostLoad{CPU: 10, Mem: 92, Disk: 50, DiskMount: "/"} // both test nodes share this reading
	a.sync()
	a.mg.cl.logStrain()
	out := buf.String()
	if strings.Count(out, "is over 85%: memory 92%") != 2 || !strings.Contains(out, "level=WARN") {
		t.Fatalf("over the limit (this node and its peer): %s", out)
	}
	buf.Reset()
	a.mg.cl.logStrain()
	if strings.Contains(buf.String(), "host load") {
		t.Fatalf("logged again with nothing new: %s", buf.String())
	}
	load = HostLoad{CPU: 10, Mem: 40, Disk: 50, DiskMount: "/"}
	a.sync()
	a.mg.cl.logStrain()
	if out := buf.String(); strings.Count(out, "back at 85% or below") != 2 || !strings.Contains(out, "level=INFO") {
		t.Fatalf("cleared: %s", out)
	}
}

// Turning something on or off reads "enabled" / "disabled" in the change log line.
func TestSummaryWordsEnableDisable(t *testing.T) {
	a, b := newDaemonConfig(), newDaemonConfig()
	b.Groups[0].Preempt = true
	b.DNS.Cache = false
	b.DNS.TLSInsecure = true
	b.DNS.DoTPort = 853
	b.DNS.ClientRate = 50
	b.DNS.Spread = false
	got := summarizeSections(configSections(a, b))
	for _, w := range []string{"preempt enabled", "cache disabled", "tls_insecure enabled", "dot_port enabled (853)", "client_rate enabled (50)", "spread disabled"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
	a, b = b, a
	got = summarizeSections(configSections(a, b))
	for _, w := range []string{"preempt disabled", "cache enabled", "tls_insecure disabled", "dot_port disabled", "client_rate disabled"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
}
