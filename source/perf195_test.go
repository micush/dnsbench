package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkQStatsApply counts events the way flush does: from a few hundred clients asking for a few hundred names.
func BenchmarkQStatsApply(b *testing.B) {
	s := NewQStats()
	clients := make([]netip.Addr, 200)
	for i := range clients {
		clients[i] = netip.AddrFrom4([4]byte{10, 1, byte(i >> 8), byte(i)})
	}
	names := make([]string, 300)
	for i := range names {
		names[i] = "host" + itoa(i) + ".example.com"
	}
	now := time.Now().Unix()
	b.ReportAllocs()
	b.ResetTimer()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < b.N; i++ {
		s.apply(qEvent{t: now, client: clients[i%len(clients)], name: names[i%len(names)], qtype: 1})
	}
}

func TestWhoStringIsReusedAndBounded(t *testing.T) {
	s := NewQStats()
	a := netip.MustParseAddr("192.0.2.7")
	if s.whoString(a) != "192.0.2.7" || s.whoString(netip.MustParseAddr("::ffff:192.0.2.7")) != "192.0.2.7" {
		t.Fatal("wrong text for an address, or a mapped IPv4 address is not the same client")
	}
	if s.whoString(netip.Addr{}) != "unknown" {
		t.Fatal("an invalid address is not \"unknown\"")
	}
	if allocs := testing.AllocsPerRun(100, func() { _ = s.whoString(a) }); allocs != 0 {
		t.Fatalf("a known client costs %v allocations", allocs)
	}
	for i := 0; i < 3*qsWhoMax; i++ {
		s.whoString(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}))
		if len(s.who) > qsWhoMax {
			t.Fatalf("the table holds %d addresses", len(s.who))
		}
	}
}

// The counts are what they were: the same events give the same tables with the address strings reused.
func TestQStatsCountsUnchangedByAddressReuse(t *testing.T) {
	s := NewQStats()
	now := time.Now().Unix()
	for i := 0; i < 50; i++ {
		s.Record(netip.MustParseAddr("192.0.2.1"), "a.example", 1, false, 0)
		s.Record(netip.MustParseAddr("192.0.2.2"), "b.example", 28, true, 3)
	}
	s.flush()
	s.mu.Lock()
	defer s.mu.Unlock()
	top := &s.fine[(now/60/qsFineSpan)%qsFineSlots]
	if top.c[qcNoError].clients["192.0.2.1"] != 50 || top.c[qcNXDomain].clients["192.0.2.2"] != 50 ||
		top.c[qcNXDomain].pairs["192.0.2.2"]["b.example"] != 50 || top.c[qcNXDomain].tcp != 50 {
		t.Fatalf("unexpected counts: %+v", top.c[qcNoError].clients)
	}
}

// ── DoH ──────────────────────────────────────────────────────────────────────

func dohFrontend(t *testing.T) *DNSFrontend {
	st := newUDPStub(t, func(q []byte, reply func([]byte)) { reply(answerTo(q)) })
	return herdFrontend(t, st)
}

func dohDo(fe *DNSFrontend, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	fe.serveDoH(rec, req)
	return rec
}

func paddedQuery(name string, pad int) []byte {
	opt := append([]byte{0, 12, byte(pad >> 8), byte(pad)}, make([]byte, pad)...)
	return cacheQuery(name, 1, withOPT(4096, false, opt))
}

func TestDoHBodiesOfEverySize(t *testing.T) {
	fe := dohFrontend(t)
	for _, c := range []struct {
		name    string
		q       []byte
		unknown bool // no Content-Length
		want    int
	}{
		{"small", cacheQuery("small.doh.example", 1, nil), false, 200},
		{"small, length unknown", cacheQuery("chunked.doh.example", 1, nil), true, 200},
		{"just over the pooled buffer", paddedQuery("over.doh.example", queryBufSize-30), false, 200},
		{"3 KB", paddedQuery("big.doh.example", 3000), false, 200},
		{"3 KB, length unknown", paddedQuery("bigchunk.doh.example", 3000), true, 200},
		{"20 KB, length unknown", paddedQuery("huge.doh.example", 20000), true, 200},
	} {
		var body io.Reader = bytes.NewReader(c.q)
		if c.unknown {
			body = io.NopCloser(bytes.NewReader(c.q))
		}
		req := httptest.NewRequest("POST", dohPath, body)
		req.Header.Set("Content-Type", "application/dns-message")
		rec := dohDo(fe, req)
		resp := rec.Body.Bytes()
		if rec.Code != c.want || !sameQuestionExact(c.q, resp) {
			t.Errorf("%s: status %d, question kept: %v", c.name, rec.Code, sameQuestionExact(c.q, resp))
		}
	}
	// too large, announced and not
	for _, unknown := range []bool{false, true} {
		var body io.Reader = bytes.NewReader(make([]byte, dohMaxAnswer+10))
		if unknown {
			body = io.NopCloser(body)
		}
		req := httptest.NewRequest("POST", dohPath, body)
		req.Header.Set("Content-Type", "application/dns-message")
		if rec := dohDo(fe, req); rec.Code != 400 {
			t.Errorf("an oversized body (length unknown=%v) got %d, want 400", unknown, rec.Code)
		}
	}
}

