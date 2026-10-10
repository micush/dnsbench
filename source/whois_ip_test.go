package main

import (
	"bufio"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

const arinAnswer = `#
# ARIN WHOIS data
NetRange:       8.8.8.0 - 8.8.8.255
CIDR:           8.8.8.0/24
NetName:        GOGL
OriginAS:       AS15169
Organization:   Google LLC (GOGL)

OrgName:        Google LLC
Country:        US
`

const ripeAnswer = `% This is the RIPE Database query service.
inetnum:        193.0.0.0 - 193.0.7.255
netname:        RIPE-NCC
descr:          RIPE Network Coordination Centre
country:        NL

route:          193.0.0.0/21
origin:         AS3333
`

func TestParseIPWhois(t *testing.T) {
	i := parseIPWhois("8.8.8.8", "whois.arin.net", arinAnswer)
	if !i.Found || i.Network != "8.8.8.0/24" || i.NetName != "GOGL" || i.Org != "Google LLC (GOGL)" && i.Org != "Google LLC" || i.Country != "US" || i.Origin != "AS15169" {
		t.Fatalf("%+v", i)
	}
	r := parseIPWhois("193.0.0.1", "whois.ripe.net", ripeAnswer)
	if !r.Found || r.Network != "193.0.0.0 - 193.0.7.255" || r.NetName != "RIPE-NCC" || r.Org != "RIPE Network Coordination Centre" || r.Country != "NL" {
		t.Fatalf("%+v", r)
	}
	if n := parseIPWhois("1.2.3.4", "x", "No match found for 1.2.3.4\n"); n.Found || n.Note == "" {
		t.Fatalf("%+v", n)
	}
	txt := i.Text()
	if !strings.HasPrefix(txt, "8.8.8.8\nNetwork: 8.8.8.0/24\nName: GOGL\n") || !strings.Contains(txt, "Country: US") {
		t.Fatalf("text:\n%s", txt)
	}
}

func TestWhoisLookupIP(t *testing.T) {
	var hits int32
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	old := whoisHostAddr
	whoisHostAddr = func(string) string { return l.Addr().String() }
	t.Cleanup(func() { whoisHostAddr = old; l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				q, _ := bufio.NewReader(c).ReadString('\n')
				q = strings.TrimSpace(q)
				atomic.AddInt32(&hits, 1)
				switch q {
				case "8.8.8.8":
					c.Write([]byte("refer:        whois.arin.net\n"))
				case "n + 8.8.8.8":
					c.Write([]byte(arinAnswer))
				default:
					c.Write([]byte("No match for \"" + q + "\".\n"))
				}
			}(c)
		}
	}()
	w := newWhoisClient()
	i, err := w.LookupIP(netip.MustParseAddr("8.8.8.8"))
	if err != nil || !i.Found || i.Server != "whois.arin.net" || i.NetName != "GOGL" {
		t.Fatalf("%+v %v", i, err)
	}
	n := atomic.LoadInt32(&hits)
	if n != 2 {
		t.Fatalf("expected 2 connections (IANA, ARIN), got %d", n)
	}
	if _, err := w.LookupIP(netip.MustParseAddr("8.8.8.8")); err != nil || atomic.LoadInt32(&hits) != n {
		t.Fatal("second lookup should come from the cache")
	}
	// addresses nobody holds are never asked
	for _, a := range []string{"10.1.2.3", "192.168.5.9", "127.0.0.1", "169.254.1.1", "100.64.0.1", "203.0.113.5", "fd00::1", "fe80::1", "::1", "224.0.0.1"} {
		i, err := w.LookupIP(netip.MustParseAddr(a))
		if err != nil || i.Found || i.Note == "" || i.Kind != "ip" {
			t.Fatalf("%s: %+v %v", a, i, err)
		}
	}
	if atomic.LoadInt32(&hits) != n {
		t.Fatal("a non-public address reached a whois server")
	}
	// through the op wrapper: an address gives its registry data, not a name lookup
	if w2, err := whoisWithAddrs("10.1.2.3"); err != nil || w2.Kind != "ip" || !strings.Contains(w2.Text(), "private") {
		t.Fatalf("%+v %v", w2, err)
	}
}
