package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// Everything ddgw keeps lives in /var/lib/ddgw: the config, the GUI
	// certificate, config history, cluster identity and update data.
	confPath     = "/var/lib/ddgw/ddgw.conf"
	statusSocket = "/run/ddgw/ddgw.sock"

	// Releases before v15 kept the config under /etc/liras.
	legacyConfPath = "/etc/liras/ddgw.conf"
)

// canonicalConf turns the pre-v15 path into the real one.  A service unit that was
// never rewritten (an in-place update does not touch it) still starts the daemon
// with `--config /etc/liras/ddgw.conf`; nothing may be read from or written to
// /etc/liras any more, so that path means the config in /var/lib/ddgw.
func canonicalConf(path string) string {
	if filepath.Clean(path) == legacyConfPath {
		return confPath
	}
	return path
}

// migrateLegacyConfig moves a pre-v15 /etc/liras config (and the self-signed
// GUI certificate next to it) to the default location.  It only acts when the
// new file does not exist yet and never overwrites anything.  It is a real move:
// each old file is removed once its copy is written and read back intact, and the
// directory goes when it is empty, so nothing is left in /etc/liras.  Errors are not fatal:
// the daemon then simply starts without a config, as for a fresh install.
func migrateLegacyConfig(dst, legacy string) (moved bool, err error) {
	if _, err := os.Stat(dst); err == nil {
		return false, nil
	}
	b, err := os.ReadFile(legacy)
	if err != nil {
		return false, nil // nothing to migrate
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return false, err
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(legacy); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.WriteFile(dst, b, mode); err != nil {
		return false, err
	}
	for _, f := range []string{"ddgw-web.crt", "ddgw-web.key"} {
		src, to := filepath.Join(filepath.Dir(legacy), f), filepath.Join(filepath.Dir(dst), f)
		if _, err := os.Stat(to); err == nil {
			continue
		}
		if cb, err := os.ReadFile(src); err == nil {
			if os.WriteFile(to, cb, 0o600) == nil {
				if back, err := os.ReadFile(to); err == nil && bytes.Equal(back, cb) {
					_ = os.Remove(src)
				}
			}
		}
	}
	if back, err := os.ReadFile(dst); err == nil && bytes.Equal(back, b) {
		_ = os.Remove(legacy)
	}
	_ = os.Remove(filepath.Dir(legacy)) // only succeeds when it is empty
	return true, nil
}

// ── Group config ─────────────────────────────────────────────────────────────

// GroupConfig holds per-group settings.  Each group gets its own engine(s).
type GroupConfig struct {
	// Name is an optional label shown instead of the address in the GUI and CLI.
	// Shared across the cluster.
	Name      string `json:"name,omitempty"`
	GroupID   int    `json:"group_id"`
	Interface string `json:"interface"`
	VIP4      string `json:"vip4"`
	VIP6      string `json:"vip6"`
	// MoreVIP4 and MoreVIP6 are further shared addresses inside the subnet of VIP4 / VIP6 (bare addresses, no prefix length:
	// they take the primary's).  They move with the gateway like the primary does and answer DNS the same way; the
	// primary stays the one in the hello packets.  Omitted when none, so older versions read the same file.
	MoreVIP4 []string `json:"more_vip4,omitempty"`
	MoreVIP6 []string `json:"more_vip6,omitempty"`
	Priority int      `json:"priority"`
	LBMethod LBMethod `json:"lb_method"`
	// RealMACs turns the virtual MACs off: no macvlan interfaces, and the controller answers ARP and neighbor
	// solicitations for the VIP with the real MAC address of the node it picks.  Every node then holds the VIP on lo.  For
	// where virtual MACs cannot work (a VMware port group that is not promiscuous, a cloud that allows one MAC per
	// interface); the price is failover that depends on the neighbors honoring an unsolicited ARP.  Off (the usual way) is
	// not written to the file, so a configuration that never used it stays readable by older versions.
	RealMACs  bool     `json:"real_macs,omitempty"`
	Weight    int      `json:"weight"`
	HelloMS   int      `json:"hello_ms"`
	HoldMS    int      `json:"hold_ms"`
	Preempt   bool     `json:"preempt"`
	MaxAFNs   int      `json:"max_afns"`
	Key       string   `json:"key"`
	Neighbors []string `json:"neighbors"`
	// DNSProxy is no longer a setting: every gateway serves DNS on its VIP.  The key
	// stays in the file, always true, so versions before v45 (where it was a switch
	// and a missing key meant off) keep serving DNS from the same file.  Validate
	// forces it on, and nothing reads it.
	DNSProxy bool `json:"dns_proxy"`
	// DNS is this group's own upstream pool (what the canvas writes).  When
	// absent the group uses the top-level "dns" block, which several groups may
	// share.
	DNS *DNSConfig `json:"dns,omitempty"`
	// ExtraVIPs are anycast addresses: extra addresses of any subnet (no check
	// against the interface) that every running node holds on lo and answers DNS
	// on, while this gateway's DNS pool is healthy.  Shared across the cluster.
	ExtraVIPs []string `json:"extra_vips,omitempty"`
	// PausedVIPs are anycast addresses whose announcing is paused on every node (shared); PausedVIPsHere the ones
	// paused on this node only (never replicated).  A paused address stays in ExtraVIPs but is taken off lo, so the
	// route is withdrawn until it is resumed.
	PausedVIPs []string `json:"paused_vips,omitempty"`
	// PausedAll pauses the gateway on every node (shared); Paused below pauses it on this node only.
	PausedAll      bool     `json:"paused_all,omitempty"`
	PausedVIPsHere []string `json:"paused_vips_here,omitempty"`
	// Paused takes this node out of the gateway for maintenance: it resigns and
	// stops answering, other nodes carry on.  Local to the node, never replicated.
	Paused bool `json:"paused,omitempty"`
	// ExcludedNodes are the cluster nodes (by node ID) that do not serve this gateway: shared, so every node agrees.  A
	// node not listed serves it.  A node that joins the cluster is added to every gateway's list by the primary at that moment.  Omitted when empty, so a configuration that never used it
	// stays readable by older versions.  ExcludedHere is set by effective() when this node is one of them (never written).
	ExcludedNodes []string `json:"excluded_nodes,omitempty"`
	ExcludedHere  bool     `json:"-"`
}

// newGatewayGroup is the starting point of a gateway that is created now (first start, Add gateway, --canvas-add,
// --configure): the same as defaultGroup but with real MAC addresses on, which is what most networks need (a VMware
// port group, a cloud, a switch with port security).  defaultGroup itself stays off: a group read from a file that does
// not say real_macs is off, as it always was, so an upgrade changes no running gateway.
func newGatewayGroup() GroupConfig {
	g := defaultGroup()
	g.RealMACs = true
	return g
}

func defaultGroup() GroupConfig {
	return GroupConfig{
		GroupID:   1,
		Interface: "eth0",
		VIP4:      "10.0.0.1/24",
		Priority:  100,
		LBMethod:  lbRoundRobin,
		Weight:    100,
		HelloMS:   333,
		HoldMS:    999,
		MaxAFNs:   defaultMaxAFNs,
		Key:       "dgw",
		DNSProxy:  true, // always (see GroupConfig.DNSProxy)
		Neighbors: []string{},
	}
}

func (m LBMethod) MarshalJSON() ([]byte, error) { return json.Marshal(m.String()) }

func (m *LBMethod) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := parseLB(s)
	if err != nil {
		return err
	}
	*m = v
	return nil
}

