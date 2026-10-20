package main

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// goldenStamp is the timestamp the golden vectors were made with: 2026-10-16 12:00:00 UTC.
var goldenStamp = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC).UnixMilli()

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
	g.HelloMS, g.HoldMS = 333, 999 // what the golden vectors were made with, not the defaults
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
	if p.Stamp != goldenStamp {
		t.Fatalf("bad timestamp %d", p.Stamp)
	}
	if p.Type != pktHello || p.GroupID != 7 || p.Priority != 150 || p.AfnID != 3 ||
		p.SenderIP != "10.0.0.2" || p.VirtualIP != "10.0.0.1" || p.HelloMS != 333 ||
		p.HoldMS != 999 || p.Weight != 60 || !p.Preempt || p.AF != afIPv4 {
		t.Fatalf("bad parse: %+v", p)
	}
	g := testCfg()
	g.VIP4 = "10.0.0.1/24"
	got, err := buildPacketAt(&g, pktHello, afIPv4, 3, 0, "10.0.0.2", goldenStamp)
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
	got, _ := buildPacketAt(&g, pktCoup, afIPv6, 0, 0x02, "fe80::1", goldenStamp)
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

// Protocol version 3 added the timestamp.  The version 2 vectors are kept to show that such a packet is refused
// (a version 2 node and a version 3 node do not talk; see the changelog).
func TestWireProtocol2Refused(t *testing.T) {
	for _, name := range []string{"testdata_proto2_v4.hex", "testdata_proto2_v6.hex"} {
		if _, err := parsePacket(golden(t, name), []byte("secret")); err == nil {
			t.Fatalf("%s: a version 2 packet was accepted", name)
		}
	}
}

// The timestamp is under the signature, and the packets one process builds never repeat or go back.
func TestWireStampSigned(t *testing.T) {
	g := testCfg()
	g.VIP4 = "10.0.0.1/24"
	raw, err := buildPacket(&g, pktHello, afIPv4, 3, 0, "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), raw...)
	bad[50] ^= 1 // inside the timestamp
	if _, err := parsePacket(bad, []byte("secret")); err == nil {
		t.Fatal("a changed timestamp was accepted")
	}
}

func TestWireStampIncreases(t *testing.T) {
	var last atomic.Int64
	fixed := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC).UnixMilli() // a clock that stands still
	prev := advanceStamp(&last, fixed)
	for i := 0; i < 1000; i++ {
		n := advanceStamp(&last, fixed)
		if n <= prev {
			t.Fatalf("stamp %d after %d", n, prev)
		}
		prev = n
	}
	if n := advanceStamp(&last, fixed-3600_000); n <= prev { // a clock that went back
		t.Fatalf("stamp %d after %d when the clock went back", n, prev)
	}
	if n := advanceStamp(&last, fixed+10_000_000); n != fixed+10_000_000 { // a clock that jumped ahead is followed
		t.Fatalf("stamp %d did not follow the clock", n)
	}
}
