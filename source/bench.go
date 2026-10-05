package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	drainGrace    = 250 * time.Millisecond // how long a duration run waits for the last replies
	flushInterval = 100 * time.Millisecond // worker -> shared stats
	maxWorkers    = 4096
	maxWindow     = 4096
)

// queryTimeout is how long a query may go unanswered. It is a variable only so
// tests can shorten it.
var queryTimeout = 5 * time.Second

// ── Parameters ───────────────────────────────────────────────────────────────

type benchParams struct {
	server   string
	protocol string // udp | tcp | dot | doh

	host string // server host as typed (no port)
	port string
	path string // DoH path

	workers int
	window  int // UDP in-flight queries per worker

	domains []string

	hasDuration  bool
	duration     time.Duration
	durationText string
	count        uint64 // per worker, when !hasDuration

	rateTotal     uint64
	ratePerWorker uint64
	rateEffective uint64

	qtype    string
	recurse  bool
	insecure bool

	dohPost  bool
	dohHTTP2 bool
}

func argBool(args map[string]string, key string, def bool) bool {
	v, ok := args[key]
	if !ok {
		return def
	}
	return strings.EqualFold(strings.TrimSpace(v), "true")
}

func argInt(args map[string]string, key string) (int, bool) {
	v := strings.TrimSpace(args[key])
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// autoWorkers and autoQueue are the defaults the UI shows as "Auto".
func autoWorkers(cpu int) int { return clampInt(cpu-1, 1, 32) }
func autoQueue(cpu int) int   { return clampInt(cpu*2, 1, 64) }

// parseDuration accepts "30s", "5m", "1h" or a bare number of seconds.
// Unparseable or non-positive numbers fall back to 30 (of the given unit).
func parseDuration(s string) time.Duration {
	s = strings.TrimSpace(s)
	mult := time.Second
	switch {
	case strings.HasSuffix(s, "m"):
		mult, s = time.Minute, s[:len(s)-1]
	case strings.HasSuffix(s, "h"):
		mult, s = time.Hour, s[:len(s)-1]
	case strings.HasSuffix(s, "s"):
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		n = 30
	}
	return time.Duration(n * float64(mult))
}

func parseParams(args map[string]string, cpu int) (benchParams, []string, error) {
	var p benchParams
	var warnings []string

	p.server = strings.TrimSpace(args["server"])
	if p.server == "" {
		p.server = "127.0.0.1"
	}
	p.protocol = strings.ToLower(strings.TrimSpace(args["protocol"]))
	if p.protocol == "" {
		p.protocol = "udp"
	}
	switch p.protocol {
	case "udp", "tcp", "dot", "doh":
	case "doq":
		return p, nil, errors.New("DoQ is not supported")
	default:
		return p, nil, fmt.Errorf("unknown protocol %q", p.protocol)
	}
	host, portText, path := parseServer(p.server, p.protocol)
	port, err := checkTarget(p.server, p.protocol, host, portText, path)
	if err != nil {
		return p, nil, err
	}
	// The port is stored from the checked number, never from the typed text.
	p.host, p.port, p.path = host, strconv.Itoa(port), path

	if n, ok := argInt(args, "concurrency"); ok {
		p.workers = clampInt(n, 1, maxWorkers)
	} else {
		p.workers = autoWorkers(cpu)
	}
	if n, ok := argInt(args, "pipeline"); ok {
		p.window = clampInt(n, 1, maxWindow)
	} else {
		p.window = autoQueue(cpu)
	}

	raw := strings.ReplaceAll(args["queries"], `\n`, "\n")
	raw = strings.ReplaceAll(raw, `\r`, "")
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == ',' || r == '\r' }) {
		if f = strings.TrimSpace(f); f != "" {
			p.domains = append(p.domains, f)
		}
	}
	if len(p.domains) == 0 {
		p.domains = []string{"google.com", "cloudflare.com", "github.com"}
	}

	p.durationText = strings.TrimSpace(args["duration"])
	if p.durationText != "" {
		p.hasDuration = true
		p.duration = parseDuration(p.durationText)
	} else {
		p.count = 100
		if n, err := strconv.ParseUint(strings.TrimSpace(args["count"]), 10, 64); err == nil && n > 0 {
			p.count = n
		}
	}

	if n, err := strconv.ParseUint(strings.TrimSpace(args["rate_limit"]), 10, 64); err == nil {
		p.rateTotal = n
	}
	if p.rateTotal > 0 {
		w := uint64(p.workers)
		p.ratePerWorker = (p.rateTotal + w - 1) / w
		p.rateEffective = p.ratePerWorker * w
	}

	p.qtype = strings.TrimSpace(args["query_type"])
	if p.qtype == "" {
		p.qtype = strings.TrimSpace(args["qtype"])
	}
	if p.qtype == "" {
		p.qtype = "A"
	}
	p.recurse = argBool(args, "recurse", true)
	p.insecure = argBool(args, "insecure", true)
	p.dohPost = !strings.EqualFold(strings.TrimSpace(args["doh_method"]), "get")
	p.dohHTTP2 = strings.TrimSpace(args["doh_protocol"]) == "2"
	return p, warnings, nil
}