func TestDoHGetParameter(t *testing.T) {
	fe := dohFrontend(t)
	q := cacheQuery("get.doh.example", 1, nil)
	enc := base64.RawURLEncoding.EncodeToString(q)
	padded := base64.URLEncoding.EncodeToString(q)
	for name, raw := range map[string]string{
		"plain":                  "dns=" + enc,
		"with padding":           "dns=" + padded,
		"padding escaped":        "dns=" + strings.ReplaceAll(padded, "=", "%3D"),
		"other parameters first": "ct=application/dns-message&x=1&dns=" + enc,
		"the first dns wins":     "dns=" + enc + "&dns=AAAA",
	} {
		rec := dohDo(fe, httptest.NewRequest("GET", dohPath+"?"+raw, nil))
		if rec.Code != 200 || !sameQuestionExact(q, rec.Body.Bytes()) {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	big := paddedQuery("getbig.doh.example", 3000)
	rec := dohDo(fe, httptest.NewRequest("GET", dohPath+"?dns="+base64.RawURLEncoding.EncodeToString(big), nil))
	if rec.Code != 200 || !sameQuestionExact(big, rec.Body.Bytes()) {
		t.Errorf("a 3 KB GET: status %d", rec.Code)
	}
	for name, raw := range map[string]string{"missing": "x=1", "empty": "dns=", "not base64": "dns=!!!!", "too short": "dns=AAAA"} {
		if rec := dohDo(fe, httptest.NewRequest("GET", dohPath+"?"+raw, nil)); rec.Code != 400 {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
}

// dohQueryParam must read what url.Values.Get does.
func TestDoHQueryParamMatchesStdlib(t *testing.T) {
	for _, raw := range []string{"", "dns=abc", "a=1&dns=abc&dns=def", "dns", "dns=", "x=dns&dns=q", "d%6es=zzz", "dns=a%2Bb", "dns=a+b",
		"dns=%zz", "&&dns=1", "dns=1;dns=2", "DNS=up", "dns=ab%3D%3D&z", "dns==x"} {
		v, err := url.ParseQuery(raw)
		want := v.Get("dns")
		if err != nil && strings.Contains(raw, ";") {
			continue // newer Go drops such pairs; they are not DoH requests anyway
		}
		if got := dohQueryParam(raw); got != want && !(err != nil && got == "") {
			t.Errorf("%q: got %q, want %q", raw, got, want)
		}
	}
}

func TestDoHPooledBuffersNeverMixQueriesUp(t *testing.T) {
	fe := dohFrontend(t)
	var wg sync.WaitGroup
	var bad atomic.Int32
	gate := make(chan struct{}, 32)
	for i := 0; i < 400; i++ {
		wg.Add(1)
		gate <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-gate }()
			q := cacheQuery(fmt.Sprintf("n%d.dohpool.example", i), 1, nil)
			var req *http.Request
			if i%2 == 0 {
				req = httptest.NewRequest("POST", dohPath, bytes.NewReader(q))
				req.Header.Set("Content-Type", "application/dns-message")
			} else {
				req = httptest.NewRequest("GET", dohPath+"?dns="+base64.RawURLEncoding.EncodeToString(q), nil)
			}
			rec := dohDo(fe, req)
			if rec.Code != 200 || !sameQuestionExact(q, rec.Body.Bytes()) {
				bad.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d of 400 DoH clients got another client's answer", bad.Load())
	}
}

func BenchmarkDoHBody(b *testing.B) {
	q := cacheQuery("bench.doh.example", 1, withOPT(1232, true))
	b.Run("io.ReadAll", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			r := io.LimitReader(bytes.NewReader(q), dohMaxAnswer+1)
			if x, err := io.ReadAll(r); err != nil || len(x) != len(q) {
				b.Fatal(err)
			}
		}
	})
	b.Run("pooled", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			x, bp, ok := readDoHBody(bytes.NewReader(q), int64(len(q)))
			if !ok || len(x) != len(q) {
				b.Fatal("bad")
			}
			queryBufs.Put(bp)
		}
	})
	enc := base64.RawURLEncoding.EncodeToString(q)
	raw := "ct=application/dns-message&dns=" + enc
	b.Run("GET std", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			v, _ := url.ParseQuery(raw)
			if x, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(v.Get("dns"), "=")); err != nil || len(x) != len(q) {
				b.Fatal(err)
			}
		}
	})
	b.Run("GET pooled", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			x, bp, ok := decodeDoHParam(dohQueryParam(raw))
			if !ok || len(x) != len(q) {
				b.Fatal("bad")
			}
			queryBufs.Put(bp)
		}
	})
}

