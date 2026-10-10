package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ── Minimal DNS wire helpers (just enough to probe and relay) ───────────────

var qtypes = map[string]uint16{
	"A": 1, "NS": 2, "CNAME": 5, "SOA": 6, "PTR": 12, "MX": 15, "TXT": 16,
	"AAAA": 28, "SRV": 33, "NAPTR": 35, "DS": 43, "DNSKEY": 48, "HTTPS": 65,
}

func parseQType(s string) (uint16, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return 1, nil
	}
	if t, ok := qtypes[s]; ok {
		return t, nil
	}
	if n, err := strconv.ParseUint(strings.TrimPrefix(s, "TYPE"), 10, 16); err == nil {
		return uint16(n), nil
	}
	return 0, fmt.Errorf("unknown query type %q", s)
}

// buildQuery encodes a recursion-desired query for name/type.
func buildQuery(id uint16, name, typ string) ([]byte, error) {
	qt, err := parseQType(typ)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" || len(name) > 253 {
		return nil, fmt.Errorf("invalid name %q", name)
	}
	b := make([]byte, 12, 12+len(name)+6)
	binary.BigEndian.PutUint16(b[0:], id)
	binary.BigEndian.PutUint16(b[2:], 0x0100) // RD
	binary.BigEndian.PutUint16(b[4:], 1)      // QDCOUNT
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return nil, fmt.Errorf("invalid label in %q", name)
		}
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0, byte(qt>>8), byte(qt), 0, 1) // type, class IN
	return b, nil
}

type dnsHeader struct {
	id      uint16
	qr      bool
	tc      bool
	rcode   int
	qdcount int
	ancount int
}

func parseHeader(b []byte) (dnsHeader, bool) {
	if len(b) < 12 {
		return dnsHeader{}, false
	}
	fl := binary.BigEndian.Uint16(b[2:])
	return dnsHeader{
		id:      binary.BigEndian.Uint16(b[0:]),
		qr:      fl&0x8000 != 0,
		tc:      fl&0x0200 != 0,
		rcode:   int(fl & 0x000F),
		qdcount: int(binary.BigEndian.Uint16(b[4:])),
		ancount: int(binary.BigEndian.Uint16(b[6:])),
	}, true
}

const (
	rcodeNoError  = 0
	rcodeFormErr  = 1
	rcodeServFail = 2
	rcodeRefused  = 5
)

// errorResponse builds a header+question reply with the given rcode.
// Returns nil if the query is too short to answer at all.
func errorResponse(query []byte, rcode int) []byte {
	if len(query) < 12 {
		return nil
	}
	qEnd := 12
	if binary.BigEndian.Uint16(query[4:]) >= 1 {
		i := 12
		for i < len(query) && query[i] != 0 && query[i]&0xC0 == 0 {
			i += int(query[i]) + 1
		}
		if i+5 <= len(query) && i < len(query) && query[i] == 0 {
			qEnd = i + 5
		}
	}
	resp := make([]byte, qEnd)
	copy(resp, query[:qEnd])
	rd := binary.BigEndian.Uint16(query[2:]) & 0x0100
	binary.BigEndian.PutUint16(resp[2:], 0x8000|rd|0x0080|uint16(rcode)) // QR, RD copy, RA
	qd := uint16(0)
	if qEnd > 12 {
		qd = 1
	}
	binary.BigEndian.PutUint16(resp[4:], qd)
	binary.BigEndian.PutUint16(resp[6:], 0)
	binary.BigEndian.PutUint16(resp[8:], 0)
	binary.BigEndian.PutUint16(resp[10:], 0)
	return resp
}

// ── Upstream exchange ────────────────────────────────────────────────────────

// dotRootCAs is the set of authorities a DNS-over-TLS server's certificate is checked against; nil means
// the system's. Only tests change it.
var dotRootCAs *x509.CertPool

var errNoServers = errors.New("no healthy upstream servers")

