// ddgw — DNS Distributed Gateway: a Distributed Gateway Load Balancing Protocol
// daemon with a built-in DNS proxy that serves the VIP and forwards to the
// fastest healthy upstream.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const usageText = `ddgw — DNS Distributed Gateway (Distributed Gateway Load Balancing Protocol + DNS proxy)
Run as a systemd service or directly. All options are runtime-configurable
and persisted to /var/lib/ddgw/ddgw.conf.

Usage:
    ddgw [options]
    ddgw ?|-h|--help
    ddgw --configure       interactive configuration wizard
    ddgw --show-config     print current configuration

Options:
  --log-level LEVEL     debug|info|warning|error (default: info)
  --configure           Interactive configuration wizard (edits all groups)
  --show-config         Print current configuration and exit
  --show-neighbors      Print live neighbor table and exit
  --show-gateways       Print all nodes per group with role (AGC/AFN) and exit
  --show-dns            Print DNS upstream pool(s): health and latency ranking
  --canvas              Print the gateway diagram as a tree with live status
  --version             Print the release number and exit
  --assert-agc          Make this node the active gateway (AGC) for all groups
  --config FILE         Config file path (default: /var/lib/ddgw/ddgw.conf)
  --status-socket PATH  Status socket path (default: /run/ddgw/ddgw.sock)
  --state-dir DIR       Where history, cluster identity, certificates and update
                        data live (default: /var/lib/ddgw)

Environment of the daemon:
  DDGW_MEMORY_LIMIT_PERCENT  memory use (1-99, default 85) at which the memory guard drops the
                        oldest statistics, host history and cached answers