// parseServer splits a user-supplied server string into host, port and (for
// DoH) path. Accepts "host", "host:port", "[v6]:port", a bare IPv6 address and
// "https://host:port/path".
func parseServer(server, protocol string) (host, port, path string) {
	def := "53"
	switch protocol {
	case "dot":
		def = "853"
	case "doh":
		def = "443"
	}
	path = "/dns-query"
	s := server
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		if p := s[i:]; p != "/" {
			path = p
		}
		s = s[:i]
	}
	host, port = splitHostPort(s, def)
	return host, port, path
}

// checkTarget refuses a server string that cannot be a host, a port and a path,
// and returns the port as a number. The target is whatever the signed-in user
// typed, so it is checked before it is used for anything: the port must be a
// number from 1 to 65535, the host must be an IP address or a name free of
// characters that have a meaning in a URL or a header, and the DoH path must be
// an absolute path without control characters.
func checkTarget(server, protocol, host, portText, path string) (int, error) {
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q", portText)
	}
	if host == "" || len(host) > 253 {
		return 0, fmt.Errorf("invalid server %q", server)
	}
	if net.ParseIP(host) == nil {
		for _, r := range host {
			if r <= ' ' || r == 0x7f || strings.ContainsRune("/\\?#@:[]%\"<>`", r) {
				return 0, fmt.Errorf("invalid server %q", server)
			}
		}
	}
	if protocol == "doh" {
		if !strings.HasPrefix(path, "/") {
			return 0, fmt.Errorf("invalid path %q", path)
		}
		for _, r := range path {
			if r < ' ' || r == 0x7f {
				return 0, fmt.Errorf("invalid path %q", path)
			}
		}
	}
	return port, nil
}

func splitHostPort(s, def string) (string, string) {
	if strings.HasPrefix(s, "[") {
		if h, p, err := net.SplitHostPort(s); err == nil {
			return h, p
		}
		return strings.Trim(s, "[]"), def
	}
	if strings.Count(s, ":") == 1 {
		i := strings.LastIndexByte(s, ':')
		if n, err := strconv.Atoi(s[i+1:]); err == nil && n > 0 && n < 65536 {
			return s[:i], s[i+1:]
		}
	}
	return s, def
}

// resolveHost returns an IP for host, preferring IPv4.
func resolveHost(host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("%s: %v", host, err)
	}
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.String(), nil
		}
	}
	if len(ips) > 0 {
		return ips[0].String(), nil
	}
	return "", fmt.Errorf("no address for %s", host)
}

// ── Shared stats ─────────────────────────────────────────────────────────────

type errSample struct{ domain, reason string }

type sharedStats struct {
	done atomic.Uint64 // completed queries (answered, failed or timed out)
	errs atomic.Uint64

	mu      sync.Mutex
	lat     hist
	rcodes  [rcSlots]uint64
	samples []errSample
}

