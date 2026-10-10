package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	ipFreebind   = 15   // IP_FREEBIND
	ipv6Freebind = 78   // IPV6_FREEBIND
	maxInflight  = 8192 // queries being worked on at once; a slow upstream must not make the proxy drop at 1024
	udpReaders   = 4    // goroutines reading the UDP socket
	udpWorkers   = 64   // goroutines that stay alive to answer UDP queries (see readUDP)
	tcpIdle      = 10 * time.Second

	// Stream connections (TCP and DoT) are limited in number: each costs a goroutine and whatever it is in the
	// middle of reading, and unlike UDP the client's address cannot be forged.  Over the limit the connection is
	// closed at once.  A client is one IPv4 address or one IPv6 /64; the node itself is not limited per client.
	maxStreamConns     = 8192
	maxStreamPerClient = 256
	readChunk          = 4096 // a message longer than a pooled buffer is read in pieces of this size
)

// DNSFrontend serves DNS on one VIP (UDP + TCP) and relays every query
// through the pool's fastest healthy upstream.
type DNSFrontend struct {
	addr netip.Addr
	port int
	pool func() *Pool
	gw   *srvSeries // the gateway's history (srvhist.go): client latency, errors, cache hits; nil when not set

	mu      sync.Mutex
	udp     net.PacketConn
	tcp     net.Listener
	dot     net.Listener // DNS over TLS, when dotPort is set
	dotPort int
	doh     *http.Server // DNS over HTTPS, when dohPort is set
	dohPort int
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	sem     chan struct{}

	fmu    sync.Mutex
	flight map[string]*flightCall // cache keys being fetched from an upstream right now

	smu       sync.Mutex
	streams   int                // open TCP and DoT connections
	streamsBy map[netip.Addr]int // ... by client
	refused   atomic.Uint64      // connections turned away at the limit

	upd updateState
}

func NewDNSFrontend(addr netip.Addr, port int, pool func() *Pool) *DNSFrontend {
	return &DNSFrontend{addr: addr, port: port, pool: pool}
}

func (f *DNSFrontend) listenAddr() string {
	return net.JoinHostPort(f.addr.String(), strconv.Itoa(f.port))
}

// freebind lets us bind the VIP before the kernel has it configured
// (e.g. an AFN whose address is added a moment later).
func freebind(network string, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		if network == "udp6" || network == "tcp6" {
			serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, ipv6Freebind, 1)
		} else {
			serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, ipFreebind, 1)
		}
	})
	if err != nil {
		return err
	}
	return serr
}

func (f *DNSFrontend) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.udp != nil {
		return nil
	}
	lc := net.ListenConfig{Control: freebind}
	netw := "4"
	if f.addr.Is6() {
		netw = "6"
	}
	ctx := context.Background()
	udp, err := lc.ListenPacket(ctx, "udp"+netw, f.listenAddr())
	if err != nil {
		return err
	}
	tcp, err := lc.Listen(ctx, "tcp"+netw, f.listenAddr())
	if err != nil {
		udp.Close()
		return err
	}
	f.udp, f.tcp = udp, tcp
	f.ctx, f.cancel = context.WithCancel(context.Background())
	f.sem = make(chan struct{}, maxInflight)
	f.wg.Add(2)
	go f.serveUDP(udp)
	go f.serveTCP(tcp)
	infof("dns: proxy listening on %s (udp+tcp)", f.listenAddr())
	f.startDoTLocked(lc, netw)
	f.startDoHLocked(lc, netw)
	return nil
}

func (f *DNSFrontend) Stop() {
	f.mu.Lock()
	if f.udp == nil {
		f.mu.Unlock()
		return
	}
	f.cancel()
	f.udp.Close()
	f.tcp.Close()
	if f.dot != nil {
		f.dot.Close()
		f.dot = nil
	}
	if f.doh != nil {
		f.doh.Close()
		f.doh = nil
	}
	f.udp, f.tcp = nil, nil
	f.mu.Unlock()
	// The sockets are closed and the context cancelled, so in-flight queries end on their own; but one stuck in an
	// upstream call must not hold up whoever stopped us (a gateway restart holds the engine lock around this).  After
	// dnsStopWait the stragglers are left to finish by themselves: they only touch their own, closed, sockets.
	done := make(chan struct{})
	go func() { f.wg.Wait(); close(done) }()
	select {
	case <-done:
		infof("dns: proxy on %s stopped", f.listenAddr())
	case <-time.After(dnsStopWait):
		warnf("dns: proxy on %s stopped; some queries were still running after %v and are left to finish on their own", f.listenAddr(), dnsStopWait)
	}
}

