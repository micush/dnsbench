package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BGP: ddgw drives FRR on this node so the anycast addresses (anycast.go) can be
// announced without hand-written routing config.
//
// The BGP settings are per node (a node usually peers with its own upstream
// router) and are never replicated; only the anycast addresses themselves are
// shared.  ddgw owns /etc/frr/frr.conf on a node where BGP is enabled: it renders
// the whole file (BGP only), enables bgpd in /etc/frr/daemons and reloads FRR.
//
// The posture is deliberately narrow: this node announces its anycast addresses
// and nothing else, and accepts nothing from its peers (an inbound route-map
// denies every route), so a peer cannot change this host's routing table.
// An announced prefix exists in FRR's table only while ddgw holds the address on
// lo, so withdrawal follows anycast.go.

// BGPNeighbor is one BGP peer.
type BGPNeighbor struct {
	Peer        string `json:"peer"` // IPv4 or IPv6 address
	RemoteAS    uint32 `json:"remote_as"`
	Description string `json:"description,omitempty"`
	Password    string `json:"password,omitempty"` // MD5 TCP session password
	// Multihop is the eBGP hop limit (2-255) for a peer that is not on a directly connected subnet, e.g. an AWS VPC
	// Route Server endpoint.  0 = off (directly connected).  FRR then runs BFD to that peer in multihop mode too, so
	// the peer must be set up the same way.  Ignored for an iBGP neighbor.
	Multihop int `json:"multihop,omitempty"`
	// Disabled keeps the neighbor in the settings but shuts the session down (Operate ▸ Anycast); it is
	// not announced to until enabled again.
	Disabled  bool `json:"disabled,omitempty"`
	LegacyBFD bool `json:"bfd,omitempty"` // v41 setting; ignored (BFD is always on), dropped on the next save
}

// Default BGP keepalive and hold time (seconds) when the settings leave them empty.  The session uses the lower
// hold time of the two ends, so a peer with longer timers simply gets this node's shorter ones agreed down.
const bgpKeepalive, bgpHold = 3, 9

// timers returns the keepalive and hold time in seconds, the defaults when unset.
func (b *BGPConfig) timers() (int, int) {
	k, h := b.Keepalive, b.Hold
	if k == 0 {
		k = bgpKeepalive
	}
	if h == 0 {
		h = bgpHold
	}
	return k, h
}

// BGPConfig is the node's BGP settings.
type BGPConfig struct {
	ASN       uint32        `json:"asn"`                 // BGP runs on this node while an AS number is set
	RouterID  string        `json:"router_id,omitempty"` // IPv4 dotted quad; empty = FRR picks one
	Keepalive int           `json:"keepalive,omitempty"` // seconds; 0 = 3
	Hold      int           `json:"hold,omitempty"`      // seconds; 0 = 9
	Neighbors []BGPNeighbor `json:"neighbors"`
	// ASPrepend adds the local AS three more times to the AS path of every anycast route this node announces, so that the
	// routes of nodes that have it are the less preferred ones (a longer path) wherever the rest is equal.
	ASPrepend bool `json:"as_prepend,omitempty"`
	// Disabled stops BGP on this node without forgetting the settings (Operate ▸ Anycast): the BGP section is
	// removed from frr.conf as if the AS were cleared, and put back when it is enabled again.
	Disabled bool `json:"disabled,omitempty"`

	// v41 settings, read so that a config written by v41 still loads and
	// ignored (BGP runs when ASN is set, BFD is always on); dropped on the next save.
	LegacyEnabled bool `json:"enabled,omitempty"`
	LegacyBFD     bool `json:"bfd,omitempty"`
}

// Configured reports whether an AS number is set (the prerequisite for everything else).
func (b *BGPConfig) Configured() bool { return b != nil && b.ASN != 0 }

// Active reports whether BGP runs on this node: an AS number is set and BGP is not disabled.
func (b *BGPConfig) Active() bool { return b.Configured() && !b.Disabled }

func (b *BGPConfig) clone() *BGPConfig {
	if b == nil {
		return nil
	}
	c := *b
	c.Neighbors = append([]BGPNeighbor{}, b.Neighbors...)
	return &c
}

