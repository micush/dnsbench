package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// Two things keep a node's answer cache from starting cold.
//
// 1. A pool that is rebuilt because its servers changed (one added, paused, resumed or removed) takes over the old pool's
//    cache when the cache settings are the same.  The cached answers say nothing about which server will be asked next, so
//    they stay valid.  Switching the cache off and on, or changing its size, TTL limit or the client-subnet settings, still
//    starts it empty.
// 2. A node that has just started asks a cluster member for the most recently used entries of each gateway's cache, with
//    the age they have there, so their TTLs go on counting down.  It is one request per gateway at start-up, in the
//    background, and nothing is replicated afterwards.  A node with no answer from anyone starts cold, as before.

const (
	cacheWarmEntries = 20000   // the most entries one warm start takes
	cacheWarmBytes   = 3 << 20 // and the most message bytes (base64 makes it a little over 4 MB, under the 5 MB request limit)
	cacheWarmMinLeft = 5 * time.Second
)

// cacheFingerprint says which cache settings an entry's key and TTL depend on.  Two caches with the same fingerprint
// hold interchangeable entries.
func cacheFingerprint(c DNSConfig) string {
	return fmt.Sprintf("on=%t entries=%d maxttl=%d ecs=%t/%d/%d", c.Cache, c.CacheEntries, c.CacheMaxTTL, c.ECS, c.ECSPrefix4, c.ECSPrefix6)
}

// cacheCarries reports whether a pool built from nu can use the cache of a pool built from old.
func cacheCarries(old, nu DNSConfig) bool {
	return old.Cache && nu.Cache && cacheFingerprint(old) == cacheFingerprint(nu)
}

type cacheWireEntry struct {
	Key string `json:"k"`
	Msg []byte `json:"m"`
	Age int64  `json:"a"` // milliseconds since it was stored
}

type cacheWire struct {
	Print   string           `json:"print"`
	Entries []cacheWireEntry `json:"entries"`
}

type cacheAsk struct {
	Group int `json:"group"`
}

// export lists up to maxN entries, the most recently used first, that have at least minLeft to live, within maxBytes.
func (c *respCache) export(maxN, maxBytes int, minLeft time.Duration) []cacheWireEntry {
	type cand struct {
		w    cacheWireEntry
		rank int // position in its shard's LRU order: 0 is the most recently used
	}
	var all []cand
	now := c.now()
	for _, sh := range c.shards {
		sh.mu.Lock()
		rank := 0
		for el := sh.lru.Front(); el != nil && rank < maxN/len(c.shards)+16; el = el.Next() {
			e := el.Value.(*cacheEntry)
			age := now.Sub(e.at)
			rank++
			if age < 0 || e.life-age < minLeft {
				continue
			}
			all = append(all, cand{cacheWireEntry{Key: e.key, Msg: e.msg, Age: age.Milliseconds()}, rank})
		}
		sh.mu.Unlock()
	}
	// the shards each have their own order: interleave them by rank
	sort.SliceStable(all, func(i, j int) bool { return all[i].rank < all[j].rank })
	var out []cacheWireEntry
	size := 0
	for _, a := range all {
		if len(out) >= maxN || size+len(a.w.Msg)+len(a.w.Key) > maxBytes {
			break
		}
		size += len(a.w.Msg) + len(a.w.Key)
		out = append(out, a.w)
	}
	return out
}

// load takes entries from another node and returns how many were kept.
func (c *respCache) load(ents []cacheWireEntry) int {
	n := 0
	for _, e := range ents {
		if e.Key == "" || len(e.Key) > 1024 || len(e.Msg) < 12 || len(e.Msg) > 65535 {
			continue
		}
		if c.store(e.Key, e.Msg, time.Duration(e.Age)*time.Millisecond, true) {
			n++
		}
	}
	return n
}

// cacheExport is what this node hands to a peer that asks for gateway gid's cache.
func (m *Mgmt) cacheExport(gid int) cacheWire {
	if m.poolOf == nil {
		return cacheWire{}
	}
	p := m.poolOf(gid)
	if p == nil || p.cache == nil {
		return cacheWire{}
	}
	return cacheWire{Print: cacheFingerprint(p.cfg), Entries: p.cache.export(cacheWarmEntries, cacheWarmBytes, cacheWarmMinLeft)}
}

// handleCache answers a peer (never forwarded on).
func (c *Cluster) handleCache(rw http.ResponseWriter, r *http.Request, caller ClusterPeer, body []byte) {
	var a cacheAsk
	if json.Unmarshal(body, &a) != nil {
		jsonError(rw, http.StatusBadRequest, "bad request")
		return
	}
	writeJSON(rw, http.StatusOK, c.mg.cacheExport(a.Group))
}

// reachablePeers lists the cluster members that answered the last sync.
func (c *Cluster) reachablePeers() []ClusterPeer {
	if !c.Enabled() {
		return nil
	}
	snap := c.node.Snapshot()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []ClusterPeer
	for _, p := range snap.Peers {
		if pi := c.info[p.Addr]; pi != nil && pi.Reachable {
			out = append(out, p)
		}
	}
	return out
}

// warmOne asks the reachable peers in turn for gateway gid's cache and loads the first useful answer.
func (m *Mgmt) warmOne(ctx context.Context, gid int) bool {
	p := m.poolOf(gid)
	if p == nil || p.cache == nil {
		return true // nothing to fill
	}
	want := cacheFingerprint(p.cfg)
	for _, peer := range m.cl.reachablePeers() {
		t0 := time.Now()
		var w cacheWire
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := m.cl.call(cctx, peer, "POST", "/cluster/cache", cacheAsk{Group: gid}, &w, 10*time.Second)
		cancel()
		if err != nil || len(w.Entries) == 0 || w.Print != want {
			continue // an older version, a node that is cold too, or other cache settings
		}
		n := p.cache.load(w.Entries)
		infof("dns: gateway %d cache filled from %s: %d of %d entries in %d ms", gid, peer.Addr, n, len(w.Entries), time.Since(t0).Milliseconds())
		return true
	}
	return false
}

// warmCaches fills the caches of this node's gateways from the cluster, in the background, once after start-up.  It
// tries a few times as the cluster comes up and then gives up quietly.
func (m *Mgmt) warmCaches(ctx context.Context, gids func() []int) {
	todo := map[int]bool{}
	for _, g := range gids() {
		todo[g] = true
	}
	for _, wait := range []time.Duration{4 * time.Second, 8 * time.Second, 15 * time.Second, 30 * time.Second} {
		if len(todo) == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if len(m.cl.reachablePeers()) == 0 {
			continue
		}
		for g := range todo {
			if m.warmOne(ctx, g) {
				delete(todo, g)
			}
		}
	}
}