// exchange sends query to addr over UDP or TCP and returns the reply and RTT.
// A fresh connected socket per exchange gives source-port randomisation and
// kernel-level source filtering.
func exchange(ctx context.Context, addr string, query []byte, tcp bool, timeout time.Duration) ([]byte, time.Duration, error) {
	return exchangeOpt(ctx, addr, query, tcp, timeout, false)
}

// exchangeOpt is exchange with the choice of not checking a TLS server's certificate (insecure).
//
// The query goes upstream under a fresh random ID, and the answer gets the client's ID back.  A client's own ID
// must not be what an upstream sees: the client chooses it, so a client that is also the attacker would know it,
// leaving only the source port (and over a reused socket, not even a new one each time) to guess before it could
// get a forged answer accepted and cached for everyone.
func exchangeOpt(ctx context.Context, addr string, query []byte, tcp bool, timeout time.Duration, insecure bool) ([]byte, time.Duration, error) {
	if len(query) < 12 || (query[2]>>3)&0x0F != 0 {
		// not a plain query (a dynamic update, say, which may be signed over its ID and whose answer is never cached)
		return exchangeOptID(ctx, addr, query, tcp, timeout, insecure)
	}
	orig := binary.BigEndian.Uint16(query)
	q := append([]byte(nil), query...)
	binary.BigEndian.PutUint16(q, uint16(rand.Uint32()))
	resp, rtt, err := exchangeOptID(ctx, addr, q, tcp, timeout, insecure)
	if err == nil && len(resp) >= 2 {
		binary.BigEndian.PutUint16(resp, orig)
	}
	return resp, rtt, err
}

