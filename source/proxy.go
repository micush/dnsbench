package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"
)

// Node picker: the GUI on any cluster node can configure and monitor any other
// node.  The browser keeps talking to the node it logged in to; that node
// relays the request over the existing pinned-TLS, HMAC-signed cluster channel
// (POST /cluster/proxy), and the target node runs it through its own web
// handlers as though the user had made it there, attributed "user via node".
// The user is authenticated and authorised (PAM, group ddgw) on the node they
// logged in to only; peers trust each other with the cluster secret already.
// Everything but login, logout and the session can be relayed — the Cluster and
// Upgrade pages included, so "Leave cluster", "Promote" or "Update now" act on
// the node picked in the top bar.

type proxyCtxKey struct{}

// proxyPrefixes are the API areas that can be driven on another node: all of
// them except login, logout and the session, which belong to the node the
// operator logged in to.
var proxyPrefixes = []string{
	"/api/gateways", "/api/neighbors", "/api/dns", "/api/canvas", "/api/assert-agc",
	"/api/config", "/api/versions", "/api/tls", "/api/cluster", "/api/update", "/api/power", "/api/users", "/api/nodepause", "/api/bgp", "/api/log", "/api/qstats", "/api/scan", "/api/whois", "/api/host", "/api/serverstats", "/api/dnsupdates", "/api/dnslookup", "/api/capture", "/api/vmactest", "/api/tshoot/node",
}

// proxyAllowed reports why method+rawPath may not be relayed, or nil.
func proxyAllowed(method, rawPath string) error {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut:
	default:
		return fmt.Errorf("method %s cannot be relayed", method)
	}
	if len(rawPath) < 6 || !strings.HasPrefix(rawPath, "/api/") {
		return errors.New("only /api/ paths can be relayed")
	}
	lower := strings.ToLower(rawPath)
	if strings.Contains(rawPath, "..") || strings.Contains(rawPath, "\\") || strings.Contains(lower, "%2e") ||
		strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.ContainsAny(rawPath, "\r\n\x00") {
		return errors.New("path not allowed")
	}
	u, err := url.Parse(rawPath)
	if err != nil || u.Host != "" || u.Scheme != "" || !strings.HasPrefix(u.Path, "/api/") {
		return errors.New("path not allowed")
	}
	for _, p := range proxyPrefixes {
		if u.Path == p || strings.HasPrefix(u.Path, p+"/") {
			return nil
		}
	}
	return errors.New("this part of the GUI always acts on the node you are logged in to")
}

// maxRelayBody keeps a relayed request, base64-encoded in its envelope, inside
// the peer channel's body limit.
const maxRelayBody = 5 << 20

type proxyReq struct {
	User   string `json:"user"`
	Method string `json:"method"`
	Path   string `json:"path"` // /api/...?query
	CT     string `json:"ct,omitempty"`
	Body   []byte `json:"body,omitempty"`
}

type proxyResp struct {
	Status int    `json:"status"`
	CT     string `json:"ct,omitempty"`
	Disp   string `json:"disp,omitempty"`
	Body   []byte `json:"body,omitempty"`
}

// handleProxy runs on the TARGET node (peer-authenticated).
func (c *Cluster) handleProxy(rw http.ResponseWriter, r *http.Request, caller ClusterPeer, body []byte) {
	var req proxyReq
	if err := json.Unmarshal(body, &req); err != nil {
		jsonError(rw, http.StatusBadRequest, "bad request")
		return
	}
	if err := proxyAllowed(req.Method, req.Path); err != nil {
		jsonError(rw, http.StatusForbidden, err.Error())
		return
	}
	h := c.mg.webH
	if h == nil {
		jsonError(rw, http.StatusServiceUnavailable, "the web interface is not running on that node")
		return
	}
	user := req.User
	if len(user) > maxUsernameLen || user == "" || strings.ContainsAny(user, " \r\n\t") {
		user = "unknown"
	}
	s := &session{user: user + " via " + caller.Addr}
	ctx := context.WithValue(r.Context(), proxyCtxKey{}, s)
	in, err := http.NewRequestWithContext(ctx, req.Method, req.Path, bytes.NewReader(req.Body))
	if err != nil {
		jsonError(rw, http.StatusBadRequest, "bad request")
		return
	}
	in.ContentLength = int64(len(req.Body))
	if req.CT != "" {
		in.Header.Set("Content-Type", req.CT)
	}
	if host, _, err := net.SplitHostPort(caller.Addr); err == nil {
		in.RemoteAddr = net.JoinHostPort(host, "0")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, in)
	res := rec.Result()
	out, _ := io.ReadAll(io.LimitReader(res.Body, maxPeerBody))
	writeJSON(rw, http.StatusOK, proxyResp{
		Status: res.StatusCode, CT: res.Header.Get("Content-Type"),
		Disp: res.Header.Get("Content-Disposition"), Body: out,
	})
}

// Relay sends req to the peer at addr (SOURCE node side).
func (c *Cluster) Relay(ctx context.Context, addr string, req proxyReq) (*proxyResp, error) {
	if !c.Enabled() {
		return nil, errors.New("this node is not in a cluster")
	}
	snap := c.node.Snapshot()
	if containsStr(snap.Removed, addr) {
		return nil, errors.New("that node was removed from the cluster")
	}
	var peer *ClusterPeer
	for i := range snap.Peers {
		if snap.Peers[i].Addr == addr {
			peer = &snap.Peers[i]
		}
	}
	if peer == nil {
		return nil, errors.New("no such cluster node")
	}
	var resp proxyResp
	if err := c.call(ctx, *peer, "POST", "/cluster/proxy", req, &resp, 90*time.Second); err != nil {
		return nil, err
	}
	return &resp, nil
}

// handleProxy is the browser-facing side: /api/proxy?node=ADDR&path=/api/...
func (w *WebServer) handleProxy(rw http.ResponseWriter, r *http.Request, s *session) {
	node, path := r.URL.Query().Get("node"), r.URL.Query().Get("path")
	if node == "" || path == "" {
		jsonError(rw, http.StatusBadRequest, "node and path are required")
		return
	}
	if err := proxyAllowed(r.Method, path); err != nil {
		jsonError(rw, http.StatusForbidden, err.Error())
		return
	}
	if w.mg.cl == nil {
		jsonError(rw, http.StatusBadRequest, "this node is not in a cluster")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, maxRelayBody))
	if err != nil {
		jsonError(rw, http.StatusRequestEntityTooLarge, "too large to send through another node (limit 5 MB) — use that node's own GUI for this upload")
		return
	}
	if r.Method != http.MethodGet {
		warnf("web: %q relaying %s %s to %s (from %s)", s.user, r.Method, strings.SplitN(path, "?", 2)[0], node, clientIP(r))
	}
	resp, err := w.mg.cl.Relay(r.Context(), node, proxyReq{
		User: s.user, Method: r.Method, Path: path, CT: r.Header.Get("Content-Type"), Body: body,
	})
	if err != nil {
		code := http.StatusBadGateway
		var pe *peerError
		if errors.As(err, &pe) && pe.Status == http.StatusForbidden {
			code = http.StatusForbidden
		}
		jsonError(rw, code, "node "+node+": "+err.Error())
		return
	}
	if resp.CT != "" {
		rw.Header().Set("Content-Type", resp.CT)
	}
	if resp.Disp != "" {
		rw.Header().Set("Content-Disposition", resp.Disp)
	}
	rw.WriteHeader(resp.Status)
	rw.Write(resp.Body)
}
