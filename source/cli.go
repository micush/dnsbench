package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Command-line front end for the management layer.  Every command is a thin
// wrapper over a status-socket call; the daemon does the work (and the GUI
// calls the same code), so the two always agree.

type cliFlags struct {
	versions, versionSnapshot, tlsStatus, tlsInstall, tlsCSR, tlsRevert, tlsRegenerate                                                                                                                  *bool
	clusterStatus, clusterToken, clusterPromote, clusterLeave, clusterSync                                                                                                                              *bool
	updateStatus, updateApply, updateHistory, noWait, yes                                                                                                                                               *bool
	versionShow, versionDiff, versionRestore, versionExport, configImport, note                                                                                                                         *string
	certFile, keyFile, cn, san                                                                                                                                                                          *string
	clusterJoin, clusterRemove, clusterUnremove                                                                                                                                                         *string
	users                                                                                                                                                                                               *bool
	userAdd, userPasswd, userExpiry, userDel, userGrant, userRevoke, expires                                                                                                                            *string
	power, powerAt                                                                                                                                                                                      *string
	powerIn                                                                                                                                                                                             *int
	bgp, bgpDisable, bgpEnable, logShow, statsShow, statsClear, hostShow, dnsUpdates, nodePause, nodeResume, nodeStatus, allNodes, captureList                                                          *bool
	logMin, logGrep, logSince, logLines, statsRange, statsRcode, statsClient, statsDomain, whoisName, scanAddr, dnsLookup, hostRange, captureIface, captureSecs, captureFilter, captureFile, tshootFile *string
	asn, routerID, asPrepend, nbrAdd, nbrDel, nbrDisable, nbrEnable, remoteAS, descr, passwd, multihop, keepalive, hold                                                                                 *string
	updateUpload, updatePush, updateCancel, updateAuto                                                                                                                                                  *string
	canvas, vmacTest, tshoot, tshootCapture                                                                                                                                                             *bool
	canvasPause, canvasResume, canvasMove, canvasAdd, canvasDel, canvasSet, vip, vip6, ifname, server, name, qtype, ecs, label, anycast, address, scope, node, realMACs                                 *string
	pos, group, ecsV4, ecsV6, spreadBand, downPercent, failThreshold, maxAttempts                                                                                                                       *int
	serverStats, gatewayStats, spread, lbMode                                                                                                                                                           *string
	latencyAlpha                                                                                                                                                                                        *float64
}

func registerCLIFlags(fs *flag.FlagSet) *cliFlags {
	b := func(n string) *bool { return fs.Bool(n, false, "") }
	s := func(n string) *string { return fs.String(n, "", "") }
	return &cliFlags{
		versions: b("versions"), versionSnapshot: b("version-snapshot"), tlsStatus: b("tls-status"),
		tlsInstall: b("tls-install"), tlsCSR: b("tls-csr"), tlsRevert: b("tls-revert"), tlsRegenerate: b("tls-regenerate"),
		clusterStatus: b("cluster-status"), clusterToken: b("cluster-token"), clusterPromote: b("cluster-promote"),
		clusterLeave: b("cluster-leave"), clusterSync: b("cluster-sync"),
		updateStatus: b("update-status"), updateApply: b("update-apply"), updateHistory: b("update-history"),
		noWait: b("no-wait"), yes: b("yes"),
		versionShow: s("version-show"), versionDiff: s("version-diff"), versionRestore: s("version-restore"),
		versionExport: s("version-export"), configImport: s("config-import"), note: s("note"),
		certFile: s("cert-file"), keyFile: s("key-file"), cn: s("cn"), san: s("san"),
		clusterJoin: s("cluster-join"), clusterRemove: s("cluster-remove"), clusterUnremove: s("cluster-unremove"),
		canvas: b("canvas"), vmacTest: b("test-vmac"), tshoot: b("tshoot"), tshootCapture: b("tshoot-capture"), tshootFile: s("tshoot-file"), realMACs: s("real-macs"), canvasAdd: s("canvas-add"), canvasDel: s("canvas-del"),
		canvasSet: s("canvas-set"), canvasMove: s("canvas-move"), pos: fs.Int("to", 0, ""), canvasPause: s("canvas-pause"), canvasResume: s("canvas-resume"), vip6: s("vip6"), ecs: s("ecs"), label: s("label"), anycast: s("anycast"), address: s("address"), scope: s("scope"), node: s("node"),
		ecsV4: fs.Int("ecs-v4", 0, ""), ecsV6: fs.Int("ecs-v6", 0, ""),
		serverStats: s("server-stats"), gatewayStats: s("gateway-stats"), spread: s("spread"), lbMode: s("lb"), spreadBand: fs.Int("spread-band", 0, ""), downPercent: fs.Int("down-percent", 0, ""),
		failThreshold: fs.Int("fail-threshold", 0, ""), maxAttempts: fs.Int("max-attempts", 0, ""), latencyAlpha: fs.Float64("latency-alpha", 0, ""),
		vip: s("vip"), ifname: s("interface"), server: s("server"), name: s("name"), qtype: s("type"),
		group: fs.Int("group", 0, ""),
		users: b("users"), userAdd: s("user-add"), userPasswd: s("user-passwd"), userExpiry: s("user-expiry"), userDel: s("user-del"), userGrant: s("user-grant"), userRevoke: s("user-revoke"), expires: s("expires"),
		power: s("power"), powerAt: s("at"), powerIn: fs.Int("in", 0, ""),
		nodePause: b("node-pause"), nodeResume: b("node-resume"), nodeStatus: b("node-status"),
		logShow: b("log"), logMin: s("log-min"), logGrep: s("log-grep"), logSince: s("log-since"), logLines: s("log-lines"), statsShow: b("stats"), statsClear: b("stats-clear"), scanAddr: s("scan"), statsRange: s("stats-range"), statsRcode: s("stats-rcode"), statsClient: s("stats-client"), statsDomain: s("stats-domain"), whoisName: s("whois"), dnsLookup: s("dns-lookup"), hostShow: b("host"), allNodes: b("all-nodes"), captureList: b("capture-interfaces"), captureIface: s("capture"), captureSecs: s("capture-seconds"), captureFilter: s("capture-filter"), captureFile: s("capture-file"), dnsUpdates: b("dns-updates"), hostRange: s("host-range"),
		bgp: b("bgp"), bgpDisable: b("bgp-disable"), bgpEnable: b("bgp-enable"), nbrDisable: s("bgp-neighbor-disable"), nbrEnable: s("bgp-neighbor-enable"), asn: s("asn"), routerID: s("router-id"), asPrepend: s("as-prepend"),
		nbrAdd: s("bgp-neighbor-add"), nbrDel: s("bgp-neighbor-del"), remoteAS: s("remote-as"), descr: s("description"),
		passwd:       s("password"),
		multihop:     s("multihop"),
		keepalive:    s("keepalive"),
		hold:         s("hold"),
		updateUpload: s("update-upload"), updatePush: s("update-push"), updateCancel: s("update-cancel"), updateAuto: s("update-auto"),
	}
}

// op runs a management command over the socket and returns the decoded data.
func op(sock, cmd string, args any) json.RawMessage {
	res, err := statusRequestTimeout(sock, map[string]any{"cmd": cmd, "args": args}, 140*time.Second)
	if err != nil {
		fatal(err)
	}
	if ok, _ := res["ok"].(bool); !ok {
		fatalf("%v", res["error"])
	}
	b, _ := json.Marshal(res["data"])
	return b
}

