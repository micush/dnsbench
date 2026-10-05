package main

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	return newApp(&Config{StateDir: dir, SchedulesFile: filepath.Join(dir, "schedules.json"), NoTLS: true, LoginGroup: defaultLoginGroup})
}

func shortTimeout(t *testing.T, d time.Duration) {
	old := queryTimeout
	queryTimeout = d
	t.Cleanup(func() { queryTimeout = old })
}

var reSummary = regexp.MustCompile(`(\d+) sent\s+(\d+) ok\s+(\d+) errors`)

type result struct {
	lines  []string
	status string
	sent   int
	ok     int
	errs   int
}

func (r result) text() string { return stripTags(strings.Join(r.lines, "\n")) }

func runJobArgs(t *testing.T, a *App, args map[string]string) result {
	t.Helper()
	j := a.startJob(args, "test")
	// A stopped Timer, not time.After: an abandoned time.After keeps running for the full 30 s,
	// then fires during whichever later test is changing time.Local, which the race detector reports.
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	select {
	case <-j.done:
	case <-timeout.C:
		j.kill()
		t.Fatalf("job did not finish:\n%s", stripTags(strings.Join(j.allLines(), "\n")))
	}
	lines, status, _ := j.snapshot(0)
	r := result{lines: lines, status: status}
	for _, l := range lines {
		if m := reSummary.FindStringSubmatch(stripTags(l)); m != nil {
			r.sent, _ = strconv.Atoi(m[1])
			r.ok, _ = strconv.Atoi(m[2])
			r.errs, _ = strconv.Atoi(m[3])
			break
		}
	}
	return r
}

func args(kv ...string) map[string]string {
	m := map[string]string{"queries": "a.example\nb.example\nc.example", "recurse": "true"}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

// ── Responders ───────────────────────────────────────────────────────────────

func reply(q []byte, rcode byte) []byte {
	r := append([]byte(nil), q...)
	r[2] |= 0x80
	r[3] = r[3]&0xf0 | rcode
	return r
}

type udpServer struct {
	addr  string
	count atomic.Uint64
	conn  net.PacketConn
}

// startUDP answers every query with rcode, except when drop(n) says to ignore
// the n-th one. dup makes it answer twice.
func startUDP(t *testing.T, rcode byte, drop func(n uint64) bool, dup bool) *udpServer {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pc.(*net.UDPConn).SetReadBuffer(4 << 20)
	s := &udpServer{addr: pc.LocalAddr().String(), conn: pc}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			c := s.count.Add(1)
			if drop != nil && drop(c) {
				continue
			}
			resp := reply(buf[:n], rcode)
			pc.WriteTo(resp, from)
			if dup {
				pc.WriteTo(resp, from)
			}
		}
	}()
	return s
}

func closedUDPPort(t *testing.T) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	pc.Close()
	return addr
}

// serveFramed handles DNS-over-TCP framing on l. closeAfter > 0 closes each
// connection after that many answers, to exercise reconnects.
func serveFramed(l net.Listener, rcode byte, closeAfter int) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			served := 0
			for {
				var lb [2]byte
				if _, err := io.ReadFull(c, lb[:]); err != nil {
					return
				}
				q := make([]byte, int(lb[0])<<8|int(lb[1]))
				if _, err := io.ReadFull(c, q); err != nil {
					return
				}
				r := reply(q, rcode)
				c.Write(append([]byte{byte(len(r) >> 8), byte(len(r))}, r...))
				served++
				if closeAfter > 0 && served >= closeAfter {
					return
				}
			}
		}(c)
	}
}

// ── UDP ──────────────────────────────────────────────────────────────────────

func TestUDPCountMode(t *testing.T) {
	srv := startUDP(t, 0, nil, false)
	r := runJobArgs(t, testApp(t), args("server", srv.addr, "protocol", "udp", "concurrency", "3", "pipeline", "8", "count", "400"))
	if r.status != "done" || r.sent != 1200 || r.errs != 0 || r.ok != 1200 {
		t.Fatalf("status=%s sent=%d ok=%d errs=%d\n%s", r.status, r.sent, r.ok, r.errs, r.text())
	}
	if got := srv.count.Load(); got != 1200 {
		t.Fatalf("server saw %d queries, want 1200", got)
	}
	if !strings.Contains(r.text(), "100% success") {
		t.Fatalf("no success line:\n%s", r.text())
	}
}

