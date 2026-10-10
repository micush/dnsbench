package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
)

func optRR(size uint16, opts []byte) []byte {
	b := []byte{0, 0, typeOPT, byte(size >> 8), byte(size), 0, 0, 0, 0}
	b = binary.BigEndian.AppendUint16(b, uint16(len(opts)))
	return append(b, opts...)
}

func queryWith(t *testing.T, opt []byte) []byte {
	t.Helper()
	q, err := buildQuery(0x1234, "example.com", "A")
	if err != nil {
		t.Fatal(err)
	}
	if opt != nil {
		q = append(q, opt...)
		binary.BigEndian.PutUint16(q[10:], 1)
	}
	return q
}

// ecsOf finds the ECS option in a message's OPT record: family, prefix, address.
func ecsOf(t *testing.T, msg []byte) (family, prefix int, addr []byte, found bool) {
	t.Helper()
	rrs, add, ok := recordsAfterQuestion(msg)
	if !ok {
		t.Fatal("message does not parse")
	}
	for _, r := range rrs[add:] {
		if r.typ != typeOPT {
			continue
		}
		rd := msg[r.rdataOff:r.end]
		for i := 0; i+4 <= len(rd); {
			code, l := binary.BigEndian.Uint16(rd[i:]), int(binary.BigEndian.Uint16(rd[i+2:]))
			if code == optionECS {
				p := rd[i+4 : i+4+l]
				return int(binary.BigEndian.Uint16(p)), int(p[2]), p[4:], true
			}
			i += 4 + l
		}
	}
	return 0, 0, nil, false
}

func TestECSOptionEncoding(t *testing.T) {
	cases := []struct {
		client string
		p4, p6 int
		family int
		prefix int
		addr   []byte
	}{
		{"192.0.2.77", 24, 56, 1, 24, []byte{192, 0, 2}},
		{"10.1.255.9", 21, 56, 1, 21, []byte{10, 1, 248}}, // bits beyond the prefix are zeroed
		{"2001:db8:1:2ff::5", 24, 56, 2, 56, []byte{0x20, 0x01, 0x0d, 0xb8, 0, 1, 0x02}},
		{"::ffff:198.51.100.9", 24, 56, 1, 24, []byte{198, 51, 100}}, // v4-mapped counts as IPv4
	}
	for _, c := range cases {
		q, st, ok := addECS(queryWith(t, nil), netip.MustParseAddr(c.client), c.p4, c.p6, false)
		if !ok || !st.addedOPT {
			t.Fatalf("%s: not added", c.client)
		}
		fam, pre, addr, found := ecsOf(t, q)
		if !found || fam != c.family || pre != c.prefix || !bytes.Equal(addr, c.addr) {
			t.Fatalf("%s: family=%d prefix=%d addr=%v, want %d/%d/%v", c.client, fam, pre, addr, c.family, c.prefix, c.addr)
		}
	}
}

func TestECSAddAndStripWithoutClientEDNS(t *testing.T) {
	orig := queryWith(t, nil)
	out, st, ok := addECS(orig, netip.MustParseAddr("192.0.2.77"), 24, 56, false)
	if !ok || !st.addedOPT || !st.addedECS {
		t.Fatalf("%v %+v", ok, st)
	}
	if binary.BigEndian.Uint16(out[10:]) != 1 {
		t.Fatal("ARCOUNT must be 1")
	}
	rrs, add, _ := recordsAfterQuestion(out)
	if size := rrs[add].class; size != 512 {
		t.Fatalf("a client without EDNS must keep its 512-byte limit over UDP, payload=%d", size)
	}
	out2, _, _ := addECS(orig, netip.MustParseAddr("192.0.2.77"), 24, 56, true)
	if r2, a2, _ := recordsAfterQuestion(out2); r2[a2].class != 1232 {
		t.Fatal("TCP queries may advertise a bigger payload")
	}
	// the upstream echoes the OPT; the client must get the plain answer back
	resp := reply(orig, 0, 1)
	resp = append(resp, optRR(512, out[len(orig)+11:])...)
	binary.BigEndian.PutUint16(resp[10:], 1)
	got := stripECS(resp, st)
	if !bytes.Equal(got, reply(orig, 0, 1)) {
		t.Fatalf("stripped answer differs from the plain one:\n%x\n%x", got, reply(orig, 0, 1))
	}
	// an answer without OPT is left alone
	if plain := reply(orig, 0, 1); !bytes.Equal(stripECS(plain, st), plain) {
		t.Fatal("answer without OPT must be unchanged")
	}
}

