package main

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client rate limiting and the allowed-clients list (per gateway pool, from the dns block):
//
//	allowed_clients  networks that may use the proxy; empty = everyone. Others are answered REFUSED.
//	client_rate      queries per second one client may send; 0 = no limit.
//	client_burst     how many it may send at once before the rate applies; 0 = twice the rate, at least 10.
//	client_action    what a client over its rate gets: drop (default; nothing is sent, safest against spoofed
//	                 sources), truncate (UDP only: a short answer with the TC bit that makes a real client retry
//	                 over TCP) or refused. Over TCP, DoT and DoH the address is real, so REFUSED is answered.
//	client_exempt    networks that are never limited (still subject to allowed_clients).
//	client_limits    "NETWORK RATE" lines: that client or network has its own rate (queries per second, its burst
//	                 twice the rate, at least 10) instead of client_rate, and is limited even when client_rate is 0 or
//	                 the network is in client_exempt. The first line that contains the client applies.
//
// The node itself (loopback) is always allowed and never limited. A client is one IPv4 address or one IPv6 /64.
// The counters are per node: with several nodes answering, a client may reach each of them in turn.

const (
	actDrop     = "drop"
	actTruncate = "truncate"
	actRefused  = "refused"

	limShards      = 16
	limShardMax    = 8192 // buckets kept per shard; a flood of new addresses cannot grow memory past this
	limLogInterval = time.Minute
)

type admitVerdict int

const (
	admitOK admitVerdict = iota
	admitDenied
	admitLimited
)

type bucket struct {
	tokens float64
	last   time.Time
	rate   float64 // what this client refills at: the pool's rate, or its own from client_limits
	burst  float64
}

// limOverride is one client_limits line.
type limOverride struct {
	net         netip.Prefix
	rate, burst float64
}

type limShard struct {
	mu sync.Mutex
	m  map[netip.Addr]*bucket
}

type clientLimits struct {
	allow  []netip.Prefix
	exempt []netip.Prefix
	over   []limOverride
	rate   float64
	burst  float64
	action string
	shards [limShards]limShard

	logMu   sync.Mutex
	lastLog time.Time
	pending int
	lastWho netip.Addr
	lastWhy string
}

// parseClientNets turns "10.0.0.0/8", "192.168.1.5" or "2001:db8::/32" into canonical prefixes.
func parseClientNets(list []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, unmapPrefix(p.Masked()))
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("%q is not an address or a network like 10.0.0.0/8", s)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// unmapPrefix turns ::ffff:a.b.c.d/N into a.b.c.d/(N-96) so it matches the unmapped client addresses.
func unmapPrefix(p netip.Prefix) netip.Prefix {
	if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
		return netip.PrefixFrom(a.Unmap(), p.Bits()-96)
	}
	return p
}

func effectiveBurst(rate, burst int) float64 {
	if burst > 0 {
		return float64(burst)
	}
	b := float64(rate) * 2
	if b < 10 {
		b = 10
	}
	return b
}

// parseClientLimits reads client_limits lines ("192.168.1.5 50", "10.2.0.0/16 200").
func parseClientLimits(list []string) ([]limOverride, error) {
	out := make([]limOverride, 0, len(list))
	for _, line := range list {
		f := strings.Fields(line)
		if len(f) != 2 {
			return nil, fmt.Errorf("%q: write an address or network, then queries per second (192.168.1.5 50)", line)
		}
		nets, err := parseClientNets(f[:1])
		if err != nil {
			return nil, err
		}
		rate, err := strconv.Atoi(f[1])
		if err != nil || rate < 1 || rate > 10_000_000 {
			return nil, fmt.Errorf("%q: the rate must be 1-10000000 queries per second", line)
		}
		out = append(out, limOverride{net: nets[0], rate: float64(rate), burst: effectiveBurst(rate, 0)})
	}
	return out, nil
}

// newClientLimits is nil when no list, rate or client limit is configured, so the common case costs nothing.
func newClientLimits(c DNSConfig) *clientLimits {
	if len(c.AllowedClients) == 0 && c.ClientRate <= 0 && len(c.ClientLimits) == 0 {
		return nil
	}
	l := &clientLimits{rate: float64(c.ClientRate), burst: effectiveBurst(c.ClientRate, c.ClientBurst), action: c.ClientAction}
	if l.action == "" {
		l.action = actDrop
	}
	l.allow, _ = parseClientNets(c.AllowedClients) // validated when the config was loaded
	l.exempt, _ = parseClientNets(c.ClientExempt)
	l.over, _ = parseClientLimits(c.ClientLimits)
	// the most specific line wins: a line for one address beats the network around it, whatever their order in the list
	sort.SliceStable(l.over, func(i, j int) bool { return l.over[i].net.Bits() > l.over[j].net.Bits() })
	for i := range l.shards {
		l.shards[i].m = map[netip.Addr]*bucket{}
	}
	return l
}

