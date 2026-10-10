package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── one node ─────────────────────────────────────────────────────────────────

// CaptureInterfaces lists what a capture can be started on, and the interfaces the gateways use (the usual choice).
type CaptureInterfaces struct {
	Interfaces []capIface `json:"interfaces"`
	Gateway    []string   `json:"gateway"` // the interfaces of the configured gateways, in order
}

func (m *Mgmt) captureInterfaces() CaptureInterfaces {
	out := CaptureInterfaces{Interfaces: captureInterfaces(), Gateway: []string{}}
	if dc, _, err := m.LiveConfig(); err == nil && dc != nil {
		seen := map[string]bool{}
		for _, g := range dc.Groups {
			if g.Interface != "" && !seen[g.Interface] {
				seen[g.Interface] = true
				out.Gateway = append(out.Gateway, g.Interface)
			}
		}
	}
	return out
}

// CaptureStart starts the capture of this node's Capture page (it replaces one that is running).
func (m *Mgmt) CaptureStart(iface, filter, actor string) error {
	if err := m.capture.startOn(iface, filter); err != nil {
		return err
	}
	warnf("capture: %s started a packet capture on %s (filter %q)", actor, iface, filter)
	return nil
}

func (m *Mgmt) CaptureStop()  { m.capture.stop() }
func (m *Mgmt) CaptureClear() { m.capture.clear() }

// CaptureRunResult is a capture that ran for a fixed time into a private buffer of its own.
type CaptureRunResult struct {
	Iface     string `json:"iface"`
	Filter    string `json:"filter"`
	Seconds   int    `json:"seconds"`
	Kept      int    `json:"kept"`      // packets in the file
	Seen      int64  `json:"seen"`      // packets the socket delivered
	Pcap      []byte `json:"pcap"`      // the .pcap
	Truncated bool   `json:"truncated"` // the newest packets that fit are in it, not all that were kept
}

// CaptureRun captures for the given seconds on iface (nothing else on the node is disturbed) and returns the .pcap.
func (m *Mgmt) CaptureRun(ctx context.Context, iface, filter string, seconds int, actor string) (*CaptureRunResult, error) {
	if seconds < 1 || seconds > capRunMaxSeconds {
		return nil, fmt.Errorf("the capture time must be 1 to %d seconds", capRunMaxSeconds)
	}
	warnf("capture: %s started a %d s packet capture on %s (filter %q)", actor, seconds, iface, filter)
	pcap, kept, seen, err := captureRun(ctx, iface, filter, time.Duration(seconds)*time.Second, capRelayBytes)
	if err != nil {
		return nil, err
	}
	return &CaptureRunResult{Iface: iface, Filter: filter, Seconds: seconds, Kept: kept, Seen: seen, Pcap: pcap, Truncated: kept >= capRunMaxPackets}, nil
}

// ── a timed capture that is started and then asked for ───────────────────────

// CaptureRunStatus is a timed capture on this node: running, or done with its .pcap.
type CaptureRunStatus struct {
	ID      string  `json:"id"`
	Iface   string  `json:"iface"`
	Filter  string  `json:"filter"`
	Seconds int     `json:"seconds"`
	Running bool    `json:"running"`
	Done    bool    `json:"done"`
	Left    float64 `json:"left"`
	Error   string  `json:"error,omitempty"`
	Kept    int     `json:"kept"`
	Seen    int64   `json:"seen"`
	Pcap    []byte  `json:"pcap,omitempty"`
}

type capRun struct {
	mu       sync.Mutex
	id       string
	iface    string
	filter   string
	seconds  int
	started  time.Time
	finished time.Time
	done     bool
	err      string
	kept     int
	seen     int64
	pcap     []byte
}

type capRuns struct {
	mu sync.Mutex
	m  map[string]*capRun
}

const capRunsKept = 4               // timed captures a node holds at once (running or waiting to be fetched)
const capRunKeep = 10 * time.Minute // how long a finished one waits to be fetched