func decode(b json.RawMessage, v any) {
	if err := json.Unmarshal(b, v); err != nil {
		fatalf("unexpected reply from the daemon: %v", err)
	}
}

func askYes(prompt string, assumeYes bool) {
	if assumeYes {
		return
	}
	fmt.Printf("%s [y/N] ", prompt)
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "y") {
		fmt.Println("aborted")
		os.Exit(1)
	}
}

func lt(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func readFileArg(path, what string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		fatalf("cannot read %s: %v", what, err)
	}
	return string(b)
}

func splitCSV(s string) []string { return splitList(s) }

// run executes the management command named by the flags, if any.
func (f *cliFlags) run(sock string) bool {
	if *f.allNodes && !*f.statsShow && !*f.statsClear && !*f.hostShow && *f.captureIface == "" && !*f.tshoot {
		fatalf("--all-nodes goes with --stats, --host or --capture (the cluster's numbers added together, or a capture on every node)")
	}
	switch {
	// ── canvas ──
	case *f.canvas:
		showCanvas(sock)
	case *f.tshoot:
		runTshoot(sock, *f.allNodes, true, *f.tshootFile) // --tshoot-capture is still accepted; the capture is always taken now
	case *f.vmacTest:
		showVmacTest(sock, *f.group)
	case *f.canvasAdd != "" || *f.canvasDel != "" || *f.canvasSet != "" || *f.canvasPause != "" || *f.canvasResume != "" || *f.canvasMove != "":
		e := canvasEdit{Action: "add", Kind: *f.canvasAdd, Group: *f.group, VIP: *f.vip, VIP6: *f.vip6, Interface: *f.ifname, Label: *f.label, Anycast: *f.anycast, Address: *f.address, Scope: *f.scope, Node: *f.node, RealMACs: *f.realMACs,
			Server: *f.server, Name: *f.name, Type: *f.qtype, ECS: *f.ecs, ECSv4: *f.ecsV4, ECSv6: *f.ecsV6,
			LB: *f.lbMode, Spread: *f.spread, SpreadBand: *f.spreadBand, DownPercent: *f.downPercent, FailThreshold: *f.failThreshold, MaxAttempts: *f.maxAttempts, LatencyAlpha: *f.latencyAlpha, Pos: *f.pos}
		if *f.canvasMove != "" {
			e.Action, e.Kind = "move", *f.canvasMove
			if e.Kind != "server" && e.Kind != "domain" && e.Kind != "anycast" {
				fatalf("only a server, a domain or an anycast address can be moved: --canvas-move server|domain|anycast ... --to N")
			}
		}
		if *f.canvasDel != "" {
			e.Action, e.Kind = "del", *f.canvasDel
		}
		if *f.canvasSet != "" {
			e.Action, e.Kind = "set", *f.canvasSet
		}
		if *f.canvasPause != "" {
			e.Action, e.Kind = "pause", *f.canvasPause
		}
		if *f.canvasResume != "" {
			e.Action, e.Kind = "resume", *f.canvasResume
		}
		if (e.Action == "pause" || e.Action == "resume") && e.Kind != "gateway" && e.Kind != "server" && e.Kind != "anycast" && e.Kind != "domain" {
			fatalf("only a gateway, server, domain or anycast address can be paused: --canvas-%s gateway|server|domain|anycast --group N [--server ADDR] [--name DOMAIN] [--address ADDR] --scope node|all", e.Action)
		}
		if e.Action == "del" && e.Kind == "gateway" {
			askYes(fmt.Sprintf("Delete gateway %d with all its DNS servers and domains?", e.Group), *f.yes)
		}
		var r struct {
			Message string `json:"message"`
		}
		decode(op(sock, "canvas.edit", e), &r)
		fmt.Println(r.Message)
	// ── configuration history ──
	case *f.versions:
		var l []VersionMeta
		decode(op(sock, "versions.list", nil), &l)
		if len(l) == 0 {
			fmt.Println("No versions recorded yet.")
			return true
		}
		fmt.Printf("%-14s %-19s %-22s %s\n", "ID", "WHEN", "WHO", "WHAT")
		fmt.Println(strings.Repeat("-", 100))
		for _, v := range l {
			what := v.Summary
			if v.Note != "" {
				what += "  [" + v.Note + "]"
			}
			fmt.Printf("%-14s %-19s %-22s %s\n", v.ID, lt(v.At), trunc(v.Actor, 22), what)
		}
	case *f.versionShow != "":
		var r struct {
			Meta   VersionMeta     `json:"meta"`
			Config json.RawMessage `json:"config"`
		}
		decode(op(sock, "versions.get", map[string]any{"id": *f.versionShow}), &r)
		fmt.Printf("Version %s — %s by %s\n%s\n\n", r.Meta.ID, lt(r.Meta.At), r.Meta.Actor, r.Meta.Summary)
		fmt.Println(string(r.Config))
	case *f.versionDiff != "":
		a, b, _ := strings.Cut(*f.versionDiff, "..")
		var d VersionDiff
		decode(op(sock, "versions.diff", map[string]any{"a": a, "b": b}), &d)
		fmt.Printf("--- %s  %s\n+++ %s  %s\n", d.A.ID, d.A.Summary, d.B.ID, d.B.Summary)
		if d.Same {
			fmt.Println("(identical)")
			return true
		}
		for _, s := range d.Sections {
			fmt.Printf("* %s: %s\n", s.Label, s.Detail)
		}
		fmt.Println()
		for _, l := range d.Lines {
			if l.Op == "@" {
				fmt.Printf("@@ %s @@\n", l.Text)
			} else {
				fmt.Printf("%s %s\n", l.Op, l.Text)
			}
		}
	case *f.versionSnapshot:
		var m VersionMeta
		decode(op(sock, "versions.snapshot", map[string]any{"note": *f.note}), &m)
		fmt.Printf("Snapshot %s saved.\n", m.ID)
	case *f.versionRestore != "":
		askYes("Restore configuration version "+*f.versionRestore+" and apply it now?", *f.yes)
		op(sock, "versions.restore", map[string]any{"id": *f.versionRestore})
		fmt.Printf("Version %s restored and applied (the previous state was snapshotted first).\n", *f.versionRestore)
	case *f.versionExport != "":
		var r struct {
			Config json.RawMessage `json:"config"`
		}
		decode(op(sock, "versions.export", map[string]any{"id": *f.versionExport}), &r)
		fmt.Println(string(r.Config))
	case *f.configImport != "":
		raw := readFileArg(*f.configImport, "the config file")
		askYes("Replace the whole configuration with "+*f.configImport+" and apply it now?", *f.yes)
		op(sock, "config.import", map[string]any{"config": raw, "note": "imported from " + *f.configImport})
		fmt.Println("Configuration imported and applied.")

	// ── certificate ──
	case *f.tlsStatus:
		var i CertInfo
		decode(op(sock, "tls.status", nil), &i)
		printCert(i)
	case *f.tlsInstall:
		if *f.certFile == "" {
			fatalf("--tls-install needs --cert-file (and --key-file, unless a CSR from --tls-csr is pending)")
		}
		args := map[string]any{"cert_pem": readFileArg(*f.certFile, "the certificate file")}
		if *f.keyFile != "" {
			args["key_pem"] = readFileArg(*f.keyFile, "the key file")
		}
		var r struct {
			Info     CertInfo `json:"info"`
			Warnings []string `json:"warnings"`
		}
		decode(op(sock, "tls.install", args), &r)
		fmt.Println("Certificate installed; the GUI serves it from the next connection on (no restart).")
		for _, w := range r.Warnings {
			fmt.Println("warning:", w)
		}
		fmt.Println()
		printCert(r.Info)
	case *f.tlsCSR:
		var r struct {
			CSR string `json:"csr"`
		}
		var names, ips []string
		for _, n := range splitCSV(*f.san) {
			if net.ParseIP(n) != nil {
				ips = append(ips, n)
			} else {
				names = append(names, n)
			}
		}
		decode(op(sock, "tls.csr", map[string]any{"cn": *f.cn, "dns": names, "ips": ips}), &r)
		fmt.Print(r.CSR)
		fmt.Fprintln(os.Stderr, "\nThe matching private key stays on this node. When your CA returns the certificate:\n  ddgw --tls-install --cert-file CERT.pem")
	case *f.tlsRevert:
		askYes("Remove the installed certificate and go back to the self-signed one?", *f.yes)
		var i CertInfo
		decode(op(sock, "tls.revert", nil), &i)
		fmt.Println("Installed certificate removed.")
		printCert(i)
	case *f.tlsRegenerate:
		var i CertInfo
		decode(op(sock, "tls.regenerate", nil), &i)
		fmt.Println("Self-signed certificate regenerated.")
		printCert(i)

	// ── cluster ──
	case *f.clusterStatus:
		var v ClusterView
		decode(op(sock, "cluster.status", nil), &v)
		printCluster(v)
	case *f.clusterToken:
		var r struct {
			Code    string    `json:"code"`
			Expires time.Time `json:"expires"`
		}
		decode(op(sock, "cluster.token", nil), &r)
		fmt.Printf("Join code (single use, valid until %s):\n\n%s\n\nOn the new node:  ddgw --cluster-join '%s'\n", lt(r.Expires), r.Code, r.Code)
		fmt.Println("\nThe new node's shared settings (DNS, VIPs, keys) will be replaced by this cluster's; its interface, priority and weight stay its own.")
	case *f.clusterJoin != "":
		askYes("Join this node to the cluster? Its shared settings (DNS block, VIPs, keys, timers) will be replaced by the cluster's; a snapshot of the current config is saved first.", *f.yes)
		var v ClusterView
		decode(op(sock, "cluster.join", map[string]any{"code": *f.clusterJoin}), &v)
		fmt.Println("Joined.")
		printCluster(v)
	case *f.clusterPromote:
		askYes("Promote THIS node to primary? Do this only if the current primary is gone (or you are moving the primary role on purpose).", *f.yes)
		var r struct {
			Epoch   uint64 `json:"epoch"`
			Primary string `json:"primary"`
		}
		decode(op(sock, "cluster.promote", nil), &r)
		fmt.Printf("This node is now primary (epoch %d).\n", r.Epoch)
	case *f.clusterRemove != "":
		askYes("Remove "+*f.clusterRemove+" from the cluster?", *f.yes)
		op(sock, "cluster.remove", map[string]any{"addr": *f.clusterRemove})
		fmt.Println("Removed.")
	case *f.clusterUnremove != "":
		op(sock, "cluster.unremove", map[string]any{"addr": *f.clusterUnremove})
		fmt.Println("Removal lifted; the node can join again with a new join code.")
	case *f.clusterLeave:
		askYes("Leave the cluster? This node keeps its current settings and becomes its own single-node cluster.", *f.yes)
		op(sock, "cluster.leave", nil)
		fmt.Println("Left the cluster.")
	case *f.clusterSync:
		var v ClusterView
		decode(op(sock, "cluster.sync", nil), &v)
		fmt.Println("Synced.")
		printCluster(v)

	// ── bgp ──
	case *f.bgp || *f.asn != "" || *f.routerID != "" || *f.asPrepend != "" || *f.keepalive != "" || *f.hold != "" || *f.nbrAdd != "" || *f.nbrDel != "" ||
		*f.bgpDisable || *f.bgpEnable || *f.nbrDisable != "" || *f.nbrEnable != "":
		runBGP(sock, f)
	// ── statistics ──
	case *f.statsShow:
		runStats(sock, f)
	case *f.hostShow:
		runHost(sock, f)
	case *f.serverStats != "" || *f.gatewayStats != "":
		runServerStats(sock, f)
	case *f.dnsUpdates:
		runDNSUpdates(sock)
	case *f.dnsLookup != "":
		var r LookupResult
		decode(op(sock, "dns.lookup", map[string]string{"lookup": *f.dnsLookup}), &r)
		switch {
		case !r.Found:
			fatalf("%s", r.Error)
		case r.Kind == "name":
			fmt.Printf("%s  ->  %s\n", r.Query, r.Name)
		default:
			fmt.Printf("%s  ->  %s\n", r.Query, r.Addr)
		}
	case *f.captureList:
		var r CaptureInterfaces
		decode(op(sock, "capture.interfaces", nil), &r)
		printCaptureInterfaces(r)
	case *f.captureIface != "":
		runCapture(sock, f)
	case *f.statsClear:
		cmd := "qstats.clear"
		if *f.allNodes {
			cmd = "qstats.clear.cluster"
		}
		var r struct {
			Nodes []ClusterNodeInfo `json:"nodes"`
		}
		decode(op(sock, cmd, map[string]string{}), &r)
		if len(r.Nodes) == 0 {
			fmt.Println("The statistics of this node are cleared.")
		}
		for _, n := range r.Nodes {
			if n.OK {
				fmt.Printf("  %s: cleared\n", n.Name)
			} else {
				fmt.Printf("  %s: %s\n", n.Name, n.Error)
			}
		}
	case *f.scanAddr != "":
		runScan(sock, *f.scanAddr)
	case *f.whoisName != "":
		var w WhoisInfo
		decode(op(sock, "whois.get", map[string]string{"domain": *f.whoisName}), &w)
		fmt.Println(w.Text())
	// ── log ──
	case *f.logShow:
		runLog(sock, f)
	// ── pause the whole node ──
	case *f.nodePause || *f.nodeResume:
		var r struct {
			Message string `json:"message"`
		}
		decode(op(sock, "node.pause", map[string]bool{"paused": *f.nodePause}), &r)
		fmt.Println(r.Message)
	case *f.nodeStatus:
		var r NodePauseState
		decode(op(sock, "node.status", nil), &r)
		if r.Paused {
			fmt.Println("This node is PAUSED: it is not serving. Resume it with --node-resume.")
		} else {
			fmt.Println("This node is running.")
		}
	// ── console users ──
	case *f.users || *f.userAdd != "" || *f.userPasswd != "" || *f.userExpiry != "" || *f.userDel != "" || *f.userGrant != "" || *f.userRevoke != "":
		runUsers(sock, f)
	// ── power ──
	case *f.power != "":
		runPower(sock, f)
	// ── updates ──
	case *f.updateStatus:
		var v UpdateView
		decode(op(sock, "update.status", nil), &v)
		printUpdate(v)
	case *f.updateUpload != "":
		b, err := os.ReadFile(*f.updateUpload)
		if err != nil {
			fatalf("cannot read %s: %v", *f.updateUpload, err)
		}
		var r struct {
			Version string `json:"version"`
		}
		decode(op(sock, "update.upload", map[string]any{"data": base64.StdEncoding.EncodeToString(b)}), &r)
		fmt.Printf("Source v%s staged on this node. Apply it here with --update-apply, or to the cluster with --update-push all / --update-auto on.\n", r.Version)
	case *f.updateApply:
		var r struct {
			Target string `json:"target"`
		}
		decode(op(sock, "update.apply", map[string]any{"force": *f.yes}), &r)
		fmt.Printf("Building v%s and restarting into it…\n", r.Target)
		if !*f.noWait {
			waitForUpdate(sock, r.Target)
		}
	case *f.updatePush != "":
		op(sock, "update.push", map[string]any{"nodes": splitCSV(*f.updatePush)})
		fmt.Println("Queued. Each node updates within its sync interval, one at a time.")
	case *f.updateCancel != "":
		op(sock, "update.cancel", map[string]any{"nodes": splitCSV(*f.updateCancel)})
		fmt.Println("Cancelled.")
	case *f.updateAuto != "":
		on := yes(*f.updateAuto)
		if !on && !isNo(*f.updateAuto) {
			fatalf("--update-auto takes on or off")
		}
		op(sock, "update.auto", map[string]any{"enabled": on})
		fmt.Println("Auto-update is now", map[bool]string{true: "ON", false: "off"}[on])
	case *f.updateHistory:
		var h []UpdateEvent
		decode(op(sock, "update.history", map[string]any{"limit": 0}), &h)
		printUpdateHistory(h)
	default:
		return false
	}
	return true
}

