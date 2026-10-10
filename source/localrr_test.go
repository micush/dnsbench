package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math/rand"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

func TestParseLocalRecords(t *testing.T) {
	d, err := parseLocal(`A 10.5.5.5, 10.5.5.6; AAAA 2001:db8::5; TXT "a;b" "c"; TTL 300`)
	if err != nil || len(d.a) != 2 || len(d.aaaa) != 1 || d.ttl != 300 || len(d.txt) != 1 || len(d.txt[0]) != 2 || d.txt[0][0] != "a;b" {
		t.Fatalf("%+v %v", d, err)
	}
	if d, err = parseLocal("CNAME Host.Example.com."); err != nil || d.cname != "host.example.com" {
		t.Fatalf("%+v %v", d, err)
	}
	for _, bad := range []string{"A 2001:db8::1", "AAAA 10.0.0.1", "A", "CNAME not a name", "CNAME a.example; A 10.0.0.1", "TXT", `TXT "open`, "TTL 99999999", "MX 10 a.example", "TTL 60"} {
		if _, err := parseLocal(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	rows, err := normalizePolicy([]PolicyRule{{Client: "*", Name: "a.example", Servers: []string{"A 10.5.5.5, 10.5.5.6"}}})
	if err != nil || len(rows[0].Servers) != 1 {
		t.Fatalf("%+v %v", rows, err)
	}
	if _, err := normalizePolicy([]PolicyRule{{Client: "*", Name: "a.example", Servers: []string{"A 10.5.5.5"}, Dest: "b.example"}}); err == nil {
		t.Fatal("records with a destination name were accepted")
	}
}

type rrInfo struct {
	typ  uint16
	ttl  uint32
	data []byte
}

func answersOf(t *testing.T, b []byte) []rrInfo {
	t.Helper()
	rrs, _, ok := recordsAfterQuestion(b)
	if !ok {
		t.Fatal("unreadable answer")
	}
	an := int(binary.BigEndian.Uint16(b[6:]))
	var out []rrInfo
	for i, rr := range rrs {
		if i >= an {
			break
		}
		out = append(out, rrInfo{rr.typ, binary.BigEndian.Uint32(b[rr.rdataOff-6:]), b[rr.rdataOff:rr.end]})
	}
	return out
}

func TestPolicyLocalRecords(t *testing.T) {
	main := newFakeDNS(t)
	cfg := testDNSCfg(main.addr)
	cfg.ECS = false
	cfg.Policy = []PolicyRule{
		{Client: "*", Name: "a.example", Servers: []string{"A 10.5.5.5, 10.5.5.6; TTL 300"}},
		{Client: "*", Name: "t.example", Servers: []string{`TXT "hello world"`}},
		{Client: "*", Name: "c.example", Servers: []string{"CNAME x.example"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return p })
	fe.ctx, fe.cancel = context.WithCancel(context.Background())
	t.Cleanup(fe.cancel)
	m0 := main.hits.Load()
	ask := func(name string, qt uint16) []byte {
		return fe.resolve(cacheQuery(name, qt, nil), false, mustAddr("10.9.9.9"))
	}
	r := answersOf(t, ask("a.example", 1))
	if len(r) != 2 || r[0].typ != 1 || r[0].ttl != 300 || !bytes.Equal(r[0].data, []byte{10, 5, 5, 5}) || !bytes.Equal(r[1].data, []byte{10, 5, 5, 6}) {
		t.Fatalf("A: %+v", r)
	}
	resp := ask("a.example", 28)
	if h, _ := parseHeader(resp); h.rcode != 0 || h.ancount != 0 {
		t.Fatalf("AAAA for an A-only row must be no data: %+v", h)
	}
	if r = answersOf(t, ask("t.example", 16)); len(r) != 1 || string(r[0].data) != "\x0bhello world" {
		t.Fatalf("TXT: %+v", r)
	}
	if main.hits.Load() != m0 {
		t.Fatal("a local record reached a server")
	}
	r = answersOf(t, ask("c.example", 5))
	if len(r) != 1 || r[0].typ != 5 || !bytes.Equal(r[0].data, packPlainName("x.example")) {
		t.Fatalf("CNAME: %+v", r)
	}
	resp = ask("c.example", 1)
	r = answersOf(t, resp)
	if len(r) != 2 || r[0].typ != 5 || r[1].typ != 1 || !bytes.Equal(r[1].data, []byte{192, 0, 2, 1}) {
		t.Fatalf("CNAME chase: %+v", r)
	}
	if got, _ := main.lastQ.Load().(string); got != "x.example" {
		t.Fatalf("the pool was asked %q", got)
	}
	if qn, _, _ := questionOf(resp); qn != "c.example" {
		t.Fatalf("question %q", qn)
	}
}

// the index must find exactly what a scan of the rows in order finds
func TestPolicyIndexMatchesAScan(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	labels := []string{"a", "b", "c", "d"}
	name := func() string {
		n := 1 + rnd.Intn(3)
		var ps []string
		for i := 0; i < n; i++ {
			ps = append(ps, labels[rnd.Intn(len(labels))])
		}
		return strings.Join(ps, ".") + ".test"
	}
	var rows []PolicyRule
	for i := 0; i < 3000; i++ {
		c := []string{"*", "10.1.0.0/16", "10.2.0.0/16", "10.1.1.1"}[rnd.Intn(4)]
		var nm string
		switch rnd.Intn(8) {
		case 0:
			nm = "*"
		case 1:
			nm = "*." + name()
		case 2:
			nm = "*" + labels[rnd.Intn(4)] + "*." + labels[rnd.Intn(4)] + ".test"
		case 3:
			nm = "*." + labels[rnd.Intn(4)] + "*.test"
		case 4:
			nm = labels[rnd.Intn(4)] + "*.*" // no literal end
		case 5:
			nm = "?." + labels[rnd.Intn(4)] + "?.test"
		default:
			nm = name()
		}
		rows = append(rows, PolicyRule{Client: c, Name: nm, Servers: []string{"null"}})
	}
	p := &Pool{policy: buildPolicy(rows)}
	for i := 0; i < 3000; i++ {
		q := name()
		cl := netip.MustParseAddr(fmt.Sprintf("10.%d.1.%d", 1+rnd.Intn(3), 1+rnd.Intn(2)))
		var qi qinfo
		parseQuestion(cacheQuery(q, 1, nil), &qi)
		var want *policyRule
		for j := range p.policy {
			if p.policy[j].matches(cl, q) {
				want = &p.policy[j]
				break
			}
		}
		if got := p.lookup(cl, &qi); got != want {
			t.Fatalf("%s from %s: index found row %v, scan row %v", q, cl, rowNo(got), rowNo(want))
		}
	}
}

func rowNo(r *policyRule) int {
	if r == nil {
		return 0
	}
	return r.idx
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func TestPolicyMatchesAreLogged(t *testing.T) {
	var buf lockedBuf
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(old) })
	main := newFakeDNS(t)
	cfg := testDNSCfg(main.addr)
	cfg.Policy = []PolicyRule{{Client: "*", Name: "x.example", Servers: []string{"null"}}, {Client: "10.1.1.1", Name: "ok.example", Servers: []string{"pool"}}}
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return p })
	fe.ctx, fe.cancel = context.WithCancel(context.Background())
	t.Cleanup(fe.cancel)
	fe.resolve(cacheQuery("x.example", 1, nil), false, mustAddr("10.9.9.9"))
	fe.resolve(cacheQuery("ok.example", 1, nil), false, mustAddr("10.1.1.1"))
	fe.resolve(cacheQuery("none.example", 1, nil), false, mustAddr("10.1.1.1"))
	out := buf.String()
	if !strings.Contains(out, "policy row 1: 10.9.9.9 asked x.example A: answered null") ||
		!strings.Contains(out, "policy row 2: 10.1.1.1 asked ok.example A: left to the gateway's servers") || strings.Contains(out, "none.example") {
		t.Fatalf("log:\n%s", out)
	}
	buf = lockedBuf{}
	p.cfg.PolicyLog = false
	fe.resolve(cacheQuery("x.example", 1, nil), false, mustAddr("10.9.9.9"))
	if strings.Contains(buf.String(), "policy row") {
		t.Fatal("logged with policy_log off")
	}
}

