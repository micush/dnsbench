package main

import (
	"context"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Anycast addresses.
//
// A gateway may carry extra addresses of any subnet (GroupConfig.ExtraVIPs).
// Unlike the shared address they take no part in the election and are not
// checked against the interface: every node that runs the gateway holds them on
// the loopback interface and answers DNS on them, so a routing daemon (FRR,
// BIRD) can announce them from every node.
//
// An address is held only while this node can really answer on it: the gateway
// is running, its DNS listener is up and its pool has at least one healthy
// upstream.  Otherwise it is removed from lo and the routing daemon withdraws
// the route.  The address is added with a short lifetime that ddgw keeps
// renewing, so if ddgw crashes or hangs the kernel removes it by itself within
// anycastLife seconds and the route is withdrawn rather than black-holing
// queries.

const (
	pausedHereWhy = "paused on this node"
	pausedAllWhy  = "paused on all nodes"
)

var (
	anycastLife  = 10              // address lifetime in seconds (valid and preferred)
	anycastEvery = 3 * time.Second // how often it is renewed or withdrawn
)

// anycastAddFn / anycastDelFn put an address on / take it off lo (replaceable in tests).
var anycastAddFn = func(addr string) bool {
	p := netip.PrefixFrom(netip.MustParseAddr(addr), netip.MustParseAddr(addr).BitLen())
	life := strconv.Itoa(anycastLife)
	args := []string{"addr", "replace", p.String(), "dev", "lo", "valid_lft", life, "preferred_lft", life}
	if p.Addr().Is6() {
		args = append([]string{"-6"}, args...)
	} else {
		args = append([]string{"-4"}, args...)
	}
	return runCmd("ip", args...)
}

var anycastDelFn = func(addr string) {
	p := netip.PrefixFrom(netip.MustParseAddr(addr), netip.MustParseAddr(addr).BitLen())
	fam := "-4"
	if p.Addr().Is6() {
		fam = "-6"
	}
	runCmd("ip", fam, "addr", "del", p.String(), "dev", "lo")
}

// AnycastState is what the GUI and CLI show for one anycast address.
type AnycastState struct {
	Addr   string  `json:"addr"`
	Up     bool    `json:"up"`               // currently on lo (announced by the routing daemon)
	Reason string  `json:"reason,omitempty"` // why it is not, when it is not
	Paused string  `json:"paused,omitempty"` // "node" (paused on this node) or "all" (paused on every node), when it is
	Status string  `json:"status,omitempty"` // ok | warn | bad: the BGP session picture, set only while the address is on lo and BGP is managed here
	Detail string  `json:"detail,omitempty"` // what Status means
	BGP    string  `json:"bgp,omitempty"`    // why Status is not ok: disabled | none | down | partial (for the drawing's label)
	Uptime *UpInfo `json:"uptime,omitempty"` // filled in for the topology view
	// Carried lists the other gateways (group numbers) that keep the address up while this one cannot answer on it.
	Carried []int `json:"carried,omitempty"`
}

// anycastClaim is one gateway's say about an anycast address: its pool, and whether it can answer on it now.
type anycastClaim struct {
	pool func() *Pool
	want bool
}

// The same anycast address may be carried by several gateways (every site of an anycast service announces the same
// address).  They share one listener and one lo entry per address: the address is on lo while ANY gateway carrying it can
// answer, and the listener answers from the pool of the first (lowest group number) gateway that can.
var anycastReg = struct {
	sync.Mutex
	claims map[string]map[int]*anycastClaim
	fes    map[string]*DNSFrontend
	onLo   map[string]bool
}{claims: map[string]map[int]*anycastClaim{}, fes: map[string]*DNSFrontend{}, onLo: map[string]bool{}}

// resetAnycast forgets everything (tests).
func resetAnycast() {
	anycastReg.Lock()
	defer anycastReg.Unlock()
	for _, fe := range anycastReg.fes {
		fe.Stop()
	}
	anycastReg.claims, anycastReg.fes, anycastReg.onLo = map[string]map[int]*anycastClaim{}, map[string]*DNSFrontend{}, map[string]bool{}
}

// anycastPool is the pool that answers on addr: the first gateway that can answer, else the first one carrying it.
func anycastPool(addr string) *Pool {
	anycastReg.Lock()
	cl := anycastReg.claims[addr]
	ids := make([]int, 0, len(cl))
	for id := range cl {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var pick func() *Pool
	for _, id := range ids {
		if pick == nil {
			pick = cl[id].pool
		}
		if cl[id].want {
			pick = cl[id].pool
			break
		}
	}
	anycastReg.Unlock()
	if pick == nil {
		return nil
	}
	return pick()
}

// carriers lists the other gateways that can answer on addr (registry locked).
func carriersLocked(addr string, self int) []int {
	var out []int
	for id, c := range anycastReg.claims[addr] {
		if id != self && c.want {
			out = append(out, id)
		}
	}
	sort.Ints(out)
	return out
}

func anyWantLocked(addr string) bool {
	for _, c := range anycastReg.claims[addr] {
		if c.want {
			return true
		}
	}
	return false
}

type anycastSet struct {
	gid   int
	addrs []string
	pool  func() *Pool
	port  func() int
	dot   func() int // the DNS-over-TLS port (0 = off); nil means off
	doh   func() int // the DNS-over-HTTPS port (0 = off); nil means off
	// paused says per address where its announcing is paused ("node" or "all"); nil means none
	paused func() map[string]string

	mu     sync.Mutex
	reason map[string]string

	cancel context.CancelFunc
	done   chan struct{}
}

func newAnycastSet(gid int, addrs []string, pool func() *Pool, port func() int) *anycastSet {
	return &anycastSet{gid: gid, addrs: append([]string(nil), addrs...), pool: pool, port: port, reason: map[string]string{}}
}

func (a *anycastSet) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	a.cancel = cancel
	a.done = make(chan struct{})
	a.step() // serve at once, do not wait for the first tick
	go func() {
		defer close(a.done)
		t := time.NewTicker(anycastEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.step()
			}
		}
	}()
}