func isNo(s string) bool {
	switch strings.ToLower(s) {
	case "no", "n", "false", "0", "off":
		return true
	}
	return false
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func printCert(i CertInfo) {
	if i.Error != "" {
		fmt.Println("Certificate problem:", i.Error)
		return
	}
	fmt.Printf("Source:      %s\n", i.Source)
	fmt.Printf("Subject:     %s\n", i.Subject)
	fmt.Printf("Issuer:      %s%s\n", i.Issuer, map[bool]string{true: "  (self-signed)", false: ""}[i.SelfSigned])
	fmt.Printf("Names:       %s\n", strings.Join(append(append([]string{}, i.DNSNames...), i.IPAddresses...), ", "))
	exp := ""
	switch {
	case i.Expired:
		exp = "  EXPIRED"
	case i.ExpiresSoon:
		exp = fmt.Sprintf("  (expires in %d days)", i.DaysLeft)
	default:
		exp = fmt.Sprintf("  (%d days left)", i.DaysLeft)
	}
	fmt.Printf("Valid:       %s → %s%s\n", lt(i.NotBefore), lt(i.NotAfter), exp)
	fmt.Printf("SHA-256:     %s\n", i.Fingerprint)
	if i.CertFile != "" {
		fmt.Printf("Files:       %s, %s\n", i.CertFile, i.KeyFile)
	}
	if i.InstalledBy != "" {
		fmt.Printf("Installed:   by %s at %s\n", i.InstalledBy, lt(i.InstalledAt))
	}
	if i.PendingCSR != "" {
		fmt.Println("A CSR is pending (its key is stored); install the issued certificate with --tls-install --cert-file FILE.")
	}
}

func printCluster(v ClusterView) {
	if !v.Enabled {
		fmt.Println("Clustering is disabled on this node (cluster.enabled = false).")
		return
	}
	fmt.Printf("This node: %s   node id %s   identity sha256 %s\n", v.Self, v.NodeID, shortFP(v.Fingerprint))
	fmt.Printf("Role: %s   epoch %d   primary %s   shared-settings revision %d\n", v.Role, v.Epoch, v.PrimaryAddr, v.SharedRev)
	if v.Joinable {
		fmt.Println("(not part of a multi-node cluster yet — mint a code with --cluster-token on another node and use --cluster-join)")
	}
	fmt.Println()
	const f = "%-16s %-40s %-28s %-8s %-9s %-6s %-7s %-7s %-19s %s\n"
	fmt.Printf(f, "NODE NAME", "NODE IP", "ADDRESS", "ROLE", "REACHABLE", "EPOCH", "RUNNING", "SOURCE", "LAST SEEN", "NOTE")
	fmt.Println(strings.Repeat("-", 170))
	for _, p := range v.Peers {
		role := string(p.Role)
		if p.IsPrimary {
			role = "primary"
		}
		name := orDefault(p.Hostname, "-")
		if p.Self {
			name += " *"
		}
		addr := p.Addr
		note := p.Error
		if p.Updating {
			note = "updating"
		}
		fmt.Printf(f, name, trunc(orDefault(strings.Join(p.IPs, " "), "-"), 40), addr, role, map[bool]string{true: "yes", false: "NO"}[p.Reachable], itoa(int(p.Epoch)),
			orDefault(p.Version, "-"), orDefault(p.SourceVersion, "-"), lt(p.LastSeen), trunc(note, 40))
	}
	fmt.Println("\n* = this node")
	for _, r := range v.Removed {
		fmt.Println("removed:", r, "(--cluster-unremove to allow it back)")
	}
	if v.Conflict != "" {
		fmt.Println("CONFLICT:", v.Conflict)
	}
	if v.LastSyncError != "" {
		fmt.Println("last sync error:", v.LastSyncError)
	}
	for _, w := range v.Warnings {
		fmt.Println("warning:", w)
	}
}

func printUpdate(v UpdateView) {
	fmt.Printf("Running v%s on %s\n", v.Running, v.Self)
	if v.SourceVersion == "" {
		fmt.Println("Staged source: none (upload a release with --update-upload FILE.tgz)")
	} else {
		fmt.Printf("Staged source: v%s\n", v.SourceVersion)
	}
	if v.Toolchain != "" {
		fmt.Printf("Go toolchain:  %s\n", v.Toolchain)
	} else {
		fmt.Println("Go toolchain:  NOT FOUND — this node cannot build updates (re-run install.sh)")
	}
	fmt.Printf("Auto-update:   %s\n", map[bool]string{true: "ON (every node updates itself when newer source is available)", false: "off"}[v.Intent.AutoAll])
	if v.Busy {
		fmt.Printf("In progress:   %s\n", orDefault(v.Phase, "yes"))
	}
	if v.Notice != "" {
		fmt.Println("NOTICE:", v.Notice)
	}
	fmt.Println()
	const f = "%-28s %-9s %-8s %-8s %-7s %s\n"
	fmt.Printf(f, "NODE", "REACHABLE", "RUNNING", "SOURCE", "QUEUED", "STATE")
	fmt.Println(strings.Repeat("-", 90))
	for _, n := range v.Nodes {
		name := n.Addr
		if n.Self {
			name += " *"
		}
		state := ""
		switch {
		case n.Updating:
			state = "updating"
		case n.Failed != "":
			state = "failed v" + n.Failed
		case n.Behind:
			state = "behind"
		}
		fmt.Printf(f, name, map[bool]string{true: "yes", false: "NO"}[n.Reachable], orDefault(n.Running, "-"),
			orDefault(n.Source, "-"), map[bool]string{true: "yes", false: ""}[n.Queued], state)
	}
	fmt.Println()
	recent := v.History
	if len(recent) > 50 {
		recent = recent[:50] // the newest; --update-history prints them all
	}
	printUpdateHistory(recent)
}

func printUpdateHistory(h []UpdateEvent) {
	if len(h) == 0 {
		fmt.Println("No update history.")
		return
	}
	fmt.Printf("%-19s %-26s %-11s %-9s %s\n", "WHEN", "NODE", "EVENT", "VERSION", "DETAIL")
	for _, e := range h {
		ver := e.To
		if e.From != "" {
			ver = e.From + "→" + e.To
		}
		d := e.Detail
		if e.By != "" {
			d = strings.TrimSpace(d + " (by " + e.By + ")")
		}
		fmt.Printf("%-19s %-26s %-11s %-9s %s\n", lt(e.At), trunc(e.Node, 26), e.Kind, ver, d)
	}
}

// waitForUpdate follows a local update until it finishes, fails or the
// daemon restarts into the new version.
func waitForUpdate(sock, target string) {
	deadline := time.Now().Add(15 * time.Minute)
	last := ""
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		res, err := statusRequestTimeout(sock, map[string]any{"cmd": "update.status"}, 10*time.Second)
		if err != nil { // socket gone: the daemon is restarting
			for i := 0; i < 30; i++ {
				time.Sleep(time.Second)
				if r2, err := statusRequestTimeout(sock, map[string]any{"cmd": "update.status"}, 5*time.Second); err == nil {
					var v UpdateView
					b, _ := json.Marshal(r2["data"])
					json.Unmarshal(b, &v)
					fmt.Printf("ddgw is back, running v%s.\n", v.Running)
					if v.Running != target {
						fmt.Println("(not the expected version — see --update-history)")
					}
					return
				}
			}
			fmt.Println("ddgw did not come back within 30s; check: systemctl status ddgw, journalctl -u ddgw")
			return
		}
		var v UpdateView
		b, _ := json.Marshal(res["data"])
		json.Unmarshal(b, &v)
		if v.Phase != "" && v.Phase != last {
			last = v.Phase
			fmt.Println(" ", v.Phase)
		}
		if !v.Busy {
			if v.Running == target {
				fmt.Printf("Running v%s.\n", v.Running)
				return
			}
			if v.Last != nil && v.Last.Kind == "failed" {
				fatalf("update failed: %s", v.Last.Detail)
			}
		}
	}
	fmt.Println("Still working; check --update-status.")
}