// exchangeOptID is exchangeOpt with the query sent exactly as given.
func exchangeOptID(ctx context.Context, addr string, query []byte, tcp bool, timeout time.Duration, insecure bool) ([]byte, time.Duration, error) {
	if !tcp && !strings.HasPrefix(addr, dohScheme) && !strings.HasPrefix(addr, dotScheme) {
		// plain UDP needs only a deadline: a child context per query registers with the parent (a lock shared by
		// every query in flight) and was about a tenth of the lock waiting on a busy node
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		return exchangeUDP(ctx, addr, query, time.Now().Add(timeout))
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if strings.HasPrefix(addr, dohScheme) {
		return dohExchange(ctx, addr, query, timeout, insecure)
	}
	if !tcp && !strings.HasPrefix(addr, dotScheme) {
		return exchangeUDP(ctx, addr, query, deadline)
	}
	var d net.Dialer
	network := "udp"
	if tcp {
		network = "tcp"
	}
	var conn net.Conn
	var err error
	if hp, ok := strings.CutPrefix(addr, dotScheme); ok {
		// DNS over TLS: a TCP connection, a handshake that checks the server's certificate against its
		// name (or IP), then the same length-prefixed messages as DNS over TCP.
		tcp = true
		host, _, _ := net.SplitHostPort(hp)
		tc, derr := d.DialContext(ctx, "tcp", hp)
		if derr != nil {
			return nil, 0, derr
		}
		tc.SetDeadline(deadline)
		tl := tls.Client(tc, &tls.Config{ServerName: host, RootCAs: dotRootCAs, MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure})
		if herr := tl.HandshakeContext(ctx); herr != nil {
			tc.Close()
			return nil, 0, herr
		}
		conn = tl
	} else if conn, err = d.DialContext(ctx, network, addr); err != nil {
		return nil, 0, err
	}
	defer conn.Close()
	conn.SetDeadline(deadline)
	id := binary.BigEndian.Uint16(query)

	start := time.Now()
	if tcp {
		msg := make([]byte, 2+len(query))
		binary.BigEndian.PutUint16(msg, uint16(len(query)))
		copy(msg[2:], query)
		if _, err := conn.Write(msg); err != nil {
			return nil, 0, err
		}
		var lb [2]byte
		if _, err := io.ReadFull(conn, lb[:]); err != nil {
			return nil, 0, err
		}
		resp := make([]byte, binary.BigEndian.Uint16(lb[:]))
		if _, err := io.ReadFull(conn, resp); err != nil {
			return nil, 0, err
		}
		rtt := time.Since(start)
		if h, ok := parseHeader(resp); !ok || h.id != id || !h.qr {
			return nil, 0, errors.New("malformed or mismatched response")
		}
		return resp, rtt, nil
	}

	return nil, 0, errors.New("unreachable")
}

// ── Server pool ──────────────────────────────────────────────────────────────

// Server is one upstream resolver with health and latency state.
type Server struct {
	Addr     string
	Fallback bool        // used only while no normal server is healthy
	noECS    atomic.Bool // refused or rejected a query carrying a client subnet but answers it without: not sent ECS
	queries  []DNSQuery  // what this server is probed with

	tests []TestStat // result of each query in the last probe round

	ipOnce   sync.Once // ip: the address of Addr (invalid for tls:// and https:// servers named by host)
	ip       netip.Addr
	loopSeen atomic.Int64 // when the "this server sends us its queries" warning was last logged (unix seconds)

	picked atomic.Uint64         // spread: the turn this server was last put first (0 = never)
	dh     map[string]*srvSeries // the history of each domain this server is probed with, by domKey (srvhist.go)
	hist   *srvSeries            // this server's per-minute history (srvhist.go); shared by every pool that lists the address; nil in a hand-made Server

	mu        sync.Mutex
	healthy   bool
	ewma      float64 // ms, 0 until first sample
	last      float64
	fails     int // consecutive failures
	okCount   uint64
	failCount uint64
	served    atomic.Uint64 // live queries answered (atomic: counted for every query without taking mu)
	failing   atomic.Bool   // fails > 0: the next good answer must reset it
	tick      atomic.Uint32 // live answers seen, to sample the latency of one in several
	lastErr   string
	lastProbe time.Time
}

// ServerStat is the exported snapshot of a Server.
type ServerStat struct {
	Addr         string  `json:"addr"`
	Name         string  `json:"name,omitempty"` // the name given to the server on the Topology page, if any
	Healthy      bool    `json:"healthy"`
	Fallback     bool    `json:"fallback,omitempty"` // a fallback server: in use only while every normal server is down
	Rank         int     `json:"rank"`               // 1 = fastest healthy; 0 = not eligible
	InBand       bool    `json:"in_band"`            // spread is on and the server is within the band of the fastest: it takes turns
	EWMAMS       float64 `json:"ewma_ms"`
	LastMS       float64 `json:"last_ms"`
	ConsecFails  int     `json:"consec_fails"`
	Successes    uint64  `json:"successes"`
	Failures     uint64  `json:"failures"`
	Served       uint64  `json:"served"`
	LastError    string  `json:"last_error,omitempty"`
	LastProbeAgo float64 `json:"last_probe_ago_s"`
	// Tests holds the outcome of each probe query in the latest round.
	Tests []TestStat `json:"tests"`
}

// TestStat is the latest result of one probe query against one server.
type TestStat struct {
	Name  string  `json:"name"`
	Type  string  `json:"type"`
	OK    bool    `json:"ok"`
	MS    float64 `json:"ms"`
	Error string  `json:"error,omitempty"`
}

func (s *Server) sample(rtt time.Duration, alpha float64) {
	ms := float64(rtt.Microseconds()) / 1000
	s.last = ms
	if s.ewma == 0 {
		s.ewma = ms
	} else {
		s.ewma = alpha*ms + (1-alpha)*s.ewma
	}
}

// Pool probes the configured servers with the configured queries and forwards
// traffic to the fastest eligible server first.
type Pool struct {
	cfg        DNSConfig
	servers    []*Server
	cache      *respCache               // nil when the cache is switched off; a rebuilt pool inherits the old one (cacheCarries)
	onFallback atomic.Bool              // the pool is answering from its fallback servers (for logging the change)
	rr         atomic.Uint64            // turn counter for spreading queries over the servers in the latency band
	rank       atomic.Pointer[rankSnap] // the ranking the forwarding path uses (see rankedSnap)
	rankGen    atomic.Uint64            // bumped when a server goes up or down: the snapshot is then out of date

	cancel context.CancelFunc
	wg     sync.WaitGroup

	Queries  atomic.Uint64 // client queries handled
	Answered atomic.Uint64 // answered from an upstream
	Failed   atomic.Uint64 // answered with synthesized SERVFAIL
	ECSSent  atomic.Uint64 // forwarded with a client subnet attached
	Denied   atomic.Uint64 // refused: the client is not in allowed_clients
	Limited  atomic.Uint64 // turned away: the client was over client_rate

	avoid    nameAvoid    // servers that timed out on a name lately (slowname.go)
	sorts    []sortRule   // the dns block's sortlist, parsed (sortlist.go)
	policy   []policyRule // the dns block's policy rows, parsed (policy.go)
	pidx     *policyIndex
	pidxOnce sync.Once
	logSec   atomic.Int64 // the second policy matches are being counted for, how many were logged, how many were not
	logN     atomic.Int64
	logSupp  atomic.Int64
	lim      *clientLimits // nil when no client list or rate is configured (clientlimit.go)
}

func NewPool(cfg DNSConfig) *Pool {
	p := &Pool{cfg: cfg, lim: newClientLimits(cfg), sorts: nil}
	if cfg.SortListOn {
		p.sorts = buildSortRules(cfg.SortList)
	}
	if cfg.PolicyOn {
		p.policy = buildPolicy(cfg.Policy)
	}
	if cfg.Cache {
		p.cache = newRespCache(cfg.CacheEntries, cfg.CacheMaxTTL)
	}
	for _, a := range cfg.Servers {
		qs := cfg.queriesFor(a)
		dh := make(map[string]*srvSeries, len(qs))
		for _, q := range qs {
			dh[domKey(a, q.Name, q.Type)] = srvhist.series(domKey(a, q.Name, q.Type))
		}
		p.servers = append(p.servers, &Server{Addr: a, queries: qs, dh: dh, hist: srvhist.series(a)})
	}
	for _, a := range cfg.FallbackServers {
		// a fallback is a last resort: it is never probed (the domains the normal servers are tested with may be
		// ones only they know) and always counts as up
		p.servers = append(p.servers, &Server{Addr: a, Fallback: true, healthy: true, hist: srvhist.series(a)})
	}
	return p
}

// Start launches the background probe loop (first round runs immediately).
func (p *Pool) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.ProbeNow(ctx)
		t := time.NewTicker(p.cfg.probeInterval())
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.ProbeNow(ctx)
			}
		}
	}()
}

