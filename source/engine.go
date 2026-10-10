package main

import (
	"context"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"sort"
	"sync"
	"syscall"
	"time"
)

// Peer is a neighbour learned from hellos.
type Peer struct {
	IP       string
	Priority int
	AfnID    int
	Weight   int
	Preempt  bool
	LastSeen time.Time
	// FirstSeen is when this peer (re)appeared; a returning node's virtual MAC is
	// handed back only after takeoverHandback so the node has it up by then.
	FirstSeen time.Time
	// Controller is whether its last hello said it is the active controller.
	Controller bool
}

// takeoverHandback is how long a returning node must have been heard before the
// controller lets go of the virtual MAC it was covering.
var takeoverHandback = 600 * time.Millisecond

func (p *Peer) expired(holdMS int) bool {
	return time.Since(p.LastSeen) > time.Duration(holdMS)*time.Millisecond
}

// stateName is the state the peer's hellos say it is in, as the tables show it: "active" for the controller
// (its hello carries the controller flag, set exactly while it is ACTIVE), "forward" for a node that holds a
// forwarder slot (a forwarder takes its slot as it moves to FORWARD), "standby" for one that has no slot yet,
// and "expired" for one not heard from within the hold time.  The hello does not carry the state itself (the
// wire format is unchanged), so a node in the middle of an election (listen, speak) shows as "standby".
func (p *Peer) stateName(holdMS int) string {
	switch {
	case p.expired(holdMS):
		return "expired"
	case p.Controller:
		return "active"
	case p.AfnID != 0:
		return "forward"
	}
	return "standby"
}

// Engine is the protocol engine for one address family within one group.
// For dual-stack groups the supervisor runs two engines.  All state is
// guarded by mu; methods ending in "Locked" expect it to be held.
type Engine struct {
	mu  sync.Mutex
	cfg GroupConfig
	af  AF

	dnsCfg func() DNSConfig // current DNS settings (port)
	pool   func() *Pool

	state    State
	peers    map[string]*Peer
	afnID    int
	agcIP    string
	rrCursor int
	myIP     string

	conn       net.PacketConn
	helloTimer *time.Timer
	reapTimer  *time.Timer
	helloGen   uint64
	reapGen    uint64
	running    bool
	cancel     context.CancelFunc

	failoverPrimary int
	takeover        map[int]bool             // AFN slots assumed on behalf of dead peers
	lingering       map[int]bool             // slots whose macvlan stays up for a while after a hand-over, see lingerLocked
	peerMACs        map[string]*peerMACEntry // real-MAC mode: the real MAC of each node, learned (see realmac.go)
	arpSaved        map[string]string        // real-MAC mode: the ARP settings of the real interface before they were changed
	assertUntil     time.Time                // until then this node has asked for the controller role (assertAGCLocked) and does not give way to the old controller
	vipSlot         int                      // the slot whose macvlan holds this engine's VIP (controller only), 0 = none
	lastAnnounce    time.Time                // when a forwarder last told the network where its virtual MAC is

	arp, ns *rawResponder

	dnsFE      *DNSFrontend
	dnsMore    []*DNSFrontend // listeners of the further shared addresses
	dnsLoAdded bool

	leaving bool // told the group we are going: no more hellos
}

// leaveGrace is how long a controller that is stopping keeps its address, ARP
// answers and virtual MAC after telling the group, so a peer has taken over
// before they disappear.
var leaveGrace = 400 * time.Millisecond

// announceBurst is when, after this node has taken a virtual MAC over, it says again that the MAC is here.  The node that
// is handing it over keeps answering on it until it has left, and each frame it sends from that MAC can move the switch's
// entry back to its port; one announcement at the start is then undone, and nothing else would put it right until the
// periodic one, up to reannounceEvery later.  The announcements go on past the hand-over's grace (leaveGrace) so the last
// word is the new owner's.
var announceBurst = []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond, 400 * time.Millisecond,
	500 * time.Millisecond, 650 * time.Millisecond, 800 * time.Millisecond, 1000 * time.Millisecond, 1500 * time.Millisecond, 2200 * time.Millisecond}

// How long a macvlan this node no longer needs stays up after a hand-over: twice the hold time, but not less than lingerMin
// nor more than lingerMax.
var (
	lingerMin = 2 * time.Second
	lingerMax = 10 * time.Second
)

// addVmacFn is addVmac, replaceable in tests.
var addVmacFn = addVmac

// prepareV4InputFn is prepareV4Input, replaceable in tests.
var prepareV4InputFn = prepareV4Input

// announceVmacFn is announceVmac, replaceable in tests.
var announceVmacFn = announceVmac

func NewEngine(cfg GroupConfig, af AF, dnsCfg func() DNSConfig, pool func() *Pool) *Engine {
	return &Engine{
		cfg: cfg, af: af, dnsCfg: dnsCfg, pool: pool,
		state: stateInit, peers: map[string]*Peer{},
		failoverPrimary: 1, takeover: map[int]bool{},
	}
}

func (e *Engine) tag() string { return fmt.Sprintf("group=%d af=%s", e.cfg.GroupID, e.af) }

// ── Lifecycle ────────────────────────────────────────────────────────────────

// Start returns immediately; a goroutine waits for the interface to have a
// usable address (it may not exist yet at boot), then binds and joins.
func (e *Engine) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	e.mu.Lock()
	e.running = true
	e.cancel = cancel
	e.mu.Unlock()
	go e.run(ctx)
}

func (e *Engine) run(ctx context.Context) {
	const poll = 10 * time.Second
	var ip string
	for {
		var ok bool
		if ip, ok = ifaceIP(e.cfg.Interface, e.af); ok {
			break
		}
		infof("Waiting for IP on %s (%s) — retrying in %ds", e.cfg.Interface, e.tag(), int(poll.Seconds()))
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
	infof("Starting distributed gateway/%s on %s ip=%s group=%d vip=%s",
		e.af, e.cfg.Interface, ip, e.cfg.GroupID, e.cfg.vipFor(e.af))

	var conn net.PacketConn
	for {
		var err error
		if conn, err = e.bind(); err == nil {
			break
		}
		errorf("bind failed (%s): %v — retrying in %ds", e.tag(), err, int(poll.Seconds()))
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}

	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		conn.Close()
		return
	}
	e.myIP = ip
	e.conn = conn
	e.transitionLocked(stateListen)
	hold := time.Duration(e.cfg.HoldMS) * time.Millisecond
	time.AfterFunc(hold, e.onListenTimeout)
	e.scheduleReapLocked()
	e.mu.Unlock()

	e.readLoop(conn)
}

