package main

import (
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// rawResponder owns an AF_PACKET socket and a goroutine that answers ARP
// requests / IPv6 neighbor solicitations for the VIP with the vMAC of the AFN
// chosen by the engine's load-balancing method.  Only the AGC runs one.
type rawResponder struct {
	fd   int
	stop atomic.Bool
}

// Stop signals the goroutine; it closes the socket on its next wake-up
// (SO_RCVTIMEO bounds that to ~1s).  Deliberately does not wait: the loop
// takes the engine lock, and Stop is called with that lock held.
func (r *rawResponder) Stop() { r.stop.Store(true) }

func (r *rawResponder) loop(handle func(fd int, frame []byte)) {
	defer syscall.Close(r.fd)
	buf := make([]byte, 4096)
	for !r.stop.Load() {
		n, _, err := syscall.Recvfrom(r.fd, buf, 0)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EINTR {
				continue
			}
			return
		}
		if r.stop.Load() {
			return
		}
		handle(r.fd, buf[:n])
	}
}

func (e *Engine) stopRespondersLocked() {
	if e.arp != nil {
		e.arp.Stop()
		e.arp = nil
	}
	if e.ns != nil {
		e.ns.Stop()
		e.ns = nil
	}
}

// ── ARP (IPv4) ───────────────────────────────────────────────────────────────

func (e *Engine) startARPResponderLocked() {
	if e.arp != nil {
		return
	}
	vip, err := vipAddr(e.cfg.VIP4)
	if err != nil {
		return
	}
	fd, err := openPacketSocket(e.cfg.Interface, syscall.ETH_P_ARP, time.Second)
	if err != nil {
		errorf("Failed to start ARP responder: %v", err)
		return
	}
	r := &rawResponder{fd: fd}
	e.arp = r
	vip4s := [][4]byte{vip.As4()}
	for _, v := range e.cfg.vipsFor(afIPv4)[1:] {
		if a, err := vipAddr(v); err == nil {
			vip4s = append(vip4s, a.As4())
		}
	}
	// The answer names the slot's MAC in its payload (a virtual MAC, or in real-MAC mode the real MAC of the node that has
	// the slot), but is sent from this node's own MAC.  With the slot's virtual MAC as the Ethernet source, every answer
	// made the switch learn that MAC on the controller's port, and the queries sent to the forwarder that really owns it
	// were then delivered to the wrong node until it next spoke.
	selfMAC := ifaceMAC(e.cfg.Interface)
	go r.loop(func(fd int, frame []byte) {
		if out := e.arpAnswerAny(frame, selfMAC, vip4s); out != nil {
			if _, err := syscall.Write(fd, out); err != nil {
				debugf("ARP reply send failed: %v", err)
			}
		}
	})
	infof("ARP responder started (group=%d vip=%s%s)", e.cfg.GroupID, e.cfg.VIP4, moreNote(e.cfg.MoreVIP4))
}

// ── NS (IPv6) ────────────────────────────────────────────────────────────────

func (e *Engine) startNSResponderLocked() {
	if e.ns != nil {
		return
	}
	vip, err := vipAddr(e.cfg.VIP6)
	if err != nil {
		return
	}
	fd, err := openPacketSocket(e.cfg.Interface, syscall.ETH_P_IPV6, time.Second)
	if err != nil {
		errorf("Failed to start NS responder: %v", err)
		return
	}
	r := &rawResponder{fd: fd}
	e.ns = r
	vip6s := [][16]byte{vip.As16()}
	for _, v := range e.cfg.vipsFor(afIPv6)[1:] {
		if a, err := vipAddr(v); err == nil {
			vip6s = append(vip6s, a.As16())
		}
	}
	selfMAC := ifaceMAC(e.cfg.Interface) // see the ARP responder: the answer is sent from this node's own MAC
	go r.loop(func(fd int, frame []byte) {
		if out := e.nsAnswerAny(frame, selfMAC, vip6s); out != nil {
			if _, err := syscall.Write(fd, out); err != nil {
				debugf("NA reply send failed: %v", err)
			}
		}
	})
	infof("NS responder started (group=%d vip=%s%s)", e.cfg.GroupID, e.cfg.VIP6, moreNote(e.cfg.MoreVIP6))
}

// ethSource is the Ethernet source of an answer sent on behalf of a slot: this node's own MAC, or the slot's MAC
// if the interface's cannot be read.
func ethSource(self, slot [6]byte) []byte {
	if self != ([6]byte{}) {
		return self[:]
	}
	return slot[:]
}

// buildARPReply answers an ARP request for the VIP: the payload says the VIP is at mac, the Ethernet source is this
// node's own MAC (see ethSource).
func buildARPReply(reqMAC, self, mac [6]byte, vip4, reqIP [4]byte) []byte {
	out := make([]byte, 0, 42)
	out = append(out, reqMAC[:]...)
	out = append(out, ethSource(self, mac)...)
	out = append(out, 0x08, 0x06)
	out = append(out, 0x00, 0x01, 0x08, 0x00, 6, 4, 0x00, 0x02)
	out = append(out, mac[:]...)
	out = append(out, vip4[:]...)
	out = append(out, reqMAC[:]...)
	out = append(out, reqIP[:]...)
	return out
}

// moreNote is " +a, b" for the log line of a gateway with further shared addresses, or "".
func moreNote(more []string) string {
	if len(more) == 0 {
		return ""
	}
	return " +" + strings.Join(more, ", ")
}
