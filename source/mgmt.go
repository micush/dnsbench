package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Mgmt is the management layer the CLI (over the status socket) and the web
// GUI both call: configuration with history, the GUI certificate, the
// cluster, and software updates.  Keeping one implementation behind both
// front ends is what guarantees GUI/CLI parity.
type Mgmt struct {
	confPath string
	stateDir string

	capture captureState // this node's Capture page
	capjobs capJobs      // the cluster-wide capture
	capruns capRuns      // timed captures started on this node (by the cluster-wide one, from another node)

	versions *VersionStore
	certs    *CertManager
	upd      *Updater
	cl       *Cluster

	cfgMu     sync.Mutex // serialises writes of the config file
	webPolicy atomic.Pointer[WebConfig]

	// endSessions signs a user out of the web GUI sessions of this node (set by the web server)
	endSessions atomic.Pointer[func(user string)]

	reloadFn  func() error          // re-read the config file and apply it
	restartFn func()                // gracefully re-exec this process
	gwFn      func() []GwState      // which gateways this node is serving right now
	gwIPsFn   func() []string       // the addresses this node uses in the gateway protocol, one per family of each group
	pausedFn  func() bool           // whether this whole node is paused (Operate ▸ Node)
	anycastFn func() []AnycastState // the anycast addresses and whether each is announced now
	tshootFn  func() map[string]any // what only the status server knows, for the troubleshooting bundle (see tshoot.go)
	poolOf    func(gid int) *Pool   // the DNS pool serving a gateway on this node (the cache warm start, cachewarm.go)
	servMu    sync.Mutex
	servSince map[int]time.Time // when each gateway started being served continuously
	bgp       *BGPManager       // keeps FRR in line with the BGP settings
	webH      http.Handler      // the web API, for requests relayed from other cluster nodes
	cancelFn  context.CancelFunc
}

// defaultStateDir holds history, certificates, cluster identity and update
// files.  It is root-only.
const defaultStateDir = "/var/lib/ddgw"

// NewMgmt builds the management layer for the given initial config.
func NewMgmt(confPath, stateDir string, dc *DaemonConfig) (*Mgmt, error) {
	// absolute paths: the update build runs go with its own working directory
	// and the daemon may re-exec itself, so nothing may depend on our cwd.
	if abs, err := filepath.Abs(stateDir); err == nil {
		stateDir = abs
	}
	if abs, err := filepath.Abs(confPath); err == nil {
		confPath = abs
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("state directory %s: %w", stateDir, err)
	}
	m := &Mgmt{confPath: confPath, stateDir: stateDir}
	m.bgp = NewBGPManager(stateDir)
	w := dc.Web
	m.webPolicy.Store(&w)
	var err error
	if m.versions, err = NewVersionStore(filepath.Join(stateDir, "versions")); err != nil {
		return nil, err
	}
	m.certs = NewCertManager(stateDir, confPath, func() WebConfig { return *m.webPolicy.Load() })
	if m.upd, err = NewUpdater(stateDir); err != nil {
		return nil, err
	}
	if m.cl, err = NewCluster(m, dc.Cluster); err != nil {
		return nil, err
	}
	return m, nil
}

// ── configuration ────────────────────────────────────────────────────────────

// LiveConfig reads the config file (defaults if absent) and its canonical bytes.
func (m *Mgmt) LiveConfig() (*DaemonConfig, []byte, error) {
	dc, err := loadConfig(m.confPath)
	if err != nil {
		return nil, nil, err
	}
	b, _ := json.Marshal(dc)
	return dc, b, nil
}