func strictUnmarshal(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (g *GroupConfig) UnmarshalJSON(b []byte) error {
	type alias GroupConfig
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	a := alias(defaultGroup())
	// back-compat: old configs have "vip" instead of vip4
	if v, ok := raw["vip"]; ok {
		if _, has := raw["vip4"]; !has {
			raw["vip4"] = v
		}
		delete(raw, "vip")
		b, _ = json.Marshal(raw)
	}
	if err := strictUnmarshal(b, &a); err != nil {
		return err
	}
	*g = GroupConfig(a)
	return nil
}

func (g *GroupConfig) keyBytes() []byte {
	if g.Key == "" {
		return []byte("dgw")
	}
	return []byte(g.Key)
}

func (g *GroupConfig) vipFor(af AF) string {
	if af == afIPv4 {
		return g.VIP4
	}
	return g.VIP6
}

// maxMoreVIPs is how many further addresses a gateway may hold per family.
const maxMoreVIPs = 32

func (g *GroupConfig) moreFor(af AF) []string {
	if af == afIPv4 {
		return g.MoreVIP4
	}
	return g.MoreVIP6
}

// vipsFor is every shared address of the family with its prefix length, the primary first.
func (g *GroupConfig) vipsFor(af AF) []string {
	p := g.vipFor(af)
	if p == "" {
		return nil
	}
	out := []string{p}
	pfx, err := netip.ParsePrefix(p)
	if err != nil {
		return out
	}
	for _, a := range g.moreFor(af) {
		out = append(out, fmt.Sprintf("%s/%d", a, pfx.Bits()))
	}
	return out
}

// cleanMoreVIPs checks the further addresses of one family against the primary: right family, inside its subnet, not the
// primary itself, not listed twice.  It returns them in canonical form.
func cleanMoreVIPs(list []string, primary string, v6 bool, pre, field string) ([]string, error) {
	if len(list) == 0 {
		return nil, nil
	}
	fam := map[bool]string{false: "IPv4", true: "IPv6"}[v6]
	if primary == "" {
		return nil, fmt.Errorf("%s: %s needs the gateway's %s address first", pre, field, fam)
	}
	if len(list) > maxMoreVIPs {
		return nil, fmt.Errorf("%s: %s holds at most %d addresses", pre, field, maxMoreVIPs)
	}
	pfx, err := netip.ParsePrefix(primary)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: the gateway's address %q is not valid", pre, field, primary)
	}
	seen := map[netip.Addr]bool{pfx.Addr(): true}
	out := make([]string, 0, len(list))
	for _, s := range list {
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil || a.Is6() != v6 || a.Is4In6() {
			return nil, fmt.Errorf("%s: %s: %q is not an %s address", pre, field, s, fam)
		}
		if !pfx.Contains(a) {
			return nil, fmt.Errorf("%s: %s: %s is not in the gateway's subnet %s", pre, field, a, pfx.Masked())
		}
		if a == pfx.Masked().Addr() || (!v6 && a == lastV4(pfx)) {
			return nil, fmt.Errorf("%s: %s: %s is the subnet's network or broadcast address", pre, field, a)
		}
		if a.IsMulticast() || a.IsUnspecified() {
			return nil, fmt.Errorf("%s: %s: %s cannot be used", pre, field, a)
		}
		if seen[a] {
			return nil, fmt.Errorf("%s: %s: %s is listed twice or is the gateway's own address", pre, field, a)
		}
		seen[a] = true
		out = append(out, a.String())
	}
	return out, nil
}

// lastV4 is the broadcast address of an IPv4 prefix (only for a prefix short enough to have one).
func lastV4(p netip.Prefix) netip.Addr {
	if p.Bits() >= 31 {
		return netip.Addr{}
	}
	b := p.Masked().Addr().As4()
	for i := p.Bits(); i < 32; i++ {
		b[i/8] |= 1 << (7 - uint(i%8))
	}
	return netip.AddrFrom4(b)
}

func (g *GroupConfig) wants(af AF) bool { return g.vipFor(af) != "" }

// unicastMode is true when explicit neighbors are configured.
func (g *GroupConfig) unicastMode() bool { return len(g.Neighbors) > 0 }

func (g *GroupConfig) Validate() error {
	pre := fmt.Sprintf("group %d", g.GroupID)
	if g.GroupID < 1 || g.GroupID > 255 {
		return fmt.Errorf("%s: group_id must be 1-255", pre)
	}
	if err := validGatewayName(g.Name); err != nil {
		return fmt.Errorf("%s: %v", pre, err)
	}
	if g.VIP4 == "" && g.VIP6 == "" {
		return fmt.Errorf("%s: at least one of vip4 or vip6 must be set", pre)
	}
	if g.VIP4 != "" {
		p, err := netip.ParsePrefix(g.VIP4)
		if err != nil || !p.Addr().Is4() {
			return fmt.Errorf("%s: vip4 %q must be IPv4 address/prefix, e.g. 10.0.0.1/24", pre, g.VIP4)
		}
	}
	if g.VIP6 != "" {
		p, err := netip.ParsePrefix(g.VIP6)
		if err != nil || !p.Addr().Is6() {
			return fmt.Errorf("%s: vip6 %q must be IPv6 address/prefix, e.g. 2001:db8::1/64", pre, g.VIP6)
		}
	}
	var err error
	if g.MoreVIP4, err = cleanMoreVIPs(g.MoreVIP4, g.VIP4, false, pre, "more_vip4"); err != nil {
		return err
	}
	if g.MoreVIP6, err = cleanMoreVIPs(g.MoreVIP6, g.VIP6, true, pre, "more_vip6"); err != nil {
		return err
	}
	for i, a := range g.ExtraVIPs {
		n, err := normalizeAnycast(a)
		if err != nil {
			return fmt.Errorf("%s: %v", pre, err)
		}
		g.ExtraVIPs[i] = n
	}
	g.PausedVIPs = keepAnycast(g.PausedVIPs, g.ExtraVIPs)
	g.PausedVIPsHere = keepAnycast(g.PausedVIPsHere, g.ExtraVIPs)
	if l, err := cleanNodeIDs(g.ExcludedNodes); err != nil {
		return fmt.Errorf("%s: excluded_nodes: %v", pre, err)
	} else {
		g.ExcludedNodes = l
	}
	for _, f := range []struct {
		name      string
		v, lo, hi int
	}{
		{"priority", g.Priority, 0, 255},
		{"weight", g.Weight, 0, 255},
		{"max_afns", g.MaxAFNs, 1, 255},
		{"hello_ms", g.HelloMS, 1, 65535},
		{"hold_ms", g.HoldMS, 1, 65535},
	} {
		if f.v < f.lo || f.v > f.hi {
			return fmt.Errorf("%s: %s must be %d-%d", pre, f.name, f.lo, f.hi)
		}
	}
	if g.Interface == "" {
		return fmt.Errorf("%s: interface must be set", pre)
	}
	for _, n := range g.Neighbors {
		if _, err := netip.ParseAddr(n); err != nil {
			return fmt.Errorf("%s: invalid neighbor address %q", pre, n)
		}
	}
	return nil
}