// dnsStopWait is how long Stop waits for queries that are still being answered (a variable for the test).
var dnsStopWait = 3 * time.Second

func (f *DNSFrontend) Listening() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.udp != nil
}

// resolve forwards one query; on failure it answers SERVFAIL so clients fail
// fast instead of timing out.
func (f *DNSFrontend) resolve(query []byte, tcp bool, client netip.Addr) []byte {
	// who may ask, and how often, comes first: before the cache and before dynamic updates are relayed
	if p := f.pool(); p != nil {
		if v := p.admit(client); v != admitOK {
			return p.turnedAway(query, tcp, v)
		}
	}
	if isUpdateMsg(query) {
		return f.handleUpdate(query, tcp, client)
	}
	var t0 time.Time
	timed := f.gw.sampled()
	if timed {
		t0 = time.Now()
	}
	var qi qinfo // the question, read once for the cache key and for the statistics
	parseQuestion(query, &qi)
	resp := f.resolveRaw(query, tcp, client, &qi)
	if p := f.pool(); p != nil && len(p.sorts) > 0 {
		resp = p.sortAnswer(resp, client)
	}
	if timed && f.ctx.Err() == nil {
		f.gw.lat(time.Since(t0))
	}
	if f.ctx.Err() == nil {
		if qi.ok {
			name, qt := qi.name(), qi.qtype
			rc := rcodeServFail
			if h, hok := parseHeader(resp); hok {
				rc = h.rcode
			}
			qstats.Record(client, name, qt, tcp, rc)
			if rc == rcodeServFail || rc == rcodeRefused {
				f.gw.addFail()
			} else {
				f.gw.addOK()
			}
		}
	}
	return resp
}

func (f *DNSFrontend) resolveRaw(query []byte, tcp bool, client netip.Addr, qi *qinfo) []byte {
	p := f.pool()
	if p == nil {
		return errorResponse(query, rcodeServFail)
	}
	r := p.lookup(client, qi)
	if r != nil {
		p.logPolicy(r, client, qi)
		if r.action == polPool { // matched, and the pool is to answer: no later row applies
			r = nil
		}
	}
	if r != nil && r.action != "" { // answered by the policy itself: nothing to cache or wait for
		resp, _ := p.ForwardFrom(f.ctx, query, tcp, client)
		return resp
	}
	var ckey string
	var call *flightCall
	cacheable, leader := false, false
	if c := p.cache; c != nil {
		var kbuf [384]byte
		var kb []byte
		if kb, cacheable = c.keyBytes(kbuf[:0], query, qi, tcp, client, p.cfg.ECS, p.cfg.ECSPrefix4, p.cfg.ECSPrefix6); cacheable {
			if r != nil { // answers from a policy row's servers are cached apart (policy.go)
				kb = append(append(kb, '|', '@'), r.tag...)
			}
			if r := c.getBytes(kb, query, qi.nameEnd+4); r != nil {
				c.Hits.Add(1)
				qstats.RecordCache(true)
				f.gw.addHit()
				p.Queries.Add(1)
				p.Answered.Add(1)
				return r
			}
			ckey = string(kb) // a miss: the key is kept (flight table, cache)
			// the same question already on its way to an upstream: wait for that answer instead of sending another
			call, leader = f.joinFlight(ckey)
			if !leader {
				select {
				case <-call.done:
				case <-f.ctx.Done():
				}
				if r := c.getBytes(kb, query, qi.nameEnd+4); r != nil { // served from the leader's answer: a hit
					c.Hits.Add(1)
					qstats.RecordCache(true)
					f.gw.addHit()
					p.Queries.Add(1)
					p.Answered.Add(1)
					return r
				}
				// An answer that does not go into the cache (SERVFAIL, a truncated one, a negative one without a
				// SOA) is shared all the same, and so is a failure: asking again for each waiting client
				// would multiply the load on an upstream that is already struggling.
				if f.ctx.Err() == nil {
					switch {
					case call.err != nil:
						c.Misses.Add(1)
						qstats.RecordCache(false)
						p.Queries.Add(1)
						p.Failed.Add(1)
						return errorResponse(query, rcodeServFail)
					case call.resp != nil:
						c.Misses.Add(1)
						qstats.RecordCache(false)
						p.Queries.Add(1)
						p.Answered.Add(1)
						return adaptResponse(call.resp, query, qi.nameEnd+4)
					}
				}
			} else {
				defer func() { f.endFlight(ckey, call) }()
			}
			c.Misses.Add(1)
			qstats.RecordCache(false)
		} else {
			c.Bypassed.Add(1)
		}
	}
	resp, err := p.ForwardFrom(f.ctx, query, tcp, client)
	if leader {
		call.resp, call.err = resp, err
		if err != nil {
			call.resp = nil
		}
	}
	if err == nil && cacheable {
		p.cache.put(ckey, resp)
	}
	if err != nil {
		if f.ctx.Err() == nil {
			p.Failed.Add(1)
			debugf("dns: %v — answering SERVFAIL", err)
		}
		return errorResponse(query, rcodeServFail)
	}
	return resp
}

