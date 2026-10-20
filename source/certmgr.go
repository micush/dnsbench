package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TLS certificate management for the web GUI, after umiss's admintls: a
// certificate can be installed, replaced or reverted live (the listener reads
// it through GetCertificate on every handshake — no restart), a CSR can be
// generated for an externally issued certificate, and expiry is reported.
//
// Source precedence, highest first:
//
//	files      web.cert_file / web.key_file in the config (re-read when the
//	           files change, so ACME-style renewals are picked up)
//	installed  installed through the GUI/CLI (or replicated by the cluster)
//	self-signed generated beside the config file, renewed near expiry
//
// This is the *GUI* certificate.  Cluster peers authenticate with a separate
// per-node identity (cluster.go) so replacing this certificate can never
// break the cluster.

const (
	certSourceFiles      = "files"
	certSourceInstalled  = "installed"
	certSourceCluster    = "cluster"
	certSourceSelfSigned = "self-signed"

	certRecheck  = 10 * time.Second
	expiryWarn   = 30 * 24 * time.Hour
	maxPEMBytes  = 256 << 10
	maxCSRNames  = 50
	maxCSRNameLn = 253
)

// CertInfo describes the certificate currently served.
type CertInfo struct {
	Source      string    `json:"source"`
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	DNSNames    []string  `json:"dns_names"`
	IPAddresses []string  `json:"ip_addresses"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	DaysLeft    int       `json:"days_left"`
	Expired     bool      `json:"expired"`
	ExpiresSoon bool      `json:"expires_soon"`
	SelfSigned  bool      `json:"self_signed"`
	ChainLength int       `json:"chain_length"`
	Fingerprint string    `json:"fingerprint"`
	CertFile    string    `json:"cert_file,omitempty"`
	KeyFile     string    `json:"key_file,omitempty"`
	InstalledBy string    `json:"installed_by,omitempty"`
	InstalledAt time.Time `json:"installed_at,omitempty"`
	PendingCSR  string    `json:"pending_csr,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type certMeta struct {
	Source string    `json:"source"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

type loadedCert struct {
	cert   tls.Certificate
	info   CertInfo
	stamp  string // identifies the inputs it was loaded from
	loaded time.Time
}

// CertManager serves and manages the GUI certificate.
type CertManager struct {
	dir      string // <state>/webcert
	confPath string
	webCfg   func() WebConfig

	mu       sync.Mutex
	cur      *loadedCert
	checked  time.Time
	lastWarn string
}

func NewCertManager(stateDir, confPath string, webCfg func() WebConfig) *CertManager {
	return &CertManager{dir: filepath.Join(stateDir, "webcert"), confPath: confPath, webCfg: webCfg}
}

func (m *CertManager) managedPaths() (crt, key string) {
	return filepath.Join(m.dir, "cert.pem"), filepath.Join(m.dir, "key.pem")
}
func (m *CertManager) metaPath() string       { return filepath.Join(m.dir, "meta.json") }
func (m *CertManager) pendingKeyPath() string { return filepath.Join(m.dir, "pending.key") }
func (m *CertManager) pendingCSRPath() string { return filepath.Join(m.dir, "pending.csr") }

func fileStamp(paths ...string) string {
	var b strings.Builder
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", p, st.Size(), st.ModTime().UnixNano())
		} else {
			b.WriteString(p + ":-;")
		}
	}
	return b.String()
}

// GetCertificate is the tls.Config callback.
func (m *CertManager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	lc, err := m.current(false)
	if err != nil {
		return nil, err
	}
	c := lc.cert
	return &c, nil
}

// Ensure loads (or generates) the certificate now, reporting any problem.
func (m *CertManager) Ensure() error {
	_, err := m.current(true)
	return err
}

// Refresh drops the cache so the next handshake re-evaluates the sources.
func (m *CertManager) Refresh() {
	m.mu.Lock()
	m.checked = time.Time{}
	m.mu.Unlock()
}

func (m *CertManager) current(force bool) (*loadedCert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !force && m.cur != nil && time.Since(m.checked) < certRecheck {
		return m.cur, nil
	}
	m.checked = time.Now()
	lc, err := m.evaluateLocked()
	if err != nil {
		if m.cur != nil { // keep serving what worked
			if msg := err.Error(); msg != m.lastWarn {
				m.lastWarn = msg
				warnf("web: certificate reload failed (%v) — continuing with the previous certificate", err)
			}
			return m.cur, nil
		}
		return nil, err
	}
	m.lastWarn = ""
	if m.cur == nil || m.cur.stamp != lc.stamp || m.cur.info.Fingerprint != lc.info.Fingerprint {
		infof("web: serving %s certificate %q (sha256 %s, expires %s)",
			lc.info.Source, lc.info.Subject, lc.info.Fingerprint[:16], lc.info.NotAfter.Format("2006-01-02"))
	}
	m.cur = lc
	return lc, nil
}

func (m *CertManager) evaluateLocked() (*loadedCert, error) {
	wc := m.webCfg()
	if wc.CertFile != "" {
		stamp := "files|" + fileStamp(wc.CertFile, wc.KeyFile)
		if m.cur != nil && m.cur.stamp == stamp {
			return m.cur, nil
		}
		c, err := tls.LoadX509KeyPair(wc.CertFile, wc.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("web.cert_file/key_file: %w", err)
		}
		info := describeCert(c, certSourceFiles)
		info.CertFile, info.KeyFile = wc.CertFile, wc.KeyFile
		return &loadedCert{cert: c, info: info, stamp: stamp, loaded: time.Now()}, nil
	}
	crt, key := m.managedPaths()
	if _, err := os.Stat(crt); err == nil {
		stamp := "managed|" + fileStamp(crt, key, m.metaPath())
		if m.cur != nil && m.cur.stamp == stamp {
			return m.cur, nil
		}
		c, err := tls.LoadX509KeyPair(crt, key)
		if err == nil {
			meta := m.readMeta()
			src := certSourceInstalled
			if meta.Source == certSourceCluster {
				src = certSourceCluster
			}
			info := describeCert(c, src)
			info.CertFile, info.KeyFile = crt, key
			info.InstalledBy, info.InstalledAt = meta.By, meta.At
			return &loadedCert{cert: c, info: info, stamp: stamp, loaded: time.Now()}, nil
		}
		warnf("web: installed certificate unusable (%v) — falling back to the self-signed certificate", err)
	}
	scrt, skey := selfSignedPaths(m.confPath)
	stamp := "self|" + fileStamp(scrt, skey)
	if m.cur != nil && m.cur.stamp == stamp && m.cur.info.Source == certSourceSelfSigned &&
		time.Until(m.cur.info.NotAfter) > renewBefore {
		return m.cur, nil
	}
	c, err := loadOrCreateSelfSigned(m.confPath, false)
	if err != nil {
		return nil, fmt.Errorf("self-signed certificate: %w", err)
	}
	info := describeCert(c, certSourceSelfSigned)
	info.CertFile, info.KeyFile = scrt, skey
	return &loadedCert{cert: c, info: info, stamp: "self|" + fileStamp(scrt, skey), loaded: time.Now()}, nil
}

func (m *CertManager) readMeta() certMeta {
	var meta certMeta
	if b, err := os.ReadFile(m.metaPath()); err == nil {
		json.Unmarshal(b, &meta)
	}
	return meta
}

func describeCert(c tls.Certificate, source string) CertInfo {
	info := CertInfo{Source: source, DNSNames: []string{}, IPAddresses: []string{}, ChainLength: len(c.Certificate)}
	if len(c.Certificate) == 0 {
		info.Error = "empty certificate"
		return info
	}
	info.Fingerprint = fingerprint(c)
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		info.Error = err.Error()
		return info
	}
	info.Subject = leaf.Subject.String()
	info.Issuer = leaf.Issuer.String()
	if info.Subject == "" {
		info.Subject = "(none)"
	}
	info.DNSNames = append(info.DNSNames, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		info.IPAddresses = append(info.IPAddresses, ip.String())
	}
	info.NotBefore, info.NotAfter = leaf.NotBefore, leaf.NotAfter
	left := time.Until(leaf.NotAfter)
	info.DaysLeft = int(left.Hours() / 24)
	info.Expired = left <= 0
	info.ExpiresSoon = !info.Expired && left < expiryWarn
	info.SelfSigned = isSelfSigned(leaf)
	return info
}

// Status reports the certificate being served and any pending CSR.
func (m *CertManager) Status() CertInfo {
	lc, err := m.current(true)
	var info CertInfo
	if err != nil {
		info = CertInfo{Error: err.Error(), DNSNames: []string{}, IPAddresses: []string{}}
	} else {
		info = lc.info
	}
	if b, err := os.ReadFile(m.pendingCSRPath()); err == nil {
		info.PendingCSR = string(b)
	}
	return info
}

// ── install / revert / regenerate / CSR ──────────────────────────────────────

// parseChain decodes every CERTIFICATE block in certPEM.
func parseChain(certPEM []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	rest := certPEM
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("certificate: %w", err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("no PEM CERTIFICATE block found")
	}
	return out, nil
}

// Install validates and installs a certificate (leaf first, then any
// intermediates) and its key.  An empty keyPEM uses the key generated with the
// last CSR.  source is certSourceInstalled, or certSourceCluster when the
// primary pushed it.  The returned warnings are advisory.
func (m *CertManager) Install(certPEM, keyPEM []byte, by, source string) (CertInfo, []string, error) {
	if len(certPEM) > maxPEMBytes || len(keyPEM) > maxPEMBytes {
		return CertInfo{}, nil, errors.New("certificate or key too large")
	}
	usedPending := false
	if strings.TrimSpace(string(keyPEM)) == "" {
		pk, err := os.ReadFile(m.pendingKeyPath())
		if err != nil {
			return CertInfo{}, nil, errors.New("no private key supplied and no pending CSR key on this node — generate a CSR first, or provide the key")
		}
		keyPEM, usedPending = pk, true
	}
	_, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		if strings.Contains(err.Error(), "private key does not match") {
			if usedPending {
				return CertInfo{}, nil, errors.New("this certificate does not match the key generated with the pending CSR")
			}
			return CertInfo{}, nil, errors.New("the private key does not match the certificate")
		}
		return CertInfo{}, nil, fmt.Errorf("invalid certificate/key pair (encrypted keys are not supported): %w", err)
	}
	chain, err := parseChain(certPEM)
	if err != nil {
		return CertInfo{}, nil, err
	}
	leaf := chain[0]
	now := time.Now()
	if now.After(leaf.NotAfter) {
		return CertInfo{}, nil, fmt.Errorf("the certificate expired on %s", leaf.NotAfter.Format("2006-01-02"))
	}
	if leaf.NotBefore.After(now.Add(24 * time.Hour)) {
		return CertInfo{}, nil, fmt.Errorf("the certificate is not valid until %s", leaf.NotBefore.Format("2006-01-02"))
	}
	var warns []string
	if leaf.IsCA {
		if len(chain) > 1 {
			return CertInfo{}, nil, errors.New("the first certificate in the file is a CA certificate; put the server (leaf) certificate first")
		}
		warns = append(warns, "the certificate is marked as a CA (typical for a hand-made self-signed certificate); browsers will still warn unless it is trusted")
	}
	if len(leaf.ExtKeyUsage) > 0 {
		ok := false
		for _, u := range leaf.ExtKeyUsage {
			if u == x509.ExtKeyUsageServerAuth || u == x509.ExtKeyUsageAny {
				ok = true
			}
		}
		if !ok {
			warns = append(warns, "the certificate is not valid for TLS server authentication; browsers will reject it")
		}
	}
	if host, _ := os.Hostname(); host != "" && leaf.VerifyHostname(host) != nil {
		covered := false
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && leaf.VerifyHostname(n.IP.String()) == nil {
					covered = true
				}
			}
		}
		if !covered {
			warns = append(warns, fmt.Sprintf("the certificate does not cover this host's name (%s) or any of its addresses; browsers will warn unless you connect by a covered name", host))
		}
	}
	selfSigned := isSelfSigned(leaf)
	if !selfSigned && len(chain) == 1 {
		warns = append(warns, "no intermediate certificates were included; some clients may fail to build the chain")
	}
	if left := time.Until(leaf.NotAfter); left < expiryWarn {
		warns = append(warns, fmt.Sprintf("the certificate expires in %d day(s)", int(left.Hours()/24)))
	}

	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return CertInfo{}, nil, err
	}
	crt, key := m.managedPaths()
	// key first: an interrupted install can then only ever leave a cert/key
	// mismatch, which current() detects and falls back from.
	if err := writeAtomic(key, keyPEM, 0o600); err != nil {
		return CertInfo{}, nil, err
	}
	if err := writeAtomic(crt, certPEM, 0o644); err != nil {
		return CertInfo{}, nil, err
	}
	meta, _ := json.Marshal(certMeta{Source: source, By: by, At: now.UTC()})
	if err := writeAtomic(m.metaPath(), meta, 0o600); err != nil {
		return CertInfo{}, nil, err
	}
	if usedPending {
		os.Remove(m.pendingKeyPath())
		os.Remove(m.pendingCSRPath())
	}
	m.Refresh()
	info := m.Status()
	infof("web: certificate installed by %s (%s, sha256 %s)", by, info.Subject, shortFP(info.Fingerprint))
	return info, warns, nil
}

func shortFP(fp string) string {
	if len(fp) > 16 {
		return fp[:16]
	}
	return fp
}

// Managed returns the installed (GUI/CLI/cluster) certificate pair, if any.
func (m *CertManager) Managed() (certPEM, keyPEM []byte, meta certMeta, ok bool) {
	crt, key := m.managedPaths()
	c, err1 := os.ReadFile(crt)
	k, err2 := os.ReadFile(key)
	if err1 != nil || err2 != nil {
		return nil, nil, certMeta{}, false
	}
	if _, err := tls.X509KeyPair(c, k); err != nil {
		return nil, nil, certMeta{}, false
	}
	return c, k, m.readMeta(), true
}

// Revert removes the installed certificate, falling back to the self-signed one.
func (m *CertManager) Revert(by string) (CertInfo, error) {
	crt, key := m.managedPaths()
	if _, err := os.Stat(crt); errors.Is(err, os.ErrNotExist) {
		return m.Status(), errors.New("no installed certificate to remove")
	}
	for _, p := range []string{crt, key, m.metaPath()} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return CertInfo{}, err
		}
	}
	m.Refresh()
	infof("web: installed certificate removed by %s", by)
	return m.Status(), nil
}

// Regenerate replaces the self-signed certificate with a fresh one.  It is
// served only while no installed certificate or web.cert_file is in effect.
func (m *CertManager) Regenerate(by string) (CertInfo, error) {
	if _, err := loadOrCreateSelfSigned(m.confPath, true); err != nil {
		return CertInfo{}, err
	}
	m.Refresh()
	infof("web: self-signed certificate regenerated by %s", by)
	return m.Status(), nil
}

// ApplyCluster makes this node's installed certificate match the primary's:
// install it (source "cluster") when it differs, or drop a previously
// replicated one when the primary no longer has any.  A certificate installed
// locally by an admin is replaced too: the primary is authoritative.
func (m *CertManager) ApplyCluster(certPEM, keyPEM []byte) error {
	if len(certPEM) == 0 {
		meta := m.readMeta()
		if _, _, _, ok := m.Managed(); ok && meta.Source == certSourceCluster {
			_, err := m.Revert("cluster sync")
			return err
		}
		return nil
	}
	if cur, curKey, _, ok := m.Managed(); ok && string(cur) == string(certPEM) && string(curKey) == string(keyPEM) {
		return nil
	}
	_, _, err := m.Install(certPEM, keyPEM, "cluster sync", certSourceCluster)
	return err
}

// GenerateCSR creates a new key and a certificate signing request.  The key is
// kept (0600) until a matching certificate is installed.
func (m *CertManager) GenerateCSR(commonName string, dnsNames, ips []string) (string, error) {
	commonName = strings.TrimSpace(commonName)
	if len(dnsNames)+len(ips) > maxCSRNames {
		return "", errors.New("too many names")
	}
	tmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: commonName}}
	for _, d := range dnsNames {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if len(d) > maxCSRNameLn || strings.ContainsAny(d, " /\\\x00") {
			return "", fmt.Errorf("invalid DNS name %q", d)
		}
		tmpl.DNSNames = append(tmpl.DNSNames, d)
	}
	for _, s := range ips {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		ip := net.ParseIP(s)
		if ip == nil {
			return "", fmt.Errorf("invalid IP address %q", s)
		}
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	}
	if commonName == "" && len(tmpl.DNSNames) == 0 && len(tmpl.IPAddresses) == 0 {
		return "", errors.New("give a common name and/or at least one DNS name or IP address")
	}
	if commonName == "" && len(tmpl.DNSNames) > 0 {
		tmpl.Subject.CommonName = tmpl.DNSNames[0]
	}
	if len(tmpl.Subject.CommonName) > 64 {
		return "", errors.New("common name longer than 64 characters")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return "", err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return "", err
	}
	if err := writeAtomic(m.pendingKeyPath(), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return "", err
	}
	if err := writeAtomic(m.pendingCSRPath(), csr, 0o644); err != nil {
		return "", err
	}
	return string(csr), nil
}

// CancelCSR forgets a pending CSR and its key.
func (m *CertManager) CancelCSR() {
	os.Remove(m.pendingKeyPath())
	os.Remove(m.pendingCSRPath())
}

// ExpiryWarning returns a human warning when the served certificate has
// expired or expires soon ("" otherwise).
func (m *CertManager) ExpiryWarning() string {
	info := m.Status()
	switch {
	case info.Error != "":
		return ""
	case info.Expired:
		return fmt.Sprintf("the GUI certificate expired on %s", info.NotAfter.Format("2006-01-02"))
	case info.ExpiresSoon && info.Source != certSourceSelfSigned:
		return fmt.Sprintf("the GUI certificate expires in %d day(s) (%s)", info.DaysLeft, info.NotAfter.Format("2006-01-02"))
	}
	return ""
}

// isSelfSigned reports whether c is signed by its own key (CA flag or not).
func isSelfSigned(c *x509.Certificate) bool {
	return c.Subject.String() == c.Issuer.String() &&
		c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) == nil
}
