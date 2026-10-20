package main

// Fuzz targets for the code that parses what other machines send: DNS queries and answers, the gateway packets, and
// ARP, neighbour and capture frames.  `go test` runs each on its seeds; to hunt for new failures run for a while:
//
//	go test -run xxx -fuzz FuzzDNSParsers -fuzztime 5m
//	go test -run xxx -fuzz FuzzEngineAndFrames -fuzztime 5m
//
// A failing input is saved under testdata/fuzz/ and replayed by every later `go test`; fix the bug and keep the file.
// A hang or a very slow input fails too (the 2 second check), since one packet must not stall a worker.

import (
	"crypto/hmac"
	"crypto/sha256"
	"net/netip"
	"testing"
	"time"
)

func seedMsgs() [][]byte {
	q := cacheQuery("www.example.com", 1, withOPT(1232, true))
	q2 := cacheQuery("a.b.example.com", 28, nil)
	resp := mkResp(q, 0, [][]byte{aRR("www.example.com", 300), aRR("www.example.com", 60)}, nil, nil)
	neg := mkResp(q2, 3, nil, [][]byte{soaRR("example.com", 300, 120)}, nil)
	return [][]byte{q, q2, resp, neg, {}, make([]byte, 12)}
}

func FuzzDNSParsers(f *testing.F) {
	for _, s := range seedMsgs() {
		f.Add(s, s)
	}
	cl := netip.MustParseAddr("198.51.100.7")
	cl6 := netip.MustParseAddr("2001:db8::1")
	p := &Pool{sorts: buildSortRules([]string{"10.0.0.0/8: 10.0.0.0/8", "192.0.2.0/24"})}
	f.Fuzz(func(t *testing.T, a, b []byte) {
		t0 := time.Now()
		defer func() {
			if d := time.Since(t0); d > 2*time.Second {
				t.Fatalf("slow input: %v len(a)=%d len(b)=%d", d, len(a), len(b))
			}
		}()
		var qi qinfo
		parseQuestion(a, &qi)
		if qi.ok {
			_ = qi.name()
		}
		parseHeader(a)
		questionEnd(a)
		skipName(a, 12)
		recordsAfterQuestion(a)
		readName(a, 12)
		parseRRs(a)
		isUpdateMsg(a)
		if isUpdateMsg(a) {
			updateReply(a, rcodeRefused)
			updateChanges(a)
		}
		errorResponse(a, rcodeServFail)
		negativeAnswer(a, rcodeNXDomain, 60)
		if e, ok := questionEnd(a); ok {
			adaptResponse(b, a, e)
		}
		adaptResponse(b, a, len(a))
		for _, c := range []netip.Addr{cl, cl6} {
			if out, st, ok := addECS(a, c, 24, 56, false); ok {
				stripECS(out, st)
			}
			if out, st, ok := addECS(a, c, 24, 56, true); ok {
				stripECS(out, st)
			}
		}
		stripECS(b, ecsState{addedECS: true})
		stripECS(b, ecsState{addedECS: true, addedOPT: true})
		c := newRespCache(10, 3600)
		for _, tcp := range []bool{false, true} {
			var qi2 qinfo
			parseQuestion(a, &qi2)
			var kb [384]byte
			if k, ok := c.keyBytes(kb[:0], a, &qi2, tcp, cl, true, 24, 56); ok {
				c.put(string(k), b)
				c.getBytes(k, a, qi2.nameEnd+4)
				c.getBytes(k, a, -1)
			}
		}
		c.prepare(a)
		p.sortAnswer(a, cl)
		p.sortAnswer(b, cl6)
	})
}

func FuzzEngineAndFrames(f *testing.F) {
	e0 := newTestEngine()
	for _, ip := range []string{"10.0.0.9"} {
		g := e0.cfg
		if b, err := buildPacket(&g, pktHello, afIPv4, 1, 0, ip); err == nil {
			f.Add(b[:headerSize], []byte("frame"))
		}
	}
	f.Add(make([]byte, headerSize), make([]byte, 64))
	f.Fuzz(func(t *testing.T, hdr, frame []byte) {
		t0 := time.Now()
		defer func() {
			if d := time.Since(t0); d > 2*time.Second {
				t.Fatalf("slow input: %v", d)
			}
		}()
		e := newTestEngine()
		e.cfg.RealMACs = len(frame)%2 == 0
		if len(hdr) == headerSize {
			// a correctly signed packet with arbitrary header fields
			mac := hmac.New(sha256.New, e.cfg.keyBytes())
			mac.Write(hdr)
			pkt := append(append([]byte(nil), hdr...), mac.Sum(nil)...)
			for _, st := range []State{stateInit, stateListen, stateSpeak, stateActive, stateForward} {
				e.mu.Lock()
				e.state = st
				e.onPacketLocked(pkt, nil)
				e.mu.Unlock()
			}
		}
		e.onPacketLocked(hdr, nil)
		self := [6]byte{2, 0, 0, 0, 0, 1}
		e.arpAnswerAny(frame, self, [][4]byte{{10, 0, 0, 123}})
		e.nsAnswerAny(frame, self, [][16]byte{{0x20, 1, 0xd, 0xb8, 15: 1}})
		for _, lt := range []int{1, 101, 0, 113, 228} {
			parsePkt(lt, frame)
			summarizePacket(lt, frame)
		}
		dnsSummary(frame)
		readPcap(frame)
		isARPReplyTo(frame, self, [4]byte{10, 0, 0, 1})
		isNAReplyTo(frame, self, [16]byte{1})
	})
}
