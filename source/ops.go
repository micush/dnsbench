package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Op is the single entry point the CLI uses (through the status socket) for
// everything in the management layer.  The web GUI calls the same Mgmt
// methods directly, so both front ends behave identically.
func (m *Mgmt) Op(cmd string, raw json.RawMessage, actor string) (any, error) {
	var a struct {
		ID       string   `json:"id"`
		A        string   `json:"a"`
		B        string   `json:"b"`
		Note     string   `json:"note"`
		Config   string   `json:"config"`
		CertPEM  string   `json:"cert_pem"`
		KeyPEM   string   `json:"key_pem"`
		CN       string   `json:"cn"`
		DNS      []string `json:"dns"`
		IPs      []string `json:"ips"`
		Code     string   `json:"code"`
		Addr     string   `json:"addr"`
		Nodes    []string `json:"nodes"`
		Force    bool     `json:"force"`
		Enabled  bool     `json:"enabled"`
		DataB64  string   `json:"data"`
		Limit    int      `json:"limit"`
		Hostname string   `json:"hostname"`
		LogLevel string   `json:"level"`
		LogText  string   `json:"q"`
		LogSince string   `json:"since"`
		LogN     string   `json:"n"`
		QFrom    string   `json:"from"`
		QTo      string   `json:"to"`
		QRcode   string   `json:"rcode"`
		QClient  string   `json:"client"`
		QDomain  string   `json:"domain"`
		Lookup   string   `json:"lookup"`
		Paused   bool     `json:"paused"`
		User     string   `json:"username"`
		Password string   `json:"password"`
		Expires  int64    `json:"expires"`
		Iface    string   `json:"iface"`
		Filter   string   `json:"filter"`
		Seconds  int      `json:"seconds"`
		Group    int      `json:"group"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("bad arguments: %w", err)
		}
	}
	switch cmd {
	case "tshoot.node":
		b, err := m.TshootNode(a.Enabled, actor)
		return map[string]any{"tgz": b}, err
	case "tshoot.cluster":
		b, err := m.TshootCluster(a.Enabled, actor)
		return map[string]any{"tgz": b}, err
	case "vmac.test":
		return m.VmacTest(a.Group)
	case "canvas.edit":
		var e canvasEdit
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("bad arguments: %w", err)
		}
		msg, err := m.CanvasEdit(e, actor)
		if err != nil {
			return nil, err
		}
		return map[string]string{"message": msg}, nil
	// configuration history
	case "versions.list":
		return m.VersionsList(), nil
	case "versions.get":
		meta, cfg, err := m.VersionGet(a.ID)
		return map[string]any{"meta": meta, "config": cfg}, err
	case "versions.diff":
		return m.VersionDiff(a.A, a.B)
	case "versions.snapshot":
		return m.VersionSnapshot(a.Note, actor)
	case "versions.restore":
		dc, err := m.VersionRestore(a.ID, actor)
		return map[string]any{"config": dc}, err
	case "versions.export":
		_, cfg, err := m.VersionGet(a.ID)
		return map[string]any{"config": cfg}, err
	case "config.import":
		dc, err := m.ConfigImport([]byte(a.Config), actor, a.Note)
		return map[string]any{"config": dc}, err

	// certificate
	case "tls.status":
		return m.certs.Status(), nil
	case "tls.install":
		info, warns, err := m.TLSInstall(a.CertPEM, a.KeyPEM, actor)
		return map[string]any{"info": info, "warnings": warns}, err
	case "tls.csr":
		csr, err := m.certs.GenerateCSR(a.CN, a.DNS, a.IPs)
		return map[string]any{"csr": csr}, err
	case "tls.revert":
		info, err := m.TLSRevert(actor)
		return info, err
	case "tls.regenerate":
		return m.certs.Regenerate(actor)

	// cluster
	case "cluster.status":
		return m.cl.View(), nil
	case "cluster.token":
		code, exp, err := m.cl.MintJoinCode(actor)
		return map[string]any{"code": code, "expires": exp}, err
	case "cluster.join":
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		err := m.cl.Join(ctx, a.Code, actor)
		return m.cl.View(), err
	case "cluster.promote":
		snap, err := m.cl.Promote(actor)
		return map[string]any{"epoch": snap.Epoch, "primary": snap.PrimaryAddr}, err
	case "cluster.remove":
		err := m.cl.RemovePeer(a.Addr, actor)
		return m.cl.View(), err
	case "cluster.unremove":
		err := m.cl.UnremovePeer(a.Addr, actor)
		return m.cl.View(), err
	case "cluster.leave":
		err := m.cl.Leave(actor)
		return m.cl.View(), err
	case "cluster.sync":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := m.cl.SyncOnce(ctx)
		return m.cl.View(), err

	// bgp
	case "bgp.status":
		return m.BGPStatus()
	case "bgp.set":
		var c BGPConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("bad arguments: %w", err)
		}
		if err := m.BGPSet(c, actor); err != nil {
			return nil, err
		}
		return m.BGPStatus()
	case "bgp.operate":
		var o BGPOperateArgs
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, fmt.Errorf("bad arguments: %w", err)
		}
		if err := m.BGPOperate(o, actor); err != nil {
			return nil, err
		}
		return m.BGPStatus()
	// log
	case "log.read", "log.view":
		q, err := logQueryFromArgs(a.LogLevel, a.LogText, a.LogSince, a.LogN)
		if err != nil {
			return nil, err
		}
		if cmd == "log.view" && (q.Limit == 0 || q.Limit > logReadMax) {
			q.Limit = logReadMax // the GUI never gets more than this in one reply
		}
		return ReadLog(q)
	// query statistics
	case "qstats.get":
		from, to, err := qstatsRange(a.QFrom, a.QTo)
		if err != nil {
			return nil, err
		}
		f, err := qstatsFilter(a.QRcode, a.QClient, a.QDomain)
		if err != nil {
			return nil, err
		}
		return qstats.Query(from, to, f), nil
	// the same for the whole cluster: every node's numbers added together (Node menu ▸ Cluster, --stats --all-nodes)
	// packet capture: this node's Capture page, a timed capture (also what each node runs for the cluster-wide one), and
	// the capture on every node at once
	case "capture.interfaces":
		return m.captureInterfaces(), nil
	case "capture.start":
		return map[string]any{"started": true}, m.CaptureStart(a.Iface, a.Filter, actor)
	case "capture.stop":
		m.CaptureStop()
		return map[string]any{"stopped": true}, nil
	case "capture.clear":
		m.CaptureClear()
		return map[string]any{"cleared": true}, nil
	case "capture.packets":
		since, _ := strconv.ParseInt(a.LogSince, 10, 64)
		return m.capture.since(since, 3000), nil
	case "capture.run":
		return m.CaptureRun(context.Background(), a.Iface, a.Filter, a.Seconds, actor)
	case "capture.job.start":
		return m.CaptureJobStart(a.ID, a.Iface, a.Filter, a.Seconds, actor)
	case "capture.job.get":
		return m.CaptureJobGet(a.ID)
	case "capture.cluster.start":
		return m.CaptureClusterStart(a.Iface, a.Filter, a.Seconds, actor)
	case "capture.cluster.bundle":
		b, err := m.CaptureClusterBundle()
		return map[string]any{"tgz": b}, err
	case "capture.cluster.status":
		if j := m.CaptureClusterStatus(); j != nil {
			return j, nil
		}
		return map[string]any{"none": true}, nil
	// clear this node's statistics, and the file they were saved to
	case "qstats.clear":
		qstats.Clear()
		if err := savePersisted(m.stateDir); err != nil {
			warnf("statistics: saving after clear: %v", err)
		}
		warnf("statistics: %q cleared the statistics of this node", actor)
		return map[string]any{"cleared": true}, nil
	// the same on every node of the cluster
	case "qstats.clear.cluster":
		parts, infos := clusterGatherReq(context.Background(), m, proxyReq{User: actor, Method: "POST", Path: "/api/qstats/clear", CT: "application/json", Body: []byte("{}")}, clusterStatsTimeout,
			func() (*struct{ Cleared bool }, error) {
				qstats.Clear()
				if err := savePersisted(m.stateDir); err != nil {
					warnf("statistics: saving after clear: %v", err)
				}
				warnf("statistics: %q cleared the statistics of this node", actor)
				return &struct{ Cleared bool }{true}, nil
			})
		if len(parts) == 0 {
			return nil, errors.New("no node answered")
		}
		return map[string]any{"cleared": true, "nodes": infos}, nil
	// nmap of a client
	case "scan.start":
		return scanStart(a.QClient, actor)
	case "scan.get":
		return scanGet(a.QClient)
	case "qstats.cluster":
		from, to, err := qstatsRange(a.QFrom, a.QTo)
		if err != nil {
			return nil, err
		}
		f, err := qstatsFilter(a.QRcode, a.QClient, a.QDomain)
		if err != nil {
			return nil, err
		}
		return m.clusterQStats(context.Background(), actor, from, to, f)
	case "host.cluster":
		from, to, err := hostRange(a.QFrom, a.QTo)
		if err != nil {
			return nil, err
		}
		return m.clusterHost(context.Background(), actor, from, to)
	// host statistics: CPU, memory, disk, network
	case "host.get":
		from, to, err := hostRange(a.QFrom, a.QTo)
		if err != nil {
			return nil, err
		}
		return hoststats.Query(from, to), nil
	// how one upstream server has answered, per minute for up to 7 days (Topology ▸ server ▸ Statistics…, --server-stats)
	case "dns.serverstats":
		from, to, err := hostRange(a.QFrom, a.QTo)
		if err != nil {
			return nil, err
		}
		addr := strings.TrimSpace(a.Addr)
		if addr == "" {
			return nil, errors.New("which server? give its address")
		}
		// a gateway ("gw:7") or a domain on a server ("dom:ADDR|name|TYPE") is named by its history key
		if kindOf(addr) == "server" {
			if n, err := normalizeServer(addr); err == nil {
				addr = n
			}
		}
		if kindOf(addr) == "domain" {
			if p := strings.SplitN(addr[4:], "|", 3); len(p) == 3 {
				if n, err := normalizeServer(p[0]); err == nil {
					p[0] = n
				}
				addr = domKey(p[0], p[1], p[2])
			}
		}
		if kindOf(addr) == "gateway" && m.cl != nil {
			return m.cl.clusterStats(addr, from, to) // every node's history of the gateway, not just this one's
		}
		return srvhist.Query(addr, from, to)
	// the recent dynamic DNS updates, newest first
	case "dnsupdates.get":
		return struct {
			Updates []UpdateLog `json:"updates"`
		}{updlog.recent()}, nil
	// whois of a domain (asked of the registry from this node, only when requested)
	case "whois.get":
		return whoisWithAddrs(a.QDomain)
	// a name for an address (reverse DNS) or an address for a name, for the Add DNS server form
	case "dns.lookup":
		return hostLookup(a.Lookup)
	// pause the whole node
	case "node.status":
		return m.NodePauseStatus()
	case "node.pause":
		msg, err := m.NodePause(a.Paused, actor)
		if err != nil {
			return nil, err
		}
		return map[string]any{"message": msg, "paused": a.Paused}, nil
	// console users
	case "users.list":
		return m.UsersList(), nil
	case "users.add", "users.password", "users.expiry", "users.delete", "users.grant", "users.revoke":
		var msg string
		var partial bool
		var err error
		switch cmd {
		case "users.add":
			msg, partial, err = m.UserAdd(a.User, a.Password, a.Expires, actor)
		case "users.password":
			msg, partial, err = m.UserPassword(a.User, a.Password, actor)
		case "users.expiry":
			msg, partial, err = m.UserExpiry(a.User, a.Expires, actor)
		case "users.grant":
			msg, partial, err = m.UserGrant(a.User, actor)
		case "users.revoke":
			msg, partial, err = m.UserRevoke(a.User, actor)
		default:
			msg, partial, err = m.UserDelete(a.User, actor)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"message": msg, "partial": partial}, nil
	// power
	case "power.status":
		return m.PowerStatus(), nil
	case "power.do":
		var r PowerReq
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("bad arguments: %w", err)
		}
		return m.Power(r, actor)

	// updates
	case "update.status":
		return m.UpdateStatus(), nil
	case "update.history":
		return m.upd.History(a.Limit), nil
	case "update.upload":
		body, err := base64.StdEncoding.DecodeString(a.DataB64)
		if err != nil {
			return nil, errors.New("upload is not valid base64")
		}
		ver, err := m.UpdateUpload(body, actor)
		return map[string]any{"version": ver}, err
	case "update.apply":
		ver, err := m.UpdateApplyLocal(actor, a.Force)
		return map[string]any{"target": ver, "started": err == nil}, err
	case "update.push":
		return m.UpdateStatus(), m.UpdatePush(resolveNodes(m, a.Nodes), actor)
	case "update.cancel":
		return m.UpdateStatus(), m.UpdateCancel(resolveNodes(m, a.Nodes), actor)
	case "update.auto":
		return m.UpdateStatus(), m.UpdateAuto(a.Enabled, actor)
	}
	return nil, fmt.Errorf("unknown command: %s", cmd)
}

// resolveNodes expands "all" and "self" to member addresses.
func resolveNodes(m *Mgmt, in []string) []string {
	var out []string
	for _, n := range in {
		switch strings.ToLower(strings.TrimSpace(n)) {
		case "all":
			for _, p := range m.cl.View().Peers {
				out = append(out, p.Addr)
			}
		case "self", "this":
			out = append(out, m.cl.selfAddrForEvents())
		default:
			if n = strings.TrimSpace(n); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}