func (r *capRun) status(withPcap bool) *CaptureRunStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := &CaptureRunStatus{ID: r.id, Iface: r.iface, Filter: r.filter, Seconds: r.seconds, Done: r.done, Running: !r.done, Error: r.err, Kept: r.kept, Seen: r.seen}
	if !r.done {
		st.Left = max(0, float64(r.seconds)-time.Since(r.started).Seconds())
	}
	if withPcap {
		st.Pcap = r.pcap
	}
	return st
}

// CaptureJobStart starts a timed capture under the caller's id and returns at once.  The same id again is the same
// capture (a request the peer channel sent twice must not start two).
func (m *Mgmt) CaptureJobStart(id, iface, filter string, seconds int, actor string) (*CaptureRunStatus, error) {
	if id == "" || len(id) > 40 || strings.ContainsAny(id, " \r\n/?&") {
		return nil, errors.New("a capture needs an id (up to 40 characters)")
	}
	if seconds < 1 || seconds > capRunMaxSeconds {
		return nil, fmt.Errorf("the capture time must be 1 to %d seconds", capRunMaxSeconds)
	}
	m.capruns.mu.Lock()
	if m.capruns.m == nil {
		m.capruns.m = map[string]*capRun{}
	}
	for k, r := range m.capruns.m { // let go of what was never fetched
		r.mu.Lock()
		old := r.done && time.Since(r.finished) > capRunKeep
		r.mu.Unlock()
		if old {
			delete(m.capruns.m, k)
		}
	}
	if r, ok := m.capruns.m[id]; ok {
		m.capruns.mu.Unlock()
		return r.status(false), nil
	}
	if len(m.capruns.m) >= capRunsKept {
		m.capruns.mu.Unlock()
		return nil, errors.New("this node already holds several timed captures: try again in a moment")
	}
	cs, err := beginPrivate(iface, filter, capRelayBytes) // refused here (no such interface, a bad filter), not later
	if err != nil {
		m.capruns.mu.Unlock()
		return nil, err
	}
	r := &capRun{id: id, iface: iface, filter: filter, seconds: seconds, started: time.Now()}
	m.capruns.m[id] = r
	m.capruns.mu.Unlock()
	warnf("capture: %s started a %d s packet capture on %s (filter %q)", actor, seconds, iface, filter)
	go func() {
		time.Sleep(time.Duration(seconds) * time.Second)
		pcap, kept, seen := finishPrivate(cs, capRelayBytes)
		r.mu.Lock()
		r.pcap, r.kept, r.seen, r.done, r.finished = pcap, kept, seen, true, time.Now()
		r.mu.Unlock()
	}()
	return r.status(false), nil
}

// CaptureJobGet is the state of a timed capture, with the .pcap once it is done.
func (m *Mgmt) CaptureJobGet(id string) (*CaptureRunStatus, error) {
	m.capruns.mu.Lock()
	r := m.capruns.m[id]
	m.capruns.mu.Unlock()
	if r == nil {
		return nil, errors.New("no such capture (it may have been fetched and let go)")
	}
	return r.status(true), nil
}

// ── every node ───────────────────────────────────────────────────────────────

// CaptureNode is one node's part in a cluster-wide capture.
type CaptureNode struct {
	Name   string `json:"name"`
	Addr   string `json:"addr,omitempty"`
	Self   bool   `json:"self,omitempty"`
	Status string `json:"status"` // running, done, error
	Error  string `json:"error,omitempty"`
	Iface  string `json:"iface"`
	Kept   int    `json:"kept"`
	Seen   int64  `json:"seen"`
	Bytes  int    `json:"bytes"`
	pcap   []byte
}

// CaptureJob is a capture that runs on every node at once for the same time and is bundled into one .tgz.
type CaptureJob struct {
	ID      int64          `json:"id"`
	Started time.Time      `json:"started"`
	Seconds int            `json:"seconds"`
	Iface   string         `json:"iface"`
	Filter  string         `json:"filter"`
	Done    bool           `json:"done"`
	Ready   bool           `json:"ready"` // there is a bundle to download
	Error   string         `json:"error,omitempty"`
	Elapsed float64        `json:"elapsed"`
	Nodes   []*CaptureNode `json:"nodes"`
	tgz     []byte
	mu      sync.Mutex
}