// restartDiffers reports whether a change between a and b needs an engine restart.
func restartDiffers(a, b *GroupConfig) bool {
	return a.Paused != b.Paused || a.Interface != b.Interface || a.VIP4 != b.VIP4 || a.VIP6 != b.VIP6 ||
		!sameList(a.MoreVIP4, b.MoreVIP4) || !sameList(a.MoreVIP6, b.MoreVIP6) ||
		a.Key != b.Key || a.RealMACs != b.RealMACs || // the MAC mode changes what the node sets up: restart the gateway
		!reflect.DeepEqual(a.Neighbors, b.Neighbors)
}

// sameList compares two string lists, treating nil and empty as equal.
func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// normalizeAnycast turns an anycast address into its canonical bare form.  A
// /32 (IPv4) or /128 (IPv6) suffix is accepted; any other prefix length is
// refused, since the address is a single host and a prefix would suggest it
// belongs to a subnet.  Loopback, link-local, multicast and unspecified
// addresses are refused.
func normalizeAnycast(s string) (string, error) {
	t := strings.TrimSpace(s)
	var a netip.Addr
	if p, err := netip.ParsePrefix(t); err == nil {
		if p.Bits() != p.Addr().BitLen() {
			return "", fmt.Errorf("anycast address %q: give the address alone (or /%d), not a subnet", s, p.Addr().BitLen())
		}
		a = p.Addr()
	} else if a, err = netip.ParseAddr(t); err != nil {
		return "", fmt.Errorf("anycast address %q must be an IPv4 or IPv6 address, e.g. 203.0.113.53 or 2001:db8::53", s)
	}
	a = a.Unmap()
	if a.Zone() != "" || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() || a.IsLinkLocalUnicast() {
		return "", fmt.Errorf("anycast address %q is not a usable unicast address", s)
	}
	return a.String(), nil
}

// ── DNS proxy config ─────────────────────────────────────────────────────────

// DNSQuery is one probe query.  In JSON it may be written as an object
// {"name": "example.com", "type": "A"} or a string "example.com" /
// "example.com AAAA".
type DNSQuery struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (q *DNSQuery) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		f := strings.Fields(s)
		switch len(f) {
		case 1:
			*q = DNSQuery{Name: f[0], Type: "A"}
		case 2:
			*q = DNSQuery{Name: f[0], Type: strings.ToUpper(f[1])}
		default:
			return fmt.Errorf("bad query %q (want \"name\" or \"name TYPE\")", s)
		}
		return nil
	}
	type alias DNSQuery
	a := alias{Type: "A"}
	if err := strictUnmarshal(b, &a); err != nil {
		return err
	}
	a.Type = strings.ToUpper(a.Type)
	*q = DNSQuery(a)
	return nil
}

func (q DNSQuery) String() string { return q.Name + "/" + q.Type }

// DNSConfig describes the upstream pool and how it is probed.
type DNSConfig struct {
	// Servers are upstream resolvers: "10.0.0.53", "10.0.0.53:5353", "[2001:db8::53]:53".
	Servers []string `json:"servers"`
	// Queries are resolved against every server on each probe round, except
	// servers that have their own list in ServerQueries.
	Queries []DNSQuery `json:"queries"`
	// ServerQueries gives individual servers their own probe queries, keyed by
	// the server as written in Servers.
	ServerQueries map[string][]DNSQuery `json:"server_queries,omitempty"`
	// ServerNames gives servers an optional name shown on the Topology page instead of the address,
	// keyed by the server as written in Servers (the same rules as a gateway's name).  It only labels:
	// it is left out of the pool's own configuration, so renaming never restarts a pool.
	ServerNames map[string]string `json:"server_names,omitempty"`
	// FallbackServers are a last resort, used only while no server in Servers is in service (all down or paused,
	// or none listed): the pool then answers from them, and goes back to Servers as soon as one of those answers
	// again.  Same address forms as Servers.  They are never probed (the domains the normal servers are tested with
	// may be ones only those know) and always count as up.
	FallbackServers []string `json:"fallback_servers,omitempty"`
	// PausedServers are listed servers that are taken out of service (no
	// queries, no probes) until resumed, e.g. during maintenance.  Replicated
	// with the rest of the DNS block.
	PausedServers []string `json:"paused_servers,omitempty"`
	// PausedQueries are probe domains taken out of testing on every node (shared), written "server|name|TYPE"
	// (see queryKey).  A paused domain is not asked, so it neither counts against its server nor shows red.
	PausedQueries []string `json:"paused_queries,omitempty"`
	// PausedServersHere / PausedQueriesHere are this node's own pauses of the same things (DaemonConfig keeps them,
	// effective() hands them to the pools).  Never replicated and never written from here.
	PausedServersHere []string `json:"-"`
	PausedQueriesHere []string `json:"-"`
	// DownPercent is the share of a server's probe queries that may fail before the server counts as
	// down: it is down when at least this percent of them fail (default 100, so only when every query fails; 50 would be 1 of 2 or 2 of 4); with
	// fewer failing it stays in use and shows as degraded.  A file that already has the key keeps its value.
	DownPercent int `json:"down_percent"`
	// Require is the old "all"/"any" switch, replaced by DownPercent.  It is still read (an old file has
	// it) but ignored, and never written back.
	Require string `json:"require,omitempty"`
	// ListenPort is the port the proxy serves on the VIP (UDP and TCP).
	ListenPort int `json:"listen_port"`
	// DoTPort serves DNS over TLS (RFC 7858) to clients on the VIP on this TCP port; 0 (the default) is off. The
	// certificate is the one the web GUI uses (Configure ▸ Web GUI).
	DoTPort int `json:"dot_port,omitempty"`
	// DoHPort serves DNS over HTTPS (RFC 8484) to clients on the VIP on this TCP port, at /dns-query; 0 (the
	// default) is off. The certificate is the web GUI's.
	DoHPort int `json:"doh_port,omitempty"`
	// ECS passes the client's network to the servers (EDNS Client Subnet,
	// RFC 7871) so they do not see every query coming from the ddgw node.  Only
	// a prefix is sent: ecs_prefix4 bits of an IPv4 client (default 24),
	// ecs_prefix6 bits of an IPv6 client (default 56).  On by default: a server that answers FORMERR or REFUSED to
	// it is asked again without it and then left alone (dns.go forward).
	ECS bool `json:"ecs"`
	// AllowedClients are the networks (10.0.0.0/8, 192.168.1.5, 2001:db8::/32) that may use the proxy; empty means
	// everyone. Any other client is answered REFUSED, dynamic updates included. The node itself always may.
	AllowedClients []string `json:"allowed_clients,omitempty"`
	// SortList orders the A and AAAA records of an answer per client network: "10.1.0.0/16: 10.1.0.0/16, 10.0.0.0/8"
	// (see sortlist.go). Empty means answers keep the order the servers gave.
	SortList []string `json:"sortlist,omitempty"`
	// SortListOn switches the sort list on (the default) or off without deleting it.
	SortListOn bool `json:"sortlist_on"`
	// Policy is Policy-Based Resolution: rows of client, name and servers; the first row that matches a query sends
	// it to its own servers instead of the pool's (see policy.go).
	Policy []PolicyRule `json:"policy,omitempty"`
	// PolicyOn switches the policy rows on (the default) or off without deleting them.
	PolicyOn bool `json:"policy_on"`
	// PolicyLog writes a log line for each query a policy row applies to (default true; at most 100 a second).
	PolicyLog bool `json:"policy_log"`
	// ClientRate limits each client (an IPv4 address or an IPv6 /64) to this many queries a second; 0 (the default)
	// is no limit. ClientBurst is how many it may send at once (0 = twice the rate, at least 10). ClientAction is what
	// a client over its rate gets: "drop" (the default, kept as empty in the file), "truncate" (UDP: a short answer
	// with the TC bit, so a real client retries over TCP) or "refused". ClientExempt are networks never limited.
	ClientRate   int      `json:"client_rate,omitempty"`
	ClientBurst  int      `json:"client_burst,omitempty"`
	ClientAction string   `json:"client_action,omitempty"`
	ClientExempt []string `json:"client_exempt,omitempty"`
	// ForwardUpdates relays dynamic DNS updates (RFC 2136, opcode UPDATE) to the primary
	// server named in the zone's SOA record, unchanged, and hands the answer back.  With it
	// off an update is answered REFUSED.  On by default.
	ForwardUpdates bool `json:"forward_updates"`
	// Cache answers repeated queries from memory for as long as their TTLs allow (cache.go); on by default.
	// CacheEntries is the most answers kept (the least recently used go first), CacheMaxTTL the most
	// seconds one is kept and shown to clients, however long its records say.
	// Spread shares the queries out: the servers within SpreadBand percent of the fastest one's latency
	// (default 20) take turns instead of the fastest getting everything; slower ones are still only fallbacks.
	Spread bool `json:"spread"`
	// TLSInsecure makes a tls:// server be accepted whatever certificate it presents (expired, self-signed, the
	// wrong name): the traffic is still encrypted but the server is not authenticated. Off by default.
	TLSInsecure     bool    `json:"tls_insecure,omitempty"`
	SpreadBand      int     `json:"spread_band"`
	Cache           bool    `json:"cache"`
	CacheEntries    int     `json:"cache_entries"`
	CacheMaxTTL     int     `json:"cache_max_ttl"`
	ECSPrefix4      int     `json:"ecs_prefix4"`
	ECSPrefix6      int     `json:"ecs_prefix6"`
	ProbeIntervalMS int     `json:"probe_interval_ms"`
	ProbeTimeoutMS  int     `json:"probe_timeout_ms"`
	QueryTimeoutMS  int     `json:"query_timeout_ms"`
	MaxAttempts     int     `json:"max_attempts"`
	FailThreshold   int     `json:"fail_threshold"`
	LatencyAlpha    float64 `json:"latency_alpha"`
	// LB is a gateway's own load-balancing settings (see LBConfig).  Only meaningful in a gateway's own pool; nil
	// means the gateway follows Settings.  A pointer with omitempty so a config that never used it stays readable
	// by older versions.
	LB *LBConfig `json:"lb,omitempty"`
}

