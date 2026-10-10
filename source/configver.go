package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Configuration history, modelled on umiss's internal/configversion: an
// automatic snapshot whenever the effective configuration changes (GUI, CLI,
// cluster sync or a hand edit of the file), manual snapshots, FIFO retention,
// a diff between any two versions (or either against the live one) and
// restore.  One small JSON file per version, named by a unix-millisecond id.
//
// The versioned unit is the whole per-node config file (groups, dns, web,
// cluster, log_level): that is what this node actually runs, including the
// parts a cluster replicates from the primary.

// ErrNoVersion means the id doesn't name a recorded version.
var ErrNoVersion = errors.New("no such configuration version")

const (
	// CurrentVersionID refers to the live configuration in Get/Diff.
	CurrentVersionID = "current"
	maxVersions      = 200
	maxNamedItems    = 6
	maxDiffLines     = 4000
)

// VersionMeta is the list-view header of one version.
type VersionMeta struct {
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	Actor   string    `json:"actor"`
	Summary string    `json:"summary"`
	Note    string    `json:"note,omitempty"`
	Manual  bool      `json:"manual,omitempty"`
}

type versionFile struct {
	VersionMeta
	Config json.RawMessage `json:"config"`
}

// Section is one labelled line of a change summary.
type Section struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// DiffLine is one line of a unified-style diff: Op is " ", "+", "-" or "@"
// (a collapsed run of unchanged lines; Text says how many).
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// VersionDiff is the result of comparing two versions.
type VersionDiff struct {
	A        VersionMeta `json:"a"`
	B        VersionMeta `json:"b"`
	Sections []Section   `json:"sections"`
	Lines    []DiffLine  `json:"lines"`
	Same     bool        `json:"same"`
}

// VersionStore manages one node's configuration history on disk.
type VersionStore struct {
	mu  sync.Mutex
	dir string

	expectActor string
	expectNote  string
	expectUntil time.Time
}

func NewVersionStore(dir string) (*VersionStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &VersionStore{dir: dir}, nil
}

// Expect tells the store that the next change it sees was made by actor, so a
// file-watcher notification racing the writer is attributed correctly.
func (s *VersionStore) Expect(actor, note string) {
	s.mu.Lock()
	s.expectActor, s.expectNote, s.expectUntil = actor, note, time.Now().Add(10*time.Second)
	s.mu.Unlock()
}

func (s *VersionStore) takeExpect() (actor, note string) {
	if time.Now().Before(s.expectUntil) {
		actor, note = s.expectActor, s.expectNote
	}
	s.expectActor, s.expectNote, s.expectUntil = "", "", time.Time{}
	return
}

// canonicalConfig parses raw as a daemon config and renders it in the one
// normal form used for comparing and storing (defaults filled in, fixed key
// order, 2-space indent).
func canonicalConfig(raw []byte) (*DaemonConfig, []byte, error) {
	dc := newDaemonConfig()
	if err := json.Unmarshal(raw, dc); err != nil {
		return nil, nil, err
	}
	b, err := json.MarshalIndent(dc, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return dc, b, nil
}

// path is the file of a version. The id is a number (validVersionID), and the
// file name is built from that number, not from the text it came in as.
func (s *VersionStore) path(id string) string {
	n, _ := strconv.ParseInt(id, 10, 64)
	return filepath.Join(s.dir, strconv.FormatInt(n, 10)+".json")
}

// validVersionID accepts the canonical decimal form of a positive number.
func validVersionID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

func (s *VersionStore) ids() []string {
	ents, _ := os.ReadDir(s.dir)
	var ids []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasSuffix(n, ".json") && validVersionID(strings.TrimSuffix(n, ".json")) {
			ids = append(ids, strings.TrimSuffix(n, ".json"))
		}
	}
	sort.Slice(ids, func(i, j int) bool { // numeric order
		a, _ := strconv.ParseInt(ids[i], 10, 64)
		b, _ := strconv.ParseInt(ids[j], 10, 64)
		return a < b
	})
	return ids
}

