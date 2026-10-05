package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// certSource serves a certificate loaded from PEM files and reloads it when
// the files change, so renewed certificates need no restart.
type certSource struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime time.Time
	checked time.Time
}

func (s *certSource) load() error {
	cert, err := tls.LoadX509KeyPair(s.certFile, s.keyFile)
	if err != nil {
		return err
	}
	st, err := os.Stat(s.certFile)
	if err != nil {
		return err
	}
	s.cert, s.modTime = &cert, st.ModTime()
	return nil
}

func (s *certSource) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.checked) > 10*time.Second {
		s.checked = time.Now()
		if st, err := os.Stat(s.certFile); err == nil && st.ModTime().After(s.modTime) {
			if err := s.load(); err != nil {
				log.Printf("tls: reloading %s failed, keeping the old certificate: %v", s.certFile, err)
			} else {
				log.Printf("tls: reloaded %s", s.certFile)
			}
		}
	}
	return s.cert, nil
}

// lastCertSource lets tests reach the most recently built certSource.
var lastCertSource *certSource

// tlsConfig builds the server TLS configuration: the configured PEM pair, or
// a self-signed certificate kept in the state directory.
func tlsConfig(cfg *Config) (*tls.Config, error) {
	certFile, keyFile := cfg.TLSCert, cfg.TLSKey
	if certFile == "" {
		certFile = filepath.Join(cfg.StateDir, "cert.pem")
		keyFile = filepath.Join(cfg.StateDir, "key.pem")
		if !usableCert(certFile, keyFile) {
			if err := generateSelfSigned(certFile, keyFile); err != nil {
				log.Printf("tls: cannot store a certificate in %s (%v); using one in memory", cfg.StateDir, err)
				cert, mErr := selfSignedInMemory()
				if mErr != nil {
					return nil, mErr
				}
				return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
			}
			log.Printf("tls: generated a self-signed certificate: %s", certFile)
		}
	}
	src := &certSource{certFile: certFile, keyFile: keyFile, checked: time.Now()}
	if err := src.load(); err != nil {
		return nil, fmt.Errorf("loading certificate: %w", err)
	}
	lastCertSource = src
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: src.get}, nil
}

// usableCert reports whether the pair exists, parses, and is valid for >30 more days.
func usableCert(certFile, keyFile string) bool {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil || len(pair.Certificate) == 0 {
		return false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return false
	}
	return time.Until(leaf.NotAfter) > 30*24*time.Hour
}

func newSelfSigned() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "dnsbench"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	if h, err := os.Hostname(); err == nil && h != "" && h != "localhost" {
		tmpl.DNSNames = append(tmpl.DNSNames, h)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLinkLocalUnicast() {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ipn.IP)
			}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

func selfSignedInMemory() (tls.Certificate, error) {
	c, k, err := newSelfSigned()
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(c, k)
}

// generateSelfSigned writes a fresh key (mode 0600) and certificate, each
// via a temporary file so a crash never leaves half a file behind.
func generateSelfSigned(certFile, keyFile string) error {
	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return err
	}
	certPEM, keyPEM, err := newSelfSigned()
	if err != nil {
		return err
	}
	if err := writeFileAtomic(keyFile, keyPEM, 0o600); err != nil {
		return err
	}
	return writeFileAtomic(certFile, certPEM, 0o644)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