var canvasTag = map[string]string{"ok": "[ OK ]", "warn": "[WARN]", "bad": "[DOWN]", "idle": "[ -- ]", "paused": "[PAUS]"}

// showCanvas prints the same tree and status colours the GUI canvas draws.
func showCanvas(sock string) {
	res := mustStatus(sock, "canvas")
	if ok, _ := res["ok"].(bool); !ok {
		fatalf("%v", res["error"])
	}
	var gws []CanvasGateway
	decodeData(res, &gws)
	if len(gws) == 0 {
		fmt.Println("The canvas is empty: no gateways. Draw one with:")
		fmt.Println("  ddgw --canvas-add gateway --vip 10.0.0.1/24 --interface eth0")
		return
	}
	now := time.Now()
	upNote := func(u *UpInfo) string {
		if u == nil {
			return ""
		}
		return " [" + u.Text(now) + "]"
	}
	for _, g := range gws {
		addr := strings.TrimSpace(g.VIP4 + " " + g.VIP6)
		title := fmt.Sprintf("gateway %d", g.GroupID)
		if g.Name != "" {
			title = fmt.Sprintf("%s (gateway %d)", g.Name, g.GroupID)
		}
		fmt.Printf("%s (O) %s  %s  on %s — %s\n", canvasTag[g.Status], title, addr, g.Interface, g.Detail+upNote(g.Uptime))
		if len(g.Families) > 1 {
			for _, f := range g.Families {
				fmt.Printf("      %s %s %s — %s\n", canvasTag[f.Status], map[string]string{"v4": "IPv4", "v6": "IPv6"}[f.AF], f.VIP, f.Detail)
			}
		}
		if len(g.Fallback) > 0 {
			note := "kept for when every server is down"
			if g.UsingFallback {
				note = "IN USE now: every server is down or paused"
			}
			fmt.Printf("      fallback servers: %s — %s\n", strings.Join(g.Fallback, ", "), note)
		}
		if g.ECS {
			fmt.Println("      client subnet (ECS) is passed to the DNS servers")
		}
		if g.LBOwn {
			fmt.Printf("      load balancing: this gateway's own (spread %s, band %d%%, down at %d%%, %d failures, %d servers tried) — --lb settings follows Settings\n",
				map[bool]string{true: "on", false: "off"}[g.LB.Spread], g.LB.SpreadBand, g.LB.DownPercent, g.LB.FailThreshold, g.LB.MaxAttempts)
		}
		for _, a := range g.Anycast {
			state := "announced from this node (on lo)"
			if !a.Up {
				state = "withdrawn: " + orDefault(a.Reason, "not held")
			}
			if a.Up && a.Detail != "" {
				state += " — " + a.Detail
			}
			if a.Paused != "" && !strings.Contains(state, "paused on") {
				state += " (paused on " + map[string]string{"node": "this node", "all": "all nodes"}[a.Paused] + ")"
			}
			fmt.Printf("      anycast %s — %s%s\n", a.Addr, state, upNote(a.Uptime))
		}
		for _, n := range g.Nodes {
			who := n.Name
			if n.Name != n.Addr {
				who += " (" + n.Addr + ")"
			}
			if n.Self {
				who += " (this node)"
			}
			fmt.Printf("      %s /_/ cluster node %s — %s\n", canvasTag[n.Status], who, n.Detail)
		}
		for i, s := range g.Servers {
			lastS := i == len(g.Servers)-1
			branch, pad := "├─", "│  "
			if lastS {
				branch, pad = "└─", "   "
			}
			extra := s.Detail
			if s.Rank > 0 {
				extra += fmt.Sprintf(" (rank %d)", s.Rank)
			}
			if s.InBand {
				extra += " [spread: takes turns]"
			}
			who := s.Addr
			if s.Name != "" {
				who = s.Name + " (" + s.Addr + ")"
			}
			fmt.Printf("  %s %s [] server %s — %s\n", branch, canvasTag[s.Status], who, extra+upNote(s.Uptime))
			for j, t := range s.Tests {
				tb := "├─"
				if j == len(s.Tests)-1 {
					tb = "└─"
				}
				fmt.Printf("  %s %s %s /_\\ %s %s — %s\n", pad, tb, canvasTag[t.Status], t.Name, t.Type, t.Detail+upNote(t.Uptime))
			}
			if len(s.Tests) == 0 {
				fmt.Printf("  %s    (no domains: add one with --canvas-add domain --group %d --server %s --name example.com)\n", pad, g.GroupID, s.Addr)
			}
		}
		if len(g.Servers) == 0 {
			fmt.Printf("      (no DNS servers: add one with --canvas-add server --group %d --server 8.8.8.8)\n", g.GroupID)
		}
	}
	fmt.Println("\n(O) gateway   [] DNS server   /_\\ domain tested on that server")
}