// LBConfig is the set of load-balancing settings (Configure ▸ DNS proxy ▸ Load Balancing): how servers share the
// queries and when one counts as down.  A gateway that has its own pool follows the shared values unless its own
// pool carries an LBConfig.
type LBConfig struct {
	Spread        bool    `json:"spread"`
	SpreadBand    int     `json:"spread_band"`
	DownPercent   int     `json:"down_percent"`
	FailThreshold int     `json:"fail_threshold"`
	MaxAttempts   int     `json:"max_attempts"`
	LatencyAlpha  float64 `json:"latency_alpha"`
}

func (d DNSConfig) lbValues() LBConfig {
	return LBConfig{Spread: d.Spread, SpreadBand: d.SpreadBand, DownPercent: d.DownPercent, FailThreshold: d.FailThreshold, MaxAttempts: d.MaxAttempts, LatencyAlpha: d.LatencyAlpha}
}

func (d *DNSConfig) setLBValues(l LBConfig) {
	d.Spread, d.SpreadBand, d.DownPercent, d.FailThreshold, d.MaxAttempts, d.LatencyAlpha = l.Spread, l.SpreadBand, l.DownPercent, l.FailThreshold, l.MaxAttempts, l.LatencyAlpha
}

func (l LBConfig) validate() error {
	if l.SpreadBand < 1 || l.SpreadBand > 1000 {
		return errors.New("spread_band must be 1-1000 percent")
	}
	if l.DownPercent < 1 || l.DownPercent > 100 {
		return errors.New("down_percent must be 1-100")
	}
	if l.FailThreshold < 1 {
		return errors.New("fail_threshold must be >= 1")
	}
	if l.MaxAttempts < 1 {
		return errors.New("max_attempts must be >= 1")
	}
	if l.LatencyAlpha <= 0 || l.LatencyAlpha > 1 {
		return errors.New("latency_alpha must be in (0, 1]")
	}
	return nil
}

// ownLB says whether a gateway's own pool has load-balancing settings of its own, and what they are.  An explicit LB
// does.  So does a pool written before LB existed whose six values differ from the defaults: those were set on
// purpose (or copied from Settings when the pool was made), and the gateway keeps them.  A pool that sits at the
// defaults follows Settings, which is what its owner expects.
func (d *DNSConfig) ownLB() (LBConfig, bool) {
	if d.LB != nil {
		return *d.LB, true
	}
	def := defaultDNS()
	if l := d.lbValues(); l != def.lbValues() {
		return l, true
	}
	return LBConfig{}, false
}

func defaultDNS() DNSConfig {
	return DNSConfig{
		Servers:         []string{},
		Queries:         []DNSQuery{},
		DownPercent:     100,
		ListenPort:      53,
		ForwardUpdates:  true,
		SortListOn:      true,
		PolicyOn:        true,
		PolicyLog:       true,
		Spread:          true,
		SpreadBand:      20,
		Cache:           true,
		CacheEntries:    cacheDefaultEntries,
		CacheMaxTTL:     cacheDefaultMaxTTL,
		ECS:             true,
		ECSPrefix4:      24,
		ECSPrefix6:      56,
		ProbeIntervalMS: 5000,
		ProbeTimeoutMS:  1000,
		QueryTimeoutMS:  1500,
		MaxAttempts:     3,
		FailThreshold:   2,
		LatencyAlpha:    0.3,
	}
}

func (d *DNSConfig) UnmarshalJSON(b []byte) error {
	type alias DNSConfig
	a := alias(defaultDNS())
	if err := strictUnmarshal(b, &a); err != nil {
		return err
	}
	a.Require = "" // superseded by down_percent
	*d = DNSConfig(a)
	return nil
}

func (d DNSConfig) probeInterval() time.Duration {
	return time.Duration(d.ProbeIntervalMS) * time.Millisecond
}
func (d DNSConfig) probeTimeout() time.Duration {
	return time.Duration(d.ProbeTimeoutMS) * time.Millisecond
}
func (d DNSConfig) queryTimeout() time.Duration {
	return time.Duration(d.QueryTimeoutMS) * time.Millisecond
}

// normalizeServer turns a server string into a canonical "host:port".  The
// host is an IP address or a DNS name (resolved each time it is probed).
func normalizeServer(s string) (string, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, dotScheme); ok {
		// DNS over TLS: "tls://host[:port]", port 853 by default; the key keeps the scheme.
		hp, err := normalizePlain(rest, "853")
		if err != nil {
			return "", fmt.Errorf("invalid dns server %q (need tls://host or tls://host:port)", s)
		}
		return dotScheme + hp, nil
	}
	if rest, ok := strings.CutPrefix(s, dohScheme); ok {
		// DNS over HTTPS: "https://host[:port][/path]", port 443 and path /dns-query by default.
		n, err := normalizeDoH(rest)
		if err != nil {
			return "", fmt.Errorf("invalid dns server %q (need https://host, https://host:port or https://host/path)", s)
		}
		return n, nil
	}
	return normalizePlain(s, "53")
}