// localStats is a worker-private accumulator, flushed periodically.
type localStats struct {
	c         *runCtx
	lat       hist
	rcodes    [rcSlots]uint64
	done      uint64
	errs      uint64
	samples   []errSample
	lastFlush time.Time
}

func (c *runCtx) newLocal() *localStats {
	return &localStats{c: c, lastFlush: time.Now()}
}

func (l *localStats) addSample(domain, reason string) {
	if len(l.samples) < 5 {
		l.samples = append(l.samples, errSample{domain, reason})
	}
}

// ok records a NOERROR answer that took ns nanoseconds.
func (l *localStats) ok(ns int64) {
	l.lat.recordNs(ns)
	l.rcodes[0]++
	l.done++
}

// bad records an answer with a non-zero RCODE slot.
func (l *localStats) bad(slot int, domain string) {
	l.rcodes[slot]++
	l.errs++
	l.done++
	l.addSample(domain, rcodeLabel(slot))
}

// fail records a transport-level failure (timeout, connection error, ...).
func (l *localStats) fail(domain, reason string) {
	l.rcodes[rcError]++
	l.errs++
	l.done++
	l.addSample(domain, reason)
}

// note records a diagnostic that does not correspond to one query.
func (l *localStats) note(domain, reason string) { l.addSample(domain, reason) }

// result records one finished exchange for the sequential (TCP/DoT/DoH) workers.
func (l *localStats) result(ns int64, slot int, errText, domain string) {
	switch {
	case slot == 0:
		l.ok(ns)
	case slot == rcError:
		l.fail(domain, errText)
	default:
		l.bad(slot, domain)
	}
	if time.Since(l.lastFlush) >= flushInterval {
		l.flush()
	}
}

func (l *localStats) flush() {
	s := &l.c.stats
	if l.done != 0 {
		s.done.Add(l.done)
		s.errs.Add(l.errs)
	}
	s.mu.Lock()
	s.lat.merge(&l.lat)
	for i, v := range l.rcodes {
		s.rcodes[i] += v
	}
	for _, e := range l.samples {
		if len(s.samples) < 5 {
			s.samples = append(s.samples, e)
		}
	}
	s.mu.Unlock()
	l.lat.reset()
	l.rcodes = [rcSlots]uint64{}
	l.done, l.errs = 0, 0
	l.samples = l.samples[:0]
	l.lastFlush = time.Now()
}

// ── Run context ──────────────────────────────────────────────────────────────

type runCtx struct {
	p     benchParams
	ctx   context.Context
	stop  atomic.Bool
	start time.Time

	ip       string // resolved server address
	hostport string // ip:port to dial

	names []string // valid query names
	tmpl  [][]byte // matching wire templates (ID = 0)

	stats sharedStats
}

func (c *runCtx) nowNs() int64 { return int64(time.Since(c.start)) }

// buildTemplates prepares the wire packets for every usable domain.
func (c *runCtx) buildTemplates() []string {
	var warn []string
	qt := qtypeID(c.p.qtype)
	for _, d := range c.p.domains {
		t, err := buildQuery(d, qt, c.p.recurse)
		if err != nil {
			warn = append(warn, fmt.Sprintf("skipping %q: %v", d, err))
			continue
		}
		c.names = append(c.names, d)
		c.tmpl = append(c.tmpl, t)
	}
	return warn
}

// ── Coordinator ──────────────────────────────────────────────────────────────

