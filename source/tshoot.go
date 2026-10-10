package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"time"
)

// The troubleshooting bundle: everything about a node (and, through the cluster, every node) that helps to find out why
// something does not work, in one .tgz that can be attached to a ticket.  Read-only; secrets are removed before anything
// is put in it, and the bundle lists what it holds and what could not be collected.
//
//	ddgw-tshoot-<time>.tgz
//	  README.txt           what is in it, which nodes answered, what was removed
//	  <node>/…             one folder per node (see tshootNode)

const (
	tshootNodeMax  = 3 << 20          // one node's compressed bundle must fit the peer channel
	tshootCmdMax   = 512 << 10        // the most one command's output may add
	tshootCmdTime  = 8 * time.Second  // the longest any command may run
	tshootLogLines = 20000            // newest log lines
	tshootNodeTime = 60 * time.Second // the most one node may take
	tshootCapSecs  = 8                // seconds of ARP / neighbor-discovery capture per interface (when asked for)
	tshootRedacted = "<removed>"
)

// A key whose value is a secret: passwords, the gateway key, tokens, join codes, private keys.
var tshootSecretKey = regexp.MustCompile(`(?i)(^|_)(pass|passwd|password|secret|token|key|psk|private|cookie|csrf|code|auth)($|_)`)

// A secret written in free text (an FRR configuration, a command's output).
var tshootSecretText = regexp.MustCompile(`(?i)\b(password|passwd|secret|md5|community|key|auth-key)([ =:]+)(?:[0-9] )?(\S+)`)

// redactJSON returns v with the value of every secret key replaced, however deep.
func redactJSON(v any) (any, int) {
	n := 0
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			for k, val := range t {
				if tshootSecretKey.MatchString(k) {
					switch tv := val.(type) {
					case nil:
					case string:
						if tv != "" {
							t[k], n = tshootRedacted, n+1
						}
					case bool, float64:
					default:
						t[k], n = tshootRedacted, n+1
					}
					continue
				}
				t[k] = walk(val)
			}
			return t
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
			return t
		}
		return x
	}
	return walk(v), n
}

// redactText removes the secrets written in free text.
func redactText(s string) (string, int) {
	n := 0
	out := tshootSecretText.ReplaceAllStringFunc(s, func(m string) string {
		n++
		p := tshootSecretText.FindStringSubmatch(m)
		return p[1] + p[2] + tshootRedacted
	})
	return out, n
}

// tshootBundle collects one node's files.
type tshootBundle struct {
	mu      sync.Mutex
	files   map[string][]byte
	problem []string // sections that could not be collected
	removed int      // secrets taken out
}

func (b *tshootBundle) put(name string, data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.files[name] = data
}

func (b *tshootBundle) fail(name string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.problem = append(b.problem, name+": "+err.Error())
}

func (b *tshootBundle) putJSON(name string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		b.fail(name, err)
		return
	}
	var generic any
	if json.Unmarshal(raw, &generic) != nil {
		b.put(name, raw)
		return
	}
	red, n := redactJSON(generic)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(red); err != nil {
		b.fail(name, err)
		return
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	b.mu.Lock()
	b.removed += n
	b.mu.Unlock()
	b.put(name, append(out, '\n'))
}

func (b *tshootBundle) putText(name, text string) {
	t, n := redactText(text)
	b.mu.Lock()
	b.removed += n
	b.mu.Unlock()
	b.put(name, []byte(t))
}

// tshootRun runs a command with a time limit and returns what it printed; a missing tool is said, not an error.
func tshootRun(name string, args ...string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return "(" + name + " is not installed)\n"
	}
	ctx, cancel := context.WithTimeout(context.Background(), tshootCmdTime)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if len(out) > tshootCmdMax {
		out = append([]byte("(output cut: the last part)\n"), out[len(out)-tshootCmdMax:]...)
	}
	s := string(out)
	if err != nil && s == "" {
		s = "(" + name + " failed: " + err.Error() + ")\n"
	}
	return s
}

func tshootFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(" + err.Error() + ")\n"
	}
	if len(b) > tshootCmdMax {
		b = b[len(b)-tshootCmdMax:]
	}
	return string(b)
}

