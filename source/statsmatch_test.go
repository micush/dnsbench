package main

import (
	"fmt"
	"net/netip"
	"testing"
	"time"
)

func sumOf(v []uint64) (n uint64) {
	for _, x := range v {
		n += x
	}
	return
}

func listSum(l []NameCount) (n uint64) {
	for _, e := range l {
		n += e.Count
	}
	return
}

// The number cards, the chart series and the lists must tell the same story over a range that is fully
// counted: every card is the sum of its chart series, the kinds add up to the total, every list adds up to
// the total (or, with an answer kind picked, to that kind's card), and the last point of the Clients line
// is the Clients card.
func TestStatsCardsChartAndListsAgree(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s, cur := qsTestStats(t0)
	rcodes := []int{0, 0, 0, 2, 3, 3, 5, 4}
	n := 0
	for m := 0; m < 90; m++ {
		*cur = t0.Add(time.Duration(m) * time.Minute)
		for i := 0; i < 7; i++ {
			c := netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", 1+(m*7+i)%40))
			s.Record(c, fmt.Sprintf("name%d.example", (m+i)%13), 1, i%3 == 0, rcodes[(m+i)%len(rcodes)])
			n++
		}
	}
	*cur = t0.Add(95 * time.Minute)
	for _, hours := range []time.Duration{3, 30} { // the 10-minute tier and a coarser step
		from := t0.Add(-time.Minute)
		to := *cur
		if hours == 30 {
			*cur = t0.Add(26 * time.Hour)
			to = *cur
		}
		r := s.Query(from, to, QFilter{})
		sm := r.Sums
		for name, pair := range map[string][2]uint64{
			"total": {sumOf(r.Total), sm.Total}, "no error": {sumOf(r.NoError), sm.NoError}, "servfail": {sumOf(r.ServFail), sm.ServFail},
			"nxdomain": {sumOf(r.NXDomain), sm.NXDomain}, "refused": {sumOf(r.Refused), sm.Refused}, "other": {sumOf(r.Other), sm.Other},
		} {
			if pair[0] != pair[1] {
				t.Errorf("%dh %s: the chart adds up to %d, the card says %d", hours, name, pair[0], pair[1])
			}
		}
		if sm.Total != uint64(n) {
			t.Errorf("%dh total card %d, recorded %d", hours, sm.Total, n)
		}
		if k := sm.NoError + sm.ServFail + sm.NXDomain + sm.Refused + sm.Other; k != sm.Total {
			t.Errorf("%dh the kinds add up to %d, the total is %d", hours, k, sm.Total)
		}
		for name, l := range map[string][]NameCount{"clients": r.Clients, "domains": r.Domains, "types": r.Types} {
			if got := listSum(l); got != sm.Total {
				t.Errorf("%dh the %s list adds up to %d, the total is %d", hours, name, got, sm.Total)
			}
		}
		if last := r.ClientsN[len(r.ClientsN)-1]; int(last) != sm.Clients {
			t.Errorf("%dh the Clients line ends at %d, the card says %d", hours, last, sm.Clients)
		}
		for kind, want := range map[string]uint64{"noerror": sm.NoError, "servfail": sm.ServFail, "nxdomain": sm.NXDomain, "refused": sm.Refused, "other": sm.Other} {
			k := s.Query(from, to, QFilter{Rcode: kind})
			if got := listSum(k.Clients); got != want {
				t.Errorf("%dh %s: the client list adds up to %d, its card says %d", hours, kind, got, want)
			}
			if got := listSum(k.Domains); got != want {
				t.Errorf("%dh %s: the domain list adds up to %d, its card says %d", hours, kind, got, want)
			}
			if last := k.ClientsN[len(k.ClientsN)-1]; int(last) != k.Sums.Clients {
				t.Errorf("%dh %s: the Clients line ends at %d, the card says %d", hours, kind, last, k.Sums.Clients)
			}
		}
	}
}
