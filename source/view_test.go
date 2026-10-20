package main

import "testing"

func TestSortRowsAndPeersOnly(t *testing.T) {
	rows := []SnapshotRow{
		{GroupID: 2, AF: "v4", PeerIP: "10.0.0.9"},
		{GroupID: 1, AF: "v6", PeerIP: "fe80::1", Local: true},
		{GroupID: 1, AF: "v4", PeerIP: "10.0.0.7"},
		{GroupID: 1, AF: "v4", PeerIP: "10.0.0.2", Local: true},
	}
	if got := len(peersOnly(rows)); got != 2 {
		t.Fatalf("peersOnly kept %d rows, want the 2 remote ones", got)
	}
	sortRows(rows)
	var order []string
	for _, r := range rows {
		order = append(order, r.PeerIP)
	}
	want := []string{"10.0.0.2", "10.0.0.7", "fe80::1", "10.0.0.9"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order %v, want %v", order, want)
		}
	}
}