func (s *VersionStore) read(id string) (*versionFile, error) {
	if !validVersionID(id) {
		return nil, ErrNoVersion
	}
	b, err := os.ReadFile(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoVersion
	}
	if err != nil {
		return nil, err
	}
	var vf versionFile
	if err := json.Unmarshal(b, &vf); err != nil {
		return nil, fmt.Errorf("version %s is corrupt: %w", id, err)
	}
	return &vf, nil
}

func (s *VersionStore) latestLocked() *versionFile {
	ids := s.ids()
	for i := len(ids) - 1; i >= 0; i-- {
		if vf, err := s.read(ids[i]); err == nil {
			return vf
		}
	}
	return nil
}

// Record snapshots raw if it differs from the latest recorded version (or
// always, for a manual snapshot).  A nil meta means "unchanged".
func (s *VersionStore) Record(raw []byte, actor, note string, manual bool) (*VersionMeta, error) {
	nu, canon, err := canonicalConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("not recorded: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ea, en := s.takeExpect(); ea != "" && !manual {
		actor = ea
		if note == "" {
			note = en
		}
	}
	prev := s.latestLocked()
	summary, changed := "initial configuration", true
	if prev != nil {
		old, oldCanon, err := canonicalConfig(prev.Config)
		switch {
		case err == nil && bytes.Equal(oldCanon, canon):
			summary, changed = "no changes since the previous version", false
		case err == nil:
			summary = summarizeSections(configSections(old, nu))
		}
	}
	if !changed && !manual {
		return nil, nil
	}
	if manual {
		if prev == nil || !changed {
			summary = "manual snapshot (" + summary + ")"
		} else {
			summary = "manual snapshot: " + summary
		}
	}
	id := strconv.FormatInt(time.Now().UnixMilli(), 10)
	if ids := s.ids(); len(ids) > 0 { // keep ids strictly increasing
		last, _ := strconv.ParseInt(ids[len(ids)-1], 10, 64)
		if n, _ := strconv.ParseInt(id, 10, 64); n <= last {
			id = strconv.FormatInt(last+1, 10)
		}
	}
	vf := versionFile{
		VersionMeta: VersionMeta{ID: id, At: time.Now().UTC(), Actor: actor, Summary: summary, Note: note, Manual: manual},
		Config:      canon,
	}
	b, err := json.MarshalIndent(vf, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(s.path(id), b, 0o600); err != nil {
		return nil, err
	}
	s.pruneLocked()
	return &vf.VersionMeta, nil
}

func (s *VersionStore) pruneLocked() {
	ids := s.ids()
	for len(ids) > maxVersions {
		os.Remove(s.path(ids[0]))
		ids = ids[1:]
	}
}

// List returns the versions, newest first.
func (s *VersionStore) List() []VersionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.ids()
	out := make([]VersionMeta, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		if vf, err := s.read(ids[i]); err == nil {
			out = append(out, vf.VersionMeta)
		}
	}
	return out
}

// Get returns one version's metadata and canonical config JSON.
func (s *VersionStore) Get(id string) (VersionMeta, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vf, err := s.read(id)
	if err != nil {
		return VersionMeta{}, nil, err
	}
	_, canon, err := canonicalConfig(vf.Config)
	if err != nil {
		return VersionMeta{}, nil, err
	}
	return vf.VersionMeta, canon, nil
}

// Diff compares versions a and b; either may be CurrentVersionID, in which
// case current (the live config file's bytes) is used.
func (s *VersionStore) Diff(a, b string, current []byte) (*VersionDiff, error) {
	load := func(id string) (VersionMeta, *DaemonConfig, []byte, error) {
		if id == CurrentVersionID {
			dc, canon, err := canonicalConfig(current)
			return VersionMeta{ID: CurrentVersionID, Summary: "live configuration", At: time.Now().UTC()}, dc, canon, err
		}
		m, canon, err := s.Get(id)
		if err != nil {
			return m, nil, nil, err
		}
		dc, _, err := canonicalConfig(canon)
		return m, dc, canon, err
	}
	ma, da, ca, err := load(a)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", a, err)
	}
	mb, db, cb, err := load(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b, err)
	}
	d := &VersionDiff{A: ma, B: mb, Sections: configSections(da, db), Same: bytes.Equal(ca, cb)}
	d.Lines = lineDiff(strings.Split(string(ca), "\n"), strings.Split(string(cb), "\n"), 3)
	if d.Lines == nil {
		d.Lines = []DiffLine{}
	}
	if d.Sections == nil {
		d.Sections = []Section{}
	}
	return d, nil
}

