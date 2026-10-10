package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newDoHServer starts a DNS-over-HTTPS responder (HTTP/2 on) at path with a self-signed certificate for
// 127.0.0.1 and trusts it. It returns the server string and a counter of the requests seen.
func newDoHServer(t *testing.T, f *fakeDNS, path string) (string, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	crt, key, err := generateSelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(crt, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	old := dotRootCAs
	dotRootCAs = roots
	t.Cleanup(func() { dotRootCAs = old })
	var hits, h2 atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.ProtoMajor == 2 {
			h2.Add(1)
		}
		if r.URL.Path != path || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/dns-message" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		q, _ := io.ReadAll(r.Body)
		if len(q) < 2 || q[0] != 0 || q[1] != 0 {
			http.Error(w, "id must be 0", http.StatusBadRequest)
			return
		}
		a := f.answer(q)
		if a == nil {
			http.Error(w, "drop", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(a)
	}))
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return "https://" + strings.TrimPrefix(srv.URL, "https://") + path, &hits, &h2
}

func TestNormalizeServerHTTPS(t *testing.T) {
	for in, want := range map[string]string{
		"https://1.1.1.1":                        "https://1.1.1.1:443/dns-query",
		"https://1.1.1.1/dns-query":              "https://1.1.1.1:443/dns-query",
		"https://DNS.Example.com/":               "https://dns.example.com:443/dns-query",
		"https://dns.example.com:8443/resolve/x": "https://dns.example.com:8443/resolve/x",
		"https://[2001:db8::53]/dns-query":       "https://[2001:db8::53]:443/dns-query",
		"https://[2001:db8::53]:8443":            "https://[2001:db8::53]:8443/dns-query",
	} {
		got, err := normalizeServer(in)
		if err != nil || got != want {
			t.Errorf("normalizeServer(%q) = %q, %v; want %q", in, got, err, want)
		}
		if again, err := normalizeServer(got); err != nil || again != got {
			t.Errorf("normalizing %q again gave %q, %v", got, again, err)
		}
	}
	for _, bad := range []string{"https://", "https:///dns-query", "https://bad host/x", "https://x/a?b=c", "https://x/a#f", "https://x:99999/", "https://x/a b", "https://x/\x01"} {
		if got, err := normalizeServer(bad); err == nil {
			t.Errorf("normalizeServer(%q) = %q, want an error", bad, got)
		}
	}
}

func TestDoHUpstreamProbeAndForward(t *testing.T) {
	f := newFakeDNS(t)
	addr, hits, h2 := newDoHServer(t, f, "/dns-query")
	p := NewPool(testDNSCfg(addr))
	p.ProbeNow(context.Background())
	if r := rankedAddrs(p); len(r) != 1 || r[0] != addr {
		t.Fatalf("ranked = %v, want [%s] (%d requests)", r, addr, hits.Load())
	}
	q, _ := buildQuery(0x7777, "a.example", "A")
	resp, err := p.Forward(context.Background(), q, false)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := parseHeader(resp); h.id != 0x7777 || !h.qr || h.ancount != 1 {
		t.Fatalf("the caller's ID must come back on the answer: %+v", h)
	}
	if hits.Load() < 3 || h2.Load() != hits.Load() {
		t.Fatalf("requests %d, over HTTP/2 %d: want at least 3, all HTTP/2", hits.Load(), h2.Load())
	}
}

func TestDoHCustomPathAndErrors(t *testing.T) {
	f := newFakeDNS(t)
	addr, _, _ := newDoHServer(t, f, "/resolve/me")
	q := mustQ(t)
	if _, _, err := exchange(context.Background(), addr, q, false, time.Second); err != nil {
		t.Fatalf("custom path: %v", err)
	}
	wrong := strings.TrimSuffix(addr, "/resolve/me") + "/dns-query"
	if _, _, err := exchange(context.Background(), wrong, q, false, time.Second); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("a wrong path must report the HTTP status, got %v", err)
	}
	f.mode.Store(2) // the responder answers 503
	if _, _, err := exchange(context.Background(), addr, q, false, time.Second); err == nil {
		t.Fatal("a 503 was accepted as an answer")
	}
}

func TestDoHRefusesUntrustedCertificateUnlessInsecure(t *testing.T) {
	f := newFakeDNS(t)
	addr, hits, _ := newDoHServer(t, f, "/dns-query")
	dotRootCAs = x509.NewCertPool()
	p := NewPool(testDNSCfg(addr))
	p.ProbeNow(context.Background())
	if len(rankedAddrs(p)) != 0 || hits.Load() != 0 {
		t.Fatalf("an untrusted certificate was accepted (%d requests)", hits.Load())
	}
	cfg := testDNSCfg(addr)
	cfg.TLSInsecure = true
	p2 := NewPool(cfg)
	p2.ProbeNow(context.Background())
	if len(rankedAddrs(p2)) != 1 {
		t.Fatal("tls_insecure did not accept the DoH server")
	}
}

func TestDoHRejectsBadContentTypeAndRedirect(t *testing.T) {
	crt, key, _ := generateSelfSigned()
	cert, _ := tls.X509KeyPair(crt, key)
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	old := dotRootCAs
	dotRootCAs = roots
	t.Cleanup(func() { dotRootCAs = old })
	mux := http.NewServeMux()
	mux.HandleFunc("/html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write(make([]byte, 40))
	})
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/html", http.StatusFound) })
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	base := "https://" + strings.TrimPrefix(srv.URL, "https://")
	for _, path := range []string{"/html", "/redir"} {
		if _, _, err := exchange(context.Background(), base+path, mustQ(t), false, time.Second); err == nil {
			t.Errorf("%s was accepted", path)
		}
	}
}