func (a *App) runJob(j *Job) {
	defer close(j.done)
	cpu := cpuThreads()

	p, _, err := parseParams(j.Args, cpu)
	if err != nil {
		j.emit(fmt.Sprintf("§err§ERROR: %v§rst§", err))
		j.finish("error", -1)
		return
	}
	j.setStatus("running")

	ip, err := resolveHost(p.host)
	if err != nil {
		j.emit(fmt.Sprintf("§err§ERROR: cannot resolve server: %v§rst§", err))
		j.finish("error", -1)
		return
	}

	wctx, wcancel := context.WithCancel(j.ctx)
	defer wcancel()
	c := &runCtx{p: p, ctx: wctx, ip: ip, hostport: net.JoinHostPort(ip, p.port)}
	stopWatch := context.AfterFunc(wctx, func() { c.stop.Store(true) })
	defer stopWatch()

	warns := c.buildTemplates()
	if len(c.tmpl) == 0 {
		j.emit("§err§ERROR: no valid domain names to query§rst§")
		j.finish("error", -1)
		return
	}

	j.emit("Protocol   : " + strings.ToUpper(p.protocol))
	j.emit("Server     : " + p.host)
	j.emit("Query type : " + p.qtype)
	j.emit(fmt.Sprintf("Workers    : %d", p.workers))
	if p.protocol == "udp" {
		j.emit(fmt.Sprintf("Queue      : %d", p.window))
	}
	if p.hasDuration {
		j.emit("Duration   : " + p.durationText)
	} else {
		j.emit(fmt.Sprintf("Count      : %d per worker", p.count))
	}
	if p.rateTotal > 0 {
		note := ""
		if p.rateEffective != p.rateTotal {
			note = fmt.Sprintf(" (effective: %d q/s)", p.rateEffective)
		}
		j.emit(fmt.Sprintf("Rate limit : %d q/s%s", p.rateTotal, note))
	} else {
		j.emit("Rate limit : none")
	}
	if p.recurse {
		j.emit("Recursion  : yes")
	} else {
		j.emit("Recursion  : no")
	}
	if p.protocol != "udp" && p.protocol != "tcp" {
		if p.insecure {
			j.emit("Skip TLS   : yes")
		} else {
			j.emit("Skip TLS   : no")
		}
	}
	if p.protocol == "doh" {
		method, ver := "GET", "HTTP/1.1"
		if p.dohPost {
			method = "POST"
		}
		if p.dohHTTP2 {
			ver = "HTTP/2"
		}
		j.emit(fmt.Sprintf("DoH        : %s %s", method, ver))
	}
	j.emit(fmt.Sprintf("Domains    : %d", len(c.names)))
	for _, d := range c.names {
		j.emit("  §dim§" + d + "§rst§")
	}
	for _, w := range warns {
		j.emit("§warn§" + w + "§rst§")
	}
	j.emit(fmt.Sprintf("Resolved   : %s -> %s", p.host, ip))
	j.emit("")

	c.start = time.Now()
	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			c.worker(id)
		}(i)
	}
	workersDone := make(chan struct{})
	go func() { wg.Wait(); close(workersDone) }()

	totalExpected := uint64(0)
	if !p.hasDuration {
		totalExpected = p.count * uint64(p.workers)
	}

	var qpsTicks []float64
	var lastTickAt time.Duration
	var lastTickDone uint64
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

loop:
	for {
		select {
		case <-workersDone:
			break loop
		case <-j.ctx.Done():
			break loop
		case <-tick.C:
		}
		elapsed := time.Since(c.start)
		if p.hasDuration && elapsed >= p.duration+2*time.Second {
			break loop // workers should have finished long ago; don't hang
		}
		done := c.stats.done.Load()
		if totalExpected > 0 && done >= totalExpected {
			break loop
		}
		if elapsed-lastTickAt >= time.Second {
			dt := (elapsed - lastTickAt).Seconds()
			qps := float64(done-lastTickDone) / dt
			qpsTicks = append(qpsTicks, qps)
			j.emit(fmt.Sprintf("  [%6.1fs]  sent=%d  qps=%.0f  errors=%d",
				elapsed.Seconds(), done, qps, c.stats.errs.Load()))
			lastTickAt, lastTickDone = elapsed, done
		}
	}
	wcancel()
	<-workersDone
	elapsedTotal := time.Since(c.start)

	c.stats.mu.Lock()
	lines := formatResults(&c.stats, c.stats.done.Load(), c.stats.errs.Load(), elapsedTotal.Seconds(), p, qpsTicks)
	c.stats.mu.Unlock()
	for _, l := range lines {
		j.emit(l)
	}
	if j.statusIs("killed") {
		j.finish("killed", -1)
	} else {
		j.finish("done", 0)
	}
}

