package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Management-cluster membership and role state, after umiss's
// internal/cluster: exactly one node is primary at any time, every promotion
// increments a cluster-wide epoch, and any node that learns of a higher epoch
// defers to it unconditionally.  Promotion is admin-confirmed (no automatic
// election), so this only has to make promotions durable and monotonic.
//
// This package-level state is network-free; clusternet.go drives it.

type Role string

const (
	RolePrimary Role = "primary"
	RoleReplica Role = "replica"
)

var (
	// ErrStaleEpoch: an incoming claim is behind this node's epoch.
	ErrStaleEpoch = errors.New("cluster: epoch is stale")
	// ErrEpochConflict: a different primary claimed the epoch this node already has.
	ErrEpochConflict = errors.New("cluster: two different primaries claimed the same epoch")
	// ErrNotJoinable: the node is already part of a cluster.
	ErrNotJoinable = errors.New("cluster: this node already belongs to a cluster (leave it first)")
)

const (
	joinTokenTTL   = time.Hour
	maxPendingToks = 20
)

// ClusterPeer is another member.  Fp is the SHA-256 fingerprint of its
// cluster identity certificate, pinned on every connection.
type ClusterPeer struct {
	Addr   string `json:"addr"`
	Fp     string `json:"fp"`
	NodeID string `json:"node_id"`
	// Alts are other ways to reach the same node (its host name, IPv4 and IPv6
	// addresses); a call tries Addr first, then these.
	Alts []string `json:"alts,omitempty"`
}

func peerEqual(a, b ClusterPeer) bool {
	if a.Addr != b.Addr || a.Fp != b.Fp || a.NodeID != b.NodeID || len(a.Alts) != len(b.Alts) {
		return false
	}
	for i := range a.Alts {
		if a.Alts[i] != b.Alts[i] {
			return false
		}
	}
	return true
}

type tokenRec struct {
	Hash    string    `json:"hash"`
	Expires time.Time `json:"expires"`
	By      string    `json:"by"`
}

type clusterState struct {
	NodeID      string        `json:"node_id"`
	Epoch       uint64        `json:"epoch"`
	Role        Role          `json:"role"`
	PrimaryAddr string        `json:"primary_addr"`
	SelfAddr    string        `json:"self_addr"`
	Secret      string        `json:"secret"`
	Peers       []ClusterPeer `json:"peers"`
	Removed     []string      `json:"removed,omitempty"`
	// RemovedFp is the identity fingerprint each removed address had, so a removed node stays refused under
	// whatever address it claims next.  MTLSFps are the members seen to present their identity certificate
	// on calls (see Cluster.peerAuth).
	RemovedFp  map[string]string `json:"removed_fp,omitempty"`
	MTLSFps    []string          `json:"mtls_fps,omitempty"`
	StrictOn   bool              `json:"strict,omitempty"` // every member presented its certificate once: never go back
	SharedRev  uint64            `json:"shared_rev"`
	SharedHash string            `json:"shared_hash"`
	Tokens     []tokenRec        `json:"tokens,omitempty"`
}

// ClusterSnapshot is a copy of the state, safe to hold.
type ClusterSnapshot struct {
	NodeID      string
	Epoch       uint64
	Role        Role
	PrimaryAddr string
	SelfAddr    string
	Peers       []ClusterPeer
	Removed     []string
	SharedRev   uint64
	SharedHash  string
}

func (s ClusterSnapshot) IsPrimary() bool { return s.Role == RolePrimary }

// ClusterNode is the durable membership state of this node.
type ClusterNode struct {
	mu   sync.Mutex
	dir  string
	st   clusterState
	cert tls.Certificate
	fp   string
	alts []string
}

func (n *ClusterNode) statePath() string { return filepath.Join(n.dir, "cluster.json") }

func randHex(nbytes int) string {
	b := make([]byte, nbytes)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newClusterSecret() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawStdEncoding.EncodeToString(b)
}

