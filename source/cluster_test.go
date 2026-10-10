package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

type tnode struct {
	t    *testing.T
	name string
	dir  string
	conf string
	addr string
	mg   *Mgmt
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func newTNode(t *testing.T, name string) *tnode {
	t.Helper()
	d := t.TempDir()
	n := &tnode{t: t, name: name, dir: d, conf: filepath.Join(d, "etc", "ddgw.conf"), addr: freeAddr(t)}
	dc := newDaemonConfig()
	dc.Cluster.Listen, dc.Cluster.Self, dc.Cluster.SyncIntervalSec = n.addr, n.addr, 1
	dc.Groups[0].Interface = "lo"
	dc.Groups[0].Priority = 100
	if err := dc.save(n.conf); err != nil {
		t.Fatal(err)
	}
	mg, err := NewMgmt(n.conf, filepath.Join(d, "state"), dc)
	if err != nil {
		t.Fatal(err)
	}
	n.mg = mg
	mg.reloadFn = func() error {
		nu, err := loadConfig(n.conf)
		if err != nil {
			return err
		}
		if err := nu.Validate(); err != nil {
			return err
		}
		mg.OnConfigLoaded(nu)
		return nil
	}
	mg.reloadFn()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", n.addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: cluster listener never came up", name)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(mg.cl.Stop)
	return n
}

func (n *tnode) cfg() *DaemonConfig {
	dc, err := loadConfig(n.conf)
	if err != nil {
		n.t.Fatal(err)
	}
	return dc
}

func (n *tnode) sync() {
	n.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := n.mg.cl.SyncOnce(ctx); err != nil {
		n.t.Fatalf("%s sync: %v", n.name, err)
	}
}

func (n *tnode) join(via *tnode) {
	n.t.Helper()
	code, _, err := via.mg.cl.MintJoinCode("test")
	if err != nil {
		n.t.Fatal(err)
	}
	if err := n.mg.cl.Join(context.Background(), code, "test"); err != nil {
		n.t.Fatalf("%s join via %s: %v", n.name, via.name, err)
	}
	seen := map[string]bool{}
	for _, p := range n.mg.cl.Snapshot().Peers {
		if seen[p.Addr] {
			n.t.Fatalf("%s lists peer %s twice after joining", n.name, p.Addr)
		}
		seen[p.Addr] = true
	}
}

func hasPeer(n *tnode, addr string) bool {
	for _, p := range n.mg.cl.Snapshot().Peers {
		if p.Addr == addr {
			return true
		}
	}
	return false
}