func (p *Pool) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

// ProbeNow runs one probe round against every server concurrently.
func (p *Pool) ProbeNow(ctx context.Context) {
	var wg sync.WaitGroup
	for _, s := range p.servers {
		if s.Fallback {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.probeServer(ctx, s)
		}()
	}
	wg.Wait()
	p.noteFallback()
}

// noteFallback logs the moment the pool starts or stops answering from its fallback servers.
func (p *Pool) noteFallback() {
	use := p.UsingFallback()
	if p.onFallback.Swap(use) == use {
		return
	}
	switch {
	case use:
		warnf("dns: every configured server is down — answering from the fallback servers")
	case p.normalUp():
		infof("dns: a configured server answers again — the fallback servers are no longer used")
	default:
		warnf("dns: the fallback servers are down too — nothing can answer")
	}
}

// UsingFallback reports whether the pool is answering from its fallback servers: no normal server is
// healthy and at least one fallback is.
func (p *Pool) UsingFallback() bool {
	var fb, normal bool
	for _, s := range p.servers {
		s.mu.Lock()
		h := s.healthy
		s.mu.Unlock()
		if !h {
			continue
		}
		if s.Fallback {
			fb = true
		} else {
			normal = true
		}
	}
	return fb && !normal
}

// normalUp reports whether any normal (non-fallback) server is healthy.
func (p *Pool) normalUp() bool {
	for _, s := range p.servers {
		s.mu.Lock()
		h := s.healthy
		s.mu.Unlock()
		if h && !s.Fallback {
			return true
		}
	}
	return false
}

