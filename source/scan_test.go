package main

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanAddr(t *testing.T) {
	for _, ok := range []string{"192.168.5.95", " 10.0.0.1 ", "2001:db8::1", "::ffff:10.1.2.3"} {
		if _, err := scanAddr(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-oN /tmp/x", "10.0.0.1; id", "10.0.0.0/24", "host.example.com", "0.0.0.0", "224.0.0.1", "255.255.255.255", "fe80::1%eth0", "10.0.0.1 --script"} {
		if _, err := scanAddr(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	a, _ := scanAddr("::ffff:10.1.2.3")
	if a.String() != "10.1.2.3" {
		t.Errorf("a mapped address is not unmapped: %s", a)
	}
}

func TestScanArgs(t *testing.T) {
	a4 := scanArgs(netip.MustParseAddr("10.1.2.3"))
	if a4[len(a4)-2] != "--" || a4[len(a4)-1] != "10.1.2.3" {
		t.Errorf("the address must come last, after --: %v", a4)
	}
	for _, x := range a4 {
		if x == "-6" {
			t.Error("-6 for an IPv4 address")
		}
	}
	a6 := scanArgs(netip.MustParseAddr("2001:db8::1"))
	has6 := false
	for _, x := range a6 {
		if x == "-6" {
			has6 = true
		}
	}
	if !has6 {
		t.Errorf("no -6 for an IPv6 address: %v", a6)
	}
}

func TestScanTidy(t *testing.T) {
	in := "Starting Nmap 7.94 ( https://nmap.org ) at 2026-10-10 04:00 MST\nNmap scan report for host.lan (10.1.2.3)\nHost is up (0.0010s latency).\nNot shown: 97 closed tcp ports (reset)\n" +
		"PORT     STATE    SERVICE VERSION\n22/tcp   open     ssh     OpenSSH 8.9p1 Ubuntu 3ubuntu0.6 (Ubuntu Linux; protocol 2.0)\n80/tcp   open     http    nginx 1.18.0\n" +
		"139/tcp  filtered netbios-ssn\n53/udp   open|filtered domain\nMAC Address: AA:BB:CC:DD:EE:FF (Acme Devices)\nDevice type: general purpose\nRunning: Linux 5.X\n" +
		"OS CPE: cpe:/o:linux:linux_kernel:5\nOS details: Linux 5.4 - 5.10\nNetwork Distance: 1 hop\nService Info: OS: Linux; CPE: cpe:/o:linux:linux_kernel\n\n" +
		"OS and Service detection performed. Please report any incorrect results\nNmap done: 1 IP address (1 host up) scanned in 9.1 seconds\n"
	want := "Up, 0.0010s latency\nOS: Linux 5.4 - 5.10\nMAC: AA:BB:CC:DD:EE:FF (Acme Devices)\nOpen ports:\n  22/tcp  ssh  OpenSSH 8.9p1 Ubuntu 3ubuntu0.6 (Ubuntu Linux; protocol 2.0)\n  80/tcp  http  nginx 1.18.0"
	if got := scanTidy(in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got := scanTidy("Nmap scan report for 10.1.2.4\nHost is up (0.01s latency).\nAll 100 scanned ports are closed\n"); got != "Up, 0.01s latency\nNo open ports found among the 100 commonest" {
		t.Errorf("no ports: %q", got)
	}
	if got := scanTidy("Note: Host seems down. If it is really up, but blocking our ping probes, try -Pn\n"); !strings.HasPrefix(got, "Host seems down") {
		t.Errorf("down: %q", got)
	}
	var many strings.Builder
	many.WriteString("Host is up (0.01s latency).\n")
	for i := 1; i <= 15; i++ {
		fmt.Fprintf(&many, "%d/tcp open svc%d\n", i, i)
	}
	if got := scanTidy(many.String()); !strings.HasSuffix(got, "… and 3 more") {
		t.Errorf("many: %q", got)
	}
	if got := scanTidy("Something else entirely\n"); got != "Something else entirely" {
		t.Errorf("unknown output: %q", got)
	}
}

// a fake nmap that prints its arguments, found through PATH
func TestScanRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nmap"), []byte("#!/bin/sh\necho \"Nmap scan report for $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if _, err := scanStart("not an address", "t"); err == nil {
		t.Fatal("a bad address started a scan")
	}
	j, err := scanStart("10.9.9.9", "tester")
	if err != nil || j.State != "running" {
		t.Fatalf("start: %v %+v", err, j)
	}
	var g *ScanJob
	for i := 0; i < 100; i++ {
		g, _ = scanGet("10.9.9.9")
		if g.State != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if g.State != "done" || !strings.Contains(g.Text, "-Pn") || !strings.HasSuffix(g.Text, "-- 10.9.9.9") {
		t.Fatalf("result %+v", g)
	}
	if n, _ := scanGet("10.8.8.8"); n.State != "none" {
		t.Errorf("never scanned: %+v", n)
	}
}

func TestScanWithoutNmap(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := scanStart("10.9.9.8", "t"); err == nil || !strings.Contains(err.Error(), "nmap is not installed") {
		t.Fatalf("got %v", err)
	}
}

func TestQStatsClear(t *testing.T) {
	s := NewQStats()
	for i := 0; i < 50; i++ {
		s.Record(netip.MustParseAddr("10.1.1.1"), "example.com", 1, false, 0)
	}
	now := time.Now()
	if r := s.Query(now.Add(-time.Hour), now.Add(time.Minute), QFilter{}); r.Sums.Total != 50 {
		t.Fatalf("before: %d", r.Sums.Total)
	}
	s.Clear()
	r := s.Query(now.Add(-time.Hour), now.Add(time.Minute), QFilter{})
	if r.Sums.Total != 0 || len(r.Clients) != 0 || len(r.Domains) != 0 {
		t.Fatalf("after: %+v", r.Sums)
	}
	s.Record(netip.MustParseAddr("10.1.1.2"), "example.org", 1, false, 0)
	if r := s.Query(now.Add(-time.Hour), now.Add(time.Minute), QFilter{}); r.Sums.Total != 1 {
		t.Fatalf("counting again: %d", r.Sums.Total)
	}
}
