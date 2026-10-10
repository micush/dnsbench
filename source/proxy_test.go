package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyAllowed(t *testing.T) {
	ok := []struct{ m, p string }{
		{"GET", "/api/canvas"}, {"PUT", "/api/config"}, {"GET", "/api/versions/diff?a=1&b=2"},
		{"POST", "/api/tls/install"}, {"GET", "/api/versions"}, {"POST", "/api/assert-agc"}, {"GET", "/api/dns"},
		// the Cluster and Upgrade pages follow the picked node too
		{"GET", "/api/cluster"}, {"POST", "/api/cluster/leave"}, {"POST", "/api/cluster/promote"},
		{"GET", "/api/update"}, {"POST", "/api/update/apply"}, {"POST", "/api/update/upload"}, {"POST", "/api/update/push"},
		{"GET", "/api/power"}, {"POST", "/api/power"}, {"GET", "/api/nodepause"}, {"POST", "/api/nodepause"}, {"GET", "/api/log?level=warn&q=x"}, {"GET", "/api/qstats?from=1d"}, {"GET", "/api/whois?domain=example.com"}, {"GET", "/api/host?from=1d"}, {"GET", "/api/dnsupdates"},
	}
	for _, c := range ok {
		if err := proxyAllowed(c.m, c.p); err != nil {
			t.Errorf("%s %s refused: %v", c.m, c.p, err)
		}
	}
	bad := []struct{ m, p string }{
		{"POST", "/api/login"}, {"POST", "/api/logout"}, {"GET", "/api/clusterx"}, {"GET", "/api/updates"},
		{"GET", "/api/session"}, {"POST", "/api/proxy"}, {"GET", "/api/proxy?node=x&path=/api/canvas"},
		{"GET", "/api/../login"}, {"GET", "/api/canvas/../login"}, {"GET", "/api/%2e%2e/login"},
		{"GET", "/api/canvas%2f..%2flogin"}, {"GET", "/api/configx"}, {"GET", "/api/"}, {"GET", "/"},
		{"GET", "/app.js"}, {"GET", "//evil/api/canvas"}, {"GET", "http://evil/api/canvas"},
		{"DELETE", "/api/config"}, {"GET", "/api/canvas\r\nX: y"}, {"GET", ""},
	}
	for _, c := range bad {
		if err := proxyAllowed(c.m, c.p); err == nil {
			t.Errorf("%s %q was allowed", c.m, c.p)
		}
	}
}

// A relayed request is attributed "user via node", needs no cookie or CSRF
// token (the signed cluster channel vouches for it), and cannot reach the
// local-only routes; a plain browser request carrying no session is still refused.
func TestProxyHandlerRunsAsViaUser(t *testing.T) {
	e := newWebEnv(t)
	e.mg.webH = e.ws.Handler()
	c := &Cluster{mg: e.mg}
	call := func(req proxyReq) (int, proxyResp) {
		b, _ := json.Marshal(req)
		rw := httptest.NewRecorder()
		c.handleProxy(rw, httptest.NewRequest("POST", "/cluster/proxy", bytes.NewReader(b)).WithContext(context.Background()),
			ClusterPeer{Addr: "10.0.0.7:53852"}, b)
		var pr proxyResp
		json.Unmarshal(rw.Body.Bytes(), &pr)
		return rw.Code, pr
	}
	code, pr := call(proxyReq{User: "alice", Method: "GET", Path: "/api/canvas"})
	if code != 200 || pr.Status != 200 {
		t.Fatalf("canvas: %d / %d %s", code, pr.Status, pr.Body)
	}
	// login, session and logout are refused at the envelope level
	for _, p := range []string{"/api/login", "/api/login/state", "/api/session", "/api/logout"} {
		if code, _ := call(proxyReq{User: "alice", Method: "GET", Path: p}); code != http.StatusForbidden {
			t.Errorf("%s relayed (%d)", p, code)
		}
	}
	// the attribution recorded in history is "alice via <node>"
	dc, _, err := e.mg.LiveConfig()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"config": dc, "note": "relayed"})
	_, pr = call(proxyReq{User: "alice", Method: "PUT", Path: "/api/config", CT: "application/json", Body: b})
	if pr.Status != 200 {
		t.Fatalf("put config: %d %s", pr.Status, pr.Body)
	}
	vs := e.mg.versions.List()
	if len(vs) == 0 {
		t.Fatal("no versions recorded")
	}
	if got := vs[0].Actor; got != "alice via 10.0.0.7:53852" {
		t.Errorf("actor = %q", got)
	}
	// a hostile user name is not trusted
	_, pr = call(proxyReq{User: "a b\nc", Method: "GET", Path: "/api/canvas"})
	if pr.Status != 200 {
		t.Errorf("sanitised user: %d", pr.Status)
	}
	// and the bypass is not reachable from outside: no session, no entry
	if r := e.do("GET", "/api/canvas", nil); r.code != 401 {
		t.Errorf("anonymous canvas = %d", r.code)
	}
	if r := e.do("GET", "/api/proxy?node=x&path=/api/canvas", nil); r.code != 401 {
		t.Errorf("anonymous proxy = %d", r.code)
	}
}

// A relayed upload carries raw bytes, not JSON: the browser-facing relay route
// must not insist on a JSON content type (it once did, which broke uploading a
// release archive to another node).
func TestProxyAcceptsRawUploads(t *testing.T) {
	e := newWebEnv(t)
	e.login("alice", "pw")
	r := e.do("POST", "/api/proxy?node=10.0.0.9:53854&path=/api/update/upload", nil, withAuth(e, true), func(q *http.Request) {
		q.Header.Set("Content-Type", "application/octet-stream")
		q.Body = io.NopCloser(bytes.NewReader([]byte("not a real archive")))
		q.ContentLength = 18
	})
	if r.code == http.StatusUnsupportedMediaType {
		t.Fatalf("raw upload refused for its content type: %s", r.raw)
	}
}
