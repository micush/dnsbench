package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	c, _ := x509.ParseCertificate(der)
	return &testCA{c, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue signs a leaf for key pub; returns the leaf+CA chain PEM.
func (ca *testCA) issue(t *testing.T, pub *ecdsa.PublicKey, notAfter time.Time, names ...string) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "leaf"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: names,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), ca.pem...)
}

func newTestCertMgr(t *testing.T) (*CertManager, string) {
	t.Helper()
	d := t.TempDir()
	conf := filepath.Join(d, "etc", "ddgw.conf")
	wc := defaultWeb()
	return NewCertManager(filepath.Join(d, "state"), conf, func() WebConfig { return wc }), d
}

func ecKeyPEM(k *ecdsa.PrivateKey) []byte {
	b, _ := x509.MarshalECPrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: b})
}

func TestCertSelfSignedThenInstallThenRevert(t *testing.T) {
	m, _ := newTestCertMgr(t)
	info := m.Status()
	if info.Source != certSourceSelfSigned || !info.SelfSigned || info.Error != "" {
		t.Fatalf("initial: %+v", info)
	}
	first := info.Fingerprint

	ca := newTestCA(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	chain := ca.issue(t, &k.PublicKey, time.Now().Add(90*24*time.Hour), "gw.example.test")
	got, warns, err := m.Install(chain, ecKeyPEM(k), "alice", certSourceInstalled)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != certSourceInstalled || got.ChainLength != 2 || got.InstalledBy != "alice" || got.Fingerprint == first {
		t.Fatalf("installed: %+v", got)
	}
	_ = warns
	// the handshake path serves it with no restart
	c, err := m.GetCertificate(nil)
	if err != nil || fingerprint(*c) != got.Fingerprint {
		t.Fatalf("GetCertificate serves wrong cert: %v", err)
	}
	if b, _, meta, ok := m.Managed(); !ok || len(b) == 0 || meta.By != "alice" {
		t.Fatal("Managed() must return the installed pair")
	}
	if _, err := m.Revert("alice"); err != nil {
		t.Fatal(err)
	}
	if m.Status().Source != certSourceSelfSigned {
		t.Fatalf("revert must fall back to self-signed: %+v", m.Status())
	}
	if _, err := m.Revert("alice"); err == nil {
		t.Fatal("second revert must say there's nothing to remove")
	}
}

func TestCertInstallRejectsBadInput(t *testing.T) {
	m, _ := newTestCertMgr(t)
	ca := newTestCA(t)
	k1, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	k2, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	good := ca.issue(t, &k1.PublicKey, time.Now().Add(48*time.Hour), "x.test")

	cases := []struct {
		name      string
		cert, key []byte
		want      string
	}{
		{"mismatched key", good, ecKeyPEM(k2), "does not match"},
		{"garbage", []byte("nope"), ecKeyPEM(k1), "invalid certificate/key pair"},
		{"no key and no pending csr", good, nil, "no private key"},
		{"expired", ca.issue(t, &k1.PublicKey, time.Now().Add(-time.Minute), "x.test"), ecKeyPEM(k1), "expired"},
	}
	for _, c := range cases {
		_, _, err := m.Install(c.cert, c.key, "t", certSourceInstalled)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v, want substring %q", c.name, err, c.want)
		}
	}
	if m.Status().Source != certSourceSelfSigned {
		t.Error("a rejected install must not change what is served")
	}
	// CA certificate first is refused
	_, _, err := m.Install(ca.pem, ecKeyPEM(k1), "t", certSourceInstalled)
	if err == nil {
		t.Error("installing a CA certificate as the server certificate must fail")
	}
}

