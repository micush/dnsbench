package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Packet capture: a tcpdump-like diagnostic for the GUI and the command line (Monitor ▸ Capture, --capture).  A raw
// AF_PACKET socket on one interface (no libpcap), a bounded rolling buffer of the most recent packets, one-line
// summaries for the live list, and an export as a standard .pcap.  It only listens (nothing is injected), needs the
// privileges the daemon already has, and is off until someone starts it.
//
// Each node has one capture slot (the Capture page of that node); the cluster-wide capture and the command line use a
// private capture of their own, so they never take over what someone is watching on the page.

const (
	capSnaplen    = 262144   // per-packet capture cap
	capMaxPackets = 5000     // rolling buffer depth of the page's capture
	capMaxBytes   = 32 << 20 // rolling buffer size of the page's capture
	// A capture that is carried to another node (the cluster capture, or the page of a node reached through the Node menu)
	// must fit the peer channel's body limit after the relay's encoding: it keeps the newest packets that fit.
	capRelayBytes = 5 << 20
	// the longest a one-shot capture may run, and the most packets and bytes it keeps (the newest)
	capRunMaxSeconds = 60
	capRunMaxPackets = 20000
	capFilterMax     = 300 // characters

	linktypeEthernet = 1   // LINKTYPE_ETHERNET
	linktypeRaw      = 101 // LINKTYPE_RAW (bare IPv4/IPv6, an interface without a hardware address)
)

type capPacket struct {
	seq     int64
	t       time.Time
	data    []byte
	origlen int
	summary string
}

// captureState is one capture's buffer and status.
type captureState struct {
	mu       sync.Mutex
	buf      []capPacket
	bytes    int
	seq      int64
	epoch    int64 // bumped on every start and stop, so a late packet from an earlier socket is dropped
	running  bool
	iface    string
	filter   string
	linktype int
	handle   capHandle
	maxPkts  int // 0: capMaxPackets
	maxBytes int // 0: capMaxBytes
	matched  int64
	dropped  int64 // packets the filter turned away
}

type capHandle interface{ stop() }

func (cs *captureState) limits() (int, int) {
	p, b := cs.maxPkts, cs.maxBytes
	if p <= 0 {
		p = capMaxPackets
	}
	if b <= 0 {
		b = capMaxBytes
	}
	return p, b
}

// begin stops any running capture, empties the buffer and returns the epoch of the new one.
func (cs *captureState) begin(iface, filter string, linktype int) int64 {
	cs.mu.Lock()
	old := cs.handle
	cs.epoch++
	cs.handle = nil
	cs.buf, cs.bytes, cs.seq, cs.matched, cs.dropped = nil, 0, 0, 0, 0
	cs.iface, cs.filter, cs.linktype, cs.running = iface, filter, linktype, true
	ep := cs.epoch
	cs.mu.Unlock()
	if old != nil {
		old.stop()
	}
	return ep
}

func (cs *captureState) setHandle(ep int64, h capHandle) {
	cs.mu.Lock()
	if cs.epoch == ep {
		cs.handle = h
		cs.mu.Unlock()
		return
	}
	cs.mu.Unlock()
	h.stop() // a newer capture took over before this one finished starting
}

func (cs *captureState) failStart(ep int64) {
	cs.mu.Lock()
	if cs.epoch == ep {
		cs.running = false
	}
	cs.mu.Unlock()
}

func (cs *captureState) stop() {
	cs.mu.Lock()
	old := cs.handle
	cs.handle = nil
	cs.running = false
	cs.epoch++
	cs.mu.Unlock()
	if old != nil {
		old.stop()
	}
}

func (cs *captureState) clear() {
	cs.mu.Lock()
	cs.buf, cs.bytes = nil, 0
	cs.mu.Unlock()
}

// add keeps a packet captured under epoch ep (a later start or stop makes it stale), after the filter.
func (cs *captureState) add(ep int64, f *capFilter, t time.Time, data []byte) {
	cs.mu.Lock()
	lt := cs.linktype
	cs.mu.Unlock()
	if f != nil && !f.match(lt, data) {
		atomic.AddInt64(&cs.dropped, 1)
		return
	}
	sum := summarizePacket(lt, data)
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if ep != cs.epoch {
		return
	}
	cs.seq++
	cs.matched++
	cs.buf = append(cs.buf, capPacket{seq: cs.seq, t: t, data: data, origlen: len(data), summary: sum})
	cs.bytes += len(data)
	maxP, maxB := cs.limits()
	drop := 0
	for len(cs.buf)-drop > maxP || cs.bytes > maxB {
		cs.bytes -= len(cs.buf[drop].data)
		drop++
	}
	if drop > 0 {
		n := copy(cs.buf, cs.buf[drop:])
		for i := n; i < len(cs.buf); i++ {
			cs.buf[i] = capPacket{} // let the dropped packets go
		}
		cs.buf = cs.buf[:n]
	}
}

