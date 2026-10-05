package main

import (
	"context"
	"errors"
	"math/bits"
	"math/rand"
	"net"
	"os"
	"syscall"
	"time"
)

const (
	udpBatch      = 32  // datagrams per sendmmsg / recvmmsg call
	udpRecvStride = 512 // only the DNS header is inspected
	expireEvery   = 50 * time.Millisecond
	bufBytes      = 4 << 20
	soRcvbufForce = 33 // SO_RCVBUFFORCE
	soSndbufForce = 32 // SO_SNDBUFFORCE
)

// worker runs one benchmark worker for the configured protocol.
func (c *runCtx) worker(id int) {
	switch c.p.protocol {
	case "udp":
		c.udpWorker(id)
	case "tcp", "dot":
		c.streamWorker(id)
	case "doh":
		c.dohWorker(id)
	}
}

// udpSlot tracks one in-flight query. The DNS transaction ID is
// (generation << idxBits) | slotIndex, so a reply finds its slot in O(1) and a
// late reply to a recycled slot is recognised by its stale generation.
type udpSlot struct {
	sentNs int64
	dom    int32
	gen    uint16
	active bool
}

func (c *runCtx) udpWorker(id int) {
	l := c.newLocal()
	defer l.flush()

	raddr, err := net.ResolveUDPAddr("udp", c.hostport)
	if err != nil {
		l.note("socket", err.Error())
		return
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		l.note("socket", err.Error())
		return
	}
	defer conn.Close()
	// Wake a blocked receive when the job is stopped; the worker itself closes
	// the socket, so no other goroutine ever races a syscall with Close.
	stopWake := context.AfterFunc(c.ctx, func() { conn.SetReadDeadline(time.Unix(1, 0)) })
	defer stopWake()

	rc, err := conn.SyscallConn()
	if err != nil {
		l.note("socket", err.Error())
		return
	}
	// Large socket buffers. The FORCE variants bypass net.core.[rw]mem_max
	// when running as root; fall back to the plain setters otherwise.
	rc.Control(func(fd uintptr) {
		if syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soRcvbufForce, bufBytes) != nil {
			conn.SetReadBuffer(bufBytes)
		}
		if syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soSndbufForce, bufBytes) != nil {
			conn.SetWriteBuffer(bufBytes)
		}
	})

	p := &c.p
	window := p.window
	idxBits := bits.Len(uint(window - 1))
	ring := 1 << idxBits
	idxMask := uint16(ring - 1)
	genMask := uint16((1 << (16 - idxBits)) - 1)

	slots := make([]udpSlot, ring)
	free := make([]uint16, 0, ring)
	for i := ring - 1; i >= 0; i-- {
		free = append(free, uint16(i))
		slots[i].gen = uint16(rand.Uint32()) & genMask
	}
	inflight := 0

	// Send and receive batches. Pointers stored in Msghdr/Iovec point into
	// slices owned by this goroutine, which stay alive for the whole run.
	maxQ := 0
	for _, t := range c.tmpl {
		if len(t) > maxQ {
			maxQ = len(t)
		}
	}
	stride := (maxQ + 15) &^ 15
	sendBuf := make([]byte, udpBatch*stride)
	sendIov := make([]syscall.Iovec, udpBatch)
	sendMsg := make([]mmsghdr, udpBatch)
	sendIdx := make([]uint16, udpBatch)
	for i := range sendMsg {
		sendMsg[i].hdr.Iov = &sendIov[i]
		sendMsg[i].hdr.Iovlen = 1
		sendIov[i].Base = &sendBuf[i*stride]
	}
	recvBuf := make([]byte, udpBatch*udpRecvStride)
	recvIov := make([]syscall.Iovec, udpBatch)
	recvMsg := make([]mmsghdr, udpBatch)
	for i := range recvMsg {
		recvIov[i].Base = &recvBuf[i*udpRecvStride]
		recvIov[i].SetLen(udpRecvStride)
		recvMsg[i].hdr.Iov = &recvIov[i]
		recvMsg[i].hdr.Iovlen = 1
	}

	refused := false

	// sendBatch transmits sendMsg[:n]; it returns how many went out.
	sendBatch := func(n int) (int, error) {
		sent := 0
		var serr error
		conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		err := rc.Write(func(fd uintptr) bool {
			for sent < n {
				r, e := sendmmsg(fd, sendMsg[sent:n])
				switch e {
				case 0:
					sent += r
				case syscall.EINTR:
				case syscall.EAGAIN:
					return false
				case syscall.ECONNREFUSED:
					refused = true // pending ICMP error; the next call proceeds
				default:
					serr = e
					return true
				}
			}
			return true
		})
		if serr == nil && err != nil {
			serr = err
		}
		return sent, serr
	}

	// recvWait returns as soon as at least one datagram is queued, or at deadline.
	recvWait := func(deadline time.Time) (int, error) {
		n := 0
		var rerr error
		conn.SetReadDeadline(deadline)
		err := rc.Read(func(fd uintptr) bool {
			for {
				r, e := recvmmsg(fd, recvMsg, syscall.MSG_DONTWAIT)
				switch e {
				case 0:
					n = r
					return true
				case syscall.EINTR:
				case syscall.EAGAIN:
					return false
				case syscall.ECONNREFUSED:
					refused = true
				default:
					rerr = e
					return true
				}
			}
		})
		if rerr == nil && err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			rerr = err
		}
		return n, rerr
	}

	// Rate limiting: token bucket, refilled continuously, capped at ~20 ms of
	// traffic so a stalled worker cannot burst far above the limit.
	rate := float64(p.ratePerWorker)
	var tokens, burst float64
	if rate > 0 {
		burst = rate / 50
		if burst < 1 {
			burst = 1
		}
		if burst > float64(window) {
			burst = float64(window)
		}
		tokens = 1
	}
	lastTok := time.Now()

	nd := len(c.tmpl)
	nextDom := id
	var sent uint64
	lastExpire := c.nowNs()
	timeout := int64(queryTimeout)

	for !c.stop.Load() {
		nowT := time.Now()
		elapsed := nowT.Sub(c.start)
		timeDone := p.hasDuration && elapsed >= p.duration
		doneSending := !p.hasDuration && sent >= p.count
		if timeDone || doneSending {
			if inflight == 0 || (timeDone && elapsed >= p.duration+drainGrace) {
				break
			}
		} else {
			if rate > 0 {
				tokens += nowT.Sub(lastTok).Seconds() * rate
				if tokens > burst {
					tokens = burst
				}
				lastTok = nowT
			}
			// Fill the window.
			for {
				avail := len(free)
				if !p.hasDuration {
					if rem := p.count - sent; rem < uint64(avail) {
						avail = int(rem)
					}
				}
				if rate > 0 {
					if t := int(tokens); t < avail {
						avail = t
					}
				}
				if avail <= 0 {
					break
				}
				b := avail
				if b > udpBatch {
					b = udpBatch
				}
				sentNs := c.nowNs()
				for j := 0; j < b; j++ {
					idx := free[len(free)-1]
					free = free[:len(free)-1]
					s := &slots[idx]
					s.gen = (s.gen + 1) & genMask
					txid := s.gen<<idxBits | idx
					dom := nextDom % nd
					nextDom++
					t := c.tmpl[dom]
					buf := sendBuf[j*stride : j*stride+len(t)]
					copy(buf, t)
					buf[0], buf[1] = byte(txid>>8), byte(txid)
					sendIov[j].SetLen(len(t))
					s.sentNs, s.dom, s.active = sentNs, int32(dom), true
					sendIdx[j] = idx
				}
				n, serr := sendBatch(b)
				inflight += n
				sent += uint64(n)
				if rate > 0 {
					tokens -= float64(n)
				}
				for j := n; j < b; j++ { // never left the host: release the slot
					idx := sendIdx[j]
					slots[idx].active = false
					free = append(free, idx)
				}
				if n < b {
					if serr != nil && !errors.Is(serr, os.ErrDeadlineExceeded) {
						l.note("send", serr.Error())
					}
					break
				}
			}
		}

		// Wait for replies, but not past the moment the next token arrives.
		wait := time.Millisecond
		if rate > 0 && len(free) > 0 && !timeDone && !doneSending && tokens < 1 {
			if w := time.Duration((1 - tokens) / rate * float64(time.Second)); w < wait {
				wait = w
			}
			if wait < 50*time.Microsecond {
				wait = 50 * time.Microsecond
			}
		}
		n, rerr := recvWait(time.Now().Add(wait))
		if rerr != nil && !c.stop.Load() {
			l.note("recv", rerr.Error())
			time.Sleep(time.Millisecond)
		}
		now := c.nowNs()
		for i := 0; i < n; i++ {
			ln := int(recvMsg[i].n)
			if ln < 2 {
				continue
			}
			b := recvBuf[i*udpRecvStride:]
			if ln > udpRecvStride {
				ln = udpRecvStride
			}
			txid := uint16(b[0])<<8 | uint16(b[1])
			idx := txid & idxMask
			s := &slots[idx]
			if !s.active || s.gen != txid>>idxBits {
				continue // stray, duplicate or late reply
			}
			s.active = false
			free = append(free, idx)
			inflight--
			if slot := rcodeSlot(b[:ln]); slot == 0 {
				l.ok(now - s.sentNs)
			} else {
				l.bad(slot, c.names[s.dom])
			}
		}
		if refused {
			refused = false
			l.note("server", "connection refused")
		}

		// Expire queries that never got a reply.
		if now-lastExpire >= int64(expireEvery) {
			lastExpire = now
			for i := range slots {
				s := &slots[i]
				if s.active && now-s.sentNs > timeout {
					s.active = false
					free = append(free, uint16(i))
					inflight--
					l.fail(c.names[s.dom], "timeout")
				}
			}
		}
		if l.done >= 2048 || time.Since(l.lastFlush) >= flushInterval {
			l.flush()
		}
	}

	// A run that ended on its own (not killed) with queries still unanswered:
	// those were sent, so they count, and as failures. Dropping them would let
	// a dead server report "0 sent, 100% success".
	if !c.stop.Load() {
		for i := range slots {
			if slots[i].active {
				l.fail(c.names[slots[i].dom], "no reply before the run ended")
			}
		}
	}
}