// Validate normalises the settings in place.  Everything that ends up in
// frr.conf is checked here, so no value can inject a config line.
func (b *BGPConfig) Validate() error {
	if b.Neighbors == nil {
		b.Neighbors = []BGPNeighbor{}
	}
	b.LegacyEnabled, b.LegacyBFD = false, false
	if b.ASN == 0 {
		// Without an AS there is nothing to run, so nothing to disable and no router ID to keep.  (An older file may
		// still hold a router ID next to a cleared AS; it is dropped here rather than refusing to load.)
		b.RouterID, b.Disabled = "", false
	}
	if b.RouterID != "" {
		a, err := netip.ParseAddr(strings.TrimSpace(b.RouterID))
		if err != nil || !a.Is4() || a.IsUnspecified() {
			return fmt.Errorf("bgp: router id %q must be an IPv4 address, e.g. 192.0.2.1 (or empty to let FRR choose)", b.RouterID)
		}
		b.RouterID = a.String()
	}
	if b.Keepalive < 0 || b.Keepalive > 21845 {
		return fmt.Errorf("bgp: keepalive %d must be 1-21845 seconds (empty = %d)", b.Keepalive, bgpKeepalive)
	}
	if b.Hold < 0 || b.Hold > 65535 || (b.Hold != 0 && b.Hold < 3) {
		return fmt.Errorf("bgp: hold time %d must be 3-65535 seconds (empty = %d)", b.Hold, bgpHold)
	}
	if k, h := b.timers(); k >= h {
		return fmt.Errorf("bgp: the keepalive (%d s) must be shorter than the hold time (%d s)", k, h)
	}
	seen := map[string]bool{}
	for i := range b.Neighbors {
		n := &b.Neighbors[i]
		n.LegacyBFD = false
		a, err := netip.ParseAddr(strings.TrimSpace(n.Peer))
		if err != nil || a.Zone() != "" || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() || a.IsLinkLocalUnicast() {
			return fmt.Errorf("bgp: neighbor %q must be an IPv4 or IPv6 address of the peer router", n.Peer)
		}
		n.Peer = a.Unmap().String()
		if seen[n.Peer] {
			return fmt.Errorf("bgp: neighbor %s is listed twice", n.Peer)
		}
		seen[n.Peer] = true
		if n.RemoteAS == 0 {
			return fmt.Errorf("bgp: neighbor %s needs its AS number (1-4294967295)", n.Peer)
		}
		if n.Multihop == 1 {
			n.Multihop = 0
		}
		if n.Multihop < 0 || n.Multihop > 255 {
			return fmt.Errorf("bgp: neighbor %s: multihop must be 2-255 (or empty for a directly connected peer)", n.Peer)
		}
		if len(n.Description) > 60 {
			return fmt.Errorf("bgp: neighbor %s: the description can be at most 60 characters", n.Peer)
		}
		for _, r := range n.Description {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("bgp: neighbor %s: the description has a control character", n.Peer)
			}
		}
		n.Description = strings.TrimSpace(n.Description)
		if len(n.Password) > 80 {
			return fmt.Errorf("bgp: neighbor %s: the password can be at most 80 characters", n.Peer)
		}
		for _, r := range n.Password {
			if r < 0x21 || r > 0x7e {
				return fmt.Errorf("bgp: neighbor %s: the password may use printable characters without spaces only", n.Peer)
			}
		}
	}
	return nil
}

// anycastAddresses lists every anycast address of every gateway, sorted, IPv4 first.
func anycastAddresses(dc *DaemonConfig) (v4, v6 []string) {
	seen := map[string]bool{}
	for _, g := range dc.Groups {
		for _, a := range g.ExtraVIPs {
			n, err := normalizeAnycast(a)
			if err != nil || seen[n] {
				continue
			}
			seen[n] = true
			if ip, _ := netip.ParseAddr(n); ip.Is4() {
				v4 = append(v4, n)
			} else {
				v6 = append(v6, n)
			}
		}
	}
	sort.Strings(v4)
	sort.Strings(v6)
	return v4, v6
}