// capStatus is what the page polls: the packets after a cursor and the capture's state.
type capStatus struct {
	Running bool     `json:"running"`
	Iface   string   `json:"iface"`
	Filter  string   `json:"filter"`
	Cursor  int64    `json:"cursor"`
	Packets []capRow `json:"packets"`
	Count   int      `json:"count"`   // packets in the buffer
	Bytes   int      `json:"bytes"`   // their size
	Matched int64    `json:"matched"` // packets kept since the start (the buffer holds the newest)
	Seen    int64    `json:"seen"`    // packets the socket delivered since the start, kept or not
}

type capRow struct {
	Seq     int64  `json:"seq"`
	Time    string `json:"time"`
	Summary string `json:"summary"`
	Len     int    `json:"len"`
}

// since returns the packets newer than the cursor (the newest max of them) and the capture's status.
func (cs *captureState) since(after int64, max int) capStatus {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	st := capStatus{Running: cs.running, Iface: cs.iface, Filter: cs.filter, Cursor: cs.seq, Count: len(cs.buf), Bytes: cs.bytes,
		Matched: cs.matched, Seen: cs.matched + atomic.LoadInt64(&cs.dropped), Packets: []capRow{}}
	for _, p := range cs.buf {
		if p.seq > after {
			st.Packets = append(st.Packets, capRow{Seq: p.seq, Time: p.t.Format("15:04:05.000"), Summary: p.summary, Len: p.origlen})
		}
	}
	if len(st.Packets) > max {
		st.Packets = st.Packets[len(st.Packets)-max:]
	}
	return st
}

// writePcap writes the buffer as a .pcap; when limit > 0 only the newest packets that fit in limit bytes of file.
func (cs *captureState) writePcap(w io.Writer, limit int) {
	cs.mu.Lock()
	linktype, iface := cs.linktype, cs.iface
	pkts := make([]capPacket, len(cs.buf))
	copy(pkts, cs.buf)
	cs.mu.Unlock()
	if iface == "" {
		linktype = linktypeEthernet // never started: an empty file
	}
	if limit > 0 {
		size, from := 24, len(pkts)
		for from > 0 {
			n := 16 + len(pkts[from-1].data)
			if size+n > limit {
				break
			}
			size += n
			from--
		}
		pkts = pkts[from:]
	}
	w.Write(pcapGlobalHeader(capSnaplen, linktype))
	for _, p := range pkts {
		w.Write(pcapRecord(p.t, len(p.data), p.origlen, p.data))
	}
}

// linktypeForIface: Ethernet framing for anything with a hardware address (and the loopback, which AF_PACKET frames as
// Ethernet); an interface without one delivers bare IP packets.
func linktypeForIface(ifi *net.Interface) int {
	if ifi.Flags&net.FlagLoopback != 0 || len(ifi.HardwareAddr) == 6 {
		return linktypeEthernet
	}
	return linktypeRaw
}

// ── the socket ───────────────────────────────────────────────────────────────

type linuxCapture struct {
	fd      int
	stopped atomic.Bool
	done    chan struct{}
}

// startCapture opens an AF_PACKET socket bound to the interface (it needs CAP_NET_RAW, which the daemon has) and gives
// every frame, sent or received, to onPacket until stop().
func startCapture(ifaceName string, snaplen int, onPacket func(time.Time, []byte)) (capHandle, error) {
	ifi, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("interface %q: %w", ifaceName, err)
	}
	// SO_RCVTIMEO so a read comes back now and then and the loop notices stop() on an idle interface
	fd, err := openPacketSocket(ifaceName, uint16(syscall.ETH_P_ALL), 300*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("cannot listen on %q (the daemon needs root or CAP_NET_RAW): %w", ifaceName, err)
	}
	h := &linuxCapture{fd: fd, done: make(chan struct{})}
	go h.loop(snaplen, ifi.Flags&net.FlagLoopback != 0, onPacket)
	return h, nil
}

// stop ends the capture and waits for its socket to be released.
func (h *linuxCapture) stop() {
	h.stopped.Store(true)
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
	}
}

func (h *linuxCapture) loop(snaplen int, loopback bool, onPacket func(time.Time, []byte)) {
	defer close(h.done)
	defer syscall.Close(h.fd)
	buf := make([]byte, snaplen)
	for !h.stopped.Load() {
		n, from, err := syscall.Recvfrom(h.fd, buf, 0)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EINTR {
				continue
			}
			return
		}
		if n <= 0 || h.stopped.Load() {
			continue
		}
		// On the loopback every packet comes twice, once as it is sent and once as it is received: keep the one
		// (as tcpdump does).
		if ll, ok := from.(*syscall.SockaddrLinklayer); ok && loopback && ll.Pkttype == syscall.PACKET_OUTGOING {
			continue
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		onPacket(time.Now(), pkt)
	}
}