// nNormal is how many servers the pool has apart from the fallback ones.
func (p *Pool) nNormal() int {
	n := 0
	for _, s := range p.servers {
		if !s.Fallback {
			n++
		}
	}
	return n
}

// probeServer runs every configured query against s.  A query passes when it returns NOERROR
// with an answer.  The server passes the round unless at least down_percent of its queries
// fail (50 by default: one of two, two of four); one that passes with some queries failing is
// degraded but stays in use.  The round's latency is the mean RTT of the successful queries.
func (p *Pool) probeServer(ctx context.Context, s *Server) {
	var total time.Duration
	var okN int
	var firstErr string
	tests := make([]TestStat, 0, len(s.queries))
	for _, q := range s.queries {
		if ctx.Err() != nil {
			return
		}
		ts := TestStat{Name: q.Name, Type: q.Type}
		pkt, err := buildQuery(uint16(rand.Uint32()), q.Name, q.Type)
		if err != nil {
			firstErr = err.Error()
			ts.Error = err.Error()
			tests = append(tests, ts)
			continue
		}
		resp, rtt, err := exchangeOpt(ctx, s.Addr, pkt, false, p.cfg.probeTimeout(), p.cfg.TLSInsecure)
		if err == nil {
			h, _ := parseHeader(resp)
			switch {
			case h.rcode != rcodeNoError:
				err = fmt.Errorf("%s: rcode %d", q, h.rcode)
			case h.ancount == 0 && !h.tc:
				err = fmt.Errorf("%s: empty answer", q)
			}
		} else {
			err = fmt.Errorf("%s: %w", q, err)
		}
		if err != nil {
			if firstErr == "" {
				firstErr = err.Error()
			}
			ts.Error = err.Error()
			tests = append(tests, ts)
			s.hist.addPrFail()
			s.dh[domKey(s.Addr, q.Name, q.Type)].addPrFail()
			continue
		}
		okN++
		s.hist.addPrOK()
		s.hist.lat(rtt)
		ds := s.dh[domKey(s.Addr, q.Name, q.Type)]
		ds.addPrOK()
		ds.lat(rtt)
		total += rtt
		ts.OK, ts.MS = true, round2(float64(rtt)/float64(time.Millisecond))
		tests = append(tests, ts)
	}
	if ctx.Err() != nil {
		return
	}
	pass := serverPasses(okN, len(s.queries), p.cfg.DownPercent)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastProbe = time.Now()
	logDomainFlips(s.Addr, s.tests, tests)
	s.tests = tests
	if pass {
		s.sample(total/time.Duration(okN), p.cfg.LatencyAlpha)
		s.okCount++
		s.fails = 0
		s.failing.Store(false)
		s.lastErr = ""
		if !s.healthy {
			s.healthy = true
			p.rankGen.Add(1)
			infof("dns: server %s is UP (%.1fms)", s.Addr, s.ewma)
		}
		return
	}
	s.failCount++
	s.fails++
	s.failing.Store(true)
	s.lastErr = firstErr
	debugf("dns: probe of %s failed (%d consecutive): %s", s.Addr, s.fails, firstErr)
	if s.healthy && s.fails >= p.cfg.FailThreshold {
		s.healthy = false
		p.rankGen.Add(1)
		warnf("dns: server %s is DOWN: %s", s.Addr, firstErr)
	}
}

// serverPasses says whether a probe round with ok of n queries passing keeps the server in use: it does
// unless at least downPercent of the queries failed (and never with nothing passing).
func serverPasses(ok, n, downPercent int) bool {
	if n == 0 || ok == 0 {
		return false
	}
	return (n-ok)*100 < downPercent*n
}

