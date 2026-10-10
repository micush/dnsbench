package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// StatusServer answers JSON-line commands on a Unix socket:
//
//	{"cmd":"snapshot"}   neighbor/gateway table
//	{"cmd":"assert-agc"} directed failover onto this node
//	{"cmd":"dns"}        upstream pool state (latency ranking)
//
// Anything that changes state or exposes configuration (assert-agc and every
// management command such as versions.restore or tls.install) is accepted
// only from root (SO_PEERCRED); the read-only snapshot/dns views stay open to
// local users as before.
type StatusServer struct {
	path string
	sup  *Supervisor
	mg   *Mgmt
	l    net.Listener
	up   *upTracker
	stop chan struct{}
}

func NewStatusServer(path string, sup *Supervisor) *StatusServer {
	return &StatusServer{path: path, sup: sup, up: newUpTracker()}
}

// peerCred returns the uid and a printable name of the process on the other
// end of a unix connection.
func peerCred(c net.Conn) (uint32, string, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, "", errors.New("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, "", err
	}
	var cred *syscall.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, "", err
	}
	if serr != nil {
		return 0, "", serr
	}
	name := "uid " + strconv.Itoa(int(cred.Uid))
	if u, err := user.LookupId(name[4:]); err == nil {
		name = u.Username
	}
	return cred.Uid, "cli:" + name, nil
}

// privileged reports whether the connection may run state-changing commands.
func privileged(uid uint32) bool { return uid == 0 || int(uid) == os.Geteuid() }

func (s *StatusServer) Start() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	os.Remove(s.path)
	l, err := net.Listen("unix", s.path)
	if err != nil {
		return err
	}
	s.l = l
	s.stop = make(chan struct{})
	go s.sampleUptime(s.stop)
	go hoststats.Run(s.stop)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	debugf("Status socket listening at %s", s.path)
	return nil
}

// sampleUptime looks at the topology picture every few seconds so the uptimes notice an
// outage even when nobody has the page open.
func (s *StatusServer) sampleUptime(stop chan struct{}) {
	t := time.NewTicker(upSampleEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			// every pass is also one availability sample for the gateways' history (idle and paused say nothing)
			for _, g := range s.canvasGroups() {
				switch g.Status {
				case "ok", "warn":
					srvhist.series(gwKey(g.GroupID)).avail(true)
				case "bad":
					srvhist.series(gwKey(g.GroupID)).avail(false)
				}
			}
		}
	}
}

func (s *StatusServer) Stop() {
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
	if s.l != nil {
		s.l.Close()
		os.Remove(s.path)
	}
}

func (s *StatusServer) handle(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(60 * time.Second)) // uploads can be large
	line, err := bufio.NewReaderSize(c, 1<<20).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	c.SetDeadline(time.Now().Add(150 * time.Second)) // joins/syncs talk to peers
	var req struct {
		Cmd  string          `json:"cmd"`
		Args json.RawMessage `json:"args"`
	}
	if json.Unmarshal(line, &req) != nil || req.Cmd == "" {
		req.Cmd = "snapshot"
	}
	uid, actor, credErr := peerCred(c)
	allowed := credErr == nil && privileged(uid)
	var res map[string]any
	switch req.Cmd {
	case "snapshot":
		res = map[string]any{"ok": true, "data": s.snapshot(), "names": s.ipNames()}
	case "dns":
		res = s.dnsStatus()
	case "canvas":
		res = s.canvasView()
	case "assert-agc":
		if !allowed {
			res = denied()
		} else {
			res = s.assertAGC()
		}
	default:
		switch {
		case !strings.Contains(req.Cmd, "."):
			res = map[string]any{"ok": false, "error": "Unknown command: " + req.Cmd}
		case !allowed:
			res = denied()
		case s.mg == nil:
			res = map[string]any{"ok": false, "error": "management is not available"}
		default:
			data, err := s.mg.Op(req.Cmd, req.Args, actor)
			if err != nil {
				res = map[string]any{"ok": false, "error": err.Error(), "data": data}
			} else {
				res = map[string]any{"ok": true, "data": data}
			}
		}
	}
	b, _ := json.Marshal(res)
	c.Write(append(b, '\n'))
}

func (s *StatusServer) snapshot() []SnapshotRow {
	rows := []SnapshotRow{}
	for _, e := range s.sup.engineList() {
		rows = append(rows, e.snapshot()...)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		if a.AF != b.AF {
			return a.AF < b.AF
		}
		return a.PeerIP < b.PeerIP
	})
	return rows
}