// stop ends the renewals and gives up this gateway's claim on every address; an address no other gateway can answer on
// is taken off lo and its listener closed.
func (a *anycastSet) stop() {
	if a.cancel == nil {
		return
	}
	a.cancel()
	<-a.done
	a.mu.Lock()
	defer a.mu.Unlock()
	anycastReg.Lock()
	defer anycastReg.Unlock()
	for _, addr := range a.addrs {
		delete(anycastReg.claims[addr], a.gid)
		if len(anycastReg.claims[addr]) == 0 {
			delete(anycastReg.claims, addr)
			if fe := anycastReg.fes[addr]; fe != nil {
				fe.Stop()
				delete(anycastReg.fes, addr)
			}
		}
		if anycastReg.onLo[addr] && !anyWantLocked(addr) {
			anycastDelFn(addr)
			infof("anycast: %s withdrawn (group %d stopped)", addr, a.gid)
			anycastReg.onLo[addr] = false
		}
	}
}

// healthy reports whether the group's pool can answer a client right now.
func (a *anycastSet) healthy() bool {
	p := a.pool()
	return p != nil && len(p.Ranked()) > 0
}

// step brings every address in line with what the gateways carrying it can answer: listener up, a pool healthy.  It
// renews the lifetime of the addresses that stay.
func (a *anycastSet) step() {
	a.mu.Lock()
	defer a.mu.Unlock()
	healthy := a.healthy()
	port := a.port()
	dot := 0
	if a.dot != nil {
		dot = a.dot()
	}
	doh := 0
	if a.doh != nil {
		doh = a.doh()
	}
	var paused map[string]string
	if a.paused != nil {
		paused = a.paused()
	}
	anycastReg.Lock()
	defer anycastReg.Unlock()
	for _, addr := range a.addrs {
		why := ""
		if !healthy {
			why = "no DNS server is answering"
		}
		switch paused[addr] {
		case "node":
			why = pausedHereWhy
		case "all":
			why = pausedAllWhy
		}
		if anycastReg.claims[addr] == nil {
			anycastReg.claims[addr] = map[int]*anycastClaim{}
		}
		cl := anycastReg.claims[addr]
		if cl[a.gid] == nil {
			cl[a.gid] = &anycastClaim{pool: a.pool}
		}
		cl[a.gid].pool = a.pool
		fe := anycastReg.fes[addr]
		if fe != nil && (fe.port != port || fe.dotPort != dot || fe.dohPort != doh) { // a port changed
			fe.Stop()
			delete(anycastReg.fes, addr)
			fe = nil
		}
		if fe == nil {
			ip, err := netip.ParseAddr(addr)
			if err == nil {
				f := NewDNSFrontend(ip, port, func() *Pool { return anycastPool(addr) })
				f.gw = srvhist.series(gwKey(a.gid))
				f.dotPort = dot
				f.dohPort = doh
				if err := f.Start(); err != nil {
					why = "cannot listen: " + err.Error()
					if a.reason[addr] != why { // once, not every renewal
						errorf("anycast: cannot listen on %s: %v", f.listenAddr(), err)
					}
				} else {
					anycastReg.fes[addr] = f
				}
			}
		}
		if anycastReg.fes[addr] == nil && why == "" {
			why = "not listening"
		}
		a.reason[addr] = why
		cl[a.gid].want = why == ""
		switch {
		case anyWantLocked(addr) && anycastAddFn(addr):
			if !anycastReg.onLo[addr] {
				infof("anycast: %s announced (group %d)", addr, a.gid)
			}
			anycastReg.onLo[addr] = true
		case anyWantLocked(addr):
			if a.reason[addr] != "could not add it to lo" {
				warnf("anycast: could not add %s to lo", addr)
			}
			a.reason[addr] = "could not add it to lo"
			anycastReg.onLo[addr] = false
		default:
			if anycastReg.onLo[addr] {
				anycastDelFn(addr)
				if paused[addr] != "" {
					infof("anycast: %s withdrawn (group %d: %s)", addr, a.gid, why)
				} else {
					warnf("anycast: %s withdrawn (group %d: %s)", addr, a.gid, why)
				}
			}
			anycastReg.onLo[addr] = false
		}
	}
}

func (a *anycastSet) state() []AnycastState {
	a.mu.Lock()
	defer a.mu.Unlock()
	anycastReg.Lock()
	defer anycastReg.Unlock()
	out := make([]AnycastState, 0, len(a.addrs))
	for _, addr := range a.addrs {
		st := AnycastState{Addr: addr, Up: anycastReg.onLo[addr], Reason: a.reason[addr]}
		if st.Up && st.Reason != "" { // on lo because another gateway carrying it can answer
			st.Carried = carriersLocked(addr, a.gid)
		}
		out = append(out, st)
	}
	return out
}
