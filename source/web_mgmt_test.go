package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

var mgmtGETs = []string{
	"/api/versions", "/api/versions/get?id=1", "/api/versions/diff?a=1&b=2", "/api/versions/download?id=current",
	"/api/tls", "/api/cluster", "/api/update", "/api/update/history", "/api/log", "/api/qstats?from=1h", "/api/whois?domain=example.com", "/api/host?from=1h", "/api/dnsupdates", "/api/log?level=warn&q=x&since=1h&n=10",
}

var mgmtPOSTs = []string{
	"/api/versions/snapshot", "/api/versions/restore", "/api/versions/upload",
	"/api/tls/install", "/api/tls/csr", "/api/tls/csr/cancel", "/api/tls/revert", "/api/tls/regenerate",
	"/api/cluster/token", "/api/cluster/join", "/api/cluster/promote", "/api/cluster/peers/remove",
	"/api/cluster/peers/unremove", "/api/cluster/leave", "/api/cluster/sync",
	"/api/update/upload", "/api/update/fetch", "/api/update/apply", "/api/update/push", "/api/update/cancel", "/api/update/auto",
	"/api/nodepause",
}

func TestMgmtRoutesRequireSessionAndCSRF(t *testing.T) {
	e := newWebEnv(t)
	cookie = ""
	for _, p := range mgmtGETs {
		if r := e.do("GET", p, nil); r.code != 401 {
			t.Errorf("GET %s without a session = %d", p, r.code)
		}
	}
	for _, p := range mgmtPOSTs {
		if r := e.do("POST", p, map[string]any{}); r.code != 401 {
			t.Errorf("POST %s without a session = %d", p, r.code)
		}
	}
	e.login("alice", "pw")
	for _, p := range mgmtPOSTs {
		if r := e.do("POST", p, map[string]any{}, withAuth(e, false)); r.code != 403 {
			t.Errorf("POST %s without CSRF token = %d", p, r.code)
		}
		if r := e.do("POST", p, map[string]any{}, withAuth(e, true), func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }); r.code != 403 {
			t.Errorf("POST %s cross-origin = %d", p, r.code)
		}
	}
	// only the archive upload may carry a non-JSON body
	if r := e.do("POST", "/api/tls/revert", map[string]any{}, withAuth(e, true), func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }); r.code != 415 {
		t.Errorf("non-JSON body accepted: %d", r.code)
	}
	if r := e.rawPost("/api/update/upload", []byte("junk"), true); r.code != 422 {
		t.Errorf("raw upload with CSRF must reach the handler: %d %s", r.code, r.raw)
	}
	if r := e.rawPost("/api/update/upload", []byte("junk"), false); r.code != 403 {
		t.Errorf("raw upload without CSRF = %d", r.code)
	}
}

func (e *webEnv) rawPost(path string, body []byte, csrf bool) resp {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.ts.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	withAuth(e, csrf)(req)
	res, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return resp{res.StatusCode, m, raw, res.Header}
}

func putCfg(e *webEnv, mut func(*DaemonConfig)) resp {
	dc, _, _ := e.mg.LiveConfig()
	if mut != nil {
		mut(dc)
	}
	b, _ := json.Marshal(dc)
	return e.do("PUT", "/api/config", map[string]any{"config": json.RawMessage(b), "note": "from test"}, withAuth(e, true))
}