// adaptResponse makes the answer a flight leader got fit another client's query of the same question: that
// query's ID and its own spelling of the name (0x20 randomisation), as a cached answer gets them.  resp is not
// changed: it is shared by every waiter.
func adaptResponse(resp, q []byte, qEnd int) []byte {
	out := append([]byte(nil), resp...)
	if len(out) >= 2 && len(q) >= 2 {
		copy(out[0:2], q[0:2])
	}
	if e, ok := questionEnd(out); ok && e == qEnd && qEnd <= len(q) {
		copy(out[12:qEnd], q[12:qEnd])
	}
	return out
}

// bigRcvBuf asks for a large receive buffer on the listening socket: a burst of queries that arrives while every
// reader is busy waits there instead of being dropped by the kernel (the client sees that as a timeout).  The
// FORCE variant ignores net.core.rmem_max and needs privilege, so the plain one is the fallback.
func bigRcvBuf(pc net.PacketConn) {
	uc, ok := pc.(*net.UDPConn)
	if !ok {
		return
	}
	const want = 8 << 20
	if rc, err := uc.SyscallConn(); err == nil {
		forced := false
		rc.Control(func(fd uintptr) {
			forced = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUFFORCE, want) == nil
		})
		if forced {
			return
		}
	}
	uc.SetReadBuffer(want)
}

// flightCall is one fetch from an upstream that other clients asking the same question wait for.  resp and err
// are set by the leader before done is closed, and read by the waiters only after.
type flightCall struct {
	done chan struct{}
	resp []byte // what the upstream answered, when it did
	err  error  // why it could not, when it could not
}

// joinFlight registers a fetch of key.  The first caller is the leader (it passes the call to endFlight when it
// has its answer); the others get the same call to wait on.
func (f *DNSFrontend) joinFlight(key string) (call *flightCall, leader bool) {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	if c, ok := f.flight[key]; ok {
		return c, false
	}
	if f.flight == nil {
		f.flight = map[string]*flightCall{}
	}
	c := &flightCall{done: make(chan struct{})}
	f.flight[key] = c
	return c, true
}

func (f *DNSFrontend) endFlight(key string, call *flightCall) {
	f.fmu.Lock()
	delete(f.flight, key)
	f.fmu.Unlock()
	close(call.done)
}

func (f *DNSFrontend) serveUDP(pc net.PacketConn) {
	defer f.wg.Done()
	bigRcvBuf(pc)
	rep := newUDPReplier(pc)
	defer rep.close() // after the readers are gone; the Stop that closed pc also waits for the workers
	var rw sync.WaitGroup
	// Each reader has its own small set of workers and its own hand-off channel.  One channel shared by all 64
	// workers (each also waiting on the shutdown channel) was the biggest lock wait left at 98k queries a second:
	// every hand-off and every wake-up took the same two channel locks.
	for i := 0; i < udpReaders; i++ {
		jobs := make(chan udpJob) // unbuffered: a query goes only to a worker that is idle right now
		for w := 0; w < udpWorkers/udpReaders; w++ {
			f.wg.Add(1)
			go f.udpWorker(jobs)
		}
		rw.Add(1)
		go func() {
			defer rw.Done()
			defer close(jobs) // the workers end with their reader
			f.readUDP(pc, rep, jobs)
		}()
	}
	rw.Wait()
}

type udpJob struct {
	rep    *udpReplier
	q      []byte
	bp     *[]byte // the pooled buffer q lives in, to be given back when the query is answered; nil if q is not pooled
	client net.Addr
}

// queryBufSize is the largest query kept in a pooled buffer (EDNS clients rarely send more than a kilobyte or
// two); a larger one gets a buffer of its own.
const queryBufSize = 2048