// OnConfigLoaded is called every time a (valid) config has been loaded and
// applied, whoever changed it: it records a version, tracks the shared
// revision, and propagates hot-applicable settings.
func (m *Mgmt) OnConfigLoaded(dc *DaemonConfig) {
	w := dc.Web
	m.webPolicy.Store(&w)
	m.bgp.Apply(dc)
	m.certs.Refresh()
	if _, raw, err := m.LiveConfig(); err == nil {
		if _, err := os.Stat(m.confPath); err == nil { // don't version a config that doesn't exist yet
			if meta, err := m.versions.Record(raw, "file edit", "", false); err != nil {
				warnf("config history: %v", err)
			} else if meta != nil {
				infof("config history: recorded version %s (%s) — %s", meta.ID, meta.Actor, meta.Summary)
			}
		}
	}
	m.cl.OnConfig(dc)
}

// PutConfig validates dc and makes it the node's configuration.  On a replica,
// a change to the shared settings is first accepted by the primary.
func (m *Mgmt) PutConfig(dc *DaemonConfig, actor, note string) error {
	if err := dc.Validate(); err != nil {
		return err
	}
	m.cfgMu.Lock()
	defer m.cfgMu.Unlock()
	cur, _, err := m.LiveConfig()
	if err != nil {
		return err
	}
	if m.cl.Enabled() && m.cl.Snapshot().Role == RoleReplica && sharedOf(dc).hash() != sharedOf(cur).hash() {
		payload := adminSharedConfig{Shared: sharedOf(dc), Seeds: seedsOf(dc)}
		if _, err := m.cl.forwardAdmin("shared-config", actor, payload); err != nil {
			return fmt.Errorf("this node is a replica: the change to the shared settings must be accepted by the primary (%s), and that failed: %w",
				m.cl.Snapshot().PrimaryAddr, err)
		}
	}
	m.versions.Expect(actor, note)
	if err := dc.save(m.confPath); err != nil {
		return fmt.Errorf("could not save: %w", err)
	}
	if m.reloadFn != nil {
		if err := m.reloadFn(); err != nil {
			return fmt.Errorf("saved, but applying failed: %w", err)
		}
	}
	return nil
}

// applyClusterShared writes the primary's shared settings into the local file.
func (m *Mgmt) applyClusterShared(sh SharedConfig, seeds []GroupSeed, rev uint64, from string) {
	m.cfgMu.Lock()
	defer m.cfgMu.Unlock()
	cur, _, err := m.LiveConfig()
	if err != nil {
		warnf("cluster: cannot read local config: %v", err)
		return
	}
	hash := sh.hash()
	changed, err := mergeShared(cur, sh, seeds)
	if err != nil {
		warnf("cluster: shared settings from the primary were refused by local validation: %v", err)
		return
	}
	if !changed {
		m.cl.node.SetShared(rev, hash)
		return
	}
	m.versions.Expect("cluster sync", fmt.Sprintf("shared settings rev %d from %s", rev, from))
	if err := cur.save(m.confPath); err != nil {
		warnf("cluster: cannot save the synced config: %v", err)
		return
	}
	m.cl.node.SetShared(rev, hash)
	infof("cluster: applied shared settings rev %d from %s", rev, from)
	if m.reloadFn != nil {
		if err := m.reloadFn(); err != nil {
			warnf("cluster: applying synced config failed: %v", err)
		}
	}
}

// ConfigImport replaces the whole configuration with an uploaded document.
func (m *Mgmt) ConfigImport(raw []byte, actor, note string) (*DaemonConfig, error) {
	dc := newDaemonConfig()
	if err := json.Unmarshal(raw, dc); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	if err := m.PutConfig(dc, actor, note); err != nil {
		return nil, err
	}
	return dc, nil
}

// ── versions ─────────────────────────────────────────────────────────────────

func (m *Mgmt) VersionsList() []VersionMeta { return m.versions.List() }

func (m *Mgmt) VersionGet(id string) (VersionMeta, json.RawMessage, error) {
	if id == CurrentVersionID {
		_, raw, err := m.LiveConfig()
		if err != nil {
			return VersionMeta{}, nil, err
		}
		_, canon, err := canonicalConfig(raw)
		return VersionMeta{ID: CurrentVersionID, Summary: "live configuration", At: time.Now().UTC()}, canon, err
	}
	meta, cfg, err := m.versions.Get(id)
	return meta, cfg, err
}