// ── cluster nonces ───────────────────────────────────────────────────────────

func TestNonceSweepIsNotPerRequest(t *testing.T) {
	a, b := twoNodeCluster(t)
	bc, _ := b.mg.cl.node.Identity()
	secret := a.mg.cl.node.Secret()
	self := b.mg.cl.node.Self()
	call := func() int {
		code, _ := rawPeerCall(t, secret, a, self, &bc, "/cluster/status")
		return code
	}
	expired := func(n int) {
		a.mg.cl.mu.Lock()
		for i := 0; i < n; i++ {
			a.mg.cl.nonces["old-"+itoa(i)] = time.Now().Add(-time.Minute)
		}
		a.mg.cl.mu.Unlock()
	}
	size := func() int {
		a.mg.cl.mu.Lock()
		defer a.mg.cl.mu.Unlock()
		return len(a.mg.cl.nonces)
	}
	// a sweep has just been done: expired entries wait for the next one
	a.mg.cl.mu.Lock()
	a.mg.cl.nonceSweep = time.Now().Add(time.Hour)
	a.mg.cl.mu.Unlock()
	expired(500)
	before := size()
	if call() != 200 {
		t.Fatal("a request was refused")
	}
	if size() < before {
		t.Fatalf("the table was swept on a request (%d -> %d entries)", before, size())
	}
	// when the time for a sweep has come, the next request does it
	a.mg.cl.mu.Lock()
	a.mg.cl.nonceSweep = time.Time{}
	a.mg.cl.mu.Unlock()
	if call() != 200 {
		t.Fatal("a request was refused")
	}
	if size() > 50 {
		t.Fatalf("expired entries were not swept: %d left", size())
	}
	// above the size limit the sweep is at once
	a.mg.cl.mu.Lock()
	a.mg.cl.nonceSweep = time.Now().Add(time.Hour)
	a.mg.cl.mu.Unlock()
	expired(maxNonces + 10)
	call()
	if size() > 50 {
		t.Fatalf("a table above the limit was not swept: %d left", size())
	}
}

func TestReplayIsStillRefused(t *testing.T) {
	a, b := twoNodeCluster(t)
	bc, _ := b.mg.cl.node.Identity()
	secret := a.mg.cl.node.Secret()
	self := b.mg.cl.node.Self()
	pj, _ := json.Marshal(self)
	peerHdr := base64.StdEncoding.EncodeToString(pj)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := randHex(12)
	send := func() int {
		req, _ := http.NewRequest("GET", peerURL("/cluster/status"), nil)
		req.Header.Set("X-Ddgw-Peer", peerHdr)
		req.Header.Set("X-Ddgw-Ts", ts)
		req.Header.Set("X-Ddgw-Nonce", nonce)
		sum := sha256.Sum256(nil)
		h := hex.EncodeToString(sum[:])
		req.Header.Set("X-Ddgw-Body", h)
		req.Header.Set("X-Ddgw-Sig", signMessageHash(secret, peerHdr, ts, nonce, "GET", "/cluster/status", h))
		res, err := pinnedClientAs(a.addr, a.mg.cl.node.Fingerprint(), 5*time.Second, &bc).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if send() != 200 {
		t.Fatal("the first request was refused")
	}
	if send() != 401 {
		t.Fatal("a replayed request was accepted")
	}
}
