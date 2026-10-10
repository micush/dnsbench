package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// A gateway's graph is drawn from every node's history: which node a client's queries land on is up to the clients and the
// network, so one node alone shows holes whenever the traffic went elsewhere. The node asked sums its own buckets with
// every reachable peer's. Servers and domains stay per node (each node probes them itself).

type histAsk struct {
	Key  string `json:"key"`
	From int64  `json:"from"`
	To   int64  `json:"to"`
}

// handleHist answers a peer: this node's own buckets for a gateway (never forwarded on, so nodes cannot ask each other in circles).
func (c *Cluster) handleHist(rw http.ResponseWriter, r *http.Request, caller ClusterPeer, body []byte) {
	var a histAsk
	if json.Unmarshal(body, &a) != nil || kindOf(a.Key) != "gateway" {
		jsonError(rw, http.StatusBadRequest, "bad request")
		return
	}
	raw, err := srvhist.Raw(a.Key, time.Unix(a.From, 0), time.Unix(a.To, 0))
	if err != nil {
		jsonError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, raw)
}

// clusterStats is one gateway's history over [from, to] summed over this node and every peer that answers.
func (c *Cluster) clusterStats(key string, from, to time.Time) (ServerStatsResult, error) {
	mine, err := srvhist.Raw(key, from, to)
	if err != nil {
		return ServerStatsResult{}, err
	}
	snap := c.node.Snapshot()
	var peers []ClusterPeer
	c.mu.Lock()
	enabled := c.cfg.Enabled
	for _, p := range snap.Peers {
		if pi := c.info[p.Addr]; pi != nil && pi.Reachable {
			peers = append(peers, p)
		}
	}
	c.mu.Unlock()
	off := 0
	if enabled {
		off = len(snap.Peers) - len(peers)
	} else {
		peers = nil
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	nodes := 1
	ask := histAsk{Key: key, From: mine.From, To: mine.To}
	for _, p := range peers {
		wg.Add(1)
		go func(p ClusterPeer) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			var raw srvRaw
			err := c.call(ctx, p, "POST", "/cluster/hist", ask, &raw, 5*time.Second)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || !mine.merge(raw) {
				off++ // an older version without this call, or no answer: shown as a node missing, not as an error
				return
			}
			nodes++
		}(p)
	}
	wg.Wait()
	res := mine.Stats()
	res.Nodes, res.NodesOff = nodes, off
	return res, nil
}