// tshootSysctl keeps the settings that decide how a node answers ARP and neighbor discovery and routes packets.
func tshootSysctl() string {
	all := tshootRun("sysctl", "-a")
	keep := regexp.MustCompile(`(arp_|rp_filter|forwarding|proxy_arp|accept_ra|disable_ipv6|ndisc|route_localnet|accept_local|nonlocal_bind|promote_secondaries|bridge-nf)`)
	var out []string
	for _, l := range strings.Split(all, "\n") {
		if keep.MatchString(l) {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return all
	}
	return strings.Join(out, "\n") + "\n"
}

// TshootNode collects this node's bundle as a .tgz (see the layout in tshootFiles).  capture adds a short capture of ARP and
// neighbor-discovery traffic on every gateway interface.
func (m *Mgmt) TshootNode(capture bool, actor string) ([]byte, error) {
	warnf("troubleshooting bundle: collected on this node for %s", actor)
	ctx, cancel := context.WithTimeout(context.Background(), tshootNodeTime)
	defer cancel()
	b := &tshootBundle{files: map[string][]byte{}}
	var wg sync.WaitGroup
	do := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					b.fail("a section", fmt.Errorf("%v", r))
				}
			}()
			f()
		}()
	}

	// what the daemon knows
	do(func() {
		dc, _, err := m.LiveConfig()
		if err != nil {
			b.fail("ddgw/config.json", err)
			return
		}
		b.putJSON("ddgw/config.json", dc)
	})
	do(func() { b.putJSON("ddgw/config-versions.json", m.VersionsList()) })
	do(func() { b.putJSON("ddgw/dns-forward-failures.json", recentFwdFailures()) })
	do(func() { // who asked for what in the last ten minutes (top clients and domains, types, transports)
		now := time.Now()
		b.putJSON("ddgw/dns-queries-last-10-min.json", qstats.Query(now.Add(-10*time.Minute), now, QFilter{}))
	})
	do(func() {
		if m.cl != nil && m.cl.Enabled() {
			b.putJSON("ddgw/cluster.json", m.cl.View())
		} else {
			b.put("ddgw/cluster.json", []byte("{\"clustered\": false}\n"))
		}
	})
	do(func() {
		st, err := m.BGPStatus()
		if err != nil {
			b.fail("ddgw/bgp.json", err)
			return
		}
		b.putJSON("ddgw/bgp.json", st)
	})
	do(func() {
		if m.anycastFn != nil {
			b.putJSON("ddgw/anycast.json", m.anycastFn())
		}
	})
	do(func() {
		if m.gwFn != nil {
			b.putJSON("ddgw/gateways-reported-to-the-cluster.json", m.localGateways())
		}
	})
	do(func() { b.putJSON("ddgw/update.json", m.UpdateStatus()) })
	do(func() { b.putJSON("ddgw/update-history.json", m.upd.History(30)) })
	do(func() { b.putJSON("ddgw/virtual-mac-tests.json", vmacAll()) })
	do(func() { b.putJSON("ddgw/host-last-hour.json", hoststats.Query(time.Now().Add(-time.Hour), time.Now())) })
	do(func() {
		if m.tshootFn != nil {
			for name, v := range m.tshootFn() {
				b.putJSON("ddgw/"+name+".json", v)
			}
		}
	})
	do(func() {
		res, err := ReadLog(LogQuery{Limit: tshootLogLines})
		if err != nil {
			b.fail("ddgw/log.txt", err)
			return
		}
		var sb strings.Builder
		for _, l := range res.Lines {
			sb.WriteString(l.Raw)
			sb.WriteByte('\n')
		}
		b.putText("ddgw/log.txt", sb.String())
	})

	// the machine
	do(func() {
		var sb strings.Builder
		host, _ := os.Hostname()
		fmt.Fprintf(&sb, "collected %s\nhost %s\nddgw v%s\ngo %s %s/%s\nstate dir %s\nconfig %s\n\n", time.Now().Format(time.RFC3339), host, version(), runtime.Version(), runtime.GOOS, runtime.GOARCH, m.stateDir, m.confPath)
		sb.WriteString("# uname -a\n" + tshootRun("uname", "-a"))
		sb.WriteString("\n# /etc/os-release\n" + tshootFile("/etc/os-release"))
		sb.WriteString("\n# virtualization\n" + tshootRun("systemd-detect-virt"))
		sb.WriteString("\n# uptime\n" + tshootRun("uptime"))
		sb.WriteString("\n# free -m\n" + tshootRun("free", "-m"))
		sb.WriteString("\n# df -h\n" + tshootRun("df", "-h"))
		sb.WriteString("\n# biggest processes (names only: command lines can hold secrets)\n" + tshootTop())
		b.putText("system/system.txt", sb.String())
	})
	do(func() {
		b.putText("system/services.txt", "# systemctl status ddgw frr\n"+tshootRun("systemctl", "status", "ddgw", "frr", "--no-pager", "-l"))
	})
	do(func() { b.putText("ddgw/goroutines.txt", goroutineDump()) })
	do(func() {
		b.putText("system/journal-ddgw.txt", tshootRun("journalctl", "-u", "ddgw", "-n", "1500", "--no-pager"))
	})
	do(func() {
		s := tshootRun("dmesg")
		if l := strings.Split(s, "\n"); len(l) > 300 {
			s = strings.Join(l[len(l)-300:], "\n")
		}
		b.putText("system/dmesg-tail.txt", s)
	})
	do(func() {
		var sb strings.Builder
		if ents, err := os.ReadDir(m.stateDir); err == nil {
			for _, e := range ents {
				if fi, err := e.Info(); err == nil {
					fmt.Fprintf(&sb, "%s %10d %s %s\n", fi.Mode(), fi.Size(), fi.ModTime().Format(time.RFC3339), e.Name())
				}
			}
		}
		b.putText("system/state-dir-listing.txt", sb.String())
	})

	// the network
	cmds := []struct {
		file string
		do   func() string
	}{
		{"net/ip-addr.txt", func() string { return tshootRun("ip", "-d", "addr") }},
		{"net/ip-link.txt", func() string { return tshootRun("ip", "-s", "-d", "link") }},
		{"net/ip-route-v4.txt", func() string { return tshootRun("ip", "-4", "route", "show", "table", "all") }},
		{"net/ip-route-v6.txt", func() string { return tshootRun("ip", "-6", "route", "show", "table", "all") }},
		{"net/ip-rule.txt", func() string { return tshootRun("ip", "-4", "rule") + tshootRun("ip", "-6", "rule") }},
		{"net/neighbors.txt", func() string {
			return "# ip -4 neigh\n" + tshootRun("ip", "-4", "neigh") + "\n# ip -6 neigh\n" + tshootRun("ip", "-6", "neigh")
		}},
		{"net/proc-net.txt", func() string {
			return "# /proc/net/arp\n" + tshootFile("/proc/net/arp") + "\n# /proc/net/route\n" + tshootFile("/proc/net/route") +
				"\n# /proc/net/ipv6_route\n" + tshootFile("/proc/net/ipv6_route") + "\n# /proc/net/if_inet6\n" + tshootFile("/proc/net/if_inet6")
		}},
		{"net/sockets.txt", func() string {
			return "# ss -s\n" + tshootRun("ss", "-s") + "\n# ss -tulnp\n" + tshootRun("ss", "-tulnp")
		}},
		{"net/sysctl.txt", tshootSysctl},
		{"net/nftables.txt", func() string { return tshootRun("nft", "list", "ruleset") }},
		{"net/iptables.txt", func() string { return tshootRun("iptables-save") + tshootRun("ip6tables-save") }},
		{"frr/bgp.txt", func() string {
			return tshootRun("vtysh", "-c", "show bgp summary", "-c", "show bgp ipv4 unicast", "-c", "show bgp ipv6 unicast", "-c", "show bfd peers", "-c", "show bgp neighbors")
		}},
		{"frr/running-config.txt", func() string { return tshootRun("vtysh", "-c", "show running-config") }},
		{"frr/frr.conf.txt", func() string { return tshootFile("/etc/frr/frr.conf") }},
	}
	for _, c := range cmds {
		c := c
		do(func() { b.putText(c.file, c.do()) })
	}

	// a short capture of the traffic that makes virtual MACs and gateways work, when asked for
	if capture {
		dc, _, err := m.LiveConfig()
		if err == nil {
			seen := map[string]bool{}
			for _, g := range dc.Groups {
				if g.Interface == "" || seen[g.Interface] {
					continue
				}
				seen[g.Interface] = true
				iface := g.Interface
				do(func() {
					res, err := m.CaptureRun(ctx, iface, "arp or icmp6", tshootCapSecs, actor)
					if err != nil {
						b.fail("capture/"+iface+".pcap", err)
						return
					}
					b.put("capture/"+iface+"-arp-nd.pcap", res.Pcap)
				})
			}
		}
	}
	wg.Wait()

	// what is in it
	b.mu.Lock()
	var names []string
	for n := range b.files {
		names = append(names, n)
	}
	sort.Strings(names)
	host, _ := os.Hostname()
	var rd strings.Builder
	fmt.Fprintf(&rd, "Troubleshooting bundle of %s, ddgw v%s, collected %s.\n", host, version(), time.Now().Format(time.RFC3339))
	fmt.Fprintf(&rd, "%d secrets were removed (passwords, the gateway key, tokens, join codes, private keys).\n\nFiles:\n", b.removed)
	for _, n := range names {
		fmt.Fprintf(&rd, "  %-45s %d bytes\n", n, len(b.files[n]))
	}
	if len(b.problem) > 0 {
		sort.Strings(b.problem)
		rd.WriteString("\nNot collected:\n")
		for _, p := range b.problem {
			rd.WriteString("  " + p + "\n")
		}
	}
	b.files["README.txt"] = []byte(rd.String())
	files := b.files
	b.mu.Unlock()

	// the peer channel carries only so much: a log that is too long loses its oldest lines
	for {
		tgz, err := tarFiles(files, "")
		if err != nil {
			return nil, err
		}
		if len(tgz) <= tshootNodeMax {
			return tgz, nil
		}
		lg := files["ddgw/log.txt"]
		if len(lg) < 64<<10 {
			return nil, fmt.Errorf("the bundle is %d bytes, more than the %d the cluster channel carries", len(tgz), tshootNodeMax)
		}
		files["ddgw/log.txt"] = append([]byte("(older lines cut to fit)\n"), lg[len(lg)/2:]...)
	}
}