func inNets(nets []netip.Prefix, a netip.Addr) bool {
	for _, p := range nets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// clientKey is what a client is counted as: an IPv4 address, or the /64 an IPv6 address is in.
func clientKey(a netip.Addr) netip.Addr {
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.Addr()
	}
	return a
}

func shardOf(a netip.Addr) int {
	b := a.As16()
	var h uint32 = 2166136261
	for _, c := range b[8:] {
		h = (h ^ uint32(c)) * 16777619
	}
	for _, c := range b[:8] {
		h = (h ^ uint32(c)) * 16777619
	}
	return int(h % limShards)
}

// check applies the list and the rate to one query from client at time now.
func (l *clientLimits) check(client netip.Addr, now time.Time) admitVerdict {
	if l == nil || !client.IsValid() {
		return admitOK
	}
	client = client.Unmap()
	if client.IsLoopback() || client.IsUnspecified() {
		return admitOK
	}
	if len(l.allow) > 0 && !inNets(l.allow, client) {
		return admitDenied
	}
	rate, burst, own := l.rate, l.burst, false
	for i := range l.over { // a client's own limit (the most specific line, see newClientLimits) comes before the exemptions and the pool's rate
		if l.over[i].net.Contains(client) {
			rate, burst, own = l.over[i].rate, l.over[i].burst, true
			break
		}
	}
	if rate <= 0 || (!own && inNets(l.exempt, client)) {
		return admitOK
	}
	key := clientKey(client)
	sh := &l.shards[shardOf(key)]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	b := sh.m[key]
	if b == nil {
		if len(sh.m) >= limShardMax {
			l.makeRoom(sh, now)
		}
		b = &bucket{tokens: burst, last: now, rate: rate, burst: burst}
		sh.m[key] = b
	} else {
		b.rate, b.burst = rate, burst
		b.tokens += now.Sub(b.last).Seconds() * rate
		if b.tokens > burst {
			b.tokens = burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return admitOK
	}
	return admitLimited
}

// makeRoom (sh.mu held) forgets clients whose bucket has refilled completely (they lose nothing), and if that
// is not enough a few arbitrary ones, whose clients simply start with a full bucket again.
func (l *clientLimits) makeRoom(sh *limShard, now time.Time) {
	for k, b := range sh.m {
		if now.Sub(b.last) >= time.Duration(b.burst/b.rate*float64(time.Second)) {
			delete(sh.m, k)
		}
	}
	for k := range sh.m {
		if len(sh.m) < limShardMax-limShardMax/8 {
			break
		}
		delete(sh.m, k)
	}
}

// clients is how many clients are being counted right now.
func (l *clientLimits) clients() int {
	if l == nil {
		return 0
	}
	n := 0
	for i := range l.shards {
		l.shards[i].mu.Lock()
		n += len(l.shards[i].m)
		l.shards[i].mu.Unlock()
	}
	return n
}

// noteRefusal logs the first refusal at once and then at most one line a minute, however many queries were
// turned away in between.
func (l *clientLimits) noteRefusal(client netip.Addr, why string, now time.Time) {
	l.logMu.Lock()
	l.pending++
	l.lastWho, l.lastWhy = client, why
	if !l.lastLog.IsZero() && now.Sub(l.lastLog) < limLogInterval {
		l.logMu.Unlock()
		return
	}
	n, who, w := l.pending, l.lastWho, l.lastWhy
	l.pending, l.lastLog = 0, now
	l.logMu.Unlock()
	warnf("dns: %d quer%s turned away (latest: %s, from %s); further ones are counted and logged at most once a minute", n, map[bool]string{true: "y", false: "ies"}[n == 1], w, who)
}

// admit applies the pool's client rules to one query; admitOK means go on.
func (p *Pool) admit(client netip.Addr) admitVerdict {
	if p.lim == nil {
		return admitOK
	}
	now := time.Now()
	v := p.lim.check(client, now)
	switch v {
	case admitDenied:
		p.Denied.Add(1)
		p.lim.noteRefusal(client, "not in allowed_clients", now)
	case admitLimited:
		p.Limited.Add(1)
		p.lim.noteRefusal(client, "over client_rate", now)
	}
	return v
}

// turnedAway is the answer for a query admit refused: REFUSED for a client not on the list, and for one over its
// rate whatever client_action says (drop = nil, truncate = a short answer with TC) — but over a stream
// (TCP, DoT, DoH) the client's address is real and it cannot retry over TCP, so it gets REFUSED.
func (p *Pool) turnedAway(query []byte, tcp bool, v admitVerdict) []byte {
	if v == admitDenied || tcp {
		return errorResponse(query, rcodeRefused)
	}
	switch p.lim.action {
	case actTruncate:
		r := errorResponse(query, rcodeNoError)
		if len(r) >= 4 {
			r[2] |= 0x02 // TC
		}
		return r
	case actRefused:
		return errorResponse(query, rcodeRefused)
	}
	return nil
}
