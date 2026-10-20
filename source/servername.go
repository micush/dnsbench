package main

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"
)

// A DNS server's name (the Name on its form, --label) that is a full host name (it has a dot, such as dns-a.example.com) is
// also registered in DNS: it gets the server's address (A or AAAA, and the PTR record), with the same RFC 2136 update a client
// name gets, and the record of the server's previous name is removed when that was a full name too.  A short name is only a
// label, as before; clearing a name leaves DNS alone, like Remove name for a client.

type serverRegistration struct {
	fqdn, old string
	addr      netip.Addr
}

// serverIP is the address of a server written as 8.8.8.8, 8.8.8.8:53, [2001:db8::1]:53 or 2001:db8::1; a host name or a
// tls:// or https:// server has no address of its own to register.
func serverIP(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return a, true
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		if a, err := netip.ParseAddr(h); err == nil {
			return a, true
		}
	}
	return netip.Addr{}, false
}

// fullName is a name worth registering: lowercased, with a dot, and a valid host name.
func fullName(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !strings.Contains(s, ".") {
		return "", false
	}
	n, err := cleanHostname(s)
	return n, err == nil
}

// planServerRegistration says what a canvas edit registers in DNS: nil unless it adds or names a server that has an IP
// address with a full host name.
func planServerRegistration(dc *DaemonConfig, e canvasEdit) *serverRegistration {
	if e.Kind != "server" || (e.Action != "add" && e.Action != "set") {
		return nil
	}
	fqdn, ok := fullName(e.Label)
	if !ok {
		return nil
	}
	addr, err := normalizeServer(e.Server)
	if err != nil {
		return nil
	}
	ip, ok := serverIP(addr)
	if !ok {
		return nil
	}
	r := &serverRegistration{fqdn: fqdn, addr: ip}
	if g, err := findGroup(dc, e.Group); err == nil {
		pool := &dc.DNS
		if g.DNS != nil {
			pool = g.DNS
		}
		if prev, ok := fullName(pool.ServerNames[addr]); ok && prev != fqdn {
			r.old = prev
		}
	}
	return r
}

// registerServerName sends the update and says how it went: a sentence, and the reason alone when it failed.
func (m *Mgmt) registerServerName(r *serverRegistration, actor string) (msg, failure string) {
	if m.regNameFn == nil {
		return "DNS was not updated: registration is not available on this node", "not available"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lines, err := m.regNameFn(ctx, r.fqdn, r.old, r.addr)
	warnf("canvas: %q registered %s as %s in DNS: %s", actor, r.addr, r.fqdn, strings.Join(lines, "; "))
	if err != nil {
		f := strings.Join(lines, "; ")
		if f == "" {
			f = err.Error()
		}
		return "DNS was not updated for " + r.fqdn + ": " + f, f
	}
	return "registered " + r.fqdn + " in DNS", ""
}