type capJobs struct {
	mu  sync.Mutex
	seq int64
	cur *CaptureJob
}

// CaptureClusterStart starts a capture on every node and returns at once; the job is polled with CaptureClusterStatus.
func (m *Mgmt) CaptureClusterStart(iface, filter string, seconds int, actor string) (*CaptureJob, error) {
	if seconds < 1 || seconds > capRunMaxSeconds {
		return nil, fmt.Errorf("the capture time must be 1 to %d seconds", capRunMaxSeconds)
	}
	if iface == "" {
		return nil, errors.New("choose the interface to capture on")
	}
	if _, err := compileCapFilter(filter); err != nil {
		return nil, err
	}
	if len(filter) > capFilterMax {
		return nil, fmt.Errorf("the filter is too long (at most %d characters)", capFilterMax)
	}
	m.capjobs.mu.Lock()
	if cur := m.capjobs.cur; cur != nil {
		cur.mu.Lock()
		running := !cur.Done
		cur.mu.Unlock()
		if running {
			m.capjobs.mu.Unlock()
			return nil, errors.New("a cluster capture is already running")
		}
	}
	m.capjobs.seq++
	job := &CaptureJob{ID: m.capjobs.seq, Started: time.Now(), Seconds: seconds, Iface: iface, Filter: filter}
	m.capjobs.cur = job
	m.capjobs.mu.Unlock()
	warnf("capture: %s started a %d s packet capture on every node, on %s (filter %q)", actor, seconds, iface, filter)
	go job.run(m, actor)
	return job, nil
}

var capNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// run starts the capture on every node (this one directly, the others through the cluster relay), asks each node for
// its result until the time is over, and bundles them.  Every request is a quick one.
func (j *CaptureJob) run(m *Mgmt, actor string) {
	token := randHex(8)
	targets := clusterTargets(m)
	nodes := make([]*CaptureNode, len(targets))
	for i, t := range targets {
		nodes[i] = &CaptureNode{Name: t.Name, Addr: t.Addr, Self: t.Self, Iface: j.Iface, Status: "running"}
		if t.Err != "" {
			nodes[i].Status, nodes[i].Error = "error", t.Err
		}
	}
	j.mu.Lock()
	j.Nodes = nodes
	j.mu.Unlock()
	var wg sync.WaitGroup
	for i, t := range targets {
		if nodes[i].Status == "error" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := j.captureOn(m, t, token, actor)
			j.mu.Lock()
			defer j.mu.Unlock()
			n := nodes[i]
			if err != nil {
				n.Status, n.Error = "error", capFriendlyError(err.Error())
				return
			}
			n.Status, n.Kept, n.Seen, n.Bytes, n.pcap = "done", res.Kept, res.Seen, len(res.Pcap), res.Pcap
		}()
	}
	wg.Wait()
	j.bundle()
}