func TestCertCSRFlow(t *testing.T) {
	m, _ := newTestCertMgr(t)
	csrPEM, err := m.GenerateCSR("gw.example.test", []string{"gw.example.test", "dns.example.test"}, []string{"10.0.0.53"})
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode([]byte(csrPEM))
	csr, err := x509.ParseCertificateRequest(blk.Bytes)
	if err != nil || csr.CheckSignature() != nil || len(csr.DNSNames) != 2 || len(csr.IPAddresses) != 1 {
		t.Fatalf("csr: %v %+v", err, csr)
	}
	if m.Status().PendingCSR == "" {
		t.Error("status must show the pending CSR")
	}
	if st, _ := os.Stat(m.pendingKeyPath()); st == nil || st.Mode().Perm() != 0o600 {
		t.Error("pending key must be 0600")
	}
	// the CA signs the CSR's public key; install without supplying a key
	ca := newTestCA(t)
	chain := ca.issue(t, csr.PublicKey.(*ecdsa.PublicKey), time.Now().Add(72*time.Hour), "gw.example.test")
	info, _, err := m.Install(chain, nil, "bob", certSourceInstalled)
	if err != nil || info.Source != certSourceInstalled {
		t.Fatalf("install with pending key: %v %+v", err, info)
	}
	if m.Status().PendingCSR != "" {
		t.Error("pending CSR must be consumed")
	}
	// a certificate for some other key must not match a (new) pending key
	m.GenerateCSR("a.test", nil, nil)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if _, _, err := m.Install(ca.issue(t, &other.PublicKey, time.Now().Add(time.Hour*72), "a.test"), nil, "bob", certSourceInstalled); err == nil ||
		!strings.Contains(err.Error(), "pending CSR") {
		t.Errorf("mismatch against pending key: %v", err)
	}
	for _, bad := range [][]string{{"bad name"}, {"x/y"}} {
		if _, err := m.GenerateCSR("", bad, nil); err == nil {
			t.Errorf("csr names %v must be rejected", bad)
		}
	}
	if _, err := m.GenerateCSR("", nil, nil); err == nil {
		t.Error("an empty CSR request must be rejected")
	}
}

func TestCertFilesPrecedenceAndReload(t *testing.T) {
	d := t.TempDir()
	conf := filepath.Join(d, "ddgw.conf")
	ca := newTestCA(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	crt, key := filepath.Join(d, "c.pem"), filepath.Join(d, "k.pem")
	os.WriteFile(crt, ca.issue(t, &k.PublicKey, time.Now().Add(72*time.Hour), "a.test"), 0o644)
	os.WriteFile(key, ecKeyPEM(k), 0o600)
	wc := defaultWeb()
	wc.CertFile, wc.KeyFile = crt, key
	m := NewCertManager(filepath.Join(d, "state"), conf, func() WebConfig { return wc })
	a := m.Status()
	if a.Source != certSourceFiles || a.CertFile != crt {
		t.Fatalf("files source: %+v", a)
	}
	// an installed cert does not override explicit files
	k2, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	m.Install(ca.issue(t, &k2.PublicKey, time.Now().Add(72*time.Hour), "b.test"), ecKeyPEM(k2), "t", certSourceInstalled)
	if m.Status().Source != certSourceFiles {
		t.Fatal("web.cert_file must take precedence")
	}
	// renewing the files is picked up without restart
	k3, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	os.WriteFile(crt, ca.issue(t, &k3.PublicKey, time.Now().Add(96*time.Hour), "c.test"), 0o644)
	os.WriteFile(key, ecKeyPEM(k3), 0o600)
	b := m.Status()
	if b.Fingerprint == a.Fingerprint {
		t.Fatal("renewed certificate files must be reloaded")
	}
	// a broken replacement keeps the old certificate serving
	os.WriteFile(crt, []byte("junk"), 0o644)
	c, err := m.GetCertificate(nil)
	m.Refresh()
	c, err = m.GetCertificate(nil)
	if err != nil || c == nil {
		t.Fatalf("must keep serving the previous certificate: %v", err)
	}
	_ = tls.Certificate{}
}

func TestCertApplyCluster(t *testing.T) {
	m, _ := newTestCertMgr(t)
	ca := newTestCA(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	chain := ca.issue(t, &k.PublicKey, time.Now().Add(72*time.Hour), "gw.test")
	if err := m.ApplyCluster(chain, ecKeyPEM(k)); err != nil {
		t.Fatal(err)
	}
	if s := m.Status(); s.Source != certSourceCluster {
		t.Fatalf("replicated cert: %+v", s)
	}
	if err := m.ApplyCluster(chain, ecKeyPEM(k)); err != nil { // idempotent
		t.Fatal(err)
	}
	if err := m.ApplyCluster(nil, nil); err != nil || m.Status().Source != certSourceSelfSigned {
		t.Fatalf("primary has none: replicated cert must be dropped: %v %+v", err, m.Status())
	}
	// a locally installed cert is not dropped when the primary simply has none
	m.Install(chain, ecKeyPEM(k), "local", certSourceInstalled)
	m.ApplyCluster(nil, nil)
	if m.Status().Source != certSourceInstalled {
		t.Fatal("a locally installed cert must survive 'primary has none'")
	}
}