// runPower is --power restart|shutdown|cancel|status [--in MINUTES | --at HH:MM].
func runPower(sock string, f *cliFlags) {
	act := strings.ToLower(*f.power)
	if act == "status" {
		var p PowerPending
		decode(op(sock, "power.status", nil), &p)
		if !p.Scheduled {
			fmt.Println("No restart or shutdown is scheduled.")
			return
		}
		fmt.Printf("Scheduled: %s at %s\n", p.Action, lt(mustParseTime(p.At)))
		return
	}
	if act == "reboot" {
		act = "restart"
	}
	if act == "poweroff" {
		act = "shutdown"
	}
	req := PowerReq{Action: act, When: "now", Force: *f.yes}
	switch {
	case act != "restart" && act != "shutdown" && act != "cancel":
		fatalf("--power takes restart, shutdown, cancel or status")
	case *f.powerIn > 0 && *f.powerAt != "":
		fatalf("give --in or --at, not both")
	case *f.powerIn > 0:
		req.When, req.Minutes = "in", *f.powerIn
	case *f.powerAt != "":
		req.When, req.Time = "at", *f.powerAt
	}
	if act != "cancel" {
		when := map[string]string{"now": "now", "in": fmt.Sprintf("in %d minute(s)", req.Minutes), "at": "at " + req.Time}[req.When]
		what := map[string]string{"restart": "Restart", "shutdown": "Shut down"}[act]
		extra := ""
		if act == "shutdown" {
			extra = " It stays off until it is powered on again."
		}
		askYes(fmt.Sprintf("%s the whole host %s? You will lose access to it for now.%s", what, when, extra), *f.yes)
	}
	var r PowerResult
	decode(op(sock, "power.do", req), &r)
	if act == "cancel" {
		fmt.Println("The scheduled power action is cancelled.")
		return
	}
	fmt.Printf("Host %s %s.\n", map[string]string{"restart": "restart", "shutdown": "shutdown"}[act], r.When)
}

func mustParseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// runLog is --log [--log-min LEVEL] [--log-grep WORDS] [--log-since 1h] [--log-lines N]:
// the daemon's log file, filtered; without --log-lines the whole matching log is printed.
func runLog(sock string, f *cliFlags) {
	n := *f.logLines
	if n == "" {
		n = "0"
	}
	var r LogResult
	decode(op(sock, "log.read", map[string]string{"level": *f.logMin, "q": *f.logGrep, "since": *f.logSince, "n": n}), &r)
	for _, l := range r.Lines {
		fmt.Println(l.Raw)
	}
	if r.Truncated {
		fmt.Fprintf(os.Stderr, "(%d older matching lines not shown; raise --log-lines)\n", r.Matched-len(r.Lines))
	}
}

// runStats is --stats [--stats-range 1h|1d|7d|30d] [--stats-rcode noerror|servfail|nxdomain|refused|other|update]: totals, query types, transports and the
// top clients and domains of the last hour (or day, week, month).  Memory only, last 30 days.
func runStats(sock string, f *cliFlags) {
	rng := *f.statsRange
	if rng == "" {
		rng = "1h"
	}
	var r QStatsResult
	cmd := "qstats.get"
	if *f.allNodes {
		cmd = "qstats.cluster"
	}
	decode(op(sock, cmd, map[string]string{"from": rng, "rcode": *f.statsRcode, "client": *f.statsClient, "domain": *f.statsDomain}), &r)
	printClusterNote(r.Cluster)
	pct := func(n uint64) string {
		if r.Sums.Total == 0 {
			return "  –"
		}
		return fmt.Sprintf("%5.1f%%", 100*float64(n)/float64(r.Sums.Total))
	}
	fmt.Printf("Queries answered since %s (statistics kept for 30 days, saved to disk; counting since %s)\n",
		lt(time.Unix(r.From, 0)), lt(time.Unix(r.Since, 0)))
	fmt.Printf("  Total          %10d\n", r.Sums.Total)
	for _, row := range []struct {
		n string
		v uint64
	}{{"No error", r.Sums.NoError}, {"Server failure", r.Sums.ServFail}, {"NX domain", r.Sums.NXDomain}, {"Refused", r.Sums.Refused}, {"Other", r.Sums.Other}} {
		fmt.Printf("  %-14s %10d  %s\n", row.n, row.v, pct(row.v))
	}
	fmt.Printf("  Updates        %10d  (dynamic DNS, %d failed)\n", r.Sums.Updates, r.Sums.UpdFail)
	fmt.Printf("  Clients        %10d\n", r.Sums.Clients)
	list := func(title string, l []NameCount, host bool) {
		if len(l) == 0 {
			return
		}
		fmt.Printf("\n%s\n", title)
		for i, e := range l {
			if i >= 10 {
				break
			}
			extra := ""
			if host && e.Host != "" {
				extra = "  " + e.Host
			}
			fmt.Printf("  %10d  %s%s\n", e.Count, e.Name, extra)
		}
	}
	if r.Rcode != "" {
		fmt.Printf("\nThe lists below count only the queries answered %q.\n", r.Rcode)
	}
	if r.Client != "" {
		fmt.Printf("\nTop domains below: what %s asked for.\n", r.Client)
	}
	if r.Domain != "" {
		fmt.Printf("\nTop clients below: who asked for %s.\n", r.Domain)
	}
	list("Query types", r.Types, false)
	list("Transport", r.Protos, false)
	list("Top clients", r.Clients, true)
	list("Top domains", r.Domains, false)
	if g := r.Guard; g.Trims > 0 {
		fmt.Printf("\nMemory guard: the oldest history was dropped %d time(s) to keep memory use under %.0f%%. Last: %s — %s\n", g.Trims, g.Limit, lt(time.Unix(g.Last, 0)), g.LastNote)
	}
}

// runHost is --host [--host-range 1h|1d|7d|30d]: what the machine is doing now, and its
// average and peak over the range.  Memory only, last 30 days.
func runHost(sock string, f *cliFlags) {
	rng := *f.hostRange
	if rng == "" {
		rng = "1h"
	}
	var r HostResult
	cmd := "host.get"
	if *f.allNodes {
		cmd = "host.cluster"
	}
	decode(op(sock, cmd, map[string]string{"from": rng}), &r)
	printClusterNote(r.Cluster)
	n := r.Now
	if n.At == 0 {
		fmt.Println("No sample yet: the daemon has only just started.")
		return
	}
	pc := func(v float64) string {
		if v < 0 {
			return "     –"
		}
		return fmt.Sprintf("%5.1f%%", v)
	}
	bps := func(v float64) string {
		if v < 0 {
			return "        –"
		}
		return humanBits(v)
	}
	stat := func(s []float64) (avg, peak float64) {
		var sum float64
		var c int
		avg, peak = -1, -1
		for _, v := range s {
			if v >= 0 {
				sum += v
				c++
				if v > peak {
					peak = v
				}
			}
		}
		if c > 0 {
			avg = sum / float64(c)
		}
		return
	}
	peakOf := func(avg, mx []float64) float64 {
		_, p := stat(mx)
		if _, p2 := stat(avg); p2 > p {
			p = p2
		}
		return p
	}
	fmt.Printf("Host statistics (kept for 30 days, saved to disk; counting since %s). Range: last %s.\n\n", lt(time.Unix(r.Since, 0)), rng)
	fmt.Printf("  %-14s %8s  %8s  %8s\n", "", "now", "average", "peak")
	ca, _ := stat(r.CPU)
	ma, mp := stat(r.Mem)
	ia, _ := stat(r.IO)
	ra, _ := stat(r.Rx)
	ta, _ := stat(r.Tx)
	if g := r.Guard; g.Trims > 0 {
		defer fmt.Printf("\nMemory guard: the oldest history was dropped %d time(s) to keep memory use under %.0f%%. Last: %s — %s\n", g.Trims, g.Limit, lt(time.Unix(g.Last, 0)), g.LastNote)
	}
	fmt.Printf("  %-14s %8s  %8s  %8s   %d cores, load %.2f %.2f %.2f\n", "CPU", pc(n.CPU), pc(ca), pc(peakOf(r.CPU, r.CPUMax)), n.Cores, n.Load[0], n.Load[1], n.Load[2])
	fmt.Printf("  %-14s %8s  %8s  %8s   %s of %s used", "Memory", pc(n.MemPct), pc(ma), pc(mp), humanBytes(n.MemUsed), humanBytes(n.MemTotal))
	if n.SwapTotal > 0 {
		fmt.Printf(", swap %s of %s", humanBytes(n.SwapUsed), humanBytes(n.SwapTotal))
	}
	fmt.Println()
	fmt.Printf("  %-14s %8s  %8s  %8s   busiest disk, share of time busy\n", "Disk I/O", pc(n.IO), pc(ia), pc(peakOf(r.IO, r.IOMax)))
	fmt.Printf("  %-14s %9s %9s %9s\n", "Network in", bps(n.Rx), bps(ra), bps(peakOf(r.Rx, r.RxMax)))
	fmt.Printf("  %-14s %9s %9s %9s\n", "Network out", bps(n.Tx), bps(ta), bps(peakOf(r.Tx, r.TxMax)))
	fmt.Println("\nFilesystems")
	for _, fs := range n.FS {
		fmt.Printf("  %-22s %6.1f%%  %s of %s  (%s, %s)\n", fs.Mount, fs.Pct, humanBytes(fs.Used), humanBytes(fs.Total), fs.Device, fs.Type)
	}
	fmt.Println("\nNetwork interfaces")
	for _, i := range n.Ifaces {
		util, speed, tag := "–", "speed unknown", ""
		if i.UtilPct >= 0 {
			util = fmt.Sprintf("%.1f%%", i.UtilPct)
		}
		if i.Speed > 0 {
			speed = fmt.Sprintf("%d Mb/s", i.Speed)
		}
		if !i.Counted {
			tag = "  (not in the totals)"
		}
		state := "down"
		if i.Up {
			state = "up"
		}
		fmt.Printf("  %-14s %-5s in %s  out %s  %s  link use %s%s\n", i.Name, state, humanBits(i.Rx), humanBits(i.Tx), speed, util, tag)
	}
}