func (m *Mgmt) VersionDiff(a, b string) (*VersionDiff, error) {
	_, raw, err := m.LiveConfig()
	if err != nil {
		return nil, err
	}
	if b == "" {
		b = CurrentVersionID
	}
	return m.versions.Diff(a, b, raw)
}

func (m *Mgmt) VersionSnapshot(note, by string) (*VersionMeta, error) {
	_, raw, err := m.LiveConfig()
	if err != nil {
		return nil, err
	}
	return m.versions.Record(raw, by, strings.TrimSpace(note), true)
}

// VersionRestore makes a recorded version the live configuration (itself
// recorded as a new version).  On a replica the shared part must be accepted
// by the primary, like any other edit.
func (m *Mgmt) VersionRestore(id, by string) (*DaemonConfig, error) {
	meta, cfg, err := m.versions.Get(id)
	if err != nil {
		return nil, err
	}
	dc := newDaemonConfig()
	if err := json.Unmarshal(cfg, dc); err != nil {
		return nil, err
	}
	// keep a way back to what is live right now
	if _, raw, err := m.LiveConfig(); err == nil {
		if _, err := os.Stat(m.confPath); err == nil {
			m.versions.Record(raw, by, "before restoring version "+id, false)
		}
	}
	if err := m.PutConfig(dc, by, "restored version "+meta.ID); err != nil {
		return nil, err
	}
	return dc, nil
}

// ── certificate ──────────────────────────────────────────────────────────────

// shareCert reports whether certificate changes go through the primary.
func (m *Mgmt) shareCert() bool {
	if !m.cl.Enabled() {
		return false
	}
	dc, _, err := m.LiveConfig()
	return err == nil && dc.Cluster.ShareCert
}