// logDomainFlips logs each domain whose result on a server changed since the last
// probe round (answers now / fails now), so the log says which domain is the problem
// even while the server as a whole stays UP.  A domain's very first result is logged
// only when it fails.  A steady state logs nothing.
func logDomainFlips(addr string, prev, cur []TestStat) {
	was := make(map[string]bool, len(prev))
	for _, t := range prev {
		was[t.Name+" "+t.Type] = t.OK
	}
	for _, t := range cur {
		ok, seen := was[t.Name+" "+t.Type]
		switch {
		case t.OK && seen && !ok:
			infof("dns: server %s: domain %s (%s) answers again (%.1fms)", addr, t.Name, t.Type, t.MS)
		case !t.OK && (!seen || ok):
			warnf("dns: server %s: domain %s (%s) fails: %s", addr, t.Name, t.Type, t.Error)
		}
	}
}

// Ranked returns the eligible servers, fastest first.
func (p *Pool) Ranked() []*Server {
	type ent struct {
		s *Server
		l float64
	}
	var es, fbs []ent
	for _, s := range p.servers {
		s.mu.Lock()
		if s.healthy {
			if s.Fallback {
				fbs = append(fbs, ent{s, s.ewma})
			} else {
				es = append(es, ent{s, s.ewma})
			}
		}
		s.mu.Unlock()
	}
	if len(es) == 0 { // every normal server is down: the fallback servers take over until one is back
		es = fbs
	}
	sort.Slice(es, func(i, j int) bool {
		if es[i].l != es[j].l {
			return es[i].l < es[j].l
		}
		return es[i].s.Addr < es[j].s.Addr
	})
	out := make([]*Server, len(es))
	for i, e := range es {
		out[i] = e.s
	}
	return out
}

// rankSnap is the ranking of the eligible servers at one moment: the servers fastest first and their latencies.
type rankSnap struct {
	at   int64 // unix nanoseconds when it was made
	gen  uint64
	list []*Server
	lat  []float64
}

// rankMaxAge is how long the forwarding path reuses a ranking.  Working it out takes every server's lock, and
// doing that for every query made those locks the busiest in the process; latencies move slowly (they are
// averages), and a server going up or down bumps the generation and so is seen at once.
const rankMaxAge = 100 * time.Millisecond

func (p *Pool) rankedSnap() *rankSnap {
	now := time.Now().UnixNano()
	gen := p.rankGen.Load()
	if sn := p.rank.Load(); sn != nil && sn.gen == gen && now-sn.at < int64(rankMaxAge) {
		return sn
	}
	sn := &rankSnap{at: now, gen: gen, list: p.Ranked()}
	sn.lat = make([]float64, len(sn.list))
	for i, s := range sn.list {
		s.mu.Lock()
		sn.lat[i] = s.ewma
		s.mu.Unlock()
	}
	p.rank.Store(sn)
	return sn
}

// Candidates is the order a query tries the servers in.  By default that is Ranked (fastest first).
// With spread on, the servers whose latency is within spread_band percent of the fastest take turns
// at the front (round-robin) and the slower ones follow in rank order as fallbacks.
func (p *Pool) Candidates() []*Server {
	sn := p.rankedSnap()
	if !p.cfg.Spread || len(sn.list) < 2 {
		return sn.list
	}
	return spreadOrder(sn.list, sn.lat, p.cfg.SpreadBand, p.rr.Add(1)-1)
}

// bandCount is how many of the servers (latencies sorted fastest first) are within band percent of the first.
func bandCount(lat []float64, band int) int {
	limit := lat[0] * (1 + float64(band)/100)
	n := 1
	for n < len(lat) && lat[n] <= limit {
		n++
	}
	return n
}

// inBand is the set of servers that currently take their turn at the front: with spread on, those within the
// band of the fastest healthy one (a lone fastest server counts); empty with spread off.
func (p *Pool) inBand() map[*Server]bool {
	out := map[*Server]bool{}
	if !p.cfg.Spread {
		return out
	}
	ranked := p.Ranked()
	if len(ranked) == 0 {
		return out
	}
	lat := make([]float64, len(ranked))
	for i, s := range ranked {
		s.mu.Lock()
		lat[i] = s.ewma
		s.mu.Unlock()
	}
	for _, s := range ranked[:bandCount(lat, p.cfg.SpreadBand)] {
		out[s] = true
	}
	return out
}

