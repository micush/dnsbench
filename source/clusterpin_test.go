package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testIdentity(t *testing.T, names ...string) (tls.Certificate, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "ddgw-node-test"},
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    names,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}, certFingerprint(c)
}

func pinServer(t *testing.T, c tls.Certificate) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{c}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	return "127.0.0.1:" + port
}

func pinGet(addr, fp string) error {
	resp, err := pinnedClient(addr, fp, 5*time.Second).Get(peerURL("/x"))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func TestPinnedClientChecksFingerprint(t *testing.T) {
	good, fp := testIdentity(t, peerServerName)
	addr := pinServer(t, good)
	if err := pinGet(addr, fp); err != nil {
		t.Fatalf("pinned peer refused: %v", err)
	}
	if err := pinGet(addr, fp); err != nil { // second call uses the remembered certificate
		t.Fatalf("second call: %v", err)
	}
	// Right address, wrong pin.
	_, other := testIdentity(t, peerServerName)
	if err := pinGet(addr, other); err == nil {
		t.Fatal("a certificate with another fingerprint was accepted")
	}
	// An impostor on the address: its own certificate does not carry the pin.
	imp, _ := testIdentity(t, peerServerName)
	if err := pinGet(pinServer(t, imp), fp); err == nil {
		t.Fatal("an impostor was accepted")
	}
	// The pin matches but the certificate is for another name.
	wrong, wfp := testIdentity(t, "something-else")
	if err := pinGet(pinServer(t, wrong), wfp); err == nil {
		t.Fatal("a certificate for another name was accepted")
	}
	// A bad address never dials.
	if _, err := dialPinned(context.Background(), "evil@127.0.0.1:1", fp); err == nil {
		t.Fatal("invalid address dialled")
	}
}
