package main

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"time"
)

// Real-MAC mode (gateway setting real_macs; on for a gateway created now, off for one read from a file that does not say, the virtual-MAC way).
//
// Without virtual MACs there is no macvlan and nothing to take over: every node holds the VIP on lo (as a forwarder
// always did), the controller answers ARP and neighbor solicitations for it, and the answer names the REAL MAC address of
// the node it picks.  For that it must know the real MACs of the other nodes: it learns them from the frames they send
// (an ARP from a node, any IPv6 frame from its link-local address) and asks, with an ARP request or a neighbor
// solicitation of its own, for the ones it has not heard.  Nothing is added to the gateway protocol, so nodes of any
// version take part.  A node whose MAC is not known yet is not picked: the controller answers with its own.
//
// What it costs is failover.  A virtual MAC is covered by another node when its node dies, so the clients' neighbor
// caches stay right; a real MAC is not.  When a node goes, the controller announces the VIP at its own MAC with an
// unsolicited ARP/NA (a few times), which updates the neighbors that honor one; the others keep sending to the dead node
// until their cache entry ages out.

// rawSend sends a frame on iface, and ifaceMACFn reads an interface's MAC (replaceable in tests).
var (
	rawSend    = sendRawFrame
	ifaceMACFn = ifaceMAC
)

// peerMACKnown is how long a learned MAC is trusted without hearing from the node again.
const peerMACKnown = 5 * time.Minute

// peerMACRefresh is how often the controller asks a node for its MAC once it knows it (a node's NIC can change).
const peerMACRefresh = time.Minute

// peerMACRetry is how often it asks while it does not know.
const peerMACRetry = 5 * time.Second

type peerMACEntry struct {
	mac   [6]byte
	at    time.Time // when it was last heard
	asked time.Time // when it was last asked for
}

// notePeerMACLocked records the MAC a known peer was heard at (e.mu held).  Anything but a known peer is ignored.
func (e *Engine) notePeerMACLocked(ip string, mac [6]byte) {
	if !e.cfg.RealMACs || mac == ([6]byte{}) || mac[0]&1 != 0 { // not a multicast source
		return
	}
	if _, ok := e.peers[ip]; !ok {
		return
	}
	if e.peerMACs == nil {
		e.peerMACs = map[string]*peerMACEntry{}
	}
	en := e.peerMACs[ip]
	if en == nil {
		en = &peerMACEntry{}
		e.peerMACs[ip] = en
	}
	en.mac, en.at = mac, time.Now()
}

// answerMACLocked is the MAC a requester is told for slot: the slot's virtual MAC, or in real-MAC mode the real MAC of
// the node that has the slot (this node's own for its own slot, or when the node's MAC is not known).
func (e *Engine) answerMACLocked(slot int, self [6]byte) [6]byte {
	if !e.cfg.RealMACs {
		return vmacBytes(e.cfg.GroupID, slot)
	}
	if slot == 0 || slot == e.afnID {
		return self
	}
	for ip, p := range e.peers {
		if p.AfnID != slot || p.expired(e.cfg.HoldMS) {
			continue
		}
		if en := e.peerMACs[ip]; en != nil && time.Since(en.at) < peerMACKnown {
			return en.mac
		}
		break
	}
	return self
}

// resolvePeerMACsLocked asks for the real MACs the controller does not know (or has not confirmed for a minute).
func (e *Engine) resolvePeerMACsLocked() {
	if !e.cfg.RealMACs || e.state != stateActive {
		return
	}
	self := ifaceMACFn(e.cfg.Interface)
	if self == ([6]byte{}) {
		return
	}
	me, err := netip.ParseAddr(e.myIP)
	if err != nil {
		return
	}
	for ip, p := range e.peers {
		if p.AfnID == 0 || p.expired(e.cfg.HoldMS) {
			continue
		}
		en := e.peerMACs[ip]
		if en != nil {
			wait := peerMACRefresh
			if time.Since(en.at) >= peerMACKnown {
				wait = peerMACRetry
			}
			if time.Since(en.asked) < wait || time.Since(en.at) < wait && time.Since(en.at) < peerMACRefresh {
				continue
			}
		}
		target, err := netip.ParseAddr(strings.TrimSuffix(ip, "%"+e.cfg.Interface))
		if err != nil {
			continue
		}
		if en == nil {
			if e.peerMACs == nil {
				e.peerMACs = map[string]*peerMACEntry{}
			}
			en = &peerMACEntry{}
			e.peerMACs[ip] = en
		} else if time.Since(en.asked) < peerMACRetry {
			continue
		}
		en.asked = time.Now()
		var frame []byte
		switch {
		case e.af == afIPv4 && me.Is4() && target.Is4():
			frame = buildARPRequest(self, me.As4(), target.As4())
		case e.af == afIPv6 && me.Is6() && target.Is6():
			frame = buildNSFor(self, me.As16(), target.As16())
		}
		if frame != nil {
			if err := rawSend(e.cfg.Interface, frame); err != nil {
				debugf("asking %s for its MAC failed: %v", ip, err)
			}
		}
	}
}