// renderFRR builds the whole frr.conf.  It is a pure function.  Without BGP it is
// only the header, which is what a node that turned BGP off is left with.
func renderFRR(b *BGPConfig, v4, v6 []string, hostname string) string {
	var o strings.Builder
	o.WriteString("! Written by ddgw. Changes made here are overwritten; use ddgw --bgp or the BGP page.\n")
	o.WriteString("frr defaults traditional\n")
	if h := safeHostname(hostname); h != "" {
		o.WriteString("hostname " + h + "\n")
	}
	o.WriteString("log syslog informational\nservice integrated-vtysh-config\n!\n")
	if !b.Active() {
		return o.String()
	}
	// what this node may announce: its anycast addresses, nothing else
	o.WriteString("ip prefix-list DDGW-ANYCAST-V4 seq 1000 deny any\n")
	for i, a := range v4 {
		o.WriteString(fmt.Sprintf("ip prefix-list DDGW-ANYCAST-V4 seq %d permit %s/32\n", 10+i, a))
	}
	o.WriteString("ipv6 prefix-list DDGW-ANYCAST-V6 seq 1000 deny any\n")
	for i, a := range v6 {
		o.WriteString(fmt.Sprintf("ipv6 prefix-list DDGW-ANYCAST-V6 seq %d permit %s/128\n", 10+i, a))
	}
	prepend := ""
	if b.ASPrepend {
		prepend = fmt.Sprintf(" set as-path prepend %d %d %d\n", b.ASN, b.ASN, b.ASN)
	}
	o.WriteString("!\nroute-map DDGW-OUT-V4 permit 10\n match ip address prefix-list DDGW-ANYCAST-V4\n" + prepend)
	o.WriteString("route-map DDGW-OUT-V6 permit 10\n match ipv6 address prefix-list DDGW-ANYCAST-V6\n" + prepend)
	o.WriteString("route-map DDGW-IN deny 10\n!\n")
	o.WriteString(fmt.Sprintf("router bgp %d\n no bgp default ipv4-unicast\n", b.ASN))
	if b.RouterID != "" {
		o.WriteString(" bgp router-id " + b.RouterID + "\n")
	}
	ka, hold := b.timers()
	o.WriteString(fmt.Sprintf(" timers bgp %d %d\n", ka, hold))
	var n4, n6 []BGPNeighbor
	for _, n := range b.Neighbors {
		if ip, _ := netip.ParseAddr(n.Peer); ip.Is6() {
			n6 = append(n6, n)
		} else {
			n4 = append(n4, n)
		}
		o.WriteString(fmt.Sprintf(" neighbor %s remote-as %d\n", n.Peer, n.RemoteAS))
		if n.Multihop > 1 && n.RemoteAS != b.ASN {
			o.WriteString(fmt.Sprintf(" neighbor %s ebgp-multihop %d\n", n.Peer, n.Multihop))
		}
		if n.Description != "" {
			o.WriteString(fmt.Sprintf(" neighbor %s description %s\n", n.Peer, n.Description))
		}
		if n.Password != "" {
			o.WriteString(fmt.Sprintf(" neighbor %s password %s\n", n.Peer, n.Password))
		}
		o.WriteString(fmt.Sprintf(" neighbor %s bfd\n", n.Peer)) // fast failure detection, always
		if n.Disabled {
			o.WriteString(fmt.Sprintf(" neighbor %s shutdown\n", n.Peer)) // administratively down: kept, not announced to
		}
	}
	// an IPv4 address goes to the IPv4 neighbors, an IPv6 address to the IPv6 ones
	o.WriteString(" address-family ipv4 unicast\n")
	for _, a := range v4 {
		o.WriteString("  network " + a + "/32\n")
	}
	for _, n := range n4 {
		o.WriteString(fmt.Sprintf("  neighbor %s activate\n  neighbor %s route-map DDGW-IN in\n  neighbor %s route-map DDGW-OUT-V4 out\n", n.Peer, n.Peer, n.Peer))
	}
	o.WriteString(" exit-address-family\n address-family ipv6 unicast\n")
	for _, a := range v6 {
		o.WriteString("  network " + a + "/128\n")
	}
	for _, n := range n6 {
		o.WriteString(fmt.Sprintf("  neighbor %s activate\n  neighbor %s route-map DDGW-IN in\n  neighbor %s route-map DDGW-OUT-V6 out\n", n.Peer, n.Peer, n.Peer))
	}
	o.WriteString(" exit-address-family\n!\nline vty\n!\n")
	return o.String()
}