// ── change summary ───────────────────────────────────────────────────────────

func summarizeSections(secs []Section) string {
	if len(secs) == 0 {
		return "no functional change"
	}
	parts := make([]string, len(secs))
	for i, s := range secs {
		parts[i] = s.Label + ": " + s.Detail
	}
	return strings.Join(parts, "; ")
}

func toMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	m := map[string]any{}
	json.Unmarshal(b, &m)
	return m
}

// changedFields lists the keys whose values differ, hiding secrets.
func changedFields(a, b map[string]any, skip ...string) []string {
	var out []string
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var names []string
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
next:
	for _, k := range names {
		for _, s := range skip {
			if k == s {
				continue next
			}
		}
		if reflect.DeepEqual(a[k], b[k]) {
			continue
		}
		if k == "key" {
			out = append(out, "key changed")
			continue
		}
		if k == "dns" {
			out = append(out, dnsChange(a[k], b[k]))
			continue
		}
		if k == "paused" {
			if on, _ := b[k].(bool); on {
				out = append(out, "paused on this node")
			} else {
				out = append(out, "resumed on this node")
			}
			continue
		}
		if t, ok := switched(k, a[k], b[k]); ok {
			out = append(out, t)
			continue
		}
		out = append(out, fmt.Sprintf("%s %s → %s", k, short(a[k]), short(b[k])))
	}
	return out
}

// switched words a change that turns something on or off as "enabled" or "disabled": a yes/no setting that flipped, or a
// number whose zero means off (the DoT and DoH ports, the client rate limit).
func switched(k string, a, b any) (string, bool) {
	ab, aok := a.(bool)
	bb, bok := b.(bool)
	if (aok || a == nil) && (bok || b == nil) && (aok || bok) && ab != bb {
		if bb {
			return k + " enabled", true
		}
		return k + " disabled", true
	}
	switch k {
	case "dot_port", "doh_port", "client_rate":
		an, _ := a.(float64)
		bn, _ := b.(float64)
		if an == 0 && bn > 0 {
			return fmt.Sprintf("%s enabled (%s)", k, short(b)), true
		}
		if an > 0 && bn == 0 {
			return k + " disabled", true
		}
	}
	return "", false
}

func short(v any) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 60 {
		s = s[:57] + "…"
	}
	return s
}

func named(items []string) string {
	if len(items) <= maxNamedItems {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:maxNamedItems], ", ") + fmt.Sprintf(" and %d more", len(items)-maxNamedItems)
}

func setDiff(a, b []string) (added, removed []string) {
	in := func(l []string, s string) bool {
		for _, x := range l {
			if x == s {
				return true
			}
		}
		return false
	}
	for _, x := range b {
		if !in(a, x) {
			added = append(added, x)
		}
	}
	for _, x := range a {
		if !in(b, x) {
			removed = append(removed, x)
		}
	}
	return
}

