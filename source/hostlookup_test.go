package main

import "testing"

func TestLookupHostParsing(t *testing.T) {
	for in, want := range map[string]string{
		"8.8.8.8": "8.8.8.8", "8.8.8.8:5353": "8.8.8.8", "[::1]:53": "::1", "[::1]": "::1", "2001:db8::1": "2001:db8::1",
		"dns.lan": "dns.lan", "dns.lan:53": "dns.lan", " dns.lan ": "dns.lan", "tls://dns.lan": "", "https://dns.lan/dns-query": "",
	} {
		if got := lookupHost(in); got != want {
			t.Errorf("lookupHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// localhost is in every hosts file: it goes both ways without any network.
func TestHostLookupBothWays(t *testing.T) {
	r, err := hostLookup("localhost")
	if err != nil || !r.Found || r.Kind != "address" || (r.Addr != "127.0.0.1" && r.Addr != "::1") {
		t.Fatalf("name to address: %+v %v", r, err)
	}
	if r.Addr != "127.0.0.1" {
		t.Errorf("IPv4 should be preferred, got %s", r.Addr)
	}
	r, err = hostLookup("127.0.0.1:53")
	if err != nil || !r.Found || r.Kind != "name" || r.Name == "" {
		t.Fatalf("address to name: %+v %v", r, err)
	}
	r, _ = hostLookup("no-such-host.invalid")
	if r.Found || r.Error == "" {
		t.Fatalf("an unknown name must say so: %+v", r)
	}
	if _, err := hostLookup(""); err == nil {
		t.Fatal("empty query must be refused")
	}
	if _, err := hostLookup("tls://dns.example"); err == nil {
		t.Fatal("a tls:// server has nothing to look up")
	}
}