type adminCert struct {
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

func (m *Mgmt) TLSInstall(certPEM, keyPEM, by string) (CertInfo, []string, error) {
	if m.shareCert() && m.cl.Snapshot().Role == RoleReplica {
		// the primary validates and installs it; it comes back via sync
		raw, err := m.cl.forwardAdmin("cert-install", by, adminCert{certPEM, keyPEM})
		if err != nil {
			return CertInfo{}, nil, fmt.Errorf("this node is a replica: the primary must install the certificate, and that failed: %w", err)
		}
		var res struct {
			Warnings []string `json:"warnings"`
		}
		json.Unmarshal(raw, &res)
		m.cl.syncNow()
		return m.certs.Status(), res.Warnings, nil
	}
	return m.certs.Install([]byte(certPEM), []byte(keyPEM), by, certSourceInstalled)
}

func (m *Mgmt) TLSRevert(by string) (CertInfo, error) {
	if m.shareCert() && m.cl.Snapshot().Role == RoleReplica {
		if _, err := m.cl.forwardAdmin("cert-revert", by, nil); err != nil {
			return CertInfo{}, err
		}
		m.cl.syncNow()
		return m.certs.Status(), nil
	}
	return m.certs.Revert(by)
}

// ── updates ──────────────────────────────────────────────────────────────────

// UpdateUpload stages a release archive on this node.
func (m *Mgmt) UpdateUpload(body []byte, by string) (string, error) {
	if len(body) == 0 {
		return "", errors.New("empty upload")
	}
	if len(body) > maxUploadBytes {
		return "", errors.New("upload too large")
	}
	ver, err := m.upd.ExtractSource(body)
	if err != nil {
		return "", fmt.Errorf("rejected: %w", err)
	}
	m.upd.Record(UpdateEvent{Node: m.cl.selfAddrForEvents(), Kind: "uploaded", To: ver, By: by,
		Detail: fmt.Sprintf("source v%s staged (running v%s)", ver, version())})
	infof("update: source v%s staged by %s", ver, by)
	return ver, nil
}

// UpdateApplyLocal starts building the staged source and restarts into it.
// The build can take minutes, so it runs in the background: progress shows in
// UpdateStatus (busy/phase, history).  Preconditions are checked up front.
func (m *Mgmt) UpdateApplyLocal(by string, force bool) (string, error) {
	target := m.upd.SourceVersion()
	switch {
	case target == "":
		return "", errors.New("no source tree staged: upload a release archive first")
	case !versionGreater(target, version()):
		return "", fmt.Errorf("the staged source (v%s) is not newer than the running version (v%s)", target, version())
	case m.upd.Busy():
		return "", errors.New("an update is already in progress on this node")
	}
	if ok, why := m.updateSafeToApply(); !ok && !force {
		return "", fmt.Errorf("not safe to update this node now: %s (to update anyway: ddgw --update-apply --yes, or confirm in the GUI)", why)
	}
	go func() {
		if _, err := m.upd.Apply(context.Background(), m.cl.selfAddrForEvents(), by); err != nil {
			debugf("update: %v", err)
			return
		}
		m.scheduleRestart(force)
	}()
	return target, nil
}

// restartRecheck is how often a node that has installed an update re-asks
// whether it may go down.
var restartRecheck = 2 * time.Second

// scheduleRestart restarts into the installed version — but only once it is safe
// to go down.  The check made before the update began is not enough: the build
// takes minutes, and meanwhile another member may have gone down for its own
// update, or the one that was serving the gateway alongside this node may have
// failed.  force skips the wait (the operator confirmed it).
func (m *Mgmt) scheduleRestart(force bool) {
	if m.restartFn == nil {
		warnf("update: installed, but this process cannot restart itself — restart ddgw to run the new version")
		return
	}
	go func() {
		time.Sleep(1500 * time.Millisecond) // let the HTTP/socket reply go out
		waitUntilSafe(m.updateSafeToApply, force, restartRecheck, m.upd.SetWaiting)
		m.restartFn()
	}()
}

// waitUntilSafe blocks until safe reports true (or force), publishing the reason
// it is holding back through setWaiting and clearing it afterwards.
func waitUntilSafe(safe func() (bool, string), force bool, every time.Duration, setWaiting func(string)) {
	if force {
		return
	}
	defer setWaiting("")
	for {
		ok, why := safe()
		if ok {
			return
		}
		setWaiting("installed; waiting to restart: " + why)
		time.Sleep(every)
	}
}

type adminNodes struct {
	Nodes   []string `json:"nodes"`
	Enabled bool     `json:"enabled"`
}

func (m *Mgmt) UpdatePush(nodes []string, by string) error {
	if len(nodes) == 0 {
		return errors.New("no nodes given")
	}
	_, err := m.runOnPrimary("update-push", by, adminNodes{Nodes: nodes})
	return err
}

func (m *Mgmt) UpdateCancel(nodes []string, by string) error {
	_, err := m.runOnPrimary("update-cancel", by, adminNodes{Nodes: nodes})
	return err
}

func (m *Mgmt) UpdateAuto(on bool, by string) error {
	_, err := m.runOnPrimary("update-auto", by, adminNodes{Enabled: on})
	return err
}

// runOnPrimary executes an admin write on the primary: here if this node is
// the primary (or not clustered), otherwise forwarded.
func (m *Mgmt) runOnPrimary(op, by string, payload any) (json.RawMessage, error) {
	if !m.cl.Enabled() || m.cl.Snapshot().Role == RolePrimary {
		b, _ := json.Marshal(payload)
		return m.execAdminOp(op, by, b)
	}
	return m.cl.forwardAdmin(op, by, payload)
}

type adminSharedConfig struct {
	Shared SharedConfig `json:"shared"`
	Seeds  []GroupSeed  `json:"seeds"`
}

// execAdminOp runs a write on this node, which must be the primary.
func (m *Mgmt) execAdminOp(op, by string, payload json.RawMessage) (json.RawMessage, error) {
	ok := func(v any) (json.RawMessage, error) { b, _ := json.Marshal(v); return b, nil }
	switch op {
	case "shared-config":
		var p adminSharedConfig
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, err
		}
		if err := p.Shared.Validate(); err != nil {
			return nil, err
		}
		m.cfgMu.Lock()
		defer m.cfgMu.Unlock()
		cur, _, err := m.LiveConfig()
		if err != nil {
			return nil, err
		}
		if _, err := mergeShared(cur, p.Shared, p.Seeds); err != nil {
			return nil, err
		}
		m.versions.Expect(by, "shared settings changed on a replica")
		if err := cur.save(m.confPath); err != nil {
			return nil, err
		}
		if m.reloadFn != nil {
			if err := m.reloadFn(); err != nil {
				return nil, err
			}
		}
		return ok(map[string]any{"rev": m.cl.Snapshot().SharedRev})
	case "cert-install":
		var p adminCert
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, err
		}
		info, warns, err := m.certs.Install([]byte(p.CertPEM), []byte(p.KeyPEM), by, certSourceInstalled)
		if err != nil {
			return nil, err
		}
		return ok(map[string]any{"info": info, "warnings": warns})
	case "cert-revert":
		if _, err := m.certs.Revert(by); err != nil {
			return nil, err
		}
		return ok(map[string]any{})
	case "update-push", "update-cancel", "update-auto", "update-done":
		var p adminNodes
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, err
		}
		switch op {
		case "update-push":
			m.upd.Push(p.Nodes, by)
		case "update-cancel", "update-done":
			m.upd.Cancel(p.Nodes, by)
		case "update-auto":
			m.upd.SetAuto(p.Enabled, by, m.cl.selfAddrForEvents())
		}
		return ok(map[string]any{})
	}
	return nil, fmt.Errorf("unknown admin operation %q", op)
}