func (s *StatusServer) assertAGC() map[string]any {
	var msgs []string
	for _, e := range s.sup.engineList() {
		e.mu.Lock()
		msgs = append(msgs, e.assertAGCLocked())
		e.mu.Unlock()
	}
	if len(msgs) == 0 {
		return map[string]any{"ok": false, "error": "No engines running"}
	}
	return map[string]any{"ok": true, "messages": msgs}
}

// ipNames maps the addresses of the cluster's nodes (as the Gateways page lists them) to the nodes' names.
func (s *StatusServer) ipNames() map[string]string {
	if s.mg == nil || s.mg.cl == nil {
		host, _ := os.Hostname()
		out := map[string]string{}
		for _, r := range s.snapshot() {
			if r.Local && r.PeerIP != "" {
				out[normIP(r.PeerIP)] = host
			}
		}
		return out
	}
	return nodeNamesByIP(s.mg.cl.View().Peers)
}

func (s *StatusServer) dnsStatus() map[string]any {
	pools := s.sup.poolList()
	if len(pools) == 0 {
		return map[string]any{"ok": false, "error": "there is no gateway, so there is no DNS proxy (draw one on the Topology page)"}
	}
	listeners := []string{}
	for _, r := range s.snapshot() {
		if r.Local && r.DNSUp {
			vip := r.VIP4
			if r.AF == "v6" {
				vip = r.VIP6
			}
			listeners = append(listeners, "group "+itoa(r.GroupID)+" "+
				strings.SplitN(vip, "/", 2)[0]+":"+itoa(s.sup.dnsFor(r.GroupID).ListenPort))
		}
	}
	var total, answered, servfail uint64
	var list []map[string]any
	for _, pi := range pools {
		total += pi.Pool.Queries.Load()
		answered += pi.Pool.Answered.Load()
		servfail += pi.Pool.Failed.Load()
		probes := 0
		for _, sv := range pi.Cfg.Servers {
			probes += len(pi.Cfg.queriesFor(sv))
		}
		name := ""
		if pi.Key != 0 {
			name = s.sup.groupName(pi.Key)
		}
		list = append(list, map[string]any{
			"key": pi.Key, "name": name, "groups": pi.Groups, "servers": s.namedServers(pi), "using_fallback": pi.Pool.UsingFallback(),
			"down_percent": pi.Cfg.DownPercent, "probes": probes,
			"queries": pi.Pool.Queries.Load(), "answered": pi.Pool.Answered.Load(),
			"servfail": pi.Pool.Failed.Load(),
			"ecs":      pi.Cfg.ECS, "ecs_sent": pi.Pool.ECSSent.Load(),
			"allowed_clients": len(pi.Cfg.AllowedClients), "client_rate": pi.Cfg.ClientRate, "client_burst": int(effectiveBurst(pi.Cfg.ClientRate, pi.Cfg.ClientBurst)),
			"client_action": orDefault(pi.Cfg.ClientAction, actDrop), "denied": pi.Pool.Denied.Load(), "limited": pi.Pool.Limited.Load(),
			"cache":    pi.Pool.cache.stats(),
			"dot_port": pi.Cfg.DoTPort, "doh_port": pi.Cfg.DoHPort, "tls_insecure": pi.Cfg.TLSInsecure, "spread": pi.Cfg.Spread, "spread_band": pi.Cfg.SpreadBand,
		})
	}
	first := list[0]
	return map[string]any{"ok": true, "data": map[string]any{
		"servers":      first["servers"],
		"pools":        list,
		"listeners":    listeners,
		"queries":      total,
		"answered":     answered,
		"servfail":     servfail,
		"down_percent": first["down_percent"],
		"probes":       first["probes"],
	}}
}

// namedServers is the pool's servers with the names given on the Topology page (Configure ▸ DNS ▸ server names).  The pool
// itself runs without them (a rename must not restart it), so they come from the full configuration.
func (s *StatusServer) namedServers(pi poolInfo) []ServerStat {
	sv := pi.Pool.Snapshot()
	names := s.sup.serverNames(pi.Key)
	for i := range sv {
		sv[i].Name = names[sv[i].Addr]
	}
	return sv
}

func denied() map[string]any {
	return map[string]any{"ok": false, "error": "permission denied: this command must be run as root (try sudo)"}
}

// statusRequest is the CLI side.
func statusRequest(path string, cmd map[string]any) (map[string]any, error) {
	return statusRequestTimeout(path, cmd, 5*time.Second)
}

func statusRequestTimeout(path string, cmd map[string]any, timeout time.Duration) (map[string]any, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("ddgw is not running (status socket not found)")
		}
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(timeout))
	b, _ := json.Marshal(cmd)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	var res map[string]any
	if err := json.NewDecoder(c).Decode(&res); err != nil {
		return nil, err
	}
	return res, nil
}
