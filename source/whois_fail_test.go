package main

import (
	"errors"
	"net"
	"testing"
)

// A timeout or refused connection is not an answer: it is remembered for seconds, not for an hour like "not found".
func TestWhoisNetworkFailureIsNotKeptLong(t *testing.T) {
	w := newWhoisClient()
	_, err := w.cached("a.example", func() (*WhoisInfo, error) { return nil, &whoisNetError{errors.New("i/o timeout")} })
	if err == nil {
		t.Fatal("the failure must be returned")
	}
	if e := w.cache["a.example"]; e.ttl != whoisFailTTL {
		t.Fatalf("a network failure is kept %v, want %v", e.ttl, whoisFailTTL)
	}
	_, _ = w.cached("b.example", func() (*WhoisInfo, error) { return nil, errors.New("no public whois server") })
	if e := w.cache["b.example"]; e.ttl != whoisMissTTL {
		t.Fatalf("a real miss is kept %v, want %v", e.ttl, whoisMissTTL)
	}
}

// When whois.iana.org does not answer, a common TLD goes straight to its registry's server.
func TestWhoisFallsBackWhenIANADoesNotAnswer(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens: every connection is refused
	old := whoisHostAddr
	whoisHostAddr = func(string) string { return addr }
	defer func() { whoisHostAddr = old }()
	w := newWhoisClient()
	if s, err := w.serverFor("org"); err != nil || s != "whois.publicinterestregistry.org" {
		t.Fatalf("org: %q %v", s, err)
	}
	if _, ok := w.tlds["org"]; ok {
		t.Fatal("the fallback must not be remembered as IANA's answer")
	}
	if _, err := w.serverFor("zz"); err == nil {
		t.Fatal("an unknown TLD with IANA down is an error")
	}
}

// A server's IPv4 addresses are tried before its IPv6 ones: an IPv6 path that connects but drops the data must not
// hold the lookup until it times out.
func TestWhoisAddrsIPv4First(t *testing.T) {
	old := whoisHostAddr
	whoisHostAddr = func(string) string { return "localhost:43" }
	defer func() { whoisHostAddr = old }()
	a, err := whoisAddrs("x")
	if err != nil || len(a) == 0 {
		t.Skip("localhost does not resolve here:", err)
	}
	seen6 := false
	for _, x := range a {
		h, _, _ := net.SplitHostPort(x)
		if ip := net.ParseIP(h); ip != nil && ip.To4() == nil {
			seen6 = true
		} else if seen6 {
			t.Fatalf("an IPv4 address after an IPv6 one: %v", a)
		}
	}
}
