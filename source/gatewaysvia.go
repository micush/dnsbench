package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
)

// The Monitor ▸ Gateways page lists the election of every gateway.  A node only runs the elections of the gateways it
// serves, so the page of a node that serves only some of them would miss the others.  Every gateway is shown whichever
// node is picked: for one this node does not run, the table is the one a node that serves it has, asked over the cluster
// channel (the nodes in it are the gateway's, none of them is this node, so none is starred).

// takeGatewayRows adds the rows of group gid (every address family) from a serving node's answer to gs, as that node
// shows them but with nobody marked local, and returns the result in group order.
func takeGatewayRows(gs, from []GatewayGroup, gid int) []GatewayGroup {
	for _, g := range from {
		if g.GroupID != gid {
			continue
		}
		ms := make([]GatewayMember, len(g.Members))
		copy(ms, g.Members)
		for i := range ms {
			ms[i].Local = false
		}
		g.Members = ms
		gs = append(gs, g)
	}
	sort.SliceStable(gs, func(i, j int) bool {
		if gs[i].GroupID != gs[j].GroupID {
			return gs[i].GroupID < gs[j].GroupID
		}
		return gs[i].AF < gs[j].AF
	})
	return gs
}

// gatewaysVia fills in the gateways this node does not run from a node that serves them.
func (s *StatusServer) gatewaysVia(ctx context.Context, gs []GatewayGroup) []GatewayGroup {
	if s.mg == nil || s.mg.cl == nil {
		return gs
	}
	have := map[int]bool{}
	for _, g := range gs {
		have[g.GroupID] = true
	}
	fetched := map[string][]GatewayGroup{}
	ask := func(n CanvasNode) []GatewayGroup {
		if l, done := fetched[n.Addr]; done {
			return l
		}
		cctx, cancel := context.WithTimeout(ctx, viaFetchTimeout)
		defer cancel()
		resp, err := s.mg.cl.Relay(cctx, n.Addr, proxyReq{User: "gateways", Method: http.MethodGet, Path: "/api/gateways?own=1"})
		var out struct {
			Data []GatewayGroup `json:"data"`
		}
		if err == nil && resp.Status == http.StatusOK && json.Unmarshal(resp.Body, &out) == nil {
			fetched[n.Addr] = out.Data
		} else {
			fetched[n.Addr] = nil
		}
		return fetched[n.Addr]
	}
	for _, cg := range s.canvasGroups() {
		if have[cg.GroupID] {
			continue
		}
		for _, n := range servingPeers(cg) {
			l := ask(n)
			if len(l) == 0 {
				continue
			}
			before := len(gs)
			gs = takeGatewayRows(gs, l, cg.GroupID)
			if len(gs) > before {
				have[cg.GroupID] = true
				break
			}
		}
	}
	return gs
}