func humanBits(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%6.2f Gb/s", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%6.2f Mb/s", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%6.2f kb/s", v/1e3)
	}
	return fmt.Sprintf("%6.0f b/s ", v)
}

func humanBytes(v uint64) string {
	f := float64(v)
	for _, u := range []string{"B", "KiB", "MiB", "GiB", "TiB"} {
		if f < 1024 || u == "TiB" {
			if u == "B" {
				return fmt.Sprintf("%d B", v)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return ""
}

// runDNSUpdates is --dns-updates: the recent dynamic DNS updates this node handled, newest first.
func runDNSUpdates(sock string) {
	var r struct {
		Updates []UpdateLog `json:"updates"`
	}
	decode(op(sock, "dnsupdates.get", nil), &r)
	if len(r.Updates) == 0 {
		fmt.Println("No dynamic DNS updates since ddgw started counting.")
		return
	}
	fmt.Printf("Recent dynamic DNS updates (the last %d are kept), newest first\n", updLogMax)
	for _, u := range r.Updates {
		to := "not delivered"
		if u.Addr != "" {
			to = "primary " + u.Primary + " (" + u.Addr + ")"
		}
		fmt.Printf("\n%s  %s -> zone %s  %s  [%s]\n", lt(time.Unix(u.At, 0)), u.Client, u.Zone, u.Result, to)
		if u.Note != "" {
			fmt.Printf("    %s\n", u.Note)
		}
		for _, c := range u.Changes {
			fmt.Printf("    %s\n", c)
		}
		if u.More > 0 {
			fmt.Printf("    ... and %d more\n", u.More)
		}
	}
}

// runServerStats is --server-stats ADDR [--name DOMAIN [--type T]] or --gateway-stats GROUP, with [--stats-range 1h|1d|7d]:
// how one upstream server, one monitored domain on a server, or one gateway has been doing (last 7 days): totals, and the
// latest buckets of latency and loss (and, for a gateway, traffic, cache hits and availability).
func runServerStats(sock string, f *cliFlags) {
	rng := *f.statsRange
	if rng == "" {
		rng = "1h"
	}
	key, what := *f.serverStats, "Server "+*f.serverStats
	if *f.gatewayStats != "" {
		n, err := strconv.Atoi(strings.TrimSpace(*f.gatewayStats))
		if err != nil || n < 1 || n > 255 {
			fatalf("--gateway-stats takes a gateway's group number (1-255)")
		}
		key, what = gwKey(n), fmt.Sprintf("Gateway (group %d)", n)
	} else if *f.name != "" {
		typ := *f.qtype
		if typ == "" {
			typ = "A"
		}
		key, what = domKey(*f.serverStats, *f.name, typ), fmt.Sprintf("Domain %s %s on server %s", *f.name, strings.ToUpper(typ), *f.serverStats)
	}
	var r ServerStatsResult
	decode(op(sock, "dns.serverstats", map[string]string{"addr": key, "from": rng}), &r)
	if !r.Known {
		fmt.Printf("Nothing recorded for %s: it is not configured on this node (or the daemon has only just started).\n", what)
		return
	}
	fmt.Printf("%s, last %s (%d s steps)\n", what, rng, r.Step)
	if r.Kind == "gateway" {
		switch {
		case r.NodesOff > 0:
			fmt.Printf("  nodes counted: %d of %d (%d not answering)\n", r.Nodes, r.Nodes+r.NodesOff, r.NodesOff)
		case r.Nodes > 1:
			fmt.Printf("  all %d nodes counted together\n", r.Nodes)
		}
	}
	gw := r.Kind == "gateway"
	switch r.Kind {
	case "gateway":
		fmt.Printf("  client queries answered %d, failed (SERVFAIL/REFUSED) %d, of which from the cache %d\n", r.Answered, r.Failed, r.Hits)
		fmt.Printf("  answer time average %.2f ms, worst %.2f ms; errors %.2f%% of queries; ", r.AvgMS, r.MaxMS, r.LossPct)
		if r.AvailPct >= 0 {
			fmt.Printf("available %.2f%% of the time\n\n", r.AvailPct)
		} else {
			fmt.Printf("availability not sampled in this range\n\n")
		}
		fmt.Printf("  %-11s %9s %9s %8s %8s %8s %8s\n", "time", "avg ms", "worst ms", "err %", "avail %", "q/s", "cache/s")
	case "domain":
		fmt.Printf("  probes passed %d, failed %d\n", r.ProbesOK, r.ProbesBad)
		fmt.Printf("  latency average %.2f ms, worst %.2f ms; probes failed %.2f%%\n\n", r.AvgMS, r.MaxMS, r.LossPct)
		fmt.Printf("  %-11s %9s %9s %8s\n", "time", "avg ms", "worst ms", "fail %")
	default:
		fmt.Printf("  live queries answered %d, failed %d; probe queries passed %d, failed %d\n", r.Answered, r.Failed, r.ProbesOK, r.ProbesBad)
		fmt.Printf("  latency average %.2f ms, worst %.2f ms; probes failed %.2f%%; client queries failed %.2f%%\n\n", r.AvgMS, r.MaxMS, r.LossPct, r.QueryLossPct)
		fmt.Printf("  %-11s %9s %9s %8s %9s %9s\n", "time", "avg ms", "worst ms", "loss %", "query %", "queries")
	}
	num := func(v float64, f string) string {
		if v < 0 {
			return "-"
		}
		return fmt.Sprintf(f, v)
	}
	rows := 0
	for i := len(r.Latency) - 1; i >= 0 && rows < 40; i-- {
		if r.Latency[i] < 0 && r.Loss[i] < 0 && r.Avail[i] < 0 && !(i < len(r.QueryLoss) && r.QueryLoss[i] >= 0) {
			continue
		}
		t := time.Unix(r.Start+int64(i)*int64(r.Step), 0).Format("15:04")
		if r.Step >= 1800 {
			t = time.Unix(r.Start+int64(i)*int64(r.Step), 0).Format("01-02 15:04")
		}
		switch {
		case gw:
			fmt.Printf("  %-11s %9s %9s %8s %8s %8s %8s\n", t, num(r.Latency[i], "%.2f"), num(r.LatMax[i], "%.2f"), num(r.Loss[i], "%.2f"), num(r.Avail[i], "%.2f"), num(r.QPS[i], "%.1f"), num(r.HitQPS[i], "%.1f"))
		case r.Kind == "domain":
			fmt.Printf("  %-11s %9s %9s %8s\n", t, num(r.Latency[i], "%.2f"), num(r.LatMax[i], "%.2f"), num(r.Loss[i], "%.2f"))
		default:
			fmt.Printf("  %-11s %9s %9s %8s %9s %9s\n", t, num(r.Latency[i], "%.2f"), num(r.LatMax[i], "%.2f"), num(r.Loss[i], "%.2f"), num(r.QueryLoss[i], "%.2f"), num(r.Queries[i], "%.0f"))
		}
		rows++
	}
	if rows == 0 {
		fmt.Println("  (nothing recorded in this range)")
	}
	fmt.Println("\n  newest first; the cap is the latest 40 buckets with data")
}

// printClusterNote says which nodes a cluster-wide answer adds together, and which could not answer.
func printClusterNote(c *ClusterInfo) {
	if c == nil {
		return
	}
	var used, missing []string
	for _, n := range c.Nodes {
		if n.OK {
			used = append(used, n.Name)
		} else {
			missing = append(missing, n.Name+" ("+n.Error+")")
		}
	}
	fmt.Printf("The whole cluster, added together: %s\n", strings.Join(used, ", "))
	if len(missing) > 0 {
		fmt.Printf("NOT included: %s\n", strings.Join(missing, ", "))
	}
	fmt.Println()
}

func printCaptureInterfaces(r CaptureInterfaces) {
	gw := map[string]bool{}
	for _, g := range r.Gateway {
		gw[g] = true
	}
	fmt.Printf("%-16s %-5s %-18s %s\n", "INTERFACE", "UP", "MAC", "")
	for _, i := range r.Interfaces {
		note := ""
		if gw[i.Name] {
			note = "(a gateway's interface)"
		}
		fmt.Printf("%-16s %-5s %-18s %s\n", i.Name, map[bool]string{true: "yes", false: "no"}[i.Up], orDefault(i.MAC, "-"), note)
	}
}

// runCapture is --capture IFACE: a timed capture on this node, or (with --all-nodes) on every node at once.
func runCapture(sock string, f *cliFlags) {
	secs := 10
	if *f.captureSecs != "" {
		n, err := strconv.Atoi(*f.captureSecs)
		if err != nil || n < 1 || n > capRunMaxSeconds {
			fatalf("--capture-seconds must be 1 to %d", capRunMaxSeconds)
		}
		secs = n
	}
	if *f.allNodes {
		runClusterCapture(sock, f, secs)
		return
	}
	fmt.Fprintf(os.Stderr, "capturing on %s for %d s%s ...\n", *f.captureIface, secs, map[bool]string{true: " (filter: " + *f.captureFilter + ")", false: ""}[*f.captureFilter != ""])
	var r CaptureRunResult
	decode(op(sock, "capture.run", map[string]any{"iface": *f.captureIface, "filter": *f.captureFilter, "seconds": secs}), &r)
	if *f.captureFile != "" {
		if err := os.WriteFile(*f.captureFile, r.Pcap, 0o644); err != nil {
			fatal(err)
		}
		fmt.Printf("%d packets kept (%d seen) written to %s\n", r.Kept, r.Seen, *f.captureFile)
		return
	}
	_, pkts, err := readPcap(r.Pcap)
	if err != nil {
		fatal(err)
	}
	for i, p := range pkts {
		if i == 500 {
			fmt.Printf("... %d more (use --capture-file to keep them all)\n", len(pkts)-500)
			break
		}
		fmt.Printf("%s  %s\n", p.t.Format("15:04:05.000000"), p.summary)
	}
	fmt.Printf("%d packets kept, %d seen on %s\n", r.Kept, r.Seen, r.Iface)
}

func runClusterCapture(sock string, f *cliFlags, secs int) {
	if *f.captureFile == "" {
		fatalf("--all-nodes needs --capture-file FILE.tgz (one .pcap per node is bundled in it)")
	}
	var job CaptureJob
	decode(op(sock, "capture.cluster.start", map[string]any{"iface": *f.captureIface, "filter": *f.captureFilter, "seconds": secs}), &job)
	fmt.Fprintf(os.Stderr, "capturing on %s on every node for %d s ...\n", *f.captureIface, secs)
	for i := 0; i < secs+60; i++ {
		time.Sleep(time.Second)
		var st CaptureJob
		decode(op(sock, "capture.cluster.status", nil), &st)
		if !st.Done {
			continue
		}
		fmt.Printf("%-24s %-8s %8s %10s  %s\n", "NODE", "RESULT", "KEPT", "BYTES", "")
		for _, n := range st.Nodes {
			name := n.Name
			if n.Self {
				name += " *"
			}
			fmt.Printf("%-24s %-8s %8d %10d  %s\n", name, n.Status, n.Kept, n.Bytes, n.Error)
		}
		if !st.Ready {
			fatalf("%s", orDefault(st.Error, "nothing to save"))
		}
		var b struct {
			TGZ []byte `json:"tgz"`
		}
		decode(op(sock, "capture.cluster.bundle", nil), &b)
		if err := os.WriteFile(*f.captureFile, b.TGZ, 0o644); err != nil {
			fatal(err)
		}
		fmt.Printf("written to %s\n", *f.captureFile)
		return
	}
	fatalf("the cluster capture did not finish")
}

// showVmacTest runs the virtual-MAC test on this node and prints what it found.
func showVmacTest(sock string, group int) {
	var rs []VmacResult
	decode(op(sock, "vmac.test", map[string]int{"group": group}), &rs)
	for _, r := range rs {
		fmt.Printf("Gateway %d: %s — %s\n", r.GroupID, r.Verdict, r.Detail)
		for _, f := range r.Families {
			fmt.Printf("    %s: %s\n", f.AF, f.Detail)
		}
		if r.Verdict == vmacNotDelivered {
			fmt.Printf("    To run without virtual MACs on every node: ddgw --canvas-set gateway --group %d --real-macs on\n", r.GroupID)
		}
	}
}

// runTshoot writes the troubleshooting bundle of this node (or of every node) to a .tgz.
func runTshoot(sock string, all, capture bool, file string) {
	cmd := "tshoot.node"
	if all {
		cmd = "tshoot.cluster"
	}
	if file == "" {
		file = "ddgw-tshoot-" + selfHost() + "-" + time.Now().Format("20060102-150405") + ".tgz"
	}
	fmt.Fprintln(os.Stderr, "collecting (up to a minute)…")
	var b struct {
		TGZ []byte `json:"tgz"`
	}
	decode(op(sock, cmd, map[string]any{"enabled": capture}), &b)
	if err := os.WriteFile(file, b.TGZ, 0o600); err != nil {
		fatal(err)
	}
	fmt.Printf("written to %s (%d bytes); secrets are removed, README.txt lists what is in it\n", file, len(b.TGZ))
}

// runScan asks this node for an nmap of addr and prints the report when it is done.
func runScan(sock, addr string) {
	var j ScanJob
	decode(op(sock, "scan.start", map[string]string{"client": addr}), &j)
	fmt.Fprintf(os.Stderr, "scanning %s with nmap…\n", j.Addr)
	for j.State == "running" {
		time.Sleep(2 * time.Second)
		decode(op(sock, "scan.get", map[string]string{"client": addr}), &j)
	}
	fmt.Println(j.Text)
	if j.State == "error" {
		os.Exit(1)
	}
}
