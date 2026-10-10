package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ── frames to test with ──────────────────────────────────────────────────────

var (
	macA = []byte{0x00, 0x1a, 0x7c, 0x01, 0x02, 0x00}
	macB = []byte{0x02, 0x00, 0x00, 0xaa, 0xbb, 0xcc}
)

func eth(dst, src []byte, etype uint16, payload []byte) []byte {
	b := append(append(append([]byte{}, dst...), src...), byte(etype>>8), byte(etype))
	return append(b, payload...)
}

func ip4(src, dst string, proto byte, l4 []byte) []byte {
	h := make([]byte, 20)
	h[0] = 0x45
	binary.BigEndian.PutUint16(h[2:], uint16(20+len(l4)))
	h[8], h[9] = 64, proto
	copy(h[12:], net.ParseIP(src).To4())
	copy(h[16:], net.ParseIP(dst).To4())
	return append(h, l4...)
}

func ip6(src, dst string, next byte, l4 []byte) []byte {
	h := make([]byte, 40)
	h[0] = 0x60
	binary.BigEndian.PutUint16(h[4:], uint16(len(l4)))
	h[6], h[7] = next, 64
	copy(h[8:], net.ParseIP(src).To16())
	copy(h[24:], net.ParseIP(dst).To16())
	return append(h, l4...)
}

func udp(sport, dport uint16, payload []byte) []byte {
	h := make([]byte, 8)
	binary.BigEndian.PutUint16(h[0:], sport)
	binary.BigEndian.PutUint16(h[2:], dport)
	binary.BigEndian.PutUint16(h[4:], uint16(8+len(payload)))
	return append(h, payload...)
}

func tcp(sport, dport uint16, flags byte, payload []byte) []byte {
	h := make([]byte, 20)
	binary.BigEndian.PutUint16(h[0:], sport)
	binary.BigEndian.PutUint16(h[2:], dport)
	h[12], h[13] = 5<<4, flags
	return append(h, payload...)
}

func arpPkt(op uint16, sha []byte, spa string, tha []byte, tpa string) []byte {
	p := []byte{0, 1, 8, 0, 6, 4, byte(op >> 8), byte(op)}
	p = append(p, sha...)
	p = append(p, net.ParseIP(spa).To4()...)
	p = append(p, tha...)
	return append(p, net.ParseIP(tpa).To4()...)
}

func dnsQuery(name string, qt uint16) []byte { return cacheQuery(name, qt, nil) }

func dnsReply(q []byte, rcode byte, an int) []byte {
	r := append([]byte(nil), q...)
	r[2] |= 0x80
	r[3] = r[3]&0xf0 | rcode
	binary.BigEndian.PutUint16(r[6:], uint16(an))
	return r
}