// configSections describes how b differs from a, one section per area.
func configSections(a, b *DaemonConfig) []Section {
	var out []Section
	if a.LogLevel != b.LogLevel {
		out = append(out, Section{"Log level", a.LogLevel + " → " + b.LogLevel})
	}

	// groups, matched by group_id
	ga, gb := map[int]GroupConfig{}, map[int]GroupConfig{}
	var ids []int
	for _, g := range a.Groups {
		ga[g.GroupID] = g
	}
	for _, g := range b.Groups {
		gb[g.GroupID] = g
	}
	seen := map[int]bool{}
	for _, g := range append(append([]GroupConfig{}, a.Groups...), b.Groups...) {
		if !seen[g.GroupID] {
			seen[g.GroupID] = true
			ids = append(ids, g.GroupID)
		}
	}
	sort.Ints(ids)
	var added, removed, changed []string
	for _, id := range ids {
		x, inA := ga[id]
		y, inB := gb[id]
		switch {
		case !inA:
			added = append(added, strconv.Itoa(id))
		case !inB:
			removed = append(removed, strconv.Itoa(id))
		default:
			if f := changedFields(toMap(x), toMap(y)); len(f) > 0 {
				changed = append(changed, fmt.Sprintf("group %d (%s)", id, named(f)))
			}
		}
	}
	if len(added) > 0 {
		out = append(out, Section{"Groups added", named(added)})
	}
	if len(removed) > 0 {
		out = append(out, Section{"Groups removed", named(removed)})
	}
	if len(changed) > 0 {
		out = append(out, Section{"Groups changed", strings.Join(changed, "; ")})
	}

	if a.NodePaused != b.NodePaused {
		if b.NodePaused {
			out = append(out, Section{"Node", "paused — this node is out of service"})
		} else {
			out = append(out, Section{"Node", "resumed"})
		}
	}

	// dns
	if sa, sb := a.DNS.Servers, b.DNS.Servers; !reflect.DeepEqual(sa, sb) {
		add, rem := setDiff(sa, sb)
		var parts []string
		if len(add) > 0 {
			parts = append(parts, "added "+named(add))
		}
		if len(rem) > 0 {
			parts = append(parts, "removed "+named(rem))
		}
		if len(parts) == 0 {
			parts = append(parts, "reordered")
		}
		out = append(out, Section{"DNS servers", strings.Join(parts, ", ")})
	}
	if !reflect.DeepEqual(a.DNS.ServerQueries, b.DNS.ServerQueries) && !(len(a.DNS.ServerQueries) == 0 && len(b.DNS.ServerQueries) == 0) {
		out = append(out, Section{"DNS per-server queries", "changed"})
	}
	if !reflect.DeepEqual(a.DNS.ServerNames, b.DNS.ServerNames) && !(len(a.DNS.ServerNames) == 0 && len(b.DNS.ServerNames) == 0) {
		out = append(out, Section{"DNS server names", "changed"})
	}
	qs := func(d DNSConfig) []string {
		var l []string
		for _, q := range d.Queries {
			l = append(l, q.String())
		}
		return l
	}
	if qa, qb := qs(a.DNS), qs(b.DNS); !reflect.DeepEqual(qa, qb) {
		add, rem := setDiff(qa, qb)
		var parts []string
		if len(add) > 0 {
			parts = append(parts, "added "+named(add))
		}
		if len(rem) > 0 {
			parts = append(parts, "removed "+named(rem))
		}
		if len(parts) == 0 {
			parts = append(parts, "reordered")
		}
		out = append(out, Section{"DNS probe queries", strings.Join(parts, ", ")})
	}
	if fadd, frem := setDiff(a.DNS.FallbackServers, b.DNS.FallbackServers); len(fadd)+len(frem) > 0 {
		var parts []string
		if len(fadd) > 0 {
			parts = append(parts, "added "+named(fadd))
		}
		if len(frem) > 0 {
			parts = append(parts, "removed "+named(frem))
		}
		out = append(out, Section{"DNS fallback servers", strings.Join(parts, "; ")})
	}
	if padd, prem := setDiff(a.DNS.PausedServers, b.DNS.PausedServers); len(padd)+len(prem) > 0 {
		var parts []string
		if len(padd) > 0 {
			parts = append(parts, "paused "+named(padd))
		}
		if len(prem) > 0 {
			parts = append(parts, "resumed "+named(prem))
		}
		out = append(out, Section{"DNS servers", strings.Join(parts, ", ")})
	}
	if qadd, qrem := setDiff(a.DNS.PausedQueries, b.DNS.PausedQueries); len(qadd)+len(qrem) > 0 {
		var parts []string
		if len(qadd) > 0 {
			parts = append(parts, "paused "+named(qadd))
		}
		if len(qrem) > 0 {
			parts = append(parts, "resumed "+named(qrem))
		}
		out = append(out, Section{"DNS probe domains", strings.Join(parts, ", ")})
	}
	if f := changedFields(toMap(a.DNS), toMap(b.DNS), "servers", "queries", "paused_servers", "paused_queries"); len(f) > 0 {
		out = append(out, Section{"DNS settings", named(f)})
	}
	if f := changedFields(toMap(a.Web), toMap(b.Web)); len(f) > 0 {
		out = append(out, Section{"Web GUI", named(f)})
	}
	if f := changedFields(toMap(a.Cluster), toMap(b.Cluster)); len(f) > 0 {
		out = append(out, Section{"Cluster", named(f)})
	}
	return out
}

