package main

import (
	"net"
	"net/netip"
	"sync"
	"time"
)

// Forwarding loops.  A server this pool forwards to may itself forward to the gateway (its forwarder is set to the
// gateway address or an anycast address): a query then goes gateway -> server -> gateway -> server ... until a
// timeout, and the server looks "down" although it is fine.  A query that arrives from one of the pool's own servers
// is never sent back to that server; the others get it, and the warning below says which server is doing it.

func (s *Server) address() netip.Addr {
	s.ipOnce.Do(func() {
		if ap, err := netip.ParseAddrPort(s.Addr); err == nil {
			s.ip = ap.Addr().Unmap()
		}
	})
	return s.ip
}

// dropAsker removes from ranked the server the query came from (nothing is copied when it is not there).
func dropAsker(ranked []*Server, client netip.Addr) []*Server {
	if !client.IsValid() {
		return ranked
	}
	client = client.Unmap()
	for i, s := range ranked {
		if s.address() != client || isThisHost(client) { // a query from this machine is a local program's, not a server forwarding
			continue
		}
		if now := time.Now().Unix(); now-s.loopSeen.Load() >= 60 {
			s.loopSeen.Store(now)
			warnf("dns: %s is one of this gateway's servers and is sending it queries: its forwarder points back at this gateway (or at an anycast address it serves). Its own queries are answered by the other servers; set its forwarders to real resolvers to stop the loop", s.Addr)
		}
		out := make([]*Server, 0, len(ranked)-1)
		out = append(out, ranked[:i]...)
		return append(out, ranked[i+1:]...)
	}
	return ranked
}

var (
	hostMu   sync.Mutex
	hostAt   time.Time
	hostSet  map[netip.Addr]bool
	hostLoad = func() []net.Addr { a, _ := net.InterfaceAddrs(); return a } // a test hook
)

// isThisHost reports whether a is loopback or an address of this machine (the list is read at most every 30 s).
func isThisHost(a netip.Addr) bool {
	if a.IsLoopback() {
		return true
	}
	hostMu.Lock()
	defer hostMu.Unlock()
	if hostSet == nil || time.Since(hostAt) > 30*time.Second {
		hostSet = map[netip.Addr]bool{}
		for _, ad := range hostLoad() {
			if ipn, ok := ad.(*net.IPNet); ok {
				if x, ok := netip.AddrFromSlice(ipn.IP); ok {
					hostSet[x.Unmap()] = true
				}
			}
		}
		hostAt = time.Now()
	}
	return hostSet[a]
}
