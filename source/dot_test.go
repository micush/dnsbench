package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// newDoTServer starts a DNS-over-TLS responder with a self-signed certificate for 127.0.0.1 and trusts it.
func newDoTServer(t *testing.T, f *fakeDNS) string {
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
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	old := dotRootCAs
	dotRootCAs = pool
	t.Cleanup(func() { dotRootCAs = old })
	l, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var lb [2]byte
				if _, err := io.ReadFull(c, lb[:]); err != nil {
					return
				}
				q := make([]byte, binary.BigEndian.Uint16(lb[:]))
				io.ReadFull(c, q)
				if r := f.answer(q); r != nil {
					c.Write(append([]byte{byte(len(r) >> 8), byte(len(r))}, r...))
				}
			}()
		}
	}()
	return "tls://" + l.Addr().String()
}

func TestNormalizeServerTLS(t *testing.T) {
	for in, want := range map[string]string{
		"tls://1.1.1.1":            "tls://1.1.1.1:853",
		"tls://1.1.1.1:8853":       "tls://1.1.1.1:8853",
		"tls://DNS.Example.com":    "tls://dns.example.com:853",
		"tls://[2001:db8::53]":     "tls://[2001:db8::53]:853",
		"tls://[2001:db8::53]:853": "tls://[2001:db8::53]:853",
		"1.1.1.1":                  "1.1.1.1:53",
		"dns.example.com:5353":     "dns.example.com:5353",
	} {
		got, err := normalizeServer(in)
		if err != nil || got != want {
			t.Errorf("normalizeServer(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"tls://", "tls://bad host", "tls://x:99999", "tls://tls://x"} {
		if got, err := normalizeServer(bad); err == nil {
			t.Errorf("normalizeServer(%q) = %q, want an error", bad, got)
		}
	}
}

func TestDoTUpstreamProbeAndForward(t *testing.T) {
	f := newFakeDNS(t)
	addr := newDoTServer(t, f)
	p := NewPool(testDNSCfg(addr))
	p.ProbeNow(context.Background())
	if r := rankedAddrs(p); len(r) != 1 || r[0] != addr {
		t.Fatalf("ranked = %v, want [%s]", r, addr)
	}
	q, _ := buildQuery(0x1234, "a.example", "A")
	resp, err := p.Forward(context.Background(), q, false) // a UDP client; the TLS server is still reached over TLS
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := parseHeader(resp); h.id != 0x1234 || !h.qr {
		t.Fatalf("bad reply header %+v", h)
	}
	if f.hits.Load() < 3 { // two probes plus the forward
		t.Fatalf("server saw %d queries, want at least 3", f.hits.Load())
	}
}

func TestDoTRefusesUntrustedCertificate(t *testing.T) {
	f := newFakeDNS(t)
	addr := newDoTServer(t, f)
	dotRootCAs = x509.NewCertPool() // trusts nothing: the server's certificate must be refused
	p := NewPool(testDNSCfg(addr))
	p.ProbeNow(context.Background())
	if r := rankedAddrs(p); len(r) != 0 {
		t.Fatalf("an untrusted certificate was accepted: %v", r)
	}
	if f.hits.Load() != 0 {
		t.Fatalf("a query reached a server whose certificate was not trusted")
	}
}

func TestDoTRefusesWrongName(t *testing.T) {
	// a certificate that names only "other.example": a server listed by its IP must be refused
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "other.example"},
		DNSNames:  []string{"other.example"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	old := dotRootCAs
	dotRootCAs = roots
	t.Cleanup(func() { dotRootCAs = old })
	l, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { c.Read(make([]byte, 1)); c.Close() }()
		}
	}()
	_, _, err = exchange(context.Background(), "tls://"+l.Addr().String(), mustQ(t), true, 500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "tls") {
		t.Fatalf("want a TLS name error, got %v", err)
	}
}