// LoadOrCreateClusterNode loads the state under dir, or founds a new
// single-node cluster (epoch 1, this node primary) with a fresh identity.
func LoadOrCreateClusterNode(dir, selfAddr string) (*ClusterNode, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	n := &ClusterNode{dir: dir}
	b, err := os.ReadFile(n.statePath())
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &n.st); err != nil {
			return nil, fmt.Errorf("%s: %w", n.statePath(), err)
		}
	case errors.Is(err, os.ErrNotExist):
		n.st = clusterState{NodeID: randHex(8), Epoch: 1, Role: RolePrimary, Secret: newClusterSecret(), Peers: []ClusterPeer{}}
	default:
		return nil, err
	}
	if n.st.NodeID == "" {
		n.st.NodeID = randHex(8)
	}
	if n.st.Secret == "" {
		n.st.Secret = newClusterSecret()
	}
	if n.st.Peers == nil {
		n.st.Peers = []ClusterPeer{}
	}
	// the self address is operational config, re-supplied on every start
	if n.st.Role == RolePrimary && (n.st.PrimaryAddr == "" || n.st.PrimaryAddr == n.st.SelfAddr) {
		n.st.PrimaryAddr = selfAddr
	}
	n.st.SelfAddr = selfAddr
	if err := n.loadIdentity(); err != nil {
		return nil, err
	}
	if err := n.saveLocked(); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *ClusterNode) loadIdentity() error {
	crt, key := filepath.Join(n.dir, "node.crt"), filepath.Join(n.dir, "node.key")
	if c, err := tls.LoadX509KeyPair(crt, key); err == nil {
		if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil && time.Until(leaf.NotAfter) > 365*24*time.Hour {
			n.cert, n.fp = c, fingerprint(c)
			return nil
		}
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "ddgw-node-" + n.st.NodeID},
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"ddgw-node"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		return err
	}
	kb, _ := x509.MarshalECPrivateKey(k)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	if err := writeAtomic(key, keyPEM, 0o600); err != nil {
		return err
	}
	if err := writeAtomic(crt, certPEM, 0o644); err != nil {
		return err
	}
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	n.cert, n.fp = c, fingerprint(c)
	return nil
}

func (n *ClusterNode) saveLocked() error {
	b, err := json.MarshalIndent(n.st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(n.statePath(), b, 0o600)
}

func (n *ClusterNode) snapshotLocked() ClusterSnapshot {
	return ClusterSnapshot{
		NodeID: n.st.NodeID, Epoch: n.st.Epoch, Role: n.st.Role, PrimaryAddr: n.st.PrimaryAddr,
		SelfAddr: n.st.SelfAddr, Peers: append([]ClusterPeer{}, n.st.Peers...),
		Removed: append([]string{}, n.st.Removed...), SharedRev: n.st.SharedRev, SharedHash: n.st.SharedHash,
	}
}

func (n *ClusterNode) Snapshot() ClusterSnapshot {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.snapshotLocked()
}

func (n *ClusterNode) Identity() (tls.Certificate, string) { return n.cert, n.fp }
func (n *ClusterNode) Fingerprint() string                 { return n.fp }

func (n *ClusterNode) Secret() []byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	b, _ := base64.RawStdEncoding.DecodeString(n.st.Secret)
	return b
}

// Self returns this node's own peer record.
func (n *ClusterNode) Self() ClusterPeer {
	n.mu.Lock()
	defer n.mu.Unlock()
	return ClusterPeer{Addr: n.st.SelfAddr, Fp: n.fp, NodeID: n.st.NodeID, Alts: append([]string(nil), n.alts...)}
}

// SetAlts records the other addresses this node can be reached on (not saved:
// interface addresses are re-read at run time).
func (n *ClusterNode) SetAlts(alts []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.alts = append([]string(nil), alts...)
}

// SetSelf updates the advertised address (config change).
func (n *ClusterNode) SetSelf(addr string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.st.SelfAddr == addr {
		return
	}
	if n.st.Role == RolePrimary && n.st.PrimaryAddr == n.st.SelfAddr {
		n.st.PrimaryAddr = addr
	}
	n.st.SelfAddr = addr
	n.saveLocked()
}

// Joinable is true only for an untouched, self-founded single-node cluster.
func (n *ClusterNode) Joinable() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.st.Role == RolePrimary && len(n.st.Peers) == 0 && n.st.Epoch == 1
}

// Promote makes this node primary at epoch+1.
func (n *ClusterNode) Promote() ClusterSnapshot {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.st.Epoch++
	n.st.Role = RolePrimary
	n.st.PrimaryAddr = n.st.SelfAddr
	n.saveLocked()
	return n.snapshotLocked()
}

// AdoptAnnounce applies a claim "primaryAddr is primary at epoch".
func (n *ClusterNode) AdoptAnnounce(epoch uint64, primaryAddr string) (ClusterSnapshot, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case epoch < n.st.Epoch:
		return n.snapshotLocked(), ErrStaleEpoch
	case epoch == n.st.Epoch:
		if primaryAddr != n.st.PrimaryAddr {
			return n.snapshotLocked(), ErrEpochConflict
		}
		return n.snapshotLocked(), nil
	}
	n.st.Epoch = epoch
	n.st.PrimaryAddr = primaryAddr
	if primaryAddr == n.st.SelfAddr {
		n.st.Role = RolePrimary
	} else {
		n.st.Role = RoleReplica
	}
	n.saveLocked()
	return n.snapshotLocked(), nil
}