// queryBufs holds the buffers received queries are copied into: a query used to cost an allocation of its own
// for every packet.  Nothing keeps a reference to the query once resolve has returned (the statistics copy the
// name, the cache stores a copy of the answer, a flight shares the answer, not the query).
var queryBufs = sync.Pool{New: func() any { b := make([]byte, 0, queryBufSize); return &b }}

func (f *DNSFrontend) answerUDP(j udpJob) {
	if resp := f.resolve(j.q, false, addrOf(j.client)); resp != nil {
		j.rep.WriteTo(resp, j.client)
	}
	if j.bp != nil {
		queryBufs.Put(j.bp)
	}
}

// udpWorker answers queries for as long as the frontend runs.  A goroutine per query costs a new stack that has
// to grow to the depth of the resolver for every one of them (a tenth of the CPU at 90k queries a second), so a
// set of workers stays alive and keeps its stack; when they are all busy (an upstream is slow) the reader falls
// back to a goroutine per query, up to maxInflight of them.
func (f *DNSFrontend) udpWorker(jobs <-chan udpJob) {
	defer f.wg.Done()
	for j := range jobs {
		f.answerUDP(j)
	}
}

func (f *DNSFrontend) readUDP(pc net.PacketConn, rep *udpReplier, jobs chan<- udpJob) {
	buf := make([]byte, 65535)
	for {
		n, client, err := pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if n < 12 {
			continue
		}
		j := udpJob{rep: rep, client: client}
		if n <= queryBufSize {
			j.bp = queryBufs.Get().(*[]byte)
			*j.bp = append((*j.bp)[:0], buf[:n]...)
			j.q = *j.bp
		} else {
			j.q = append([]byte(nil), buf[:n]...)
		}
		select {
		case jobs <- j:
			continue
		default:
		}
		select {
		case f.sem <- struct{}{}:
		default:
			if j.bp != nil {
				queryBufs.Put(j.bp)
			}
			continue // overloaded: drop, client will retry
		}
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			defer func() { <-f.sem }()
			f.answerUDP(j)
		}()
	}
}

// streamAcquire counts a new TCP or DoT connection from client, or says it is over the limit.
func (f *DNSFrontend) streamAcquire(client netip.Addr) (key netip.Addr, ok bool) {
	key = client.Unmap()
	if key.IsValid() {
		key = clientKey(key)
	}
	f.smu.Lock()
	defer f.smu.Unlock()
	local := key.IsLoopback()
	if f.streams >= maxStreamConns || !local && f.streamsBy[key] >= maxStreamPerClient {
		return key, false
	}
	if f.streamsBy == nil {
		f.streamsBy = map[netip.Addr]int{}
	}
	f.streams++
	f.streamsBy[key]++
	return key, true
}

func (f *DNSFrontend) streamRelease(key netip.Addr) {
	f.smu.Lock()
	defer f.smu.Unlock()
	f.streams--
	if n := f.streamsBy[key] - 1; n > 0 {
		f.streamsBy[key] = n
	} else {
		delete(f.streamsBy, key)
	}
}

func (f *DNSFrontend) serveTCP(l net.Listener) {
	defer f.wg.Done()
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		key, ok := f.streamAcquire(addrOf(c.RemoteAddr()))
		if !ok {
			f.refused.Add(1)
			c.Close()
			continue
		}
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			defer f.streamRelease(key)
			f.handleTCP(c)
		}()
	}
}

// readStreamMessage reads the n bytes of a message that a stream client announced.  A message that fits a pooled
// buffer is read into one (bp is then to be given back to queryBufs); a longer one is read in pieces, so memory is
// used only for what the client has really sent, not for what it said it would: a client that announces 64 KB
// and sends nothing used to cost 64 KB for as long as it held the connection.
func readStreamMessage(c net.Conn, n int) (msg []byte, bp *[]byte, err error) {
	if n <= queryBufSize {
		bp = queryBufs.Get().(*[]byte)
		msg = (*bp)[:n]
		if _, err = io.ReadFull(c, msg); err != nil {
			queryBufs.Put(bp)
			return nil, nil, err
		}
		return msg, bp, nil
	}
	msg = make([]byte, 0, readChunk)
	for len(msg) < n {
		step := n - len(msg)
		if step > readChunk {
			step = readChunk
		}
		if cap(msg)-len(msg) < step {
			grown := make([]byte, len(msg), 2*cap(msg))
			copy(grown, msg)
			msg = grown
		}
		m, err := io.ReadFull(c, msg[len(msg):len(msg)+step])
		msg = msg[:len(msg)+m]
		if err != nil {
			return nil, nil, err
		}
	}
	return msg, nil, nil
}