var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,62}$`)

func safeHostname(h string) string {
	if hostnameRe.MatchString(h) {
		return h
	}
	return ""
}

// ── applying it to FRR ───────────────────────────────────────────────────────

var (
	frrDir = "/etc/frr"
	// frrRun runs a command and returns its output (replaceable in tests).
	frrRun = func(name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
)

func frrConfPath() string    { return filepath.Join(frrDir, "frr.conf") }
func frrDaemonsPath() string { return filepath.Join(frrDir, "daemons") }

var daemonLineRe = func(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `=\w*[ \t]*$`)
}

// setDaemon switches one daemon on or off in the contents of /etc/frr/daemons.
func setDaemon(daemons, name string, on bool) string {
	v := "no"
	if on {
		v = "yes"
	}
	re := daemonLineRe(name)
	if re.MatchString(daemons) {
		return re.ReplaceAllString(daemons, name+"="+v)
	}
	if !strings.HasSuffix(daemons, "\n") && daemons != "" {
		daemons += "\n"
	}
	return daemons + name + "=" + v + "\n"
}

// BGPApplied is the outcome of the last attempt to bring FRR in line.
type BGPApplied struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	At     string `json:"at,omitempty"`
}

// BGPManager keeps FRR in line with the config.
type BGPManager struct {
	stateDir string
	hostname string

	mu      sync.Mutex
	last    BGPApplied
	pending *DaemonConfig // latest config waiting to be applied
	running bool
}

func NewBGPManager(stateDir string) *BGPManager {
	h, _ := os.Hostname()
	return &BGPManager{stateDir: stateDir, hostname: h}
}

func (b *BGPManager) markerPath() string { return filepath.Join(b.stateDir, "bgp.applied") }

// Apply schedules bringing FRR in line with dc.  It returns at once; runs are
// serialised and only the latest config is applied.
func (b *BGPManager) Apply(dc *DaemonConfig) {
	b.mu.Lock()
	b.pending = &DaemonConfig{Groups: dc.Groups, BGP: dc.BGP.clone()}
	if b.running {
		b.mu.Unlock()
		return
	}
	b.running = true
	b.mu.Unlock()
	go func() {
		for {
			b.mu.Lock()
			p := b.pending
			b.pending = nil
			if p == nil {
				b.running = false
				b.mu.Unlock()
				return
			}
			b.mu.Unlock()
			res := b.applyNow(p)
			b.mu.Lock()
			if res != nil {
				b.last = *res
			}
			b.mu.Unlock()
		}
	}()
}

// Last returns the outcome of the last attempt.
func (b *BGPManager) Last() BGPApplied {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

func (b *BGPManager) applyNow(dc *DaemonConfig) *BGPApplied {
	res := func(ok bool, f string, a ...any) *BGPApplied {
		return &BGPApplied{OK: ok, Detail: fmt.Sprintf(f, a...), At: time.Now().Format(time.RFC3339)}
	}
	enabled := dc.BGP.Active()
	_, markerErr := os.Stat(b.markerPath())
	managed := markerErr == nil
	if !enabled && !managed {
		return res(true, "BGP is off")
	}
	if _, err := os.Stat(frrDir); err != nil {
		if enabled {
			warnf("bgp: FRR is not installed (%s is missing); BGP is not configured", frrDir)
			return res(false, "FRR is not installed on this node (no %s)", frrDir)
		}
		os.Remove(b.markerPath())
		return res(true, "BGP is off")
	}
	v4, v6 := anycastAddresses(dc)
	conf := renderFRR(dc.BGP, v4, v6, b.hostname)
	oldConf, _ := os.ReadFile(frrConfPath())
	oldDaemons, err := os.ReadFile(frrDaemonsPath())
	if err != nil && !os.IsNotExist(err) {
		return res(false, "cannot read %s: %v", frrDaemonsPath(), err)
	}
	daemons := setDaemon(setDaemon(string(oldDaemons), "bgpd", enabled), "bfdd", enabled)
	confChanged, daemonsChanged := string(oldConf) != conf, daemons != string(oldDaemons)

	if confChanged {
		if err := writeFRRFile(frrConfPath(), []byte(conf)); err != nil {
			return res(false, "cannot write %s: %v", frrConfPath(), err)
		}
	}
	if daemonsChanged {
		if err := writeFRRFile(frrDaemonsPath(), []byte(daemons)); err != nil {
			return res(false, "cannot write %s: %v", frrDaemonsPath(), err)
		}
	}
	if enabled && !managed {
		backupForeignFRRConf(oldConf)
		_ = os.WriteFile(b.markerPath(), []byte(time.Now().Format(time.RFC3339)+"\n"), 0o600)
		frrRun("systemctl", "enable", "frr")
	}
	switch {
	case daemonsChanged: // a daemon is switched on or off: the service must restart
		if out, err := frrRestart(); err != nil {
			return res(false, "restarting FRR failed: %v %s", err, out)
		}
	case confChanged && frrCanReload():
		if out, err := frrRun("systemctl", "reload", "frr"); err != nil {
			warnf("bgp: reloading FRR failed (%v %s), restarting it", err, strings.TrimSpace(string(out)))
			if out, err := frrRestart(); err != nil {
				return res(false, "reloading FRR failed: %v %s", err, out)
			}
		}
	case confChanged:
		// FRR's reload needs frr-reload.py (package frr-pythontools); without it a reload
		// fails and every attempt costs a restart, so restart once, on purpose
		warnf("bgp: frr-reload.py is missing (package frr-pythontools), restarting FRR to apply the change")
		if out, err := frrRestart(); err != nil {
			return res(false, "restarting FRR failed: %v %s", err, out)
		}
	default:
		if _, err := frrRun("systemctl", "is-active", "--quiet", "frr"); err != nil && enabled {
			_, _ = frrRun("systemctl", "reset-failed", "frr")
			if out, err := frrRun("systemctl", "start", "frr"); err != nil {
				return res(false, "starting FRR failed: %v %s%s", err, strings.TrimSpace(string(out)), frrJournalTail())
			}
		}
	}
	if !enabled {
		os.Remove(b.markerPath())
		infof("bgp: turned off; frr.conf no longer has a BGP section")
		return res(true, "BGP is off")
	}
	if confChanged || daemonsChanged {
		infof("bgp: FRR configured (AS %d, %d neighbor(s), %d IPv4 + %d IPv6 anycast address(es))", dc.BGP.ASN, len(dc.BGP.Neighbors), len(v4), len(v6))
	}
	return res(true, "FRR is configured")
}

// frrCanReload says whether "systemctl reload frr" can work: FRR's reload script
// lives in the separate frr-pythontools package on Debian and the RHEL family.
var frrCanReload = func() bool {
	_, err := os.Stat("/usr/lib/frr/frr-reload.py")
	return err == nil
}

// frrRestart restarts FRR.  A failed unit is reset first: systemd refuses to start
// a unit that hit its start limit (frr.service allows 3 starts in 3 minutes), and
// that would hide the real error behind "start request repeated too quickly".
func frrRestart() (string, error) {
	_, _ = frrRun("systemctl", "reset-failed", "frr")
	out, err := frrRun("systemctl", "restart", "frr")
	if err != nil {
		return strings.TrimSpace(string(out)) + frrJournalTail(), err
	}
	return "", nil
}

// frrJournalTail returns the last few meaningful lines of FRR's journal, so the BGP
// page can show why it would not start.  FRR logs every vty command; those are skipped.
func frrJournalTail() string {
	out, err := frrRun("journalctl", "-u", "frr", "-n", "40", "--no-pager", "-o", "cat")
	if err != nil {
		return ""
	}
	var keep []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.Contains(l, "vty[") {
			continue
		}
		keep = append(keep, l)
	}
	if len(keep) > 4 {
		keep = keep[len(keep)-4:]
	}
	if len(keep) == 0 {
		return ""
	}
	return " [journal: " + strings.Join(keep, " | ") + "]"
}

// backupForeignFRRConf keeps a copy of an frr.conf that ddgw did not write, the
// first time ddgw takes the file over, so a host's earlier FRR setup is not lost.
func backupForeignFRRConf(old []byte) {
	if len(old) == 0 || strings.HasPrefix(string(old), "! Written by ddgw") {
		return
	}
	p := frrConfPath() + ".pre-ddgw"
	if _, err := os.Stat(p); err == nil {
		return
	}
	if err := os.WriteFile(p, old, 0o640); err != nil {
		warnf("bgp: could not back up %s: %v", frrConfPath(), err)
		return
	}
	infof("bgp: the existing %s was saved as %s", frrConfPath(), p)
}

// writeFRRFile replaces a file atomically, readable by FRR's group.
func writeFRRFile(path string, data []byte) error {
	tmp := path + ".ddgw.tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	if g, err := user.LookupGroup("frr"); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			_ = os.Chown(tmp, 0, gid)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ── status ───────────────────────────────────────────────────────────────────

// BGPPeer is one neighbor as FRR reports it.
type BGPPeer struct {
	Peer     string `json:"peer"`
	AF       string `json:"af"` // ipv4 | ipv6
	RemoteAS uint32 `json:"remote_as"`
	State    string `json:"state"` // Established, Active, Connect, Idle …
	Uptime   string `json:"uptime,omitempty"`
	Sent     int    `json:"sent"`          // prefixes announced to it
	Received int    `json:"received"`      // prefixes received (always 0 here: inbound is denied)
	BFD      string `json:"bfd,omitempty"` // up | down | init | shutdown; empty = FRR has no BFD session for it
}

// BGPStatus is the BGP page / --bgp view.
type BGPStatus struct {
	Config    BGPConfig      `json:"config"`
	Applied   BGPApplied     `json:"applied"`
	Installed bool           `json:"installed"`
	Running   bool           `json:"running"` // bgpd answered
	Peers     []BGPPeer      `json:"peers"`
	Announce  []string       `json:"announce"`  // anycast addresses this node is set up to announce
	Addresses []AnycastState `json:"addresses"` // the same, and whether each is announced right now
	Notes     []string       `json:"notes"`
}

// vtyshBGP asks FRR for the BGP summary (replaceable in tests).
var vtyshBGP = func() ([]byte, error) { return frrRun("vtysh", "-c", "show bgp summary json") }

// vtyshBFD asks FRR for its BFD sessions (replaceable in tests).
var vtyshBFD = func() ([]byte, error) { return frrRun("vtysh", "-c", "show bfd peers json") }

// parseBFDPeers reads `show bfd peers json` into peer address -> status.
func parseBFDPeers(raw []byte) (map[string]string, error) {
	var l []struct {
		Peer   string `json:"peer"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range l {
		if a, err := netip.ParseAddr(p.Peer); err == nil {
			out[a.Unmap().String()] = p.Status
		}
	}
	return out, nil
}