func trustFrontendCert(t *testing.T) *http.Client {
	t.Helper()
	crt, key, err := generateSelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(crt, key)
	if err != nil {
		t.Fatal(err)
	}
	old := dotCertificate
	dotCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil }
	t.Cleanup(func() { dotCertificate = old })
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	oldRoots := dotRootCAs
	dotRootCAs = roots
	t.Cleanup(func() { dotRootCAs = oldRoots })
	return &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: true, DisableKeepAlives: true}}
}

func TestDoHFrontendServesClients(t *testing.T) {
	up := newFakeDNS(t)
	p := NewPool(testDNSCfg(up.addr))
	p.ProbeNow(context.Background())
	cl := trustFrontendCert(t)
	port, hport := freeTCPPort(t), freeTCPPort(t)
	fe := NewDNSFrontend(mustAddr("127.0.0.1"), port, func() *Pool { return p })
	fe.dohPort = hport
	if err := fe.Start(); err != nil {
		t.Skipf("cannot bind: %v", err)
	}
	defer fe.Stop()
	base := "https://127.0.0.1:" + itoa(hport) + "/dns-query"
	q, _ := buildQuery(0xABCD, "doh.example", "A")

	// POST over HTTP/2
	resp, err := cl.Post(base, "application/dns-message", strings.NewReader(string(q)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.ProtoMajor != 2 || resp.Header.Get("Content-Type") != "application/dns-message" {
		t.Fatalf("POST: %d proto %d type %q", resp.StatusCode, resp.ProtoMajor, resp.Header.Get("Content-Type"))
	}
	if h, _ := parseHeader(body); h.id != 0xABCD || !h.qr || h.ancount != 1 {
		t.Fatalf("POST answer %+v", h)
	}
	// GET with the message as base64url
	resp, err = cl.Get(base + "?dns=" + base64.RawURLEncoding.EncodeToString(q))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if h, _ := parseHeader(body); resp.StatusCode != 200 || h.id != 0xABCD || h.ancount != 1 {
		t.Fatalf("GET: %d %+v", resp.StatusCode, h)
	}
	// refusals
	check := func(name string, want int, do func() (*http.Response, error)) {
		r, err := do()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		r.Body.Close()
		if r.StatusCode != want {
			t.Errorf("%s: status %d, want %d", name, r.StatusCode, want)
		}
	}
	check("wrong path", 404, func() (*http.Response, error) {
		return cl.Post(strings.Replace(base, "/dns-query", "/other", 1), "application/dns-message", strings.NewReader(string(q)))
	})
	check("wrong content type", 415, func() (*http.Response, error) { return cl.Post(base, "text/plain", strings.NewReader(string(q))) })
	check("bad dns parameter", 400, func() (*http.Response, error) { return cl.Get(base + "?dns=***") })
	check("no dns parameter", 400, func() (*http.Response, error) { return cl.Get(base) })
	check("short message", 400, func() (*http.Response, error) {
		return cl.Post(base, "application/dns-message", strings.NewReader("abc"))
	})
	check("PUT", 405, func() (*http.Response, error) {
		req, _ := http.NewRequest(http.MethodPut, base, strings.NewReader(string(q)))
		return cl.Do(req)
	})
	// ddgw's own DoH client against it, end to end
	if _, _, err := exchange(context.Background(), base, q, false, time.Second); err != nil {
		t.Fatalf("own client: %v", err)
	}
	// plain DNS unaffected; the port closes on Stop
	if _, _, err := exchange(context.Background(), "127.0.0.1:"+itoa(port), q, true, time.Second); err != nil {
		t.Fatal(err)
	}
	fe.Stop()
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+itoa(hport), 300*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("the DoH port is still open after Stop")
	}
}

func TestDoHFrontendOffByDefault(t *testing.T) {
	up := newFakeDNS(t)
	p := NewPool(testDNSCfg(up.addr))
	fe := NewDNSFrontend(mustAddr("127.0.0.1"), freeTCPPort(t), func() *Pool { return p })
	if err := fe.Start(); err != nil {
		t.Skip(err)
	}
	defer fe.Stop()
	if fe.doh != nil {
		t.Fatal("a DoH listener exists with doh_port unset")
	}
}

func TestDoHPortValidation(t *testing.T) {
	d := defaultDNS()
	d.DoHPort = 443
	if err := d.Validate(); err != nil {
		t.Fatalf("443 should be valid: %v", err)
	}
	for _, bad := range []int{70000, -1} {
		d.DoHPort = bad
		if d.Validate() == nil {
			t.Fatalf("%d accepted", bad)
		}
	}
	d.DoHPort = d.ListenPort
	if d.Validate() == nil {
		t.Fatal("doh_port equal to listen_port accepted")
	}
	d.DoTPort, d.DoHPort = 853, 853
	if d.Validate() == nil {
		t.Fatal("doh_port equal to dot_port accepted")
	}
	d.DoTPort, d.DoHPort = 0, 0
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "doh_port") {
		t.Fatal("an off doh_port must not be written")
	}
}