// respBufs holds the buffers a stream answer (length prefix and message) is assembled in.
var respBufs = sync.Pool{New: func() any { b := make([]byte, 0, 1024); return &b }}

func (f *DNSFrontend) handleTCP(c net.Conn) {
	defer c.Close()
	stop := context.AfterFunc(f.ctx, func() { c.Close() })
	defer stop()
	for {
		c.SetReadDeadline(time.Now().Add(tcpIdle))
		var lb [2]byte
		if _, err := io.ReadFull(c, lb[:]); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint16(lb[:]))
		if n < 12 {
			return // not a DNS message
		}
		q, qbp, err := readStreamMessage(c, n)
		if err != nil {
			return
		}
		resp := f.resolve(q, true, addrOf(c.RemoteAddr()))
		if qbp != nil {
			queryBufs.Put(qbp) // nothing keeps the query once resolve has returned
		}
		if resp == nil {
			return
		}
		// the length prefix and the answer go out in one write (one TLS record for DoT), from a pooled buffer
		bp := respBufs.Get().(*[]byte)
		out := append((*bp)[:0], 0, 0)
		binary.BigEndian.PutUint16(out, uint16(len(resp)))
		out = append(out, resp...)
		c.SetWriteDeadline(time.Now().Add(tcpIdle))
		_, err = c.Write(out)
		if cap(out) <= 16<<10 { // do not keep a buffer that one huge answer grew
			*bp = out[:0]
			respBufs.Put(bp)
		}
		if err != nil {
			return
		}
	}
}

// dotCertificate hands out the certificate for DNS over TLS: the one the web GUI serves (set in main).
var dotCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)

// startDoTLocked opens the DNS-over-TLS listener (RFC 7858) when a port is set. A failure is logged and
// leaves plain DNS running. Clients are served exactly as over TCP: length-prefixed messages on one
// connection, the same idle limit.
func (f *DNSFrontend) startDoTLocked(lc net.ListenConfig, netw string) {
	if f.dotPort == 0 {
		return
	}
	addr := net.JoinHostPort(f.addr.String(), strconv.Itoa(f.dotPort))
	if dotCertificate == nil {
		errorf("dns: cannot serve DNS over TLS on %s: no certificate is available", addr)
		return
	}
	l, err := lc.Listen(context.Background(), "tcp"+netw, addr)
	if err != nil {
		errorf("dns: cannot listen for DNS over TLS on %s: %v", addr, err)
		return
	}
	f.dot = tls.NewListener(l, &tls.Config{GetCertificate: dotCertificate, MinVersion: tls.VersionTLS12, NextProtos: []string{"dot"}})
	f.wg.Add(1)
	go f.serveTCP(f.dot)
	infof("dns: DNS over TLS listening on %s", addr)
}

// dohPath is where DNS over HTTPS is served (RFC 8484's usual path).
const dohPath = "/dns-query"

// startDoHLocked opens the DNS-over-HTTPS listener when a port is set (HTTP/2 and HTTP/1.1, TLS 1.2+, the
// web GUI's certificate). A failure is logged and leaves the other listeners running.
func (f *DNSFrontend) startDoHLocked(lc net.ListenConfig, netw string) {
	if f.dohPort == 0 {
		return
	}
	addr := net.JoinHostPort(f.addr.String(), strconv.Itoa(f.dohPort))
	if dotCertificate == nil {
		errorf("dns: cannot serve DNS over HTTPS on %s: no certificate is available", addr)
		return
	}
	l, err := lc.Listen(context.Background(), "tcp"+netw, addr)
	if err != nil {
		errorf("dns: cannot listen for DNS over HTTPS on %s: %v", addr, err)
		return
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(f.serveDoH),
		ReadHeaderTimeout: tcpIdle,
		ReadTimeout:       tcpIdle,
		WriteTimeout:      tcpIdle,
		IdleTimeout:       tcpIdle,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	f.doh = srv
	tl := tls.NewListener(l, &tls.Config{GetCertificate: dotCertificate, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}})
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		srv.Serve(tl)
	}()
	infof("dns: DNS over HTTPS listening on %s%s", addr, dohPath)
}