Management commands (talk to the running daemon; root only; every one is also in
the web GUI):
  Topology         --canvas-add gateway --vip ADDR/PREFIX [--vip6 ADDR/PREFIX] [--group N]
                                        [--interface IF] [--label NAME] [--anycast ADDR[,ADDR]] [--ecs on]
                                        [--server ADDR --name DOMAIN [--type T]]
                   --canvas-add server  --group N --server ADDR --name DOMAIN [--type T] [--label NAME]
                   --canvas-add domain  --group N --server ADDR --name DOMAIN [--type T]
                   --canvas-add fallback --group N --server ADDR   (used only while every server of the gateway is down)
                   --canvas-add anycast --group N --address ADDR   (one anycast address; see --anycast)
                   --canvas-set gateway --group N [--vip A/P] [--vip6 A/P] [--interface IF]
                                        [--label NAME|-]   (a name shown under the address; - clears)
                                        [--anycast ADDR[,ADDR]|-]   (extra addresses of ANY subnet, held on lo
                                         by every running node while its DNS answers, for BGP; - clears)
                                        [--real-macs on|off]   (run without virtual MACs on every node)
                                        [--ecs on|off] [--ecs-v4 BITS] [--ecs-v6 BITS]
                                        [--spread on|off] [--spread-band PCT] [--down-percent PCT]
                                        [--fail-threshold N] [--max-attempts N] [--latency-alpha A]
                                          (this gateway's own load balancing; --lb settings goes back to
                                           following Settings, which is the default)
                   --tshoot [--all-nodes] [--tshoot-file F.tgz]
                                         (a troubleshooting bundle for support: logs, configuration, gateway, DNS, BGP and
                                         anycast state, addresses, routes, neighbors, firewall, FRR, service status, host
                                         numbers, 8 s of ARP / neighbor-discovery traffic per gateway interface, a goroutine dump — of
                                         this node, or of every node with --all-nodes. Passwords, the gateway key, tokens
                                         and join codes are removed. kill -USR1 on a daemon that is up but not answering
                                         writes the goroutine dump to the journal.)
                   --test-vmac [--group N]   (can clients' replies reach this node through virtual MACs? sends an ARP/NS
                                         probe from a throwaway virtual MAC and listens for the answer; changes nothing.
                                         When it says "not delivered": --canvas-set gateway --group N --real-macs on)
                   --canvas-set server  --group N --server ADDR --label NAME|-   (name shown instead of the
                                         address on the Topology page; - clears)
                   --canvas-pause|--canvas-resume gateway --group N   (this node stops/starts serving it)
                   --canvas-pause|--canvas-resume server --group N --server ADDR  (no queries/probes)
                   --canvas-del node --group N --node NODE   (remove a cluster node from the gateway: it stops serving it, on every
                                         node's view; NODE is its address or host name, as --cluster-status lists them)
                   --canvas-add node --group N --node NODE   (put it back; a node that joins starts out removed from every gateway)
                   --dns-lookup IP|NAME   (the reverse name of an address, or the address of a name, as the Add DNS server form fills them in)
                   --server-stats ADDR [--stats-range 1h|1d|7d]   (how one DNS server has answered: latency and loss)
                   --server-stats ADDR --name DOMAIN [--type A]   (one monitored domain on that server)
                   --gateway-stats GROUP [--stats-range 1h|1d|7d]  (one gateway: client latency, errors, traffic, cache, availability)
                   --node-pause | --node-resume | --node-status   (this whole node stops/starts serving)
                   --canvas-move server  --group N --server ADDR --to N   (its place in the row, 1 = leftmost; the drag in the drawing)
                   --canvas-move domain  --group N --server ADDR --name DOMAIN --to N   (its place under the server, 1 = top)
                   --canvas-move anycast --group N --address ADDR --to N   (its place in the list, 1 = top)
                   --canvas-pause|--canvas-resume anycast --group N --address ADDR --scope node|all
                                        (stop/start announcing one anycast address on this node only, or on every node)
                   --canvas-del gateway --group N          (removes everything under it)
                   --canvas-del server  --group N --server ADDR  (and its domains)
                   --canvas-del domain  --group N --server ADDR --name DOMAIN
                   --canvas-del fallback --group N --server ADDR
                   --canvas-del anycast --group N --address ADDR
  Config history   --versions  --version-show ID  --version-diff A[..B]
                   --version-snapshot [--note TEXT]  --version-restore ID
                   --version-export [ID]  --config-import FILE
  GUI certificate  --tls-status  --tls-install --cert-file F --key-file F
                   --tls-csr --cn NAME [--san NAME_OR_IP,...]
                   --tls-revert  --tls-regenerate
  BGP (FRR)        --bgp   (this node's settings, announced addresses, neighbor and BFD state)
                   --asn N|off [--router-id A.B.C.D|-]     (BGP runs while an AS is set)
                   [--keepalive S] [--hold S] [--as-prepend on|off]   (on: the local AS three more times on every announcement)
                   --bgp-neighbor-add ADDR --remote-as N [--description T] [--password P] [--multihop N]
                   --bgp-neighbor-del ADDR
                   --bgp-disable | --bgp-enable   (stop / restart BGP on this node, settings kept)
                   --bgp-neighbor-disable ADDR | --bgp-neighbor-enable ADDR
                                                 (per node; ddgw then owns /etc/frr/frr.conf; BFD is always on)
  Cluster          --cluster-status  --cluster-token  --cluster-join CODE
                   --cluster-promote  --cluster-remove ADDR
                   --cluster-unremove ADDR  --cluster-leave  --cluster-sync
                   (GUI: the Node menu, top right, configures any member)
  Upgrade          --update-status  --update-history  --update-upload FILE
                   --update-apply  --update-push all|ADDR,...
                   --update-cancel all|ADDR,...  --update-auto on|off
  Statistics       --stats [--stats-range 1h|1d|7d|30d]
                   [--stats-rcode nxdomain|servfail|refused|noerror|other|update]
                   [--stats-client ADDR | --stats-domain NAME]
                   [--all-nodes]   (every cluster node's numbers added together)
                   --stats-clear [--all-nodes]   (forget the counts and top lists, here or on every node)
                   --scan ADDR   (nmap of a client, run from this node; needs the nmap package)
                   --whois NAME   (registration data of a domain, asked of its registry from this node)
                   --dns-updates   (the recent dynamic DNS updates: who, which zone, what, the primary's answer)
  Capture          --capture IFACE [--capture-seconds N] [--capture-filter EXPR] [--capture-file FILE] [--all-nodes]
                   --capture-interfaces   (a packet capture on this node, printed or written as .pcap;
                                           --all-nodes: every cluster node at once, one .pcap each in a .tgz)
  Host             --host [--host-range 1h|1d|7d|30d] [--all-nodes]   (CPU, memory, disk and network use; the cluster as one)
  Log              --log [--log-min debug|info|warn|error] [--log-grep WORDS]
                   [--log-since 15m|6h|2d] [--log-lines N]   (the whole log if no --log-lines)
  Users            --users   (the accounts that may sign in to the web GUI)
                   --user-add NAME [--expires YYYY-MM-DD]   (asks for the password)
                   --user-passwd NAME   --user-del NAME
                   --user-grant NAME   (an account that already exists joins the group)   --user-revoke NAME
                   --user-expiry NAME --expires YYYY-MM-DD|never
  Power            --power restart|shutdown [--in MINUTES | --at HH:MM]
                   --power cancel  --power status
                   (restarts or shuts down the whole host; refused while it is the only
                   node serving a gateway unless --yes)
  Modifiers        --yes (no confirmation)  --no-wait (do not wait for a restart)

Per-group settings are managed via --configure or by editing the config
file directly. Each group entry supports:
  group_id, interface, vip4, vip6, priority, lb_method, weight,
  hello_ms, hold_ms, preempt, max_afns, key, neighbors, real_macs, dns

  vip4: IPv4 VIP with prefix, e.g. 10.0.0.1/24  (required when no vip6)
  vip6: IPv6 VIP with prefix, e.g. 2001:db8::1/64 (required when no vip4)
  neighbors: list of peer IP addresses for unicast mode (optional).
    When present, ddgw sends hellos directly to each neighbor via unicast
    instead of multicast — required when peers are on different VNIs or
    separated by a routed boundary.
  Every ACTIVE/FORWARD node answers DNS on the VIP (there is no switch for it; an old
  "dns_proxy" key is accepted and ignored).

DNS proxy ("dns" block at the top level of the config):
  servers            upstream resolvers, e.g. ["10.0.0.53", "10.0.1.53:5353", "tls://dns.example.com", "https://dns.example.com/dns-query"]
                     (tls://host[:853] is DNS over TLS, https://host[:443][/dns-query] is DNS over HTTPS;
                     the certificate must match the host)
  tls_insecure       true accepts any certificate from tls:// and https:// servers (default false; not safe)
  queries            probe queries: "example.com" or {"name":..., "type":"AAAA"}
  down_percent       a server is down when at least this % of its probe queries fail (default
                     100); with fewer failing it stays in use and shows as degraded. The old
                     "require" key is read and ignored
  listen_port        port served on the VIP, UDP+TCP (default 53)
  doh_port           serve DNS over HTTPS to clients at /dns-query on this TCP port, e.g. 443 (default 0 = off);
                     the certificate is the GUI's
  dot_port           serve DNS over TLS to clients on this TCP port, e.g. 853 (default 0 = off);
                     the certificate is the GUI's (see --tls-status)
  allowed_clients    networks that may use the proxy, e.g. ["10.0.0.0/8", "192.168.1.5"]; empty (default) =
                     everyone. Anyone else is answered REFUSED, dynamic updates included; the node itself always may
  client_rate        queries per second one client (an IPv4 address or an IPv6 /64) may send; 0 (default) = no limit
  client_burst       how many it may send at once before client_rate applies; 0 (default) = twice the rate, at least 10
  client_action      what a client over its rate gets: drop (default; nothing is sent), truncate (UDP: a short answer
                     with the TC bit, so a real client retries over TCP) or refused. Over TCP/DoT/DoH: always REFUSED
  client_exempt      networks never rate limited (still subject to allowed_clients)
  sortlist_on        true (default) uses sortlist; false keeps the list but does not sort
  sortlist           per-client-network order of the A/AAAA records in answers, one entry each:
                     "client-network: preferred-network, ...", e.g. ["10.1.0.0/16: 10.1.0.0/16, 10.0.0.0/8"];
                     "any" matches every client; unmatched addresses keep their order after the preferred ones.
                     A plain network ("10.20.0.0/16") is for every client; the one the client is in comes first
  ecs                true (default) passes the client's network to the servers (EDNS Client
                     Subnet, RFC 7871); ecs_prefix4 (24) / ecs_prefix6 (56) bits only; a server that
                     refuses it (FORMERR/REFUSED) is asked again without and then left alone
  forward_updates    true (default) forwards dynamic DNS updates (RFC 2136) to the primary
                     server named in the zone's SOA record, unchanged; false answers REFUSED
  spread             true (default) shares queries out: servers within spread_band (20) percent
                     of the fastest one's latency take turns (round-robin); false = fastest first
  cache              true (default) answers repeated queries from memory while the records'
                     TTLs allow (negative answers per their SOA); cache_entries (10000) is the
                     most answers kept, cache_max_ttl (3600) the most seconds one is kept
  probe_interval_ms  default 5000     probe_timeout_ms  default 1000
  query_timeout_ms   default 1500     max_attempts      default 3
  fail_threshold     consecutive failures before a server is dropped (2)
  latency_alpha      EWMA smoothing for response latency (default 0.3)
  server_queries     optional per-server probe queries: {"8.8.8.8": ["google.com"]}
  server_names       optional names shown under the addresses: {"8.8.8.8": "google-a"}
A server may also be a host name. A group can have its own pool: put the same
keys in a "dns" block inside the group (that is what the Topology page writes); groups
without one share the top-level block. A server receives traffic only while its
probe queries succeed; eligible servers are tried in turns within the spread band of the fastest (or fastest-first with spread off) (live query
latency feeds the same estimate). An explicit "groups": [] means no gateways.

Web GUI ("web" block): everything above is also available in a browser.
  Served over HTTPS (default port 53853). Log in with a system account that
  is a member of the ddgw group; passwords are checked through PAM (service
  "ddgw", see contrib/pam.d/). The page follows the system light/dark theme.
  The GUI is always on.      listen   default ":53853"
  group                default "ddgw"      pam_service  default "ddgw"
  session_idle_minutes default 30
  max_failed_logins    default 3   wrong passwords ...
  failed_login_window_minutes default 1    ... within this window lock the
  lockout_minutes      default 15  address (and address with user name) out for this long
  min_password_length  default 8   fewest characters of a password set on the Users page
  The certificate is managed with --tls-* / Configure ▸ Web GUI (the old
  cert_file/key_file settings still work and take precedence).
  Pages: Topology (--canvas, --canvas-add/-del: draw gateways, DNS servers and
  the domains to test as circle, squares and trapezoids), Gateways (--show-gateways, --show-neighbors),
  Cluster monitor (--cluster-status), DNS (--show-dns), Configure (--show-config,
  --configure: form), History (--versions...), Certificate
  (--tls-...), Node (--assert-agc, --node-pause..., --power), Cluster (--cluster-...), Upgrade (--update-...); the ? at the top right
  opens help for the page. Version is in the header. A certificate installed through the GUI/CLI is
  picked up without restarting the GUI.

Config history: every change (GUI, CLI, cluster sync or a hand edit of the
  file) is stored as a version in <state-dir>/versions (newest 200 kept). Any
  version can be compared, downloaded or restored; secrets are hidden in summaries.

Cluster ("cluster" block; management only, independent of the AGC/AFN election):
  listen             default ":53854" (TCP)
  self               address other nodes use to reach this one, e.g. "10.0.0.5:53854"
  sync_interval_sec  default 5
  One node is the primary, the others replicas. Settings shared by the cluster
  (the DNS block, group VIPs/keys/timers, ...) are edited anywhere and applied
  on the primary; interface, priority, weight and the web/cluster blocks stay
  per node. Create a join code on a member (--cluster-token), use it on the new
  node (--cluster-join). Nodes pin each other's certificates and sign requests;
  a code works once for an hour. Promotion is explicit (--cluster-promote).
  The primary's GUI certificate is replicated to every member.

Updates: upload ddgw_vN.tgz (or .zip) on any node (--update-upload); the node
  checks it, keeps the source, and each member that is asked to update (or all of
  them with --update-auto on) pulls it from a peer, builds it with its own Go
  toolchain (gcc + PAM headers needed), swaps the binary and restarts - one node
  at a time. A new binary that fails to start is rolled back automatically.

Live reload: the daemon watches the config file via inotify.
  Edit and save ddgw.conf — changes apply within ~50ms, no restart needed.
  Live fields (no engine restart): priority, lb_method, weight,
    hello_ms, hold_ms, preempt, max_afns, log_level, the whole "dns" block,
    web group/pam_service/session timeout, web cert/key (only listen restarts the GUI)
  Restart fields (engine restarts, brief per-group disruption):
    interface, vip4, vip6, group_id, key, neighbors, real_macs
  Adding/removing groups: new groups start, removed groups stop cleanly.`

// the cgroup CPU limit applied at start-up (see cpulimit.go)
var (
	cpuQuota float64
	cpuProcs int
)

func main() {
	cpuQuota, cpuProcs = limitProcsToCgroup() // logged by the daemon only, not by every CLI command
	if len(os.Args) > 1 && os.Args[1] == "?" {
		fmt.Println(usageText)
		return
	}
	fs := flag.NewFlagSet("ddgw", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		configure    = fs.Bool("configure", false, "")
		showConfig   = fs.Bool("show-config", false, "")
		showNeigh    = fs.Bool("show-neighbors", false, "")
		showGateways = fs.Bool("show-gateways", false, "")
		showDNS      = fs.Bool("show-dns", false, "")
		showVersion  = fs.Bool("version", false, "")
		assertAGC    = fs.Bool("assert-agc", false, "")
		conf         = fs.String("config", confPath, "")
		sock         = fs.String("status-socket", statusSocket, "")
		level        = fs.String("log-level", "", "")
		stateDir     = fs.String("state-dir", defaultStateDir, "")
		cli          = registerCLIFlags(fs)
	)
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(usageText)
			return
		}
		fmt.Fprintln(os.Stderr, "ddgw:", err)
		fmt.Fprintln(os.Stderr, "try: ddgw --help")
		os.Exit(2)
	}

	if *showVersion {
		fmt.Println("ddgw v" + version())
		return
	}

	// pre-v15 installs kept the config in /etc/liras: bring it over once, and never
	// use that location again, even if an old service unit still names it
	if *conf == legacyConfPath {
		fmt.Fprintf(os.Stderr, "ddgw: %s is no longer used; using %s (update the service unit's --config, or re-run install.sh)\n", legacyConfPath, confPath)
	}
	*conf = canonicalConf(*conf)
	if *conf == confPath && os.Geteuid() == 0 {
		if moved, err := migrateLegacyConfig(confPath, legacyConfPath); err != nil {
			fmt.Fprintf(os.Stderr, "ddgw: could not move %s to %s: %v\n", legacyConfPath, confPath, err)
		} else if moved {
			fmt.Fprintf(os.Stderr, "ddgw: moved your configuration from %s to %s and removed the old files\n",
				legacyConfPath, confPath)
		}
	}

	switch {
	case *configure:
		if err := interactiveConfigure(*conf); err != nil {
			fatal(err)
		}
		return
	case *showNeigh:
		showNeighbors(*sock)
		return
	case *showGateways:
		showGatewayTable(*sock)
		return
	case *showDNS:
		showDNSTable(*sock)
		return
	case cli.run(*sock):
		return
	case *assertAGC:
		res := mustStatus(*sock, "assert-agc")
		if ok, _ := res["ok"].(bool); !ok {
			fmt.Printf("assert-agc failed: %v\n", res["error"])
			os.Exit(1)
		}
		for _, m := range res["messages"].([]any) {
			fmt.Println(m)
		}
		return
	}

	_, statErr := os.Stat(*conf)
	configExists := statErr == nil
	created := false
	dc, err := loadConfig(*conf)
	if err != nil {
		fatalf("Configuration error: %v", err)
	}
	if *level != "" {
		dc.LogLevel = *level
	}
	if *showConfig {
		b, _ := json.MarshalIndent(dc, "", "  ")
		fmt.Println(string(b))
		return
	}

	if configExists {
		if err := dc.Validate(); err != nil {
			fatalf("Configuration error: %v", err)
		}
	} else {
		// Absent config: write one with the defaults and no gateways, then start.
		dc.Groups = []GroupConfig{}
		if err := dc.save(*conf); err != nil {
			fatalf("cannot create %s: %v", *conf, err)
		}
		created = true
	}

	setupLogging(dc.LogLevel)
	startLogFile(*stateDir)
	if !created {
		infof("ddgw starting — %d group(s)", len(dc.Groups))
	} else {
		infof("ddgw starting — config file %s not found, created it with the defaults", *conf)
	}
	if err := run(dc, *conf, *sock, *stateDir); err != nil {
		fatal(err)
	}
}

func run(dc *DaemonConfig, confFile, sockPath, stateDir string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// kill -USR1 writes every goroutine's stack to the log (the journal): the way to see what a daemon that is up but not
	// answering is stuck on, when its own pages and the troubleshooting bundle cannot be reached
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-usr1:
				warnf("SIGUSR1: every goroutine's stack is written to standard error (journalctl -u ddgw)")
				fmt.Fprintf(os.Stderr, "--- goroutine dump (SIGUSR1) %s ---\n%s--- end of goroutine dump ---\n", time.Now().Format(time.RFC3339), goroutineDump())
			}
		}
	}()

	// the path of the running binary, taken before any update can replace it
	exe, _ := os.Executable()
	exe = strings.TrimSuffix(exe, " (deleted)")

	mg, err := NewMgmt(confFile, stateDir, dc)
	if err != nil {
		return err
	}
	mg.upd.exePath = func() (string, error) { return exe, nil }
	if mg.upd.GuardOnStart() { // the previous update never stayed up: back to the old binary
		warnf("restarting into the restored previous version")
		return syscall.Exec(exe, os.Args, os.Environ())
	}
	if n := mg.upd.RolledBackNotice(); n != "" {
		warnf("update: %s", n)
	}
	var reexec atomic.Bool
	mg.restartFn = func() { reexec.Store(true); stop() }

	// the saved statistics come back before any query is counted; the last save happens once the DNS frontends have stopped
	savedStats := startPersistence(stateDir, ctx.Done())

	dotCertificate = mg.certs.GetCertificate
	sup := NewSupervisor(ctx, dc)
	sup.StartAll()
	memguard.caches = sup.allCaches
	qstats.ptrVia = sup.ptrViaPools
	addrVia = sup.addrViaPools
	go memguard.Run(ctx.Done())

	st := NewStatusServer(sockPath, sup)
	st.mg = mg
	mg.gwIPsFn = func() []string {
		var out []string
		seen := map[string]bool{}
		for _, r := range st.snapshot() {
			if r.Local && r.PeerIP != "" && !seen[r.PeerIP] {
				seen[r.PeerIP] = true
				out = append(out, r.PeerIP)
			}
		}
		sort.Strings(out)
		return out
	}
	mg.gwFn = func() []GwState {
		cfg, snap := sup.config(), st.snapshot()
		gs := gatewayStates(cfg, snap)
		// each gateway's health as this node's own drawing colours it, so the other nodes can show it too
		groups := buildCanvas(cfg, snap, sup.poolList())
		markWarming(groups, sup.warmingGroups())
		markOffnet(groups, sup.offnetGroups())
		off := sup.offnetGroups()
		for i := range gs {
			_, gs[i].Offnet = off[gs[i].GroupID]
			for _, g := range cfg.Groups { // removed from the gateway: it can never serve it either
				if g.GroupID == gs[i].GroupID && containsStr(g.ExcludedNodes, localNodeID()) {
					gs[i].Offnet = true
				}
			}
			for _, g := range groups {
				if g.GroupID == gs[i].GroupID {
					gs[i].Health, gs[i].HealthWhy = g.Status, g.Detail
				}
			}
		}
		return gs
	}
	mg.pausedFn = func() bool { return sup.config().NodePaused }
	mg.anycastFn = sup.AllAnycastStates
	mg.poolOf = sup.poolFor
	mg.tshootFn = func() map[string]any {
		return map[string]any{"canvas": st.canvasGroups(), "dns-pools": st.dnsStatus(), "gateway-snapshot": st.snapshot()}
	}
	if err := st.Start(); err != nil {
		warnf("status socket unavailable (%s): %v — --show-* commands will not work", sockPath, err)
	}

	var web *WebServer
	reload := func() error {
		nu, err := loadConfig(confFile)
		if err == nil {
			err = nu.Validate()
		}
		if err != nil {
			warnf("Config reload skipped — %v", err)
			return err
		}
		infof("Config file changed — reloading")
		sup.Reload(nu)
		mg.OnConfigLoaded(nu)
		if web != nil {
			go web.Apply(nu.Web) // async: a listener restart must not wait on the request that triggered it
		}
		return nil
	}
	mg.reloadFn = reload
	web = NewWebServer(mg, st, sysAuth{})
	mg.webH = web.Handler()
	web.Apply(dc.Web)
	defer web.Stop()
	mg.OnConfigLoaded(dc) // first history entry, cluster listener
	defer mg.cl.Stop()
	go mg.cl.Run(ctx)
	go mg.warmCaches(ctx, func() []int {
		var gids []int
		for _, g := range sup.config().Groups {
			if !g.Paused {
				gids = append(gids, g.GroupID)
			}
		}
		return gids
	})
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(bootConfirmAfter):
			mg.upd.GuardConfirm(mg.cl.selfAddrForEvents())
		}
	}()
	go certExpiryWatch(ctx, mg)
	w := NewConfigWatcher(confFile, func() { reload() })
	if err := w.Start(ctx); err != nil {
		warnf("config watch unavailable: %v — edits need a restart", err)
	}

	infof("ddgw running — %d group(s), watching %s", len(dc.Groups), confFile)
	if cpuProcs > 0 {
		infof("CPU limit of %.2f in this cgroup: using %d scheduler threads instead of %d", cpuQuota, cpuProcs, runtime.NumCPU())
	}
	<-ctx.Done()
	infof("Shutdown signal received")
	st.Stop()
	done := make(chan struct{})
	go func() { sup.StopAll(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		warnf("shutdown timed out")
	}
	savedStats()
	if reexec.Load() {
		web.Stop()
		mg.cl.Stop()
		infof("re-executing %s", exe)
		return syscall.Exec(exe, os.Args, os.Environ())
	}
	return nil
}

// certExpiryWatch logs a warning at start and then daily when the GUI
// certificate has expired or is about to.
func certExpiryWatch(ctx context.Context, mg *Mgmt) {
	for {
		if w := mg.certs.ExpiryWarning(); w != "" {
			warnf("web: %s — replace it under Configure ▸ Web GUI or with ddgw --tls-install", w)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}

func fatal(err error)           { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
func fatalf(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(1) }

// ── CLI views ────────────────────────────────────────────────────────────────

func mustStatus(sock, cmd string) map[string]any {
	res, err := statusRequest(sock, map[string]any{"cmd": cmd})
	if err != nil {
		fatal(err)
	}
	return res
}

func decodeData(res map[string]any, v any) {
	b, _ := json.Marshal(res["data"])
	json.Unmarshal(b, v)
}

func showNeighbors(sock string) {
	var rows []SnapshotRow
	decodeData(mustStatus(sock, "snapshot"), &rows)
	peers := peersOnly(rows)
	if len(peers) == 0 {
		fmt.Println("No neighbors.")
		return
	}
	const f = "%-8s %-5s %-42s %-5s %-5s %-7s %-8s %s\n"
	fmt.Printf(f, "GROUP", "AF", "PEER IP", "PRI", "AFN", "WEIGHT", "AGE(ms)", "STATE")
	fmt.Println(strings.Repeat("-", 90))
	for _, p := range peers {
		state := p.State
		if p.Preempt {
			state += " preempt"
		}
		fmt.Printf(f, itoa(p.GroupID), p.AF, p.PeerIP, itoa(p.Priority), itoa(p.AfnID),
			itoa(p.Weight), strconv.FormatInt(p.AgeMS, 10), state)
	}
}

func showGatewayTable(sock string) {
	var rows []SnapshotRow
	res := mustStatus(sock, "snapshot")
	decodeData(res, &rows)
	groups := buildGateways(rows)
	ipNames := map[string]string{}
	if nm, ok := res["names"].(map[string]any); ok {
		for ip, n := range nm {
			ipNames[ip], _ = n.(string)
		}
	}
	nameMembers(groups, ipNames)
	if len(groups) == 0 {
		fmt.Println("No gateway groups.")
		return
	}
	names := map[int]string{}
	if res, err := statusRequestTimeout(sock, map[string]any{"cmd": "canvas"}, 10*time.Second); err == nil {
		var cg []CanvasGateway
		decodeData(res, &cg)
		for _, c := range cg {
			names[c.GroupID] = c.Name
		}
	}
	for _, g := range groups {
		title := fmt.Sprintf("Group %d", g.GroupID)
		if n := names[g.GroupID]; n != "" {
			title = fmt.Sprintf("%s (group %d)", n, g.GroupID)
		}
		fmt.Printf("%s  VIP %s\n", title, g.VIP)
		fmt.Printf("%-18s %-40s %-5s %-5s %-7s %-8s %-10s %-9s %s\n", "NODE NAME", "NODE IP", "PRI", "SLOT", "WEIGHT", "ROLE", "STATE", "AGE(ms)", "VMAC")
		fmt.Println(strings.Repeat("-", 122))
		for _, m := range g.Members {
			age, flag := strconv.FormatInt(m.AgeMS, 10), ""
			if m.Local {
				age, flag = "(local)", " *"
			}
			fmt.Printf("%-18s %-40s %-5d %-5d %-7d %-8s %-10s %-9s %s\n", orDefault(m.Name, "-")+flag, m.IP, m.Priority,
				m.Slot, m.Weight, m.Role, strings.ToUpper(m.State), age, orDefault(m.VMAC, "-"))
		}
		fmt.Println()
	}
	fmt.Println("* = this node")
}

func showDNSTable(sock string) {
	res := mustStatus(sock, "dns")
	if ok, _ := res["ok"].(bool); !ok {
		fmt.Println(res["error"])
		os.Exit(1)
	}
	var d struct {
		Pools []struct {
			Key     int          `json:"key"`
			Name    string       `json:"name"`
			Groups  []int        `json:"groups"`
			Servers []ServerStat `json:"servers"`
			UsingFB bool         `json:"using_fallback"`
			DownPct int          `json:"down_percent"`
			Probes  int          `json:"probes"`
			ECS     bool         `json:"ecs"`
			ECSSent uint64       `json:"ecs_sent"`
			Cache   cacheStats   `json:"cache"`
			Spread  bool         `json:"spread"`
			Insec   bool         `json:"tls_insecure"`
			DoT     int          `json:"dot_port"`
			DoH     int          `json:"doh_port"`
			Band    int          `json:"spread_band"`
			Allowed int          `json:"allowed_clients"`
			Rate    int          `json:"client_rate"`
			Burst   int          `json:"client_burst"`
			Action  string       `json:"client_action"`
			Denied  uint64       `json:"denied"`
			Limited uint64       `json:"limited"`
		} `json:"pools"`
		Listeners []string `json:"listeners"`
		Queries   uint64   `json:"queries"`
		Answered  uint64   `json:"answered"`
		ServFail  uint64   `json:"servfail"`
	}
	decodeData(res, &d)
	if len(d.Listeners) == 0 {
		fmt.Println("Listening: (not active on this node)")
	} else {
		fmt.Println("Listening: " + strings.Join(d.Listeners, ", "))
	}
	fmt.Printf("Queries: %d  answered: %d  SERVFAIL: %d\n", d.Queries, d.Answered, d.ServFail)
	const f = "%-4s %-16s %-28s %-6s %-10s %-10s %-8s %-8s %-8s %s\n"
	for _, p := range d.Pools {
		who := "shared pool"
		if p.Key != 0 {
			who = "gateway " + itoa(p.Key)
			if p.Name != "" {
				who += " (" + p.Name + ")"
			}
		}
		var gl []string
		for _, g := range p.Groups {
			gl = append(gl, itoa(g))
		}
		fmt.Printf("\n%s (used by group %s): %d probe(s) in total, a server is down when %d%% of its tests fail\n", who, strings.Join(gl, ","), p.Probes, p.DownPct)
		if p.ECS {
			fmt.Printf("client subnet (ECS): on, attached to %d forwarded queries\n", p.ECSSent)
		} else {
			fmt.Println("client subnet (ECS): off (servers see this node's address; turn it on with ecs: true)")
		}
		if p.Allowed > 0 {
			fmt.Printf("allowed clients: %d network(s) listed (allowed_clients); %d queries from other addresses were refused\n", p.Allowed, p.Denied)
		} else {
			fmt.Println("allowed clients: everyone (allowed_clients is empty)")
		}
		if p.Rate > 0 {
			fmt.Printf("client rate limit: %d queries/s per client (burst %d), over the limit: %s; %d queries turned away\n", p.Rate, p.Burst, p.Action, p.Limited)
		} else {
			fmt.Println("client rate limit: off")
		}
		if p.DoH != 0 {
			fmt.Printf("DNS over HTTPS: served to clients on port %d at /dns-query (the GUI certificate)\n", p.DoH)
		}
		if p.DoT != 0 {
			fmt.Printf("DNS over TLS: served to clients on port %d (the GUI certificate)\n", p.DoT)
		}
		if p.Insec {
			fmt.Println("tls:// and https:// servers: certificates are NOT checked (tls_insecure) — anyone on the path could answer")
		}
		if p.Spread {
			fmt.Printf("spread: on, servers within %d%% of the fastest take turns (see the Served column)\n", p.Band)
		} else {
			fmt.Println("spread: off (the fastest server gets the queries; the others are fallbacks)")
		}
		if c := p.Cache; c.On {
			total := c.Hits + c.Misses
			rate := "–"
			if total > 0 {
				rate = fmt.Sprintf("%.1f%%", 100*float64(c.Hits)/float64(total))
			}
			fmt.Printf("answer cache: on, %d of %d entries, %d hits, %d misses (hit rate %s), %d not cacheable, %d evicted; answers kept at most %ds\n",
				c.Entries, c.Max, c.Hits, c.Misses, rate, c.Bypassed, c.Evicted, c.MaxTTL)
		} else {
			fmt.Println("answer cache: off (every query goes to an upstream server)")
		}
		if p.UsingFB {
			fmt.Println("no configured server is in service (down or paused): answering from the fallback servers")
		}
		fmt.Printf(f, "RANK", "SERVER NAME", "SERVER IP", "STATE", "EWMA(ms)", "LAST(ms)", "OK", "FAIL", "SERVED", "LAST ERROR")
		fmt.Println(strings.Repeat("-", 127))
		for _, s := range p.Servers {
			rank, state := "-", "down"
			if s.Healthy {
				rank, state = "-", "up"
				if s.Rank > 0 {
					rank = itoa(s.Rank)
				}
			}
			addr := s.Addr
			if s.Fallback {
				addr += " (fallback)"
			}
			fmt.Printf(f, rank, orDefault(s.Name, "-"), addr, state, fmt.Sprintf("%.2f", s.EWMAMS), fmt.Sprintf("%.2f", s.LastMS),
				strconv.FormatUint(s.Successes, 10), strconv.FormatUint(s.Failures, 10),
				strconv.FormatUint(s.Served, 10), s.LastError)
		}
	}
}

// ── Interactive configure ────────────────────────────────────────────────────

type prompter struct{ r *bufio.Reader }

func (p *prompter) ask(label, cur string) string {
	fmt.Printf("  %s [%s]: ", label, cur)
	s, _ := p.r.ReadString('\n')
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return cur
}

func (p *prompter) askInt(label string, cur int) int {
	for {
		v, err := strconv.Atoi(p.ask(label, strconv.Itoa(cur)))
		if err == nil {
			return v
		}
		fmt.Println("    please enter a number")
	}
}

func yes(s string) bool {
	switch strings.ToLower(s) {
	case "yes", "y", "true", "1":
		return true
	}
	return false
}

func yn(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func interactiveConfigure(path string) error {
	dc, err := loadConfig(path)
	if err != nil {
		return err
	}
	p := &prompter{bufio.NewReader(os.Stdin)}
	fmt.Println("Distributed Gateway Interactive Configuration")
	fmt.Printf("Config file: %s\nPress Enter to keep current value.\n\n", path)

	dc.LogLevel = p.ask("Log level (debug/info/warning/error)", dc.LogLevel)
	fmt.Printf("\nCurrently %d group(s) configured.\n", len(dc.Groups))
	n := p.askInt("How many groups?", len(dc.Groups))
	for len(dc.Groups) < n {
		g := newGatewayGroup()
		g.GroupID = len(dc.Groups) + 1
		dc.Groups = append(dc.Groups, g)
	}
	if n < len(dc.Groups) && n >= 0 {
		dc.Groups = dc.Groups[:n]
	}
	for i := range dc.Groups {
		g := &dc.Groups[i]
		fmt.Printf("\n  Group %d\n", i+1)
		g.GroupID = p.askInt("Group ID (1-255)", g.GroupID)
		g.Interface = p.ask("Interface", g.Interface)
		g.VIP4 = blankable(p.ask("IPv4 VIP/prefix (e.g. 10.0.0.1/24, '-' to disable)", orDash(g.VIP4)))
		g.VIP6 = blankable(p.ask("IPv6 VIP/prefix (e.g. 2001:db8::1/64, '-' to disable)", orDash(g.VIP6)))
		g.Priority = p.askInt("Priority (0-255)", g.Priority)
		g.HelloMS = p.askInt("Hello interval ms", g.HelloMS)
		g.HoldMS = p.askInt("Hold time ms", g.HoldMS)
		g.MaxAFNs = p.askInt("Max active forwarders (1-255)", g.MaxAFNs)
		fmt.Println("    LB methods: roundrobin, weighted, hostpinned, failover")
		for {
			m, err := parseLB(p.ask("LB method", g.LBMethod.String()))
			if err == nil {
				g.LBMethod = m
				break
			}
			fmt.Println("   ", err)
		}
		g.Weight = p.askInt("Weight (weighted mode)", g.Weight)
		g.Preempt = yes(p.ask("Preemption enabled? (yes/no)", yn(g.Preempt)))
		g.RealMACs = yes(p.ask("Use real MAC addresses instead of virtual MACs? (yes/no; for VMware port groups that are not promiscuous or clouds that allow one MAC per interface)", yn(g.RealMACs)))
		g.Key = p.ask("HMAC-SHA256 shared key", g.Key)
	}
	if len(dc.Groups) > 0 {
		fmt.Println("\n  DNS proxy")
		servers := p.ask("Upstream servers (comma separated)", strings.Join(dc.DNS.Servers, ","))
		dc.DNS.Servers = splitList(servers)
		var qs []string
		for _, q := range dc.DNS.Queries {
			qs = append(qs, q.Name+" "+q.Type)
		}
		queries := p.ask("Probe queries (comma separated, \"name[ TYPE]\")", strings.Join(qs, ","))
		dc.DNS.Queries = nil
		for _, q := range splitList(queries) {
			var dq DNSQuery
			b, _ := json.Marshal(q)
			if err := dq.UnmarshalJSON(b); err != nil {
				return err
			}
			dc.DNS.Queries = append(dc.DNS.Queries, dq)
		}
		dc.DNS.DownPercent = p.askInt("A server is down when this % of its probe queries fail", dc.DNS.DownPercent)
		dc.DNS.ListenPort = p.askInt("Listen port", dc.DNS.ListenPort)
	}
	if err := dc.Validate(); err != nil {
		return fmt.Errorf("not saved: %w", err)
	}
	if err := dc.save(path); err != nil {
		return err
	}
	fmt.Printf("\nConfiguration saved to %s\n", path)
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func blankable(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