// spreadOrder puts the servers of ranked (sorted fastest first, lat their latencies) that are within band
// percent of the first one at the front, the one picked longest ago first (so they take turns, and a server
// that was out of the band for a while does not get a burst when it comes back), and keeps the slower ones
// behind them in rank order.  turn is a counter that grows with every call.
func spreadOrder(ranked []*Server, lat []float64, band int, turn uint64) []*Server {
	n := bandCount(lat, band)
	if n < 2 {
		return ranked
	}
	in := append([]*Server(nil), ranked[:n]...)
	sort.Slice(in, func(i, j int) bool {
		pi, pj := in[i].picked.Load(), in[j].picked.Load()
		if pi != pj {
			return pi < pj
		}
		return in[i].Addr < in[j].Addr
	})
	in[0].picked.Store(turn + 1)
	return append(in, ranked[n:]...)
}

// noteForwardFailure counts a failed live exchange toward health.
// noteForwardFailure records a query that a server did not answer. It never takes the server out of service: a
// timeout on one name says little about the server (it may just not answer that name), so only the server's own
// probes (probeServer) mark it DOWN. The failure shows in the server's counts, history and last error.
func (p *Pool) noteForwardFailure(s *Server, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCount++
	s.failing.Store(true)
	s.hist.addFail()
	s.lastErr = "forward: " + err.Error()
}

// Forward relays a client query to the fastest eligible server, falling back
// to the next fastest on timeout/SERVFAIL/REFUSED, up to max_attempts servers.
// Live round-trip times feed the same latency estimate as the probes.
func (p *Pool) Forward(ctx context.Context, query []byte, tcp bool) ([]byte, error) {
	return p.ForwardFrom(ctx, query, tcp, netip.Addr{})
}

// ForwardFrom is Forward for a query that came from client.  With ECS enabled
// the client's network is attached for the upstream server; the option is taken
// out of the answer again, and a server that rejects it (FORMERR) is asked once
// more without it.
func (p *Pool) ForwardFrom(ctx context.Context, query []byte, tcp bool, client netip.Addr) ([]byte, error) {
	return p.forward(ctx, query, tcp, client, true)
}