// tarFiles writes files as a .tgz, every name under prefix.
func tarFiles(files map[string][]byte, prefix string) ([]byte, error) {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	now := time.Now()
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: prefix + n, Mode: 0o600, Size: int64(len(files[n])), ModTime: now}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(files[n]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// untarFiles reads a .tgz written by tarFiles.
func untarFiles(tgz []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		name := filepath.ToSlash(filepath.Clean("/" + h.Name))[1:] // never above the node's folder
		b, err := io.ReadAll(io.LimitReader(tr, 64<<20))
		if err != nil {
			return nil, err
		}
		out[name] = b
	}
}

// TshootCluster collects the bundle of every node (this one directly, the others through the cluster relay, all at once)
// into one .tgz with a folder per node; a node that cannot be reached has a file saying why.
func (m *Mgmt) TshootCluster(capture bool, actor string) ([]byte, error) {
	targets := clusterTargets(m)
	type res struct {
		files map[string][]byte
		err   error
	}
	results := make([]res, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		i, t := i, t
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch {
			case t.Err != "":
				results[i].err = errors.New(t.Err)
			case t.Self:
				tgz, err := m.TshootNode(capture, actor)
				if err != nil {
					results[i].err = err
					return
				}
				results[i].files, results[i].err = untarFiles(tgz)
			default:
				// shorter than the web server's 90 s write limit, so that one node that does not answer gives a note in the bundle instead of
				// the whole download being cut off
				ctx, cancel := context.WithTimeout(context.Background(), tshootNodeTime+15*time.Second)
				defer cancel()
				body, _ := json.Marshal(map[string]any{"enabled": capture})
				resp, err := m.cl.Relay(ctx, t.Addr, proxyReq{User: actor, Method: "POST", Path: "/api/tshoot/node", CT: "application/json", Body: body})
				if err != nil {
					results[i].err = err
					return
				}
				var env struct {
					OK   bool `json:"ok"`
					Data struct {
						TGZ []byte `json:"tgz"`
					} `json:"data"`
					Error string `json:"error"`
				}
				if json.Unmarshal(resp.Body, &env) != nil || resp.Status != http.StatusOK || !env.OK {
					msg := firstNonEmpty(env.Error, fmt.Sprintf("answered %d", resp.Status))
					if resp.Status == http.StatusNotFound || strings.Contains(msg, "always acts on the node you are logged in to") {
						msg = "this node's version has no troubleshooting bundle: update it"
					}
					results[i].err = errors.New(msg)
					return
				}
				results[i].files, results[i].err = untarFiles(env.Data.TGZ)
			}
		}()
	}
	wg.Wait()

	all := map[string][]byte{}
	var rd strings.Builder
	fmt.Fprintf(&rd, "Troubleshooting bundle of the cluster, collected %s by %s.\nOne folder per node; each has its own README.txt.\n\n", time.Now().Format(time.RFC3339), actor)
	used := map[string]int{}
	ok := 0
	for i, t := range targets {
		name := strings.Trim(capNameRe.ReplaceAllString(t.Name, "_"), "_")
		if name == "" {
			name = "node"
		}
		if t.Self {
			name += "-this-node"
		}
		if used[name]++; used[name] > 1 {
			name = fmt.Sprintf("%s-%d", name, used[name])
		}
		if results[i].err != nil {
			fmt.Fprintf(&rd, "%-30s NOT COLLECTED: %v\n", name, results[i].err)
			all[name+"/NOT-COLLECTED.txt"] = []byte(results[i].err.Error() + "\n")
			continue
		}
		ok++
		fmt.Fprintf(&rd, "%-30s collected (%d files)\n", name, len(results[i].files))
		for fn, data := range results[i].files {
			all[name+"/"+fn] = data
		}
	}
	if ok == 0 {
		return nil, errors.New("nothing could be collected from any node: " + results[0].err.Error())
	}
	all["README.txt"] = []byte(rd.String())
	return tarFiles(all, "")
}