func TestUDPWindowSizes(t *testing.T) {
	// Small windows with several workers; a 250-deep window with one worker
	// (a burst bigger than that would overflow the loopback responder's socket
	// buffer and legitimately time out, which is not what is being tested).
	cases := []struct{ window, workers string }{{"1", "2"}, {"2", "2"}, {"3", "2"}, {"64", "2"}, {"250", "1"}}
	for _, c := range cases {
		t.Run("pipeline="+c.window, func(t *testing.T) {
			srv := startUDP(t, 0, nil, false)
			r := runJobArgs(t, testApp(t), args("server", srv.addr, "concurrency", c.workers, "pipeline", c.window, "count", "700"))
			want := 700 * atoiOr(c.workers, 1)
			if r.sent != want || r.errs != 0 {
				t.Fatalf("sent=%d (want %d) errs=%d\n%s", r.sent, want, r.errs, r.text())
			}
		})
	}
}

// A huge window, paced so bursts stay small: slots are recycled thousands of
// times, which exercises generation wrap-around in the transaction IDs.
func TestUDPHugeWindowSlotReuse(t *testing.T) {
	srv := startUDP(t, 0, nil, false)
	r := runJobArgs(t, testApp(t), args("server", srv.addr, "concurrency", "1", "pipeline", "4096", "duration", "2s", "rate_limit", "3000"))
	if r.errs != 0 || r.sent < 4500 {
		t.Fatalf("sent=%d errs=%d\n%s", r.sent, r.errs, r.text())
	}
	if uint64(r.sent) != srv.count.Load() {
		t.Fatalf("counted %d, server saw %d", r.sent, srv.count.Load())
	}
}

func TestUDPDurationMode(t *testing.T) {
	srv := startUDP(t, 0, nil, false)
	start := time.Now()
	r := runJobArgs(t, testApp(t), args("server", srv.addr, "concurrency", "2", "pipeline", "16", "duration", "1s"))
	el := time.Since(start)
	if r.status != "done" || r.sent < 100 || r.errs != 0 {
		t.Fatalf("status=%s sent=%d errs=%d\n%s", r.status, r.sent, r.errs, r.text())
	}
	if el < 900*time.Millisecond || el > 3*time.Second {
		t.Fatalf("1s run took %v", el)
	}
	// Every query sent was answered and counted: nothing lost at the end.
	if uint64(r.sent) != srv.count.Load() {
		t.Fatalf("counted %d, server saw %d", r.sent, srv.count.Load())
	}
}

func TestUDPErrorRcodesCounted(t *testing.T) {
	srv := startUDP(t, 3, nil, false)
	r := runJobArgs(t, testApp(t), args("server", srv.addr, "concurrency", "1", "pipeline", "4", "count", "50"))
	if r.sent != 50 || r.errs != 50 || r.ok != 0 {
		t.Fatalf("sent=%d errs=%d\n%s", r.sent, r.errs, r.text())
	}
	if !strings.Contains(r.text(), "NXDOMAIN 50 (100%)") || !strings.Contains(r.text(), "NXDOMAIN") {
		t.Fatalf("rcode breakdown missing:\n%s", r.text())
	}
}

func TestUDPTimeouts(t *testing.T) {
	shortTimeout(t, 300*time.Millisecond)
	srv := startUDP(t, 0, func(uint64) bool { return true }, false)
	r := runJobArgs(t, testApp(t), args("server", srv.addr, "concurrency", "1", "pipeline", "10", "count", "20"))
	if r.sent != 20 || r.errs != 20 {
		t.Fatalf("sent=%d errs=%d\n%s", r.sent, r.errs, r.text())
	}
	if !strings.Contains(r.text(), "timeout") {
		t.Fatalf("no timeout sample:\n%s", r.text())
	}
}

func TestUDPPartialLoss(t *testing.T) {
	shortTimeout(t, 300*time.Millisecond)
	srv := startUDP(t, 0, func(n uint64) bool { return n%5 == 0 }, false)
	r := runJobArgs(t, testApp(t), args("server", srv.addr, "concurrency", "2", "pipeline", "8", "count", "100"))
	if r.sent != 200 || r.errs != 40 || r.ok != 160 {
		t.Fatalf("sent=%d ok=%d errs=%d\n%s", r.sent, r.ok, r.errs, r.text())
	}
}

