package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"
)

func TestValidHostName(t *testing.T) {
	for _, ok := range []string{"ann-pc.corp.example", "A_b.C.d.", "x1.y"} {
		if _, err := validHostName(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ann", "-a.b", "a-.b", "a..b", "a b.c", "ä.example", strings.Repeat("a", 64) + ".x"} {
		if _, err := validHostName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func regTable(primary string) func(string, uint16) (int, [][]byte, [][]byte) {
	base := updTable(primary)
	return func(name string, qt uint16) (int, [][]byte, [][]byte) {
		if qt == typeSOA && strings.HasSuffix(name, ".in-addr.arpa") {
			return 0, nil, [][]byte{rr("10.in-addr.arpa", typeSOA, 60, soaData("ns1.corp.test."))}
		}
		if qt == typeSOA && name == "ann-pc.corp.test" {
			return 0, nil, [][]byte{rr("corp.test", typeSOA, 60, soaData("ns1.corp.test."))}
		}
		if qt == typeSOA && name == "old-pc.corp.test" {
			return 0, nil, [][]byte{rr("corp.test", typeSOA, 60, soaData("ns1.corp.test."))}
		}
		return base(name, qt)
	}
}

// updateParts reads the zone, the update-section records and whether a TSIG follows.
func updateParts(t *testing.T, m []byte) (zone string, rrs []msgRR, tsig bool, names []string) {
	t.Helper()
	if m[2]>>3&15 != opcodeUpdate {
		t.Fatalf("opcode %d", m[2]>>3&15)
	}
	z, n, err := readName(m, 12)
	if err != nil {
		t.Fatal(err)
	}
	off := n + 4
	all := int(binary.BigEndian.Uint16(m[8:])) + int(binary.BigEndian.Uint16(m[10:]))
	for i := 0; i < all; i++ {
		nm, n, err := readName(m, off)
		if err != nil {
			t.Fatal(err)
		}
		r := msgRR{name: nm, typ: binary.BigEndian.Uint16(m[n:]), ttl: binary.BigEndian.Uint32(m[n+4:]), off: n + 10, length: int(binary.BigEndian.Uint16(m[n+8:]))}
		off = r.off + r.length
		if r.typ == typeTSIG {
			tsig = true
			continue
		}
		rrs = append(rrs, r)
		names = append(names, nm)
	}
	return z, rrs, tsig, names
}

func TestRegisterHostName(t *testing.T) {
	pr := newFakePrimary(t)
	old := updPort
	updPort = pr.port
	t.Cleanup(func() { updPort = old })
	res := newFakeResolver(t, regTable(pr.port))
	p := NewPool(testDNSCfg(res.addr))
	p.ProbeNow(context.Background())

	lines, err := registerHostName(context.Background(), p, "ann-pc.corp.test", "old-pc.corp.test", mustAddr("10.1.2.3"), 0, "", "")
	if err != nil {
		t.Fatalf("%v %v", err, lines)
	}
	ms := pr.msgs()
	if len(ms) != 4 || len(lines) != 2 {
		t.Fatalf("%d updates, lines %v", len(ms), lines)
	}
	// each record set gets a delete update first and then an update that adds
	z, rrs, signed, names := updateParts(t, ms[0])
	if z != "corp.test" || signed || len(rrs) != 2 || names[0] != "ann-pc.corp.test" || names[1] != "old-pc.corp.test" || rrs[0].typ != typeA {
		t.Fatalf("delete update: zone %q signed %v names %v", z, signed, names)
	}
	if binary.BigEndian.Uint16(ms[0][rrs[0].off-8:]) != classANY || binary.BigEndian.Uint16(ms[0][rrs[1].off-8:]) != classNONE {
		t.Fatal("the delete update's classes are wrong")
	}
	z, rrs, _, names = updateParts(t, ms[1])
	if z != "corp.test" || len(rrs) != 1 || names[0] != "ann-pc.corp.test" || rrs[0].ttl != defaultRegTT || binary.BigEndian.Uint16(ms[1][rrs[0].off-8:]) != classIN {
		t.Fatalf("add update: zone %q names %v", z, names)
	}
	if got := ms[1][rrs[0].off : rrs[0].off+4]; got[0] != 10 || got[3] != 3 {
		t.Fatalf("address %v", got)
	}
	z, rrs, _, names = updateParts(t, ms[2])
	if z != "10.in-addr.arpa" || names[0] != "3.2.1.10.in-addr.arpa" || binary.BigEndian.Uint16(ms[2][rrs[0].off-8:]) != classANY {
		t.Fatalf("PTR delete: zone %q names %v", z, names)
	}
	z, rrs, _, _ = updateParts(t, ms[3])
	if rrs[0].typ != typePTR || binary.BigEndian.Uint16(ms[3][rrs[0].off-8:]) != classIN {
		t.Fatal("PTR add wrong")
	}
	if n, _, _ := readName(ms[3], rrs[0].off); n != "ann-pc.corp.test" {
		t.Fatalf("PTR target %q", n)
	}
}

func TestRegisterHostNameSigned(t *testing.T) {
	pr := newFakePrimary(t)
	old := updPort
	updPort = pr.port
	t.Cleanup(func() { updPort = old })
	res := newFakeResolver(t, regTable(pr.port))
	p := NewPool(testDNSCfg(res.addr))
	p.ProbeNow(context.Background())
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	if _, err := registerHostName(context.Background(), p, "ann-pc.corp.test", "", mustAddr("10.1.2.3"), 120, "dnsdir-key", key); err != nil {
		t.Fatal(err)
	}
	for _, m := range pr.msgs() {
		if _, _, signed, _ := updateParts(t, m); !signed {
			t.Fatal("no TSIG")
		}
	}
	if _, err := registerHostName(context.Background(), p, "ann-pc.corp.test", "", mustAddr("10.1.2.3"), 0, "k", "!!notbase64"); err == nil {
		t.Fatal("a bad key was accepted")
	}
}

func TestRegisterHostNameNoZone(t *testing.T) {
	pr := newFakePrimary(t)
	old := updPort
	updPort = pr.port
	t.Cleanup(func() { updPort = old })
	res := newFakeResolver(t, regTable(pr.port))
	p := NewPool(testDNSCfg(res.addr))
	p.ProbeNow(context.Background())
	lines, err := registerHostName(context.Background(), p, "ann-pc.nowhere.test", "", mustAddr("192.0.2.9"), 0, "", "")
	if err == nil || pr.n() != 2 { // the address fails, the reverse record is still tried
		t.Fatalf("err %v, %d updates, %v", err, pr.n(), lines)
	}
}

// TestWriteSignedUpdate leaves a signed message for an outside check when DNSDIR_TSIG_OUT names a file.
func TestWriteSignedUpdate(t *testing.T) {
	out := os.Getenv("DNSDIR_TSIG_OUT")
	if out == "" {
		t.Skip("DNSDIR_TSIG_OUT not set")
	}
	m := buildUpdate(0x1234, "corp.test", []updRR{{name: "ann-pc.corp.test", typ: typeA, class: classIN, ttl: 300, rdata: []byte{10, 1, 2, 3}}}, "dnsdir-key", []byte("0123456789abcdef0123456789abcdef"), time.Now())
	if err := os.WriteFile(out, m, 0o600); err != nil {
		t.Fatal(err)
	}
}