func mustQ(t *testing.T) []byte {
	q, err := buildQuery(7, "a.example", "A")
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestDoTInsecureAcceptsAnyCertificate(t *testing.T) {
	f := newFakeDNS(t)
	addr := newDoTServer(t, f)
	dotRootCAs = x509.NewCertPool() // trusts nothing, so only tls_insecure can let it through
	cfg := testDNSCfg(addr)
	cfg.TLSInsecure = true
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	if r := rankedAddrs(p); len(r) != 1 {
		t.Fatalf("tls_insecure did not accept the server: %v", r)
	}
	q, _ := buildQuery(0x4321, "a.example", "A")
	if _, err := p.Forward(context.Background(), q, true); err != nil {
		t.Fatal(err)
	}
	cfg.TLSInsecure = false
	p2 := NewPool(cfg)
	p2.ProbeNow(context.Background())
	if r := rankedAddrs(p2); len(r) != 0 {
		t.Fatalf("with tls_insecure off the same server must be refused: %v", r)
	}
}

func TestTLSInsecureConfigKey(t *testing.T) {
	d := defaultDNS()
	if d.TLSInsecure {
		t.Fatal("tls_insecure must be off by default")
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "tls_insecure") {
		t.Fatal("an off tls_insecure must not be written")
	}
	d.TLSInsecure = true
	b, _ = json.Marshal(d)
	var back DNSConfig
	if err := json.Unmarshal(b, &back); err != nil || !back.TLSInsecure {
		t.Fatalf("round trip: %v %v", err, back.TLSInsecure)
	}
}

func freeTCPPort(t *testing.T) int {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestDoTFrontendServesClients(t *testing.T) {
	up := newFakeDNS(t)
	p := NewPool(testDNSCfg(up.addr))
	p.ProbeNow(context.Background())
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

	port, dport := freeTCPPort(t), freeTCPPort(t)
	fe := NewDNSFrontend(mustAddr("127.0.0.1"), port, func() *Pool { return p })
	fe.dotPort = dport
	if err := fe.Start(); err != nil {
		t.Skipf("cannot bind: %v", err)
	}
	defer fe.Stop()

	srv := "tls://127.0.0.1:" + itoa(dport)
	q, _ := buildQuery(0xD0D0, "dot.example", "A")
	resp, _, err := exchange(context.Background(), srv, q, true, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := parseHeader(resp); h.id != 0xD0D0 || h.ancount != 1 {
		t.Fatalf("dot reply %+v", h)
	}
	// a client that does not trust the certificate refuses it; one that skips the check is served
	dotRootCAs = x509.NewCertPool()
	if _, _, err := exchange(context.Background(), srv, q, true, time.Second); err == nil {
		t.Fatal("an untrusted certificate was accepted")
	}
	if _, _, err := exchangeOpt(context.Background(), srv, q, true, time.Second, true); err != nil {
		t.Fatalf("skipping the check should work: %v", err)
	}
	// plain TCP to the DoT port is not DNS: it gets no answer
	if _, _, err := exchange(context.Background(), "127.0.0.1:"+itoa(dport), q, true, 300*time.Millisecond); err == nil {
		t.Fatal("plain DNS was answered on the DoT port")
	}
	// plain DNS still works on its own port
	if _, _, err := exchange(context.Background(), "127.0.0.1:"+itoa(port), q, true, time.Second); err != nil {
		t.Fatal(err)
	}
	fe.Stop()
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+itoa(dport), 300*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("the DoT port is still open after Stop")
	}
}

func TestDoTFrontendOffByDefault(t *testing.T) {
	up := newFakeDNS(t)
	p := NewPool(testDNSCfg(up.addr))
	fe := NewDNSFrontend(mustAddr("127.0.0.1"), freeTCPPort(t), func() *Pool { return p })
	if err := fe.Start(); err != nil {
		t.Skip(err)
	}
	defer fe.Stop()
	if fe.dot != nil {
		t.Fatal("a DoT listener exists with dot_port unset")
	}
}

func TestDoTPortValidation(t *testing.T) {
	d := defaultDNS()
	d.DoTPort = 853
	if err := d.Validate(); err != nil {
		t.Fatalf("853 should be valid: %v", err)
	}
	d.DoTPort = 70000
	if d.Validate() == nil {
		t.Fatal("70000 accepted")
	}
	d.DoTPort = -1
	if d.Validate() == nil {
		t.Fatal("-1 accepted")
	}
	d.DoTPort = d.ListenPort
	if d.Validate() == nil {
		t.Fatal("dot_port equal to listen_port accepted")
	}
	d.DoTPort = 0
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "dot_port") {
		t.Fatal("an off dot_port must not be written")
	}
}
