package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── Markdown ─────────────────────────────────────────────────────────────────

func TestMarkdownEscapesAndRenders(t *testing.T) {
	md := "# Title <b>\n\nText with `code <x>`, **bold** and [site](https://example.com) and [bad](javascript:alert(1)).\n\n" +
		"```\n<script>alert(1)</script>\n```\n\n- one\n- two\n\n1. first\n2. second\n\n| A | B |\n|---|---|\n| `x` | y |\n\n---\n"
	out := renderMarkdown(md)
	for _, want := range []string{
		"<h3", "Title &lt;b&gt;", "<code>code &lt;x&gt;</code>", "<strong>bold</strong>",
		`<a href="https://example.com" target="_blank" rel="noopener">site</a>`,
		"&lt;script&gt;alert(1)&lt;/script&gt;", "<ul>", "<li>one</li>", "<ol>", "<li>second</li>",
		"<table", "<th>A</th>", "<td><code>x</code></td>", "<hr>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<script>") || strings.Contains(out, `href="javascript:`) {
		t.Fatalf("unsafe output:\n%s", out)
	}
	if strings.Contains(out, "<th>---") {
		t.Fatal("table separator row rendered as data")
	}
}

func TestShippedReadmeRenders(t *testing.T) {
	b, err := os.ReadFile("../README.md")
	if err != nil {
		t.Skip("README not alongside the source")
	}
	out := renderMarkdown(string(b))
	if strings.Count(out, "<pre") != strings.Count(out, "</pre>") || strings.Count(out, "<table") != strings.Count(out, "</table>") {
		t.Fatal("unbalanced tags in rendered README")
	}
	if strings.Contains(out, "```") {
		t.Fatal("code fence leaked into output")
	}
}

// ── Config ───────────────────────────────────────────────────────────────────

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigDefaultsEnvAndFlags(t *testing.T) {
	var buf bytes.Buffer
	c, err := loadConfig(nil, env(nil), &buf)
	if err != nil || c.Port != 8453 || c.Host != "0.0.0.0" || c.StateDir != "/var/lib/dnsbench" ||
		c.SchedulesFile != "/var/lib/dnsbench/schedules.json" || c.NoTLS || c.PAMService != "" || c.LoginGroup != "dnsbench" || !c.AllowUpdates {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	c, err = loadConfig(nil, env(map[string]string{"LISTEN_PORT": "9000", "LISTEN_HOST": "127.0.0.1", "STATE_DIR": "/s", "NO_TLS": "true", "PAM_SERVICE": "x", "LOGIN_GROUP": "netops"}), &buf)
	if err != nil || c.LoginGroup != "netops" || c.Port != 9000 || c.Host != "127.0.0.1" || c.SchedulesFile != "/s/schedules.json" || !c.NoTLS || c.PAMService != "x" {
		t.Fatalf("env: %+v %v", c, err)
	}
	c, err = loadConfig([]string{"--port", "1234", "-host", "::1", "--schedules-file", "/z.json", "--login-group", "domain users"}, env(map[string]string{"LISTEN_PORT": "9000", "LOGIN_GROUP": "netops"}), &buf)
	if err != nil || c.LoginGroup != "domain users" || c.Port != 1234 || c.Host != "::1" || c.SchedulesFile != "/z.json" {
		t.Fatalf("flags must win over env: %+v %v", c, err)
	}
}

func TestConfigRejectsBadInput(t *testing.T) {
	var buf bytes.Buffer
	for name, tc := range map[string]struct {
		args []string
		env  map[string]string
	}{
		"port word":     {nil, map[string]string{"LISTEN_PORT": "http"}},
		"port zero":     {[]string{"--port", "0"}, nil},
		"port too big":  {[]string{"--port", "70000"}, nil},
		"cert only":     {[]string{"--tls-cert", "/c"}, nil},
		"key only":      {nil, map[string]string{"TLS_KEY": "/k"}},
		"stray arg":     {[]string{"extra"}, nil},
		"unknown flag":  {[]string{"--tdns-api", "x"}, nil},
		"old pfx flag":  {[]string{"--pfx", "x"}, nil},
		"old secret":    {[]string{"--secret-key", "x"}, nil},
		"updates typo":  {nil, map[string]string{"ALLOW_UPDATES": "flase"}},
		"updates maybe": {[]string{"--allow-updates=maybe"}, nil},
	} {
		if _, err := loadConfig(tc.args, env(tc.env), &buf); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := loadConfig([]string{"--version"}, env(nil), &buf); !errors.Is(err, errVersion) {
		t.Errorf("--version: %v", err)
	}
	if _, err := loadConfig([]string{"-h"}, env(nil), &buf); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: %v", err)
	}
}

func TestVersionIsPlainInteger(t *testing.T) {
	v := version()
	if v == "" || strings.Trim(v, "0123456789") != "" || strings.HasPrefix(v, "0") {
		t.Fatalf("VERSION must be a plain positive integer, got %q", v)
	}
}

// ── TLS ──────────────────────────────────────────────────────────────────────

func TestSelfSignedCertificate(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "state", "cert.pem"), filepath.Join(dir, "state", "key.pem")
	if err := generateSelfSigned(cert, key); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(key)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode %v", st.Mode().Perm())
	}
	if !usableCert(cert, key) {
		t.Fatal("fresh certificate not usable")
	}
	pair, _ := tls.LoadX509KeyPair(cert, key)
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	if leaf.VerifyHostname("localhost") != nil || leaf.VerifyHostname("127.0.0.1") != nil {
		t.Fatal("certificate does not cover localhost / 127.0.0.1")
	}
	if time.Until(leaf.NotAfter) < 9*365*24*time.Hour {
		t.Fatal("certificate lifetime too short")
	}
	// Partial files are never visible.
	if entries, _ := os.ReadDir(filepath.Dir(cert)); len(entries) != 2 {
		t.Fatalf("leftover files: %v", entries)
	}
	if usableCert(cert, filepath.Join(dir, "missing")) {
		t.Fatal("missing key reported usable")
	}
}

