package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	selfSignedValidity = 2 * 365 * 24 * time.Hour
	renewBefore        = 30 * 24 * time.Hour
)

func fingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(sum[:])
}

// selfSignedPaths are where the auto-generated certificate lives: beside the
// config file.
func selfSignedPaths(confPath string) (crt, key string) {
	dir := filepath.Dir(confPath)
	return filepath.Join(dir, "ddgw-web.crt"), filepath.Join(dir, "ddgw-web.key")
}

// loadOrCreateSelfSigned returns the stored self-signed certificate, creating
// (or, when force is set or it is close to expiry, renewing) it as needed.
func loadOrCreateSelfSigned(confPath string, force bool) (tls.Certificate, error) {
	crt, key := selfSignedPaths(confPath)
	if !force {
		if c, err := tls.LoadX509KeyPair(crt, key); err == nil {
			if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil &&
				time.Until(leaf.NotAfter) > renewBefore {
				return c, nil
			}
		}
	}
	certPEM, keyPEM, err := generateSelfSigned()
	if err != nil {
		return tls.Certificate{}, err
	}
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return c, err
	}
	if err := os.MkdirAll(filepath.Dir(crt), 0o755); err == nil {
		if err := writeAtomic(key, keyPEM, 0o600); err == nil {
			err = writeAtomic(crt, certPEM, 0o644)
		}
		if err != nil {
			warnf("web: could not store the self-signed certificate (%v) — a new one is generated at every start", err)
		}
	}
	infof("web: generated self-signed certificate (sha256 %s) — browsers will warn until you install a certificate (GUI: Configure ▸ Web GUI, CLI: --tls-install)", fingerprint(c))
	return c, nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func generateSelfSigned() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, err
	}
	host, _ := os.Hostname()
	names := []string{"localhost"}
	if host != "" {
		names = append(names, host)
	}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLinkLocalUnicast() {
				ips = append(ips, n.IP)
			}
		}
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "ddgw", Organization: []string{"ddgw (DNS Distributed Gateway)"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(selfSignedValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              names,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	if len(der) == 0 || len(kb) == 0 {
		return nil, nil, errors.New("empty certificate")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), nil
}
