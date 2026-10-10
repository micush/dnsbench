package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The cluster network layer.  Peers talk to each other on a dedicated TLS
// listener (default :53854).  Each node has its own long-lived identity
// certificate whose SHA-256 fingerprint the other members pin (learned from
// the join code and then gossiped), so the channel does not depend on the GUI
// certificate, which admins may replace at will.  On top of that every request
// carries an HMAC over the cluster secret (handed out at join), a timestamp
// and a nonce.  Nothing here uses the PAM/GUI login.

const (
	joinCodePrefix  = "ddgw-join-v1:"
	clockSkew       = 90 * time.Second
	nonceTTL        = 5 * time.Minute
	nonceSweepEvery = time.Second
	maxNonces       = 50000 // sweep at once above this many remembered nonces
	maxPeerBody     = 8 << 20
	peerTimeout     = 6 * time.Second
	sourceTimeout   = 60 * time.Second
)

// PeerStatusMsg is what a node tells others about itself.
type PeerStatusMsg struct {
	Peer          ClusterPeer   `json:"peer"`
	Epoch         uint64        `json:"epoch"`
	Role          Role          `json:"role"`
	PrimaryAddr   string        `json:"primary_addr"`
	Peers         []ClusterPeer `json:"peers"`
	Removed       []string      `json:"removed"`
	Version       string        `json:"version"`
	Hostname      string        `json:"hostname,omitempty"`
	SourceVersion string        `json:"source_version"`
	Updating      bool          `json:"updating"`
	UpdateFailed  string        `json:"update_failed,omitempty"`
	SharedRev     uint64        `json:"shared_rev"`
	Intent        UpdateIntent  `json:"intent"`
	// Which gateways this node is serving; GwKnown is false for nodes that
	// predate the field (their health is then assumed).
	GwKnown  bool      `json:"gw_known,omitempty"`
	Gateways []GwState `json:"gateways,omitempty"`
	// NodePaused: the whole node is paused (Operate ▸ Node); shown on the Topology drawing.
	NodePaused bool `json:"node_paused,omitempty"`
	// Host is this node's CPU, memory and disk use, for the Topology drawing (absent on older nodes).
	Host *HostLoad `json:"host,omitempty"`
	// Addrs are this node's Ethernet interfaces with their IPv4 and IPv6 GUA addresses, for the node's tooltip on the
	// Topology drawing (absent on older nodes).
	Addrs []NodeIface `json:"addrs,omitempty"`
	// GwIPs are the addresses this node uses in the gateway protocol (one per address family of each group): what the
	// Gateways page shows as a member's IP, so another node can say which node that is.
	GwIPs []string `json:"gw_ips,omitempty"`
}

type clusterStateMsg struct {
	Epoch       uint64        `json:"epoch"`
	PrimaryAddr string        `json:"primary_addr"`
	Peers       []ClusterPeer `json:"peers"`
	SharedRev   uint64        `json:"shared_rev"`
	Shared      SharedConfig  `json:"shared"`
	Seeds       []GroupSeed   `json:"seeds"`
	Cert        *adminCert    `json:"cert,omitempty"`
	Intent      UpdateIntent  `json:"intent"`
}

type joinRequest struct {
	Token        string      `json:"token"`
	Peer         ClusterPeer `json:"peer"`
	ExplicitSelf bool        `json:"explicit_self"`
	MTLS         bool        `json:"mtls,omitempty"` // the joiner presents its identity certificate on calls
}

type joinResponse struct {
	Secret      string        `json:"secret"`
	Epoch       uint64        `json:"epoch"`
	PrimaryAddr string        `json:"primary_addr"`
	Server      ClusterPeer   `json:"server"`
	Peers       []ClusterPeer `json:"peers"`
}

type joinCode struct {
	Token string   `json:"t"`
	Fp    string   `json:"f"`
	Addrs []string `json:"a"`
}

type announceMsg struct {
	Epoch       uint64 `json:"epoch"`
	PrimaryAddr string `json:"primary_addr"`
}

type adminMsg struct {
	Op      string          `json:"op"`
	By      string          `json:"by"`
	Payload json.RawMessage `json:"payload"`
}

// peerInfo is what we last learned about a peer.
type peerInfo struct {
	Reachable bool
	Msg       PeerStatusMsg
	LastSeen  time.Time
	Err       string
}

// Cluster runs the listener, the sync loop and the client side.
type Cluster struct {
	mg   *Mgmt
	node *ClusterNode

	strain strainLog // host-load over/under log lines (strainlog.go)

	mu         sync.Mutex
	cfg        ClusterConfig
	listen     string
	srv        *http.Server
	cancelSrv  context.CancelFunc
	info       map[string]*peerInfo
	nonces     map[string]time.Time
	nonceSweep time.Time // when the nonces were last swept for expired ones
	joinFails  map[string]*failRec
	goodAddr   map[string]string // peer's main address -> the address that last worked
	lastSync   time.Time
	lastErr    string
	conflict   string
	kick       chan struct{}
	syncMu     sync.Mutex
}

func clusterSelfAddr(cfg ClusterConfig) string {
	if cfg.Self != "" {
		return cfg.Self
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "localhost"
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		port = strconv.Itoa(defaultClusterPort)
	}
	return net.JoinHostPort(host, port)
}

func NewCluster(mg *Mgmt, cfg ClusterConfig) (*Cluster, error) {
	node, err := LoadOrCreateClusterNode(mg.stateDir+"/cluster", clusterSelfAddr(cfg))
	if err != nil {
		return nil, err
	}
	c := &Cluster{
		mg: mg, node: node, cfg: cfg, info: map[string]*peerInfo{}, nonces: map[string]time.Time{},
		joinFails: map[string]*failRec{}, goodAddr: map[string]string{}, kick: make(chan struct{}, 1),
	}
	c.refreshAlts()
	setLocalNodeID(node.Self().NodeID)
	return c, nil
}

func (c *Cluster) Enabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.Enabled
}

func (c *Cluster) Snapshot() ClusterSnapshot { return c.node.Snapshot() }

func (c *Cluster) selfAddrForEvents() string { return c.node.Self().Addr }

// OnConfig applies a (re)loaded config: self address, listener, shared revision.
func (c *Cluster) OnConfig(dc *DaemonConfig) {
	c.mu.Lock()
	c.cfg = dc.Cluster
	c.mu.Unlock()
	c.node.SetSelf(clusterSelfAddr(dc.Cluster))
	c.refreshAlts()
	if snap := c.node.Snapshot(); snap.Role == RolePrimary || !dc.Cluster.Enabled {
		if c.node.BumpShared(sharedOf(dc).hash()) {
			infof("cluster: shared settings changed (revision %d)", c.node.Snapshot().SharedRev)
		}
	}
	go c.applyListener()
}

// ── listener ─────────────────────────────────────────────────────────────────

func (c *Cluster) applyListener() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.cfg.Enabled || c.cfg.Listen != c.listen {
		if c.srv != nil {
			c.cancelSrv()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			c.srv.Shutdown(ctx)
			cancel()
			c.srv = nil
			c.listen = ""
			infof("cluster: listener stopped")
		}
	}
	if !c.cfg.Enabled || c.srv != nil {
		return
	}
	ln, err := net.Listen("tcp", c.cfg.Listen)
	if err != nil {
		errorf("cluster: cannot listen on %s: %v", c.cfg.Listen, err)
		return
	}
	cert, _ := c.node.Identity()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cluster/join", c.handleJoin)
	mux.HandleFunc("GET /cluster/status", c.peerAuth(c.handleStatus))
	mux.HandleFunc("GET /cluster/state", c.peerAuth(c.handleState))
	mux.HandleFunc("POST /cluster/announce", c.peerAuth(c.handleAnnounce))
	mux.HandleFunc("POST /cluster/peers/add", c.peerAuth(c.handlePeerAdd))
	mux.HandleFunc("POST /cluster/peers/remove", c.peerAuth(c.handlePeerRemove))
	mux.HandleFunc("POST /cluster/peers/unremove", c.peerAuth(c.handlePeerUnremove))
	mux.HandleFunc("POST /cluster/admin", c.peerAuth(c.handleAdmin))
	mux.HandleFunc("GET /cluster/source", c.peerAuth(c.handleSource))
	mux.HandleFunc("POST /cluster/proxy", c.peerAuth(c.handleProxy))
	mux.HandleFunc("POST /cluster/hist", c.peerAuth(c.handleHist))
	mux.HandleFunc("POST /cluster/cache", c.peerAuth(c.handleCache))
	mux.HandleFunc("POST /cluster/users", c.peerAuth(c.handleUsers))
	srv := &http.Server{
		ErrorLog: log.New(io.Discard, "", 0), Handler: mux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 2 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
		// the client certificate is asked for but not chain-checked (there is no CA): peerAuth compares its
		// fingerprint with the identity the signed request claims
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, ClientAuth: tls.RequestClientCert},
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.srv, c.cancelSrv, c.listen = srv, cancel, c.cfg.Listen
	go func() {
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorf("cluster: listener stopped: %v", err)
		}
	}()
	_ = ctx
	infof("cluster: listening on %s as %s (node %s, %s, epoch %d, identity sha256 %s)",
		c.cfg.Listen, c.node.Self().Addr, c.node.Snapshot().NodeID, c.node.Snapshot().Role,
		c.node.Snapshot().Epoch, shortFP(c.node.Fingerprint()))
}

