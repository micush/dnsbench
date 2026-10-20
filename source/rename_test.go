package main

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
)

func nameAt(t *testing.T, b []byte, off int) (string, int) {
	t.Helper()
	l, n, ok := unpackName(b, off)
	if !ok {
		t.Fatal("unreadable name")
	}
	return strings.Join(l, "."), n
}

func TestDestinationNameValidation(t *testing.T) {
	for _, c := range []struct {
		name, dest string
		ok         bool
	}{
		{"reddit.com", "zdnet.com", true},
		{"*.reddit.com", "*.zdnet.com", true},
		{"*.reddit.com", "zdnet.com", true},
		{"*", "zdnet.com", true},
		{"*.reddit.com", "*", true},
		{"reddit.com", "*.zdnet.com", false}, // nothing to keep the front of
		{"*", "*.zdnet.com", false},
		{"reddit.com", "not a name", false},
	} {
		_, err := normalizePolicy([]PolicyRule{{Client: "*", Name: c.name, Servers: []string{"192.0.2.9"}, Dest: c.dest}})
		if (err == nil) != c.ok {
			t.Errorf("%s -> %s: ok=%v err=%v", c.name, c.dest, c.ok, err)
		}
	}
}

func TestRenameQueryAndAnswer(t *testing.T) {
	rows := buildPolicy([]PolicyRule{
		{Client: "*", Name: "*.src.example", Servers: []string{"192.0.2.9"}, Dest: "*.dst.example"},
		{Client: "*", Name: "one.example", Servers: []string{"192.0.2.9"}, Dest: "other.test"},
	})
	p := &Pool{policy: rows}
	// wildcard: www.SRC.example is asked as www.dst.example
	q := cacheQuery("WWW.src.example", 1, nil)
	var qi qinfo
	parseQuestion(q, &qi)
	r := p.policyFor(mustAddr("10.0.0.1"), &qi)
	rn, nq, err := r.newRenamer(q, &qi)
	if err != nil || rn == nil {
		t.Fatal(err)
	}
	if got, _ := nameAt(t, nq, 12); got != "WWW.dst.example" {
		t.Fatalf("asked %q", got)
	}
	// an answer: question, A for the name, CNAME to a name under dst.example, SOA in authority
	w := &msgWriter{buf: []byte{0x11, 0x11, 0x81, 0x80, 0, 1, 0, 2, 0, 1, 0, 0}, seen: map[string]int{}}
	w.name([]string{"WWW", "dst", "example"}, true)
	w.buf = append(w.buf, 0, 1, 0, 1)
	w.name([]string{"WWW", "dst", "example"}, true)
	w.buf = append(w.buf, 0, 5, 0, 1, 0, 0, 0, 60)
	at := len(w.buf)
	w.buf = append(w.buf, 0, 0)
	w.name([]string{"edge", "dst", "example"}, true)
	binary.BigEndian.PutUint16(w.buf[at:], uint16(len(w.buf)-at-2))
	w.name([]string{"edge", "dst", "example"}, true)
	w.buf = append(w.buf, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 7)
	w.name([]string{"dst", "example"}, true)
	w.buf = append(w.buf, 0, 6, 0, 1, 0, 0, 0, 60)
	at = len(w.buf)
	w.buf = append(w.buf, 0, 0)
	w.name([]string{"ns", "dst", "example"}, true)
	w.name([]string{"host", "dst", "example"}, true)
	w.buf = append(w.buf, make([]byte, 20)...)
	binary.BigEndian.PutUint16(w.buf[at:], uint16(len(w.buf)-at-2))
	out, err := rn.response(w.buf)
	if err != nil {
		t.Fatal(err)
	}
	if qn, _ := nameAt(t, out, 12); qn != "WWW.src.example" {
		t.Fatalf("question %q", qn)
	}
	rrs, _, ok := recordsAfterQuestion(out)
	if !ok || len(rrs) != 3 {
		t.Fatalf("records %d ok=%v", len(rrs), ok)
	}
	if n, _ := nameAt(t, out, rrs[0].start); n != "WWW.src.example" {
		t.Fatalf("owner %q", n)
	}
	if n, _ := nameAt(t, out, rrs[0].rdataOff); n != "edge.src.example" {
		t.Fatalf("cname target %q", n)
	}
	if n, _ := nameAt(t, out, rrs[1].start); n != "edge.src.example" {
		t.Fatalf("second owner %q", n)
	}
	if n, _ := nameAt(t, out, rrs[2].start); n != "src.example" { // the zone apex of a "*.name" destination is the source apex
		t.Fatalf("soa owner %q", n)
	}
	// exact: one.example is asked as other.test and the answer reads as one.example
	q = cacheQuery("one.example", 1, nil)
	parseQuestion(q, &qi)
	r = p.policyFor(mustAddr("10.0.0.1"), &qi)
	rn, nq, err = r.newRenamer(q, &qi)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := nameAt(t, nq, 12); got != "other.test" {
		t.Fatalf("asked %q", got)
	}
	// a record of a type that may hold names that cannot be moved is refused
	bad := append([]byte(nil), reply(nq, 0, 0)...)
	binary.BigEndian.PutUint16(bad[6:], 1)
	bad = append(bad, 0xC0, 12, 0, 14 /* RP */, 0, 1, 0, 0, 0, 60, 0, 2, 0, 0)
	if _, err := rn.response(bad); err == nil {
		t.Fatal("an unknown record type was passed through")
	}
}