func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	// A controller that is going away (update, restart, pause, shutdown) says so
	// and hands over first.  Without this the others notice only when its hellos
	// stop — a hold time later — and every client that cached the controller's
	// virtual MAC loses the gateway for that long, or until its ARP entry expires.
	if e.state == stateActive && e.conn != nil && leaveGrace > 0 {
		if pkt, err := buildPacket(&e.cfg, pktResign, e.af, e.afnID, 0, e.myIP); err == nil {
			e.leaving = true
			e.broadcastLocked(pkt)
			e.broadcastLocked(pkt) // multicast is not reliable and the grace is short: say it twice, as a forwarder does
			infof("Leaving the group: told the others to take over (%s)", e.tag())
			e.mu.Unlock()
			time.Sleep(leaveGrace)
			e.mu.Lock()
			if !e.running {
				e.mu.Unlock()
				return
			}
		}
	}
	// A forwarder that is going away says so, so the controller covers its slot at
	// once instead of a hold time later.
	if e.state == stateForward && e.afnID != 0 && e.conn != nil && leaveGrace > 0 && !e.leaving {
		if pkt, err := buildPacket(&e.cfg, pktHello, e.af, e.afnID, flagLeaving, e.myIP); err == nil {
			e.leaving = true
			e.broadcastLocked(pkt)
			e.broadcastLocked(pkt) // multicast is not reliable: say it twice
			infof("Leaving the group: told the controller to cover slot %d (%s)", e.afnID, e.tag())
			e.mu.Unlock()
			time.Sleep(leaveGrace)
			e.mu.Lock()
			if !e.running {
				e.mu.Unlock()
				return
			}
		}
	}
	e.running = false
	if e.cancel != nil {
		e.cancel()
	}
	e.helloGen++
	e.reapGen++
	if e.helloTimer != nil {
		e.helloTimer.Stop()
	}
	if e.reapTimer != nil {
		e.reapTimer.Stop()
	}
	if e.conn != nil {
		e.conn.Close()
	}
	e.stopRespondersLocked()
	e.cleanupVmacsLocked()
	e.forgetSlots()
	e.mu.Unlock()
	infof("distributed gateway/%s group=%d stopped", e.af, e.cfg.GroupID)
}

// UpdateLive applies the settings that do not need an engine restart.
func (e *Engine) UpdateLive(n *GroupConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	timing := e.cfg.HelloMS != n.HelloMS || e.cfg.HoldMS != n.HoldMS
	e.cfg.Priority, e.cfg.LBMethod, e.cfg.Weight = n.Priority, n.LBMethod, n.Weight
	e.cfg.HelloMS, e.cfg.HoldMS = n.HelloMS, n.HoldMS
	e.cfg.Preempt, e.cfg.MaxAFNs = n.Preempt, n.MaxAFNs
	if timing && e.running && e.helloTimer != nil {
		e.scheduleHelloLocked()
	}
}

// ── Sockets ──────────────────────────────────────────────────────────────────

func (e *Engine) bind() (net.PacketConn, error) {
	iface := e.cfg.Interface
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, err
	}
	ctrl := func(multicast bool) func(network, address string, c syscall.RawConn) error {
		return func(network, address string, c syscall.RawConn) error {
			var serr error
			err := c.Control(func(fd uintptr) {
				f := int(fd)
				set := func(e error) {
					if serr == nil {
						serr = e
					}
				}
				set(syscall.SetsockoptInt(f, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1))
				set(syscall.SetsockoptInt(f, syscall.SOL_SOCKET, soReusePort, 1))
				if !multicast {
					set(syscall.SetsockoptString(f, syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, iface))
					return
				}
				if e.af == afIPv4 {
					set(syscall.SetsockoptInt(f, syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, 1))
					set(syscall.SetsockoptInt(f, syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, 0))
					group := netip.MustParseAddr(multicast4).As4()
					set(syscall.SetsockoptIPMreqn(f, syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP,
						&syscall.IPMreqn{Multiaddr: group, Ifindex: int32(ifc.Index)}))
					set(syscall.SetsockoptIPMreqn(f, syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF,
						&syscall.IPMreqn{Ifindex: int32(ifc.Index)}))
				} else {
					set(syscall.SetsockoptInt(f, syscall.IPPROTO_IPV6, syscall.IPV6_MULTICAST_HOPS, 1))
					set(syscall.SetsockoptInt(f, syscall.IPPROTO_IPV6, syscall.IPV6_MULTICAST_LOOP, 0))
					group := netip.MustParseAddr(multicast6).As16()
					set(syscall.SetsockoptIPv6Mreq(f, syscall.IPPROTO_IPV6, syscall.IPV6_JOIN_GROUP,
						&syscall.IPv6Mreq{Multiaddr: group, Interface: uint32(ifc.Index)}))
					set(syscall.SetsockoptInt(f, syscall.IPPROTO_IPV6, syscall.IPV6_MULTICAST_IF, ifc.Index))
				}
			})
			if err != nil {
				return err
			}
			return serr
		}
	}

	ctx := context.Background()
	switch {
	case e.cfg.unicastMode():
		// Plain UDP on the protocol port; hellos go to each neighbour.  Works
		// across routed/VNI boundaries.  SO_BINDTODEVICE keeps VRF routing.
		lc := net.ListenConfig{Control: ctrl(false)}
		conn, err := lc.ListenPacket(ctx, map[AF]string{afIPv4: "udp4", afIPv6: "udp6"}[e.af],
			map[AF]string{afIPv4: "0.0.0.0", afIPv6: "[::]"}[e.af]+portSuffix())
		if err == nil {
			infof("Unicast mode: %d neighbor(s) configured (%s)", len(e.cfg.Neighbors), e.tag())
		}
		return conn, err
	case e.af == afIPv4:
		lc := net.ListenConfig{Control: ctrl(true)}
		return lc.ListenPacket(ctx, "udp4", multicast4+portSuffix())
	default:
		lc := net.ListenConfig{Control: ctrl(true)}
		return lc.ListenPacket(ctx, "udp6", "["+multicast6+"%"+iface+"]"+portSuffix())
	}
}

func portSuffix() string { return fmt.Sprintf(":%d", dgwPort) }

func (e *Engine) readLoop(conn net.PacketConn) {
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if isClosed(err) {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		data := append([]byte(nil), buf[:n]...)
		e.mu.Lock()
		if e.running {
			e.onPacketLocked(data, addr)
		}
		e.mu.Unlock()
	}
}

func isClosed(err error) bool {
	return err != nil && (errorsIs(err, net.ErrClosed))
}