// parseBGPSummary reads `show bgp summary json`, which has one object per
// address family with a "peers" map.
func parseBGPSummary(raw []byte) ([]BGPPeer, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	var out []BGPPeer
	for _, key := range []string{"ipv4Unicast", "ipv6Unicast"} {
		blob, ok := top[key]
		if !ok {
			continue
		}
		var fam struct {
			Peers map[string]struct {
				RemoteAs uint32 `json:"remoteAs"`
				State    string `json:"state"`
				Uptime   string `json:"peerUptime"`
				PfxRcd   int    `json:"pfxRcd"`
				PfxSnt   int    `json:"pfxSnt"`
			} `json:"peers"`
		}
		if err := json.Unmarshal(blob, &fam); err != nil {
			return nil, err
		}
		af := "ipv4"
		if key == "ipv6Unicast" {
			af = "ipv6"
		}
		for peer, p := range fam.Peers {
			up := ""
			if p.State == "Established" {
				up = p.Uptime
			}
			out = append(out, BGPPeer{Peer: peer, AF: af, RemoteAS: p.RemoteAs, State: p.State, Uptime: up, Sent: p.PfxSnt, Received: p.PfxRcd})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AF != out[j].AF {
			return out[i].AF < out[j].AF
		}
		return out[i].Peer < out[j].Peer
	})
	return out, nil
}

