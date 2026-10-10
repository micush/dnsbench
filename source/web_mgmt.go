package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Web routes for configuration history, the certificate, the cluster and
// updates.  Each one is a thin wrapper over Mgmt.Op — the same dispatcher the
// CLI uses — so the two front ends cannot drift apart.

func (w *WebServer) mgmtRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/vmactest", w.authed(w.op("vmac.test", nil)))
	// configuration history
	mux.HandleFunc("GET /api/versions", w.authed(w.op("versions.list", nil)))
	mux.HandleFunc("GET /api/versions/get", w.authed(w.op("versions.get", []string{"id"})))
	mux.HandleFunc("GET /api/versions/diff", w.authed(w.op("versions.diff", []string{"a", "b"})))
	mux.HandleFunc("POST /api/versions/snapshot", w.authed(w.op("versions.snapshot", nil)))
	mux.HandleFunc("POST /api/versions/restore", w.authed(w.op("versions.restore", nil)))
	mux.HandleFunc("GET /api/versions/download", w.authed(w.handleVersionDownload))
	mux.HandleFunc("POST /api/versions/upload", w.authed(w.handleVersionUpload))
	// certificate
	mux.HandleFunc("GET /api/tls", w.authed(w.op("tls.status", nil)))
	mux.HandleFunc("POST /api/tls/install", w.authed(w.op("tls.install", nil)))
	mux.HandleFunc("POST /api/tls/csr", w.authed(w.op("tls.csr", nil)))
	mux.HandleFunc("POST /api/tls/csr/cancel", w.authed(w.handleCSRCancel))
	mux.HandleFunc("POST /api/tls/revert", w.authed(w.op("tls.revert", nil)))
	mux.HandleFunc("POST /api/tls/regenerate", w.authed(w.op("tls.regenerate", nil)))
	// cluster
	mux.HandleFunc("GET /api/cluster", w.authed(w.op("cluster.status", nil)))
	mux.HandleFunc("POST /api/cluster/token", w.authed(w.op("cluster.token", nil)))
	mux.HandleFunc("POST /api/cluster/join", w.authed(w.op("cluster.join", nil)))
	mux.HandleFunc("POST /api/cluster/promote", w.authed(w.op("cluster.promote", nil)))
	mux.HandleFunc("POST /api/cluster/peers/remove", w.authed(w.op("cluster.remove", nil)))
	mux.HandleFunc("POST /api/cluster/peers/unremove", w.authed(w.op("cluster.unremove", nil)))
	mux.HandleFunc("POST /api/cluster/leave", w.authed(w.op("cluster.leave", nil)))
	mux.HandleFunc("POST /api/cluster/sync", w.authed(w.op("cluster.sync", nil)))
	w.registerCapture(mux)
	// updates
	mux.HandleFunc("GET /api/bgp", w.authed(w.op("bgp.status", nil)))
	mux.HandleFunc("PUT /api/bgp", w.authed(w.op("bgp.set", nil)))
	mux.HandleFunc("POST /api/bgp/operate", w.authed(w.op("bgp.operate", nil)))
	mux.HandleFunc("GET /api/log", w.authed(w.op("log.view", []string{"level", "q", "since", "n"})))
	mux.HandleFunc("GET /api/dnsupdates", w.authed(w.op("dnsupdates.get", nil)))
	mux.HandleFunc("GET /api/host", w.authed(w.op("host.get", []string{"from", "to"})))
	mux.HandleFunc("GET /api/serverstats", w.authed(w.op("dns.serverstats", []string{"addr", "from", "to"})))
	mux.HandleFunc("GET /api/dnslookup", w.authed(w.op("dns.lookup", []string{"lookup"})))
	mux.HandleFunc("GET /api/whois", w.authed(w.op("whois.get", []string{"domain"})))
	mux.HandleFunc("GET /api/qstats", w.authed(w.op("qstats.get", []string{"from", "to", "rcode", "client", "domain"})))
	mux.HandleFunc("POST /api/qstats/clear", w.authed(w.op("qstats.clear", nil)))
	mux.HandleFunc("POST /api/clusterstats/clear", w.authed(w.op("qstats.clear.cluster", nil)))
	mux.HandleFunc("POST /api/scan", w.authed(w.op("scan.start", nil)))
	mux.HandleFunc("GET /api/scan", w.authed(w.op("scan.get", []string{"client"})))
	// the whole cluster's numbers added together (not relayable: the node asked does the asking of the others)
	mux.HandleFunc("GET /api/clusterstats", w.authed(w.op("qstats.cluster", []string{"from", "to", "rcode", "client", "domain"})))
	mux.HandleFunc("GET /api/clusterhost", w.authed(w.op("host.cluster", []string{"from", "to"})))
	mux.HandleFunc("GET /api/nodepause", w.authed(w.op("node.status", nil)))
	mux.HandleFunc("POST /api/nodepause", w.authed(w.op("node.pause", nil)))
	mux.HandleFunc("GET /api/users", w.authed(w.op("users.list", nil)))
	mux.HandleFunc("POST /api/users/add", w.authed(w.op("users.add", nil)))
	mux.HandleFunc("POST /api/users/password", w.authed(w.op("users.password", nil)))
	mux.HandleFunc("POST /api/users/expiry", w.authed(w.op("users.expiry", nil)))
	mux.HandleFunc("POST /api/users/delete", w.authed(w.op("users.delete", nil)))
	mux.HandleFunc("POST /api/users/grant", w.authed(w.op("users.grant", nil)))
	mux.HandleFunc("POST /api/users/revoke", w.authed(w.op("users.revoke", nil)))
	mux.HandleFunc("GET /api/power", w.authed(w.op("power.status", nil)))
	mux.HandleFunc("POST /api/power", w.authed(w.op("power.do", nil)))
	mux.HandleFunc("GET /api/update", w.authed(w.op("update.status", nil)))
	mux.HandleFunc("GET /api/update/history", w.authed(w.op("update.history", nil)))
	mux.HandleFunc("POST /api/update/upload", w.authedCT(w.handleUpdateUpload, false))
	mux.HandleFunc("POST /api/update/apply", w.authed(w.op("update.apply", nil)))
	mux.HandleFunc("POST /api/update/push", w.authed(w.op("update.push", nil)))
	mux.HandleFunc("POST /api/update/cancel", w.authed(w.op("update.cancel", nil)))
	mux.HandleFunc("POST /api/update/auto", w.authed(w.op("update.auto", nil)))
}