// captureOn runs the capture on one node: start it, then ask for it until it is done.
func (j *CaptureJob) captureOn(m *Mgmt, t clusterTarget, token, actor string) (*CaptureRunStatus, error) {
	ask := func(method, path string, body any) (*CaptureRunStatus, error) {
		if t.Self {
			if method == "POST" {
				return m.CaptureJobStart(token, j.Iface, j.Filter, j.Seconds, actor)
			}
			return m.CaptureJobGet(token)
		}
		req := proxyReq{User: actor, Method: method, Path: path}
		if body != nil {
			req.Body, _ = json.Marshal(body)
			req.CT = "application/json"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		resp, err := m.cl.Relay(ctx, t.Addr, req)
		if err != nil {
			return nil, err
		}
		var env struct {
			OK    bool              `json:"ok"`
			Data  *CaptureRunStatus `json:"data"`
			Error string            `json:"error"`
		}
		if json.Unmarshal(resp.Body, &env) != nil || resp.Status != 200 || !env.OK || env.Data == nil {
			return nil, errors.New(firstNonEmpty(env.Error, fmt.Sprintf("answered %d", resp.Status)))
		}
		return env.Data, nil
	}
	st, err := ask("POST", "/api/capture/job", map[string]any{"id": token, "iface": j.Iface, "filter": j.Filter, "seconds": j.Seconds})
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(time.Duration(j.Seconds)*time.Second + 40*time.Second)
	bad := 0
	for !st.Done {
		time.Sleep(time.Second)
		if time.Now().After(deadline) {
			return nil, errors.New("it did not finish in time")
		}
		next, err := ask("GET", "/api/capture/job?id="+token, nil)
		if err != nil {
			if bad++; bad >= 6 { // a slow answer or two is not the end
				return nil, err
			}
			continue
		}
		bad, st = 0, next
	}
	if st.Error != "" {
		return nil, errors.New(st.Error)
	}
	return st, nil
}

// capFriendlyError says what an error from a peer means when the peer is on a version without capture.
func capFriendlyError(e string) string {
	if strings.Contains(e, "always acts on the node you are logged in to") || strings.Contains(e, "404") {
		return "this node's version has no packet capture: update it"
	}
	return e
}

func (m *Mgmt) clusterSelfAddr() string {
	if m.cl == nil || !m.cl.Enabled() {
		return ""
	}
	for _, p := range m.cl.View().Peers {
		if p.Self {
			return p.Addr
		}
	}
	return ""
}

// bundle puts every node's .pcap (and the reasons of the ones that failed) in one .tgz.
func (j *CaptureJob) bundle() {
	j.mu.Lock()
	nodes := append([]*CaptureNode(nil), j.Nodes...)
	j.mu.Unlock()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	now := time.Now()
	used := map[string]int{}
	ok := 0
	var errLines []string
	for _, n := range nodes {
		who := n.Name
		if n.Status != "done" {
			errLines = append(errLines, who+": "+n.Error)
			continue
		}
		ok++
		base := strings.Trim(capNameRe.ReplaceAllString(who, "_"), "_")
		if base == "" {
			base = "node"
		}
		if n.Self {
			base += "-this-node"
		}
		used[base]++
		name := base + ".pcap"
		if used[base] > 1 {
			name = fmt.Sprintf("%s-%d.pcap", base, used[base])
		}
		if tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(n.pcap)), ModTime: now}) == nil {
			tw.Write(n.pcap)
		}
	}
	summary := fmt.Sprintf("Capture on %s for %d s, filter %q, started %s\n", j.Iface, j.Seconds, j.Filter, j.Started.Format(time.RFC3339))
	for _, n := range nodes {
		if n.Status == "done" {
			summary += fmt.Sprintf("%s: %d packets kept (%d seen), %d bytes\n", n.Name, n.Kept, n.Seen, n.Bytes)
		}
	}
	for name, txt := range map[string]string{"summary.txt": summary, "errors.txt": strings.Join(errLines, "\n") + "\n"} {
		if name == "errors.txt" && len(errLines) == 0 {
			continue
		}
		if tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(txt)), ModTime: now}) == nil {
			tw.Write([]byte(txt))
		}
	}
	tw.Close()
	gz.Close()
	j.mu.Lock()
	defer j.mu.Unlock()
	if ok == 0 {
		if len(errLines) == 1 {
			j.Error = errLines[0]
		} else {
			j.Error = "the capture failed on every node: see the list"
		}
	} else {
		j.tgz = out.Bytes()
		j.Ready = true
	}
	j.Done = true
}

// CaptureClusterStatus is the current (or last) job, or nil.
func (m *Mgmt) CaptureClusterStatus() *CaptureJob {
	m.capjobs.mu.Lock()
	j := m.capjobs.cur
	m.capjobs.mu.Unlock()
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := &CaptureJob{ID: j.ID, Started: j.Started, Seconds: j.Seconds, Iface: j.Iface, Filter: j.Filter, Done: j.Done, Ready: j.Ready,
		Error: j.Error, Elapsed: time.Since(j.Started).Seconds()}
	for _, n := range j.Nodes {
		c := *n
		c.pcap = nil
		out.Nodes = append(out.Nodes, &c)
	}
	sort.SliceStable(out.Nodes, func(a, b int) bool { return out.Nodes[a].Self && !out.Nodes[b].Self })
	return out
}