func TestCapSummaries(t *testing.T) {
	q := dnsQuery("Example.COM", 1)
	for name, c := range map[string]struct {
		frame []byte
		want  string
	}{
		"dns query":       {eth(macA, macB, 0x0800, ip4("192.0.2.1", "10.20.0.205", 17, udp(53124, 53, q))), "UDP 192.0.2.1.53124 > 10.20.0.205.53, length"},
		"dns query text":  {eth(macA, macB, 0x0800, ip4("192.0.2.1", "10.20.0.205", 17, udp(53124, 53, q))), "DNS A? example.com"},
		"dns reply":       {eth(macB, macA, 0x0800, ip4("10.20.0.205", "192.0.2.1", 17, udp(53, 53124, dnsReply(q, 3, 0)))), "DNS NXDOMAIN 0 ans A example.com"},
		"dns reply ok":    {eth(macB, macA, 0x0800, ip4("10.20.0.205", "192.0.2.1", 17, udp(53, 53124, dnsReply(q, 0, 2)))), "DNS NOERROR 2 ans"},
		"dns over tcp":    {eth(macA, macB, 0x0800, ip4("192.0.2.1", "10.20.0.205", 6, tcp(40000, 53, 0x18, append([]byte{0, byte(len(q))}, q...)))), "DNS A? example.com"},
		"tcp syn":         {eth(macA, macB, 0x0800, ip4("192.0.2.1", "10.0.0.2", 6, tcp(1234, 443, 0x02, nil))), "TCP 192.0.2.1.1234 > 10.0.0.2.443 [S]"},
		"tcp synack":      {eth(macA, macB, 0x0800, ip4("192.0.2.1", "10.0.0.2", 6, tcp(1234, 443, 0x12, nil))), "[S.]"},
		"icmp echo":       {eth(macA, macB, 0x0800, ip4("192.0.2.1", "10.0.0.2", 1, []byte{8, 0, 0, 0})), "ICMP 192.0.2.1 > 10.0.0.2: echo request"},
		"arp request":     {eth([]byte{255, 255, 255, 255, 255, 255}, macB, 0x0806, arpPkt(1, macB, "10.20.0.1", make([]byte, 6), "10.20.0.205")), "ARP, who-has 10.20.0.205 tell 10.20.0.1 (eth 02:00:00:aa:bb:cc > ff:ff:ff:ff:ff:ff)"},
		"arp reply":       {eth(macB, []byte{0x02, 9, 9, 9, 9, 9}, 0x0806, arpPkt(2, macA, "10.20.0.205", macB, "10.20.0.1")), "ARP, 10.20.0.205 is-at 00:1a:7c:01:02:00 (eth 02:09:09:09:09:09 > 02:00:00:aa:bb:cc)"},
		"ipv6 udp":        {eth(macA, macB, 0x86dd, ip6("2001:db8::1", "2001:db8::2", 17, udp(1000, 53, q))), "UDP 2001:db8::1.1000 > 2001:db8::2.53"},
		"vlan":            {eth(macA, macB, 0x8100, append([]byte{0, 5, 8, 0}, ip4("192.0.2.1", "10.0.0.2", 17, udp(1, 2, nil))...)), "UDP 192.0.2.1.1 > 10.0.0.2.2"},
		"other ethertype": {eth(macA, macB, 0x88cc, []byte{1, 2, 3}), "ethertype 0x88cc"},
		"short":           {[]byte{1, 2, 3}, "short frame (3 bytes)"},
	} {
		if got := summarizePacket(linktypeEthernet, c.frame); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q does not contain %q", name, got, c.want)
		}
	}
	// neighbour discovery, with the target's MAC
	ns := append([]byte{135, 0, 0, 0, 0, 0, 0, 0}, net.ParseIP("2001:db8::205").To16()...)
	if got := summarizePacket(linktypeEthernet, eth(macA, macB, 0x86dd, ip6("fe80::1", "ff02::1:ff00:205", 58, ns))); !strings.Contains(got, "neighbor solicitation, who has 2001:db8::205 (eth 02:00:00:aa:bb:cc > 00:1a:7c:01:02:00)") {
		t.Errorf("NS: %q", got)
	}
	na := append(append([]byte{136, 0, 0, 0, 0x60, 0, 0, 0}, net.ParseIP("2001:db8::205").To16()...), 2, 1, 0x00, 0x1a, 0x7c, 0x01, 0x02, 0x00)
	if got := summarizePacket(linktypeEthernet, eth(macB, macA, 0x86dd, ip6("2001:db8::205", "fe80::1", 58, na))); !strings.Contains(got, "neighbor advertisement, 2001:db8::205 is at 00:1a:7c:01:02:00") {
		t.Errorf("NA: %q", got)
	}
	// a bare IP packet (an interface without a hardware address)
	if got := summarizePacket(linktypeRaw, ip4("192.0.2.1", "10.0.0.2", 17, udp(5, 6, nil))); !strings.Contains(got, "UDP 192.0.2.1.5 > 10.0.0.2.6") {
		t.Errorf("raw: %q", got)
	}
}

