package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DNS over HTTPS to an upstream server (RFC 8484): a server written "https://host[:port][/path]" is sent each
// query as the body of an HTTPS POST (application/dns-message) and its answer comes back the same way.

const (
	dohScheme      = "https://"
	dohDefaultPath = "/dns-query"
	dohMaxAnswer   = 65535
)

// normalizeDoH canonicalises what follows "https://": host[:port][/path]; the port is 443 and the path
// /dns-query unless given. The result keeps the scheme, so it can be used as the server's key.
func normalizeDoH(rest string) (string, error) {
	hostPort, path := rest, dohDefaultPath
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		hostPort, path = rest[:i], rest[i:]
	}
	if path == "/" {
		path = dohDefaultPath
	}
	if len(path) > 200 || strings.ContainsAny(path, "?#\\") {
		return "", errors.New("bad path")
	}
	for _, c := range path {
		if c < 0x21 || c > 0x7e {
			return "", errors.New("bad path")
		}
	}
	hp, err := normalizePlain(hostPort, "443")
	if err != nil {
		return "", err
	}
	return dohScheme + hp + path, nil
}

// dohExchange sends one query to the DoH server addr and returns its answer and the round trip. Every call
// uses a new connection (as the other transports do), so the time measured is what a client would see.
func dohExchange(ctx context.Context, addr string, query []byte, timeout time.Duration, insecure bool) ([]byte, time.Duration, error) {
	if len(query) < 12 {
		return nil, 0, errors.New("query too short")
	}
	u, err := url.Parse(addr)
	if err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{ServerName: u.Hostname(), RootCAs: dotRootCAs, MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure},
		DisableKeepAlives: true,
		ForceAttemptHTTP2: true,
		Proxy:             nil, // never through a proxy from the environment
		DialContext:       (&net.Dialer{}).DialContext,
	}
	defer tr.CloseIdleConnections()
	// RFC 8484 asks for ID 0 on the wire; the caller's ID is put back on the answer
	id := binary.BigEndian.Uint16(query)
	body := append([]byte(nil), query...)
	body[0], body[1] = 0, 0
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, addr, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	start := time.Now()
	resp, err := (&http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("https: %s", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "application/dns-message") {
		return nil, 0, fmt.Errorf("https: unexpected content type %q", ct)
	}
	ans, err := io.ReadAll(io.LimitReader(resp.Body, dohMaxAnswer+1))
	if err != nil {
		return nil, 0, err
	}
	rtt := time.Since(start)
	if len(ans) > dohMaxAnswer {
		return nil, 0, errors.New("https: answer too large")
	}
	if h, ok := parseHeader(ans); !ok || !h.qr {
		return nil, 0, errors.New("malformed response")
	}
	binary.BigEndian.PutUint16(ans, id)
	return ans, rtt, nil
}