func (c *Cluster) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.srv != nil {
		c.cancelSrv()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		c.srv.Shutdown(ctx)
		cancel()
		c.srv = nil
		c.listen = ""
	}
}

// ── request authentication ───────────────────────────────────────────────────

func signMessage(secret []byte, peerHdr, ts, nonce, method, path string, body []byte) string {
	bh := sha256.Sum256(body)
	return signMessageHash(secret, peerHdr, ts, nonce, method, path, hex.EncodeToString(bh[:]))
}

// signMessageHash is signMessage with the hex SHA-256 of the body already known.  A request carries that hash in
// X-Ddgw-Body, so the signature can be checked before the body is read; the body is then held to the hash.
func signMessageHash(secret []byte, peerHdr, ts, nonce, method, path, bodyHash string) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "ddgw-cluster-v1\n%s\n%s\n%s\n%s\n%s\n%s", ts, nonce, method, path, peerHdr, bodyHash)
	return hex.EncodeToString(mac.Sum(nil))
}

// legacyPreAuthBody is how much of a request body is read from a caller that cannot be told apart from a stranger
// before its signature is checked: a peer older than v194 does not send X-Ddgw-Body, so its signature covers a
// body that has to be read first.  A member known by its certificate may send up to maxPeerBody.
const legacyPreAuthBody = 64 << 10

func isHexHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func (c *Cluster) peerAuth(h func(http.ResponseWriter, *http.Request, ClusterPeer, []byte)) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		ts, nonce, sig, peerHdr := r.Header.Get("X-Ddgw-Ts"), r.Header.Get("X-Ddgw-Nonce"), r.Header.Get("X-Ddgw-Sig"), r.Header.Get("X-Ddgw-Peer")
		t, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || nonce == "" || len(nonce) > 64 || sig == "" {
			jsonError(rw, http.StatusUnauthorized, "unauthenticated")
			return
		}
		if d := time.Since(time.Unix(t, 0)); d > clockSkew || d < -clockSkew {
			jsonError(rw, http.StatusUnauthorized, "clock skew too large (check NTP on both nodes)")
			return
		}
		// Nothing is read from a stranger before the signature is checked.  A request carries the hash of its
		// body, which the signature covers: the signature is checked first, then the body is read and held to
		// the hash.  A request from a peer that predates that header can only be checked after its body is
		// read, so that is limited to a small size unless the TLS client certificate is a known member's.
		var body []byte
		if bodyHash := r.Header.Get("X-Ddgw-Body"); isHexHash(bodyHash) {
			want := signMessageHash(c.node.Secret(), peerHdr, ts, nonce, r.Method, r.URL.Path, bodyHash)
			if subtle.ConstantTimeCompare([]byte(want), []byte(sig)) != 1 {
				jsonError(rw, http.StatusUnauthorized, "unauthenticated")
				return
			}
			if r.ContentLength > maxPeerBody {
				jsonError(rw, http.StatusRequestEntityTooLarge, "request too large")
				return
			}
			body, err = io.ReadAll(http.MaxBytesReader(rw, r.Body, maxPeerBody))
			if err != nil {
				jsonError(rw, http.StatusRequestEntityTooLarge, "request too large")
				return
			}
			if sum := sha256.Sum256(body); subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(bodyHash)) != 1 {
				jsonError(rw, http.StatusUnauthorized, "the body is not the one that was signed")
				return
			}
		} else {
			limit := int64(legacyPreAuthBody)
			if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
				if _, known := c.node.PeerByFp(certFingerprint(r.TLS.PeerCertificates[0])); known {
					limit = maxPeerBody
				}
			}
			if r.ContentLength > limit {
				jsonError(rw, http.StatusRequestEntityTooLarge, "request too large")
				return
			}
			body, err = io.ReadAll(http.MaxBytesReader(rw, r.Body, limit))
			if err != nil {
				jsonError(rw, http.StatusRequestEntityTooLarge, "request too large")
				return
			}
			want := signMessage(c.node.Secret(), peerHdr, ts, nonce, r.Method, r.URL.Path, body)
			if subtle.ConstantTimeCompare([]byte(want), []byte(sig)) != 1 {
				jsonError(rw, http.StatusUnauthorized, "unauthenticated")
				return
			}
		}
		c.mu.Lock()
		now := time.Now()
		// Expired nonces are swept at most once a second (or at once when the table is large), not on every
		// request: the sweep visits every entry, and every peer call used to pay for it.  An entry that has
		// expired but not yet been swept is no replay: the timestamp check already refuses anything that old.
		if now.After(c.nonceSweep) || len(c.nonces) > maxNonces {
			for k, exp := range c.nonces {
				if now.After(exp) {
					delete(c.nonces, k)
				}
			}
			c.nonceSweep = now.Add(nonceSweepEvery)
		}
		if exp, dup := c.nonces[sig]; dup && now.Before(exp) {
			c.mu.Unlock()
			jsonError(rw, http.StatusUnauthorized, "replayed request")
			return
		}
		c.nonces[sig] = now.Add(nonceTTL)
		c.mu.Unlock()

		var caller ClusterPeer
		if raw, err := base64.StdEncoding.DecodeString(peerHdr); err == nil {
			json.Unmarshal(raw, &caller)
		}
		// The signature proves the caller holds the cluster secret, which every member (and every member that was
		// ever removed) has.  The identity certificate it presented on the TLS connection proves which member it
		// is: it must be the one the request claims, and that member must be on the list and not removed.
		certFp := ""
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			certFp = certFingerprint(r.TLS.PeerCertificates[0])
			if caller.Fp != certFp {
				jsonError(rw, http.StatusUnauthorized, "the client certificate is not the identity this request claims")
				return
			}
		}
		if c.node.IsRemovedFp(caller.Fp) || certFp != "" && c.node.IsRemovedFp(certFp) {
			writeJSON(rw, http.StatusForbidden, map[string]any{"ok": false, "error": "removed from the cluster", "removed": true})
			return
		}
		strict := c.node.Strict()
		switch {
		case certFp == "" && (strict || c.node.IsMTLS(caller.Fp)):
			jsonError(rw, http.StatusUnauthorized, "a client certificate is required")
			return
		case certFp != "":
			if known, ok := c.node.PeerByFp(certFp); ok && known.NodeID == caller.NodeID {
				c.node.NoteMTLS(certFp)
			} else if strict {
				jsonError(rw, http.StatusForbidden, "not a member of this cluster")
				return
			}
		}
		if caller.Addr != "" {
			if containsStr(c.node.Snapshot().Removed, caller.Addr) {
				writeJSON(rw, http.StatusForbidden, map[string]any{"ok": false, "error": "removed from the cluster", "removed": true})
				return
			}
			if validPeer(caller) && c.node.AddPeer(caller) {
				infof("cluster: learned peer %s (node %s)", caller.Addr, caller.NodeID)
				if certFp != "" {
					c.node.NoteMTLS(certFp)
				}
			}
		}
		h(rw, r, caller, body)
	}
}

func validPeer(p ClusterPeer) bool {
	if p.Addr == "" || len(p.Fp) != 64 || p.NodeID == "" || len(p.NodeID) > 64 {
		return false
	}
	if _, err := hex.DecodeString(p.Fp); err != nil {
		return false
	}
	if len(p.Alts) > 16 {
		return false
	}
	for _, a := range p.Alts {
		if !validHostPort(a) {
			return false
		}
	}
	return validHostPort(p.Addr)
}