// ── Results ──────────────────────────────────────────────────────────────────

func percentOf(n, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}

func spark(vals []float64) string {
	bars := []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	if len(vals) == 0 {
		return ""
	}
	mn, mx := math.Inf(1), math.Inf(-1)
	for _, v := range vals {
		mn = math.Min(mn, v)
		mx = math.Max(mx, v)
	}
	span := math.Max(mx-mn, 1.0)
	var b strings.Builder
	for _, v := range vals {
		idx := int(math.Round((v - mn) / span * 8))
		if idx > 8 {
			idx = 8
		}
		b.WriteRune(bars[idx])
	}
	return b.String()
}

// formatResults renders the summary block. s.mu must be held.
func formatResults(s *sharedStats, sent, errors uint64, elapsed float64, p benchParams, qpsTicks []float64) []string {
	const sep = "§dim§────────────────────────────────────────────────────────§rst§"
	ok := uint64(0)
	if sent > errors {
		ok = sent - errors
	}
	qps := 0.0
	if elapsed > 0 {
		qps = float64(sent) / elapsed
	}
	lat := &s.lat
	p95 := lat.percentileMs(95)

	var lines []string
	lines = append(lines, sep, "")
	lines = append(lines, fmt.Sprintf("§hdr§  %s  %s  [%s]  %d workers  %.2fs§rst§",
		strings.ToUpper(p.protocol), p.host, p.qtype, p.workers, elapsed))
	lines = append(lines, fmt.Sprintf("   %s  %.0f q/s  p95 %.1fms", spark(qpsTicks), qps, p95))
	lines = append(lines, sep)
	if errors == 0 {
		lines = append(lines, fmt.Sprintf("  %d sent  %d ok   0 errors  §ok§✓ 100%% success§rst§", sent, ok))
	} else {
		lines = append(lines, fmt.Sprintf("  %d sent  %d ok   %d errors  (%.1f%%)", sent, ok, errors, percentOf(errors, sent)))
	}
	lines = append(lines, sep)
	lines = append(lines, fmt.Sprintf("  Latency (ms)  min %.2f  mean %.2f  p50 %.2f  p95 %.2f  p99 %.2f  max %.2f",
		lat.minMs(), lat.meanMs(), lat.percentileMs(50), p95, lat.percentileMs(99), lat.maxMs()))
	lines = append(lines, sep)

	// RCODE breakdown: merge every RCODE above 5 under UNKNOWN.
	type rcCount struct {
		label string
		n     uint64
	}
	merged := map[string]uint64{}
	for slot, n := range s.rcodes {
		if n > 0 {
			merged[rcodeLabel(slot)] += n
		}
	}
	var rcs []rcCount
	for l, n := range merged {
		rcs = append(rcs, rcCount{l, n})
	}
	sort.Slice(rcs, func(i, j int) bool {
		if rcs[i].n != rcs[j].n {
			return rcs[i].n > rcs[j].n
		}
		return rcs[i].label < rcs[j].label
	})
	var parts []string
	for _, r := range rcs {
		tag := "§err§"
		if r.label == "NOERROR" {
			tag = "§ok§"
		}
		parts = append(parts, fmt.Sprintf("%s%s§rst§ %d (%.0f%%)", tag, r.label, r.n, percentOf(r.n, sent)))
	}
	lines = append(lines, "  RCODE  "+strings.Join(parts, "   "))

	if len(s.samples) > 0 {
		lines = append(lines, sep, "  Sample errors")
		for _, e := range s.samples {
			lines = append(lines, fmt.Sprintf("    §err§%s§rst§: %s", e.domain, e.reason))
		}
	}
	lines = append(lines, sep, "")
	return lines
}
