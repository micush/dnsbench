package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Plain-UDP exchanges with an upstream used to open a socket, connect it, wait and close it for every query,
// and to allocate a 64 KiB read buffer each time.  At tens of thousands of queries a second that was most of
// the proxy's CPU (socket and close system calls, then the garbage collector sweeping the buffers).  Sockets
// are now kept per upstream and reused, and read buffers come from a pool.

const (
	udpIdleMax   = 256              // idle sockets kept per upstream
	udpIdleLife  = 30 * time.Second // an idle socket older than this is closed instead of reused
	udpMaxLife   = 20 * time.Second // a socket is retired this long after it was opened, however busy: a busy one
	udpMaxUses   = 1024             // (or after this many exchanges) would otherwise keep one source port for ever
	udpReadBuf   = 65535
	udpSockBufSz = 1 << 20
)

type idleUDP struct {
	c    *net.UDPConn
	t    time.Time // when it was put back
	born time.Time // when it was opened
	uses int       // exchanges it has carried
}

// The idle sockets are kept in several shards, each with its own lock: with one lock, every query that
// started or finished an exchange queued behind the others (the two together were the biggest lock wait in a
// profile of a busy node).  A query takes from, and gives to, whichever shard it finds free first.
const udpShards = 16

type udpShard struct {
	mu   sync.Mutex
	idle map[string][]idleUDP
	_    [32]byte // keep neighbouring shards off one cache line
}

type udpUpstreams struct {
	n      atomic.Uint32 // where the next caller starts looking
	shards [udpShards]udpShard
}

func newUDPUpstreams() *udpUpstreams {
	u := &udpUpstreams{}
	for i := range u.shards {
		u.shards[i].idle = map[string][]idleUDP{}
	}
	return u
}

var upstreamSockets = newUDPUpstreams()

var readBufs = sync.Pool{New: func() any { b := make([]byte, udpReadBuf); return &b }}

// take returns a reused socket for addr, or nil.
func (u *udpUpstreams) take(addr string) *net.UDPConn {
	if e, ok := u.takeEntry(addr); ok {
		return e.c
	}
	return nil
}

// takeEntry is take with the socket's age and use count.  It starts at a shard of its own and moves on past any
// shard that is busy or has nothing for addr, so a socket is only dialled when none is idle anywhere.
func (u *udpUpstreams) takeEntry(addr string) (idleUDP, bool) {
	now := time.Now()
	start := u.n.Add(1)
	for i := uint32(0); i < udpShards; i++ {
		sh := &u.shards[(start+i)%udpShards]
		if !sh.mu.TryLock() {
			continue
		}
		e, ok := sh.takeLocked(addr, now)
		sh.mu.Unlock()
		if ok {
			return e, true
		}
	}
	// every shard was busy or empty: wait for the one that is ours
	sh := &u.shards[start%udpShards]
	sh.mu.Lock()
	e, ok := sh.takeLocked(addr, now)
	sh.mu.Unlock()
	return e, ok
}

func (sh *udpShard) takeLocked(addr string, now time.Time) (idleUDP, bool) {
	l := sh.idle[addr]
	for len(l) > 0 {
		e := l[len(l)-1]
		l = l[:len(l)-1]
		if now.Sub(e.t) < udpIdleLife && now.Sub(e.born) < udpMaxLife {
			sh.idle[addr] = l
			return e, true
		}
		e.c.Close()
	}
	delete(sh.idle, addr)
	return idleUDP{}, false
}

// give puts a socket that just carried a good exchange back, or closes it when the upstream already has enough.
func (u *udpUpstreams) give(addr string, c *net.UDPConn) {
	u.giveEntry(addr, idleUDP{c: c, born: time.Now()})
}

// giveEntry is give for a socket whose age and use count are known.  A socket that is too old or has carried too
// many exchanges is closed instead: its source port is then no longer something an off-path guesser can keep
// trying against, and the next exchange gets a new random one.
func (u *udpUpstreams) giveEntry(addr string, e idleUDP) {
	e.uses++
	if e.uses >= udpMaxUses || time.Since(e.born) >= udpMaxLife {
		e.c.Close()
		return
	}
	start := u.n.Add(1)
	for i := uint32(0); i < udpShards; i++ {
		sh := &u.shards[(start+i)%udpShards]
		if sh.mu.TryLock() {
			sh.giveLocked(addr, e)
			sh.mu.Unlock()
			return
		}
	}
	sh := &u.shards[start%udpShards]
	sh.mu.Lock()
	sh.giveLocked(addr, e)
	sh.mu.Unlock()
}

