package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Dynamic DNS updates (RFC 2136).  A client that sends an UPDATE message to the VIP wants a zone
// changed.  The upstream servers of the pool are resolvers and cannot take it, so ddgw does what
// RFC 2136 section 6 describes for a server that is not the primary: it asks for the zone's SOA
// record, takes the primary server named in it (MNAME; the zone's NS records when that name does
// not resolve) and forwards the message there, unchanged, on port 53.  Unchanged matters: the
// message keeps its ID and any TSIG or SIG(0) signature, so the primary can check it as if the
// client had sent it directly.  The primary's answer goes back to the client as it came.
//
// A primary that filters by source address sees the ddgw node's address, not the client's;
// signed updates (TSIG) do not depend on that.

const (
	opcodeUpdate   = 5
	rcodeNXDomain  = 3
	rcodeNotImp    = 4
	rcodeNotAuth   = 9
	updTimeout     = 5 * time.Second
	updCacheMax    = 256
	updCacheMinTTL = 30 * time.Second
	updCacheMaxTTL = 5 * time.Minute
	updMaxTargets  = 4
	typeSOA        = 6
	typeNS         = 2
	typeA          = 1
	typeAAAA       = 28
)

// updPort is the port primaries are reached on; a variable only so tests can use another.
var updPort = "53"

var errNoZone = errors.New("no SOA record: the zone is not served by any upstream")

type updZone struct {
	mname string
	addrs []netip.Addr
	until time.Time
}

type updateState struct {
	mu       sync.Mutex
	zones    map[string]updZone
	inflight map[string]netip.Addr // (ID, zone) → the client it came from
}

// isUpdateMsg is true for a request whose opcode is UPDATE.
func isUpdateMsg(m []byte) bool {
	return len(m) >= 12 && m[2]&0x80 == 0 && (m[2]>>3)&0x0F == opcodeUpdate
}

// updateReply answers an update with an rcode: the header and zone section only, opcode UPDATE.
func updateReply(msg []byte, rcode int) []byte {
	r := errorResponse(msg, rcode)
	if r == nil {
		return nil
	}
	r[2] = r[2]&^0x78 | opcodeUpdate<<3
	r[3] &^= 0x80 // no recursion available
	return r
}

var rcodeNames = map[int]string{0: "NOERROR", 1: "FORMERR", 2: "SERVFAIL", 3: "NXDOMAIN", 4: "NOTIMP", 5: "REFUSED", 6: "YXDOMAIN", 7: "YXRRSET", 8: "NXRRSET", 9: "NOTAUTH", 10: "NOTZONE"}

func rcodeName(rc int) string {
	if n, ok := rcodeNames[rc]; ok {
		return n
	}
	return fmt.Sprintf("rcode %d", rc)
}

// readName reads a (possibly compressed) domain name at off: lower-case, no trailing dot, "." for
// the root; next is the offset just after the name where it started.
func readName(msg []byte, off int) (name string, next int, err error) {
	var labels []string
	next, hops, total := -1, 0, 0
	for {
		if off >= len(msg) {
			return "", 0, errors.New("name runs past the message")
		}
		l := int(msg[off])
		switch {
		case l == 0:
			if next < 0 {
				next = off + 1
			}
			name = strings.ToLower(strings.Join(labels, "."))
			if name == "" {
				name = "."
			}
			return name, next, nil
		case l&0xC0 == 0xC0:
			if off+1 >= len(msg) {
				return "", 0, errors.New("bad compression pointer")
			}
			if next < 0 {
				next = off + 2
			}
			if hops++; hops > 16 {
				return "", 0, errors.New("compression loop")
			}
			off = (l&0x3F)<<8 | int(msg[off+1])
		case l&0xC0 != 0:
			return "", 0, errors.New("bad label")
		default:
			if off+1+l > len(msg) {
				return "", 0, errors.New("label runs past the message")
			}
			if total += l + 1; total > 255 {
				return "", 0, errors.New("name too long")
			}
			labels = append(labels, string(msg[off+1:off+1+l]))
			off += 1 + l
		}
	}
}

type msgRR struct {
	sec    int // 0 answer, 1 authority, 2 additional
	name   string
	typ    uint16
	ttl    uint32
	off    int // start of the rdata
	length int
}

// parseRRs lists the resource records of a response (the questions are skipped).
func parseRRs(msg []byte) ([]msgRR, error) {
	if len(msg) < 12 {
		return nil, errors.New("short message")
	}
	off := 12
	for i := 0; i < int(binary.BigEndian.Uint16(msg[4:])); i++ {
		_, n, err := readName(msg, off)
		if err != nil || n+4 > len(msg) {
			return nil, errors.New("bad question")
		}
		off = n + 4
	}
	var out []msgRR
	for sec := 0; sec < 3; sec++ {
		for i := 0; i < int(binary.BigEndian.Uint16(msg[6+2*sec:])); i++ {
			name, n, err := readName(msg, off)
			if err != nil || n+10 > len(msg) {
				return out, errors.New("bad record")
			}
			rr := msgRR{sec: sec, name: name, typ: binary.BigEndian.Uint16(msg[n:]), ttl: binary.BigEndian.Uint32(msg[n+4:]), off: n + 10, length: int(binary.BigEndian.Uint16(msg[n+8:]))}
			if rr.off+rr.length > len(msg) {
				return out, errors.New("record runs past the message")
			}
			off = rr.off + rr.length
			out = append(out, rr)
		}
	}
	return out, nil
}

