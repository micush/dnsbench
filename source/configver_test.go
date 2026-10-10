package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func cfgBytes(t *testing.T, mut func(*DaemonConfig)) []byte {
	t.Helper()
	dc := newDaemonConfig()
	if mut != nil {
		mut(dc)
	}
	b, err := json.Marshal(dc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVersionRecordDedupeAndSummary(t *testing.T) {
	vs, err := NewVersionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m1, err := vs.Record(cfgBytes(t, nil), "startup", "", false)
	if err != nil || m1 == nil || m1.Summary != "initial configuration" {
		t.Fatalf("first record: %v %+v", err, m1)
	}
	if m, _ := vs.Record(cfgBytes(t, nil), "x", "", false); m != nil {
		t.Fatalf("identical config must not create a version: %+v", m)
	}
	m2, _ := vs.Record(cfgBytes(t, func(d *DaemonConfig) {
		d.Groups[0].Priority = 200
		d.Groups[0].Key = "secret-new"
		d.DNS.Servers = []string{"10.0.0.1"}
	}), "alice", "bump", false)
	if m2 == nil {
		t.Fatal("changed config must be recorded")
	}
	for _, want := range []string{"group 1", "priority 100 → 200", "key changed", "added 10.0.0.1"} {
		if !strings.Contains(m2.Summary, want) {
			t.Errorf("summary %q lacks %q", m2.Summary, want)
		}
	}
	if strings.Contains(m2.Summary, "secret-new") {
		t.Error("summary leaks the key")
	}
	if m2.Actor != "alice" || m2.Note != "bump" {
		t.Errorf("meta: %+v", m2)
	}
	l := vs.List()
	if len(l) != 2 || l[0].ID != m2.ID || l[1].ID != m1.ID {
		t.Fatalf("list order: %+v", l)
	}
	if m, _ := vs.Record(cfgBytes(t, nil), "bob", "", true); m == nil || !m.Manual {
		t.Fatal("manual snapshot must always record")
	}
}

func TestVersionExpectAttributesActor(t *testing.T) {
	vs, _ := NewVersionStore(t.TempDir())
	vs.Record(cfgBytes(t, nil), "startup", "", false)
	vs.Expect("carol", "via gui")
	m, _ := vs.Record(cfgBytes(t, func(d *DaemonConfig) { d.LogLevel = "debug" }), "file edit", "", false)
	if m == nil || m.Actor != "carol" || m.Note != "via gui" {
		t.Fatalf("expectation not applied: %+v", m)
	}
	m, _ = vs.Record(cfgBytes(t, func(d *DaemonConfig) { d.LogLevel = "error" }), "file edit", "", false)
	if m == nil || m.Actor != "file edit" {
		t.Fatalf("expectation must be single-use: %+v", m)
	}
}

func TestVersionDiffAndGet(t *testing.T) {
	vs, _ := NewVersionStore(t.TempDir())
	a, _ := vs.Record(cfgBytes(t, nil), "s", "", false)
	b, _ := vs.Record(cfgBytes(t, func(d *DaemonConfig) { d.Groups[0].HelloMS = 500 }), "s", "", false)
	d, err := vs.Diff(a.ID, b.ID, nil)
	if err != nil || d.Same {
		t.Fatalf("diff: %v %+v", err, d)
	}
	var plus, minus int
	for _, l := range d.Lines {
		switch l.Op {
		case "+":
			plus++
			if !strings.Contains(l.Text, "500") {
				t.Errorf("unexpected added line %q", l.Text)
			}
		case "-":
			minus++
		}
	}
	if plus != 1 || minus != 1 {
		t.Errorf("want one +/-, got %d/%d: %+v", plus, minus, d.Lines)
	}
	cur := cfgBytes(t, func(d *DaemonConfig) { d.Groups[0].HelloMS = 500 })
	d2, err := vs.Diff(b.ID, CurrentVersionID, cur)
	if err != nil || !d2.Same || len(d2.Lines) != 0 {
		t.Fatalf("version vs identical live must be Same: %v %+v", err, d2)
	}
	// the GUI maps over these lists: they must serialise as [] and never null
	if js, _ := json.Marshal(d2); strings.Contains(string(js), "null") {
		t.Errorf("an identical diff must not contain nulls: %s", js)
	}
	if _, _, err := vs.Get("123"); err != ErrNoVersion {
		t.Errorf("missing version: %v", err)
	}
	if _, _, err := vs.Get("../etc/passwd"); err != ErrNoVersion {
		t.Errorf("path traversal id must be rejected: %v", err)
	}
	_, cfg, err := vs.Get(a.ID)
	if err != nil || !strings.Contains(string(cfg), `"hello_ms": 333`) {
		t.Errorf("get: %v %s", err, cfg)
	}
}

func TestVersionRetention(t *testing.T) {
	vs, _ := NewVersionStore(t.TempDir())
	for i := 0; i < maxVersions+5; i++ {
		n := i
		vs.Record(cfgBytes(t, func(d *DaemonConfig) { d.Groups[0].Priority = n % 250 }), "s", "", true)
	}
	if got := len(vs.List()); got != maxVersions {
		t.Fatalf("retention: %d versions, want %d", got, maxVersions)
	}
}

func TestVersionRejectsGarbage(t *testing.T) {
	vs, _ := NewVersionStore(t.TempDir())
	if m, err := vs.Record([]byte("{not json"), "s", "", false); err == nil || m != nil {
		t.Fatal("garbage must not be recorded")
	}
}