func TestUDPDuplicateRepliesNotDoubleCounted(t *testing.T) {
	srv := startUDP(t, 0, nil, true)
	r := runJobArgs(t, testApp(t), args("server", srv.addr, "concurrency", "2", "pipeline", "8", "count", "300"))
	if r.sent != 600 || r.errs != 0 {
		t.Fatalf("sent=%d errs=%d\n%s", r.sent, r.errs, r.text())
	}
}

func TestUDPConnectionRefused(t *testing.T) {
	shortTimeout(t, 400*time.Millisecond)
	r := runJobArgs(t, testApp(t), args("server", closedUDPPort(t), "concurrency", "1", "pipeline", "4", "count", "10"))
	if r.sent != 10 || r.errs != 10 {
		t.Fatalf("sent=%d errs=%d\n%s", r.sent, r.errs, r.text())
	}
	if !strings.Contains(r.text(), "connection refused") {
		t.Fatalf("expected a 'connection refused' sample:\n%s", r.text())
	}
}

func TestUDPRateLimit(t *testing.T) {
	srv := startUDP(t, 0, nil, false)
	r := runJobArgs(t, testApp(t), args("server", srv.addr, "concurrency", "2", "pipeline", "16", "duration", "2s", "rate_limit", "400"))
	// 400 q/s for 2 s ≈ 800; allow generous slack for timer granularity on a busy box.
	if r.sent < 560 || r.sent > 880 {
		t.Fatalf("sent %d queries, expected about 800\n%s", r.sent, r.text())
	}
}

func TestKillStopsJobQuickly(t *testing.T) {
	a := testApp(t)
	srv := startUDP(t, 0, nil, false)
	j := a.startJob(args("server", srv.addr, "protocol", "udp", "duration", "60s"), "test")
	time.Sleep(400 * time.Millisecond)
	start := time.Now()
	j.kill()
	timeout := time.NewTimer(5 * time.Second) // stopped on return; see runJobArgs
	defer timeout.Stop()
	select {
	case <-j.done:
	case <-timeout.C:
		t.Fatal("job still running 5s after kill")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("kill took %v", d)
	}
	if !j.statusIs("killed") {
		t.Fatal("status should be killed")
	}
}

func TestStartJobReplacesRunningJob(t *testing.T) {
	a := testApp(t)
	srv := startUDP(t, 0, nil, false)
	first := a.startJob(args("server", srv.addr, "duration", "60s"), "u")
	time.Sleep(200 * time.Millisecond)
	second := a.startJob(args("server", srv.addr, "count", "10"), "u")
	select {
	case <-first.done:
	default:
		t.Fatal("first job still running after the second started")
	}
	if !first.statusIs("killed") {
		t.Fatal("first job should be marked killed")
	}
	<-second.done
}

func TestUnresolvableServerFailsCleanly(t *testing.T) {
	r := runJobArgs(t, testApp(t), args("server", "no-such-host.invalid", "count", "1"))
	if r.status != "error" || !strings.Contains(r.text(), "cannot resolve") {
		t.Fatalf("status=%s\n%s", r.status, r.text())
	}
}

// ── TCP and DoT ──────────────────────────────────────────────────────────────

func TestTCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go serveFramed(l, 0, 0)
	r := runJobArgs(t, testApp(t), args("server", l.Addr().String(), "protocol", "tcp", "concurrency", "3", "count", "100"))
	if r.sent != 300 || r.errs != 0 {
		t.Fatalf("sent=%d errs=%d\n%s", r.sent, r.errs, r.text())
	}
}

func TestTCPReconnectsWhenServerClosesConnections(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go serveFramed(l, 0, 3) // hang up after every 3 answers
	r := runJobArgs(t, testApp(t), args("server", l.Addr().String(), "protocol", "tcp", "concurrency", "2", "count", "60"))
	if r.sent != 120 || r.errs != 0 {
		t.Fatalf("sent=%d errs=%d\n%s", r.sent, r.errs, r.text())
	}
}

func TestTCPConnectionRefused(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	r := runJobArgs(t, testApp(t), args("server", addr, "protocol", "tcp", "concurrency", "1", "count", "3"))
	if r.sent != 3 || r.errs != 3 || !strings.Contains(r.text(), "connect failed") {
		t.Fatalf("sent=%d errs=%d\n%s", r.sent, r.errs, r.text())
	}
}

