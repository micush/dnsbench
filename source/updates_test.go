package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func wireName(n string) []byte {
	var b []byte
	if n != "." && n != "" {
		for _, l := range strings.Split(strings.TrimSuffix(n, "."), ".") {
			b = append(b, byte(len(l)))
			b = append(b, l...)
		}
	}
	return append(b, 0)
}

func rr(owner string, typ uint16, ttl uint32, rdata []byte) []byte {
	b := wireName(owner)
	b = binary.BigEndian.AppendUint16(b, typ)
	b = binary.BigEndian.AppendUint16(b, 1)
	b = binary.BigEndian.AppendUint32(b, ttl)
	b = binary.BigEndian.AppendUint16(b, uint16(len(rdata)))
	return append(b, rdata...)
}

func soaData(mname string) []byte {
	b := append(wireName(mname), wireName("hostmaster.example.")...)
	for i := 0; i < 5; i++ {
		b = binary.BigEndian.AppendUint32(b, 60)
	}
	return b
}

// fakeResolver answers the lookups an update needs, from a table, and counts them.
type fakeResolver struct {
	addr  string
	soaQ  atomic.Int32
	table func(name string, qt uint16) (rcode int, answer, authority [][]byte)
}

func newFakeResolver(t *testing.T, table func(string, uint16) (int, [][]byte, [][]byte)) *fakeResolver {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeResolver{addr: pc.LocalAddr().String(), table: table}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := append([]byte(nil), buf[:n]...)
			name, qt, ok := questionOf(q)
			if !ok {
				continue
			}
			if qt == typeSOA {
				f.soaQ.Add(1)
			}
			rc, an, ns := f.table(name, qt)
			end := 12 + len(wireName(name)) + 4
			r := append([]byte(nil), q[:end]...)
			binary.BigEndian.PutUint16(r[2:], 0x8180|uint16(rc))
			binary.BigEndian.PutUint16(r[6:], uint16(len(an)))
			binary.BigEndian.PutUint16(r[8:], uint16(len(ns)))
			binary.BigEndian.PutUint16(r[10:], 0)
			for _, x := range an {
				r = append(r, x...)
			}
			for _, x := range ns {
				r = append(r, x...)
			}
			pc.WriteTo(r, from)
		}
	}()
	t.Cleanup(func() { pc.Close() })
	return f
}

// fakePrimary is a primary server: it records every message and answers NOERROR with a marker.
type fakePrimary struct {
	port string
	mu   sync.Mutex
	got  [][]byte
	tcp  atomic.Int32
}

func (p *fakePrimary) msgs() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.got...)
}

func (p *fakePrimary) n() int { return len(p.msgs()) }

func (p *fakePrimary) reply(m []byte) []byte {
	p.mu.Lock()
	p.got = append(p.got, append([]byte(nil), m...))
	p.mu.Unlock()
	r := append([]byte(nil), m[:12]...)
	binary.BigEndian.PutUint16(r[2:], 0x8000|opcodeUpdate<<11) // QR, opcode UPDATE, NOERROR
	binary.BigEndian.PutUint16(r[4:], 0)
	binary.BigEndian.PutUint16(r[6:], 0)
	binary.BigEndian.PutUint16(r[8:], 0)
	binary.BigEndian.PutUint16(r[10:], 0)
	return r
}

func newFakePrimary(t *testing.T) *fakePrimary {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	tl, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePrimary{port: itoa(port)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(p.reply(buf[:n]), from)
		}
	}()
	go func() {
		for {
			c, err := tl.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var lb [2]byte
				if _, err := io.ReadFull(c, lb[:]); err != nil {
					return
				}
				m := make([]byte, binary.BigEndian.Uint16(lb[:]))
				io.ReadFull(c, m)
				p.tcp.Add(1)
				r := p.reply(m)
				c.Write(append([]byte{byte(len(r) >> 8), byte(len(r))}, r...))
			}()
		}
	}()
	t.Cleanup(func() { pc.Close(); tl.Close() })
	return p
}

// updateMsg builds an UPDATE for zone adding one A record, with a stand-in TSIG at the end.
func updateMsg(id uint16, zone string, ztype, zclass uint16) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b, id)
	binary.BigEndian.PutUint16(b[2:], opcodeUpdate<<11)
	binary.BigEndian.PutUint16(b[4:], 1) // ZOCOUNT
	binary.BigEndian.PutUint16(b[8:], 1) // UPCOUNT
	binary.BigEndian.PutUint16(b[10:], 1)
	b = append(b, wireName(zone)...)
	b = binary.BigEndian.AppendUint16(b, ztype)
	b = binary.BigEndian.AppendUint16(b, zclass)
	b = append(b, rr("host."+zone, typeA, 300, []byte{192, 0, 2, 7})...)
	return append(b, rr("key.", 250, 0, []byte("not-really-a-signature"))...)
}