// Status gathers the node's BGP configuration, what was applied and live neighbor state.
func (m *Mgmt) BGPStatus() (*BGPStatus, error) {
	dc, _, err := m.LiveConfig()
	if err != nil {
		return nil, err
	}
	st := &BGPStatus{Config: BGPConfig{Neighbors: []BGPNeighbor{}}, Peers: []BGPPeer{}, Announce: []string{}, Addresses: []AnycastState{}, Notes: []string{}}
	if dc.BGP != nil {
		st.Config = *dc.BGP.clone()
	}
	st.Applied = m.bgp.Last()
	if _, err := os.Stat(frrDir); err == nil {
		st.Installed = true
	}
	v4, v6 := anycastAddresses(dc)
	st.Announce = append(append(st.Announce, v4...), v6...)
	if m.anycastFn != nil {
		st.Addresses = append(st.Addresses, m.anycastFn()...)
	}
	if st.Config.Active() && st.Installed {
		if raw, err := vtyshBGP(); err == nil {
			if peers, err := parseBGPSummary(raw); err == nil {
				st.Running = true
				st.Peers = peers
				if raw, err := vtyshBFD(); err == nil {
					if bfd, err := parseBFDPeers(raw); err == nil {
						for i := range st.Peers {
							st.Peers[i].BFD = bfd[st.Peers[i].Peer]
						}
					}
				}
			}
		}
	}
	if st.Config.Active() {
		if st.Installed && !frrCanReload() {
			st.Notes = append(st.Notes, "FRR cannot reload its config here (the frr-pythontools package is missing), so every BGP change restarts FRR and drops the sessions briefly. Install frr-pythontools.")
		}
		if len(st.Announce) == 0 {
			st.Notes = append(st.Notes, "Nothing is announced yet: add an anycast address to a gateway on the Topology page.")
		}
	}
	return st, nil
}