// op adapts a Mgmt.Op command to an HTTP handler.  GET handlers take their
// arguments from the listed query parameters, POST handlers from the JSON body.
func (w *WebServer) op(cmd string, query []string) func(http.ResponseWriter, *http.Request, *session) {
	return func(rw http.ResponseWriter, r *http.Request, s *session) {
		var args json.RawMessage
		switch {
		case r.Method == http.MethodGet:
			m := map[string]string{}
			for _, q := range query {
				m[q] = r.URL.Query().Get(q)
			}
			args, _ = json.Marshal(m)
		case r.ContentLength != 0:
			body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, maxBody))
			if err != nil {
				jsonError(rw, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			args = body
		}
		if cmd == "versions.restore" || cmd == "cluster.promote" || cmd == "cluster.leave" || cmd == "cluster.join" || cmd == "update.apply" || cmd == "power.do" || cmd == "node.pause" || cmd == "qstats.clear" || cmd == "qstats.clear.cluster" || strings.HasPrefix(cmd, "users.") && cmd != "users.list" {
			warnf("web: %q ran %s (from %s)", s.user, cmd, clientIP(r))
		}
		data, err := w.mg.Op(cmd, args, s.user)
		if err != nil {
			code := http.StatusUnprocessableEntity
			if errors.Is(err, ErrNoVersion) {
				code = http.StatusNotFound
			}
			jsonError(rw, code, err.Error())
			return
		}
		writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "data": data})
	}
}

func (w *WebServer) handleCSRCancel(rw http.ResponseWriter, r *http.Request, s *session) {
	w.mg.certs.CancelCSR()
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

// handleVersionDownload serves one version (or the live config) as a file
// that can be uploaded again.
func (w *WebServer) handleVersionDownload(rw http.ResponseWriter, r *http.Request, s *session) {
	id := r.URL.Query().Get("id")
	meta, cfg, err := w.mg.VersionGet(id)
	if err != nil {
		code := http.StatusUnprocessableEntity
		if errors.Is(err, ErrNoVersion) {
			code = http.StatusNotFound
		}
		jsonError(rw, code, err.Error())
		return
	}
	name := "ddgw-config-" + meta.ID + ".json"
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	rw.Write(append([]byte(cfg), '\n'))
}

func (w *WebServer) handleVersionUpload(rw http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Config json.RawMessage `json:"config"`
		Note   string          `json:"note"`
	}
	if !readJSON(rw, r, maxBody, &req) {
		return
	}
	if len(req.Config) == 0 {
		jsonError(rw, http.StatusBadRequest, "missing \"config\"")
		return
	}
	warnf("web: %q imported a configuration (from %s)", s.user, clientIP(r))
	dc, err := w.mg.ConfigImport(req.Config, s.user, req.Note)
	if err != nil {
		jsonError(rw, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "config": dc})
}

func (w *WebServer) handleUpdateUpload(rw http.ResponseWriter, r *http.Request, s *session) {
	body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, maxUploadBytes))
	if err != nil {
		jsonError(rw, http.StatusRequestEntityTooLarge, "upload too large (limit "+strconv.Itoa(maxUploadBytes>>20)+" MB)")
		return
	}
	warnf("web: %q uploaded a release archive (%d bytes, from %s)", s.user, len(body), clientIP(r))
	ver, err := w.mg.UpdateUpload(body, s.user)
	if err != nil {
		jsonError(rw, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"version": ver}})
}