func TestPolicyRenameThroughFrontend(t *testing.T) {
	up := newFakeDNS(t)
	main := newFakeDNS(t)
	cfg := testDNSCfg(main.addr)
	cfg.ECS = false
	cfg.Policy = []PolicyRule{
		{Client: "10.1.1.1", Name: "*.src.example", Servers: []string{up.addr}, Dest: "*.dst.example"},
		{Client: "*", Name: "*.src.example", Servers: []string{up.addr}, Dest: "other.test"},
	}
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return p })
	fe.ctx, fe.cancel = context.WithCancel(context.Background())
	t.Cleanup(fe.cancel)

	ask := func(client string) []byte {
		t.Helper()
		resp := fe.resolve(cacheQuery("www.src.example", 1, nil), false, mustAddr(client))
		if h, _ := parseHeader(resp); h.rcode != 0 || h.ancount != 1 {
			t.Fatalf("%s: %+v", client, h)
		}
		if qn, _, _ := questionOf(resp); qn != "www.src.example" {
			t.Fatalf("question %q", qn)
		}
		return resp
	}
	ask("10.1.1.1")
	if got, _ := up.lastQ.Load().(string); got != "www.dst.example" {
		t.Fatalf("servers asked %q", got)
	}
	resp := ask("10.9.9.9")
	if got, _ := up.lastQ.Load().(string); got != "other.test" {
		t.Fatalf("servers asked %q", got)
	}
	rrs, _, _ := recordsAfterQuestion(resp)
	if n, _ := nameAt(t, resp, rrs[0].start); n != "www.src.example" {
		t.Fatalf("answer owner %q", n)
	}
}

func TestPolicyRenameWithoutServersUsesThePool(t *testing.T) {
	if _, err := normalizePolicy([]PolicyRule{{Client: "*", Name: "a.example"}}); err == nil {
		t.Fatal("a row with neither servers nor a destination was accepted")
	}
	main := newFakeDNS(t)
	cfg := testDNSCfg(main.addr)
	cfg.ECS = false
	cfg.Policy = []PolicyRule{
		{Client: "*", Name: "*.src.example", Dest: "*.dst.example"},
		{Client: "*", Name: "two.example", Servers: []string{"pool"}, Dest: "other.test"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Policy[1].Servers) != 0 {
		t.Fatalf("pool with a destination must mean no servers: %+v", cfg.Policy[1])
	}
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return p })
	fe.ctx, fe.cancel = context.WithCancel(context.Background())
	t.Cleanup(fe.cancel)
	for _, c := range []struct{ ask, want string }{{"www.src.example", "www.dst.example"}, {"two.example", "other.test"}} {
		resp := fe.resolve(cacheQuery(c.ask, 1, nil), false, mustAddr("10.2.2.2"))
		if h, _ := parseHeader(resp); h.rcode != 0 || h.ancount != 1 {
			t.Fatalf("%s: %+v", c.ask, h)
		}
		if qn, _, _ := questionOf(resp); qn != c.ask {
			t.Fatalf("question %q", qn)
		}
		if got, _ := main.lastQ.Load().(string); got != c.want {
			t.Fatalf("%s: the pool was asked %q, want %q", c.ask, got, c.want)
		}
	}
}

func TestWildcardIncludesTheNameItself(t *testing.T) {
	p := &Pool{policy: buildPolicy([]PolicyRule{{Client: "*", Name: "*.src.example", Dest: "*.dst.example"}})}
	q := cacheQuery("src.example", 1, nil)
	var qi qinfo
	parseQuestion(q, &qi)
	r := p.policyFor(mustAddr("10.0.0.1"), &qi)
	if r == nil {
		t.Fatal("*.src.example must match src.example")
	}
	rn, nq, err := r.newRenamer(q, &qi)
	if err != nil || rn == nil {
		t.Fatal(err)
	}
	if got, _ := nameAt(t, nq, 12); got != "dst.example" {
		t.Fatalf("asked %q", got)
	}
	if got := strings.Join(rn.mapName([]string{"dst", "example"}), "."); got != "src.example" {
		t.Fatalf("answer name %q", got)
	}
}