func TestDoT(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	if err := generateSelfSigned(cert, key); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go serveFramed(l, 0, 0)

	r := runJobArgs(t, testApp(t), args("server", l.Addr().String(), "protocol", "dot", "concurrency", "2", "count", "50", "insecure", "true"))
	if r.sent != 100 || r.errs != 0 {
		t.Fatalf("insecure: sent=%d errs=%d\n%s", r.sent, r.errs, r.text())
	}
	// With verification on, the untrusted self-signed certificate must be refused.
	r = runJobArgs(t, testApp(t), args("server", l.Addr().String(), "protocol", "dot", "concurrency", "1", "count", "2", "insecure", "false"))
	if r.errs != 2 || !strings.Contains(r.text(), "TLS handshake failed") {
		t.Fatalf("verify on: errs=%d\n%s", r.errs, r.text())
	}
}

// ── DoH ──────────────────────────────────────────────────────────────────────

type dohSeen struct {
	mu     sync.Mutex
	protos map[string]int
	meths  map[string]int
	ct     string
	accept string
	host   string // Host header of the last request
	sni    string // TLS server name of the last request
}

func startDoH(t *testing.T, status int) (*httptest.Server, *dohSeen) {
	seen := &dohSeen{protos: map[string]int{}, meths: map[string]int{}}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q []byte
		var err error
		if r.Method == http.MethodGet {
			q, err = base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
		} else {
			q, err = io.ReadAll(r.Body)
		}
		seen.mu.Lock()
		seen.protos[r.Proto]++
		seen.meths[r.Method]++
		seen.ct, seen.accept = r.Header.Get("Content-Type"), r.Header.Get("Accept")
		seen.host = r.Host
		if r.TLS != nil {
			seen.sni = r.TLS.ServerName
		}
		seen.mu.Unlock()
		if err != nil || len(q) < 12 {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		if status != 200 {
			http.Error(w, "nope", status)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(reply(q, 0))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestDoHMethodsAndVersions(t *testing.T) {
	cases := []struct{ method, ver, wantProto, wantMethod string }{
		{"post", "1.1", "HTTP/1.1", "POST"},
		{"get", "1.1", "HTTP/1.1", "GET"},
		{"post", "2", "HTTP/2.0", "POST"},
		{"get", "2", "HTTP/2.0", "GET"},
	}
	for _, c := range cases {
		t.Run(c.method+"/"+c.ver, func(t *testing.T) {
			srv, seen := startDoH(t, 200)
			addr := strings.TrimPrefix(srv.URL, "https://")
			r := runJobArgs(t, testApp(t), args("server", addr, "protocol", "doh", "concurrency", "2", "count", "40",
				"doh_method", c.method, "doh_protocol", c.ver))
			if r.sent != 80 || r.errs != 0 {
				t.Fatalf("sent=%d errs=%d\n%s", r.sent, r.errs, r.text())
			}
			seen.mu.Lock()
			defer seen.mu.Unlock()
			if seen.protos[c.wantProto] != 80 || seen.meths[c.wantMethod] != 80 {
				t.Fatalf("protos=%v methods=%v", seen.protos, seen.meths)
			}
			if c.wantMethod == "POST" && seen.ct != "application/dns-message" {
				t.Fatalf("Content-Type = %q", seen.ct)
			}
			if seen.accept != "application/dns-message" {
				t.Fatalf("Accept = %q", seen.accept)
			}
		})
	}
}

func TestDoHVerifyAndHTTPErrors(t *testing.T) {
	srv, _ := startDoH(t, 200)
	addr := strings.TrimPrefix(srv.URL, "https://")
	r := runJobArgs(t, testApp(t), args("server", addr, "protocol", "doh", "concurrency", "1", "count", "2", "insecure", "false"))
	if r.errs != 2 {
		t.Fatalf("untrusted certificate accepted: errs=%d\n%s", r.errs, r.text())
	}

	srv2, _ := startDoH(t, 503)
	r = runJobArgs(t, testApp(t), args("server", strings.TrimPrefix(srv2.URL, "https://"), "protocol", "doh", "concurrency", "1", "count", "3"))
	if r.errs != 3 || !strings.Contains(r.text(), "HTTP 503") {
		t.Fatalf("errs=%d\n%s", r.errs, r.text())
	}
}

// ── Parameters ───────────────────────────────────────────────────────────────

func TestParseParamsDefaultsAndLimits(t *testing.T) {
	p, _, err := parseParams(map[string]string{"server": "192.0.2.1"}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if p.protocol != "udp" || p.workers != 7 || p.window != 16 || p.hasDuration || p.count != 100 ||
		!p.recurse || !p.insecure || p.qtype != "A" || len(p.domains) != 3 || p.port != "53" {
		t.Fatalf("unexpected defaults: %+v", p)
	}
	p, _, _ = parseParams(map[string]string{"server": "x", "concurrency": "999999", "pipeline": "0", "rate_limit": "1000", "concurrency2": ""}, 4)
	if p.workers != maxWorkers || p.window != 1 {
		t.Fatalf("limits not applied: workers=%d window=%d", p.workers, p.window)
	}
	p, _, _ = parseParams(map[string]string{"server": "x", "concurrency": "3", "rate_limit": "10"}, 4)
	if p.ratePerWorker != 4 || p.rateEffective != 12 {
		t.Fatalf("rate rounding: per=%d effective=%d", p.ratePerWorker, p.rateEffective)
	}
	if _, _, err := parseParams(map[string]string{"server": "x", "protocol": "doq"}, 4); err == nil || !strings.Contains(err.Error(), "DoQ") {
		t.Fatalf("doq: %v", err)
	}
	if _, _, err := parseParams(map[string]string{"server": "x", "protocol": "gopher"}, 4); err == nil {
		t.Fatal("unknown protocol accepted")
	}
	p, _, _ = parseParams(map[string]string{"server": "x", "qtype": "MX"}, 4) // the scheduler's key
	if p.qtype != "MX" {
		t.Fatalf("qtype key ignored: %s", p.qtype)
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"30s": 30 * time.Second, "5m": 5 * time.Minute, "1h": time.Hour, "45": 45 * time.Second,
		"1.5m": 90 * time.Second, "junk": 30 * time.Second, "0s": 30 * time.Second, "-5s": 30 * time.Second,
	} {
		if got := parseDuration(in); got != want {
			t.Errorf("%q = %v, want %v", in, got, want)
		}
	}
}

func TestParseServer(t *testing.T) {
	cases := []struct{ in, proto, host, port, path string }{
		{"8.8.8.8", "udp", "8.8.8.8", "53", "/dns-query"},
		{"8.8.8.8:5353", "udp", "8.8.8.8", "5353", "/dns-query"},
		{"dns.example", "dot", "dns.example", "853", "/dns-query"},
		{"dns.example", "doh", "dns.example", "443", "/dns-query"},
		{"https://dns.example/custom", "doh", "dns.example", "443", "/custom"},
		{"https://dns.example:8443/dns-query", "doh", "dns.example", "8443", "/dns-query"},
		{"[2001:db8::1]:5353", "udp", "2001:db8::1", "5353", "/dns-query"},
		{"2001:db8::1", "udp", "2001:db8::1", "53", "/dns-query"},
		{"::1", "udp", "::1", "53", "/dns-query"},
	}
	for _, c := range cases {
		h, p, path := parseServer(c.in, c.proto)
		if h != c.host || p != c.port || path != c.path {
			t.Errorf("%s/%s -> %q %q %q", c.in, c.proto, h, p, path)
		}
	}
}

func TestInvalidDomainsAreSkippedWithWarning(t *testing.T) {
	srv := startUDP(t, 0, nil, false)
	r := runJobArgs(t, testApp(t), args("server", srv.addr, "count", "10", "concurrency", "1",
		"queries", "good.example\n"+strings.Repeat("x", 70)+".example"))
	if r.sent != 10 || r.errs != 0 || !strings.Contains(r.text(), "skipping") || !strings.Contains(r.text(), "Domains    : 1") {
		t.Fatalf("sent=%d errs=%d\n%s", r.sent, r.errs, r.text())
	}
	r = runJobArgs(t, testApp(t), args("server", srv.addr, "count", "1", "queries", "a..b"))
	if r.status != "error" || !strings.Contains(r.text(), "no valid domain") {
		t.Fatalf("status=%s\n%s", r.status, r.text())
	}
}

func TestOutputFormatMatchesWhatTheUIParses(t *testing.T) {
	srv := startUDP(t, 0, nil, false)
	r := runJobArgs(t, testApp(t), args("server", srv.addr, "count", "30", "concurrency", "1"))
	text := r.text()
	for _, re := range []string{`[0-9][0-9,.]+ q/s`, `\d+ sent`, `\d+ errors`, ` p95 [0-9][0-9.]*`, `Latency \(ms\)  min`, `RCODE  NOERROR 30 \(100%\)`, `Workers    : 1`, `Queue      : \d+`} {
		if !regexp.MustCompile(re).MatchString(text) {
			t.Errorf("output does not match /%s/:\n%s", re, text)
		}
	}
	if fmt.Sprint(strings.Count(text, "§")) != "0" {
		t.Error("stripTags left markers")
	}
}

// A server that is down must never produce a clean-looking report.
func TestDurationRunAgainstDeadServerReportsFailures(t *testing.T) {
	shortTimeout(t, 300*time.Millisecond)
	cases := map[string]string{
		"closed port": closedUDPPort(t),
		"black hole":  startUDP(t, 0, func(uint64) bool { return true }, false).addr,
	}
	for name, addr := range cases {
		t.Run(name, func(t *testing.T) {
			r := runJobArgs(t, testApp(t), args("server", addr, "concurrency", "2", "pipeline", "8", "duration", "1s"))
			if r.sent == 0 || r.errs != r.sent || r.ok != 0 {
				t.Fatalf("sent=%d ok=%d errs=%d\n%s", r.sent, r.ok, r.errs, r.text())
			}
			if strings.Contains(r.text(), "100% success") {
				t.Fatalf("dead server reported as success:\n%s", r.text())
			}
		})
	}
}

// The request goes to the address that was resolved, but the name the user typed
// is still what the server sees as the Host header and the TLS server name.
func TestDoHKeepsTypedNameForHostAndSNI(t *testing.T) {
	srv, seen := startDoH(t, 200)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	for _, ver := range []string{"1.1", "2"} {
		t.Run("http"+ver, func(t *testing.T) {
			r := runJobArgs(t, testApp(t), args("server", "localhost:"+port, "protocol", "doh", "concurrency", "1",
				"count", "3", "doh_protocol", ver))
			if r.sent != 3 || r.errs != 0 {
				t.Fatalf("sent=%d errs=%d\n%s", r.sent, r.errs, r.text())
			}
			seen.mu.Lock()
			defer seen.mu.Unlock()
			if seen.host != "localhost:"+port || seen.sni != "localhost" {
				t.Fatalf("server saw Host %q and SNI %q, want %q and %q", seen.host, seen.sni, "localhost:"+port, "localhost")
			}
		})
	}
}

func TestCheckTarget(t *testing.T) {
	good := []struct{ in, proto string }{
		{"8.8.8.8", "udp"}, {"8.8.8.8:5353", "udp"}, {"dns.example", "dot"}, {"dns_1.example.", "tcp"},
		{"2001:db8::1", "udp"}, {"[2001:db8::1]:5353", "udp"}, {"https://dns.example:8443/dns-query", "doh"},
		{"https://dns.example/a/b%20c", "doh"}, {"127.0.0.1:65535", "udp"},
	}
	for _, c := range good {
		if _, _, err := parseParams(args("server", c.in, "protocol", c.proto), 1); err != nil {
			t.Errorf("%s/%s refused: %v", c.in, c.proto, err)
		}
	}
	bad := []struct{ in, proto string }{
		{"[::1]:99999", "udp"},              // port out of range
		{"[::1]:0", "udp"},                  // port zero
		{"[::1]:abc", "udp"},                // port not a number
		{"user@evil.example", "doh"},        // userinfo
		{"evil.example:80:90", "udp"},       // two colons that are not an IPv6 address
		{"a b.example", "udp"},              // whitespace
		{"evil.example%0d%0aX: y", "doh"},   // escape sequence
		{"https://dns.example/\x01", "doh"}, // control character in the path
		{"https://dns.example/a\nb", "doh"}, // newline in the path
		{"https://", "doh"},                 // no host
		{"dns.example\\evil", "udp"},        // backslash
	}
	for _, c := range bad {
		if _, _, err := parseParams(args("server", c.in, "protocol", c.proto), 1); err == nil {
			t.Errorf("%q/%s was accepted", c.in, c.proto)
		}
	}
	// The port is rewritten from the checked number.
	p, _, err := parseParams(args("server", "dns.example:0053", "protocol", "udp"), 1)
	if err != nil || p.port != "53" {
		t.Errorf("port = %q, err = %v, want 53", p.port, err)
	}
	// A path is only examined for DoH.
	if _, _, err := parseParams(args("server", "dns.example/\x01", "protocol", "udp"), 1); err != nil {
		t.Errorf("udp server with a path-like suffix refused: %v", err)
	}
}