// repointVIPLocked tells the neighbors the VIP is at this node's own MAC (an unsolicited ARP or neighbor advertisement,
// now and twice more shortly after): what real-MAC mode has in place of taking a dead node's MAC over.
func (e *Engine) repointVIPLocked() {
	if !e.cfg.RealMACs || e.state != stateActive {
		return
	}
	self := ifaceMACFn(e.cfg.Interface)
	if self == ([6]byte{}) {
		return
	}
	send := func() {
		for _, vip := range e.cfg.vipsFor(e.af) {
			if e.af == afIPv4 {
				sendGratuitousARPMAC(e.cfg.Interface, self, vip)
			} else {
				sendUnsolicitedNAMAC(e.cfg.Interface, self, vip)
			}
		}
	}
	send()
	for _, d := range []time.Duration{200 * time.Millisecond, time.Second} {
		time.AfterFunc(d, func() {
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.running && e.state == stateActive {
				send()
			}
		})
	}
}

// setupRealLocked is what a forwarder or the controller sets up in real-MAC mode: the VIP on lo and the DNS proxy.  The
// controller also announces the VIP at its own MAC.
func (e *Engine) setupRealLocked() {
	if e.afnID == 0 {
		return
	}
	infof("Real-MAC mode: slot %d, VIP on lo, no virtual MAC (%s)", e.afnID, e.tag())
	e.setupDNSLocked(true)
	if e.state == stateActive {
		e.repointVIPLocked()
	}
}

// ── what the responders do with a frame (separate from the socket so they can be tested) ──

// arpAnswerAny answers a request for any of the gateway's shared addresses (the first that matches).
func (e *Engine) arpAnswerAny(frame []byte, selfMAC [6]byte, vips [][4]byte) []byte {
	for _, v := range vips {
		if out := e.arpAnswer(frame, selfMAC, v); out != nil {
			return out
		}
	}
	return nil
}

// nsAnswerAny is arpAnswerAny for IPv6.
func (e *Engine) nsAnswerAny(frame []byte, selfMAC [6]byte, vips [][16]byte) []byte {
	for _, v := range vips {
		if out := e.nsAnswer(frame, selfMAC, v); out != nil {
			return out
		}
	}
	return nil
}

// arpAnswer is the reply to an ARP request for the VIP, or nil for any other frame.  It also learns the real MAC of the
// node that sent an ARP (real-MAC mode).
func (e *Engine) arpAnswer(frame []byte, selfMAC [6]byte, vip4 [4]byte) []byte {
	if len(frame) < 42 {
		return nil
	}
	arp := frame[14:]
	senderIP := [4]byte(arp[14:18])
	if e.cfg.RealMACs && senderIP != ([4]byte{}) {
		e.mu.Lock()
		e.notePeerMACLocked(netip.AddrFrom4(senderIP).String(), [6]byte(frame[6:12]))
		e.mu.Unlock()
	}
	if arp[6] != 0 || arp[7] != 1 { // only requests
		return nil
	}
	if [4]byte(arp[24:28]) != vip4 {
		return nil
	}
	senderMAC := [6]byte(frame[6:12])
	requester := netip.AddrFrom4(senderIP).String()
	e.mu.Lock()
	slot := e.pickAFNLocked(requester)
	mac := e.answerMACLocked(slot, selfMAC)
	e.mu.Unlock()
	debugf("ARP req for VIP from %s — replying with slot %d MAC %s", requester, slot, net.HardwareAddr(mac[:]))
	return buildARPReply(senderMAC, selfMAC, mac, vip4, senderIP)
}

// nsAnswer is the neighbor advertisement for a solicitation of the VIP, or nil.  It also learns the real MAC of a
// node from any IPv6 frame it sends from its address (real-MAC mode).
func (e *Engine) nsAnswer(frame []byte, selfMAC [6]byte, vip6 [16]byte) []byte {
	const eth, ip6 = 14, 40
	if len(frame) < eth+ip6+8 {
		return nil
	}
	h := frame[eth:]
	if h[0]>>4 != 6 {
		return nil
	}
	src := [16]byte(h[8:24])
	if e.cfg.RealMACs {
		e.mu.Lock()
		e.notePeerMACLocked(netip.AddrFrom16(src).String(), [6]byte(frame[6:12]))
		e.mu.Unlock()
	}
	if len(frame) < eth+ip6+24 || h[6] != 58 { // ICMPv6
		return nil
	}
	icmp := h[ip6:]
	if icmp[0] != 135 || [16]byte(icmp[8:24]) != vip6 { // NS for our VIP
		return nil
	}
	srcMAC := [6]byte(frame[6:12])
	requester := netip.AddrFrom16(src).String()
	e.mu.Lock()
	slot := e.pickAFNLocked(requester)
	vm := e.answerMACLocked(slot, selfMAC)
	e.mu.Unlock()
	debugf("NS for VIP6 from %s — replying with slot %d MAC %s", requester, slot, net.HardwareAddr(vm[:]))
	// Solicited + Override, unicast back to the solicitor.
	return buildNA(srcMAC, [6]byte(ethSource(selfMAC, vm)), vm, vip6, src, 0x60000000)
}

// ── the kernel's ARP settings on the real interface, saved and put back ──

// rememberSysctl sets path to val, keeping what it was (once) to put back.
func (e *Engine) rememberSysctl(path, val string) bool {
	if e.arpSaved == nil {
		e.arpSaved = map[string]string{}
	}
	if _, ok := e.arpSaved[path]; !ok {
		if b, err := os.ReadFile(path); err == nil {
			e.arpSaved[path] = strings.TrimSpace(string(b))
		}
	}
	return ensureSysctl(path, val)
}

func (e *Engine) restoreSysctls() {
	for path, old := range e.arpSaved {
		setSysctl(path, old)
	}
	e.arpSaved = nil
}

var _ = fmt.Sprintf
var _ = syscall.EAGAIN
