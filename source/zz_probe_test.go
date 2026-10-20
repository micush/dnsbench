package main

import (
	"net/netip"
	"testing"
)

func TestProbeRewriteGlob(t *testing.T) {
	rows, err := normalizePolicy([]PolicyRule{{Client: "*", Name: "ntp*.sopragroup.com", Dest: "time.axway.int"}})
	if err != nil {
		t.Fatal(err)
	}
	r := buildPolicy(rows)
	c := netip.MustParseAddr("10.1.1.1")
	for _, n := range []string{"ntp1.sopragroup.com", "ntp.sopragroup.com", "ntpserver.sopragroup.com", "NTP2.sopragroup.com", "ntp.a.sopragroup.com", "time.sopragroup.com", "sopragroup.com"} {
		t.Logf("%-28s match=%v rename=%v dest=%v", n, r[0].matches(c, n), r[0].rename, r[0].destLit)
	}
}