// CheckEpoch rejects an epoch behind this node's own.
func (n *ClusterNode) CheckEpoch(epoch uint64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if epoch < n.st.Epoch {
		return ErrStaleEpoch
	}
	return nil
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// AddPeer records a member (never one on the removed list, nor ourselves).
// It reports whether anything changed.
func (n *ClusterNode) AddPeer(p ClusterPeer) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if p.Addr == "" || p.Fp == "" || p.Addr == n.st.SelfAddr || p.NodeID == n.st.NodeID || containsStr(n.st.Removed, p.Addr) || n.removedFpLocked(p.Fp) {
		return false
	}
	for i, q := range n.st.Peers {
		if q.NodeID == p.NodeID && p.NodeID != "" || q.Addr == p.Addr {
			if peerEqual(q, p) {
				return false
			}
			n.st.Peers[i] = p
			n.saveLocked()
			return true
		}
	}
	n.st.Peers = append(n.st.Peers, p)
	sort.Slice(n.st.Peers, func(i, j int) bool { return n.st.Peers[i].Addr < n.st.Peers[j].Addr })
	n.saveLocked()
	return true
}

// RemovePeer drops a member and blocks it from being re-added by gossip.
func (n *ClusterNode) RemovePeer(addr string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	changed := false
	kept := n.st.Peers[:0]
	fp := n.st.RemovedFp[addr]
	for _, p := range n.st.Peers {
		if p.Addr == addr {
			changed = true
			fp = p.Fp
			continue
		}
		kept = append(kept, p)
	}
	n.st.Peers = kept
	if !containsStr(n.st.Removed, addr) {
		n.st.Removed = append(n.st.Removed, addr)
		changed = true
	}
	if fp != "" && n.st.RemovedFp[addr] != fp {
		if n.st.RemovedFp == nil {
			n.st.RemovedFp = map[string]string{}
		}
		n.st.RemovedFp[addr] = fp
		changed = true
	}
	if changed {
		n.saveLocked()
	}
	return changed
}

// removedFpLocked: fp is the identity of a removed member (n.mu held).
func (n *ClusterNode) removedFpLocked(fp string) bool {
	if fp == "" {
		return false
	}
	for _, f := range n.st.RemovedFp {
		if f == fp {
			return true
		}
	}
	return false
}

// IsRemovedFp reports whether fp is the identity of a removed member.
func (n *ClusterNode) IsRemovedFp(fp string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.removedFpLocked(fp)
}

// UnremoveFp lifts the block on an identity (a node that is let back in, possibly under a new address).
func (n *ClusterNode) UnremoveFp(fp string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for a, f := range n.st.RemovedFp {
		if f == fp {
			delete(n.st.RemovedFp, a)
			n.saveLocked()
		}
	}
}

// PeerByFp returns the member whose identity certificate has fingerprint fp.
func (n *ClusterNode) PeerByFp(fp string) (ClusterPeer, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, p := range n.st.Peers {
		if fp != "" && p.Fp == fp {
			return p, true
		}
	}
	return ClusterPeer{}, false
}

// NoteMTLS records that the member with identity fp presented its certificate on a call.  From then on a call
// claiming that identity without one is refused (no downgrade).
func (n *ClusterNode) NoteMTLS(fp string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if fp == "" || containsStr(n.st.MTLSFps, fp) {
		return
	}
	n.st.MTLSFps = append(n.st.MTLSFps, fp)
	n.saveLocked()
}

// IsMTLS: the member with identity fp has been seen presenting its certificate.
func (n *ClusterNode) IsMTLS(fp string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return containsStr(n.st.MTLSFps, fp)
}

// Strict is true once every member has been seen presenting its certificate, i.e. when the whole cluster runs
// a version that does.  From then on only members already on the list are served: a caller that merely holds
// the cluster secret (a node that was removed, say) cannot make itself a member by calling.
func (n *ClusterNode) Strict() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.st.StrictOn {
		return true // sticky: when members leave or are removed the cluster does not fall back to trusting the secret alone
	}
	if len(n.st.Peers) == 0 {
		return false
	}
	for _, p := range n.st.Peers {
		if !containsStr(n.st.MTLSFps, p.Fp) {
			return false
		}
	}
	n.st.StrictOn = true
	n.saveLocked()
	return true
}