// UpdateView is the update status for the CLI/GUI.
type UpdateView struct {
	Running       string         `json:"running"`
	SourceVersion string         `json:"source_version"`
	Intent        UpdateIntent   `json:"intent"`
	Nodes         []UpdateNode   `json:"nodes"`
	History       []UpdateEvent  `json:"history"`
	Toolchain     string         `json:"toolchain"`
	Notice        string         `json:"notice,omitempty"`
	Busy          bool           `json:"busy"`
	Phase         string         `json:"phase,omitempty"`
	Self          string         `json:"self"`
	Clustered     bool           `json:"clustered"`
	Last          *UpdateEvent   `json:"last,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
	Waiting       string         `json:"waiting,omitempty"` // why this node is holding back its queued update
}

type UpdateNode struct {
	Addr      string `json:"addr"`
	Self      bool   `json:"self"`
	Reachable bool   `json:"reachable"`
	Running   string `json:"running"`
	Source    string `json:"source"`
	Queued    bool   `json:"queued"`
	Updating  bool   `json:"updating"`
	Failed    string `json:"failed,omitempty"`
	Behind    bool   `json:"behind"`
}

func (m *Mgmt) UpdateStatus() UpdateView {
	// History(0) is every event kept: the GUI pages through them.
	v := UpdateView{
		Running: version(), SourceVersion: m.upd.SourceVersion(), Intent: m.upd.Intent(),
		History: m.upd.History(0), Busy: m.upd.Busy(), Phase: m.upd.Phase(), Notice: m.upd.RolledBackNotice(),
		Self: m.cl.selfAddrForEvents(), Clustered: m.cl.Enabled(), Last: m.upd.LastEvent(), Waiting: m.upd.Waiting(),
	}
	if g, err := m.upd.findGo(); err == nil {
		v.Toolchain = g
	}
	nodes := m.cl.updateNodes()
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Addr < nodes[j].Addr })
	v.Nodes = nodes
	return v
}

// localGwIPs is this node's addresses in the gateway protocol (nil before the supervisor exists).
func (m *Mgmt) localGwIPs() []string {
	if m.gwIPsFn == nil {
		return nil
	}
	return m.gwIPsFn()
}
