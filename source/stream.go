package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// pace sleeps so that one loop iteration takes at least interval.
func pace(start time.Time, interval time.Duration) {
	if interval <= 0 {
		return
	}
	if d := interval - time.Since(start); d > 0 {
		time.Sleep(d)
	}
}

func (c *runCtx) paceInterval() time.Duration {
	if c.p.ratePerWorker == 0 {
		return 0
	}
	return time.Duration(float64(time.Second) / float64(c.p.ratePerWorker))
}

// streamDone reports whether a sequential worker should stop.
func (c *runCtx) streamDone(sent uint64) bool {
	if c.stop.Load() {
		return true
	}
	if c.p.hasDuration {
		return time.Since(c.start) >= c.p.duration
	}
	return sent >= c.p.count
}

// ── TCP and DoT ──────────────────────────────────────────────────────────────

func (c *runCtx) streamWorker(id int) {
	l := c.newLocal()
	defer l.flush()
	p := &c.p

	var mu sync.Mutex
	var conn net.Conn
	closeCur := func() {
		mu.Lock()
		if conn != nil {
			conn.Close()
		}
		mu.Unlock()
	}
	stopWatch := context.AfterFunc(c.ctx, closeCur)
	defer stopWatch()
	defer closeCur()

	connErr := ""
	dial := func() {
		closeCur()
		mu.Lock()
		conn = nil
		mu.Unlock()
		d := net.Dialer{Timeout: queryTimeout}
		raw, err := d.DialContext(c.ctx, "tcp", c.hostport)
		if err != nil {
			connErr = "TCP connect failed: " + err.Error()
			return
		}
		nc := raw
		if p.protocol == "dot" {
			tc := tls.Client(raw, &tls.Config{
				ServerName:         p.host,
				InsecureSkipVerify: p.insecure,
				MinVersion:         tls.VersionTLS12,
			})
			hctx, cancel := context.WithTimeout(c.ctx, queryTimeout)
			err := tc.HandshakeContext(hctx)
			cancel()
			if err != nil {
				raw.Close()
				connErr = "TLS handshake failed: " + err.Error()
				return
			}
			nc = tc
		}
		mu.Lock()
		conn = nc
		mu.Unlock()
		connErr = ""
	}

	respBuf := make([]byte, 65536)
	var frame []byte
	txid := uint16(rand.Uint32())

	// exchange sends one query and reads one reply on the current connection.
	exchange := func(t []byte) (int, string) {
		mu.Lock()
		cn := conn
		mu.Unlock()
		if cn == nil {
			return rcError, connErr
		}
		txid++
		frame = append(frame[:0], byte((len(t))>>8), byte(len(t)))
		frame = append(frame, t...)
		frame[2], frame[3] = byte(txid>>8), byte(txid)
		cn.SetDeadline(time.Now().Add(queryTimeout))
		if _, err := cn.Write(frame); err != nil {
			return rcError, err.Error()
		}
		var lb [2]byte
		if _, err := io.ReadFull(cn, lb[:]); err != nil {
			return rcError, "read error"
		}
		n := int(lb[0])<<8 | int(lb[1])
		if _, err := io.ReadFull(cn, respBuf[:n]); err != nil {
			return rcError, "read error"
		}
		if n >= 2 && (respBuf[0] != byte(txid>>8) || respBuf[1] != byte(txid)) {
			return rcError, "response ID mismatch"
		}
		return rcodeSlot(respBuf[:n]), ""
	}

	dial()
	interval := c.paceInterval()
	nd := len(c.tmpl)
	idx := id
	var sent uint64
	for !c.streamDone(sent) {
		dom := idx % nd
		idx++
		t0 := time.Now()
		slot, errText := exchange(c.tmpl[dom])
		if slot == rcError {
			if c.stop.Load() {
				break
			}
			if conn == nil {
				time.Sleep(10 * time.Millisecond) // don't spin on a dead server
			}
			dial()
			t0 = time.Now()
			slot, errText = exchange(c.tmpl[dom])
		}
		l.result(int64(time.Since(t0)), slot, errText, c.names[dom])
		sent++
		pace(t0, interval)
	}
}

// ── DoH ──────────────────────────────────────────────────────────────────────

func (c *runCtx) dohWorker(id int) {
	l := c.newLocal()
	defer l.flush()
	p := &c.p

	dialer := net.Dialer{Timeout: queryTimeout}
	tr := &http.Transport{
		// Always connect to the address resolved once at job start, while the
		// URL host (and so SNI / certificate name) stays what the user typed.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", c.hostport)
		},
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: p.insecure, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: queryTimeout,
		MaxIdleConns:        1,
		MaxIdleConnsPerHost: 1,
		MaxConnsPerHost:     1, // one connection per worker
		IdleConnTimeout:     time.Minute,
		DisableCompression:  true,
		ForceAttemptHTTP2:   p.dohHTTP2,
	}
	if !p.dohHTTP2 {
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{} // HTTP/1.1 only
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: queryTimeout}

	host := p.host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p.port != "443" {
		host += ":" + p.port
	}
	base := url.URL{Scheme: "https", Host: host, Path: p.path}

	// RFC 8484 recommends ID 0 for DoH, which keeps GET requests cacheable.
	var one func(t []byte) (int, string)
	one = func(t []byte) (int, string) {
		var req *http.Request
		var err error
		if p.dohPost {
			req, err = http.NewRequestWithContext(c.ctx, http.MethodPost, base.String(), bytes.NewReader(t))
			if err == nil {
				req.Header.Set("Content-Type", "application/dns-message")
			}
		} else {
			u := base
			u.RawQuery = "dns=" + base64.RawURLEncoding.EncodeToString(t)
			req, err = http.NewRequestWithContext(c.ctx, http.MethodGet, u.String(), nil)
		}
		if err != nil {
			return rcError, err.Error()
		}
		req.Header.Set("Accept", "application/dns-message")
		resp, err := client.Do(req)
		if err != nil {
			return rcError, shortErr(err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 65535))
		resp.Body.Close()
		if err != nil {
			return rcError, "read error"
		}
		if resp.StatusCode != http.StatusOK {
			return rcError, fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return rcodeSlot(body), ""
	}

	interval := c.paceInterval()
	nd := len(c.tmpl)
	idx := id
	var sent uint64
	for !c.streamDone(sent) {
		dom := idx % nd
		idx++
		t0 := time.Now()
		slot, errText := one(c.tmpl[dom])
		if slot == rcError {
			if c.stop.Load() {
				break
			}
			t0 = time.Now()
			slot, errText = one(c.tmpl[dom]) // retry once on a fresh connection
		}
		l.result(int64(time.Since(t0)), slot, errText, c.names[dom])
		sent++
		pace(t0, interval)
	}
}

// shortErr trims net/http's "Post "https://...": " prefix so sample errors stay readable.
func shortErr(err error) string {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err.Error()
	}
	return err.Error()
}
