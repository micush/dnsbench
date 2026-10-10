package main

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testCfg() GroupConfig {
	g := defaultGroup()
	g.GroupID, g.Priority, g.Weight, g.Preempt, g.Key = 7, 150, 60, true, "secret"
	return g
}

// The golden vectors in testdata must parse, and the Go encoder must
// produce byte-identical packets.
func TestWireCompatV4(t *testing.T) {
	raw := golden(t, "testdata_v4.hex")
	p, err := parsePacket(raw, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != pktHello || p.GroupID != 7 || p.Priority != 150 || p.AfnID != 3 ||
		p.SenderIP != "10.0.0.2" || p.VirtualIP != "10.0.0.1" || p.HelloMS != 333 ||
		p.HoldMS != 999 || p.Weight != 60 || !p.Preempt || p.AF != afIPv4 {
		t.Fatalf("bad parse: %+v", p)
	}
	g := testCfg()
	g.VIP4 = "10.0.0.1/24"
	got, err := buildPacket(&g, pktHello, afIPv4, 3, 0, "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("encoding differs from the golden vector\n go: %x\n vec: %x", got, raw)
	}
}

func TestWireCompatV6(t *testing.T) {
	raw := golden(t, "testdata_v6.hex")
	p, err := parsePacket(raw, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != pktCoup || p.SenderIP != "fe80::1" || p.VirtualIP != "2001:db8::1" || p.AF != afIPv6 {
		t.Fatalf("bad parse: %+v", p)
	}
	g := testCfg()
	g.VIP4, g.VIP6 = "", "2001:db8::1/64"
	got, _ := buildPacket(&g, pktCoup, afIPv6, 0, 0x02, "fe80::1")
	if !bytes.Equal(got, raw) {
		t.Fatalf("encoding differs from the golden vector\n go: %x\n vec: %x", got, raw)
	}
}

func TestWireRejects(t *testing.T) {
	raw := golden(t, "testdata_v4.hex")
	if _, err := parsePacket(raw, []byte("wrong")); err == nil {
		t.Fatal("bad key accepted")
	}
	if _, err := parsePacket(raw[:len(raw)-1], []byte("secret")); err == nil {
		t.Fatal("short packet accepted")
	}
	bad := append([]byte(nil), raw...)
	bad[5] ^= 1
	if _, err := parsePacket(bad, []byte("secret")); err == nil {
		t.Fatal("tampered packet accepted")
	}
}