// validHostPort accepts only "host:port" where the host is an IP address or a
// plain host name and the port is 1-65535.  Peer addresses end up in a URL, so
// anything that could change where the request goes ("@", "/", "?", "#",
// spaces, a zone) is refused.
func validHostPort(a string) bool {
	if a == "" || len(a) > 255 {
		return false
	}
	host, port, err := net.SplitHostPort(a)
	if err != nil || host == "" {
		return false
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return !strings.Contains(host, "%")
	}
	if len(host) > 253 {
		return false
	}
	for _, c := range host {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

// ── client ───────────────────────────────────────────────────────────────────

type peerError struct {
	Status  int
	Msg     string
	Removed bool
}

func (e *peerError) Error() string { return e.Msg }

// call performs an authenticated request to peer.
func (c *Cluster) call(ctx context.Context, peer ClusterPeer, method, path string, in, out any, timeout time.Duration) error {
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	return c.callRaw(ctx, peer, method, path, body, func(rd io.Reader) error {
		if out == nil {
			return nil
		}
		return json.NewDecoder(rd).Decode(out)
	}, timeout, nil)
}

// callRaw talks to a peer, trying every address it is known by (the preferred
// one first: the last that worked, then its main address, then its alternatives
// — host name, IPv4, IPv6). Only connection-level failures move on to the next
// address; an answer from the peer, even a refusal, is final.
func (c *Cluster) callRaw(ctx context.Context, peer ClusterPeer, method, path string, body []byte, onBody func(io.Reader) error, timeout time.Duration, hdr http.Header) error {
	addrs := c.addrsFor(peer)
	var errs []string
	for _, a := range addrs {
		// A dead address is given up on when the connection cannot be made (peerConnectTimeout, in the dial), not by cutting
		// the whole call short: a call that takes a while (a troubleshooting bundle, an upload) must not be abandoned at the
		// first address and started again on the next.
		err := c.callAddr(ctx, a, peer, method, path, body, onBody, timeout, hdr)
		if err == nil {
			c.noteGoodAddr(peer, a)
			return nil
		}
		var pe *peerError
		if errors.As(err, &pe) || ctx.Err() != nil || len(addrs) == 1 {
			return err
		}
		errs = append(errs, a+": "+err.Error())
	}
	return errors.New(strings.Join(errs, "; "))
}

func (c *Cluster) addrsFor(peer ClusterPeer) []string {
	all := append([]string{peer.Addr}, peer.Alts...)
	c.mu.Lock()
	good := c.goodAddr[peer.Addr]
	c.mu.Unlock()
	// Addresses first, names last: a name needs DNS, which may be this very cluster's gateway (paused or
	// restarting), while an address needs nothing. Within each group the last one that worked comes first.
	var ips, names []string
	seen := map[string]bool{}
	for _, a := range append([]string{good}, all...) {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		if h, _, err := net.SplitHostPort(a); err == nil && net.ParseIP(h) != nil {
			ips = append(ips, a)
		} else {
			names = append(names, a)
		}
	}
	return append(ips, names...)
}

func (c *Cluster) noteGoodAddr(peer ClusterPeer, addr string) {
	c.mu.Lock()
	c.goodAddr[peer.Addr] = addr
	c.mu.Unlock()
}

func (c *Cluster) callAddr(ctx context.Context, addr string, peer ClusterPeer, method, path string, body []byte, onBody func(io.Reader) error, timeout time.Duration, hdr http.Header) error {
	if !validHostPort(addr) || !strings.HasPrefix(path, "/") {
		return errors.New("refusing to call " + strconv.Quote(addr) + path)
	}
	self := c.node.Self()
	pj, _ := json.Marshal(self)
	peerHdr := base64.StdEncoding.EncodeToString(pj)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := randHex(12)
	req, err := http.NewRequestWithContext(ctx, method, peerURL(path), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Ddgw-Peer", peerHdr)
	req.Header.Set("X-Ddgw-Ts", ts)
	req.Header.Set("X-Ddgw-Nonce", nonce)
	bsum := sha256.Sum256(body)
	bodyHash := hex.EncodeToString(bsum[:])
	req.Header.Set("X-Ddgw-Body", bodyHash)
	req.Header.Set("X-Ddgw-Sig", signMessageHash(c.node.Secret(), peerHdr, ts, nonce, method, path, bodyHash))
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	me, _ := c.node.Identity()
	resp, err := pinnedClientAs(addr, peer.Fp, timeout, &me).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if hdr != nil {
		for k, v := range resp.Header {
			hdr[k] = v
		}
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error   string `json:"error"`
			Removed bool   `json:"removed"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return &peerError{Status: resp.StatusCode, Msg: e.Error, Removed: e.Removed}
	}
	return onBody(io.LimitReader(resp.Body, maxUploadBytes+1<<20))
}

// ── server handlers ──────────────────────────────────────────────────────────

func (c *Cluster) statusMsg() PeerStatusMsg {
	snap := c.node.Snapshot()
	host, _ := os.Hostname()
	return PeerStatusMsg{
		Hostname: host,
		Peer:     c.node.Self(), Epoch: snap.Epoch, Role: snap.Role, PrimaryAddr: snap.PrimaryAddr,
		Peers: append(c.knownPeers(snap), c.node.Self()), Removed: snap.Removed,
		Version: version(), SourceVersion: c.mg.upd.SourceVersion(), Updating: c.mg.upd.Busy(),
		UpdateFailed: c.mg.upd.FailedFor(), SharedRev: snap.SharedRev, Intent: c.mg.upd.Intent(),
		GwKnown: c.mg.gwFn != nil, Gateways: c.mg.localGateways(), NodePaused: c.mg.pausedFn != nil && c.mg.pausedFn(), Host: hostLoadPtr(), Addrs: ethernetAddrs(), GwIPs: c.mg.localGwIPs(),
	}
}

func (c *Cluster) knownPeers(snap ClusterSnapshot) []ClusterPeer { return snap.Peers }

func (c *Cluster) handleStatus(rw http.ResponseWriter, r *http.Request, caller ClusterPeer, _ []byte) {
	writeJSON(rw, http.StatusOK, c.statusMsg())
}

func (c *Cluster) handleState(rw http.ResponseWriter, r *http.Request, caller ClusterPeer, _ []byte) {
	snap := c.node.Snapshot()
	if snap.Role != RolePrimary {
		jsonError(rw, http.StatusConflict, "this node is not the primary")
		return
	}
	dc, _, err := c.mg.LiveConfig()
	if err != nil {
		jsonError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	msg := clusterStateMsg{
		Epoch: snap.Epoch, PrimaryAddr: snap.PrimaryAddr, Peers: append(snap.Peers, c.node.Self()),
		SharedRev: snap.SharedRev, Shared: sharedOf(dc), Seeds: seedsOf(dc), Intent: c.mg.upd.Intent(),
	}
	if dc.Cluster.ShareCert {
		if cp, kp, meta, ok := c.mg.certs.Managed(); ok && meta.Source != "" {
			msg.Cert = &adminCert{CertPEM: string(cp), KeyPEM: string(kp)}
		}
	}
	writeJSON(rw, http.StatusOK, msg)
}

func (c *Cluster) handleAnnounce(rw http.ResponseWriter, r *http.Request, caller ClusterPeer, body []byte) {
	var m announceMsg
	if json.Unmarshal(body, &m) != nil || m.PrimaryAddr == "" {
		jsonError(rw, http.StatusBadRequest, "bad announce")
		return
	}
	before := c.node.Snapshot()
	snap, err := c.node.AdoptAnnounce(m.Epoch, m.PrimaryAddr)
	switch {
	case errors.Is(err, ErrStaleEpoch):
		jsonError(rw, http.StatusConflict, fmt.Sprintf("stale epoch %d (this node is at %d)", m.Epoch, snap.Epoch))
		return
	case errors.Is(err, ErrEpochConflict):
		c.setConflict(fmt.Sprintf("%s claimed to be primary at epoch %d, but this node already follows %s at that epoch", m.PrimaryAddr, m.Epoch, snap.PrimaryAddr))
		jsonError(rw, http.StatusConflict, err.Error())
		return
	}
	if snap.Epoch != before.Epoch {
		infof("cluster: epoch %d — %s is primary (this node is now %s)", snap.Epoch, snap.PrimaryAddr, snap.Role)
		c.syncSoon()
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "epoch": snap.Epoch, "role": snap.Role})
}

func (c *Cluster) setConflict(s string) {
	c.mu.Lock()
	c.conflict = s
	c.mu.Unlock()
	warnf("cluster: %s", s)
}

// handlePeerAdd tells this node about a member that just joined through another node, so it does not have to wait
// for the next sync to serve it (a strict cluster refuses callers it has not been told about).
func (c *Cluster) handlePeerAdd(rw http.ResponseWriter, r *http.Request, caller ClusterPeer, body []byte) {
	var m struct {
		Peer ClusterPeer `json:"peer"`
		MTLS bool        `json:"mtls,omitempty"`
	}
	if json.Unmarshal(body, &m) != nil || !validPeer(m.Peer) {
		jsonError(rw, http.StatusBadRequest, "bad request")
		return
	}
	if c.node.AddPeer(m.Peer) {
		infof("cluster: %s (node %s) joined through %s", m.Peer.Addr, m.Peer.NodeID, caller.Addr)
	}
	if m.MTLS {
		c.node.NoteMTLS(m.Peer.Fp)
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (c *Cluster) handlePeerRemove(rw http.ResponseWriter, r *http.Request, caller ClusterPeer, body []byte) {
	var m struct {
		Addr string `json:"addr"`
	}
	if json.Unmarshal(body, &m) != nil || m.Addr == "" {
		jsonError(rw, http.StatusBadRequest, "bad request")
		return
	}
	if m.Addr == c.node.Self().Addr {
		c.leaveLocal("removed by an administrator of " + caller.Addr)
	} else {
		c.node.RemovePeer(m.Addr)
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (c *Cluster) handlePeerUnremove(rw http.ResponseWriter, r *http.Request, caller ClusterPeer, body []byte) {
	var m struct {
		Addr string `json:"addr"`
	}
	if json.Unmarshal(body, &m) != nil || m.Addr == "" {
		jsonError(rw, http.StatusBadRequest, "bad request")
		return
	}
	c.node.UnremovePeer(m.Addr)
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (c *Cluster) handleAdmin(rw http.ResponseWriter, r *http.Request, caller ClusterPeer, body []byte) {
	var m adminMsg
	if json.Unmarshal(body, &m) != nil || m.Op == "" {
		jsonError(rw, http.StatusBadRequest, "bad request")
		return
	}
	if c.node.Snapshot().Role != RolePrimary {
		jsonError(rw, http.StatusConflict, "this node is not the primary")
		return
	}
	res, err := c.mg.execAdminOp(m.Op, m.By+" (via "+caller.Addr+")", m.Payload)
	if err != nil {
		jsonError(rw, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if res == nil {
		res = json.RawMessage(`{}`)
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Write(res)
}

func (c *Cluster) handleSource(rw http.ResponseWriter, r *http.Request, caller ClusterPeer, _ []byte) {
	data, ver, err := c.mg.upd.SourceTarball()
	if err != nil {
		jsonError(rw, http.StatusNotFound, err.Error())
		return
	}
	rw.Header().Set("Content-Type", "application/gzip")
	rw.Header().Set("X-Ddgw-Version", ver)
	rw.Write(data)
}

func (c *Cluster) handleJoin(rw http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if _, locked := c.joinThrottled(ip); locked {
		jsonError(rw, http.StatusTooManyRequests, "too many failed attempts")
		return
	}
	var req joinRequest
	if !readJSON(rw, r, 16<<10, &req) {
		return
	}
	if req.Token == "" || !c.node.RedeemToken(req.Token) {
		c.joinFail(ip)
		time.Sleep(300 * time.Millisecond)
		jsonError(rw, http.StatusUnauthorized, "invalid or expired join token")
		return
	}
	peer := req.Peer
	if !req.ExplicitSelf { // trust the address we actually saw, with the joiner's port
		if _, port, err := net.SplitHostPort(peer.Addr); err == nil {
			peer.Addr = net.JoinHostPort(ip, port)
		}
	}
	if !validPeer(peer) {
		jsonError(rw, http.StatusBadRequest, "invalid peer description")
		return
	}
	c.node.UnremovePeer(peer.Addr)
	c.node.UnremoveFp(peer.Fp) // a token is an administrator's decision to let this identity in
	newcomer := peer.NodeID != "" && !c.knowsNode(peer.NodeID)
	c.node.AddPeer(peer)
	if newcomer && c.node.Snapshot().Role == RolePrimary && c.mg != nil {
		// A node that joins does not start serving every gateway: it is left out of each until an administrator adds it
		// (right-click the gateway ▸ Add node).  Done before the answer, so the joiner's first sync already carries it.
		c.mg.excludeNewNode(peer.NodeID)
	}
	if req.MTLS {
		c.node.NoteMTLS(peer.Fp)
	}
	snap := c.node.Snapshot()
	// tell the other members now: the joiner calls them (the primary above all) as soon as it has our answer
	c.broadcast(snap.Peers, "/cluster/peers/add", map[string]any{"peer": peer, "mtls": req.MTLS})
	infof("cluster: %s (node %s) joined", peer.Addr, peer.NodeID)
	writeJSON(rw, http.StatusOK, joinResponse{
		Secret: base64.RawStdEncoding.EncodeToString(c.node.Secret()), Epoch: snap.Epoch, PrimaryAddr: snap.PrimaryAddr,
		Server: c.node.Self(), Peers: append(snap.Peers, c.node.Self()),
	})
}

func (c *Cluster) joinThrottled(ip string) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f := c.joinFails[ip]; f != nil && time.Now().Before(f.locked) {
		return time.Until(f.locked), true
	}
	return 0, false
}

func (c *Cluster) joinFail(ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.joinFails[ip]
	if f == nil || time.Since(f.first) > failWindow {
		f = &failRec{first: time.Now()}
		c.joinFails[ip] = f
	}
	if f.count++; f.count >= 8 {
		f.locked = time.Now().Add(lockDuration)
		f.count = 0
	}
}

// ── admin API (also used by CLI/GUI) ─────────────────────────────────────────

func (c *Cluster) requireEnabled() error {
	if !c.Enabled() {
		return errors.New("clustering is disabled on this node (cluster.enabled is false)")
	}
	return nil
}

// sharedAddrs are the addresses that belong to the gateways and not to this node: every gateway's VIPs and its anycast
// addresses.  Every node answers on them, so one of them can never be a way to reach one particular node.
func (c *Cluster) sharedAddrs() map[string]bool {
	out := map[string]bool{}
	if c.mg == nil {
		return out
	}
	dc, _, err := c.mg.LiveConfig()
	if err != nil || dc == nil {
		return out
	}
	for _, g := range dc.Groups {
		for _, v := range append(g.vipsFor(afIPv4), g.vipsFor(afIPv6)...) {
			if a, err := vipAddr(v); err == nil {
				out[a.String()] = true
			}
		}
		for _, x := range g.ExtraVIPs {
			if a, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimSuffix(x, "/32"), "/128")); err == nil {
				out[a.String()] = true
			}
		}
	}
	return out
}

func (c *Cluster) candidateAddrs() []string {
	var ifs []ifaceAddrs
	if all, err := net.Interfaces(); err == nil {
		for _, i := range all {
			if as, err := i.Addrs(); err == nil {
				ifs = append(ifs, ifaceAddrs{Name: i.Name, Loopback: i.Flags&net.FlagLoopback != 0, Addrs: as})
			}
		}
	}
	host, _ := os.Hostname()
	return nodeAddrCandidates(c.node.Self().Addr, host, ifs, c.sharedAddrs())
}

type ifaceAddrs struct {
	Name     string
	Loopback bool
	Addrs    []net.Addr
}

// nodeAddrCandidates is the addresses this node tells the others it can be reached on: its cluster address, its host name
// and the addresses of its interfaces, each with the cluster port.  Left out: the loopback and its addresses (the anycast
// addresses live there), the virtual-MAC interfaces ddgwN.M (the VIP lives there), link-local addresses, and any address
// in shared (the VIPs and anycast addresses of the gateways, which every node answers on).  A peer that dialled one of
// those would reach whichever node holds it at the moment, and be refused there for the certificate.
func nodeAddrCandidates(self, host string, ifs []ifaceAddrs, shared map[string]bool) []string {
	_, port, _ := net.SplitHostPort(self)
	addrs := []string{self}
	if host != "" {
		addrs = append(addrs, net.JoinHostPort(host, port))
	}
	for _, i := range ifs {
		if i.Loopback || strings.HasPrefix(i.Name, "ddgw") {
			continue
		}
		for _, a := range i.Addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() && !shared[n.IP.String()] {
				addrs = append(addrs, net.JoinHostPort(n.IP.String(), port))
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, a := range addrs {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// refreshAlts re-reads this node's host name and interface addresses (IPv4 and
// IPv6) so peers can reach it by any of them; they are shared with every
// request and in the join code.
func (c *Cluster) refreshAlts() {
	all := c.candidateAddrs()
	self := c.node.Self().Addr
	var alts []string
	for _, a := range all {
		if a != self {
			alts = append(alts, a)
		}
	}
	if len(alts) > 12 {
		alts = alts[:12]
	}
	sort.Strings(alts)
	c.node.SetAlts(alts)
}

// MintJoinCode creates a single-use code that lets another node join.
func (c *Cluster) MintJoinCode(by string) (string, time.Time, error) {
	if err := c.requireEnabled(); err != nil {
		return "", time.Time{}, err
	}
	tok, exp, err := c.node.MintToken(by)
	if err != nil {
		return "", time.Time{}, err
	}
	b, _ := json.Marshal(joinCode{Token: tok, Fp: c.node.Fingerprint(), Addrs: c.candidateAddrs()})
	infof("cluster: join code minted by %s (valid until %s)", by, exp.Format(time.RFC3339))
	return joinCodePrefix + base64.RawURLEncoding.EncodeToString(b), exp, nil
}

func decodeJoinCode(code string) (joinCode, error) {
	var jc joinCode
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, joinCodePrefix) {
		return jc, errors.New("not a ddgw join code (expected it to start with \"" + joinCodePrefix + "\")")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, joinCodePrefix))
	if err != nil || json.Unmarshal(raw, &jc) != nil || jc.Token == "" || len(jc.Fp) != 64 || len(jc.Addrs) == 0 || len(jc.Addrs) > 32 {
		return jc, errors.New("the join code is damaged — copy it again in full")
	}
	for _, a := range jc.Addrs {
		if !validHostPort(a) {
			return jc, errors.New("the join code is damaged — copy it again in full")
		}
	}
	return jc, nil
}

// Join makes this node a member of the cluster the code came from.  Its shared
// settings are then replaced by the cluster's.
func (c *Cluster) Join(ctx context.Context, code, by string) error {
	if err := c.requireEnabled(); err != nil {
		return err
	}
	jc, err := decodeJoinCode(code)
	if err != nil {
		return err
	}
	if !c.node.Joinable() {
		return ErrNotJoinable
	}
	c.mu.Lock()
	explicit := c.cfg.Self != ""
	c.mu.Unlock()
	req := joinRequest{Token: jc.Token, Peer: c.node.Self(), ExplicitSelf: explicit, MTLS: true}
	var errs []string
	for _, addr := range jc.Addrs {
		if !validHostPort(addr) {
			errs = append(errs, "skipped an invalid address in the join code")
			continue
		}
		var res joinResponse
		body, _ := json.Marshal(req)
		hreq, err := http.NewRequestWithContext(ctx, "POST", peerURL("/cluster/join"), bytes.NewReader(body))
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		hreq.Header.Set("Content-Type", "application/json")
		resp, err := pinnedClient(addr, jc.Fp, 8*time.Second).Do(hreq)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", addr, err))
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			var e struct {
				Error string `json:"error"`
			}
			json.Unmarshal(data, &e)
			if e.Error == "" {
				e.Error = resp.Status
			}
			return fmt.Errorf("%s refused the join: %s", addr, e.Error)
		}
		if err := json.Unmarshal(data, &res); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", addr, err))
			continue
		}
		// The server keeps the address it calls itself (peers are identified by
		// it, including as primary); the address that worked is remembered and
		// listed among its alternatives.
		if addr != res.Server.Addr && !containsStr(res.Server.Alts, addr) {
			res.Server.Alts = append([]string{addr}, res.Server.Alts...)
		}
		c.noteGoodAddr(res.Server, addr)
		res.Server.Fp = jc.Fp
		// safety net: a way back to the configuration this node had
		if _, raw, err := c.mg.LiveConfig(); err == nil {
			if _, err := os.Stat(c.mg.confPath); err == nil {
				c.mg.versions.Record(raw, by, "before joining the cluster of "+addr, true)
			}
		}
		peers := append([]ClusterPeer{res.Server}, res.Peers...)
		primary := res.PrimaryAddr
		if err := c.node.JoinAs(res.Secret, res.Epoch, primary, peers); err != nil {
			return err
		}
		infof("cluster: joined the cluster via %s (primary %s, epoch %d) — requested by %s", addr, primary, res.Epoch, by)
		if err := c.SyncOnce(ctx); err != nil {
			warnf("cluster: joined, but the first sync failed: %v", err)
		}
		c.mg.usersPull(ctx, res.Server)
		return nil
	}
	return fmt.Errorf("could not reach the cluster: %s", strings.Join(errs, "; "))
}

// Promote makes this node the primary at the next epoch and tells the others.
func (c *Cluster) Promote(by string) (ClusterSnapshot, error) {
	if err := c.requireEnabled(); err != nil {
		return ClusterSnapshot{}, err
	}
	snap := c.node.Promote()
	warnf("cluster: this node was promoted to primary (epoch %d) by %s", snap.Epoch, by)
	c.announce(snap)
	return snap, nil
}

func (c *Cluster) announce(snap ClusterSnapshot) {
	var wg sync.WaitGroup
	for _, p := range snap.Peers {
		wg.Add(1)
		go func(p ClusterPeer) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), peerTimeout)
			defer cancel()
			if err := c.call(ctx, p, "POST", "/cluster/announce", announceMsg{snap.Epoch, snap.PrimaryAddr}, nil, peerTimeout); err != nil {
				warnf("cluster: could not announce epoch %d to %s: %v (it will learn on its next sync)", snap.Epoch, p.Addr, err)
			}
		}(p)
	}
	wg.Wait()
}

// RemovePeer removes a member cluster-wide.
func (c *Cluster) RemovePeer(addr, by string) error {
	if err := c.requireEnabled(); err != nil {
		return err
	}
	snap := c.node.Snapshot()
	if addr == snap.SelfAddr {
		return errors.New("use \"leave\" to remove this node itself")
	}
	if addr == snap.PrimaryAddr && snap.Role != RolePrimary {
		return errors.New("that node is the primary; promote this node first")
	}
	var target *ClusterPeer
	for i := range snap.Peers {
		if snap.Peers[i].Addr == addr {
			target = &snap.Peers[i]
		}
	}
	if target == nil {
		return fmt.Errorf("%s is not a member", addr)
	}
	c.node.RemovePeer(addr)
	warnf("cluster: %s removed by %s", addr, by)
	c.broadcast(snap.Peers, "/cluster/peers/remove", map[string]string{"addr": addr})
	return nil
}

func (c *Cluster) UnremovePeer(addr, by string) error {
	if err := c.requireEnabled(); err != nil {
		return err
	}
	if !c.node.UnremovePeer(addr) {
		return fmt.Errorf("%s is not on the removed list", addr)
	}
	c.broadcast(c.node.Snapshot().Peers, "/cluster/peers/unremove", map[string]string{"addr": addr})
	return nil
}

func (c *Cluster) broadcast(peers []ClusterPeer, path string, body any) {
	var wg sync.WaitGroup
	for _, p := range peers {
		wg.Add(1)
		go func(p ClusterPeer) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), peerTimeout)
			defer cancel()
			if err := c.call(ctx, p, "POST", path, body, nil, peerTimeout); err != nil {
				debugf("cluster: %s to %s: %v", path, p.Addr, err)
			}
		}(p)
	}
	wg.Wait()
}

// Leave tells the other members and becomes a single-node cluster again.
func (c *Cluster) Leave(by string) error {
	if err := c.requireEnabled(); err != nil {
		return err
	}
	snap := c.node.Snapshot()
	if len(snap.Peers) == 0 {
		return errors.New("this node is not part of a multi-node cluster")
	}
	if snap.Role == RolePrimary && len(snap.Peers) > 0 {
		warnf("cluster: leaving while primary — promote another member first if the others should keep a primary")
	}
	c.broadcast(snap.Peers, "/cluster/peers/remove", map[string]string{"addr": snap.SelfAddr})
	c.leaveLocal("left by " + by)
	return nil
}

func (c *Cluster) leaveLocal(why string) {
	c.node.Reset()
	c.mu.Lock()
	c.info = map[string]*peerInfo{}
	c.conflict = ""
	c.mu.Unlock()
	warnf("cluster: this node is no longer in the cluster (%s) — it is now its own single-node cluster", why)
}

// forwardAdmin sends an admin write to the primary and returns its reply.
func (c *Cluster) forwardAdmin(op, by string, payload any) (json.RawMessage, error) {
	snap := c.node.Snapshot()
	var primary *ClusterPeer
	for i := range snap.Peers {
		if snap.Peers[i].Addr == snap.PrimaryAddr {
			primary = &snap.Peers[i]
		}
	}
	if primary == nil {
		return nil, fmt.Errorf("the primary %s is not a known peer", snap.PrimaryAddr)
	}
	pb, _ := json.Marshal(payload)
	if payload == nil {
		pb = []byte("null")
	}
	var out json.RawMessage
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := c.call(ctx, *primary, "POST", "/cluster/admin", adminMsg{Op: op, By: by, Payload: pb}, &out, 30*time.Second)
	return out, err
}

// ── sync loop ────────────────────────────────────────────────────────────────

func (c *Cluster) syncSoon() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (c *Cluster) syncNow() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c.SyncOnce(ctx)
}

// Run is the background loop: poll peers, pull the primary's state, and
// consider a software update.  It also drives updates on unclustered nodes.
func (c *Cluster) Run(ctx context.Context) {
	for {
		c.mu.Lock()
		iv := time.Duration(c.cfg.SyncIntervalSec) * time.Second
		c.mu.Unlock()
		if iv < time.Second {
			iv = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(iv):
		case <-c.kick:
		}
		sctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		if c.Enabled() {
			c.SyncOnce(sctx)
		}
		c.mg.updateTick(sctx)
		cancel()
		c.logStrain()
	}
}

// SyncOnce polls every peer (epoch discovery, gossip), pulls state if this
// node is a replica, and records the outcome.
func (c *Cluster) SyncOnce(ctx context.Context) error {
	if !c.Enabled() {
		return nil
	}
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	c.refreshAlts()
	snap := c.node.Snapshot()

	type result struct {
		peer ClusterPeer
		msg  PeerStatusMsg
		err  error
	}
	results := make(chan result, len(snap.Peers))
	for _, p := range snap.Peers {
		go func(p ClusterPeer) {
			var m PeerStatusMsg
			cctx, cancel := context.WithTimeout(ctx, peerTimeout)
			defer cancel()
			err := c.call(cctx, p, "GET", "/cluster/status", nil, &m, peerTimeout)
			results <- result{p, m, err}
		}(p)
	}
	infos := map[string]*peerInfo{}
	for range snap.Peers {
		r := <-results
		pi := &peerInfo{Msg: r.msg, Reachable: r.err == nil}
		if r.err == nil {
			pi.LastSeen = time.Now()
		} else {
			pi.Err = r.err.Error()
			var pe *peerError
			if errors.As(r.err, &pe) && pe.Removed {
				c.leaveLocal("the cluster removed this node (" + r.peer.Addr + ")")
				return nil
			}
		}
		infos[r.peer.Addr] = pi
	}
	var firstErr error
	c.mu.Lock()
	for addr, pi := range infos {
		if old := c.info[addr]; old != nil && !pi.Reachable {
			pi.LastSeen = old.LastSeen
			pi.Msg = old.Msg
		}
		c.info[addr] = pi
	}
	for addr := range c.info {
		if _, ok := infos[addr]; !ok {
			delete(c.info, addr)
		}
	}
	c.mu.Unlock()

	// epoch discovery and gossip
	for addr, pi := range infos {
		if !pi.Reachable {
			continue
		}
		m := pi.Msg
		// Remember the peer's own addresses (saved with the member list), so that after a restart the cluster
		// can reach it without asking DNS, which may be the very service that is down.
		for _, p := range snap.Peers {
			if p.Addr == addr {
				if np, ok := learnPeerAddrs(p, addrList(m.Addrs)); ok {
					c.node.AddPeer(np)
				}
			}
		}
		if m.Epoch > c.node.Snapshot().Epoch {
			if s, err := c.node.AdoptAnnounce(m.Epoch, m.PrimaryAddr); err == nil {
				infof("cluster: learned from %s that %s is primary at epoch %d (this node is now %s)", addr, s.PrimaryAddr, s.Epoch, s.Role)
			}
		} else if m.Epoch == c.node.Snapshot().Epoch && m.PrimaryAddr != c.node.Snapshot().PrimaryAddr && m.PrimaryAddr != "" {
			c.setConflict(fmt.Sprintf("%s reports primary %s at epoch %d, this node follows %s — promote exactly one node to resolve", addr, m.PrimaryAddr, m.Epoch, c.node.Snapshot().PrimaryAddr))
		}
		for _, rem := range m.Removed {
			if rem == c.node.Self().Addr {
				c.leaveLocal("another member removed this node")
				return nil
			}
			if !containsStr(c.node.Snapshot().Removed, rem) {
				c.node.RemovePeer(rem)
			}
		}
		for _, p := range m.Peers {
			if validPeer(p) && c.node.AddPeer(p) {
				infof("cluster: learned peer %s from %s", p.Addr, addr)
			}
		}
	}

	snap = c.node.Snapshot()
	if snap.Role == RoleReplica {
		if err := c.pullState(ctx, snap); err != nil {
			firstErr = err
		}
	} else {
		c.mu.Lock()
		c.conflict = pickConflict(c.conflict, snap, infos)
		c.mu.Unlock()
	}
	c.mu.Lock()
	c.lastSync = time.Now()
	if firstErr != nil {
		c.lastErr = firstErr.Error()
	} else {
		c.lastErr = ""
	}
	c.mu.Unlock()
	return firstErr
}

// pickConflict clears a stale conflict message once everyone agrees again.
func pickConflict(cur string, snap ClusterSnapshot, infos map[string]*peerInfo) string {
	for _, pi := range infos {
		if pi.Reachable && pi.Msg.Epoch == snap.Epoch && pi.Msg.PrimaryAddr != snap.PrimaryAddr && pi.Msg.PrimaryAddr != "" {
			return cur
		}
	}
	return ""
}

func (c *Cluster) pullState(ctx context.Context, snap ClusterSnapshot) error {
	var primary *ClusterPeer
	for i := range snap.Peers {
		if snap.Peers[i].Addr == snap.PrimaryAddr {
			primary = &snap.Peers[i]
		}
	}
	if primary == nil {
		return fmt.Errorf("the primary %s is not a known peer", snap.PrimaryAddr)
	}
	var st clusterStateMsg
	cctx, cancel := context.WithTimeout(ctx, peerTimeout*2)
	defer cancel()
	if err := c.call(cctx, *primary, "GET", "/cluster/state", nil, &st, peerTimeout*2); err != nil {
		return fmt.Errorf("pulling state from the primary %s: %w", primary.Addr, err)
	}
	if st.Epoch != snap.Epoch {
		if st.Epoch > snap.Epoch {
			c.node.AdoptAnnounce(st.Epoch, st.PrimaryAddr)
		}
		return fmt.Errorf("the primary answered with epoch %d, this node is at %d", st.Epoch, snap.Epoch)
	}
	for _, p := range st.Peers {
		if validPeer(p) {
			c.node.AddPeer(p)
		}
	}
	c.mg.applyClusterShared(st.Shared, st.Seeds, st.SharedRev, primary.Addr)
	c.mg.upd.SetIntent(st.Intent)
	dc, _, err := c.mg.LiveConfig()
	if err == nil && dc.Cluster.ShareCert {
		var cp, kp []byte
		if st.Cert != nil {
			cp, kp = []byte(st.Cert.CertPEM), []byte(st.Cert.KeyPEM)
		}
		if err := c.mg.certs.ApplyCluster(cp, kp); err != nil {
			warnf("cluster: could not apply the shared certificate: %v", err)
		}
	}
	return nil
}

// ── views ────────────────────────────────────────────────────────────────────

type PeerView struct {
	Addr          string    `json:"addr"`
	NodeID        string    `json:"node_id"`
	Fingerprint   string    `json:"fingerprint"`
	Self          bool      `json:"self"`
	Reachable     bool      `json:"reachable"`
	Role          Role      `json:"role"`
	Epoch         uint64    `json:"epoch"`
	IsPrimary     bool      `json:"is_primary"`
	Version       string    `json:"version"`
	Hostname      string    `json:"hostname,omitempty"`
	SourceVersion string    `json:"source_version"`
	Updating      bool      `json:"updating"`
	UpdateFailed  string    `json:"update_failed,omitempty"`
	SharedRev     uint64    `json:"shared_rev"`
	LastSeen      time.Time `json:"last_seen,omitempty"`
	Error         string    `json:"error,omitempty"`
	// IPs are the node's addresses (IPv4, IPv6 global and unique local) and GwIPs the ones it uses in the gateway protocol
	IPs   []string `json:"ips,omitempty"`
	GwIPs []string `json:"gw_ips,omitempty"`
}

type ClusterView struct {
	Enabled       bool       `json:"enabled"`
	NodeID        string     `json:"node_id"`
	Self          string     `json:"self"`
	Role          Role       `json:"role"`
	Epoch         uint64     `json:"epoch"`
	PrimaryAddr   string     `json:"primary_addr"`
	Fingerprint   string     `json:"fingerprint"`
	Listen        string     `json:"listen"`
	Joinable      bool       `json:"joinable"`
	SharedRev     uint64     `json:"shared_rev"`
	Peers         []PeerView `json:"peers"`
	Removed       []string   `json:"removed"`
	LastSync      time.Time  `json:"last_sync,omitempty"`
	LastSyncError string     `json:"last_sync_error,omitempty"`
	Conflict      string     `json:"conflict,omitempty"`
	Warnings      []string   `json:"warnings"`
}

func (c *Cluster) View() ClusterView {
	snap := c.node.Snapshot()
	selfIPs, selfGw := addrList(ethernetAddrs()), c.mg.localGwIPs() // before c.mu: they ask the engines
	c.mu.Lock()
	defer c.mu.Unlock()
	v := ClusterView{
		Enabled: c.cfg.Enabled, NodeID: snap.NodeID, Self: snap.SelfAddr, Role: snap.Role, Epoch: snap.Epoch,
		PrimaryAddr: snap.PrimaryAddr, Fingerprint: c.node.Fingerprint(), Listen: c.cfg.Listen,
		SharedRev: snap.SharedRev, Removed: snap.Removed, LastSync: c.lastSync, LastSyncError: c.lastErr,
		Conflict: c.conflict, Peers: []PeerView{}, Warnings: []string{},
	}
	v.Joinable = snap.Role == RolePrimary && len(snap.Peers) == 0 && snap.Epoch == 1
	v.Peers = append(v.Peers, PeerView{
		Addr: snap.SelfAddr, NodeID: snap.NodeID, Fingerprint: c.node.Fingerprint(), Self: true, Reachable: true,
		Role: snap.Role, Epoch: snap.Epoch, IsPrimary: snap.Role == RolePrimary, Version: version(),
		SourceVersion: c.mg.upd.SourceVersion(), Updating: c.mg.upd.Busy(), UpdateFailed: c.mg.upd.FailedFor(),
		SharedRev: snap.SharedRev, LastSeen: time.Now(), Hostname: selfHost(), IPs: selfIPs, GwIPs: selfGw,
	})
	for _, p := range snap.Peers {
		pv := PeerView{Addr: p.Addr, NodeID: p.NodeID, Fingerprint: p.Fp, IsPrimary: p.Addr == snap.PrimaryAddr}
		if pi := c.info[p.Addr]; pi != nil {
			pv.Reachable, pv.LastSeen, pv.Error = pi.Reachable, pi.LastSeen, pi.Err
			m := pi.Msg
			pv.Role, pv.Epoch, pv.Version, pv.SourceVersion = m.Role, m.Epoch, m.Version, m.SourceVersion
			pv.Updating, pv.UpdateFailed, pv.SharedRev = m.Updating, m.UpdateFailed, m.SharedRev
			pv.Hostname = m.Hostname
			pv.IPs, pv.GwIPs = addrList(m.Addrs), m.GwIPs
		}
		v.Peers = append(v.Peers, pv)
	}
	sort.SliceStable(v.Peers[1:], func(i, j int) bool { return v.Peers[1+i].Addr < v.Peers[1+j].Addr })
	if !c.cfg.Enabled {
		v.Warnings = append(v.Warnings, "clustering is disabled on this node (cluster.enabled is false)")
	}
	if snap.Role == RoleReplica {
		pri := false
		for _, p := range v.Peers {
			if p.Addr == snap.PrimaryAddr && p.Reachable {
				pri = true
			}
		}
		if !pri {
			v.Warnings = append(v.Warnings, "the primary "+snap.PrimaryAddr+" is not reachable — settings changes will fail until it is back; promote this node if it is gone for good")
		}
	}
	return v
}

// updateNodes feeds the update status table.
func (c *Cluster) updateNodes() []UpdateNode {
	intent := c.mg.upd.Intent()
	snap := c.node.Snapshot()
	self := UpdateNode{
		Addr: snap.SelfAddr, Self: true, Reachable: true, Running: version(), Source: c.mg.upd.SourceVersion(),
		Queued: intent.wants(snap.SelfAddr), Updating: c.mg.upd.Busy(), Failed: c.mg.upd.FailedFor(),
	}
	self.Behind = versionGreater(self.Source, self.Running)
	out := []UpdateNode{self}
	if !c.Enabled() {
		return out
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range snap.Peers {
		n := UpdateNode{Addr: p.Addr, Queued: intent.wants(p.Addr)}
		if pi := c.info[p.Addr]; pi != nil {
			n.Reachable, n.Running, n.Source, n.Updating, n.Failed = pi.Reachable, pi.Msg.Version, pi.Msg.SourceVersion, pi.Msg.Updating, pi.Msg.UpdateFailed
		}
		out = append(out, n)
	}
	return out
}

// bestSourcePeer returns the reachable peer offering the highest source version.
func (c *Cluster) bestSourcePeer(above string) (ClusterPeer, string, bool) {
	snap := c.node.Snapshot()
	c.mu.Lock()
	defer c.mu.Unlock()
	best, bestVer := ClusterPeer{}, above
	for _, p := range snap.Peers {
		if pi := c.info[p.Addr]; pi != nil && pi.Reachable && versionGreater(pi.Msg.SourceVersion, bestVer) {
			best, bestVer = p, pi.Msg.SourceVersion
		}
	}
	return best, bestVer, best.Addr != ""
}

// pullSource downloads and stages a peer's source tree.
func (c *Cluster) pullSource(ctx context.Context, peer ClusterPeer) (string, error) {
	var buf bytes.Buffer
	err := c.callRaw(ctx, peer, "GET", "/cluster/source", nil, func(r io.Reader) error {
		_, err := io.Copy(&buf, r)
		return err
	}, sourceTimeout, nil)
	if err != nil {
		return "", err
	}
	return c.mg.upd.ExtractSource(buf.Bytes())
}

// shouldWaitForOthers staggers a rolling update: wait while another reachable
// member is updating, or while an earlier-sorted one that also wants and needs
// the update (and hasn't failed on it) is still to go.
//
// An earlier-sorted member that cannot go yet because it is the only one serving
// a gateway (the others are paused or down) is not waited for: it would hold
// everyone behind it forever, including the paused members that serve nothing and
// are the very ones that can update safely.
func (c *Cluster) shouldWaitForOthers(target string, intent UpdateIntent) bool {
	snap := c.node.Snapshot()
	var mine []GwState
	if c.mg != nil {
		mine = c.mg.localGateways() // before c.mu: it takes its own lock
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// what every reachable member (self included) says it serves
	members := []peerGateways{selfGateways(snap.SelfAddr, c.mg != nil && c.mg.gwFn != nil, mine)}
	for _, p := range snap.Peers {
		if pi := c.info[p.Addr]; pi != nil && pi.Reachable {
			members = append(members, gatewaysOf(p.Addr, pi))
		}
	}
	for _, p := range snap.Peers {
		pi := c.info[p.Addr]
		if pi == nil || !pi.Reachable {
			continue
		}
		if pi.Msg.Updating {
			return true
		}
		if p.Addr < snap.SelfAddr && intent.wants(p.Addr) && versionGreater(target, pi.Msg.Version) && pi.Msg.UpdateFailed != target {
			if heldBySafety(p.Addr, pi.Msg.Gateways, members) {
				continue
			}
			return true
		}
	}
	return false
}

// selfGateways and gatewaysOf express one member's gateway report as a peerGateways.
func selfGateways(addr string, known bool, gs []GwState) peerGateways {
	pg := peerGateways{Addr: addr, Known: known, Serving: map[int]bool{}, Fresh: map[int]bool{}, Offnet: map[int]bool{}}
	for _, g := range gs {
		pg.Serving[g.GroupID], pg.Fresh[g.GroupID], pg.Offnet[g.GroupID] = g.Serving, g.Fresh, g.Offnet
	}
	return pg
}

func gatewaysOf(addr string, pi *peerInfo) peerGateways {
	return selfGateways(addr, pi.Msg.GwKnown, pi.Msg.Gateways)
}

// heldBySafety reports whether member addr, which reports gws, is currently
// stopped by the "keep every gateway served" rule: it serves a gateway that no
// other reachable member serves.  members includes addr itself, which is left out.
func heldBySafety(addr string, gws []GwState, members []peerGateways) bool {
	others := make([]peerGateways, 0, len(members))
	for _, m := range members {
		if m.Addr != addr {
			others = append(others, m)
		}
	}
	ok, _ := safeToTakeDown(gws, others)
	return !ok
}

// updateTick decides whether this node should update now (see umiss's
// maybeSelfUpdateManager).  Called every sync interval on every node.
func (m *Mgmt) updateTick(ctx context.Context) {
	self := m.cl.selfAddrForEvents()
	run := version()
	intent := m.upd.Intent()

	// a node that is up to date clears its own queue entry
	// "up to date" must take the cluster's sources into account, or a node
	// that has no source of its own would drop its entry before it could pull.
	best := m.bestKnownSource(run)
	if containsStr(intent.Pending, self) && !versionGreater(best, run) && m.upd.RolledBackNotice() == "" {
		if _, err := m.runOnPrimary("update-done", "self", adminNodes{Nodes: []string{self}}); err != nil {
			debugf("update: could not clear queue entry: %v", err)
		}
	}
	if m.upd.Busy() || !intent.wants(self) {
		return
	}
	target := m.upd.SourceVersion()
	var srcPeer ClusterPeer
	havePeer := false
	if m.cl.Enabled() {
		if p, v, ok := m.cl.bestSourcePeer(max2(target, run)); ok {
			srcPeer, target, havePeer = p, v, true
		}
	}
	if !versionGreater(target, run) {
		return
	}
	if m.upd.FailedFor() == target {
		return
	}
	if m.cl.Enabled() && m.cl.shouldWaitForOthers(target, intent) {
		return
	}
	if ok, why := m.updateSafeToApply(); !ok {
		m.upd.SetWaiting(why)
		return
	}
	m.upd.SetWaiting("")
	if havePeer {
		infof("update: pulling source v%s from %s", target, srcPeer.Addr)
		v, err := m.cl.pullSource(ctx, srcPeer)
		if err != nil {
			m.upd.noteFailure(self, run, target, "pulling source from "+srcPeer.Addr+": "+err.Error())
			return
		}
		m.upd.Record(UpdateEvent{Node: self, Kind: "pulled", To: v, Detail: "source from " + srcPeer.Addr})
	}
	if _, err := m.upd.Apply(ctx, self, "queued update"); err != nil {
		debugf("update: %v", err)
		return
	}
	m.scheduleRestart(false)
}

// bestKnownSource is the highest source version this node could install: its
// own staged source or the best one a reachable peer offers.
func (m *Mgmt) bestKnownSource(run string) string {
	best := m.upd.SourceVersion()
	if m.cl.Enabled() {
		if _, v, ok := m.cl.bestSourcePeer(max2(best, run)); ok {
			best = v
		}
	}
	return best
}

func max2(a, b string) string {
	if versionGreater(a, b) {
		return a
	}
	return b
}

// ── keeping the gateways served during a rolling update ──────────────────────

// GwState says whether this node is serving one gateway right now: it holds the
// address (AGC) or forwards for it (AFN) in every address family the gateway
// has and, with a DNS proxy, the DNS listener is up.
type GwState struct {
	GroupID int  `json:"group_id"`
	Serving bool `json:"serving"`
	// Fresh: serving, but only for the last few seconds (servingSettle).  Clients
	// have not all moved back yet, so it does not count as cover for a neighbour's
	// restart.  Absent (older nodes) means settled.
	Fresh bool `json:"fresh,omitempty"`
	// Offnet: this node cannot take part in the gateway at all (it is not on the gateway's subnet), so it can never cover it
	// for another node.  Absent on older nodes.
	Offnet bool `json:"offnet,omitempty"`
	// Health is the gateway's colour on that node as its own drawing shows it (ok, warn, bad, idle) and
	// HealthWhy the reason ("running, but some DNS servers are down").  Absent on older nodes.
	Health    string `json:"health,omitempty"`
	HealthWhy string `json:"health_why,omitempty"`
}

// servingSettle is how long a gateway must have been served continuously before
// another member may restart on the strength of it.
var servingSettle = 15 * time.Second

// gatewayStates computes what this node serves.  Paused gateways are left out:
// this node is not expected to serve them.
func gatewayStates(dc *DaemonConfig, rows []SnapshotRow) []GwState {
	out := []GwState{}
	for i := range dc.Groups {
		g := &dc.Groups[i]
		if g.Paused {
			continue
		}
		serving, dnsUp := map[string]bool{}, false
		for _, r := range rows {
			if r.GroupID == g.GroupID && r.Local {
				serving[r.AF] = r.State == "active" || r.State == "forward"
				dnsUp = dnsUp || r.DNSUp
			}
		}
		ok := true
		for _, af := range []struct {
			name string
			vip  string
		}{{"v4", g.VIP4}, {"v6", g.VIP6}} {
			if af.vip != "" && !serving[af.name] {
				ok = false
			}
		}
		if !dnsUp {
			ok = false
		}
		out = append(out, GwState{GroupID: g.GroupID, Serving: ok})
	}
	return out
}

func (m *Mgmt) localGateways() []GwState {
	if m.gwFn == nil {
		return nil
	}
	gs := m.gwFn()
	m.servMu.Lock()
	defer m.servMu.Unlock()
	if m.servSince == nil {
		m.servSince = map[int]time.Time{}
	}
	seen := map[int]bool{}
	for i := range gs {
		gid := gs[i].GroupID
		seen[gid] = true
		if !gs[i].Serving {
			delete(m.servSince, gid)
			continue
		}
		since, ok := m.servSince[gid]
		if !ok {
			since = time.Now()
			m.servSince[gid] = since
		}
		gs[i].Fresh = time.Since(since) < servingSettle
	}
	for gid := range m.servSince {
		if !seen[gid] {
			delete(m.servSince, gid)
		}
	}
	return gs
}

// peerGateways is what one other cluster member reported.
type peerGateways struct {
	Addr    string
	NodeID  string // the member's node ID, when known
	Known   bool
	Serving map[int]bool
	Fresh   map[int]bool // serving, but not yet for servingSettle
	Offnet  map[int]bool // cannot take part in the gateway (another subnet): never a cover for it
	Down    bool         // not reachable now: it may come back, so it is still a possible cover
}

// safeToTakeDown reports whether this node may go down now: for every gateway
// it is serving, some other reachable member must be serving it too, so the
// gateway never goes unserved while members update one after another.
func safeToTakeDown(mine []GwState, peers []peerGateways) (bool, string) {
	for _, g := range mine {
		if !g.Serving {
			continue // already not serving: nothing more to lose
		}
		others, fresh, capable := 0, 0, 0
		for _, p := range peers {
			if p.Down {
				capable++ // not reachable now, and may be the one that could cover it
				continue
			}
			if !p.Known || !p.Offnet[g.GroupID] {
				capable++
			}
			if !p.Known || (p.Serving[g.GroupID] && !p.Fresh[g.GroupID]) { // an older node cannot say; assume it is fine
				others++
			} else if p.Serving[g.GroupID] {
				fresh++
			}
		}
		if others == 0 && fresh > 0 {
			return false, fmt.Sprintf("gateway %d is only just being served again by the other member — waiting a few seconds for clients to move back", g.GroupID)
		}
		if others == 0 && capable == 0 && len(peers) > 0 {
			continue // every other member is on another subnet and can never serve it: waiting would be for ever
		}
		if others == 0 {
			// say what each other member is doing, so the reason is not a guess
			var who []string
			for _, p := range peers {
				switch {
				case p.Down:
					who = append(who, p.Addr+" is not reachable")
				case p.Offnet[g.GroupID]:
					who = append(who, p.Addr+" cannot serve it (another subnet, or removed from the gateway)")
				case p.Serving[g.GroupID]:
					who = append(who, p.Addr+" has only just started serving it")
				default:
					who = append(who, p.Addr+" is not serving it (paused, still recovering, or its DNS servers are down)")
				}
			}
			detail := "this is the only member"
			if len(who) > 0 {
				detail = strings.Join(who, "; ")
			}
			return false, fmt.Sprintf("gateway %d would have no other cluster member serving it (%s) — resume a node that has already updated, or force it with --update-apply --yes", g.GroupID, detail)
		}
	}
	return true, ""
}

// updateSafe applies safeToTakeDown with what the other members last reported.
// A node that is not clustered, or serves nothing, can always update.
func (m *Mgmt) updateSafe() (bool, string) {
	if m.gwFn == nil || !m.cl.Enabled() {
		return true, ""
	}
	peers := m.cl.peerGateways()
	// A member that has been removed from a gateway (a shared setting every node holds) can never serve it, whatever
	// version it runs and whatever it says: it does not count as a cover for that gateway.
	if dc, _, err := m.LiveConfig(); err == nil {
		for i := range peers {
			for _, g := range dc.Groups {
				if peers[i].NodeID != "" && containsStr(g.ExcludedNodes, peers[i].NodeID) {
					if peers[i].Offnet == nil {
						peers[i].Offnet = map[int]bool{}
					}
					peers[i].Offnet[g.GroupID] = true
				}
			}
		}
	}
	return safeToTakeDown(m.localGateways(), peers)
}

// updateSafeToApply is updateSafe for installing an update: a node that knows no other member is never held back, since no
// node exists that could ever cover its gateways and waiting would be for ever (a brief restart is the price of updating
// a single node).  Power actions keep the stricter updateSafe: rebooting the only server of a gateway stays a decision.
func (m *Mgmt) updateSafeToApply() (bool, string) {
	if m.cl != nil && len(m.cl.node.Snapshot().Peers) == 0 {
		return true, ""
	}
	return m.updateSafe()
}

func (c *Cluster) peerGateways() []peerGateways {
	snap := c.node.Snapshot()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []peerGateways
	for _, p := range snap.Peers {
		pi := c.info[p.Addr]
		if pi == nil || !pi.Reachable {
			out = append(out, peerGateways{Addr: p.Addr, NodeID: p.NodeID, Down: true})
			continue
		}
		pg := peerGateways{Addr: p.Addr, NodeID: p.NodeID, Known: pi.Msg.GwKnown, Serving: map[int]bool{}, Fresh: map[int]bool{}, Offnet: map[int]bool{}}
		for _, g := range pi.Msg.Gateways {
			pg.Serving[g.GroupID] = g.Serving
			pg.Fresh[g.GroupID] = g.Fresh
			pg.Offnet[g.GroupID] = g.Offnet
		}
		out = append(out, pg)
	}
	return out
}

func selfHost() string { h, _ := os.Hostname(); return h }

// handleUsers applies an account change another member made (users.go).
func (c *Cluster) handleUsers(rw http.ResponseWriter, r *http.Request, caller ClusterPeer, body []byte) {
	var m usersMsg
	if json.Unmarshal(body, &m) != nil || m.Op == "" {
		jsonError(rw, http.StatusBadRequest, "bad request")
		return
	}
	if m.Op == "list" { // a node that has just joined asks for the accounts
		b, _ := json.Marshal(map[string]any{"users": c.mg.usersExport()})
		rw.Header().Set("Content-Type", "application/json")
		rw.Write(b)
		return
	}
	if err := c.mg.usersPeer(m, firstNonEmpty(m.By, "a member")+" (via "+caller.Addr+")"); err != nil {
		jsonError(rw, http.StatusUnprocessableEntity, err.Error())
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Write([]byte(`{}`))
}

// learnPeerAddrs returns p with its alternative addresses brought up to date with the IP addresses the peer
// reported about itself (ips, without port): the IP alternatives are replaced by them, names are kept. ok is
// false when nothing changes. At most 12 alternatives are kept, as in refreshAlts.
func learnPeerAddrs(p ClusterPeer, ips []string) (ClusterPeer, bool) {
	_, port, err := net.SplitHostPort(p.Addr)
	if err != nil || len(ips) == 0 {
		return p, false
	}
	var alts []string
	for _, a := range p.Alts {
		if h, _, err := net.SplitHostPort(a); err == nil && net.ParseIP(h) == nil {
			alts = append(alts, a)
		}
	}
	var have []string
	for _, ip := range ips {
		if net.ParseIP(ip) == nil {
			continue
		}
		a := net.JoinHostPort(ip, port)
		if a != p.Addr && !containsStr(alts, a) {
			have = append(have, a)
		}
	}
	alts = append(have, alts...)
	if len(alts) > 12 {
		alts = alts[:12]
	}
	if len(alts) == len(p.Alts) {
		same := true
		for i := range alts {
			if alts[i] != p.Alts[i] {
				same = false
			}
		}
		if same {
			return p, false
		}
	}
	p.Alts = alts
	return p, true
}

// knowsNode says whether a member with this node ID is already in the member list.
func (c *Cluster) knowsNode(id string) bool {
	for _, p := range c.node.Snapshot().Peers {
		if p.NodeID == id {
			return true
		}
	}
	return false
}

// excludeNewNode leaves a node that has just joined the cluster out of every gateway (the shared excluded_nodes list), so
// that joining does not change which nodes answer for a gateway; an administrator adds it where it should serve.
func (m *Mgmt) excludeNewNode(id string) {
	dc, _, err := m.LiveConfig()
	if err != nil || dc == nil {
		return
	}
	changed := false
	for i := range dc.Groups {
		if !containsStr(dc.Groups[i].ExcludedNodes, id) {
			dc.Groups[i].ExcludedNodes = append(dc.Groups[i].ExcludedNodes, id)
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := m.PutConfig(dc, "cluster join", "a new node is not added to the gateways"); err != nil {
		warnf("cluster: could not leave the new node %s out of the gateways: %v", id, err)
		return
	}
	infof("cluster: node %s joined; it serves no gateway until it is added (right-click the gateway ▸ Add node)", id)
}