// isSelf reports whether a neighbor entry is this node's own address.
func (e *Engine) isSelf(n string) bool {
	a, err := netip.ParseAddr(n)
	return err == nil && a.Unmap().String() == e.myIP
}

// sendToLocked sends pkt to a unicast target.
func (e *Engine) sendToLocked(pkt []byte, target string) {
	if e.conn == nil {
		return
	}
	a, err := netip.ParseAddr(target)
	if err != nil {
		return
	}
	ua := &net.UDPAddr{IP: a.AsSlice(), Port: dgwPort, Zone: a.Zone()}
	if a.Is6() && a.Zone() == "" && a.IsLinkLocalUnicast() {
		ua.Zone = e.cfg.Interface
	}
	if _, err := e.conn.WriteTo(pkt, ua); err != nil {
		debugf("send to %s failed: %v", target, err)
	}
}

// broadcastLocked sends pkt to every neighbour (unicast mode) or the group.
func (e *Engine) broadcastLocked(pkt []byte) {
	if e.conn == nil {
		return
	}
	if e.cfg.unicastMode() {
		for _, n := range e.cfg.Neighbors {
			if e.isSelf(n) {
				continue // the list names every node, this one too
			}
			e.sendToLocked(pkt, n)
		}
		return
	}
	var ua *net.UDPAddr
	if e.af == afIPv4 {
		ua = &net.UDPAddr{IP: net.ParseIP(multicast4), Port: dgwPort}
	} else {
		ua = &net.UDPAddr{IP: net.ParseIP(multicast6), Port: dgwPort, Zone: e.cfg.Interface}
	}
	if _, err := e.conn.WriteTo(pkt, ua); err != nil {
		debugf("multicast send failed: %v", err)
	}
}

// ── State machine ────────────────────────────────────────────────────────────

func (e *Engine) transitionLocked(n State) {
	if n == e.state {
		return
	}
	infof("State %s → %s (%s)", e.state, n, e.tag())
	e.state = n
	switch n {
	case stateSpeak:
		e.scheduleHelloLocked()
		// If no peer announces itself within hold_ms, we win by default.
		time.AfterFunc(time.Duration(e.cfg.HoldMS)*time.Millisecond, e.onSpeakTimeout)
	case stateActive, stateForward:
		e.scheduleHelloLocked()
		e.setupVmacsLocked()
	}
}

func (e *Engine) onListenTimeout() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running && e.state == stateListen {
		e.transitionLocked(stateSpeak)
	}
}

func (e *Engine) onSpeakTimeout() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running && e.state == stateSpeak {
		infof("Speak timeout — no AGC seen, running election (%s)", e.tag())
		e.runElectionLocked()
	}
}

// ── Timers ───────────────────────────────────────────────────────────────────

func (e *Engine) scheduleHelloLocked() {
	if e.helloTimer != nil {
		e.helloTimer.Stop()
	}
	e.helloGen++
	gen := e.helloGen
	e.helloTimer = time.AfterFunc(time.Duration(e.cfg.HelloMS)*time.Millisecond, func() { e.sendHello(gen) })
}

func (e *Engine) scheduleReapLocked() {
	if e.reapTimer != nil {
		e.reapTimer.Stop()
	}
	e.reapGen++
	gen := e.reapGen
	d := time.Duration(e.cfg.HoldMS) * time.Millisecond / 2
	if d < 100*time.Millisecond {
		d = 100 * time.Millisecond
	}
	e.reapTimer = time.AfterFunc(d, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if !e.running || gen != e.reapGen {
			return
		}
		e.reapPeersLocked()
		e.publishSlotsLocked()
		e.resolveCrossFamilyLocked()
		e.reannounceLocked()
		e.scheduleReapLocked()
	})
}

func (e *Engine) sendHello(gen uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running || gen != e.helloGen || e.leaving {
		return
	}
	var flags uint8
	if e.state == stateActive {
		flags = flagController
	}
	if pkt, err := buildPacket(&e.cfg, pktHello, e.af, e.afnID, flags, e.myIP); err != nil {
		debugf("hello build error: %v", err)
	} else {
		e.broadcastLocked(pkt)
	}
	e.scheduleHelloLocked()
}

// ── Packet receive ───────────────────────────────────────────────────────────

func (e *Engine) onPacketLocked(data []byte, from net.Addr) {
	if e.leaving {
		// Going away and already said so: whatever the others do now is theirs to
		// handle.  Reacting (yielding the role, rebuilding a virtual MAC) would put
		// the MAC back on this port just before it disappears, and the network would
		// keep sending it here.
		return
	}
	pkt, err := parsePacket(data, e.cfg.keyBytes())
	if err != nil {
		debugf("Invalid/unauthenticated packet from %v", from)
		return
	}
	if pkt.GroupID != e.cfg.GroupID || pkt.AF != e.af {
		return
	}
	vip := e.cfg.vipFor(e.af)
	if i := indexByte(vip, '/'); i >= 0 {
		vip = vip[:i]
	}
	if pkt.VirtualIP != vip {
		return
	}
	sender := pkt.SenderIP
	if sender == e.myIP {
		return // own multicast echo
	}

	peer := e.peers[sender]
	if peer == nil {
		peer = &Peer{IP: sender, Priority: pkt.Priority, AfnID: pkt.AfnID,
			Weight: pkt.Weight, Preempt: pkt.Preempt, LastSeen: time.Now(), FirstSeen: time.Now()}
		e.peers[sender] = peer
		infof("New peer discovered: %s pri=%d (%s)", sender, peer.Priority, e.tag())
	} else {
		peer.Priority, peer.AfnID, peer.Weight = pkt.Priority, pkt.AfnID, pkt.Weight
		peer.LastSeen = time.Now()
	}
	if pkt.Type == pktHello {
		peer.Controller = isController(pkt)
	}

	switch pkt.Type {
	case pktHello:
		if pkt.Flags&flagLeaving != 0 {
			// the sender is stopping: let the reap below treat it as gone now
			if p := e.peers[sender]; p != nil && sender != e.agcIP {
				p.LastSeen = time.Now().Add(-time.Hour)
				infof("Peer %s is leaving (%s)", sender, e.tag())
			}
			break
		}
		e.handleHelloLocked(sender, pkt)
	case pktCoup:
		e.handleCoupLocked(sender, pkt)
	case pktResign:
		e.handleResignLocked(sender, pkt)
	}
	e.reapPeersLocked()
	e.publishSlotsLocked()
	e.resolveCrossFamilyLocked()
}

