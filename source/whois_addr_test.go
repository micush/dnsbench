package main

import (
	"context"
	"strings"
	"testing"
)

func TestWhoisWithAddrs(t *testing.T) {
	old := addrVia
	defer func() { addrVia = old }()
	addrVia = func(ctx context.Context, name string) (string, string) { return "192.0.2.7", "2001:db8::7" }
	// an internal name has no whois record but still gets its addresses
	w, err := whoisWithAddrs("pbs1.cush.local.")
	if err != nil {
		t.Fatal(err)
	}
	if w.Found || w.Name != "pbs1.cush.local" || w.IPv4 != "192.0.2.7" || w.IPv6 != "2001:db8::7" {
		t.Fatalf("%+v", w)
	}
	txt := w.Text()
	if !strings.HasPrefix(txt, "pbs1.cush.local\nIPv4: 192.0.2.7\nIPv6: 2001:db8::7\n\n") {
		t.Fatalf("text:\n%s", txt)
	}
	// only one family found
	addrVia = func(ctx context.Context, name string) (string, string) { return "192.0.2.7", "" }
	w, _ = whoisWithAddrs("pbs1.cush.local")
	if strings.Contains(w.Text(), "IPv6") && w.IPv6 == "" {
		t.Fatal("IPv6 line without an address")
	}
	if _, err := whoisWithAddrs(" "); err == nil {
		t.Fatal("empty name accepted")
	}
}