// forward is ForwardFrom; count says whether the query is a client's and so goes into the pool's counters
// (the daemon's own reverse lookups do not).
func (p *Pool) forward(ctx context.Context, query []byte, tcp bool, client netip.Addr, count bool) ([]byte, error) {
	if len(query) < 12 {
		return nil, errors.New("short query")
	}
	if count {
		p.Queries.Add(1)
	}
	var qi qinfo
	parseQuestion(query, &qi)
	var ranked []*Server
	var rn *renamer
	if r := p.policyFor(client, &qi); r != nil && count && r.action != "" { // the row answers by itself
		p.Answered.Add(1)
		return r.localAnswer(ctx, p, query, &qi), nil
	} else if r != nil && count { // a policy row's servers instead of the pool's
		if len(r.servers) == 0 { // only a destination name: the pool's servers are asked for it
			ranked = p.Candidates()
		} else {
			ranked = r.order()
		}
		if r.rename { // ... and perhaps another name for them to look up (rename.go)
			var err error
			if rn, query, err = r.newRenamer(query, &qi); err != nil {
				return nil, err
			}
		}
	} else {
		ranked = p.Candidates()
	}
	ranked = dropAsker(ranked, client)
	if qi.ok && count {
		ranked = p.avoid.order(ranked, qi.name(), qi.qtype)
	}
	if len(ranked) == 0 {
		return nil, errNoServers
	}
	if len(ranked) > p.cfg.MaxAttempts {
		ranked = ranked[:p.cfg.MaxAttempts]
	}
	var lastResp []byte
	var lastErr error
	sendQ, ecs := query, ecsState{}
	if p.cfg.ECS {
		var added bool
		if sendQ, ecs, added = addECS(query, client, p.cfg.ECSPrefix4, p.cfg.ECSPrefix6, tcp); added {
			p.ECSSent.Add(1)
		}
	}
	for _, s := range ranked {
		withECS := ecs.addedECS && !s.noECS.Load()
		q := sendQ
		if !withECS {
			q = query
		}
		resp, rtt, err := exchangeOpt(ctx, s.Addr, q, tcp, p.cfg.queryTimeout(), p.cfg.TLSInsecure)
		if err == nil && withECS {
			// a server that dislikes the option (FORMERR) or refuses a query that carries one (public resolvers do
			// for a private client network) is asked again without it, and from then on is not sent it
			if h, _ := parseHeader(resp); h.rcode == rcodeFormErr || h.rcode == rcodeRefused {
				r2, rtt2, err2 := exchangeOpt(ctx, s.Addr, query, tcp, p.cfg.queryTimeout(), p.cfg.TLSInsecure)
				if err2 == nil {
					if h2, _ := parseHeader(r2); h2.rcode != rcodeFormErr && h2.rcode != rcodeRefused && !s.noECS.Swap(true) {
						warnf("dns: server %s answers %s to queries with a client subnet (ECS) and fine without: not sending it ECS any more", s.Addr, rcodeName(h.rcode))
					}
				}
				resp, rtt, err = r2, rtt2, err2
			} else {
				resp = stripECS(resp, ecs)
			}
		}
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break
			}
			p.noteForwardFailure(s, err)
			if count && ctx.Err() == nil {
				noteFwdFailure(query, client, s.Addr, tcp, err)
				if qi.ok {
					p.avoid.mark(s.Addr, qi.name(), qi.qtype)
				}
			}
			continue
		}
		// every live answer is counted, but the server's lock (shared by every query it serves) is taken only to
		// clear a failure streak or to fold in the latency of one answer in eight: at tens of thousands of answers
		// a second that sampling is as good as sampling all of them and the lock is no longer contended
		s.served.Add(1)
		if s.failing.Load() || s.tick.Add(1)&7 == 0 {
			s.mu.Lock()
			s.sample(rtt, p.cfg.LatencyAlpha)
			s.fails = 0
			s.failing.Store(false)
			s.mu.Unlock()
			s.hist.lat(rtt)
		}
		if h, _ := parseHeader(resp); h.rcode == rcodeServFail || h.rcode == rcodeRefused {
			s.hist.addFail()
			lastResp = resp // try the next server, but keep this as a last resort
			continue
		}
		s.hist.addOK()
		if count {
			p.Answered.Add(1)
		}
		return rn.back(resp)
	}
	if lastResp != nil {
		if count {
			p.Answered.Add(1)
		}
		return rn.back(lastResp)
	}
	if lastErr == nil {
		lastErr = errNoServers
	}
	return nil, lastErr
}

func (p *Pool) Snapshot() []ServerStat {
	rank := map[*Server]int{}
	for i, s := range p.Ranked() {
		rank[s] = i + 1
	}
	band := p.inBand()
	out := make([]ServerStat, 0, len(p.servers))
	for _, s := range p.servers {
		s.mu.Lock()
		st := ServerStat{
			Addr: s.Addr, Fallback: s.Fallback, Healthy: s.healthy, Rank: rank[s], InBand: band[s],
			EWMAMS: round2(s.ewma), LastMS: round2(s.last),
			ConsecFails: s.fails, Successes: s.okCount, Failures: s.failCount,
			Served: s.served.Load(), LastError: s.lastErr,
			Tests: append([]TestStat{}, s.tests...),
		}
		if !s.lastProbe.IsZero() {
			st.LastProbeAgo = round2(time.Since(s.lastProbe).Seconds())
		}
		s.mu.Unlock()
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Rank == 0) != (b.Rank == 0) {
			return a.Rank != 0
		}
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		return a.Addr < b.Addr
	})
	return out
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