func (sh *udpShard) giveLocked(addr string, ne idleUDP) {
	c := ne.c
	l := sh.idle[addr]
	if len(l) >= udpIdleMax/udpShards {
		c.Close()
		return
	}
	// drop the sockets that have gone stale while we are here, so an upstream that is no longer used empties out
	if len(l) > 0 && time.Since(l[0].t) >= udpIdleLife {
		keep := l[:0]
		for _, e := range l {
			if time.Since(e.t) >= udpIdleLife {
				e.c.Close()
			} else {
				keep = append(keep, e)
			}
		}
		l = keep
	}
	ne.t = time.Now()
	sh.idle[addr] = append(l, ne)
}

func (u *udpUpstreams) closeAll() {
	for i := range u.shards {
		sh := &u.shards[i]
		sh.mu.Lock()
		for a, l := range sh.idle {
			for _, e := range l {
				e.c.Close()
			}
			delete(sh.idle, a)
		}
		sh.mu.Unlock()
	}
}

func dialUDP(ctx context.Context, addr string) (*net.UDPConn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return nil, err
	}
	uc := c.(*net.UDPConn)
	uc.SetReadBuffer(udpSockBufSz)
	return uc, nil
}

// sameQuestion reports whether response r answers the question of query q.  A reused socket can receive the
// late answer to an earlier query that carried the same ID, and that must not be taken for this one's.
func sameQuestion(q, r []byte) bool {
	end, ok := questionEnd(q)
	if !ok {
		return true
	}
	if len(r) < 12 {
		return false
	}
	if binary.BigEndian.Uint16(r[4:]) == 0 {
		return true // an answer that leaves the question out (FORMERR, a bare update acknowledgement) is still the answer
	}
	if len(r) < end || binary.BigEndian.Uint16(r[4:]) != 1 {
		return false
	}
	return bytes.EqualFold(q[12:end], r[12:end])
}

// exchangeUDP sends query to addr over UDP and waits for its answer until deadline.
func exchangeUDP(ctx context.Context, addr string, query []byte, deadline time.Time) ([]byte, time.Duration, error) {
	for attempt := 0; ; attempt++ {
		ent, reused := upstreamSockets.takeEntry(addr)
		if !reused {
			c, err := dialUDP(ctx, addr)
			if err != nil {
				return nil, 0, err
			}
			ent = idleUDP{c: c, born: time.Now()}
		}
		c := ent.c
		resp, rtt, err := udpRoundTrip(c, query, deadline)
		if err == nil {
			upstreamSockets.giveEntry(addr, ent)
			return resp, rtt, nil
		}
		c.Close()
		// a reused socket may hold an error from before (the upstream refused an earlier packet): try once more
		// on a fresh one unless this is simply the time running out
		if reused && attempt == 0 && !errors.Is(err, os.ErrDeadlineExceeded) && ctx.Err() == nil {
			continue
		}
		return nil, 0, err
	}
}

func udpRoundTrip(c *net.UDPConn, query []byte, deadline time.Time) ([]byte, time.Duration, error) {
	c.SetDeadline(deadline)
	id := binary.BigEndian.Uint16(query)
	start := time.Now()
	if _, err := c.Write(query); err != nil {
		return nil, 0, err
	}
	bp := readBufs.Get().(*[]byte)
	defer readBufs.Put(bp)
	buf := *bp
	for {
		n, err := c.Read(buf)
		if err != nil {
			return nil, 0, err
		}
		if h, ok := parseHeader(buf[:n]); ok && h.id == id && h.qr && sameQuestion(query, buf[:n]) {
			return append([]byte(nil), buf[:n]...), time.Since(start), nil
		}
		// wrong ID / not a response: keep waiting until the deadline
	}
}
