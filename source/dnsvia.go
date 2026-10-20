package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Monitor ▸ DNS is the DNS pools of the gateways this node runs.  Every gateway is shown whichever node is picked: for
// one this node does not run, the pool is the one a node that serves it has, asked over the cluster channel, with the
// addresses it answers on.  The query totals at the top stay this node's own.

type dnsAnswer struct {
	Data struct {
		Pools     []map[string]any `json:"pools"`
		Listeners []string         `json:"listeners"`
	} `json:"data"`
}

// asInt reads a number that is an int in this node's own answer and a float64 once it has come through JSON.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	}
	return 0, false
}

// poolHasGroup: the pool is the one of group gid (its own, or a shared pool the group is one of).  Groups are []int in
// this node's own answer and []any after JSON.
func poolHasGroup(p map[string]any, gid int) bool {
	if k, ok := asInt(p["key"]); ok && k == gid {
		return true
	}
	switch gs := p["groups"].(type) {
	case []int:
		for _, g := range gs {
			if g == gid {
				return true
			}
		}
	case []any:
		for _, g := range gs {
			if n, ok := asInt(g); ok && n == gid {
				return true
			}
		}
	}
	return false
}

// addDNSPools adds to resp (a dnsStatus answer) the pool serving group gid from a serving node's answer, and the
// addresses that node says the group answers on.  It reports whether the group was found.
func addDNSPools(resp map[string]any, from dnsAnswer, gid int) bool {
	var found map[string]any
	for _, p := range from.Data.Pools {
		if poolHasGroup(p, gid) {
			found = p
			break
		}
	}
	if found == nil {
		return false
	}
	data, _ := resp["data"].(map[string]any)
	if data == nil { // this node has no pool at all
		data = map[string]any{"servers": found["servers"], "pools": []map[string]any{}, "listeners": []string{},
			"queries": uint64(0), "answered": uint64(0), "servfail": uint64(0),
			"down_percent": found["down_percent"], "probes": found["probes"]}
		resp["ok"] = true
		delete(resp, "error")
		resp["data"] = data
	}
	pools, _ := data["pools"].([]map[string]any)
	fk, _ := asInt(found["key"])
	for _, p := range pools {
		if k, _ := asInt(p["key"]); k == fk {
			return true // already there (a shared pool of several groups)
		}
	}
	pools = append(pools, found)
	sort.SliceStable(pools, func(i, j int) bool {
		a, _ := asInt(pools[i]["key"])
		b, _ := asInt(pools[j]["key"])
		return a < b
	})
	data["pools"] = pools
	ls, _ := data["listeners"].([]string)
	for _, l := range from.Data.Listeners {
		if strings.HasPrefix(l, "group "+strconv.Itoa(gid)+" ") {
			ls = append(ls, l)
		}
	}
	data["listeners"] = ls
	return true
}

// dnsVia fills in the pools of the gateways this node does not run.
func (s *StatusServer) dnsVia(ctx context.Context, resp map[string]any) map[string]any {
	if s.mg == nil || s.mg.cl == nil {
		return resp
	}
	fetched := map[string]*dnsAnswer{}
	ask := func(n CanvasNode) *dnsAnswer {
		if a, done := fetched[n.Addr]; done {
			return a
		}
		cctx, cancel := context.WithTimeout(ctx, viaFetchTimeout)
		defer cancel()
		r, err := s.mg.cl.Relay(cctx, n.Addr, proxyReq{User: "dns", Method: http.MethodGet, Path: "/api/dns?own=1"})
		var out dnsAnswer
		if err == nil && r.Status == http.StatusOK && json.Unmarshal(r.Body, &out) == nil {
			fetched[n.Addr] = &out
		} else {
			fetched[n.Addr] = nil
		}
		return fetched[n.Addr]
	}
	local := func(gid int) bool {
		data, _ := resp["data"].(map[string]any)
		if data == nil {
			return false
		}
		pools, _ := data["pools"].([]map[string]any)
		for _, p := range pools {
			if poolHasGroup(p, gid) {
				return true
			}
		}
		return false
	}
	for _, cg := range s.canvasGroups() {
		if local(cg.GroupID) {
			continue
		}
		for _, n := range servingPeers(cg) {
			if a := ask(n); a != nil && addDNSPools(resp, *a, cg.GroupID) {
				break
			}
		}
	}
	return resp
}
