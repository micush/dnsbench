package main

import (
	"path/filepath"
	"testing"
)

func TestValidHostPort(t *testing.T) {
	good := []string{"10.0.0.5:53854", "[2001:db8::1]:53854", "node-1.example.com:53854", "ns1:1"}
	bad := []string{"", "10.0.0.5", "10.0.0.5:0", "10.0.0.5:70000", "10.0.0.5:x", "evil@10.0.0.5:53854",
		"10.0.0.5/x:53854", "a b:1", "host?x:1", "host#x:1", "[fe80::1%eth0]:1", ":53854", "ho/st:1"}
	for _, a := range good {
		if !validHostPort(a) {
			t.Errorf("%q should be accepted", a)
		}
	}
	for _, a := range bad {
		if validHostPort(a) {
			t.Errorf("%q should be refused", a)
		}
	}
}

func TestVersionPathStaysInside(t *testing.T) {
	s := &VersionStore{dir: "/var/lib/ddgw/versions"}
	if got := s.path("1790827200558"); got != "/var/lib/ddgw/versions/1790827200558.json" {
		t.Fatalf("path: %s", got)
	}
	// Whatever text comes in, the file is inside the directory.
	for _, id := range []string{"../../etc/passwd", "/etc/passwd", "..", "", "1/../2", "007"} {
		if got := s.path(id); filepath.Dir(got) != s.dir {
			t.Errorf("path(%q) escaped: %s", id, got)
		}
		if validVersionID(id) {
			t.Errorf("%q should not be a valid version id", id)
		}
	}
}