// UnremovePeer lifts the block so the member can be added again.
func (n *ClusterNode) UnremovePeer(addr string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	changed := false
	for _, a := range n.st.Removed {
		if a == addr {
			changed = true
			continue
		}
		out = append(out, a)
	}
	n.st.Removed = out
	if _, ok := n.st.RemovedFp[addr]; ok {
		delete(n.st.RemovedFp, addr)
		changed = true
	}
	if changed {
		n.saveLocked()
	}
	return changed
}

// JoinAs makes this (fresh) node a replica of an existing cluster.
func (n *ClusterNode) JoinAs(secret string, epoch uint64, primaryAddr string, peers []ClusterPeer) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.st.Role != RolePrimary || len(n.st.Peers) != 0 || n.st.Epoch != 1 {
		return ErrNotJoinable
	}
	if raw, err := base64.RawStdEncoding.DecodeString(secret); err != nil || len(raw) < 16 {
		return errors.New("cluster: invalid secret from the cluster")
	}
	n.st.Secret = secret
	n.st.Epoch = epoch
	n.st.PrimaryAddr = primaryAddr
	n.st.Role = RoleReplica
	n.st.Peers = []ClusterPeer{}
	n.st.Removed, n.st.RemovedFp, n.st.MTLSFps, n.st.StrictOn = nil, nil, nil, false
	n.st.Tokens = nil
	for _, p := range peers {
		if p.Addr == n.st.SelfAddr || p.NodeID == n.st.NodeID || p.Addr == "" || p.Fp == "" {
			continue
		}
		dup := false
		for _, q := range n.st.Peers {
			if q.Addr == p.Addr || (p.NodeID != "" && q.NodeID == p.NodeID) {
				dup = true
			}
		}
		if !dup {
			n.st.Peers = append(n.st.Peers, p)
		}
	}
	sort.Slice(n.st.Peers, func(i, j int) bool { return n.st.Peers[i].Addr < n.st.Peers[j].Addr })
	return n.saveLocked()
}

// Reset leaves the cluster: a new single-node cluster with a new secret.
func (n *ClusterNode) Reset() ClusterSnapshot {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.st.Epoch, n.st.Role, n.st.PrimaryAddr = 1, RolePrimary, n.st.SelfAddr
	n.st.Secret = newClusterSecret()
	n.st.Peers, n.st.Removed, n.st.Tokens = []ClusterPeer{}, nil, nil
	n.st.RemovedFp, n.st.MTLSFps, n.st.StrictOn = nil, nil, false
	n.saveLocked()
	return n.snapshotLocked()
}

// SetShared records the primary's shared-config revision.
func (n *ClusterNode) SetShared(rev uint64, hash string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.st.SharedRev == rev && n.st.SharedHash == hash {
		return
	}
	n.st.SharedRev, n.st.SharedHash = rev, hash
	n.saveLocked()
}

// BumpShared increments the revision when the shared config hash changed.
// It reports whether it did.
func (n *ClusterNode) BumpShared(hash string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.st.SharedHash == hash {
		return false
	}
	n.st.SharedRev++
	n.st.SharedHash = hash
	n.saveLocked()
	return true
}

// ── join tokens ──────────────────────────────────────────────────────────────

func hashToken(tok string) string {
	h := sha256.Sum256([]byte("ddgw-join-v1\x00" + tok))
	return hex.EncodeToString(h[:])
}

// MintToken creates a single-use join token valid for joinTokenTTL.
func (n *ClusterNode) MintToken(by string) (string, time.Time, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	live := n.st.Tokens[:0]
	for _, t := range n.st.Tokens {
		if now.Before(t.Expires) {
			live = append(live, t)
		}
	}
	n.st.Tokens = live
	if len(n.st.Tokens) >= maxPendingToks {
		return "", time.Time{}, errors.New("too many unused join tokens; wait for them to expire")
	}
	b := make([]byte, 24)
	rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	exp := now.Add(joinTokenTTL)
	n.st.Tokens = append(n.st.Tokens, tokenRec{Hash: hashToken(tok), Expires: exp, By: by})
	return tok, exp, n.saveLocked()
}

// RedeemToken consumes a token; it reports whether it was valid.
func (n *ClusterNode) RedeemToken(tok string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	h := hashToken(tok)
	now := time.Now()
	ok := false
	kept := n.st.Tokens[:0]
	for _, t := range n.st.Tokens {
		if now.After(t.Expires) {
			continue
		}
		if !ok && subtle.ConstantTimeCompare([]byte(t.Hash), []byte(h)) == 1 {
			ok = true
			continue // single use: drop it
		}
		kept = append(kept, t)
	}
	n.st.Tokens = kept
	n.saveLocked()
	return ok
}