func (e *Engine) handleHelloLocked(sender string, pkt *Packet) {
	switch e.state {
	case stateSpeak:
		// If there is already an active AGC on the network (afn_id=1), join as
		// an AFN regardless of our own priority.
		if isController(pkt) {
			e.agcIP = sender
			infof("Existing AGC detected at %s — joining as AFN (%s)", sender, e.tag())
		}
		e.runElectionLocked()
	case stateActive:
		switch {
		case e.peerBeatsUs(pkt) && yieldsTo(pkt) && isController(pkt) && time.Now().Before(e.assertUntil):
			// This node asked for the role and the controller it asked has not stepped down (the request was
			// lost, or not yet read): ask it again instead of giving way, or the request would come to nothing.
			e.sendResignToLocked(sender)
		case e.peerBeatsUs(pkt) && yieldsTo(pkt):
			warnf("Higher-priority peer %s detected — yielding AGC", sender)
			e.becomeAFNLocked()
		default:
			e.assignAFNIDsLocked()
		}
	case stateListen:
		// What a node hears while it listens counts: a controller heard here is the incumbent, and the election at
		// the end of SPEAK must not ignore it (it did — only hellos heard in SPEAK were looked at, so a newcomer
		// that outranked the controller took the role from it every time).
		e.noteControllerLocked(sender, pkt)
	case stateForward, stateStandby:
		e.noteControllerLocked(sender, pkt)
		// Two forwarders on one slot are one MAC on two ports: the one that ranks lower moves (and
		// anyone yields a slot to the controller).
		if e.state == stateForward && e.afnID != 0 && pkt.AfnID == e.afnID && (isController(pkt) || e.peerBeatsUs(pkt)) {
			e.moveSlotLocked(sender)
		}
	}
}

// noteControllerLocked keeps agcIP true to what the hellos say.  A forwarder used to learn it once (or from an
// election among whoever it had heard) and never again, so after a missed hello it could follow a node that was
// not the controller, and then not notice when the real one went away.
func (e *Engine) noteControllerLocked(sender string, pkt *Packet) {
	if !isController(pkt) || e.agcIP == sender {
		return
	}
	if cur := e.peers[e.agcIP]; cur != nil && cur.Controller {
		// two controllers at once: follow the one that ranks higher; the other yields
		if cur.Priority > pkt.Priority || (cur.Priority == pkt.Priority && cur.IP > sender) {
			return
		}
	}
	infof("Controller is %s (was %q) (%s)", sender, e.agcIP, e.tag())
	e.agcIP = sender
}

// moveSlotLocked gives up a forwarder slot another node also uses and takes a free one.
func (e *Engine) moveSlotLocked(other string) {
	infof("AFN slot %d is also used by %s — moving to a free slot (%s)", e.afnID, other, e.tag())
	e.cleanupVmacsLocked()
	e.afnID = 0
	e.state = stateStandby
	e.joinAsAFNLocked()
}

func (e *Engine) handleCoupLocked(sender string, pkt *Packet) {
	if e.state == stateActive && e.peerBeatsUs(pkt) {
		infof("Coup from %s — stepping down", sender)
		e.becomeAFNLocked()
	}
}

func (e *Engine) handleResignLocked(sender string, pkt *Packet) {
	if sender == e.agcIP {
		infof("AGC %s resigned — running election", sender)
		slot := 0
		if p := e.peers[sender]; p != nil {
			slot = p.AfnID
		}
		delete(e.peers, sender)
		e.agcIP = "" // no incumbent: the best of the rest becomes the controller
		e.runElectionLocked()
		e.takeOverControllerSlotLocked(slot)
	} else if e.state == stateActive {
		// An AFN sent us a RESIGN as a directed assert-agc request: the role goes to it, whatever the ranking.  (An
		// election here would be won by this node again, as it was the highest of the group when it got the role.)
		infof("Resign request from AFN %s — stepping down from AGC in its favour (%s)", sender, e.tag())
		e.stepDownToLocked(sender)
	}
}

// ── Election ─────────────────────────────────────────────────────────────────

// yieldsTo reports whether a sitting controller gives way to a peer that outranks
// it: only a peer that is itself a controller (two at once must settle on one) or
// one that asked to preempt.  Without this a node that merely restarted — and so
// outranks the node that took over while it was away — pulled the role straight
// back on its first hello, leaving the group with no controller for a moment and
// moving every client's virtual MAC twice per restart.
func yieldsTo(pkt *Packet) bool { return pkt.Preempt || isController(pkt) }

// flagController on a hello says the sender is the active controller.  Slot 1
// alone cannot say so: a node that took over from the old controller keeps the
// slot it had (2, 3, …), and a node joining or restarting then took it for a
// forwarder, won the election on the tie-break and pulled the role back.  Older
// nodes ignore the bit.
const flagController = 0x08

// isController reports whether a hello comes from the active controller.
func isController(pkt *Packet) bool { return pkt.AfnID == 1 || pkt.Flags&flagController != 0 }

// flagLeaving on a hello says the sender is stopping: the controller covers its
// slot at once instead of waiting a hold time.  Older nodes ignore the bit.
const flagLeaving = 0x04

// peerBeatsUs: higher priority wins; ties go to the greater sender IP
// (compared as text; this is part of the protocol, so every node elects the
// same winner).
func (e *Engine) peerBeatsUs(pkt *Packet) bool {
	if pkt.Priority > e.cfg.Priority {
		return true
	}
	if pkt.Priority == e.cfg.Priority {
		return pkt.SenderIP > e.myIP
	}
	return false
}

func (e *Engine) runElectionLocked() {
	bestIP, bestPri := e.myIP, e.cfg.Priority
	for _, p := range e.peers {
		if p.Priority > bestPri || (p.Priority == bestPri && p.IP > bestIP) {
			bestIP, bestPri = p.IP, p.Priority
		}
	}
	if bestIP == e.myIP {
		if e.state == stateActive {
			return
		}
		// Only assert AGC if there is no known incumbent.  A node that comes
		// online while an AGC is present joins as an AFN regardless of
		// priority; priority only breaks ties when the group has no AGC.
		if e.agcIP != "" && e.agcIP != e.myIP {
			infof("Won priority election but AGC %s is active — joining as AFN (%s)", e.agcIP, e.tag())
			e.joinAsAFNLocked()
			return
		}
		infof("Won AGC election (pri=%d ip=%s group=%d af=%s)", e.cfg.Priority, e.myIP, e.cfg.GroupID, e.af)
		e.agcIP = e.myIP
		e.becomeAGCLocked()
		return
	}
	e.agcIP = bestIP
	e.joinAsAFNLocked()
}