// dotScheme marks a server that is spoken to over TLS (RFC 7858).
const dotScheme = "tls://"

func normalizePlain(s, defPort string) (string, error) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.String(), nil
	}
	t := strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	if a, err := netip.ParseAddr(t); err == nil {
		return net.JoinHostPort(a.String(), defPort), nil
	}
	host, port := s, defPort
	if h, p, err := net.SplitHostPort(s); err == nil {
		host, port = h, p
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid dns server %q (bad port)", s)
		}
	}
	if validHostname(host) {
		return net.JoinHostPort(strings.ToLower(host), port), nil
	}
	return "", fmt.Errorf("invalid dns server %q (need an IP address or host name, optionally with :port)", s)
}

func validHostname(h string) bool {
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 {
		return false
	}
	for _, l := range strings.Split(h, ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

// queriesFor returns the probe queries that apply to a (normalised) server.
func (d DNSConfig) queriesFor(addr string) []DNSQuery {
	if q, ok := d.ServerQueries[addr]; ok {
		return q
	}
	return d.Queries
}

func (d *DNSConfig) Validate() error {
	// No servers is valid: a gateway just drawn has none yet, and its proxy answers
	// SERVFAIL until the first one is added.
	seen := map[string]bool{}
	for i, s := range d.Servers {
		n, err := normalizeServer(s)
		if err != nil {
			return fmt.Errorf("dns: %w", err)
		}
		if seen[n] {
			return fmt.Errorf("dns: duplicate server %q", s)
		}
		seen[n] = true
		d.Servers[i] = n
	}
	if len(d.FallbackServers) > 0 {
		fb := make([]string, 0, len(d.FallbackServers))
		for _, s := range d.FallbackServers {
			n, err := normalizeServer(s)
			if err != nil {
				return fmt.Errorf("dns: fallback server: %w", err)
			}
			if seen[n] {
				return fmt.Errorf("dns: fallback server %q is also a normal server", s)
			}
			seen[n] = true // for the duplicate check only; the maps below look at Servers
			fb = append(fb, n)
		}
		d.FallbackServers = fb
		for _, n := range fb {
			delete(seen, n)
		}
	} else {
		d.FallbackServers = nil
	}
	if len(d.PausedServers) > 0 { // keep only real, distinct servers
		keep := []string{}
		done := map[string]bool{}
		for _, p := range d.PausedServers {
			if n, err := normalizeServer(p); err == nil && seen[n] && !done[n] {
				done[n] = true
				keep = append(keep, n)
			}
		}
		d.PausedServers = keep
		if len(keep) == 0 {
			d.PausedServers = nil
		}
	}
	d.PausedQueries = keepQueries(d.PausedQueries, d.validQueryKeys())
	for k, qs := range d.ServerQueries {
		n, err := normalizeServer(k)
		if err != nil || !seen[n] {
			return fmt.Errorf("dns: server_queries entry %q is not one of the servers", k)
		}
		if n != k {
			delete(d.ServerQueries, k)
			d.ServerQueries[n] = qs
		}
	}
	names := map[string]string{}
	for k, name := range d.ServerNames {
		n, err := normalizeServer(k)
		if err != nil || !seen[n] {
			return fmt.Errorf("dns: server_names entry %q is not one of the servers", k)
		}
		name = strings.TrimSpace(name)
		if err := validGatewayName(name); err != nil {
			return fmt.Errorf("dns: name of server %s: %w", k, err)
		}
		if name != "" {
			names[n] = name
		}
	}
	d.ServerNames = names
	if len(names) == 0 {
		d.ServerNames = nil
	}
	for _, sv := range d.Servers {
		qs := d.queriesFor(sv)
		if len(qs) == 0 {
			return fmt.Errorf("dns: server %s has no probe queries (a server can only be verified by a query)", sv)
		}
		for _, q := range qs {
			if _, err := buildQuery(0, q.Name, q.Type); err != nil {
				return fmt.Errorf("dns: query %s: %w", q, err)
			}
		}
	}
	if d.DownPercent < 1 || d.DownPercent > 100 {
		return errors.New("dns: down_percent must be 1-100")
	}
	if d.ListenPort < 1 || d.ListenPort > 65535 {
		return errors.New("dns: listen_port must be 1-65535")
	}
	if d.DoTPort < 0 || d.DoTPort > 65535 {
		return errors.New("dns: dot_port must be 0 (off) or 1-65535")
	}
	if d.DoHPort < 0 || d.DoHPort > 65535 {
		return errors.New("dns: doh_port must be 0 (off) or 1-65535")
	}
	if d.DoHPort != 0 && (d.DoHPort == d.ListenPort || d.DoHPort == d.DoTPort) {
		return errors.New("dns: doh_port must differ from listen_port and dot_port (all use TCP)")
	}
	if d.DoTPort != 0 && d.DoTPort == d.ListenPort {
		return errors.New("dns: dot_port must differ from listen_port (both use TCP)")
	}
	for _, f := range []struct {
		name string
		v    int
	}{
		{"probe_interval_ms", d.ProbeIntervalMS}, {"probe_timeout_ms", d.ProbeTimeoutMS},
		{"query_timeout_ms", d.QueryTimeoutMS}, {"max_attempts", d.MaxAttempts},
		{"fail_threshold", d.FailThreshold},
	} {
		if f.v < 1 {
			return fmt.Errorf("dns: %s must be >= 1", f.name)
		}
	}
	if d.CacheEntries < cacheMinEntries || d.CacheEntries > cacheMaxEntries {
		return fmt.Errorf("dns: cache_entries must be %d-%d", cacheMinEntries, cacheMaxEntries)
	}
	if d.CacheMaxTTL < 1 || d.CacheMaxTTL > cacheMaxTTLLimit {
		return fmt.Errorf("dns: cache_max_ttl must be 1-%d seconds", cacheMaxTTLLimit)
	}
	if d.SpreadBand < 1 || d.SpreadBand > 1000 {
		return errors.New("dns: spread_band must be 1-1000 percent")
	}
	if err := d.validateClients(); err != nil {
		return err
	}
	sl, err := normalizeSortList(d.SortList)
	if err != nil {
		return err
	}
	d.SortList = sl
	pol, err := normalizePolicy(d.Policy)
	if err != nil {
		return err
	}
	d.Policy = pol
	if d.ECSPrefix4 < 1 || d.ECSPrefix4 > 32 {
		return errors.New("dns: ecs_prefix4 must be 1-32")
	}
	if d.ECSPrefix6 < 1 || d.ECSPrefix6 > 128 {
		return errors.New("dns: ecs_prefix6 must be 1-128")
	}
	if d.LatencyAlpha <= 0 || d.LatencyAlpha > 1 {
		return errors.New("dns: latency_alpha must be in (0, 1]")
	}
	if d.LB != nil {
		if err := d.LB.validate(); err != nil {
			return fmt.Errorf("dns: lb: %w", err)
		}
	}
	return nil
}

// ── Daemon config ────────────────────────────────────────────────────────────

type DaemonConfig struct {
	LogLevel string        `json:"log_level"`
	Groups   []GroupConfig `json:"groups"`
	DNS      DNSConfig     `json:"dns"`
	Web      WebConfig     `json:"web"`
	Cluster  ClusterConfig `json:"cluster"`
	// BGP is this node's FRR/BGP settings (see bgp.go).  A pointer with omitempty so
	// a config that never used BGP has no "bgp" key and older versions still read it.
	BGP *BGPConfig `json:"bgp,omitempty"`
	// NodePaused takes the whole node out of service for maintenance, the same as
	// pausing every gateway on it (present and future): it resigns, stops answering
	// and stops probing, and the other nodes carry on.  Local to the node, never
	// replicated; omitempty so a config that never used it stays readable by older
	// versions.
	NodePaused bool `json:"node_paused,omitempty"`
	// PausedServersHere / PausedQueriesHere are the DNS servers and probe domains paused on this node only (the
	// shared lists live in the DNS block).  Local, never replicated.
	PausedServersHere []string `json:"paused_servers_here,omitempty"`
	PausedQueriesHere []string `json:"paused_queries_here,omitempty"`
}

// effective is the configuration the supervisor runs: with the node paused every
// gateway is paused.  The file keeps each gateway's own flag, so resuming the
// node puts back exactly what was paused before.
func (dc *DaemonConfig) effective() *DaemonConfig {
	if dc == nil {
		return dc
	}
	c := *dc
	c.Groups = append([]GroupConfig(nil), dc.Groups...)
	here := func(d DNSConfig) DNSConfig {
		d.PausedServersHere, d.PausedQueriesHere = dc.PausedServersHere, dc.PausedQueriesHere
		return d
	}
	c.DNS = here(c.DNS)
	for i := range c.Groups {
		if dc.NodePaused || c.Groups[i].PausedAll { // paused on every node, or this whole node is
			c.Groups[i].Paused = true
		}
		if id := localNodeID(); id != "" && containsStr(c.Groups[i].ExcludedNodes, id) { // removed from this gateway: it serves nothing here
			c.Groups[i].Paused, c.Groups[i].ExcludedHere = true, true
		}
		if c.Groups[i].DNS != nil {
			d := here(*c.Groups[i].DNS)
			c.Groups[i].DNS = &d
		}
	}
	return &c
}

func newDaemonConfig() *DaemonConfig {
	return &DaemonConfig{LogLevel: "info", Groups: []GroupConfig{newGatewayGroup()},
		DNS: defaultDNS(), Web: defaultWeb(), Cluster: defaultCluster()}
}

func (dc *DaemonConfig) UnmarshalJSON(b []byte) error {
	type alias DaemonConfig
	a := alias(*newDaemonConfig())
	a.Groups = nil
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	// A missing "groups" key means the default group; an explicit empty list
	// really is "no gateways" (what deleting the last canvas produces).
	var probe struct {
		Groups json.RawMessage `json:"groups"`
	}
	_ = json.Unmarshal(b, &probe)
	if a.Groups == nil && (len(probe.Groups) == 0 || string(probe.Groups) == "null") {
		a.Groups = []GroupConfig{newGatewayGroup()}
	}
	if a.Groups == nil {
		a.Groups = []GroupConfig{}
	}
	*dc = DaemonConfig(a)
	return nil
}

// poolFor returns the pool key and DNS settings for a group: its own pool
// (keyed by group id) or the shared one (key 0).
func (dc *DaemonConfig) poolFor(g *GroupConfig) (int, DNSConfig) {
	if g.DNS != nil {
		c := *g.DNS
		lb, own := g.DNS.ownLB()
		if !own {
			lb = dc.DNS.lbValues() // follows Settings
		}
		c.setLBValues(lb)
		c.LB = nil
		c.followSettings(dc.DNS)
		return g.GroupID, c
	}
	return 0, dc.DNS
}

// followSettings makes c use the answer cache and client rules (allowed clients, rate, burst, action, exemptions)
// of the shared Settings.  They are edited only on the Settings page, so a gateway's own pool must not keep the copy
// it was made with: it would go on caching, or limiting, after Settings said otherwise.
func (c *DNSConfig) followSettings(shared DNSConfig) {
	c.Cache, c.CacheEntries, c.CacheMaxTTL = shared.Cache, shared.CacheEntries, shared.CacheMaxTTL
	c.AllowedClients = append([]string(nil), shared.AllowedClients...)
	if len(c.AllowedClients) == 0 {
		c.AllowedClients = nil
	}
	c.ClientExempt = append([]string(nil), shared.ClientExempt...)
	if len(c.ClientExempt) == 0 {
		c.ClientExempt = nil
	}
	c.PolicyOn, c.PolicyLog = shared.PolicyOn, shared.PolicyLog
	c.Policy = clonePolicy(shared.Policy)
	c.SortListOn = shared.SortListOn
	c.SortList = append([]string(nil), shared.SortList...)
	if len(c.SortList) == 0 {
		c.SortList = nil
	}
	c.ClientRate, c.ClientBurst, c.ClientAction = shared.ClientRate, shared.ClientBurst, shared.ClientAction
}

func (dc *DaemonConfig) Validate() error {
	ids := map[int]bool{}
	for i := range dc.Groups {
		g := &dc.Groups[i]
		if err := g.Validate(); err != nil {
			return err
		}
		if ids[g.GroupID] {
			return fmt.Errorf("duplicate group_id %d", g.GroupID)
		}
		ids[g.GroupID] = true
	}
	// two gateways cannot own the same shared address, and an anycast address is not a gateway's shared address; the same
	// anycast address on several gateways is how an anycast service is run (it is held while any of them can answer)
	vips := map[string]int{}
	norm := func(v string) string {
		if p, err := netip.ParsePrefix(v); err == nil {
			return p.Addr().String()
		}
		return v
	}
	for _, g := range dc.Groups {
		for _, v := range append(append([]string{g.VIP4, g.VIP6}, g.MoreVIP4...), g.MoreVIP6...) {
			if v == "" {
				continue
			}
			a := norm(v)
			if other, dup := vips[a]; dup {
				return fmt.Errorf("groups %d and %d both use the shared address %s", other, g.GroupID, a)
			}
			vips[a] = g.GroupID
		}
	}
	for _, g := range dc.Groups {
		seen := map[string]bool{}
		for _, v := range g.ExtraVIPs {
			a := norm(v)
			if other, dup := vips[a]; dup {
				return fmt.Errorf("group %d: anycast address %s is the shared address of group %d", g.GroupID, a, other)
			}
			if seen[a] {
				return fmt.Errorf("group %d lists the anycast address %s twice", g.GroupID, a)
			}
			seen[a] = true
		}
	}
	// this node's own pauses must still name a server / probe domain that exists somewhere
	hereServers, hereQueries := map[string]bool{}, map[string]bool{}
	blocks := []*DNSConfig{&dc.DNS}
	for i := range dc.Groups {
		if dc.Groups[i].DNS != nil {
			blocks = append(blocks, dc.Groups[i].DNS)
		}
	}
	for _, b := range blocks {
		for _, sv := range b.Servers {
			hereServers[sv] = true
		}
		for k := range b.validQueryKeys() {
			hereQueries[k] = true
		}
	}
	dc.PausedServersHere = keepQueries(dc.PausedServersHere, hereServers)
	dc.PausedQueriesHere = keepQueries(dc.PausedQueriesHere, hereQueries)
	switch strings.ToLower(dc.LogLevel) {
	case "debug", "info", "warning", "error":
	default:
		return fmt.Errorf("log_level %q must be debug|info|warning|error", dc.LogLevel)
	}
	if dc.DNS.LB != nil {
		return errors.New("dns: lb belongs to a gateway's own pool (the shared settings are the load-balancing settings themselves)")
	}
	needGlobal := false
	for i := range dc.Groups {
		g := &dc.Groups[i]
		g.DNSProxy = true // always on, see GroupConfig.DNSProxy
		if g.DNS == nil {
			needGlobal = true
			continue
		}
		if err := g.DNS.Validate(); err != nil {
			return fmt.Errorf("group %d: %w", g.GroupID, err)
		}
	}
	if needGlobal {
		if err := dc.DNS.Validate(); err != nil {
			return err
		}
	}
	if err := dc.Web.Validate(); err != nil {
		return err
	}
	if dc.BGP != nil {
		if err := dc.BGP.Validate(); err != nil {
			return err
		}
	}
	return dc.Cluster.Validate()
}

func loadConfig(path string) (*DaemonConfig, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// No file yet: defaults, and no gateways (a gateway is something you draw;
		// inventing one would claim an address on the network).
		dc := newDaemonConfig()
		dc.Groups = []GroupConfig{}
		return dc, nil
	}
	if err != nil {
		return nil, err
	}
	dc := newDaemonConfig()
	if err := json.Unmarshal(b, dc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return dc, nil
}

func (dc *DaemonConfig) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if dc.Groups == nil {
		dc.Groups = []GroupConfig{}
	}
	b, err := json.MarshalIndent(dc, "", "  ")
	if err != nil {
		return err
	}
	// The file holds the groups' shared HMAC keys: keep the mode of an existing
	// file (other tools may read it), and create new files private.
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil { // WriteFile is subject to umask
		return err
	}
	return os.Rename(tmp, path)
}

// ── Web GUI config ───────────────────────────────────────────────────────────

// WebConfig describes the HTTPS management GUI.
type WebConfig struct {
	// LegacyEnabled is the old "enabled" switch: the GUI is always on now.  The
	// key is still accepted so existing config files load; it is dropped on save.
	LegacyEnabled *bool `json:"enabled,omitempty"`
	// Listen is the address:port to serve HTTPS on (default ":53853").
	Listen string `json:"listen"`
	// CertFile/KeyFile: PEM certificate and key.  Leave both empty to use an
	// auto-generated self-signed certificate stored next to the config file.
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
	// Group is the system group whose members may log in.
	Group string `json:"group"`
	// PAMService is the /etc/pam.d service used to check passwords.
	PAMService string `json:"pam_service"`
	// SessionIdleMinutes logs a browser out after this much inactivity.
	SessionIdleMinutes int `json:"session_idle_minutes"`
	// Failed-login lockout: MaxFailedLogins wrong attempts within
	// FailedLoginWindowMinutes lock that address (and that user name) out for
	// LockoutMinutes.
	MaxFailedLogins          int `json:"max_failed_logins"`
	FailedLoginWindowMinutes int `json:"failed_login_window_minutes"`
	LockoutMinutes           int `json:"lockout_minutes"`
	// MinPasswordLength is the fewest characters a password set on the Users page (or with --user-add and
	// --user-password) may have.  0, which is also what an absent key means and what is not written to the file
	// (so a config that never set it stays readable by older versions), is the default, 8.
	MinPasswordLength int `json:"min_password_length,omitempty"`
}

const (
	defaultWebPort       = 53853
	defaultMinPassword   = 8
	maxMinPasswordLength = 128
)

// minPassword is the effective minimum password length.
func (w WebConfig) minPassword() int {
	if w.MinPasswordLength <= 0 {
		return defaultMinPassword
	}
	return w.MinPasswordLength
}

func defaultWeb() WebConfig {
	return WebConfig{
		Listen:                   fmt.Sprintf(":%d", defaultWebPort),
		Group:                    "ddgw",
		PAMService:               "ddgw",
		SessionIdleMinutes:       30,
		MaxFailedLogins:          3,
		FailedLoginWindowMinutes: 1,
		LockoutMinutes:           15,
	}
}

func (w *WebConfig) UnmarshalJSON(b []byte) error {
	type alias WebConfig
	a := alias(defaultWeb())
	if err := strictUnmarshal(b, &a); err != nil {
		return err
	}
	a.LegacyEnabled = nil
	*w = WebConfig(a)
	return nil
}

func (w *WebConfig) Validate() error {
	_, port, err := net.SplitHostPort(w.Listen)
	if err != nil {
		return fmt.Errorf("web: listen %q must be host:port, e.g. \":53853\"", w.Listen)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("web: listen port %q must be 1-65535", port)
	}
	if (w.CertFile == "") != (w.KeyFile == "") {
		return errors.New("web: cert_file and key_file must be set together (or both empty for a self-signed certificate)")
	}
	if w.Group == "" || w.PAMService == "" {
		return errors.New("web: group and pam_service must not be empty")
	}
	if w.SessionIdleMinutes < 1 {
		return errors.New("web: session_idle_minutes must be >= 1")
	}
	if w.MaxFailedLogins < 1 || w.MaxFailedLogins > 1000 {
		return errors.New("web: max_failed_logins must be 1-1000")
	}
	if w.FailedLoginWindowMinutes < 1 || w.FailedLoginWindowMinutes > 10080 {
		return errors.New("web: failed_login_window_minutes must be 1-10080")
	}
	if w.LockoutMinutes < 1 || w.LockoutMinutes > 10080 {
		return errors.New("web: lockout_minutes must be 1-10080")
	}
	if w.MinPasswordLength < 0 || w.MinPasswordLength > maxMinPasswordLength {
		return fmt.Errorf("web: min_password_length must be 1-%d (empty or 0 means the default, %d)", maxMinPasswordLength, defaultMinPassword)
	}
	return nil
}

func (w WebConfig) failWindow() time.Duration {
	return time.Duration(w.FailedLoginWindowMinutes) * time.Minute
}

func (w WebConfig) lockout() time.Duration { return time.Duration(w.LockoutMinutes) * time.Minute }

func (w WebConfig) sessionIdle() time.Duration {
	return time.Duration(w.SessionIdleMinutes) * time.Minute
}

// ── Management cluster config ────────────────────────────────────────────────

// ClusterConfig describes this node's membership of the management cluster
// (config replication, shared certificate, coordinated updates).  It is
// per-node and never replicated.  The gateway protocol itself (AGC/AFN
// election) is independent of it.
type ClusterConfig struct {
	// Enabled starts the cluster listener.  Always true (the key is only kept so
	// older files still load).  Every node is the primary of its own
	// single-node cluster until it joins another.
	Enabled bool `json:"-"`
	// Listen is the address:port of the peer-to-peer listener (default ":53854").
	Listen string `json:"listen"`
	// Self is the host:port other nodes use to reach this one.  Empty means
	// "this host's name and the listen port".
	Self string `json:"self"`
	// SyncIntervalSec is how often replicas pull state and peers are polled.
	SyncIntervalSec int `json:"sync_interval_sec"`
	// ShareCert replicates the certificate installed through the GUI/CLI to
	// every member.  Always true, like Enabled.
	ShareCert bool `json:"-"`
}

const defaultClusterPort = 53854

func defaultCluster() ClusterConfig {
	return ClusterConfig{
		Enabled:         true,
		Listen:          fmt.Sprintf(":%d", defaultClusterPort),
		SyncIntervalSec: 5,
		ShareCert:       true,
	}
}

func (c *ClusterConfig) UnmarshalJSON(b []byte) error {
	type alias ClusterConfig
	a := struct {
		alias
		LegacyEnabled   *bool `json:"enabled"`
		LegacyShareCert *bool `json:"share_cert"`
	}{alias: alias(defaultCluster())}
	if err := strictUnmarshal(b, &a); err != nil {
		return err
	}
	*c = ClusterConfig(a.alias)
	// Clustering and certificate sharing are not options any more.  Older files
	// still carry the keys: accepted, ignored, and not written back.
	c.Enabled, c.ShareCert = true, true
	return nil
}

func (c *ClusterConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if _, port, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("cluster: listen %q must be host:port, e.g. \":53854\"", c.Listen)
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("cluster: listen port %q must be 1-65535", port)
	}
	if c.Self != "" {
		if _, _, err := net.SplitHostPort(c.Self); err != nil {
			return fmt.Errorf("cluster: self %q must be host:port", c.Self)
		}
	}
	if c.SyncIntervalSec < 1 || c.SyncIntervalSec > 3600 {
		return errors.New("cluster: sync_interval_sec must be 1-3600")
	}
	return nil
}