// lookup asks the pool a question and returns the response (an upstream error is an error).
func lookup(ctx context.Context, p *Pool, name string, qtype uint16) ([]byte, error) {
	q, err := buildQuery(uint16(rand.Uint32()), name, qtypeName(qtype))
	if err != nil {
		return nil, err
	}
	return p.Forward(ctx, q, false)
}

// findPrimary returns the primary server named in the SOA record of zone, and its TTL.
func findPrimary(ctx context.Context, p *Pool, zone string) (string, uint32, error) {
	resp, err := lookup(ctx, p, zone, typeSOA)
	if err != nil {
		return "", 0, err
	}
	if h, _ := parseHeader(resp); h.rcode == rcodeNXDomain {
		return "", 0, errNoZone
	}
	rrs, _ := parseRRs(resp)
	for _, rr := range rrs {
		if rr.typ == typeSOA && rr.sec <= 1 {
			m, _, err := readName(resp, rr.off)
			if err != nil {
				return "", 0, err
			}
			return m, rr.ttl, nil
		}
	}
	return "", 0, errNoZone
}

// addrsOf resolves a host name to its addresses (A and AAAA) through the pool.
func addrsOf(ctx context.Context, p *Pool, host string) []netip.Addr {
	var out []netip.Addr
	seen := map[netip.Addr]bool{}
	for _, t := range []uint16{typeA, typeAAAA} {
		resp, err := lookup(ctx, p, host, t)
		if err != nil {
			continue
		}
		rrs, _ := parseRRs(resp)
		for _, rr := range rrs {
			if rr.sec != 0 || rr.typ != t {
				continue
			}
			var a netip.Addr
			switch {
			case t == typeA && rr.length == 4:
				a = netip.AddrFrom4([4]byte(resp[rr.off : rr.off+4]))
			case t == typeAAAA && rr.length == 16:
				a = netip.AddrFrom16([16]byte(resp[rr.off : rr.off+16]))
			default:
				continue
			}
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out
}

// nsAddrs resolves the name servers of zone (up to three of them), the fallback when the SOA's
// primary name does not resolve.
func nsAddrs(ctx context.Context, p *Pool, zone string) []netip.Addr {
	resp, err := lookup(ctx, p, zone, typeNS)
	if err != nil {
		return nil
	}
	rrs, _ := parseRRs(resp)
	var out []netip.Addr
	n := 0
	for _, rr := range rrs {
		if rr.sec != 0 || rr.typ != typeNS || n >= 3 {
			continue
		}
		if host, _, err := readName(resp, rr.off); err == nil {
			n++
			out = append(out, addrsOf(ctx, p, host)...)
		}
	}
	return out
}

// primaryFor finds where to send an update for zone: the SOA's primary, cached for its TTL
// (between 30 s and 5 min).  fresh skips the cache.
func (f *DNSFrontend) primaryFor(ctx context.Context, p *Pool, zone string, fresh bool) (string, []netip.Addr, bool, error) {
	f.upd.mu.Lock()
	if z, ok := f.upd.zones[zone]; ok && !fresh && time.Now().Before(z.until) {
		f.upd.mu.Unlock()
		return z.mname, z.addrs, true, nil
	}
	f.upd.mu.Unlock()
	mname, ttl, err := findPrimary(ctx, p, zone)
	if err != nil {
		return "", nil, false, err
	}
	var addrs []netip.Addr
	if mname != "." && mname != "" {
		addrs = addrsOf(ctx, p, mname)
	}
	if len(addrs) == 0 {
		addrs = nsAddrs(ctx, p, zone)
	}
	if len(addrs) == 0 {
		return mname, nil, false, fmt.Errorf("the primary server %q of zone %s does not resolve, and neither do its name servers", mname, zone)
	}
	life := time.Duration(ttl) * time.Second
	if life < updCacheMinTTL {
		life = updCacheMinTTL
	} else if life > updCacheMaxTTL {
		life = updCacheMaxTTL
	}
	f.upd.mu.Lock()
	if f.upd.zones == nil || len(f.upd.zones) >= updCacheMax {
		f.upd.zones = map[string]updZone{}
	}
	f.upd.zones[zone] = updZone{mname: mname, addrs: addrs, until: time.Now().Add(life)}
	f.upd.mu.Unlock()
	return mname, addrs, false, nil
}

// sendUpdate forwards the message untouched to the first address that answers.
func (f *DNSFrontend) sendUpdate(ctx context.Context, msg []byte, tcp bool, addrs []netip.Addr) ([]byte, netip.Addr, error) {
	var last error
	tried := 0
	for _, a := range addrs {
		if a == f.addr { // ourselves: forwarding there would loop
			continue
		}
		if tried++; tried > updMaxTargets {
			break
		}
		hp := net.JoinHostPort(a.String(), updPort)
		resp, _, err := exchange(ctx, hp, msg, tcp, updTimeout)
		if err == nil && !tcp {
			if h, _ := parseHeader(resp); h.tc { // too big for UDP: the primary wants TCP
				resp, _, err = exchange(ctx, hp, msg, true, updTimeout)
			}
		}
		if err == nil {
			return resp, a, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("no usable address for the primary server")
	}
	return nil, netip.Addr{}, last
}

// handleUpdate serves one UPDATE message.
func (f *DNSFrontend) handleUpdate(msg []byte, tcp bool, client netip.Addr) []byte {
	p := f.pool()
	if p == nil {
		return updateReply(msg, rcodeServFail)
	}
	if !p.cfg.ForwardUpdates {
		debugf("dns: dynamic update from %s refused: forwarding updates is switched off", client)
		return updateReply(msg, rcodeRefused)
	}
	if binary.BigEndian.Uint16(msg[4:]) != 1 { // ZOCOUNT: exactly one zone
		return updateReply(msg, rcodeFormErr)
	}
	zone, n, err := readName(msg, 12)
	if err != nil || n+4 > len(msg) {
		return updateReply(msg, rcodeFormErr)
	}
	if binary.BigEndian.Uint16(msg[n:]) != typeSOA {
		return updateReply(msg, rcodeFormErr)
	}
	if binary.BigEndian.Uint16(msg[n+2:]) != 1 {
		return updateReply(msg, rcodeNotImp) // only class IN
	}
	key := fmt.Sprintf("%d|%s", binary.BigEndian.Uint16(msg), zone)
	f.upd.mu.Lock()
	if f.upd.inflight == nil {
		f.upd.inflight = map[string]netip.Addr{}
	}
	if from, busy := f.upd.inflight[key]; busy {
		f.upd.mu.Unlock()
		if from == client { // the client's own retransmission: the first copy is being handled, say nothing
			debugf("dns: duplicate dynamic update for zone %s from %s ignored", zone, client)
			return nil
		}
		warnf("dns: dynamic update for zone %s from %s has the ID of one already being forwarded for %s (it came back to this node: does the primary resolve to a ddgw address?); refused", zone, client, from)
		return updateReply(msg, rcodeRefused)
	}
	f.upd.inflight[key] = client
	f.upd.mu.Unlock()
	defer func() { f.upd.mu.Lock(); delete(f.upd.inflight, key); f.upd.mu.Unlock() }()

	var primary, primaryAddr string // filled in once the primary is known, for the list of recent updates
	finish := func(resp []byte, rc int, how string) []byte {
		if f.ctx.Err() == nil {
			qstats.Record(client, zone, qtUpdate, tcp, rc)
			u := UpdateLog{At: time.Now().Unix(), Client: client.Unmap().String(), Zone: zone, Primary: primary, Addr: primaryAddr, Result: rcodeName(rc), OK: rc == rcodeNoError, TCP: tcp}
			if primaryAddr == "" {
				u.Note = how
			}
			u.Changes, u.More = updateChanges(msg)
			updlog.add(u)
		}
		if rc == rcodeNoError {
			infof("dns: dynamic update for zone %s from %s %s: %s", zone, client, how, rcodeName(rc))
		} else {
			warnf("dns: dynamic update for zone %s from %s %s: %s", zone, client, how, rcodeName(rc))
		}
		return resp
	}
	for attempt := 0; ; attempt++ {
		mname, addrs, cached, err := f.primaryFor(f.ctx, p, zone, attempt > 0)
		if err != nil {
			if errors.Is(err, errNoZone) {
				return finish(updateReply(msg, rcodeNotAuth), rcodeNotAuth, "not forwarded (no SOA found, so no primary to send it to)")
			}
			return finish(updateReply(msg, rcodeServFail), rcodeServFail, "not forwarded ("+err.Error()+")")
		}
		primary, primaryAddr = mname, ""
		resp, to, err := f.sendUpdate(f.ctx, msg, tcp, addrs)
		if err == nil {
			primaryAddr = to.String()
			rc := rcodeServFail
			if h, ok := parseHeader(resp); ok {
				rc = h.rcode
			}
			return finish(resp, rc, fmt.Sprintf("forwarded to the primary %s (%s)", mname, to))
		}
		if cached && attempt == 0 { // the cached primary may have moved: look it up again once
			continue
		}
		return finish(updateReply(msg, rcodeServFail), rcodeServFail, fmt.Sprintf("not delivered to the primary %s (%v)", mname, err))
	}
}