// startOn starts a capture of iface into cs with the filter (already compiled from filterText).
func (cs *captureState) startOn(iface, filterText string) error {
	if len(filterText) > capFilterMax {
		return fmt.Errorf("the filter is too long (at most %d characters)", capFilterMax)
	}
	f, err := compileCapFilter(filterText)
	if err != nil {
		return err
	}
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return fmt.Errorf("no interface named %q on this node", iface)
	}
	ep := cs.begin(ifi.Name, filterText, linktypeForIface(ifi))
	h, err := startCapture(ifi.Name, capSnaplen, func(t time.Time, d []byte) { cs.add(ep, f, t, d) })
	if err != nil {
		cs.failStart(ep)
		return err
	}
	cs.setHandle(ep, h)
	return nil
}

// beginPrivate starts a capture of iface into a buffer of its own (the newest packets, at most limit bytes); finish ends it.
func beginPrivate(iface, filter string, limit int) (*captureState, error) {
	cs := &captureState{maxPkts: capRunMaxPackets, maxBytes: limit}
	if cs.maxBytes <= 0 {
		cs.maxBytes = capMaxBytes
	}
	if err := cs.startOn(iface, filter); err != nil {
		return nil, err
	}
	return cs, nil
}

// finishPrivate stops a private capture and returns the .pcap (the newest packets that fit limit bytes, 0 for no limit),
// how many packets it holds and how many the socket delivered.
func finishPrivate(cs *captureState, limit int) (pcap []byte, kept int, seen int64) {
	cs.stop()
	var b bytes.Buffer
	cs.writePcap(&b, limit)
	st := cs.since(1<<62, 0)
	cs.mu.Lock()
	kept = len(cs.buf)
	cs.mu.Unlock()
	return b.Bytes(), kept, st.Seen
}

// captureRun captures on iface for d (or until ctx ends) into a private buffer and returns what finishPrivate does.
func captureRun(ctx context.Context, iface, filter string, d time.Duration, limit int) (pcap []byte, kept int, seen int64, err error) {
	cs, err := beginPrivate(iface, filter, limit)
	if err != nil {
		return nil, 0, 0, err
	}
	t := time.NewTimer(d)
	select {
	case <-t.C:
	case <-ctx.Done():
		t.Stop()
	}
	pcap, kept, seen = finishPrivate(cs, limit)
	return pcap, kept, seen, nil
}

// captureInterfaces lists the interfaces a capture can be started on.
type capIface struct {
	Name string `json:"name"`
	Up   bool   `json:"up"`
	MAC  string `json:"mac,omitempty"`
}

func captureInterfaces() []capIface {
	ifs, err := net.Interfaces()
	out := []capIface{}
	if err != nil {
		return out
	}
	for _, i := range ifs {
		out = append(out, capIface{Name: i.Name, Up: i.Flags&net.FlagUp != 0, MAC: i.HardwareAddr.String()})
	}
	return out
}

// ── pcap (classic libpcap, microsecond timestamps, little-endian) ────────────

func pcapGlobalHeader(snaplen, linktype int) []byte {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint32(b[0:], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(b[4:], 2)
	binary.LittleEndian.PutUint16(b[6:], 4)
	binary.LittleEndian.PutUint32(b[16:], uint32(snaplen))
	binary.LittleEndian.PutUint32(b[20:], uint32(linktype))
	return b
}

func pcapRecord(t time.Time, caplen, origlen int, data []byte) []byte {
	b := make([]byte, 16+len(data))
	binary.LittleEndian.PutUint32(b[0:], uint32(t.Unix()))
	binary.LittleEndian.PutUint32(b[4:], uint32(t.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(b[8:], uint32(caplen))
	binary.LittleEndian.PutUint32(b[12:], uint32(origlen))
	copy(b[16:], data)
	return b
}

// readPcap reads a classic little-endian .pcap (what writePcap makes): the link type and the packets.
func readPcap(b []byte) (linktype int, pkts []capPacket, err error) {
	if len(b) < 24 || binary.LittleEndian.Uint32(b[0:]) != 0xa1b2c3d4 {
		return 0, nil, fmt.Errorf("not a .pcap file")
	}
	linktype = int(binary.LittleEndian.Uint32(b[20:]))
	for off := 24; off+16 <= len(b); {
		caplen := int(binary.LittleEndian.Uint32(b[off+8:]))
		orig := int(binary.LittleEndian.Uint32(b[off+12:]))
		if caplen < 0 || off+16+caplen > len(b) {
			break // a cut-off last record
		}
		t := time.Unix(int64(binary.LittleEndian.Uint32(b[off:])), int64(binary.LittleEndian.Uint32(b[off+4:]))*1000)
		d := b[off+16 : off+16+caplen]
		pkts = append(pkts, capPacket{seq: int64(len(pkts) + 1), t: t, data: d, origlen: orig, summary: summarizePacket(linktype, d)})
		off += 16 + caplen
	}
	return linktype, pkts, nil
}