// the answer may be written in the destination name column
func TestPolicyLocalAnswerInDestName(t *testing.T) {
	main := newFakeDNS(t)
	cfg := testDNSCfg(main.addr)
	cfg.ECS = false
	cfg.Policy = []PolicyRule{
		{Client: "*", Name: "a.example", Dest: "10.5.5.5"},
		{Client: "*", Name: "b.example", Dest: "10.5.5.6, 2001:db8::6"},
		{Client: "*", Name: "t.example", Dest: `TXT "hi"; TTL 120`},
		{Client: "*", Name: "c.example", Dest: "CNAME x.example"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Policy[0].Dest != "" || len(cfg.Policy[0].Servers) != 1 || cfg.Policy[0].Servers[0] != "A 10.5.5.5" {
		t.Fatalf("normalized: %+v", cfg.Policy[0])
	}
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return p })
	fe.ctx, fe.cancel = context.WithCancel(context.Background())
	t.Cleanup(fe.cancel)
	ask := func(name string, qt uint16) []byte {
		return fe.resolve(cacheQuery(name, qt, nil), false, mustAddr("10.9.9.9"))
	}
	if r := answersOf(t, ask("a.example", 1)); len(r) != 1 || !bytes.Equal(r[0].data, []byte{10, 5, 5, 5}) {
		t.Fatalf("A: %+v", r)
	}
	if r := answersOf(t, ask("b.example", 28)); len(r) != 1 || r[0].typ != 28 {
		t.Fatalf("AAAA: %+v", r)
	}
	if r := answersOf(t, ask("t.example", 16)); len(r) != 1 || r[0].ttl != 120 {
		t.Fatalf("TXT: %+v", r)
	}
	if r := answersOf(t, ask("c.example", 5)); len(r) != 1 || r[0].typ != 5 {
		t.Fatalf("CNAME: %+v", r)
	}
	bad := testDNSCfg(main.addr)
	bad.Policy = []PolicyRule{{Client: "*", Name: "a.example", Servers: []string{"8.8.8.8"}, Dest: "10.5.5.5"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("servers and a local answer together must be refused")
	}
	// a hostname destination is still a rename
	ok := testDNSCfg(main.addr)
	ok.Policy = []PolicyRule{{Client: "*", Name: "a.example", Servers: []string{"8.8.8.8"}, Dest: "b.example"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNameGlob(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"*host*.sub*.xyzzy.com", "host1.sub1.xyzzy.com", true},
		{"*host*.sub*.xyzzy.com", "myhost.sub.xyzzy.com", true},
		{"*host*.sub*.xyzzy.com", "a.host1.sub1.xyzzy.com", false}, // not a leading "*.": no extra labels
		{"*host*.sub*.xyzzy.com", "hos.sub1.xyzzy.com", false},
		{"*host*.sub*.xyzzy.com", "host1.sub1.xyzzy.org", false},
		{"ad-*.example.com", "ad-12.example.com", true},
		{"ad-*.example.com", "x.ad-12.example.com", false},
		{"*.sub*.xyzzy.com", "a.b.sub9.xyzzy.com", true},
		{"*.sub*.xyzzy.com", "sub9.xyzzy.com", true},
		{"*.sub*.xyzzy.com", "xyzzy.com", false},
		{"*host?.sub?.xyzzy.com", "myhost1.sub2.xyzzy.com", true},
		{"*host?.sub?.xyzzy.com", "host.sub2.xyzzy.com", false},
		{"*host?.sub?.xyzzy.com", "host12.sub2.xyzzy.com", false},
		{"*host?.sub?.xyzzy.com", "host1.sub.xyzzy.com", false},
		{"?", "a", true},
		{"?", "ab", false},
		{"host*", "host1", true},
		{"a*b", "ab", true},
		{"a*b", "axxb", true},
		{"a*b", "axxbc", false},
	}
	for _, c := range cases {
		g, is, err := parseGlob(c.pat)
		if !is || err != nil {
			t.Fatalf("%s: is=%v err=%v", c.pat, is, err)
		}
		if got := g.match(c.name); got != c.want {
			t.Errorf("%s vs %s: %v", c.pat, c.name, got)
		}
	}
	for _, s := range []string{"*", "*.example.com", "example.com"} {
		if _, is, _ := parseGlob(s); is {
			t.Errorf("%q is not a glob", s)
		}
	}
	if _, _, err := parseGlob("a*..b"); err == nil {
		t.Error("an empty label must be refused")
	}
	cfg := testDNSCfg("192.0.2.1")
	cfg.Policy = []PolicyRule{{Client: "*", Name: "*host*.sub*.xyzzy.com", Servers: []string{"1.1.1.1"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Policy = []PolicyRule{{Client: "*", Name: "*host*.xyzzy.com", Servers: []string{"1.1.1.1"}, Dest: "*.other.com"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("a *.name destination needs a *.name source")
	}
	cfg.Policy = []PolicyRule{{Client: "*", Name: "x.com", Servers: []string{"1.1.1.1"}, Dest: "a*.com"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("a pattern as a destination must be refused")
	}
}