func updTable(primary string) func(string, uint16) (int, [][]byte, [][]byte) {
	return func(name string, qt uint16) (int, [][]byte, [][]byte) {
		switch {
		case qt == typeSOA && name == "corp.test":
			return 0, [][]byte{rr(name, typeSOA, 60, soaData("ns1.corp.test."))}, nil
		case qt == typeSOA && name == "fb.test":
			return 0, [][]byte{rr(name, typeSOA, 60, soaData("gone.fb.test."))}, nil
		case qt == typeSOA && name == "loop.test":
			return 0, [][]byte{rr(name, typeSOA, 60, soaData("self.loop.test."))}, nil
		case qt == typeSOA && name == "sub.corp.test": // below the apex: the SOA is in the authority section
			return 0, nil, [][]byte{rr("corp.test", typeSOA, 60, soaData("ns1.corp.test."))}
		case qt == typeSOA:
			return 3, nil, nil
		case qt == typeA && name == "ns1.corp.test", qt == typeA && name == "ns2.fb.test":
			return 0, [][]byte{rr(name, typeA, 60, []byte{127, 0, 0, 1})}, nil
		case qt == typeA && name == "self.loop.test":
			return 0, [][]byte{rr(name, typeA, 60, []byte{127, 0, 0, 2})}, nil
		case qt == typeNS && name == "fb.test":
			return 0, [][]byte{rr(name, typeNS, 60, wireName("ns2.fb.test."))}, nil
		case qt == typeA || qt == typeAAAA && strings.HasSuffix(name, ".example"):
			if strings.HasSuffix(name, ".example") {
				return 0, [][]byte{rr(name, typeA, 60, []byte{192, 0, 2, 1})}, nil
			}
		}
		return 0, nil, nil
	}
}

func updRig(t *testing.T) (*DNSFrontend, *fakeResolver, *fakePrimary, string, *Pool) {
	t.Helper()
	pr := newFakePrimary(t)
	old := updPort
	updPort = pr.port
	t.Cleanup(func() { updPort = old })
	res := newFakeResolver(t, updTable(pr.port))
	p := NewPool(testDNSCfg(res.addr))
	p.ProbeNow(context.Background())
	fe := NewDNSFrontend(mustAddr("127.0.0.2"), 0, func() *Pool { return p })
	fe.ctx, fe.cancel = context.WithCancel(context.Background())
	t.Cleanup(fe.cancel)
	return fe, res, pr, "", p
}

func rcodeOf(t *testing.T, resp []byte) int {
	t.Helper()
	h, ok := parseHeader(resp)
	if !ok {
		t.Fatal("no response")
	}
	return h.rcode
}

func TestUpdateForwardedUnchangedToSOAPrimary(t *testing.T) {
	fe, res, pr, _, _ := updRig(t)
	msg := updateMsg(0x1234, "corp.test", typeSOA, 1)
	resp := fe.handleUpdate(msg, false, mustAddr("192.0.2.9"))
	if rcodeOf(t, resp) != rcodeNoError || (resp[2]>>3)&0xF != opcodeUpdate || binary.BigEndian.Uint16(resp) != 0x1234 {
		t.Fatalf("response %x", resp)
	}
	if got := pr.msgs(); len(got) != 1 || !bytes.Equal(got[0], msg) {
		t.Fatalf("the primary must get the message byte for byte: %d messages", len(got))
	}
	// the second update uses the cached primary: no new SOA lookup
	before := res.soaQ.Load()
	fe.handleUpdate(updateMsg(0x1235, "corp.test", typeSOA, 1), false, mustAddr("192.0.2.9"))
	if res.soaQ.Load() != before || pr.n() != 2 {
		t.Fatalf("cache: soa queries %d→%d, delivered %d", before, res.soaQ.Load(), pr.n())
	}
}

func TestUpdateOverTCP(t *testing.T) {
	fe, _, pr, _, _ := updRig(t)
	if rc := rcodeOf(t, fe.handleUpdate(updateMsg(7, "corp.test", typeSOA, 1), true, mustAddr("192.0.2.9"))); rc != rcodeNoError || pr.tcp.Load() != 1 {
		t.Fatalf("rcode %d, tcp deliveries %d", rc, pr.tcp.Load())
	}
}

func TestUpdateBelowApexUsesAuthoritySOA(t *testing.T) {
	fe, _, pr, _, _ := updRig(t)
	if rc := rcodeOf(t, fe.handleUpdate(updateMsg(8, "sub.corp.test", typeSOA, 1), false, mustAddr("192.0.2.9"))); rc != rcodeNoError || pr.n() != 1 {
		t.Fatalf("rcode %d delivered %d", rc, pr.n())
	}
}

func TestUpdateFallsBackToNameServers(t *testing.T) {
	fe, _, pr, _, _ := updRig(t)
	if rc := rcodeOf(t, fe.handleUpdate(updateMsg(9, "fb.test", typeSOA, 1), false, mustAddr("192.0.2.9"))); rc != rcodeNoError || pr.n() != 1 {
		t.Fatalf("rcode %d delivered %d", rc, pr.n())
	}
}