// isPaused reports whether the server (as written in Servers) is paused, on every node or on this one.
func (d *DNSConfig) isPaused(addr string) bool { return d.serverPausedScope(addr) != "" }

// serverPausedScope says where the server is paused: "all" (every node), "node" (this one) or "" (not paused).
func (d *DNSConfig) serverPausedScope(addr string) string {
	if containsStr(d.PausedServers, addr) {
		return "all"
	}
	if containsStr(d.PausedServersHere, addr) {
		return "node"
	}
	return ""
}

// queryKey names one probe domain of one server in the paused lists.
func queryKey(server, name, typ string) string {
	return server + "|" + strings.ToLower(strings.TrimSuffix(name, ".")) + "|" + strings.ToUpper(typ)
}

// queryPausedScope says where the server's probe domain is paused: "all", "node" or "".
func (d *DNSConfig) queryPausedScope(server string, q DNSQuery) string {
	k := queryKey(server, q.Name, q.Type)
	if containsStr(d.PausedQueries, k) {
		return "all"
	}
	if containsStr(d.PausedQueriesHere, k) {
		return "node"
	}
	return ""
}

// pausedQueryCount is how many of the server's probe domains are paused, and how many it has.
func (d *DNSConfig) pausedQueryCount(server string) (paused, total int) {
	qs := d.queriesFor(server)
	for _, q := range qs {
		if d.queryPausedScope(server, q) != "" {
			paused++
		}
	}
	return paused, len(qs)
}