func TestClusterJoinSyncPromote(t *testing.T) {
	failDelay = 0
	a, b := newTNode(t, "A"), newTNode(t, "B")

	if s := a.mg.cl.Snapshot(); s.Role != RolePrimary || s.Epoch != 1 {
		t.Fatalf("a fresh node is a primary at epoch 1: %+v", s)
	}
	// seed distinct local settings
	bc := b.cfg()
	bc.Groups[0].Priority = 50
	bc.Groups[0].Interface = "lo"
	bc.save(b.conf)
	b.mg.reloadFn()

	a.join2(t, b)
	if s := b.mg.cl.Snapshot(); s.Role != RoleReplica || s.PrimaryAddr != a.addr {
		t.Fatalf("B must be a replica of A: %+v", s)
	}
	if !hasPeer(a, b.addr) {
		t.Fatal("A must know B after the join")
	}
	// a node that already belongs to a cluster can't join another
	code, _, _ := a.mg.cl.MintJoinCode("t")
	if err := b.mg.cl.Join(context.Background(), code, "t"); err != ErrNotJoinable {
		t.Fatalf("second join: %v", err)
	}

	// shared settings flow from the primary; local ones stay
	ac := a.cfg()
	ac.DNS.Servers = []string{"10.1.1.1", "10.1.1.2"}
	ac.DNS.Queries = []DNSQuery{{"example.com", "A"}}
	ac.Groups[0].Key = "cluster-key"
	ac.Groups[0].DNSProxy = true
	ac.Groups[0].Priority = 200 // local to A
	if err := a.mg.PutConfig(ac, "alice", "set upstreams"); err != nil {
		t.Fatal(err)
	}
	b.sync()
	got := b.cfg()
	if got.Groups[0].Key != "cluster-key" || !got.Groups[0].DNSProxy || len(got.DNS.Servers) != 2 {
		t.Fatalf("shared settings not replicated: %+v", got.Groups[0])
	}
	if got.Groups[0].Priority != 50 {
		t.Fatalf("local priority must not be overwritten: %d", got.Groups[0].Priority)
	}
	vl := b.mg.VersionsList()
	if len(vl) == 0 || vl[0].Actor != "cluster sync" {
		t.Fatalf("the synced change must be recorded in B's history: %+v", vl)
	}

	// a replica's edit of shared settings goes through the primary
	bc = b.cfg()
	bc.Groups[0].HelloMS = 400
	bc.Groups[0].Priority = 60
	if err := b.mg.PutConfig(bc, "bob", ""); err != nil {
		t.Fatal(err)
	}
	if a.cfg().Groups[0].HelloMS != 400 {
		t.Fatal("the primary must have accepted the forwarded shared change")
	}
	if a.cfg().Groups[0].Priority != 200 {
		t.Fatal("a replica's local field must not leak to the primary")
	}
	// an invalid shared change is refused by the primary and leaves B untouched
	bc = b.cfg()
	bc.Groups[0].HelloMS = 400
	bc.DNS.Servers = []string{"not an ip!"}
	before := b.cfg().DNS.Servers
	if err := b.mg.PutConfig(bc, "bob", ""); err == nil {
		t.Fatal("invalid dns server must be refused")
	}
	if strings.Join(b.cfg().DNS.Servers, ",") != strings.Join(before, ",") {
		t.Fatal("a refused change must not be saved locally")
	}

	// third node joins through the replica B
	c := newTNode(t, "C")
	c.join(b)
	c.sync()
	if c.cfg().Groups[0].Key != "cluster-key" {
		t.Fatal("C must pull the shared settings from the primary")
	}
	a.sync()
	b.sync()
	for _, pair := range [][2]*tnode{{a, c}, {b, c}, {c, a}, {c, b}, {a, b}, {b, a}} {
		if !hasPeer(pair[0], pair[1].addr) {
			t.Errorf("%s should know %s after gossip", pair[0].name, pair[1].name)
		}
	}

	// promote B: epoch bump, everyone follows
	if _, err := b.mg.cl.Promote("admin"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	c.sync()
	for _, n := range []*tnode{a, b, c} {
		s := n.mg.cl.Snapshot()
		if s.Epoch != 2 || s.PrimaryAddr != b.addr {
			t.Errorf("%s: want epoch 2 / primary B, got %+v", n.name, s)
		}
	}
	if a.mg.cl.Snapshot().Role != RoleReplica || b.mg.cl.Snapshot().Role != RolePrimary {
		t.Error("roles after promotion")
	}
	// stale announce is rejected
	if err := a.mg.cl.node.CheckEpoch(1); err != ErrStaleEpoch {
		t.Errorf("stale epoch: %v", err)
	}

	// removing a member: it resets itself on its next sync
	if err := b.mg.cl.RemovePeer(c.addr, "admin"); err != nil {
		t.Fatal(err)
	}
	if hasPeer(b, c.addr) {
		t.Fatal("removed peer still listed")
	}
	c.mg.cl.SyncOnce(context.Background())
	if s := c.mg.cl.Snapshot(); s.Role != RolePrimary || len(s.Peers) != 0 || s.Epoch != 1 {
		t.Fatalf("removed node must reset to a single-node cluster: %+v", s)
	}
	b.sync()
	if hasPeer(b, c.addr) {
		t.Fatal("a removed peer must not be re-added by gossip")
	}
}

// join2 makes `via` (an untouched node) join this node's cluster.
func (n *tnode) join2(t *testing.T, via *tnode) { t.Helper(); via.join(n); via.sync() }

func TestClusterAuthRejections(t *testing.T) {
	a, b := newTNode(t, "A"), newTNode(t, "B")
	// wrong token
	bogus := joinCode{Token: "nope", Fp: a.mg.cl.node.Fingerprint(), Addrs: []string{a.addr}}
	if err := b.mg.cl.Join(context.Background(), encodeJoinCode(bogus), "t"); err == nil || !strings.Contains(err.Error(), "invalid or expired") {
		t.Fatalf("bad token: %v", err)
	}
	// wrong pinned fingerprint
	code, _, _ := a.mg.cl.MintJoinCode("t")
	jc, _ := decodeJoinCode(code)
	jc.Fp = strings.Repeat("ab", 32)
	if err := b.mg.cl.Join(context.Background(), encodeJoinCode(jc), "t"); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("wrong fingerprint must be refused: %v", err)
	}
	// a token is single use
	code, _, _ = a.mg.cl.MintJoinCode("t")
	if err := b.mg.cl.Join(context.Background(), code, "t"); err != nil {
		t.Fatal(err)
	}
	c := newTNode(t, "C")
	if err := c.mg.cl.Join(context.Background(), code, "t"); err == nil {
		t.Fatal("a used token must not work twice")
	}
	// an outsider with the right fingerprint but no secret is refused
	outsider := newTNode(t, "X")
	self := outsider.mg.cl.node.Self()
	pa := a.mg.cl.node.Self()
	err := outsider.mg.cl.call(context.Background(), pa, "GET", "/cluster/status", nil, nil, 3*time.Second)
	if err == nil {
		t.Fatalf("a node without the cluster secret must be refused (as %s)", self.Addr)
	}
	if pe, ok := err.(*peerError); !ok || pe.Status != 401 {
		t.Fatalf("want 401, got %v", err)
	}
	if hasPeer(a, outsider.addr) {
		t.Fatal("an unauthenticated caller must not be learned as a peer")
	}
	// garbage join code
	if _, err := decodeJoinCode("ddgw-join-v1:!!!"); err == nil {
		t.Error("damaged code")
	}
	if _, err := decodeJoinCode("hello"); err == nil {
		t.Error("foreign code")
	}
}

func encodeJoinCode(jc joinCode) string {
	b, _ := jsonMarshal(jc)
	return joinCodePrefix + b64url(b)
}