func TestUpdateUnknownZoneIsNotAuth(t *testing.T) {
	fe, _, pr, _, _ := updRig(t)
	resp := fe.handleUpdate(updateMsg(10, "nosuch.test", typeSOA, 1), false, mustAddr("192.0.2.9"))
	if rcodeOf(t, resp) != rcodeNotAuth || (resp[2]>>3)&0xF != opcodeUpdate || pr.n() != 0 {
		t.Fatalf("response %x delivered %d", resp, pr.n())
	}
}

func TestUpdateNeverForwardedToItself(t *testing.T) {
	fe, _, pr, _, _ := updRig(t)
	// the primary's name resolves to the frontend's own address (127.0.0.2): refuse to loop
	if rc := rcodeOf(t, fe.handleUpdate(updateMsg(11, "loop.test", typeSOA, 1), false, mustAddr("192.0.2.9"))); rc != rcodeServFail || pr.n() != 0 {
		t.Fatalf("rcode %d delivered %d", rc, pr.n())
	}
}

func TestUpdateSwitchedOffAndMalformed(t *testing.T) {
	fe, _, pr, _, p := updRig(t)
	for _, c := range []struct {
		msg  []byte
		want int
	}{
		{updateMsg(1, "corp.test", typeA, 1), rcodeFormErr},  // zone type must be SOA
		{updateMsg(2, "corp.test", typeSOA, 3), rcodeNotImp}, // class CH
		{func() []byte {
			m := updateMsg(3, "corp.test", typeSOA, 1)
			binary.BigEndian.PutUint16(m[4:], 0)
			return m
		}(), rcodeFormErr},
		{func() []byte { m := updateMsg(4, "corp.test", typeSOA, 1); return m[:20] }(), rcodeFormErr},
	} {
		if rc := rcodeOf(t, fe.handleUpdate(c.msg, false, mustAddr("192.0.2.9"))); rc != c.want {
			t.Errorf("rcode %d, want %d", rc, c.want)
		}
	}
	p.cfg.ForwardUpdates = false
	if rc := rcodeOf(t, fe.handleUpdate(updateMsg(5, "corp.test", typeSOA, 1), false, mustAddr("192.0.2.9"))); rc != rcodeRefused {
		t.Fatalf("switched off: rcode %d", rc)
	}
	if pr.n() != 0 {
		t.Fatalf("nothing may be forwarded: %d", pr.n())
	}
}

func TestUpdateReachesResolveAndStats(t *testing.T) {
	fe, _, pr, _, p := updRig(t)
	p.cfg.ForwardUpdates = true
	msg := updateMsg(0x2222, "corp.test", typeSOA, 1)
	if !isUpdateMsg(msg) || isUpdateMsg(append([]byte{msg[0], msg[1], 0x80 | msg[2]}, msg[3:]...)) {
		t.Fatal("isUpdateMsg")
	}
	q, _ := buildQuery(1, "x.example", "A")
	if isUpdateMsg(q) {
		t.Fatal("a query is not an update")
	}
	resp := fe.resolve(msg, false, mustAddr("192.0.2.9"))
	if rcodeOf(t, resp) != rcodeNoError || pr.n() != 1 {
		t.Fatalf("resolve: %x delivered %d", resp, pr.n())
	}
	if qtypeName(qtUpdate) != "UPDATE" {
		t.Fatal("statistics name")
	}
	r := qstats.Query(time.Now().Add(-time.Hour), time.Now(), QFilter{})
	found := false
	for _, d := range r.Domains {
		if d.Name == "corp.test" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the update is not in the statistics: %+v", r.Domains)
	}
}

func TestReadName(t *testing.T) {
	m := append(make([]byte, 12), wireName("a.example.com")...)
	m = append(m, 0xC0, 12) // pointer back to the first name
	if n, next, err := readName(m, len(m)-2); err != nil || n != "a.example.com" || next != len(m) {
		t.Fatalf("%q %d %v", n, next, err)
	}
	loop := append(make([]byte, 12), 0xC0, 12)
	if _, _, err := readName(loop, 12); err == nil {
		t.Fatal("a pointer loop must fail")
	}
	if _, _, err := readName([]byte{3, 'a', 'b'}, 0); err == nil {
		t.Fatal("a truncated name must fail")
	}
	if n, _, _ := readName([]byte{0}, 0); n != "." {
		t.Fatalf("root %q", n)
	}
}

func TestUpdateInflightDuplicateAndLoop(t *testing.T) {
	fe, _, pr, _, _ := updRig(t)
	msg := updateMsg(0x7777, "corp.test", typeSOA, 1)
	key := "30583|corp.test" // 0x7777
	fe.upd.inflight = map[string]netip.Addr{key: mustAddr("192.0.2.9")}
	if resp := fe.handleUpdate(msg, false, mustAddr("192.0.2.9")); resp != nil {
		t.Fatalf("a retransmission from the same client gets no second answer: %x", resp)
	}
	if rc := rcodeOf(t, fe.handleUpdate(msg, false, mustAddr("10.9.9.9"))); rc != rcodeRefused {
		t.Fatalf("same ID and zone from another address: rcode %d", rc)
	}
	if pr.n() != 0 {
		t.Fatalf("nothing may be forwarded: %d", pr.n())
	}
}
