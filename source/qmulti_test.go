package main

import (
	"net/netip"
	"testing"
	"time"
)

func TestSeveralPickedClientsAreAddedTogether(t *testing.T) {
	s := NewQStats()
	a, b, c := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2"), netip.MustParseAddr("192.0.2.3")
	for _, x := range []netip.Addr{a, b, c} {
		s.rdns[x.String()] = rdnsEntry{at: time.Now()}
	}
	s.Record(a, "x.example", 1, false, 0)
	s.Record(a, "x.example", 1, false, 0)
	s.Record(b, "y.example", 1, false, 0)
	s.Record(c, "z.example", 1, false, 0)
	f, err := qstatsFilter("", "192.0.2.1, 192.0.2.2,192.0.2.1", "")
	if err != nil || f.Client != "192.0.2.1,192.0.2.2" {
		t.Fatalf("%+v %v", f, err)
	}
	r := s.Query(time.Now().Add(-time.Hour), time.Now().Add(time.Minute), f)
	got := map[string]uint64{}
	for _, x := range r.Domains {
		got[x.Name] = x.Count
	}
	if len(got) != 2 || got["x.example"] != 2 || got["y.example"] != 1 {
		t.Fatalf("domains of the two clients: %v", got)
	}
	f, _ = qstatsFilter("", "", "X.example., z.example")
	r = s.Query(time.Now().Add(-time.Hour), time.Now().Add(time.Minute), f)
	got = map[string]uint64{}
	for _, x := range r.Clients {
		got[x.Name] = x.Count
	}
	if len(got) != 2 || got["192.0.2.1"] != 2 || got["192.0.2.3"] != 1 {
		t.Fatalf("clients of the two domains: %v", got)
	}
	if _, err := qstatsFilter("", "192.0.2.1,not-an-ip", ""); err == nil {
		t.Fatal("a bad name in the list is accepted")
	}
}