func TestECSInsideClientOPT(t *testing.T) {
	cookie := []byte{0, 10, 0, 2, 0xAB, 0xCD} // some other option (code 10)
	orig := queryWith(t, optRR(1232, cookie))
	out, st, ok := addECS(orig, netip.MustParseAddr("192.0.2.77"), 24, 56, false)
	if !ok || st.addedOPT || !st.addedECS {
		t.Fatalf("%v %+v", ok, st)
	}
	rrs, add, _ := recordsAfterQuestion(out)
	if rrs[add].class != 1232 {
		t.Fatal("the client's own payload size must be kept")
	}
	if _, _, _, found := ecsOf(t, out); !found || !bytes.Contains(out, cookie) {
		t.Fatal("ECS added, the client's other option kept")
	}
	// strip: only the ECS option goes, the client's other option stays
	resp := append(reply(orig, 0, 1), optRR(1232, append(append([]byte(nil), cookie...), out[len(orig):][11:]...))...)
	binary.BigEndian.PutUint16(resp[10:], 1)
	got := stripECS(resp, st)
	if _, _, _, found := ecsOf(t, got); found {
		t.Fatal("ECS must be gone from the answer")
	}
	if !bytes.Contains(got, cookie) {
		t.Fatal("the client's own option must stay in the answer")
	}
}

func TestECSLeavesOtherQueriesAlone(t *testing.T) {
	c := netip.MustParseAddr("192.0.2.77")
	// the client already sent ECS (a downstream resolver): its choice wins
	own := ecsOption(netip.MustParseAddr("203.0.113.0"), 24, 56)
	q := queryWith(t, optRR(1232, own))
	if out, _, ok := addECS(q, c, 24, 56, false); ok || !bytes.Equal(out, q) {
		t.Fatal("an existing ECS option must be passed through untouched")
	}
	// extra records after the OPT (e.g. TSIG) must not be rewritten
	q2 := append(queryWith(t, optRR(512, nil)), 0, 0, 250, 0, 255, 0, 0, 0, 0, 0, 0)
	binary.BigEndian.PutUint16(q2[10:], 2)
	if out, _, ok := addECS(q2, c, 24, 56, false); ok || !bytes.Equal(out, q2) {
		t.Fatal("a query with more than the OPT record must go out unchanged")
	}
	// clients that are not worth reporting
	for _, a := range []string{"127.0.0.1", "::1", "169.254.1.1", "fe80::1", "0.0.0.0", "224.0.0.1"} {
		if _, _, ok := addECS(queryWith(t, nil), netip.MustParseAddr(a), 24, 56, false); ok {
			t.Fatalf("%s must not be sent", a)
		}
	}
	// garbage in, unchanged out
	for _, bad := range [][]byte{nil, {1, 2, 3}, append(queryWith(t, nil)[:20], 0xFF)} {
		if out, _, ok := addECS(bad, c, 24, 56, false); ok || !bytes.Equal(out, bad) {
			t.Fatalf("garbage %x must pass through", bad)
		}
		if got := stripECS(bad, ecsState{addedECS: true}); !bytes.Equal(got, bad) {
			t.Fatal("garbage answer must pass through")
		}
	}
}

// ecsServer answers like a real server: it echoes ECS back, optionally rejects
// EDNS with FORMERR, and remembers every query it saw.
type ecsServer struct {
	addr    string
	mu      sync.Mutex
	seen    [][]byte
	formerr bool
	refuse  atomic.Bool // REFUSED to any query that carries EDNS (as a public resolver does for a private client subnet)
	pc      net.PacketConn
	tcpAlso bool
}

func newECSServer(t *testing.T, formerr bool) *ecsServer {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &ecsServer{addr: pc.LocalAddr().String(), formerr: formerr, pc: pc}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.seen = append(s.seen, q)
			s.mu.Unlock()
			rrs, add, _ := recordsAfterQuestion(q)
			var optBytes []byte
			if len(rrs) > add && rrs[add].typ == typeOPT {
				optBytes = q[rrs[add].start:rrs[add].end]
			}
			var r []byte
			switch {
			case optBytes != nil && s.formerr:
				r = reply(q, rcodeFormErr, 0)
			case optBytes != nil && s.refuse.Load():
				r = reply(q, rcodeRefused, 0)
			default:
				r = reply(q, 0, 1)
				if optBytes != nil {
					r = append(r, optBytes...)
					binary.BigEndian.PutUint16(r[10:], 1)
				}
			}
			pc.WriteTo(r, from)
		}
	}()
	t.Cleanup(func() { pc.Close() })
	return s
}

func (s *ecsServer) last() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[len(s.seen)-1]
}

func ecsPool(addr string, on bool) *Pool {
	cfg := testDNSCfg(addr)
	cfg.ECS = on
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	return p
}

func TestForwardAttachesClientSubnet(t *testing.T) {
	srv := newECSServer(t, false)
	client := netip.MustParseAddr("198.51.100.77")
	q, _ := buildQuery(0x4242, "example.com", "A")

	p := ecsPool(srv.addr, true)
	resp, err := p.ForwardFrom(context.Background(), q, false, client)
	if err != nil {
		t.Fatal(err)
	}
	fam, pre, addr, found := ecsOf(t, srv.last())
	if !found || fam != 1 || pre != 24 || !bytes.Equal(addr, []byte{198, 51, 100}) {
		t.Fatalf("the server must see the client's /24, got found=%v fam=%d pre=%d addr=%v", found, fam, pre, addr)
	}
	if _, _, _, found := ecsOf(t, resp); found || binary.BigEndian.Uint16(resp[10:]) != 0 {
		t.Fatal("the client never asked for EDNS: the answer must not carry the OPT record")
	}
	if p.ECSSent.Load() != 1 {
		t.Fatalf("ECSSent = %d", p.ECSSent.Load())
	}

	// off: the server sees the plain query
	off := ecsPool(srv.addr, false)
	if _, err := off.ForwardFrom(context.Background(), q, false, client); err != nil {
		t.Fatal(err)
	}
	if !sameExceptID(srv.last(), q) {
		t.Fatal("with ECS off the query must be forwarded byte for byte")
	}
	// no known client (Forward without an address): nothing is attached either
	if _, err := p.Forward(context.Background(), q, false); err != nil || !sameExceptID(srv.last(), q) {
		t.Fatal("without a client address there is nothing to attach")
	}
}