func TestWebVersionsFlow(t *testing.T) {
	e := newWebEnv(t)
	e.login("alice", "pw")
	if r := putCfg(e, func(d *DaemonConfig) { d.Groups = []GroupConfig{defaultGroup()} }); r.code != 200 {
		t.Fatalf("initial save: %d %s", r.code, r.raw)
	}
	if r := putCfg(e, func(d *DaemonConfig) { d.Groups[0].Priority = 77 }); r.code != 200 {
		t.Fatalf("second save: %d %s", r.code, r.raw)
	}
	r := e.do("GET", "/api/versions", nil, withAuth(e, false))
	list, _ := r.body["data"].([]any)
	if r.code != 200 || len(list) != 2 {
		t.Fatalf("versions: %d %s", r.code, r.raw)
	}
	newest := list[0].(map[string]any)
	if newest["actor"] != "alice" || newest["note"] != "from test" || !strings.Contains(newest["summary"].(string), "priority 100 → 77") {
		t.Fatalf("newest: %v", newest)
	}
	oldest := list[1].(map[string]any)["id"].(string)

	d := e.do("GET", "/api/versions/diff?a="+oldest+"&b=current", nil, withAuth(e, false))
	data := d.body["data"].(map[string]any)
	if d.code != 200 || data["same"] != false || len(data["lines"].([]any)) == 0 {
		t.Fatalf("diff: %d %s", d.code, d.raw)
	}
	if r := e.do("GET", "/api/versions/get?id=nonexistent", nil, withAuth(e, false)); r.code != 404 {
		t.Fatalf("missing version: %d", r.code)
	}
	if r := e.do("GET", "/api/versions/get?id=../../etc/passwd", nil, withAuth(e, false)); r.code != 404 {
		t.Fatalf("path traversal id: %d", r.code)
	}

	// download is a plain config file; uploading it again is an import
	dl := e.do("GET", "/api/versions/download?id="+oldest, nil, withAuth(e, false))
	if dl.code != 200 || !strings.Contains(dl.hdr.Get("Content-Disposition"), "attachment") || !json.Valid(dl.raw) {
		t.Fatalf("download: %d %v", dl.code, dl.hdr)
	}

	// snapshot, restore
	if r := e.do("POST", "/api/versions/snapshot", map[string]any{"note": "keep"}, withAuth(e, true)); r.code != 200 {
		t.Fatalf("snapshot: %d %s", r.code, r.raw)
	}
	if r := e.do("POST", "/api/versions/restore", map[string]any{"id": oldest}, withAuth(e, true)); r.code != 200 {
		t.Fatalf("restore: %d %s", r.code, r.raw)
	}
	if dc, _, _ := e.mg.LiveConfig(); dc.Groups[0].Priority != 100 {
		t.Fatalf("restore did not apply: priority %d", dc.Groups[0].Priority)
	}
	if r := e.do("POST", "/api/versions/restore", map[string]any{"id": "42"}, withAuth(e, true)); r.code != 404 {
		t.Fatalf("restore of unknown id: %d", r.code)
	}
	up := e.do("POST", "/api/versions/upload", map[string]any{"config": json.RawMessage(dl.raw), "note": "re-upload"}, withAuth(e, true))
	if up.code != 200 {
		t.Fatalf("upload: %d %s", up.code, up.raw)
	}
	bad := e.do("POST", "/api/versions/upload", map[string]any{"config": map[string]any{"groups": []any{map[string]any{"group_id": 999}}}}, withAuth(e, true))
	if bad.code != 422 {
		t.Fatalf("an invalid imported config must be refused: %d %s", bad.code, bad.raw)
	}
	if r := e.do("POST", "/api/versions/upload", map[string]any{"config": map[string]any{"groups": []any{}}}, withAuth(e, true)); r.code != 200 {
		t.Fatalf("an explicit empty groups list means no gateways and is valid: %d %s", r.code, r.raw)
	}
}