// vmacAll lists the last virtual-MAC test of every gateway.
func vmacAll() []VmacResult {
	vmacResults.Lock()
	defer vmacResults.Unlock()
	out := make([]VmacResult, 0, len(vmacResults.m))
	for _, r := range vmacResults.m {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GroupID < out[j].GroupID })
	return out
}

// ── the web side ─────────────────────────────────────────────────────────────

func (w *WebServer) registerTshoot(mux *http.ServeMux) {
	// this node's own bundle (what the cluster-wide one asks of every other node)
	mux.HandleFunc("POST /api/tshoot/node", w.authed(w.op("tshoot.node", nil)))
	// the bundle of every node, asked of the node you are logged in to: it asks the others itself
	mux.HandleFunc("GET /api/tshoot/download", w.authed(w.handleTshootDownload))
}

func (w *WebServer) handleTshootDownload(rw http.ResponseWriter, r *http.Request, s *session) {
	capture := r.URL.Query().Get("capture") != "0" // the capture is always part of it unless a caller asks otherwise
	var b []byte
	var err error
	if r.URL.Query().Get("all") == "0" {
		b, err = w.mg.TshootNode(capture, s.user)
	} else {
		b, err = w.mg.TshootCluster(capture, s.user)
	}
	if err != nil {
		jsonError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	host, _ := hostnameShort()
	rw.Header().Set("Content-Type", "application/gzip")
	rw.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "ddgw-tshoot-"+host+"-"+time.Now().Format("20060102-150405")+".tgz"))
	rw.Write(b)
}

// tshootTop lists the 30 biggest processes by name (never their command lines).
func tshootTop() string {
	l := strings.Split(strings.TrimSpace(tshootRun("ps", "-eo", "pid,etimes,rss,comm", "--sort=-rss")), "\n")
	if len(l) > 31 {
		l = l[:31]
	}
	return strings.Join(l, "\n") + "\n"
}

// goroutineDump is every goroutine's stack in this process: what to read when the daemon is up but something in it is
// stuck (a cluster port that does not answer, a gateway that does not start).  Function names and addresses only; no
// request or configuration data is in it.
func goroutineDump() string {
	var sb strings.Builder
	if pr := pprof.Lookup("goroutine"); pr != nil {
		pr.WriteTo(&sb, 2)
	}
	return sb.String()
}