func TestClusterCertificateReplication(t *testing.T) {
	a, b := newTNode(t, "A"), newTNode(t, "B")
	b.join(a)
	b.sync()

	ca := newTestCA(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	chain := string(ca.issue(t, &k.PublicKey, time.Now().Add(72*time.Hour), "gw.test"))
	if _, _, err := a.mg.TLSInstall(chain, string(ecKeyPEM(k)), "alice"); err != nil {
		t.Fatal(err)
	}
	b.sync()
	if s := b.mg.certs.Status(); s.Source != certSourceCluster || s.Fingerprint != a.mg.certs.Status().Fingerprint {
		t.Fatalf("B must serve A's certificate: %+v", s)
	}
	// installing on the replica goes through the primary
	k2, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	chain2 := string(ca.issue(t, &k2.PublicKey, time.Now().Add(96*time.Hour), "gw2.test"))
	if _, _, err := b.mg.TLSInstall(chain2, string(ecKeyPEM(k2)), "bob"); err != nil {
		t.Fatal(err)
	}
	if a.mg.certs.Status().Fingerprint != b.mg.certs.Status().Fingerprint {
		t.Fatal("replica install must end up on both nodes")
	}
	if a.mg.certs.Status().Source != certSourceInstalled {
		t.Fatalf("primary holds it as installed: %+v", a.mg.certs.Status())
	}
	if _, err := b.mg.TLSRevert("bob"); err != nil {
		t.Fatal(err)
	}
	if a.mg.certs.Status().Source != certSourceSelfSigned || b.mg.certs.Status().Source != certSourceSelfSigned {
		t.Fatal("revert must reach both nodes")
	}
}

func fakeSourceTarball(t *testing.T, ver string) []byte {
	t.Helper()
	files := []srcFile{
		{"ddgw/source/go.mod", []byte("module ddgw\n\ngo 1.24\n"), 0o644},
		{"ddgw/source/main.go", []byte("package main\nfunc main(){}\n"), 0o644},
		{"ddgw/source/VERSION", []byte(ver + "\n"), 0o644},
	}
	return makeTgz(t, files)
}

func TestClusterUpdateIntentAndSourceDistribution(t *testing.T) {
	a, b := newTNode(t, "A"), newTNode(t, "B")
	b.join(a)
	b.sync()
	// auto-update is on by default; this test is about hand-queued updates
	if err := a.mg.UpdateAuto(false, "test"); err != nil {
		t.Fatal(err)
	}
	b.sync()
	running := version()
	next := fmt.Sprintf("%d", mustInt(running)+1)

	if _, err := a.mg.UpdateUpload(fakeSourceTarball(t, next), "alice"); err != nil {
		t.Fatal(err)
	}
	if a.mg.upd.SourceVersion() != next {
		t.Fatal("source not staged")
	}
	// B asks for the update through the primary; intent replicates
	if err := b.mg.UpdatePush([]string{b.addr}, "bob"); err != nil {
		t.Fatal(err)
	}
	if !a.mg.upd.Intent().wants(b.addr) {
		t.Fatal("the primary must hold the queued intent")
	}
	b.sync()
	if !b.mg.upd.Intent().wants(b.addr) {
		t.Fatal("the intent must replicate to B")
	}
	// B sees that A has newer source and pulls it
	p, ver, ok := b.mg.cl.bestSourcePeer(running)
	if !ok || ver != next || p.Addr != a.addr {
		t.Fatalf("bestSourcePeer: %v %v %v", p, ver, ok)
	}
	// before pulling, B still counts A's newer source as available, so its
	// queue entry must not be treated as satisfied (it was dropped once)
	if !versionGreater(b.mg.bestKnownSource(running), running) {
		t.Fatal("B must see A's newer source before pulling it")
	}
	got, err := b.mg.cl.pullSource(context.Background(), p)
	if err != nil || got != next || b.mg.upd.SourceVersion() != next {
		t.Fatalf("pull: %v %v", got, err)
	}
	// stagger: with A not wanting the update and B wanting it, nobody waits
	if b.mg.cl.shouldWaitForOthers(next, b.mg.upd.Intent()) {
		t.Fatal("B should not wait when nobody else is updating")
	}
	// auto-all, and cancel
	if err := a.mg.UpdateAuto(true, "alice"); err != nil {
		t.Fatal(err)
	}
	if !a.mg.upd.Intent().AutoAll {
		t.Fatal("auto flag")
	}
	if err := a.mg.UpdateCancel([]string{b.addr}, "alice"); err != nil || a.mg.upd.Intent().Pending != nil && len(a.mg.upd.Intent().Pending) != 0 {
		t.Fatalf("cancel: %v", err)
	}
	st := a.mg.UpdateStatus()
	if len(st.Nodes) != 2 || st.SourceVersion != next {
		t.Fatalf("status: %+v", st)
	}
	_ = os.Stdout
}

// A peer is reachable if ANY of its addresses works: a dead main address
// (stale DNS name, wrong family) must fall through to the alternatives, and the
// one that worked is preferred afterwards.
func TestClusterPeerAddressFallback(t *testing.T) {
	a, b := newTNode(t, "A"), newTNode(t, "B")
	b.join(a)
	ap := a.mg.cl.node.Self()
	dead := ClusterPeer{Addr: "127.0.0.1:1", Fp: ap.Fp, NodeID: ap.NodeID, Alts: []string{"127.0.0.1:2", a.addr}}
	var msg PeerStatusMsg
	if err := b.mg.cl.call(context.Background(), dead, "GET", "/cluster/status", nil, &msg, 20*time.Second); err != nil {
		t.Fatalf("a peer with a working alternative must be reachable: %v", err)
	}
	if got := b.mg.cl.addrsFor(dead)[0]; got != a.addr {
		t.Fatalf("the address that worked must be tried first next time, got %s", got)
	}
	// nothing works -> one error naming every address
	none := ClusterPeer{Addr: "127.0.0.1:3", Fp: ap.Fp, NodeID: ap.NodeID, Alts: []string{"127.0.0.1:2"}}
	err := b.mg.cl.call(context.Background(), none, "GET", "/cluster/status", nil, &msg, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:3") || !strings.Contains(err.Error(), "127.0.0.1:2") {
		t.Fatalf("the error must name every address tried: %v", err)
	}
	// the join code lists name and addresses, and the node advertises alternatives
	if alts := a.mg.cl.node.Self().Alts; len(alts) == 0 {
		t.Log("no non-loopback interface addresses in this sandbox; alternatives list is empty")
	}
	jc, err := decodeJoinCode(mustCode(t, a))
	if err != nil || len(jc.Addrs) < 1 || jc.Addrs[0] != a.addr {
		t.Fatalf("join code addresses: %+v %v", jc, err)
	}
}

func mustCode(t *testing.T, n *tnode) string {
	t.Helper()
	code, _, err := n.mg.cl.MintJoinCode("test")
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestSafeToTakeDown(t *testing.T) {
	serving := func(ids ...int) map[int]bool {
		m := map[int]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	mine := []GwState{{GroupID: 1, Serving: true}, {GroupID: 2, Serving: true}}
	cases := []struct {
		name  string
		mine  []GwState
		peers []peerGateways
		ok    bool
	}{
		{"another member serves everything", mine, []peerGateways{{Addr: "b", Known: true, Serving: serving(1, 2)}}, true},
		{"split between two members", mine, []peerGateways{{Addr: "b", Known: true, Serving: serving(1)}, {Addr: "c", Known: true, Serving: serving(2)}}, true},
		{"the only other member is still recovering", mine, []peerGateways{{Addr: "b", Known: true, Serving: serving()}}, false},
		{"one gateway would be left unserved", mine, []peerGateways{{Addr: "b", Known: true, Serving: serving(1)}}, false},
		{"no other member is reachable", mine, nil, false},
		{"an older node cannot say: assumed fine", mine, []peerGateways{{Addr: "b", Known: false}}, true},
		{"this node serves nothing: nothing to lose", []GwState{{GroupID: 1, Serving: false}}, nil, true},
		{"no gateways at all", nil, nil, true},
		{"the only other member is on another subnet and can never cover it", mine, []peerGateways{{Addr: "b", Known: true, Serving: serving(), Offnet: serving(1, 2)}}, true},
		{"one member is on another subnet, another is just recovering", mine, []peerGateways{{Addr: "b", Known: true, Serving: serving(), Offnet: serving(1, 2)}, {Addr: "c", Known: true, Serving: serving()}}, false},
		{"another subnet, but one gateway could be covered", mine, []peerGateways{{Addr: "b", Known: true, Serving: serving(), Offnet: serving(1)}}, false},
		{"the other member is unreachable and may come back", mine, []peerGateways{{Addr: "b", Down: true}}, false},
		{"the other member only just came back", mine, []peerGateways{{Addr: "b", Known: true, Serving: serving(1, 2), Fresh: serving(1, 2)}}, false},
		{"one settled member is enough", mine, []peerGateways{{Addr: "b", Known: true, Serving: serving(1, 2), Fresh: serving(1, 2)}, {Addr: "c", Known: true, Serving: serving(1, 2)}}, true},
	}
	for _, c := range cases {
		if ok, why := safeToTakeDown(c.mine, c.peers); ok != c.ok || (!ok && why == "") {
			t.Errorf("%s: got %v %q, want %v", c.name, ok, why, c.ok)
		}
	}
}

// Two of three nodes paused: the one serving node cannot go (nothing covers it),
// so a paused node must not wait for it even though it sorts earlier.
func TestHeldMemberDoesNotBlockPausedOnes(t *testing.T) {
	srv := func(ids ...int) map[int]bool {
		m := map[int]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	active := []GwState{{GroupID: 1, Serving: true}}
	members := []peerGateways{
		{Addr: "a", Known: true, Serving: srv(1)}, // the only one serving
		{Addr: "b", Known: true, Serving: srv()},  // paused
		{Addr: "c", Known: true, Serving: srv()},  // paused
	}
	if !heldBySafety("a", active, members) {
		t.Fatal("the only serving member must count as held back")
	}
	if heldBySafety("b", nil, members) {
		t.Fatal("a paused member serves nothing; it is never held back")
	}
	// once a paused member is back and settled, the serving one is no longer held
	members[1].Serving = srv(1)
	if heldBySafety("a", active, members) {
		t.Fatal("with a settled second server the first is free to go")
	}
	// an older node cannot report gateways: never treated as held
	if heldBySafety("a", nil, members) {
		t.Fatal("no gateway report means not held")
	}
}

func TestGatewayStates(t *testing.T) {
	g1, g2, g3 := defaultGroup(), defaultGroup(), defaultGroup()
	g1.GroupID, g1.VIP4, g1.DNSProxy = 1, "10.1.0.1/24", true
	g2.GroupID, g2.VIP4, g2.VIP6 = 2, "10.2.0.1/24", "2001:db8::1/64"
	g3.GroupID, g3.VIP4, g3.Paused = 3, "10.3.0.1/24", true
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g1, g2, g3}
	rows := []SnapshotRow{
		{GroupID: 1, AF: "v4", Local: true, State: "forward", DNSUp: true},
		{GroupID: 2, AF: "v4", Local: true, State: "active"},
		{GroupID: 2, AF: "v6", Local: true, State: "speak"}, // v6 not yet elected
		{GroupID: 1, AF: "v4", Local: false, State: "active"},
	}
	got := gatewayStates(dc, rows)
	if len(got) != 2 || got[0].GroupID != 1 || !got[0].Serving || got[1].GroupID != 2 || got[1].Serving {
		t.Fatalf("gateway states (paused left out, dual-stack needs both, DNS must be up): %+v", got)
	}
	rows[0].DNSUp = false
	if gatewayStates(dc, rows)[0].Serving {
		t.Fatal("a gateway whose DNS listener is down is not serving")
	}
}

// A gateway counts as cover for a neighbour only after it has been served for
// servingSettle without a break.
func TestLocalGatewaysFreshThenSettled(t *testing.T) {
	old := servingSettle
	servingSettle = 60 * time.Millisecond
	defer func() { servingSettle = old }()
	m := &Mgmt{}
	up := true
	m.gwFn = func() []GwState { return []GwState{{GroupID: 1, Serving: up}} }
	if g := m.localGateways(); !g[0].Serving || !g[0].Fresh {
		t.Fatalf("just started serving must be fresh: %+v", g)
	}
	time.Sleep(90 * time.Millisecond)
	if g := m.localGateways(); !g[0].Serving || g[0].Fresh {
		t.Fatalf("settled: %+v", g)
	}
	up = false
	if g := m.localGateways(); g[0].Serving || g[0].Fresh {
		t.Fatalf("not serving: %+v", g)
	}
	up = true
	if g := m.localGateways(); !g[0].Fresh {
		t.Fatalf("a break must restart the clock: %+v", g)
	}
}

// Three clustered nodes, two of them paused (they serve nothing): the paused ones
// must be free to update ahead of the serving node, and the serving node must keep
// waiting until another member covers it.  Before v123 nobody moved.
func TestRollingUpdateWithPausedNodes(t *testing.T) {
	ns := []*tnode{newTNode(t, "A"), newTNode(t, "B"), newTNode(t, "C")}
	ns[1].join(ns[0])
	ns[2].join(ns[0])
	sort.Slice(ns, func(i, j int) bool { return ns[i].addr < ns[j].addr })
	active := ns[0] // sorts first, so the paused ones would wait for it
	for _, n := range ns {
		n := n
		serving := n == active
		n.mg.gwFn = func() []GwState {
			if !serving {
				return []GwState{} // paused: no gateways reported
			}
			return []GwState{{GroupID: 1, Serving: true}}
		}
	}
	for i := 0; i < 2; i++ {
		for _, n := range ns {
			n.sync()
		}
	}
	// the nodes that joined are left out of the gateway (see TestNewNodeIsNotAddedToTheGateways); this test is about members
	// that serve it, so they are added to it
	for _, n := range ns {
		if n != active {
			if _, err := active.mg.CanvasEdit(canvasEdit{Action: "add", Kind: "node", Group: 1, Node: n.addr}, "tester"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < 2; i++ {
		for _, n := range ns {
			n.sync()
		}
	}
	time.Sleep(servingSettle + 200*time.Millisecond) // the serving node's cover is settled
	next := fmt.Sprintf("%d", mustInt(version())+1)
	// the first paused node goes at once; the second still takes its turn after it
	if ns[1].mg.cl.shouldWaitForOthers(next, ns[1].mg.upd.Intent()) {
		t.Fatalf("paused node %s must not wait for the serving node that cannot go", ns[1].name)
	}
	if !ns[2].mg.cl.shouldWaitForOthers(next, ns[2].mg.upd.Intent()) {
		t.Fatalf("paused node %s must still wait its turn behind %s", ns[2].name, ns[1].name)
	}
	for _, n := range ns[1:] {
		if ok, _ := n.mg.updateSafe(); !ok {
			t.Fatalf("paused node %s serves nothing and must be safe to update", n.name)
		}
	}
	if ok, why := active.mg.updateSafe(); ok || why == "" {
		t.Fatalf("the only serving node must keep waiting: %v %q", ok, why)
	}
}

// A gateway's statistics add up the history of every reachable node: over the real signed cluster call, two nodes of one
// process (so they share the one history) count it twice, and a node that cannot be reached is reported, not hidden.
func TestClusterGatewayStatsAddUpNodes(t *testing.T) {
	failDelay = 0
	a, b := newTNode(t, "A"), newTNode(t, "B")
	a.join2(t, b)
	a.sync()
	b.sync()
	key := gwKey(9992)
	for i := 0; i < 5; i++ {
		srvhist.series(key).addOK()
	}
	now := time.Now()
	r, err := a.mg.cl.clusterStats(key, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Nodes != 2 || r.NodesOff != 0 || r.Answered != 10 || !r.Known {
		t.Fatalf("two nodes: nodes %d off %d answered %d known %v", r.Nodes, r.NodesOff, r.Answered, r.Known)
	}
	// through the operation the web page and the CLI use
	res, err := a.mg.Op("dns.serverstats", json.RawMessage(`{"addr":"`+key+`","from":"1h"}`), "t")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.(ServerStatsResult); got.Nodes != 2 || got.Answered != 10 {
		t.Fatalf("op: %+v", got)
	}
	// B goes away: A still answers, with one node, and says one did not answer
	b.mg.cl.Stop()
	a.sync()
	r, err = a.mg.cl.clusterStats(key, now.Add(-time.Hour), now)
	if err != nil || r.Nodes != 1 || r.NodesOff != 1 || r.Answered != 5 {
		t.Fatalf("one node gone: %+v %v", r, err)
	}
}

// The Topology drawing's cluster nodes: this node first with the gateway's own colour, the others from what they report,
// and a node that stops answering turns red; one node alone is drawn too: just this node.
func TestCanvasClusterNodes(t *testing.T) {
	failDelay = 0
	a, b := newTNode(t, "A"), newTNode(t, "B")
	if got := a.mg.cl.canvasNodes(1, "ok", "fine", false); len(got) != 1 || !got[0].Self || got[0].Status != "ok" || got[0].Gw != "ok" || got[0].Name == "" {
		t.Fatalf("a lone node is drawn as just itself, serving: %+v", got)
	}
	var none *Cluster // no cluster at all: still this node
	if got := none.canvasNodes(1, "warn", "no server", false); len(got) != 1 || !got[0].Self || got[0].Gw != "degraded" {
		t.Fatalf("no cluster: %+v", got)
	}
	a.join2(t, b)
	a.mg.gwFn = func() []GwState { return []GwState{{GroupID: 1, Serving: true}} }
	b.mg.gwFn = func() []GwState { return []GwState{{GroupID: 1, Serving: true}} }
	paused := false
	b.mg.pausedFn = func() bool { return paused }
	a.sync()
	b.sync()
	ns := a.mg.cl.canvasNodes(1, "warn", "one address down", false)
	if len(ns) != 2 || !ns[0].Self || ns[0].Status != "warn" || ns[0].Label != "degraded" || ns[0].Detail != "one address down" {
		t.Fatalf("self: %+v", ns)
	}
	if ns[1].Addr != b.addr || ns[1].Status != "ok" || ns[1].Label != "serving" || !ns[1].Reachable || ns[1].Self {
		t.Fatalf("peer: %+v", ns[1])
	}
	// a gateway B does not run (paused or absent) and a node B that is paused
	if n := a.mg.cl.canvasNodes(2, "ok", "", false)[1]; n.Status != "paused" || n.Label != "not serving" {
		t.Fatalf("gateway not on the peer: %+v", n)
	}
	b.mg.gwFn = func() []GwState { return []GwState{{GroupID: 1, Serving: false}} }
	a.sync()
	if n := a.mg.cl.canvasNodes(1, "ok", "", false)[1]; n.Status != "warn" || n.Label != "not serving" {
		t.Fatalf("up but not serving: %+v", n)
	}
	paused = true
	a.sync()
	if n := a.mg.cl.canvasNodes(1, "ok", "", false)[1]; n.Status != "paused" || n.Label != "paused" || !n.Paused {
		t.Fatalf("paused node: %+v", n)
	}
	if n := a.mg.cl.canvasNodes(2, "ok", "", false)[1]; n.Status != "paused" || n.Paused != true {
		t.Fatalf("paused node, other gateway: %+v", n)
	}
	if n := a.mg.cl.canvasNodes(1, "paused", "p", true)[0]; !n.Paused || n.Label != "paused" {
		t.Fatalf("this node paused: %+v", n)
	}
	// B goes away
	b.mg.cl.Stop()
	a.sync()
	n := a.mg.cl.canvasNodes(1, "ok", "", false)[1]
	if n.Status != "bad" || n.Label != "not answering" || n.Reachable || n.LastSeen == 0 {
		t.Fatalf("unreachable node: %+v", n)
	}
}

// The picture the GUI and the CLI get carries the nodes; a daemon alone carries just itself.
func TestMarkNodesOnCanvas(t *testing.T) {
	failDelay = 0
	(&StatusServer{}).markNodes([]CanvasGateway{{GroupID: 1}}) // no management at all: no panic
	a, b := newTNode(t, "A"), newTNode(t, "B")
	s := &StatusServer{mg: a.mg}
	gs := []CanvasGateway{{GroupID: 1, Status: "ok", Detail: "up"}}
	s.markNodes(gs)
	if len(gs[0].Nodes) != 1 || !gs[0].Nodes[0].Self || gs[0].ClusterStatus != "ok" {
		t.Fatalf("a lone node: %+v (%q)", gs[0].Nodes, gs[0].ClusterStatus)
	}
	a.join2(t, b)
	a.sync()
	s.markNodes(gs)
	if len(gs[0].Nodes) != 2 || !gs[0].Nodes[0].Self || gs[0].Nodes[0].Status != "ok" {
		t.Fatalf("two nodes: %+v", gs[0].Nodes)
	}
	// two nodes of one host name are told apart by address
	if gs[0].Nodes[0].Name != gs[0].Nodes[0].Addr || gs[0].Nodes[1].Name != gs[0].Nodes[1].Addr {
		t.Fatalf("names: %q %q", gs[0].Nodes[0].Name, gs[0].Nodes[1].Name)
	}
}

// A node whose CPU, memory or disk is over 85% is red on the drawing until all are back under it: this node from its own
// reading, another from what it reports; one that stops answering stays "not answering".
func TestCanvasNodeYellowWhenStrained(t *testing.T) {
	failDelay = 0
	old := hostLoadFn
	t.Cleanup(func() { hostLoadFn = old })
	load := HostLoad{CPU: 10, Mem: 40, Disk: 50, DiskMount: "/"}
	hostLoadFn = func() (HostLoad, bool) { return load, true }
	a, b := newTNode(t, "A"), newTNode(t, "B")
	a.join2(t, b)
	a.mg.gwFn = func() []GwState { return []GwState{{GroupID: 1, Serving: true}} }
	b.mg.gwFn = a.mg.gwFn
	a.sync()
	b.sync()
	get := func() []CanvasNode { return a.mg.cl.canvasNodes(1, "ok", "fine", false) }
	ns := get()
	if ns[0].Status != "ok" || ns[1].Status != "ok" || len(ns[0].Strain)+len(ns[1].Strain) != 0 || ns[0].Host == nil || ns[1].Host == nil || ns[1].Host.Disk != 50 {
		t.Fatalf("a quiet cluster: %+v", ns)
	}
	load = HostLoad{CPU: 85, Mem: 85, Disk: 85, DiskMount: "/"} // exactly 85% is not over
	a.sync()
	if ns := get(); ns[0].Status != "ok" || ns[1].Status != "ok" {
		t.Fatalf("85%% is not over: %+v", ns)
	}
	load = HostLoad{CPU: 91, Mem: 40, Disk: 88, DiskMount: "/var"}
	a.sync()
	ns = get()
	for i, n := range ns {
		if n.Status != "warn" || n.Label != "CPU 91% · disk 88%" || len(n.Strain) != 2 || n.Strain[1] != "disk 88% (/var)" {
			t.Fatalf("node %d over the limit: %+v", i, n)
		}
	}
	if !strings.Contains(ns[0].Detail, "Over 85%") || !strings.HasSuffix(ns[0].Detail, "Serving this gateway") {
		t.Fatalf("detail: %q", ns[0].Detail)
	}
	// paused is not an excuse
	b.mg.pausedFn = func() bool { return true }
	a.sync()
	if n := get()[1]; n.Status != "warn" || !n.Paused {
		t.Fatalf("paused and over the limit: %+v", n)
	}
	load = HostLoad{CPU: 20, Mem: 40, Disk: 50, DiskMount: "/"}
	a.sync()
	if ns := get(); ns[0].Status != "ok" || ns[1].Status != "paused" || len(ns[0].Strain) != 0 {
		t.Fatalf("back under the limit: %+v", ns)
	}
	// unreachable stays "not answering", whatever it last reported
	load = HostLoad{CPU: 99, Mem: 99, Disk: 99, DiskMount: "/"}
	a.sync()
	b.mg.cl.Stop()
	a.sync()
	if n := get()[1]; n.Label != "not answering" || len(n.Strain) != 0 {
		t.Fatalf("unreachable: %+v", n)
	}
}

// The Upgrade page pages through the whole history, so the status carries every event kept, newest first.
func TestUpdateStatusCarriesWholeHistory(t *testing.T) {
	a := newTNode(t, "A")
	a.mg.upd.mu.Lock()
	for i := 0; i < 130; i++ {
		a.mg.upd.addEventLocked(UpdateEvent{Kind: "applied", To: fmt.Sprint(i)})
	}
	a.mg.upd.mu.Unlock()
	h := a.mg.UpdateStatus().History
	if len(h) != 130 || h[0].To != "129" || h[129].To != "0" {
		t.Fatalf("status history: %d events, first %q", len(h), h[0].To)
	}
}

// A peer that serves but sees its own DNS servers failing is drawn degraded, like this node's own shape would be.
func TestCanvasPeerShowsItsOwnHealth(t *testing.T) {
	failDelay = 0
	a, b := newTNode(t, "A"), newTNode(t, "B")
	a.join2(t, b)
	a.mg.gwFn = func() []GwState { return []GwState{{GroupID: 1, Serving: true}} }
	health, why := "warn", "running, but some DNS servers are down"
	b.mg.gwFn = func() []GwState { return []GwState{{GroupID: 1, Serving: true, Health: health, HealthWhy: why}} }
	a.sync()
	b.sync()
	peer := func() CanvasNode {
		for _, n := range a.mg.cl.canvasNodes(1, "ok", "fine", false) {
			if !n.Self {
				return n
			}
		}
		t.Fatal("no peer node")
		return CanvasNode{}
	}
	if n := peer(); n.Status != "warn" || n.Label != "degraded" || !strings.Contains(n.Detail, "some DNS servers are down") {
		t.Fatalf("degraded peer: %+v", n)
	}
	health = "ok"
	a.sync()
	if n := peer(); n.Status != "ok" || n.Label != "serving" {
		t.Fatalf("healthy peer: %+v", n)
	}
	health = "" // an older node says nothing about its health
	a.sync()
	if n := peer(); n.Status != "ok" {
		t.Fatalf("peer without health: %+v", n)
	}
}

// This node's own entry says the same words as the other nodes' when it is healthy, and its own reason when it is not.
func TestCanvasSelfNodeSaysWhatThePeersSay(t *testing.T) {
	a, _ := twoNodeCluster(t)
	self := func(status, detail string, paused bool) CanvasNode {
		return a.mg.cl.canvasNodes(1, status, detail, paused)[0]
	}
	if n := self("ok", "IPv4: running and answering; IPv6: running and answering", false); !n.Self || n.Detail != "Serving this gateway" || n.Status != "ok" || n.Label != "serving" {
		t.Fatalf("healthy: %+v", n)
	}
	for _, c := range []struct {
		status, detail string
		paused         bool
	}{{"warn", "running, but some DNS servers are down", false}, {"bad", "no DNS server is healthy", false}, {"paused", "Paused on this node", true}, {"idle", "starting", false}} {
		if n := self(c.status, c.detail, c.paused); n.Detail != c.detail {
			t.Errorf("%s: detail %q, want the gateway's own reason %q", c.status, n.Detail, c.detail)
		}
	}
	// a node held back for being on another subnet says "not serving" like the others do, not "starting"
	if n := self("idle", "not running here — this node has no address in the gateway's subnet 10.0.0.0/24 on eth0; members on that subnet serve it", false); n.Label != "not serving" || n.Status != "warn" {
		t.Errorf("off-net: %+v", n)
	}
	if n := self("idle", "starting — waiting for the DNS servers to answer before this node serves", false); n.Label != "starting" {
		t.Errorf("warming: %+v", n)
	}
	// a healthy colour does not hide that the node itself is paused
	if n := self("ok", "fine", true); n.Detail != "fine" {
		t.Errorf("paused node: detail %q", n.Detail)
	}
}

// A node that is the only member has nobody to cover its gateways, so the "keep every gateway served" rule must not hold
// its update back for ever; once another member is known the rule applies again.
func TestUpdateSafeForTheOnlyMember(t *testing.T) {
	failDelay = 0
	a, b := newTNode(t, "A"), newTNode(t, "B")
	a.mg.gwFn = func() []GwState { return []GwState{{GroupID: 1, Serving: true}} }
	b.mg.gwFn = func() []GwState { return []GwState{} }
	if ok, why := a.mg.updateSafeToApply(); !ok {
		t.Fatalf("the only member serving a gateway was held back from updating: %q", why)
	}
	if ok, _ := a.mg.updateSafe(); ok {
		t.Fatal("a power action on the only serving member must stay refused")
	}
	a.join2(t, b)
	a.sync()
	b.sync()
	// a node that has just joined is left out of every gateway: it cannot cover it, so it does not hold the update back
	if ok, why := a.mg.updateSafeToApply(); !ok {
		t.Fatalf("a member that has only joined (and is left out of the gateway) held the update back: %q", why)
	}
	// once it is added to the gateway and serves nothing yet, the serving one must wait
	if _, err := a.mg.CanvasEdit(canvasEdit{Action: "add", Kind: "node", Group: 1, Node: b.addr}, "tester"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	b.sync()
	if ok, why := a.mg.updateSafeToApply(); ok || why == "" {
		t.Fatalf("with a second member that serves nothing, the serving one must wait: %v %q", ok, why)
	}
	// the other member is removed from the gateway (Topology ▸ node ▸ remove): it can never cover it, so waiting is for ever
	if _, err := a.mg.CanvasEdit(canvasEdit{Action: "del", Kind: "node", Group: 1, Node: b.addr}, "tester"); err != nil {
		t.Fatal(err)
	}
	if ok, why := a.mg.updateSafeToApply(); !ok {
		t.Fatalf("a member removed from the gateway must not hold the update back: %q", why)
	}
}

// A gateway paused on every node reads "paused" on every node that answers, not "not serving".
func TestLabelPausedAll(t *testing.T) {
	nodes := []CanvasNode{
		{Self: true, Reachable: true, Status: "paused", Label: "paused", Gw: "paused"},
		{Reachable: true, Status: "paused", Label: "not serving", Gw: "paused", Detail: "This gateway is paused or not set up on that node"},
		{Reachable: true, Status: "warn", Label: "not serving", Gw: "notserving", Detail: "That node is up but is not serving this gateway (yet)"},
		{Reachable: false, Status: "bad", Label: "not answering", Gw: "down"},
		{Reachable: true, Excluded: true, Status: "paused", Label: "removed", Gw: "removed"},
		{Reachable: true, Status: "paused", Label: "paused", Gw: "paused", Paused: true, Detail: "This node is paused"},
		{Reachable: true, Status: "warn", Label: "disk 91%", Gw: "paused", Detail: "Over 85%: disk 91%. This gateway is paused or not set up on that node"},
	}
	labelPausedAll(nodes)
	if nodes[1].Label != "paused" || nodes[1].Status != "paused" || !strings.Contains(nodes[1].Detail, "all nodes") {
		t.Fatalf("a node that answers: %+v", nodes[1])
	}
	if nodes[0].Label != "paused" || nodes[2].Label != "not serving" || nodes[3].Label != "not answering" || nodes[4].Label != "removed" || nodes[5].Detail != "This node is paused" {
		t.Fatalf("the others must keep their words: %+v", nodes)
	}
	if nodes[6].Label != "disk 91%" || !strings.Contains(nodes[6].Detail, "Over 85%") || !strings.Contains(nodes[6].Detail, "all nodes") {
		t.Fatalf("a node that is also over the load limit keeps its warning: %+v", nodes[6])
	}
}