func TestForwardRetriesWithoutECSOnFormErr(t *testing.T) {
	srv := newECSServer(t, true) // rejects any EDNS
	q, _ := buildQuery(0x4242, "example.com", "A")
	p := ecsPool(srv.addr, true)
	resp, err := p.ForwardFrom(context.Background(), q, false, netip.MustParseAddr("198.51.100.77"))
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := parseHeader(resp); h.rcode != 0 || h.ancount != 1 {
		t.Fatalf("the client must get the real answer, got rcode=%d answers=%d", h.rcode, h.ancount)
	}
	if !sameExceptID(srv.last(), q) {
		t.Fatal("the retry must be the original query")
	}
}

func TestECSConfigValidation(t *testing.T) {
	d := testDNSCfg("1.1.1.1")
	if !d.ECS || d.ECSPrefix4 != 24 || d.ECSPrefix6 != 56 { // on by default since v107
		t.Fatalf("defaults: ecs=%v /%d /%d", d.ECS, d.ECSPrefix4, d.ECSPrefix6)
	}
	for _, c := range []struct{ p4, p6 int }{{0, 56}, {33, 56}, {24, 0}, {24, 129}} {
		x := testDNSCfg("1.1.1.1")
		x.ECSPrefix4, x.ECSPrefix6 = c.p4, c.p6
		if err := x.Validate(); err == nil {
			t.Fatalf("prefixes /%d /%d must be refused", c.p4, c.p6)
		}
	}
	// old configs without the new keys still load with the defaults
	var old DNSConfig
	if err := (&old).UnmarshalJSON([]byte(`{"servers":["1.1.1.1"],"queries":["a.example"]}`)); err != nil || old.ECSPrefix4 != 24 {
		t.Fatalf("old config: %v %+v", err, old)
	}
}

// A server that REFUSES queries carrying a client subnet (a public resolver, for a private client network) is
// asked again without it, the client gets the real answer, and the server is not sent ECS from then on.
func TestForwardRetriesWithoutECSOnRefused(t *testing.T) {
	srv := newECSServer(t, false)
	srv.refuse.Store(true)
	p := ecsPool(srv.addr, true)
	client := netip.MustParseAddr("192.168.5.20")
	q1, _ := buildQuery(0x1111, "one.example", "A")
	resp, err := p.ForwardFrom(context.Background(), q1, false, client)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := parseHeader(resp); h.rcode != 0 || h.ancount != 1 {
		t.Fatalf("the client must get the real answer, got rcode=%d answers=%d", h.rcode, h.ancount)
	}
	if !sameExceptID(srv.last(), q1) {
		t.Fatal("the retry must be the original query")
	}
	srv.mu.Lock()
	before := len(srv.seen)
	srv.mu.Unlock()
	q2, _ := buildQuery(0x2222, "two.example", "A")
	if resp, err := p.ForwardFrom(context.Background(), q2, false, client); err != nil {
		t.Fatal(err)
	} else if h, _ := parseHeader(resp); h.rcode != 0 {
		t.Fatalf("second query rcode %d", h.rcode)
	}
	srv.mu.Lock()
	after := len(srv.seen)
	srv.mu.Unlock()
	if after-before != 1 {
		t.Fatalf("the second query took %d round trips, want 1 (no ECS sent any more)", after-before)
	}
}

// ECS is on unless the file says otherwise (since v107): a dns block without the key gets it, an explicit false keeps it off
func TestECSOnByDefaultInFiles(t *testing.T) {
	var d DNSConfig
	if err := json.Unmarshal([]byte(`{"servers":["1.1.1.1"]}`), &d); err != nil || !d.ECS {
		t.Fatalf("no ecs key: ecs=%v err=%v", d.ECS, err)
	}
	var off DNSConfig
	if err := json.Unmarshal([]byte(`{"servers":["1.1.1.1"],"ecs":false}`), &off); err != nil || off.ECS {
		t.Fatalf("ecs:false must stay off: ecs=%v err=%v", off.ECS, err)
	}
	if !defaultDNS().ECS {
		t.Fatal("defaultDNS must have ECS on")
	}
}

// sameExceptID: equal messages whatever their transaction IDs (the forwarder sends its own random ID upstream).
func sameExceptID(a, b []byte) bool {
	return len(a) == len(b) && len(a) >= 2 && bytes.Equal(a[2:], b[2:])
}
