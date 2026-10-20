package main

import (
	"bufio"
	"net"
	"strings"
	"sync/atomic"
	"testing"
)

const whoisVerisign = "   Domain Name: EXAMPLE.COM\r\n   Registry Domain ID: 2336799_DOMAIN_COM-VRSN\r\n   Registrar WHOIS Server: whois.iana.org\r\n" +
	"   Registrar: RESERVED-Internet Assigned Numbers Authority\r\n   Updated Date: 2026-01-16T18:26:12Z\r\n   Creation Date: 1995-08-14T04:00:00Z\r\n" +
	"   Registry Expiry Date: 2026-08-13T04:00:00Z\r\n   Name Server: A.IANA-SERVERS.NET\r\n   Name Server: B.IANA-SERVERS.NET\r\n" +
	">>> Last update of whois database: 2026-10-01T00:00:00Z <<<\r\n"

const whoisNominet = "\r\n    Domain name:\r\n        example.co.uk\r\n\r\n    Registrant:\r\n        Example Holdings Ltd\r\n\r\n" +
	"    Registrar:\r\n        Some Registrar Ltd [Tag = SOME]\r\n\r\n    Relevant dates:\r\n        Registered on: 05-Mar-2001\r\n" +
	"        Expiry date:  05-Mar-2027\r\n        Last updated:  01-Feb-2026\r\n\r\n    Name servers:\r\n        ns1.example.net      1.2.3.4\r\n        ns2.example.net\r\n\r\n"

func TestParseWhois(t *testing.T) {
	i := parseWhois("example.com", "whois.verisign-grs.com", whoisVerisign)
	if !i.Found || i.Registrar != "RESERVED-Internet Assigned Numbers Authority" || i.Registered != "1995-08-14" || i.Expires != "2026-08-13" || i.Updated != "2026-01-16" ||
		len(i.NameServers) != 2 || i.NameServers[0] != "a.iana-servers.net" {
		t.Fatalf("verisign: %+v", i)
	}
	i = parseWhois("example.co.uk", "whois.nic.uk", whoisNominet)
	if !i.Found || i.Registrant != "Example Holdings Ltd" || i.Registrar != "Some Registrar Ltd" || i.Registered != "05-Mar-2001" || i.Expires != "05-Mar-2027" ||
		len(i.NameServers) != 2 || i.NameServers[0] != "ns1.example.net" {
		t.Fatalf("nominet: %+v", i)
	}
	// several matches: only the exact domain
	multi := "   Domain Name: EXAMPLE.COM.AU\n   Registrar: Wrong\n\n   Domain Name: EXAMPLE.COM\n   Registrar: Right\n\n   Domain Name: EXAMPLE.COMPUTER\n   Registrar: Other\n"
	if i := parseWhois("example.com", "x", multi); i.Registrar != "Right" {
		t.Fatalf("multi: %+v", i)
	}
	// redacted registrant is dropped
	if i := parseWhois("a.com", "x", "Registrar: R\nRegistrant Organization: REDACTED FOR PRIVACY\n"); i.Registrant != "" || !i.Found {
		t.Fatalf("redacted: %+v", i)
	}
	i = parseWhois("nope.com", "x", "No match for domain \"NOPE.COM\".\n")
	if i.Found || !strings.Contains(i.Note, "no registration") || !strings.Contains(i.Text(), "nope.com") {
		t.Fatalf("no match: %+v", i)
	}
	if txt := parseWhois("example.com", "whois.verisign-grs.com", whoisVerisign).Text(); !strings.Contains(txt, "Registrar: RESERVED") || !strings.Contains(txt, "Expires: 2026-08-13") {
		t.Fatalf("text: %s", txt)
	}
}

func TestWhoisName(t *testing.T) {
	ok := map[string]string{"www.Example.com.": "example.com", "a.b.example.org": "example.org", "shop.example.co.uk": "example.co.uk", "example.co.uk": "example.co.uk"}
	for in, want := range ok {
		if got, _, err := whoisName(in); err != nil || got != want {
			t.Errorf("%q → %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "(others)", "localhost", "printer.lan", "192.0.2.1", "1.2.0.192.in-addr.arpa", "co.uk", "host.home.arpa", "a b.com", "x.local"} {
		if _, _, err := whoisName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestWhoisLookupCachesAndRefers(t *testing.T) {
	// the "#host" suffix is not dialable: strip it in a wrapper dialer by mapping hosts to one listener
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
				case "com":
					c.Write([]byte("refer:        whois.verisign-grs.com\n"))
				case "zzz":
					c.Write([]byte("% IANA WHOIS server\n% no such TLD\n"))
				case "domain example.com":
					c.Write([]byte(whoisVerisign))
				default:
					c.Write([]byte("No match for \"" + q + "\".\n"))
				}
			}(c)
		}
	}()
	w := newWhoisClient()
	i, err := w.Lookup("www.example.com")
	if err != nil || !i.Found || i.Registrar == "" || i.Server != "whois.verisign-grs.com" {
		t.Fatalf("%+v %v", i, err)
	}
	n := atomic.LoadInt32(&hits)
	if n != 2 { // IANA, then the registry
		t.Fatalf("expected 2 connections, got %d", n)
	}
	if _, err := w.Lookup("example.com"); err != nil || atomic.LoadInt32(&hits) != n {
		t.Fatalf("second lookup should come from the cache (%d)", atomic.LoadInt32(&hits))
	}
	// a name the registry has not heard of is cached too
	if i, err := w.Lookup("nothere.com"); err != nil || i.Found {
		t.Fatalf("%+v %v", i, err)
	}
	m := atomic.LoadInt32(&hits)
	if _, _ = w.Lookup("nothere.com"); atomic.LoadInt32(&hits) != m {
		t.Fatal("miss not cached")
	}
	if _, err := w.Lookup("host.zzz"); err == nil || !strings.Contains(err.Error(), "no public whois") {
		t.Fatalf("tld without server: %v", err)
	}
	if _, err := w.Lookup("printer.lan"); err == nil {
		t.Fatal("private suffix must not be looked up")
	}
}

func TestWhoisUnreachable(t *testing.T) {
	old := whoisHostAddr
	whoisHostAddr = func(string) string { return "127.0.0.1:1" } // nothing listens
	t.Cleanup(func() { whoisHostAddr = old })
	if _, err := newWhoisClient().Lookup("example.com"); err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("%v", err)
	}
}