func TestCapFilter(t *testing.T) {
	q := dnsQuery("example.com", 1)
	dq := eth(macA, macB, 0x0800, ip4("192.0.2.1", "10.20.0.205", 17, udp(53124, 53, q))) // client > VIP, DNS
	dr := eth(macB, macA, 0x0800, ip4("10.20.0.205", "192.0.2.1", 17, udp(53, 53124, q))) // VIP > client
	web := eth(macA, macB, 0x0800, ip4("192.0.2.9", "10.0.0.2", 6, tcp(40000, 443, 0x02, nil)))
	v6 := eth(macA, macB, 0x86dd, ip6("2001:db8::1", "2001:db8::2", 6, tcp(40000, 53, 0x02, nil)))
	arpq := eth([]byte{255, 255, 255, 255, 255, 255}, macB, 0x0806, arpPkt(1, macB, "10.20.0.1", make([]byte, 6), "10.20.0.205"))
	ping := eth(macA, macB, 0x0800, ip4("192.0.2.1", "10.0.0.2", 1, []byte{8, 0, 0, 0}))
	frames := map[string][]byte{"dq": dq, "dr": dr, "web": web, "v6": v6, "arp": arpq, "ping": ping}
	for filter, want := range map[string]string{
		"":                                     "dq dr web v6 arp ping",
		"host 10.20.0.205":                     "dq dr arp", // the ARP's target address counts, as in tcpdump
		"src host 10.20.0.205":                 "dr",
		"dst host 10.20.0.205":                 "dq arp",
		"net 192.0.2.0/24":                     "dq dr web ping",
		"src net 192.0.2.0/24 and not port 53": "web ping",
		"port 53":                              "dq dr v6",
		"dst port 53":                          "dq v6",
		"src port 53":                          "dr",
		"portrange 50-60":                      "dq dr v6",
		"dns":                                  "dq dr v6",
		"udp":                                  "dq dr",
		"tcp":                                  "web v6",
		"tcp port 53":                          "v6",
		"udp port 53 and host 192.0.2.1":       "dq dr",
		"ip6":                                  "v6",
		"ip6 host 2001:db8::1":                 "v6",
		"ip host 2001:db8::1":                  "",
		"arp":                                  "arp",
		"icmp":                                 "ping",
		"host 10.20.0.205 and not arp":         "dq dr",
		"(tcp or icmp) and host 192.0.2.9":     "web",
		"tcp || icmp":                          "web v6 ping",
		"! udp && ! arp":                       "web v6 ping",
		"ether host 00:1a:7c:01:02:00":         "dq dr web v6 ping",
		"ether src 00:1a:7c:01:02:00":          "dr",
		"ether dst 02:00:00:aa:bb:cc":          "dr",
		"UDP PORT DOMAIN":                      "dq dr",
		"port https":                           "web",
	} {
		f, err := compileCapFilter(filter)
		if err != nil {
			t.Errorf("%q: %v", filter, err)
			continue
		}
		var got []string
		for _, n := range []string{"dq", "dr", "web", "v6", "arp", "ping"} {
			if f.match(linktypeEthernet, frames[n]) {
				got = append(got, n)
			}
		}
		if strings.Join(got, " ") != want {
			t.Errorf("%q matches %q, want %q", filter, strings.Join(got, " "), want)
		}
	}
	for _, bad := range []string{"host", "host notanip", "port abc", "port 70000", "net 10.0.0.0", "foo", "(tcp", "tcp)", "tcp and", "not", "ether host zz", "src", "portrange 5", "tcp udp"} {
		if _, err := compileCapFilter(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	// a fragment other than the first carries no ports
	frag := ip4("192.0.2.1", "10.0.0.2", 17, udp(1000, 53, nil))
	binary.BigEndian.PutUint16(frag[6:], 0x00b9) // fragment offset 185
	if f, _ := compileCapFilter("port 53"); f.match(linktypeEthernet, eth(macA, macB, 0x0800, frag)) {
		t.Error("a later fragment matched on a port")
	}
	// a truncated frame never panics
	f, _ := compileCapFilter("tcp port 53 or host 1.2.3.4 or ether host 00:00:00:00:00:00")
	for n := 0; n <= len(v6); n++ {
		f.match(linktypeEthernet, v6[:n])
		summarizePacket(linktypeEthernet, v6[:n])
		summarizePacket(linktypeEthernet, dq[:min(n, len(dq))])
	}
}

func TestCaptureStateRingAndEpoch(t *testing.T) {
	cs := &captureState{maxPkts: 5}
	ep := cs.begin("eth0", "", linktypeEthernet)
	now := time.Now()
	for i := 0; i < 8; i++ {
		cs.add(ep, nil, now, eth(macA, macB, 0x88cc, []byte{byte(i)}))
	}
	st := cs.since(0, 100)
	if st.Count != 5 || len(st.Packets) != 5 || st.Packets[0].Seq != 4 || st.Cursor != 8 || !st.Running || st.Iface != "eth0" {
		t.Fatalf("ring: %+v", st)
	}
	if st := cs.since(6, 100); len(st.Packets) != 2 || st.Packets[0].Seq != 7 {
		t.Fatalf("since a cursor: %+v", st.Packets)
	}
	if st := cs.since(0, 2); len(st.Packets) != 2 || st.Packets[1].Seq != 8 {
		t.Fatalf("the newest max: %+v", st.Packets)
	}
	// a packet from an earlier socket is dropped once a new capture has begun, or this one was stopped
	ep2 := cs.begin("eth1", "", linktypeEthernet)
	cs.add(ep, nil, now, []byte{1})
	if st := cs.since(0, 10); st.Count != 0 || st.Iface != "eth1" {
		t.Fatalf("after a new begin: %+v", st)
	}
	cs.stop()
	cs.add(ep2, nil, now, []byte{1})
	if st := cs.since(0, 10); st.Count != 0 || st.Running {
		t.Fatalf("after stop: %+v", st)
	}
	// the byte cap
	b := &captureState{maxBytes: 100}
	ep = b.begin("x", "", linktypeRaw)
	for i := 0; i < 10; i++ {
		b.add(ep, nil, now, make([]byte, 30))
	}
	if st := b.since(0, 100); st.Bytes > 100 || st.Count != 3 {
		t.Fatalf("byte cap: %d bytes %d packets", st.Bytes, st.Count)
	}
	// the filter counts what it turned away
	f, _ := compileCapFilter("udp")
	c := &captureState{}
	ep = c.begin("x", "udp", linktypeEthernet)
	c.add(ep, f, now, eth(macA, macB, 0x0800, ip4("1.1.1.1", "2.2.2.2", 17, udp(1, 2, nil))))
	c.add(ep, f, now, eth(macA, macB, 0x0800, ip4("1.1.1.1", "2.2.2.2", 6, tcp(1, 2, 2, nil))))
	if st := c.since(0, 10); st.Count != 1 || st.Matched != 1 || st.Seen != 2 {
		t.Fatalf("filter counts: %+v", st)
	}
}

func TestPcapRoundTripAndLimit(t *testing.T) {
	cs := &captureState{}
	ep := cs.begin("eth0", "", linktypeEthernet)
	t0 := time.Unix(1700000000, 123456000)
	var frames [][]byte
	for i := 0; i < 20; i++ {
		f := eth(macA, macB, 0x0800, ip4("192.0.2.1", "10.0.0.2", 17, udp(uint16(1000+i), 53, dnsQuery("x.example", 1))))
		frames = append(frames, f)
		cs.add(ep, nil, t0.Add(time.Duration(i)*time.Millisecond), f)
	}
	var buf bytes.Buffer
	cs.writePcap(&buf, 0)
	lt, pkts, err := readPcap(buf.Bytes())
	if err != nil || lt != linktypeEthernet || len(pkts) != 20 {
		t.Fatalf("round trip: %v %d %d", err, lt, len(pkts))
	}
	for i, p := range pkts {
		if !bytes.Equal(p.data, frames[i]) || !p.t.Equal(t0.Add(time.Duration(i)*time.Millisecond)) || p.origlen != len(frames[i]) {
			t.Fatalf("packet %d differs", i)
		}
	}
	// a limit keeps the newest that fit, and the file is still a whole .pcap
	per := 16 + len(frames[0])
	buf.Reset()
	cs.writePcap(&buf, 24+3*per+5)
	_, pkts, err = readPcap(buf.Bytes())
	if err != nil || len(pkts) != 3 || !bytes.Equal(pkts[2].data, frames[19]) || !bytes.Equal(pkts[0].data, frames[17]) {
		t.Fatalf("limit: %v %d", err, len(pkts))
	}
	if buf.Len() > 24+3*per+5 {
		t.Fatalf("the limit was passed: %d", buf.Len())
	}
	// a file that is not a .pcap, and one cut in the middle of a record
	if _, _, err := readPcap([]byte("nope")); err == nil {
		t.Error("garbage read as a .pcap")
	}
	var full bytes.Buffer
	cs.writePcap(&full, 0)
	if _, p, err := readPcap(full.Bytes()[:full.Len()-10]); err != nil || len(p) != 19 {
		t.Errorf("cut file: %v %d", err, len(p))
	}
	// the header is what tcpdump expects
	h := pcapGlobalHeader(capSnaplen, linktypeEthernet)
	if binary.LittleEndian.Uint32(h[0:]) != 0xa1b2c3d4 || binary.LittleEndian.Uint16(h[4:]) != 2 || binary.LittleEndian.Uint16(h[6:]) != 4 || binary.LittleEndian.Uint32(h[20:]) != 1 {
		t.Errorf("header %x", h)
	}
}

// ── a real capture (needs the socket; skipped where the sandbox does not allow it) ──

func canCapture(t *testing.T) {
	t.Helper()
	cs := &captureState{}
	if err := cs.startOn("lo", ""); err != nil {
		t.Skipf("no raw sockets here: %v", err)
	}
	cs.stop()
}

func sendUDP(t *testing.T, port int, n int) {
	t.Helper()
	c, err := net.Dial("udp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < n; i++ {
		c.Write([]byte("ddgw capture test"))
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCaptureOnTheLoopback(t *testing.T) {
	canCapture(t)
	l, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer l.Close()
	port := l.LocalAddr().(*net.UDPAddr).Port
	l2, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer l2.Close()
	other := l2.LocalAddr().(*net.UDPAddr).Port

	cs := &captureState{}
	if err := cs.startOn("lo", "udp and port "+itoa(port)); err != nil {
		t.Fatal(err)
	}
	sendUDP(t, port, 3)
	sendUDP(t, other, 3) // the filter turns these away
	time.Sleep(300 * time.Millisecond)
	st := cs.since(0, 100)
	cs.stop()
	if st.Count != 3 || st.Seen < 6 {
		t.Fatalf("kept %d, seen %d", st.Count, st.Seen)
	}
	if !strings.Contains(st.Packets[0].Summary, "UDP 127.0.0.1.") || !strings.Contains(st.Packets[0].Summary, "> 127.0.0.1."+itoa(port)) {
		t.Fatalf("summary %q", st.Packets[0].Summary)
	}

	// a timed capture into a private buffer
	go func() { time.Sleep(300 * time.Millisecond); sendUDP(t, port, 4) }()
	pcap, kept, seen, err := captureRun(context.Background(), "lo", "udp and dst port "+itoa(port), 1200*time.Millisecond, capRelayBytes)
	if err != nil || kept != 4 || seen < 4 {
		t.Fatalf("run: %v kept %d seen %d", err, kept, seen)
	}
	if _, pk, err := readPcap(pcap); err != nil || len(pk) != 4 {
		t.Fatalf("the file: %v %d", err, len(pk))
	}
}

func TestCaptureRefusals(t *testing.T) {
	cs := &captureState{}
	if err := cs.startOn("no-such-if0", ""); err == nil || !strings.Contains(err.Error(), "no interface named") {
		t.Errorf("unknown interface: %v", err)
	}
	if err := cs.startOn("lo", "port nine"); err == nil || !strings.Contains(err.Error(), "filter") {
		t.Errorf("bad filter: %v", err)
	}
	if err := cs.startOn("lo", strings.Repeat("tcp or ", 100)); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("long filter: %v", err)
	}
	if cs.since(0, 1).Running {
		t.Error("a refused start left a capture running")
	}
}

// ── the web side ─────────────────────────────────────────────────────────────

func TestCaptureHTTP(t *testing.T) {
	canCapture(t)
	e := newWebEnv(t)
	e.login("alice", "pw")
	r := e.do("GET", "/api/capture/interfaces", nil, withAuth(e, false))
	var ifs struct {
		Data CaptureInterfaces `json:"data"`
	}
	json.Unmarshal(r.raw, &ifs)
	if r.code != 200 || len(ifs.Data.Interfaces) == 0 {
		t.Fatalf("interfaces: %d %s", r.code, r.raw)
	}
	if r := e.do("POST", "/api/capture/start", map[string]string{"iface": "lo", "filter": "port nine"}, withAuth(e, true)); r.code != http.StatusUnprocessableEntity || !strings.Contains(string(r.raw), "filter") {
		t.Fatalf("a bad filter: %d %s", r.code, r.raw)
	}
	if r := e.do("POST", "/api/capture/start", map[string]string{"iface": "lo", "filter": "udp"}, withAuth(e, true)); r.code != 200 {
		t.Fatalf("start: %d %s", r.code, r.raw)
	}
	l, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer l.Close()
	sendUDP(t, l.LocalAddr().(*net.UDPAddr).Port, 3)
	time.Sleep(300 * time.Millisecond)
	r = e.do("GET", "/api/capture/packets?since=0", nil, withAuth(e, false))
	var pk struct {
		Data capStatus `json:"data"`
	}
	json.Unmarshal(r.raw, &pk)
	if !pk.Data.Running || pk.Data.Iface != "lo" || len(pk.Data.Packets) < 3 {
		t.Fatalf("packets: %s", r.raw)
	}
	r = e.do("GET", "/api/capture/download", nil, withAuth(e, false))
	if r.code != 200 || r.hdr.Get("Content-Type") != "application/gzip" || !strings.HasSuffix(r.hdr.Get("Content-Disposition"), `.tgz"`) {
		t.Fatalf("download: %d %v", r.code, r.hdr)
	}
	zr, err := gzip.NewReader(bytes.NewReader(r.raw))
	if err != nil {
		t.Fatalf("not a gzip: %v", err)
	}
	tr := tar.NewReader(zr)
	hd, err := tr.Next()
	if err != nil || !strings.HasSuffix(hd.Name, ".pcap") {
		t.Fatalf("the archive's first file: %v %+v", err, hd)
	}
	pcapBytes, _ := io.ReadAll(tr)
	if _, p, err := readPcap(pcapBytes); err != nil || len(p) < 3 {
		t.Fatalf("the pcap: %v %d", err, len(p))
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("the archive holds more than the one .pcap: %v", err)
	}
	if r := e.do("GET", "/api/capture/pcap", nil, withAuth(e, false)); r.code == 200 {
		t.Fatalf("the old .pcap download still answers")
	}
	if r := e.do("POST", "/api/capture/clear", map[string]string{}, withAuth(e, true)); r.code != 200 {
		t.Fatalf("clear: %d", r.code)
	}
	json.Unmarshal(e.do("GET", "/api/capture/packets?since=0", nil, withAuth(e, false)).raw, &pk)
	if len(pk.Data.Packets) != 0 {
		t.Fatalf("not cleared: %d", len(pk.Data.Packets))
	}
	e.do("POST", "/api/capture/stop", map[string]string{}, withAuth(e, true))
	json.Unmarshal(e.do("GET", "/api/capture/packets?since=0", nil, withAuth(e, false)).raw, &pk)
	if pk.Data.Running {
		t.Fatal("still running after stop")
	}
	// not logged in
	if r := e.do("GET", "/api/capture/interfaces", nil); r.code != http.StatusUnauthorized {
		t.Fatalf("without a session: %d", r.code)
	}
	// a timed capture is started and then asked for; its time is bounded
	if r := e.do("POST", "/api/capture/job", map[string]any{"id": "t0", "iface": "lo", "seconds": 61}, withAuth(e, true)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("61 s: %d", r.code)
	}
	if r := e.do("POST", "/api/capture/job", map[string]any{"id": "t1", "iface": "lo", "filter": "udp", "seconds": 1}, withAuth(e, true)); r.code != 200 {
		t.Fatalf("job start: %d %s", r.code, r.raw)
	}
	var st struct {
		Data CaptureRunStatus `json:"data"`
	}
	for i := 0; i < 40; i++ {
		time.Sleep(100 * time.Millisecond)
		json.Unmarshal(e.do("GET", "/api/capture/job?id=t1", nil, withAuth(e, false)).raw, &st)
		if st.Data.Done {
			break
		}
	}
	if !st.Data.Done || st.Data.Error != "" || len(st.Data.Pcap) < 24 || st.Data.Iface != "lo" {
		t.Fatalf("job: %+v", st.Data)
	}
	if r := e.do("GET", "/api/capture/job?id=nope", nil, withAuth(e, false)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown job: %d", r.code)
	}
}

// What a node relays must be allowed, and what asks the other nodes itself must not be.
func TestCaptureRelayRules(t *testing.T) {
	for _, p := range []string{"/api/capture/interfaces", "/api/capture/start", "/api/capture/packets?since=3", "/api/capture/download", "/api/capture/job", "/api/capture/job?id=x"} {
		m := "GET"
		if strings.HasSuffix(p, "start") || p == "/api/capture/job" {
			m = "POST"
		}
		if err := proxyAllowed(m, p); err != nil {
			t.Errorf("%s cannot be relayed: %v", p, err)
		}
	}
	for _, p := range []string{"/api/clustercapture/start", "/api/clustercapture/status", "/api/clustercapture/download"} {
		if proxyAllowed("GET", p) == nil || proxyAllowed("POST", p) == nil {
			t.Errorf("%s can be relayed", p)
		}
	}
}

// ── every node ───────────────────────────────────────────────────────────────

func tgzFiles(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		d, _ := io.ReadAll(tr)
		out[h.Name] = d
	}
	return out
}

func waitJob(t *testing.T, m *Mgmt) *CaptureJob {
	t.Helper()
	for i := 0; i < 200; i++ {
		if j := m.CaptureClusterStatus(); j != nil && j.Done {
			return j
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the cluster capture never finished")
	return nil
}

func TestClusterCaptureBundlesEveryNode(t *testing.T) {
	canCapture(t)
	a, b := twoNodeCluster(t)
	withWeb(t, a)
	withWeb(t, b)
	l, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer l.Close()
	port := l.LocalAddr().(*net.UDPAddr).Port
	if _, err := a.mg.CaptureClusterStart("lo", "udp and dst port "+itoa(port), 2, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.mg.CaptureClusterStart("lo", "", 2, "tester"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("a second job at once: %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	sendUDP(t, port, 3)
	j := waitJob(t, a.mg)
	if !j.Ready || len(j.Nodes) != 2 {
		t.Fatalf("job: %+v", j)
	}
	selves := 0
	for _, n := range j.Nodes {
		if n.Status != "done" || n.Kept != 3 {
			t.Errorf("node %+v", n)
		}
		if n.Self {
			selves++
		}
	}
	if selves != 1 || !j.Nodes[0].Self {
		t.Errorf("this node is marked %d times, first %v", selves, j.Nodes[0].Self)
	}
	tgz, err := a.mg.CaptureClusterBundle()
	if err != nil {
		t.Fatal(err)
	}
	files := tgzFiles(t, tgz)
	pcaps := 0
	for name, d := range files {
		if strings.HasSuffix(name, ".pcap") {
			pcaps++
			if _, p, err := readPcap(d); err != nil || len(p) != 3 {
				t.Errorf("%s: %v %d", name, err, len(p))
			}
		}
	}
	if pcaps != 2 || len(files["summary.txt"]) == 0 || files["errors.txt"] != nil {
		t.Fatalf("bundle: %v", keys(files))
	}

	// a node that cannot be asked is named in errors.txt and the others still make the bundle
	b.mg.webH = nil
	if _, err := a.mg.CaptureClusterStart("lo", "", 1, "tester"); err != nil {
		t.Fatal(err)
	}
	j = waitJob(t, a.mg)
	bad := 0
	for _, n := range j.Nodes {
		if n.Status == "error" && n.Error != "" {
			bad++
		}
	}
	files = tgzFiles(t, mustBundle(t, a.mg))
	if bad != 1 || !j.Ready || files["errors.txt"] == nil {
		t.Fatalf("one node down: bad %d ready %v files %v", bad, j.Ready, keys(files))
	}
	// an interface the nodes do not have: every node says so, and there is nothing to download
	if _, err := a.mg.CaptureClusterStart("nonesuch0", "", 1, "tester"); err != nil {
		t.Fatal(err)
	}
	j = waitJob(t, a.mg)
	if j.Ready || j.Error == "" {
		t.Fatalf("a missing interface: %+v", j)
	}
	if _, err := a.mg.CaptureClusterBundle(); err == nil {
		t.Error("a bundle of nothing")
	}
}

func TestClusterCaptureRefusals(t *testing.T) {
	e := newWebEnv(t)
	for _, c := range []struct {
		iface, filter string
		secs          int
		want          string
	}{{"lo", "", 0, "1 to 60"}, {"lo", "", 61, "1 to 60"}, {"", "", 5, "interface"}, {"lo", "port x", 5, "filter"}} {
		if _, err := e.mg.CaptureClusterStart(c.iface, c.filter, c.secs, "t"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

func keys(m map[string][]byte) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	return k
}

func mustBundle(t *testing.T, m *Mgmt) []byte {
	t.Helper()
	b, err := m.CaptureClusterBundle()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ── timed captures started on a node and asked for ───────────────────────────

func TestCaptureJobIsIdempotentAndBounded(t *testing.T) {
	canCapture(t)
	e := newWebEnv(t)
	a, err := e.mg.CaptureJobStart("same", "lo", "udp", 1, "t")
	if err != nil || !a.Running {
		t.Fatalf("start: %v %+v", err, a)
	}
	if _, err := e.mg.CaptureJobStart("same", "lo", "udp", 1, "t"); err != nil {
		t.Fatalf("the same id again: %v", err)
	}
	e.mg.capruns.mu.Lock()
	n := len(e.mg.capruns.m)
	e.mg.capruns.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d captures for one id (a request sent twice must not start two)", n)
	}
	for _, bad := range []string{"", strings.Repeat("x", 41), "a b", "a/b", "a?b"} {
		if _, err := e.mg.CaptureJobStart(bad, "lo", "", 1, "t"); err == nil {
			t.Errorf("id %q accepted", bad)
		}
	}
	if _, err := e.mg.CaptureJobStart("x2", "nope0", "", 1, "t"); err == nil || !strings.Contains(err.Error(), "no interface") {
		t.Errorf("a missing interface must be refused when the capture starts: %v", err)
	}
	for i := 0; i < capRunsKept; i++ {
		e.mg.CaptureJobStart("fill"+itoa(i), "lo", "", 1, "t")
	}
	if _, err := e.mg.CaptureJobStart("one-too-many", "lo", "", 1, "t"); err == nil || !strings.Contains(err.Error(), "several") {
		t.Errorf("no limit on held captures: %v", err)
	}
	time.Sleep(1300 * time.Millisecond)
	st, err := e.mg.CaptureJobGet("same")
	if err != nil || !st.Done || len(st.Pcap) < 24 {
		t.Fatalf("get: %v %+v", err, st)
	}
	if _, err := e.mg.CaptureJobGet("never"); err == nil {
		t.Error("an unknown capture was found")
	}
}

// A capture longer than the few seconds the peer channel gives one address must work, and start once on each node: the
// first version of the cluster capture asked each node to hold one request open for the whole time, so every address
// "timed out" and the request was sent again to the next one.
func TestClusterCaptureOutlastsTheFailoverTimeout(t *testing.T) {
	canCapture(t)
	a, b := twoNodeCluster(t)
	withWeb(t, a)
	withWeb(t, b)
	peer := a.mg.cl.node.Snapshot().Peers[0]
	if n := len(a.mg.cl.addrsFor(peer)); n < 2 {
		t.Skipf("the peer has one address (%d): nothing to fail over to", n)
	}
	l, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer l.Close()
	port := l.LocalAddr().(*net.UDPAddr).Port
	if _, err := a.mg.CaptureClusterStart("lo", "udp and dst port "+itoa(port), 8, "tester"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	sendUDP(t, port, 3)
	j := waitJob(t, a.mg)
	if !j.Ready {
		t.Fatalf("job: %+v", j)
	}
	for _, n := range j.Nodes {
		if n.Status != "done" || n.Kept != 3 {
			t.Errorf("node %+v", n)
		}
	}
	for name, mg := range map[string]*Mgmt{"a": a.mg, "b": b.mg} {
		mg.capruns.mu.Lock()
		n := len(mg.capruns.m)
		mg.capruns.mu.Unlock()
		if n != 1 {
			t.Errorf("node %s ran %d captures for one job", name, n)
		}
	}
}

func TestNodeAddrCandidatesLeaveOutSharedAddresses(t *testing.T) {
	ipn := func(s string) net.Addr {
		ip, n, _ := net.ParseCIDR(s)
		n.IP = ip
		return n
	}
	ifs := []ifaceAddrs{
		{Name: "lo", Loopback: true, Addrs: []net.Addr{ipn("127.0.0.1/8"), ipn("192.168.168.168/32")}}, // the anycast address is on lo
		{Name: "eth0", Addrs: []net.Addr{ipn("192.0.2.5/24"), ipn("2001:db8::5/64"), ipn("fe80::1/64"), ipn("192.0.2.9/24")}},
		{Name: "ddgw1.2", Addrs: []net.Addr{ipn("192.0.2.9/24"), ipn("2001:db8::9/64")}}, // the VIPs, on the virtual-MAC interface
		{Name: "eth1", Addrs: []net.Addr{ipn("198.51.100.7/24")}},
	}
	got := nodeAddrCandidates("192.0.2.5:53854", "ns1", ifs, map[string]bool{"192.0.2.9": true, "2001:db8::9": true, "192.168.168.168": true})
	want := []string{"192.0.2.5:53854", "ns1:53854", "[2001:db8::5]:53854", "198.51.100.7:53854"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v, want %v", got, want)
	}
	// the VIP is left out even if it is on a physical interface and the interface name says nothing
	for _, a := range got {
		if strings.Contains(a, "192.0.2.9") || strings.Contains(a, "168.168") || strings.Contains(a, "2001:db8::9") {
			t.Errorf("a shared address is advertised: %s", a)
		}
	}
}

// What the cluster reads from the configuration: the VIPs and the anycast addresses, in any spelling.
func TestSharedAddrsComeFromTheGateways(t *testing.T) {
	e := newWebEnv(t)
	dc, _, _ := e.mg.LiveConfig()
	g := defaultGroup()
	g.GroupID, g.VIP4, g.VIP6, g.ExtraVIPs = 1, "10.20.0.205/28", "2620:ad:8081:c0cf:10:129:0:205/64", []string{"192.168.168.168", "fd00::53/128"}
	dc.Groups = []GroupConfig{g}
	if err := e.mg.PutConfig(dc, "t", ""); err != nil {
		t.Skipf("cannot set the test config: %v", err)
	}
	c := &Cluster{mg: e.mg}
	sh := c.sharedAddrs()
	for _, want := range []string{"10.20.0.205", "2620:ad:8081:c0cf:10:129:0:205", "192.168.168.168", "fd00::53"} {
		if !sh[want] {
			t.Errorf("%s is not among the shared addresses %v", want, sh)
		}
	}
}