func TestWebTLSFlow(t *testing.T) {
	e := newWebEnv(t)
	e.login("alice", "pw")
	r := e.do("GET", "/api/tls", nil, withAuth(e, false))
	if d := r.body["data"].(map[string]any); r.code != 200 || d["source"] != certSourceSelfSigned {
		t.Fatalf("status: %d %s", r.code, r.raw)
	}
	ca := newTestCA(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	chain := string(ca.issue(t, &k.PublicKey, time.Now().Add(72*time.Hour), "gw.test"))
	if r := e.do("POST", "/api/tls/install", map[string]any{"cert_pem": chain, "key_pem": string(ecKeyPEM(other))}, withAuth(e, true)); r.code != 422 ||
		!strings.Contains(r.body["error"].(string), "does not match") {
		t.Fatalf("mismatched key: %d %s", r.code, r.raw)
	}
	r = e.do("POST", "/api/tls/install", map[string]any{"cert_pem": chain, "key_pem": string(ecKeyPEM(k))}, withAuth(e, true))
	if r.code != 200 || r.body["data"].(map[string]any)["info"].(map[string]any)["source"] != certSourceInstalled {
		t.Fatalf("install: %d %s", r.code, r.raw)
	}
	if strings.Contains(string(r.raw), "PRIVATE KEY") {
		t.Fatal("the API must never echo a private key")
	}
	if r := e.do("POST", "/api/tls/csr", map[string]any{"cn": "x.test", "dns": []string{"x.test"}}, withAuth(e, true)); r.code != 200 ||
		!strings.Contains(r.body["data"].(map[string]any)["csr"].(string), "CERTIFICATE REQUEST") {
		t.Fatalf("csr: %d %s", r.code, r.raw)
	}
	if st := e.do("GET", "/api/tls", nil, withAuth(e, false)); strings.Contains(string(st.raw), "PRIVATE KEY") ||
		st.body["data"].(map[string]any)["pending_csr"] == "" {
		t.Fatalf("status must show the CSR and no key: %s", st.raw)
	}
	if r := e.do("POST", "/api/tls/csr/cancel", map[string]any{}, withAuth(e, true)); r.code != 200 {
		t.Fatalf("csr cancel: %d", r.code)
	}
	if r := e.do("POST", "/api/tls/revert", map[string]any{}, withAuth(e, true)); r.code != 200 ||
		r.body["data"].(map[string]any)["source"] != certSourceSelfSigned {
		t.Fatalf("revert: %d %s", r.code, r.raw)
	}
	if r := e.do("POST", "/api/tls/regenerate", map[string]any{}, withAuth(e, true)); r.code != 200 {
		t.Fatalf("regenerate: %d %s", r.code, r.raw)
	}
}

func TestWebClusterAndUpdateBasics(t *testing.T) {
	e := newWebEnv(t)
	e.login("alice", "pw")
	r := e.do("GET", "/api/cluster", nil, withAuth(e, false))
	if d := r.body["data"].(map[string]any); r.code != 200 || d["enabled"] != false {
		t.Fatalf("cluster status: %d %s", r.code, r.raw)
	}
	if r := e.do("POST", "/api/cluster/token", map[string]any{}, withAuth(e, true)); r.code != 422 {
		t.Fatalf("token while disabled: %d %s", r.code, r.raw)
	}
	if r := e.do("POST", "/api/cluster/join", map[string]any{"code": "garbage"}, withAuth(e, true)); r.code != 422 {
		t.Fatalf("join: %d", r.code)
	}

	// updates: upload validation, status, "not newer" refusal
	if r := e.rawPost("/api/update/upload", []byte("not an archive"), true); r.code != 422 {
		t.Fatalf("bad archive: %d %s", r.code, r.raw)
	}
	old := fakeSourceTarball(t, "1")
	if r := e.rawPost("/api/update/upload", old, true); r.code != 200 {
		t.Fatalf("good archive: %d %s", r.code, r.raw)
	}
	st := e.do("GET", "/api/update", nil, withAuth(e, false))
	if d := st.body["data"].(map[string]any); d["source_version"] != "1" || d["running"] != version() {
		t.Fatalf("update status: %s", st.raw)
	}
	if r := e.do("POST", "/api/update/apply", map[string]any{}, withAuth(e, true)); r.code != 422 ||
		!strings.Contains(r.body["error"].(string), "not newer") {
		t.Fatalf("apply of an older tree: %d %s", r.code, r.raw)
	}
	// fresh state: auto-update defaults to on; switching it off and on again is recorded
	for _, on := range []bool{false, true} {
		if r := e.do("POST", "/api/update/auto", map[string]any{"enabled": on}, withAuth(e, true)); r.code != 200 {
			t.Fatalf("auto %v: %d %s", on, r.code, r.raw)
		}
	}
	if r := e.do("POST", "/api/update/push", map[string]any{"nodes": []string{}}, withAuth(e, true)); r.code != 422 {
		t.Fatalf("push with no nodes: %d", r.code)
	}
	if h := e.do("GET", "/api/update/history", nil, withAuth(e, false)); h.code != 200 || len(h.body["data"].([]any)) < 2 {
		t.Fatalf("history: %s", h.raw)
	}
}