// joinAsAFNLocked self-assigns an AFN slot (the AGC never sends them back)
// and moves to FORWARD so the macvlan comes up.
//
// NOTE: the "won election but an AGC exists" case must also run this logic,
// otherwise a higher-priority joiner stays in SPEAK forever.
func (e *Engine) joinAsAFNLocked() {
	if e.state == stateStandby && e.afnID != 0 && e.slotClashLocked(e.afnID) {
		// A controller that yielded still holds slot 1, which is now the new controller's (and its hellos
		// with slot 1 would make everyone think it is a controller): take a forwarder slot instead.
		infof("Giving up slot %d (%s)", e.afnID, e.tag())
		e.lingerLocked(e.afnID) // its MAC is covered by the new controller; until then this node still answers on it
		e.afnID = 0
	}
	if e.afnID == 0 {
		if slot := e.freeSlotLocked(); slot != 0 {
			e.afnID = slot
			infof("Self-assigned AFN id=%d (%s)", slot, e.tag())
		}
	}
	defer e.publishSlotsLocked()
	switch {
	case e.state == stateStandby && e.afnID != 0:
		e.transitionLocked(stateForward)
	case e.state != stateForward && e.state != stateStandby:
		if e.afnID != 0 {
			e.transitionLocked(stateForward)
		} else {
			e.transitionLocked(stateStandby)
		}
	}
}

// slotClashLocked reports whether slot cannot be a forwarder slot of ours: it is the controller's, or a peer has it.
func (e *Engine) slotClashLocked(slot int) bool {
	if slot == 1 {
		return true
	}
	for _, p := range e.peers {
		if p.AfnID == slot {
			return true
		}
	}
	return false
}

func (e *Engine) sendCoupLocked(target string) {
	if pkt, err := buildPacket(&e.cfg, pktCoup, e.af, e.afnID, 0, e.myIP); err == nil {
		e.sendToLocked(pkt, target)
		infof("Sent COUP to %s (%s)", target, e.tag())
	}
}

// sendResignToLocked asks the current AGC to step down so this node can win.
func (e *Engine) sendResignToLocked(target string) {
	if pkt, err := buildPacket(&e.cfg, pktResign, e.af, e.afnID, 0, e.myIP); err == nil {
		e.sendToLocked(pkt, target)
		infof("Sent RESIGN request to AGC %s (%s)", target, e.tag())
	}
}

// assertAGCLocked is directed failover on the target node: this node takes the controller role, whatever the two rank, and
// then tells the incumbent to step down in its favour.  (It used to run an election with no incumbent, which a node that
// ranked below the controller (equal priority, a smaller address) lost, and the controller stepped down only to win its own
// election again: the button did nothing.)
//
// The order is make before break: this node is the controller (VIP on its macvlan, ARP/NS answered, announced) before the
// incumbent is asked to step down, and the incumbent steps down in place (see leaveControllerLocked), so at every moment
// somebody answers for the VIP.  Until the incumbent has stopped saying it is the controller, this node does not give way
// to it and asks again (see handleHelloLocked).
func (e *Engine) assertAGCLocked() string {
	label := fmt.Sprintf("group %d %s", e.cfg.GroupID, e.af)
	if e.state == stateActive {
		return label + ": already AGC — no change"
	}
	old, oldSlot := e.agcIP, 0
	if p := e.peers[old]; p != nil {
		oldSlot = p.AfnID
	}
	window := 3 * time.Duration(e.cfg.HoldMS) * time.Millisecond
	if window < 3*time.Second {
		window = 3 * time.Second
	}
	e.assertUntil = time.Now().Add(window)
	e.agcIP = e.myIP
	e.becomeAGCLocked()
	if oldSlot == 1 {
		// the incumbent gives slot 1 up (it is the controller's): clients that cached its MAC keep being answered
		e.takeOverControllerSlotLocked(1)
	}
	if old != "" && old != e.myIP {
		e.sendResignToLocked(old)
	}
	return fmt.Sprintf("%s: asserting AGC (was AFN, asked %s to step down)", label, old)
}

func (e *Engine) becomeAGCLocked() {
	if e.afnID == 0 {
		e.afnID = 1
	}
	e.transitionLocked(stateActive)
	e.publishSlotsLocked()
	e.assignAFNIDsLocked()
	// The AGC is the sole ARP/NS authority for the VIP and hands out
	// per-AFN vMACs.
	if e.af == afIPv4 {
		e.startARPResponderLocked()
	} else {
		e.startNSResponderLocked()
	}
}

func (e *Engine) becomeAFNLocked() {
	e.leaveControllerLocked()
	e.runElectionLocked()
}

// leaveControllerLocked gives up the controller role IN PLACE: the ARP/NS responder stops and the VIP moves off the
// macvlan, and that is all.  The macvlans (this node's own slot and the MACs it covers for others) and the DNS proxy stay
// up: clients that cached one of those MACs are still answered while the new controller takes them over, and the DNS
// listener never closes.  Taking everything down and building it again, as this used to, left the node without its MAC and
// its DNS for a moment, which the clients saw as lost packets.  The VIP goes onto lo before it comes off the macvlan, so
// the node answers for it at every instant; the MACs it covered are released after a grace (lingerLocked).
// delVIPsLocked takes every shared address of this engine's family off the slot's macvlan.
func (e *Engine) delVIPsLocked(slot int) {
	for _, v := range e.cfg.vipsFor(e.af) {
		delVIPFn(e.cfg.GroupID, slot, v)
	}
}

func (e *Engine) leaveControllerLocked() {
	e.stopRespondersLocked()
	e.state = stateStandby
	if e.vipSlot != 0 {
		slot := e.vipSlot
		e.setupDNSLocked(true) // the VIP onto lo (and ARP not answered for it), the listener untouched
		e.delVIPsLocked(slot)
		e.vipSlot = 0
	}
	for slot := range e.takeover {
		delete(e.takeover, slot)
		e.lingerLocked(slot)
	}
}

// lingerLocked keeps the macvlan of a slot this node no longer uses up for a while (see lingerMin), then releases it, unless the node has come to use the slot again.  Both the old and the new owner of a MAC
// can answer for the VIP, so a MAC on two nodes for that long costs nothing, while a MAC on none drops packets.
func (e *Engine) lingerLocked(slot int) {
	if slot == 0 {
		return
	}
	if e.lingering == nil {
		e.lingering = map[int]bool{}
	}
	e.lingering[slot] = true
	d := 2 * time.Duration(e.cfg.HoldMS) * time.Millisecond
	if d > lingerMax {
		d = lingerMax
	}
	if d < lingerMin {
		d = lingerMin
	}
	time.AfterFunc(d, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if !e.lingering[slot] {
			return // released already (the engine stopped)
		}
		delete(e.lingering, slot)
		if e.running && slot != e.afnID && !e.takeover[slot] {
			e.releaseVmacLocked(slot)
		}
	})
}

