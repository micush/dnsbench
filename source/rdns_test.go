package main

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReverseName(t *testing.T) {
	for ip, want := range map[string]string{
		"192.168.5.101":    "101.5.168.192.in-addr.arpa",
		"::ffff:10.0.0.7":  "7.0.0.10.in-addr.arpa", // an IPv4 client seen on a dual-stack socket
		"fdf5:168:5::55":   "5.5.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.5.0.0.0.8.6.1.0.5.f.d.f.ip6.arpa",
		"2001:db8::1":      "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa",
		"not an address":   "",
		"192.168.5.101/24": "",
	} {
		got, ok := reverseName(ip)
		if got != want || ok != (want != "") {
			t.Errorf("%s: %q %v, want %q", ip, got, ok, want)
		}
	}
}

// ptrResponse builds a response to a PTR query for name with the given targets; compress makes the owner
// names (and the target's suffix) pointers, as real servers do.
func ptrResponse(q []byte, targets []string, compress bool) []byte {
	r := append([]byte(nil), q...)
	r[2], r[3] = 0x81, 0x80
	binary.BigEndian.PutUint16(r[6:], uint16(len(targets)))
	for _, tg := range targets {
		if compress {
			r = append(r, 0xC0, 0x0C)
		} else {
			r = append(r, r[12:12+qnameLen(q)]...)
		}
		r = append(r, 0, 12, 0, 1, 0, 0, 0, 60)
		var rd []byte
		for _, l := range strings.Split(tg, ".") {
			rd = append(rd, byte(len(l)))
			rd = append(rd, l...)
		}
		rd = append(rd, 0)
		r = binary.BigEndian.AppendUint16(r, uint16(len(rd)))
		r = append(r, rd...)
	}
	return r
}

func qnameLen(q []byte) int {
	i := 12
	for q[i] != 0 {
		i += int(q[i]) + 1
	}
	return i + 1 - 12
}

func TestPtrNames(t *testing.T) {
	q, _ := buildQuery(1, "101.5.168.192.in-addr.arpa", "PTR")
	for _, compress := range []bool{true, false} {
		got := ptrNames(ptrResponse(q, []string{"Printer.Cush.Local", "pc.cush.local"}, compress))
		if len(got) != 2 || got[0] != "printer.cush.local" || got[1] != "pc.cush.local" {
			t.Fatalf("compress=%v: %v", compress, got)
		}
	}
	if n := ptrNames(ptrResponse(q, nil, true)); len(n) != 0 {
		t.Fatalf("no answers: %v", n)
	}
	nx := ptrResponse(q, []string{"x.local"}, true)
	nx[3] = 0x83 // NXDOMAIN
	if n := ptrNames(nx); len(n) != 0 {
		t.Fatalf("NXDOMAIN: %v", n)
	}
	// damaged messages never panic and give nothing wrong
	full := ptrResponse(q, []string{"a.local", "b.local"}, true)
	for i := 0; i < len(full); i++ {
		_ = ptrNames(full[:i])
	}
	bad := append([]byte(nil), full...)
	bad[len(bad)-1] = 0xC0 // a pointer that runs off the end
	_ = ptrNames(bad)
	if ptrNames(nil) != nil || ptrNames([]byte{1, 2, 3}) != nil {
		t.Fatal("garbage gave names")
	}
}

// ptrDNS is a UDP server that answers PTR queries from a table and counts them.
func ptrDNS(t *testing.T, table map[string]string) (addr string, hits *atomic.Int64) {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hits = new(atomic.Int64)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := append([]byte(nil), buf[:n]...)
			hits.Add(1)
			name, _, _ := readName(q, 12)
			var r []byte
			if tg, ok := table[name]; ok && len(q) > 12+qnameLen(q)+1 && q[12+qnameLen(q)+1] == 12 {
				r = ptrResponse(q, []string{tg}, true)
			} else {
				r = reply(q, 0, 1) // an A answer to everything else, as the other fakes do (probes)
			}
			pc.WriteTo(r, from)
		}
	}()
	t.Cleanup(func() { pc.Close() })
	return pc.LocalAddr().String(), hits
}

func TestPoolLookupPTR(t *testing.T) {
	addr, _ := ptrDNS(t, map[string]string{"101.5.168.192.in-addr.arpa": "printer.cush.local"})
	cfg := testDNSCfg(addr)
	p := NewPool(cfg)
	p.ProbeNow(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := p.lookupPTR(ctx, "192.168.5.101")
	if err != nil || len(got) != 1 || got[0] != "printer.cush.local" {
		t.Fatalf("lookup: %v %v", got, err)
	}
	if got, err := p.lookupPTR(ctx, "192.168.5.102"); err != nil || len(got) != 0 {
		t.Fatalf("an address with no record: %v %v", got, err)
	}
	if _, err := p.lookupPTR(ctx, "nonsense"); err == nil {
		t.Fatal("a non-address was looked up")
	}
	// the daemon's own lookups are not client queries
	if p.Queries.Load() != 0 || p.Answered.Load() != 0 {
		t.Fatalf("reverse lookups were counted as client queries: %d / %d", p.Queries.Load(), p.Answered.Load())
	}
}

func TestQStatsLookupUsesPoolsFirstThenRetriesMisses(t *testing.T) {
	s := NewQStats()
	var asked atomic.Int64
	answer := atomic.Pointer[[]string]{}
	names := []string{"printer.cush.local."}
	answer.Store(&names)
	s.ptrVia = func(ctx context.Context, ip string) []string {
		asked.Add(1)
		return *answer.Load()
	}
	wait := func() {
		for i := 0; i < 200; i++ {
			s.rmu.Lock()
			n := len(s.rwork)
			s.rmu.Unlock()
			if n == 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("lookup did not finish")
	}
	cl := []NameCount{{Name: "192.168.5.101"}}
	s.fillHosts(cl)
	wait()
	cl = []NameCount{{Name: "192.168.5.101"}}
	s.fillHosts(cl)
	if cl[0].Host != "printer.cush.local" || asked.Load() != 1 {
		t.Fatalf("name %q after %d lookups", cl[0].Host, asked.Load())
	}
	// a client with no name is asked again after the short retry time, not after the long one
	empty := []string{}
	answer.Store(&empty)
	s.rmu.Lock()
	s.rdns["192.168.5.102"] = rdnsEntry{at: time.Now().Add(-qsRDNSNegTTL - time.Second)}
	s.rdns["192.168.5.103"] = rdnsEntry{at: time.Now().Add(-time.Second)}
	s.rmu.Unlock()
	asked.Store(0)
	s.fillHosts([]NameCount{{Name: "192.168.5.102"}, {Name: "192.168.5.103"}})
	wait()
	if asked.Load() != 1 {
		t.Fatalf("only the old miss should be retried, %d lookups", asked.Load())
	}
	// a name that was found keeps its long lifetime
	s.rmu.Lock()
	s.rdns["192.168.5.104"] = rdnsEntry{names: []string{"x"}, at: time.Now().Add(-qsRDNSNegTTL - time.Second)}
	s.rmu.Unlock()
	asked.Store(0)
	s.fillHosts([]NameCount{{Name: "192.168.5.104"}})
	if asked.Load() != 0 {
		t.Fatal("a known name was looked up again early")
	}
}
