package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Persistent statistics.  The query statistics (Monitor ▸ Statistics), the host statistics (Monitor ▸
// Host) and the list of recent dynamic updates live in memory while ddgw runs, and are saved to
// <state-dir>/stats.json.gz every persistEvery and when the daemon stops, then read back at start-up, so
// a restart or an update no longer empties the 30 days.  The file is a gzip stream of JSON values (a header,
// then records: the minute counters, one record per upstream server's per-minute history, one record per top-list slot, the host minutes, the recent updates) and is
// written to a temporary file and renamed, so a crash leaves the old one.  It is private to root (0600): it
// holds client addresses and the names they asked for.  Delete the file (with ddgw stopped) to forget it all.
// Time the daemon was down shows as a gap.  Each node has its own file; nothing is shared in a cluster.

const (
	persistFile   = "stats.json.gz"
	persistEvery  = 5 * time.Minute
	persistFormat = 1
	persistMax    = 1 << 30 // most the decompressed stream may hold when reading
)

type pHeader struct {
	Format    int   `json:"ddgw_stats"`
	Saved     int64 `json:"saved"`
	QSStart   int64 `json:"qs_start"`
	HostStart int64 `json:"host_start"`
}

type pKind struct {
	Clients map[string]uint32            `json:"c,omitempty"`
	Domains map[string]uint32            `json:"d,omitempty"`
	Types   map[string]uint32            `json:"t,omitempty"`
	Pairs   map[string]map[string]uint32 `json:"p,omitempty"`
	NPairs  int                          `json:"np,omitempty"`
	UDP     uint32                       `json:"u,omitempty"`
	TCP     uint32                       `json:"tc,omitempty"`
}

type pHostRow struct {
	T int64                   `json:"t"`
	N uint16                  `json:"n"`
	H [11 + hostMaxFS]float32 `json:"h"`
}

// pRec is one value of the stream; K says which fields are used.
type pRec struct {
	K      string       `json:"k"` // hdr, mins, fine, coarse, host, mounts
	Hdr    *pHeader     `json:"hdr,omitempty"`
	Mins   [][11]uint32 `json:"mins,omitempty"` // stamp, total, noerror, servfail, nxdomain, refused, other, updates, updfail, cache hits, cache misses (older files have 9 numbers)
	Stamp  int64        `json:"s,omitempty"`    // slot period of a top-list record
	Top    []pKind      `json:"top,omitempty"`  // one per answer kind (qcCount)
	Host   []pHostRow   `json:"host,omitempty"`
	Mounts []string     `json:"mounts,omitempty"`
	Upd    []UpdateLog  `json:"upd,omitempty"` // the recent dynamic updates, oldest first
	Srv    string       `json:"srv,omitempty"` // K "srv": the upstream server address whose minutes follow
	SrvMin [][9]uint64  `json:"sm,omitempty"`  // minute, ok<<32|fail, probOK<<32|probFail, latency sum (µs), latency count, worst latency (µs), cache hits, up samples, down samples (older files have 7 numbers)
}

// ── saving ──

func (s *QStats) persistTo(emit func(pRec) error) error {
	s.flush()
	s.mu.Lock()
	mins := make([][11]uint32, 0, 1024)
	oldest := s.now().Add(-qsRetain).Unix() / 60
	for i := range s.mins {
		m := &s.mins[i]
		if m.stamp <= oldest || m.total+m.chit+m.cmiss == 0 {
			continue
		}
		mins = append(mins, [11]uint32{uint32(m.stamp), m.total, m.noerror, m.servfail, m.nxdomain, m.refuse, m.other, m.updates, m.updfail, m.chit, m.cmiss})
	}
	qsStart := s.start.Unix()
	s.mu.Unlock()
	if err := emit(pRec{K: "mins", Mins: mins, Stamp: qsStart}); err != nil {
		return err
	}
	for _, tier := range []struct {
		name  string
		slots []qsTop
	}{{"fine", s.fine[:]}, {"coarse", s.coarse[:]}} {
		for i := range tier.slots {
			s.mu.Lock()
			tp := &tier.slots[i]
			if tp.stamp == 0 {
				s.mu.Unlock()
				continue
			}
			rec := pRec{K: tier.name, Stamp: tp.stamp, Top: make([]pKind, qcCount)}
			used := false
			for ci := range tp.c {
				k := &tp.c[ci]
				if k.clients == nil {
					continue
				}
				used = true
				rec.Top[ci] = pKind{Clients: copyU32(k.clients), Domains: copyU32(k.domains), Types: copyU32(k.types), NPairs: k.npairs, UDP: k.udp, TCP: k.tcp,
					Pairs: make(map[string]map[string]uint32, len(k.pairs))}
				for c, dm := range k.pairs {
					rec.Top[ci].Pairs[c] = copyU32(dm)
				}
			}
			s.mu.Unlock()
			if !used {
				continue
			}
			if err := emit(rec); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyU32(m map[string]uint32) map[string]uint32 {
	o := make(map[string]uint32, len(m))
	for k, v := range m {
		o[k] = v
	}
	return o
}

func (h *HostStats) persistTo(emit func(pRec) error) error {
	h.mu.Lock()
	rows := make([]pHostRow, 0, 1024)
	oldest := h.now().Add(-hostRetain).Unix() / 60
	for i := range h.slots {
		s := &h.slots[i]
		if s.stamp <= oldest || s.n == 0 {
			continue
		}
		r := pHostRow{T: s.stamp, N: s.n}
		r.H = [11 + hostMaxFS]float32{s.cpu, s.cpuMax, s.mem, s.io, s.ioMax, s.rx, s.rxMax, s.tx, s.txMax}
		copy(r.H[11:], s.fs[:])
		rows = append(rows, r)
	}
	mounts := append([]string(nil), h.mounts...)
	start := h.start.Unix()
	h.mu.Unlock()
	if err := emit(pRec{K: "host", Host: rows, Stamp: start}); err != nil {
		return err
	}
	return emit(pRec{K: "mounts", Mounts: mounts})
}

// savePersisted writes the whole file.  The rest of the daemon keeps running while it does: every part is
// copied under its own lock a slice at a time and encoded outside it.
func savePersisted(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	final := filepath.Join(dir, persistFile)
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	bw := bufio.NewWriterSize(f, 1<<16)
	zw := gzip.NewWriter(bw)
	enc := json.NewEncoder(zw)
	emit := func(r pRec) error { return enc.Encode(r) }
	if err := emit(pRec{K: "hdr", Hdr: &pHeader{Format: persistFormat, Saved: time.Now().Unix()}}); err != nil {
		return err
	}
	if err := qstats.persistTo(emit); err != nil {
		return err
	}
	if err := hoststats.persistTo(emit); err != nil {
		return err
	}
	if err := emit(pRec{K: "upd", Upd: updlog.snapshot()}); err != nil {
		return err
	}
	if err := srvhist.persistTo(emit); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return os.Rename(tmp, final)
}

// ── loading ──

// loadPersisted reads the file back into the collectors.  A missing file is not an error; a damaged
// one keeps whatever was read before the damage and reports it.  It must run before traffic arrives.
func loadPersisted(dir string) (int, error) {
	if dir == "" {
		return 0, nil
	}
	f, err := os.Open(filepath.Join(dir, persistFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(bufio.NewReaderSize(f, 1<<16))
	if err != nil {
		return 0, err
	}
	dec := json.NewDecoder(io.LimitReader(zr, persistMax))
	n := 0
	sawHdr := false
	for {
		var r pRec
		if err := dec.Decode(&r); err != nil {
			if err == io.EOF {
				return n, nil
			}
			return n, err
		}
		if r.K == "hdr" {
			if r.Hdr == nil || r.Hdr.Format != persistFormat {
				return n, errors.New("unknown statistics file format")
			}
			sawHdr = true
			continue
		}
		if !sawHdr {
			return n, errors.New("not a statistics file")
		}
		switch r.K {
		case "mins", "fine", "coarse":
			n += qstats.restore(r)
		case "host", "mounts":
			n += hoststats.restore(r)
		case "upd":
			n += updlog.restore(r.Upd)
		case "srv":
			n += srvhist.restore(r)
		}
	}
}

func (s *QStats) restore(r pRec) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	nowMin := now.Unix() / 60
	switch r.K {
	case "mins":
		if r.Stamp > 0 && r.Stamp <= now.Unix() {
			s.start = time.Unix(r.Stamp, 0) // counting since the very first start
		}
		n := 0
		for _, v := range r.Mins {
			m := int64(v[0])
			if m <= nowMin-qsMinSlots || m > nowMin+1 {
				continue
			}
			s.mins[m%qsMinSlots] = qsMin{stamp: m, total: v[1], noerror: v[2], servfail: v[3], nxdomain: v[4], refuse: v[5], other: v[6], updates: v[7], updfail: v[8], chit: v[9], cmiss: v[10]}
			n++
		}
		return n
	case "fine", "coarse":
		slots, span := s.fine[:], int64(qsFineSpan)
		if r.K == "coarse" {
			slots, span = s.coarse[:], qsCoarseSpan
		}
		p := r.Stamp
		if p <= nowMin/span-int64(len(slots)) || p > nowMin/span+1 || len(r.Top) == 0 || len(r.Top) > qcCount { // an older file has fewer kinds
			return 0
		}
		tp := qsTop{stamp: p}
		for ci := range r.Top {
			k := &r.Top[ci]
			if k.Clients == nil {
				continue
			}
			q := &tp.c[ci]
			q.clients, q.domains, q.types, q.pairs = k.Clients, k.Domains, k.Types, k.Pairs
			q.npairs, q.udp, q.tcp = k.NPairs, k.UDP, k.TCP
			if q.domains == nil {
				q.domains = map[string]uint32{}
			}
			if q.types == nil {
				q.types = map[string]uint32{}
			}
			if q.pairs == nil {
				q.pairs = map[string]map[string]uint32{}
			}
		}
		slots[p%int64(len(slots))] = tp
		return 1
	}
	return 0
}

func (h *HostStats) restore(r pRec) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch r.K {
	case "host":
		now := h.now()
		nowMin := now.Unix() / 60
		if r.Stamp > 0 && r.Stamp <= now.Unix() {
			h.start = time.Unix(r.Stamp, 0)
		}
		n := 0
		for _, row := range r.Host {
			if row.T <= nowMin-hostSlots || row.T > nowMin+1 {
				continue
			}
			h.slots[row.T%hostSlots] = hostSlot{stamp: row.T, n: row.N, cpu: row.H[0], cpuMax: row.H[1], mem: row.H[2], io: row.H[3], ioMax: row.H[4],
				rx: row.H[5], rxMax: row.H[6], tx: row.H[7], txMax: row.H[8], fs: [hostMaxFS]float32(row.H[11:])}
			n++
		}
		return n
	case "mounts":
		if len(r.Mounts) > hostMaxFS {
			r.Mounts = r.Mounts[:hostMaxFS]
		}
		h.mounts = r.Mounts
		return 0
	}
	return 0
}

// startPersistence loads the saved statistics and keeps saving them until stop is closed.  The returned
// function stops the saver and writes the file one last time; call it once the DNS frontends have stopped.
func startPersistence(dir string, stop <-chan struct{}) (final func()) {
	if n, err := loadPersisted(dir); err != nil {
		warnf("statistics: reading %s: %v (kept %d entries)", filepath.Join(dir, persistFile), err, n)
	} else if n > 0 {
		infof("statistics: restored %d entries from %s", n, filepath.Join(dir, persistFile))
	}
	done := make(chan struct{})
	quit := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(persistEvery)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-stop:
				return
			case <-t.C:
				if err := savePersisted(dir); err != nil {
					warnf("statistics: saving: %v", err)
				}
			}
		}
	}()
	return func() {
		close(quit)
		<-done
		if err := savePersisted(dir); err != nil {
			warnf("statistics: saving: %v", err)
		}
	}
}
