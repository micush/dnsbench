package main

import (
	"bytes"
	"net"
	"strings"
	"testing"
)

func TestBuildQueryLayout(t *testing.T) {
	q, err := buildQuery("www.Example.com.", 28, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0, 0, 0x01, 0, 0, 1, 0, 0, 0, 0, 0, 0,
		3, 'w', 'w', 'w', 7, 'E', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0,
		0, 28, 0, 1,
	}
	if !bytes.Equal(q, want) {
		t.Fatalf("got % x\nwant % x", q, want)
	}
	q, _ = buildQuery("example.com", 1, false)
	if q[2] != 0 {
		t.Fatalf("RD set although recursion is off: %x", q[2])
	}
}

func TestBuildQueryRoot(t *testing.T) {
	q, err := buildQuery(".", 2, true)
	if err != nil || len(q) != 12+1+4 || q[12] != 0 {
		t.Fatalf("root query wrong: %v % x", err, q)
	}
}

func TestBuildQueryRejectsBadNames(t *testing.T) {
	for _, name := range []string{"a..b", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 130) + "com"} {
		if _, err := buildQuery(name, 1, true); err == nil {
			t.Errorf("%.20q accepted", name)
		}
	}
}

func TestPTRFromIP(t *testing.T) {
	a, _ := buildQuery("192.0.2.10", 12, true)
	b, _ := buildQuery("10.2.0.192.in-addr.arpa", 12, true)
	if !bytes.Equal(a, b) {
		t.Fatal("IPv4 PTR name wrong")
	}
	if got := arpaName(net.ParseIP("2001:db8::1")); !strings.HasPrefix(got, "1.0.0.0.") || !strings.HasSuffix(got, "8.b.d.0.1.0.0.2.ip6.arpa") {
		t.Fatalf("IPv6 arpa name wrong: %s", got)
	}
	// An IP in a non-PTR query is just a (odd) name, not rewritten.
	c, _ := buildQuery("192.0.2.10", 1, true)
	if bytes.Equal(c[12:], a[12:len(a)-4]) {
		t.Fatal("A query was rewritten")
	}
}

func TestQtypeIDIncludesPTR(t *testing.T) {
	for name, want := range map[string]uint16{"A": 1, "aaaa": 28, "PTR": 12, "ANY": 255, "bogus": 1} {
		if got := qtypeID(name); got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
}

func TestRcodeSlot(t *testing.T) {
	if rcodeSlot([]byte{1, 2, 3}) != rcTooShort {
		t.Fatal("short packet")
	}
	if rcodeSlot([]byte{0, 0, 0x81, 0x83}) != 3 || rcodeLabel(3) != "NXDOMAIN" {
		t.Fatal("NXDOMAIN")
	}
	if rcodeLabel(9) != "UNKNOWN" || rcodeLabel(rcError) != "ERROR" {
		t.Fatal("labels")
	}
}