// announceBurstLocked repeats the announcement of a MAC this node has taken over (see announceBurst).
func (e *Engine) announceBurstLocked(slot int) {
	for _, d := range announceBurst {
		time.AfterFunc(d, func() {
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.running && e.state == stateActive && e.takeover[slot] {
				announceVmacFn(e.cfg.Interface, e.cfg.GroupID, slot, e.cfg.vipFor(e.af))
			}
		})
	}
}

// stepDownToLocked is becomeAFNLocked for a hand-over to a named node: no election, the other node is the controller.
func (e *Engine) stepDownToLocked(newAGC string) {
	e.leaveControllerLocked()
	e.agcIP = newAGC
	e.joinAsAFNLocked()
}

func (e *Engine) assignAFNIDsLocked() {
	if e.state != stateActive {
		return
	}
	used := map[int]bool{e.afnID: true}
	for _, p := range e.peers {
		if p.AfnID != 0 {
			used[p.AfnID] = true
		}
	}
	// deterministic order for slot hand-out
	ips := make([]string, 0, len(e.peers))
	for ip := range e.peers {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	slot := 1
	for _, ip := range ips {
		p := e.peers[ip]
		if p.AfnID != 0 {
			continue
		}
		for used[slot] && slot <= e.cfg.MaxAFNs {
			slot++
		}
		if slot <= e.cfg.MaxAFNs {
			p.AfnID = slot
			used[slot] = true
			infof("Assigned AFN id=%d to peer %s", slot, p.IP)
		}
	}
	// Release takeover slots now covered by a live peer.
	for s := range e.takeover {
		for _, p := range e.peers {
			if p.AfnID == s && time.Since(p.FirstSeen) >= takeoverHandback {
				infof("Peer reclaimed AFN slot %d — releasing takeover MAC", s)
				e.releaseTakeoverLocked(s)
				break
			}
		}
	}
}

func (e *Engine) activeSlotsLocked() []int {
	set := map[int]bool{}
	if e.afnID != 0 {
		set[e.afnID] = true
	}
	for _, p := range e.peers {
		if p.AfnID != 0 {
			set[p.AfnID] = true
		}
	}
	out := make([]int, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Ints(out)
	return out
}

// pickAFNLocked chooses the AFN slot whose vMAC a requester is pointed at.
func (e *Engine) pickAFNLocked(requester string) int {
	slots := e.activeSlotsLocked()
	if len(slots) == 0 {
		if e.afnID != 0 {
			return e.afnID
		}
		return 1
	}
	switch e.cfg.LBMethod {
	case lbFailover:
		if !containsInt(slots, e.failoverPrimary) {
			e.failoverPrimary = slots[0]
			infof("Failover: promoted primary to AFN slot %d", e.failoverPrimary)
		}
		return e.failoverPrimary
	case lbRoundRobin:
		s := slots[e.rrCursor%len(slots)]
		e.rrCursor++
		return s
	case lbHostPinned:
		a, err := netip.ParseAddr(requester)
		if err != nil {
			return slots[0]
		}
		n := new(big.Int).SetBytes(a.WithZone("").AsSlice())
		return slots[int(new(big.Int).Mod(n, big.NewInt(int64(len(slots)))).Int64())]
	case lbWeighted:
		weights := make([]int, len(slots))
		total := 0
		for i, s := range slots {
			w := e.cfg.Weight
			if s != e.afnID {
				for _, p := range e.peers {
					if p.AfnID == s {
						w = p.Weight
						break
					}
				}
			}
			weights[i] = w
			total += w
		}
		if total == 0 {
			total = 1
		}
		e.rrCursor++
		pos, acc := e.rrCursor%total, 0
		for i, s := range slots {
			acc += weights[i]
			if pos < acc {
				return s
			}
		}
		return slots[len(slots)-1]
	}
	return slots[0]
}

// PickAFN is the responders' entry point.
func (e *Engine) PickAFN(requester string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pickAFNLocked(requester)
}

// ── Virtual MAC / VIP management ─────────────────────────────────────────────

func (e *Engine) setupVmacsLocked() {
	if e.afnID == 0 {
		return
	}
	if e.cfg.RealMACs {
		e.setupRealLocked()
		return
	}
	infof("Setting up vMAC for AFN id=%d (%s)", e.afnID, e.tag())
	// The vMAC is shared by the v4 and v6 engines of a group; addVmac is
	// idempotent (delete then add).
	ok := e.claimVmacLocked(e.afnID, claimVmacFn)
	if ok && e.af == afIPv4 {
		// Queries for the VIP arrive on this interface; without this the kernel's reverse-path check drops them.
		changed, failed := prepareV4InputFn(e.cfg.GroupID, e.afnID, e.state != stateActive, e.cfg.Interface)
		if len(changed) > 0 {
			infof("rp_filter was strict on %v: set to loose (2) so queries for the VIP are accepted (%s)", changed, e.tag())
		}
		if len(failed) > 0 {
			warnf("rp_filter is strict on %v and could not be changed: queries for the VIP may be dropped (%s)", failed, e.tag())
		}
	}
	if ok {
		// Only the AGC holds the VIP on its macvlan.  AFNs own their macvlan
		// but clients are steered to it by the AGC's ARP/NS responder.
		if e.state == stateActive {
			for _, vip := range e.cfg.vipsFor(e.af) {
				addVIP(e.cfg.GroupID, e.afnID, vip)
				if e.af == afIPv4 {
					sendGratuitousARP(e.cfg.Interface, e.cfg.GroupID, e.afnID, vip)
				} else {
					sendUnsolicitedNA(e.cfg.Interface, e.cfg.GroupID, e.afnID, vip)
				}
			}
			e.vipSlot = e.afnID
		} else {
			infof("AFN id=%d (%s): macvlan up, no VIP assigned (AGC handles ARP/NS for this slot)", e.afnID, e.tag())
		}
	}
	e.setupDNSLocked(ok)
	// A forwarder tells the network its virtual MAC is here only once it can answer:
	// the switch moves the MAC to this port on the first frame, and the node that
	// was covering for it (or the old port) stops getting it from then on.
	if ok && e.state != stateActive {
		announceVmacFn(e.cfg.Interface, e.cfg.GroupID, e.afnID, e.cfg.vipFor(e.af))
		e.lastAnnounce = time.Now()
	}
}

// reannounceEvery is how often a forwarder repeats its announcement (see reannounceLocked).
var reannounceEvery = 2 * time.Second

// reannounceLocked repeats a forwarder's "my virtual MAC is on this port" frame.  A forwarder is otherwise silent
// (the controller answers ARP and the clients' traffic only flows towards it), so if the switch learned its MAC
// on another port — the controller covered the slot for a moment and its announcement came last, as happens
// when several nodes start together — nothing would correct it until the entry aged out, minutes later, and
// every query sent to that MAC went to the wrong node.  The frame is an ARP probe with sender 0.0.0.0, which no
// host takes into its cache.
func (e *Engine) reannounceLocked() {
	if e.cfg.RealMACs {
		e.resolvePeerMACsLocked() // no virtual MACs to announce; the controller keeps the nodes' real MACs up to date
		return
	}
	if time.Since(e.lastAnnounce) < reannounceEvery {
		return
	}
	switch {
	case e.state == stateForward && e.afnID != 0:
		e.lastAnnounce = time.Now()
		announceVmacFn(e.cfg.Interface, e.cfg.GroupID, e.afnID, e.cfg.vipFor(e.af))
	case e.state == stateActive && len(e.takeover) > 0:
		// The controller does the same for the slots it covers: the dead node's port (or a frame still in
		// flight from it) can leave the switch pointing at the old place, and nothing else would correct it.
		e.lastAnnounce = time.Now()
		for slot := range e.takeover {
			announceVmacFn(e.cfg.Interface, e.cfg.GroupID, slot, e.cfg.vipFor(e.af))
		}
	}
}

func (e *Engine) cleanupVmacsLocked() {
	e.teardownDNSLocked()
	if e.afnID != 0 {
		e.releaseVmacLocked(e.afnID)
	}
	for slot := range e.takeover {
		e.releaseTakeoverLocked(slot)
	}
	for slot := range e.lingering {
		delete(e.lingering, slot)
		e.releaseVmacLocked(slot)
	}
}

// takeoverAFNLocked assumes a dead peer's vMAC so clients with that MAC
// cached are not blackholed.  AGC only.
func (e *Engine) takeoverAFNLocked(dead int) {
	if e.cfg.RealMACs {
		// no MAC to take over: tell the neighbors the VIP is at this node (the ones that cached the dead node's MAC)
		if e.state == stateActive {
			e.repointVIPLocked()
		}
		return
	}
	if e.state != stateActive || e.takeover[dead] {
		return
	}
	infof("MAC takeover: assuming AFN slot %d (%s)", dead, e.tag())
	if !e.claimVmacLocked(dead, addVmacFn) {
		warnf("MAC takeover: failed to create macvlan for slot %d", dead)
		e.releaseVmacLocked(dead)
		return
	}
	e.takeover[dead] = true
	if e.af == afIPv4 {
		// Clients that cached the dead node's MAC send their queries here: without an address and a loose
		// reverse-path filter on this interface the kernel drops every one of them (see prepareV4Input).
		prepareV4InputFn(e.cfg.GroupID, dead, true, e.cfg.Interface)
	}
	announceVmacFn(e.cfg.Interface, e.cfg.GroupID, dead, e.cfg.vipFor(e.af))
	e.announceBurstLocked(dead)
	infof("MAC takeover: macvlan for slot %d up, no VIP assigned (ARP/NS responder maps VIP to this slot)", dead)
}

// takeOverControllerSlotLocked: when the controller is gone and this node has
// become the new one, it also assumes the old controller's virtual MAC — clients
// cached it for the address — exactly as it would for any dead forwarder.
func (e *Engine) takeOverControllerSlotLocked(slot int) {
	if slot != 0 && slot != e.afnID && e.state == stateActive {
		e.takeoverAFNLocked(slot)
	}
}

func (e *Engine) releaseTakeoverLocked(slot int) {
	if !e.takeover[slot] {
		return
	}
	infof("MAC takeover: releasing slot %d (%s)", slot, e.tag())
	e.releaseVmacLocked(slot)
	delete(e.takeover, slot)
}

// ── DNS proxy on the VIP ─────────────────────────────────────────────────────
//
// Every node that is ACTIVE or FORWARD terminates DNS for the VIP.  The AGC
// has the VIP on its macvlan.  An AFN is reached through its vMAC, but the
// kernel only delivers packets for addresses it owns, so an AFN adds the VIP
// to loopback as a host route and is told not to answer or advertise it in
// ARP (arp_ignore/arp_announce) — the AGC stays the only ARP authority.

// addLoVIPsLocked puts every shared address on lo (the node answers DNS for them without holding them on a macvlan); it
// returns false if one could not be added.
func (e *Engine) addLoVIPsLocked(where string) bool {
	ok := true
	for _, v := range e.cfg.vipsFor(e.af) {
		if !runCmd("ip", "addr", "replace", hostCIDR(v), "dev", "lo") {
			warnf("dns: could not add %s to lo%s (%s)", v, where, e.tag())
			ok = false
		}
	}
	return ok
}

func (e *Engine) delLoVIPsLocked() {
	for _, v := range e.cfg.vipsFor(e.af) {
		runCmd("ip", "addr", "del", hostCIDR(v), "dev", "lo")
	}
}

func (e *Engine) setupDNSLocked(macvlanUp bool) {
	vip := e.cfg.vipFor(e.af)
	if e.cfg.RealMACs {
		// no macvlan: the VIP is on lo on every node, the controller included, and the real interface is told not to
		// answer ARP for it (the controller's responder is the one that does)
		if !e.dnsLoAdded {
			if e.af == afIPv4 {
				for _, dev := range []string{e.cfg.Interface, "all"} {
					base := "/proc/sys/net/ipv4/conf/" + dev + "/"
					if !e.rememberSysctl(base+"arp_ignore", "1") || !e.rememberSysctl(base+"arp_announce", "2") {
						warnf("could not set arp_ignore/arp_announce on %s: this node may answer ARP for the VIP (%s)", dev, e.tag())
					}
				}
			}
			if e.addLoVIPsLocked("") {
				e.dnsLoAdded = true
			}
		}
	} else if e.state == stateActive {
		if e.dnsLoAdded {
			e.delLoVIPsLocked()
			e.dnsLoAdded = false
		}
	} else if macvlanUp && !e.dnsLoAdded {
		if e.af == afIPv4 {
			for _, dev := range []string{e.cfg.Interface, vmacName(e.cfg.GroupID, e.afnID), "all"} {
				base := "/proc/sys/net/ipv4/conf/" + dev + "/"
				if !ensureSysctl(base+"arp_ignore", "1") || !ensureSysctl(base+"arp_announce", "2") {
					warnf("could not set arp_ignore/arp_announce on %s: this node may answer ARP for the VIP (%s)", dev, e.tag())
				}
			}
		}
		if e.addLoVIPsLocked(" on AFN") {
			e.dnsLoAdded = true
		}
	}
	if e.dnsFE != nil {
		e.startMoreDNSLocked()
		return
	}
	addr, err := vipAddr(vip)
	if err != nil {
		return
	}
	fe := e.newDNSFrontendLocked(addr)
	if err := fe.Start(); err != nil {
		errorf("dns: cannot listen on %s: %v", fe.listenAddr(), err)
		return
	}
	e.dnsFE = fe
	e.startMoreDNSLocked()
}

func (e *Engine) newDNSFrontendLocked(addr netip.Addr) *DNSFrontend {
	dc := e.dnsCfg()
	fe := NewDNSFrontend(addr, dc.ListenPort, e.pool)
	fe.gw = srvhist.series(gwKey(e.cfg.GroupID))
	fe.dotPort = dc.DoTPort
	fe.dohPort = dc.DoHPort
	return fe
}

// startMoreDNSLocked opens a listener for each further shared address that has none yet.
func (e *Engine) startMoreDNSLocked() {
	have := map[netip.Addr]bool{}
	for _, f := range e.dnsMore {
		have[f.addr] = true
	}
	for _, v := range e.cfg.vipsFor(e.af)[1:] {
		addr, err := vipAddr(v)
		if err != nil || have[addr] {
			continue
		}
		fe := e.newDNSFrontendLocked(addr)
		if err := fe.Start(); err != nil {
			errorf("dns: cannot listen on %s: %v", fe.listenAddr(), err)
			continue
		}
		e.dnsMore = append(e.dnsMore, fe)
	}
}

func (e *Engine) stopMoreDNSLocked(wait bool) {
	more := e.dnsMore
	e.dnsMore = nil
	for _, f := range more {
		if wait {
			f.Stop()
		} else {
			go f.Stop()
		}
	}
}

func (e *Engine) teardownDNSLocked() {
	e.stopMoreDNSLocked(false)
	if e.dnsFE != nil {
		fe := e.dnsFE
		e.dnsFE = nil
		go fe.Stop() // Stop waits for in-flight queries; never block the engine
	}
	if e.dnsLoAdded {
		e.delLoVIPsLocked()
		e.dnsLoAdded = false
	}
	e.restoreSysctls() // real-MAC mode changed the real interface's ARP settings: put them back
}

// RestartDNS re-creates the listener (listen_port changed).
func (e *Engine) RestartDNS() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running || e.dnsFE == nil {
		return
	}
	fe := e.dnsFE
	e.dnsFE = nil
	fe.Stop()
	e.stopMoreDNSLocked(true)
	e.setupDNSLocked(true)
}

// ── Peer reaping ─────────────────────────────────────────────────────────────

func (e *Engine) reapPeersLocked() {
	var dead []string
	for ip, p := range e.peers {
		if p.expired(e.cfg.HoldMS) {
			dead = append(dead, ip)
		}
	}
	sort.Strings(dead)
	for _, ip := range dead {
		p := e.peers[ip]
		delete(e.peers, ip)
		warnf("Peer %s expired (hold=%dms) — afn_id=%d lost", ip, e.cfg.HoldMS, p.AfnID)
		if ip == e.agcIP {
			warnf("AGC lost — running election")
			e.agcIP = ""
			e.runElectionLocked()
			e.takeOverControllerSlotLocked(p.AfnID)
		} else if p.AfnID != 0 && e.state == stateActive {
			infof("AFN slot %d freed", p.AfnID)
			if e.cfg.LBMethod == lbFailover && p.AfnID == e.failoverPrimary {
				if slots := e.activeSlotsLocked(); len(slots) > 0 {
					e.failoverPrimary = slots[0]
					warnf("Failover: primary AFN %d lost — cutting over to AFN slot %d", p.AfnID, e.failoverPrimary)
				} else {
					errorf("Failover: no AFN slots remaining")
				}
			}
			e.takeoverAFNLocked(p.AfnID)
		}
	}
}

// ── Status ───────────────────────────────────────────────────────────────────

type SnapshotRow struct {
	GroupID  int    `json:"group_id"`
	AF       string `json:"af"`
	PeerIP   string `json:"peer_ip"`
	Priority int    `json:"priority"`
	AfnID    int    `json:"afn_id"`
	Weight   int    `json:"weight"`
	Preempt  bool   `json:"preempt"`
	AgeMS    int64  `json:"age_ms"`
	State    string `json:"state"`
	Local    bool   `json:"local"`
	VIP4     string `json:"vip4"`
	VIP6     string `json:"vip6"`
	AGCIP    string `json:"agc_ip"`
	DNSUp    bool   `json:"dns_listening"`
	// RealMACs: the gateway runs without virtual MACs; MAC is then the node's real MAC address, when known (the
	// controller learns the others'; a forwarder knows only its own)
	RealMACs bool   `json:"real_macs,omitempty"`
	MAC      string `json:"mac,omitempty"`
}

func (e *Engine) snapshot() []SnapshotRow {
	e.mu.Lock()
	defer e.mu.Unlock()
	row := func() SnapshotRow {
		return SnapshotRow{GroupID: e.cfg.GroupID, AF: e.af.String(), VIP4: e.cfg.VIP4,
			VIP6: e.cfg.VIP6, AGCIP: e.agcIP,
			DNSUp: e.dnsFE != nil && e.dnsFE.Listening()}
	}
	local := row()
	if e.cfg.RealMACs {
		local.RealMACs = true
		if m := ifaceMACFn(e.cfg.Interface); m != ([6]byte{}) {
			local.MAC = net.HardwareAddr(m[:]).String()
		}
	}
	local.PeerIP, local.Priority, local.AfnID, local.Weight = e.myIP, e.cfg.Priority, e.afnID, e.cfg.Weight
	local.Preempt, local.State, local.Local = e.cfg.Preempt, lower(e.state.String()), true
	rows := []SnapshotRow{local}
	for ip, p := range e.peers {
		r := row()
		r.PeerIP, r.Priority, r.AfnID, r.Weight, r.Preempt = ip, p.Priority, p.AfnID, p.Weight, p.Preempt
		r.AgeMS = time.Since(p.LastSeen).Milliseconds()
		r.State = p.stateName(e.cfg.HoldMS)
		if e.cfg.RealMACs {
			r.RealMACs = true
			if en := e.peerMACs[ip]; en != nil {
				r.MAC = net.HardwareAddr(en.mac[:]).String()
			}
		}
		rows = append(rows, r)
	}
	return rows
}