func TestTLSConfigGeneratesReusesAndServes(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{StateDir: dir}
	c1, err := tlsConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if _, err := tlsConfig(cfg); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if !bytes.Equal(first, second) {
		t.Fatal("a valid certificate was regenerated on restart")
	}
	if c1.MinVersion < tls.VersionTLS12 {
		t.Fatal("TLS floor too low")
	}

	// Serve with it and connect.
	ln, err := tls.Listen("tcp", "127.0.0.1:0", c1)
	if err != nil {
		t.Fatal(err)
	}
	// The accepting goroutine must be finished before this test returns: closing a TLS
	// connection reads time.Local, and the scheduler tests that run next replace it.
	done := make(chan struct{})
	defer func() { ln.Close(); <-done }()
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err == nil {
			c.(*tls.Conn).Handshake()
			c.Close()
		}
	}()
	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestCertificateIsReloadedWhenFileChanges(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	generateSelfSigned(cert, key)
	cfg := &Config{StateDir: dir, TLSCert: cert, TLSKey: key}
	c, err := tlsConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := c.GetCertificate(nil)
	serialA := mustLeaf(t, a).SerialNumber

	generateSelfSigned(cert, key)
	future := time.Now().Add(time.Hour)
	os.Chtimes(cert, future, future)
	// The check is throttled to every 10 s; age the marker instead of sleeping.
	src := certSourceOf(t, c)
	src.mu.Lock()
	src.checked = time.Now().Add(-time.Minute)
	src.mu.Unlock()
	b, _ := c.GetCertificate(nil)
	if mustLeaf(t, b).SerialNumber.Cmp(serialA) == 0 {
		t.Fatal("renewed certificate not picked up")
	}

	// A broken replacement keeps the old certificate serving.
	os.WriteFile(cert, []byte("garbage"), 0o644)
	later := time.Now().Add(2 * time.Hour)
	os.Chtimes(cert, later, later)
	src.mu.Lock()
	src.checked = time.Now().Add(-time.Minute)
	src.mu.Unlock()
	d, err := c.GetCertificate(nil)
	if err != nil || mustLeaf(t, d).SerialNumber.Cmp(mustLeaf(t, b).SerialNumber) != 0 {
		t.Fatal("did not keep the previous certificate after a bad reload")
	}
}

func TestTLSConfigErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := tlsConfig(&Config{StateDir: dir, TLSCert: filepath.Join(dir, "no"), TLSKey: filepath.Join(dir, "no")}); err == nil {
		t.Fatal("missing configured certificate accepted")
	}
	// An unwritable state directory falls back to an in-memory certificate.
	file := filepath.Join(dir, "afile")
	os.WriteFile(file, nil, 0o644)
	c, err := tlsConfig(&Config{StateDir: filepath.Join(file, "sub")})
	if err != nil || len(c.Certificates) != 1 {
		t.Fatalf("no in-memory fallback: %v", err)
	}
}

func TestListenError(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	_, err := net.Listen("tcp", l.Addr().String())
	if e := listenError(l.Addr().String(), err); !strings.Contains(e.Error(), "already in use") || !strings.Contains(e.Error(), "LISTEN_PORT") {
		t.Fatalf("unhelpful: %v", e)
	}
}

func mustLeaf(t *testing.T, c *tls.Certificate) *x509.Certificate {
	t.Helper()
	l, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// certSourceOf digs the certSource out of a tls.Config built by tlsConfig.
func certSourceOf(t *testing.T, c *tls.Config) *certSource {
	t.Helper()
	cfg := &Config{}
	_ = cfg
	// GetCertificate is src.get; recover the receiver through a package-level hook.
	if lastCertSource == nil {
		t.Fatal("no certSource recorded")
	}
	return lastCertSource
}

func TestAllowUpdatesSwitch(t *testing.T) {
	var buf bytes.Buffer
	for val, want := range map[string]bool{"": true, "true": true, "TRUE": true, "1": true, "yes": true, "on": true, " true ": true,
		"false": false, "False": false, "0": false, "no": false, "off": false} {
		c, err := loadConfig(nil, env(map[string]string{"ALLOW_UPDATES": val}), &buf)
		if err != nil || c.AllowUpdates != want {
			t.Errorf("ALLOW_UPDATES=%q: %v %v", val, c, err)
		}
	}
	// a typo must stop the daemon, not leave root-level updates on by accident
	for _, bad := range []string{"flase", "disabled", "2", "enable"} {
		if _, err := loadConfig(nil, env(map[string]string{"ALLOW_UPDATES": bad}), &buf); err == nil || !strings.Contains(err.Error(), "ALLOW_UPDATES") {
			t.Errorf("ALLOW_UPDATES=%q accepted (%v)", bad, err)
		}
	}
	c, err := loadConfig([]string{"--allow-updates=false"}, env(map[string]string{"ALLOW_UPDATES": "true"}), &buf)
	if err != nil || c.AllowUpdates {
		t.Errorf("the flag must win over the environment: %v %v", c, err)
	}
	c, err = loadConfig([]string{"--allow-updates=true"}, env(map[string]string{"ALLOW_UPDATES": "false"}), &buf)
	if err != nil || !c.AllowUpdates {
		t.Errorf("the flag must win over the environment: %v %v", c, err)
	}
}

// The Updates page replaces the binary in /opt/dnsbench. The shipped unit mounts the system
// read-only, so it has to say that directory may be written; without this line every update
// fails with a read-only file system.
func TestShippedUnitLetsTheServiceReplaceItsOwnBinary(t *testing.T) {
	b, err := os.ReadFile("../contrib/dnsbench.service")
	if err != nil {
		t.Skip("contrib/ is not next to source/ here")
	}
	unit := string(b)
	if !strings.Contains(unit, "ProtectSystem=strict") {
		t.Fatal("the unit no longer uses ProtectSystem=strict; revisit this test and the README")
	}
	if !strings.Contains(unit, "\nReadWritePaths=-/opt/dnsbench\n") {
		t.Error("the unit does not allow writing /opt/dnsbench, so the Updates page could not install anything")
	}
	if !strings.Contains(unit, "ExecStart=/opt/dnsbench/dnsbench\n") {
		t.Error("ExecStart moved: ReadWritePaths must cover the directory the binary runs from")
	}
	// every other path stays read-only: no blanket write access
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "ReadWritePaths=") && line != "ReadWritePaths=-/opt/dnsbench" {
			t.Errorf("unexpected writable path: %s", line)
		}
	}
	conf, err := os.ReadFile("../contrib/dnsbench.conf")
	if err == nil && !strings.Contains(string(conf), "\nALLOW_UPDATES=true\n") {
		t.Error("the example config does not document ALLOW_UPDATES")
	}
}
