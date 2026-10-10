package main

import (
	"net"
	"net/netip"
	"testing"
)

func TestDropAskerNeverSendsAQueryBackToItsAsker(t *testing.T) {
	a, b, c := &Server{Addr: "10.20.0.196:53"}, &Server{Addr: "10.20.0.201:53"}, &Server{Addr: "tls://dns.example"}
	ranked := []*Server{a, b, c}
	got := dropAsker(ranked, netip.MustParseAddr("10.20.0.196"))
	if len(got) != 2 || got[0] != b || got[1] != c {
		t.Fatalf("asker not dropped: %v", got)
	}
	if len(ranked) != 3 || ranked[0] != a {
		t.Fatal("the candidate list was modified")
	}
	if got := dropAsker(ranked, netip.MustParseAddr("::ffff:10.20.0.201")); len(got) != 2 || got[0] != a {
		t.Fatalf("mapped address: %v", got)
	}
	if got := dropAsker(ranked, netip.MustParseAddr("10.1.1.1")); len(got) != 3 {
		t.Fatal("an ordinary client lost a server")
	}
	if got := dropAsker(ranked, netip.Addr{}); len(got) != 3 {
		t.Fatal("no client lost a server")
	}
}

func TestDropAskerIgnoresThisMachine(t *testing.T) {
	old := hostLoad
	defer func() { hostLoad = old; hostSet = nil }()
	hostSet = nil
	hostLoad = func() []net.Addr {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("10.20.0.199"), Mask: net.CIDRMask(28, 32)}}
	}
	local, remote := &Server{Addr: "10.20.0.199:53"}, &Server{Addr: "127.0.0.1:53"}
	ranked := []*Server{local, remote}
	if got := dropAsker(ranked, netip.MustParseAddr("10.20.0.199")); len(got) != 2 {
		t.Fatal("a query from this machine's own address was taken for a server forwarding")
	}
	if got := dropAsker(ranked, netip.MustParseAddr("127.0.0.1")); len(got) != 2 {
		t.Fatal("loopback was taken for a server forwarding")
	}
}