// serveDoH answers one DNS-over-HTTPS request: a POST whose body is the DNS message, or a GET with the
// message as the base64url "dns" parameter (RFC 8484). It goes through the same path as a TCP query.
func (f *DNSFrontend) serveDoH(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != dohPath {
		http.NotFound(w, r)
		return
	}
	var q []byte
	var qbp *[]byte // the pooled buffer q lives in, when it does
	defer func() {
		if qbp != nil {
			queryBufs.Put(qbp) // nothing keeps the query once resolve has returned
		}
	}()
	switch r.Method {
	case http.MethodPost:
		if ct := strings.ToLower(r.Header.Get("Content-Type")); !strings.HasPrefix(ct, "application/dns-message") {
			http.Error(w, "Content-Type must be application/dns-message", http.StatusUnsupportedMediaType)
			return
		}
		b, bp, ok := readDoHBody(r.Body, r.ContentLength)
		if !ok {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		q, qbp = b, bp
	case http.MethodGet:
		b, bp, ok := decodeDoHParam(dohQueryParam(r.URL.RawQuery))
		if !ok {
			http.Error(w, "missing or bad dns parameter", http.StatusBadRequest)
			return
		}
		q, qbp = b, bp
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	if len(q) < 12 {
		http.Error(w, "not a DNS message", http.StatusBadRequest)
		return
	}
	if f.ctx == nil || f.ctx.Err() != nil {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	resp := f.resolve(q, true, httpClientAddr(r))
	if resp == nil {
		http.Error(w, "no answer", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/dns-message")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(resp)
}

// readDoHBody reads a DoH POST body of at most dohMaxAnswer bytes.  An ordinary query fits a pooled buffer (bp is
// then to be given back to queryBufs); io.ReadAll started from 512 bytes and grew by copying, for every request.
func readDoHBody(body io.Reader, contentLen int64) (msg []byte, bp *[]byte, ok bool) {
	if contentLen > dohMaxAnswer {
		return nil, nil, false
	}
	bp = queryBufs.Get().(*[]byte)
	buf := (*bp)[:cap(*bp)]
	n := 0
	for n < len(buf) {
		m, err := body.Read(buf[n:])
		n += m
		if err == io.EOF {
			return buf[:n], bp, true
		}
		if err != nil {
			queryBufs.Put(bp)
			return nil, nil, false
		}
	}
	// the pooled buffer is full, and there may be more
	rest, err := io.ReadAll(io.LimitReader(body, dohMaxAnswer+1-int64(n)))
	if err != nil || n+len(rest) > dohMaxAnswer {
		queryBufs.Put(bp)
		return nil, nil, false
	}
	msg = make([]byte, n, n+len(rest))
	copy(msg, buf[:n])
	msg = append(msg, rest...)
	queryBufs.Put(bp)
	return msg, nil, true
}

// dohQueryParam is the value of the first "dns" parameter of a raw query string (what r.URL.Query().Get("dns")
// returns, without building the map of every parameter).
func dohQueryParam(raw string) string {
	for raw != "" {
		var kv string
		kv, raw, _ = strings.Cut(raw, "&")
		k, v, _ := strings.Cut(kv, "=")
		if strings.ContainsAny(k, "%+") {
			k, _ = url.QueryUnescape(k)
		}
		if k != "dns" {
			continue
		}
		if strings.ContainsAny(v, "%+") {
			u, err := url.QueryUnescape(v)
			if err != nil {
				return ""
			}
			return u
		}
		return v
	}
	return ""
}

// decodeDoHParam decodes the base64url "dns" parameter of a DoH GET.  A query that fits a pooled buffer is decoded
// into one (bp is then to be given back to queryBufs).
func decodeDoHParam(s string) (msg []byte, bp *[]byte, ok bool) {
	s = strings.TrimRight(s, "=")
	if s == "" {
		return nil, nil, false
	}
	n := base64.RawURLEncoding.DecodedLen(len(s))
	if n > dohMaxAnswer {
		return nil, nil, false
	}
	var src [2736]byte // the base64 text of a full pooled buffer (2048 bytes) is 2731 characters
	if n <= queryBufSize && len(s) <= len(src) {
		copy(src[:], s)
		bp = queryBufs.Get().(*[]byte)
		buf := (*bp)[:n]
		m, err := base64.RawURLEncoding.Decode(buf, src[:len(s)])
		if err != nil || m == 0 {
			queryBufs.Put(bp)
			return nil, nil, false
		}
		return buf[:m], bp, true
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) == 0 || len(b) > dohMaxAnswer {
		return nil, nil, false
	}
	return b, nil, true
}

// httpClientAddr is the address a request came from (the zero Addr when it cannot be read).
func httpClientAddr(r *http.Request) netip.Addr {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap()
	}
	return netip.Addr{}
}