// BGPSet replaces this node's BGP settings.  The disabled flags (Operate ▸ Anycast) are not part of the settings
// and are carried over from the running config, so editing the settings never switches anything on or off.
func (m *Mgmt) BGPSet(c BGPConfig, actor string) error {
	dc, _, err := m.LiveConfig()
	if err != nil {
		return err
	}
	if c.ASN == 0 && strings.TrimSpace(c.RouterID) != "" {
		return fmt.Errorf("bgp: a router id needs a local AS number; set the AS first")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	was := map[string]bool{}
	if dc.BGP != nil {
		for _, n := range dc.BGP.Neighbors {
			was[n.Peer] = n.Disabled
		}
		if c.ASN != 0 {
			c.Disabled = dc.BGP.Disabled
		}
	}
	for i := range c.Neighbors {
		c.Neighbors[i].Disabled = was[c.Neighbors[i].Peer]
	}
	note := "bgp: "
	switch {
	case dc.BGP == nil && c.ASN == 0 && len(c.Neighbors) == 0:
		return nil
	case c.Active():
		note += fmt.Sprintf("AS %d, %d neighbor(s)", c.ASN, len(c.Neighbors))
	default:
		note += "off"
	}
	cc := c
	dc.BGP = &cc
	return m.PutConfig(dc, actor, note)
}

// BGPOperateArgs switches the BGP process (Peer empty) or one neighbor on or off.
type BGPOperateArgs struct {
	Peer    string `json:"peer,omitempty"`
	Enabled bool   `json:"enabled"`
}

// BGPOperate enables or disables BGP on this node, or one of its neighbors, without touching the settings.
// A disabled process leaves frr.conf as if the AS were cleared; a disabled neighbor stays in frr.conf, shut down.
func (m *Mgmt) BGPOperate(a BGPOperateArgs, actor string) error {
	dc, _, err := m.LiveConfig()
	if err != nil {
		return err
	}
	if !dc.BGP.Configured() {
		return fmt.Errorf("bgp: set a local AS number first")
	}
	c := dc.BGP.clone()
	var note string
	if p := strings.TrimSpace(a.Peer); p == "" {
		if c.Disabled == !a.Enabled {
			return nil
		}
		c.Disabled = !a.Enabled
		note = "bgp: " + map[bool]string{true: "enabled", false: "disabled"}[a.Enabled]
	} else {
		ip, err := netip.ParseAddr(p)
		if err != nil {
			return fmt.Errorf("bgp: %q is not a neighbor address", a.Peer)
		}
		p = ip.Unmap().String()
		found := false
		for i := range c.Neighbors {
			if c.Neighbors[i].Peer != p {
				continue
			}
			found = true
			if c.Neighbors[i].Disabled == !a.Enabled {
				return nil
			}
			c.Neighbors[i].Disabled = !a.Enabled
		}
		if !found {
			return fmt.Errorf("bgp: there is no neighbor %s", p)
		}
		note = "bgp: neighbor " + p + map[bool]string{true: " enabled", false: " disabled"}[a.Enabled]
	}
	dc.BGP = c
	return m.PutConfig(dc, actor, note)
}

// ── anycast colour ───────────────────────────────────────────────────────────

// anycastBGPStatus says how an announced anycast address looks given this node's BGP neighbors of its family:
// green when every one of them has an established session, amber when at least one does and at least one does not,
// red when none does (or none is configured, or FRR does not answer).  A neighbor that is disabled or still
// connecting counts as not established.  known is false when FRR could not be asked.  kind says what red or amber
// means ("" when green): down, none or partial; the drawing words its label from it.
func anycastBGPStatus(addr string, nbrs []BGPNeighbor, peers []BGPPeer, known bool) (status, detail, kind string) {
	af, label := "ipv4", "IPv4"
	if ip, err := netip.ParseAddr(addr); err == nil && ip.Unmap().Is6() {
		af, label = "ipv6", "IPv6"
	}
	if !known {
		return "bad", "BGP is not answering: no session, so the address is not reachable", "down"
	}
	state := map[string]string{}
	for _, p := range peers {
		state[p.Peer] = p.State
	}
	var n, est int
	for _, nb := range nbrs {
		if ip, err := netip.ParseAddr(nb.Peer); err != nil || ip.Is6() != (af == "ipv6") {
			continue
		}
		n++
		if !nb.Disabled && state[nb.Peer] == "Established" {
			est++
		}
	}
	switch {
	case n == 0:
		return "bad", "No " + label + " BGP neighbor is configured: the address is not announced to anyone", "none"
	case est == n:
		return "ok", "", ""
	case est > 0:
		return "warn", fmt.Sprintf("%d of %d %s BGP neighbors are not established", n-est, n, label), "partial"
	}
	return "bad", "No " + label + " BGP neighbor is established: the address is not reachable", "down"
}

var bgpLive struct {
	sync.Mutex
	at    time.Time
	peers []BGPPeer
	ok    bool
}

// liveBGPPeers is the neighbor list from FRR, kept for 3 s so a busy canvas does
// not run vtysh on every poll.
func liveBGPPeers() ([]BGPPeer, bool) {
	bgpLive.Lock()
	defer bgpLive.Unlock()
	if !bgpLive.at.IsZero() && time.Since(bgpLive.at) < 3*time.Second {
		return bgpLive.peers, bgpLive.ok
	}
	bgpLive.at, bgpLive.peers, bgpLive.ok = time.Now(), nil, false
	if _, err := os.Stat(frrDir); err != nil {
		return nil, false
	}
	if raw, err := vtyshBGP(); err == nil {
		if p, err := parseBGPSummary(raw); err == nil {
			bgpLive.peers, bgpLive.ok = p, true
		}
	}
	return bgpLive.peers, bgpLive.ok
}