// allQueriesPaused says the server has probe domains and every one is paused: it cannot be verified, so it counts as down.
func (d *DNSConfig) allQueriesPaused(server string) bool {
	p, n := d.pausedQueryCount(server)
	return n > 0 && p == n
}

// validQueryKeys lists the paused-list keys of every probe domain this block has.
func (d *DNSConfig) validQueryKeys() map[string]bool {
	m := map[string]bool{}
	for _, sv := range d.Servers {
		for _, q := range d.queriesFor(sv) {
			m[queryKey(sv, q.Name, q.Type)] = true
		}
	}
	return m
}

// keepQueries returns the distinct entries of l that are in valid (nil when none).
func keepQueries(l []string, valid map[string]bool) []string {
	var out []string
	for _, k := range l {
		if valid[k] && !containsStr(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// live is the configuration the probe pool runs with: paused servers left out.
func (d DNSConfig) live() DNSConfig {
	d.ServerNames = nil // labels only: a rename must not restart the pool
	if len(d.PausedServers) != 0 || len(d.PausedServersHere) != 0 {
		var up []string
		for _, s := range d.Servers {
			if !d.isPaused(s) {
				up = append(up, s)
			}
		}
		d.Servers = up
	}
	// paused probe domains are not asked; a server with every one of them paused cannot be verified, so it counts as
	// down and is left out of the pool like a paused server
	if len(d.PausedQueries) != 0 || len(d.PausedQueriesHere) != 0 {
		var up []string
		for _, s := range d.Servers {
			if !d.allQueriesPaused(s) {
				up = append(up, s)
			}
		}
		d.Servers = up
		sq := make(map[string][]DNSQuery, len(d.ServerQueries)+len(d.Servers))
		for k, v := range d.ServerQueries {
			sq[k] = v
		}
		for _, sv := range d.Servers {
			all := d.queriesFor(sv)
			var kept []DNSQuery
			for _, q := range all {
				if d.queryPausedScope(sv, q) == "" {
					kept = append(kept, q)
				}
			}
			if len(kept) > 0 && len(kept) < len(all) {
				sq[sv] = kept
			}
		}
		d.ServerQueries = sq
	}
	d.PausedServers, d.PausedServersHere, d.PausedQueries, d.PausedQueriesHere = nil, nil, nil, nil
	return d
}

// maxGatewayName is the longest label a gateway may carry.
const maxGatewayName = 40

// validGatewayName accepts an empty name or up to 40 characters with no control characters.
func validGatewayName(n string) error {
	if utf8.RuneCountInString(n) > maxGatewayName {
		return fmt.Errorf("name is longer than %d characters", maxGatewayName)
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return errors.New("name must not contain control characters")
		}
	}
	return nil
}

// validateClients checks and normalises the client list, exemptions, rate and action (in place).
func (d *DNSConfig) validateClients() error {
	for _, f := range []struct {
		name string
		list *[]string
	}{{"allowed_clients", &d.AllowedClients}, {"client_exempt", &d.ClientExempt}} {
		if len(*f.list) == 0 {
			*f.list = nil
			continue
		}
		nets, err := parseClientNets(*f.list)
		if err != nil {
			return fmt.Errorf("dns: %s: %w", f.name, err)
		}
		norm := make([]string, 0, len(nets))
		seen := map[string]bool{}
		for _, n := range nets {
			if s := n.String(); !seen[s] {
				seen[s] = true
				norm = append(norm, s)
			}
		}
		*f.list = norm
	}
	if d.ClientRate < 0 || d.ClientRate > 10_000_000 {
		return errors.New("dns: client_rate must be 0-10000000 queries per second")
	}
	if d.ClientBurst < 0 || d.ClientBurst > 100_000_000 {
		return errors.New("dns: client_burst must be 0-100000000")
	}
	switch d.ClientAction {
	case "", actDrop:
		d.ClientAction = "" // the default is not written
	case actTruncate, actRefused:
	default:
		return fmt.Errorf("dns: client_action must be drop, truncate or refused, not %q", d.ClientAction)
	}
	return nil
}

// keepAnycast returns the distinct members of paused that are still anycast addresses of the gateway, normalised
// (nil when none): deleting or editing an address drops its pause.
func keepAnycast(paused, extra []string) []string {
	var out []string
	for _, p := range paused {
		n, err := normalizeAnycast(p)
		if err != nil || !containsStr(extra, n) || containsStr(out, n) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// pausedAnycast says, per anycast address of the gateway, where its announcing is paused: "all" (every node) or
// "node" (this one).
func (g GroupConfig) pausedAnycast() map[string]string {
	out := map[string]string{}
	for _, a := range g.PausedVIPsHere {
		out[a] = "node"
	}
	for _, a := range g.PausedVIPs {
		out[a] = "all"
	}
	return out
}