// CaptureClusterBundle is the .tgz of a finished job.
func (m *Mgmt) CaptureClusterBundle() ([]byte, error) {
	m.capjobs.mu.Lock()
	j := m.capjobs.cur
	m.capjobs.mu.Unlock()
	if j == nil {
		return nil, errors.New("no cluster capture has been run")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.Done {
		return nil, errors.New("the capture is still running")
	}
	if j.tgz == nil {
		return nil, errors.New(firstNonEmpty(j.Error, "there is nothing to download"))
	}
	return j.tgz, nil
}

// ── the web side ─────────────────────────────────────────────────────────────

func (w *WebServer) registerCapture(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/capture/interfaces", w.authed(w.op("capture.interfaces", nil)))
	mux.HandleFunc("POST /api/capture/start", w.authed(w.op("capture.start", nil)))
	mux.HandleFunc("POST /api/capture/stop", w.authed(w.op("capture.stop", nil)))
	mux.HandleFunc("POST /api/capture/clear", w.authed(w.op("capture.clear", nil)))
	mux.HandleFunc("GET /api/capture/packets", w.authed(w.op("capture.packets", []string{"since"})))
	// a timed capture in a buffer of its own, started and then asked for (each request is quick: the peer channel
	// gives a slow request only a few seconds per address before it tries the next one)
	mux.HandleFunc("POST /api/capture/job", w.authed(w.op("capture.job.start", nil)))
	mux.HandleFunc("GET /api/capture/job", w.authed(w.op("capture.job.get", []string{"id"})))
	mux.HandleFunc("GET /api/capture/download", w.authed(w.handleCaptureDownload))
	// every node at once: the node asked asks the others itself, so none of these can be relayed
	mux.HandleFunc("POST /api/clustercapture/start", w.authed(w.op("capture.cluster.start", nil)))
	mux.HandleFunc("GET /api/clustercapture/status", w.authed(w.op("capture.cluster.status", nil)))
	mux.HandleFunc("GET /api/clustercapture/download", w.authed(w.handleClusterCaptureDownload))
	w.registerTshoot(mux)
}

// handleCaptureDownload sends the buffer of this node's Capture page as a .tgz holding one .pcap.  Reached through another
// node it is the newest packets that fit the peer channel.
func (w *WebServer) handleCaptureDownload(rw http.ResponseWriter, r *http.Request, s *session) {
	w.mg.capture.mu.Lock()
	iface := w.mg.capture.iface
	w.mg.capture.mu.Unlock()
	if iface == "" {
		iface = "capture"
	}
	limit := 0
	if _, relayed := r.Context().Value(proxyCtxKey{}).(*session); relayed {
		limit = capRelayBytes
	}
	host, _ := hostnameShort()
	now := time.Now()
	base := fmt.Sprintf("ddgw-%s-%s-%s", host, capNameRe.ReplaceAllString(iface, "_"), now.Format("20060102-150405"))
	var pcap bytes.Buffer
	w.mg.capture.writePcap(&pcap, limit)
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if tw.WriteHeader(&tar.Header{Name: base + ".pcap", Mode: 0o644, Size: int64(pcap.Len()), ModTime: now}) == nil {
		tw.Write(pcap.Bytes())
	}
	tw.Close()
	gz.Close()
	rw.Header().Set("Content-Type", "application/gzip")
	rw.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", base+".tgz"))
	rw.Write(out.Bytes())
}

func (w *WebServer) handleClusterCaptureDownload(rw http.ResponseWriter, r *http.Request, s *session) {
	b, err := w.mg.CaptureClusterBundle()
	if err != nil {
		jsonError(rw, http.StatusConflict, err.Error())
		return
	}
	rw.Header().Set("Content-Type", "application/gzip")
	rw.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "ddgw-cluster-capture-"+time.Now().Format("20060102-150405")+".tgz"))
	rw.Write(b)
}

func hostnameShort() (string, error) {
	h := selfHost()
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return capNameRe.ReplaceAllString(h, "_"), nil
}