// ── line diff ────────────────────────────────────────────────────────────────

// lineDiff is a plain LCS diff with `ctx` lines of context; runs of unchanged
// lines further away are collapsed into one "@" entry.
func lineDiff(a, b []string, ctx int) []DiffLine {
	if len(a) > maxDiffLines || len(b) > maxDiffLines {
		return []DiffLine{{Op: "@", Text: "configuration too large to diff line by line"}}
	}
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var all []DiffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			all = append(all, DiffLine{" ", a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			all = append(all, DiffLine{"-", a[i]})
			i++
		default:
			all = append(all, DiffLine{"+", b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		all = append(all, DiffLine{"-", a[i]})
	}
	for ; j < m; j++ {
		all = append(all, DiffLine{"+", b[j]})
	}
	// keep changed lines and ctx lines around them
	keep := make([]bool, len(all))
	for k, l := range all {
		if l.Op != " " {
			for d := -ctx; d <= ctx; d++ {
				if k+d >= 0 && k+d < len(all) {
					keep[k+d] = true
				}
			}
		}
	}
	var out []DiffLine
	skipped := 0
	flush := func() {
		if skipped > 0 {
			out = append(out, DiffLine{"@", fmt.Sprintf("%d unchanged line(s)", skipped)})
			skipped = 0
		}
	}
	for k, l := range all {
		if keep[k] {
			flush()
			out = append(out, l)
		} else {
			skipped++
		}
	}
	flush()
	if len(out) == 1 && out[0].Op == "@" { // identical
		return nil
	}
	return out
}

// dnsChange describes a change of a group's own DNS pool in words.
func dnsChange(a, b any) string {
	parse := func(v any) DNSConfig {
		var d DNSConfig
		if v != nil {
			raw, _ := json.Marshal(v)
			_ = json.Unmarshal(raw, &d)
		}
		return d
	}
	da, db := parse(a), parse(b)
	if a == nil {
		return fmt.Sprintf("own DNS pool created (%d server(s))", len(db.Servers))
	}
	if b == nil {
		return "own DNS pool removed (back to the shared one)"
	}
	var parts []string
	add, rem := setDiff(da.Servers, db.Servers)
	if len(add) > 0 {
		parts = append(parts, "servers added "+named(add))
	}
	if len(rem) > 0 {
		parts = append(parts, "servers removed "+named(rem))
	}
	if fadd, frem := setDiff(da.FallbackServers, db.FallbackServers); len(fadd)+len(frem) > 0 {
		if len(fadd) > 0 {
			parts = append(parts, "fallback servers added "+named(fadd))
		}
		if len(frem) > 0 {
			parts = append(parts, "fallback servers removed "+named(frem))
		}
	}
	padd, prem := setDiff(da.PausedServers, db.PausedServers)
	if len(padd) > 0 {
		parts = append(parts, "servers paused "+named(padd))
	}
	if len(prem) > 0 {
		parts = append(parts, "servers resumed "+named(prem))
	}
	tests := func(d DNSConfig) []string {
		var l []string
		for _, sv := range d.Servers {
			for _, q := range d.queriesFor(sv) {
				l = append(l, sv+" "+q.String())
			}
		}
		return l
	}
	tadd, trem := setDiff(tests(da), tests(db))
	if len(tadd) > 0 {
		parts = append(parts, "domains added "+named(tadd))
	}
	if len(trem) > 0 {
		parts = append(parts, "domains removed "+named(trem))
	}
	if len(parts) == 0 {
		parts = append(parts, "settings changed")
	}
	return "DNS pool: " + strings.Join(parts, ", ")
}
