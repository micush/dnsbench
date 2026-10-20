package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Who asked for what.  The statistics answer "what did this client ask for" and "who asked for this" from the pairs
// (client, name) they have counted.  The pairs are far too many to keep in memory for 30 days, so they live on disk: the
// pairs of the unit being counted (pairUnitMin minutes) are in memory, and each finished unit is written to its own file,
// <state-dir>/pairs/p<unit>.gz, which is read back only when a client or a domain is picked.  Nothing is dropped
// but a flood: a unit stops taking new pairs after pairMaxRows, and what it could not keep shows as "(others)" in the lists.
//
// A file is a gzip stream: a string table (each client and name once), then the rows (kind, client, name, count) as
// varints.  Files older than the statistics' retention are deleted as new ones are written.  They hold client
// addresses and the names asked for, so the directory and the files are private to root (0700 / 0600).

const (
	pairUnitMin = 10        // minutes per file; the finest top-list slot (qsFineSpan) is one unit
	pairMaxRows = 400000    // most distinct pairs a unit keeps; a flood of random names stops here
	pairMaxStrs = 1 << 20   // most distinct strings a unit keeps
	pairMaxRead = 256 << 20 // most a file may hold decompressed
	pairMagic   = "PR1\n"
	pairKindSh  = 56
	pairIdMask  = 1<<28 - 1
)

// pairAcc is the pairs of one unit.
type pairAcc struct {
	unit int64
	idx  map[string]uint32
	strs []string
	rows map[uint64]uint32 // kind<<56 | client<<28 | name  →  count
}

func newPairAcc(unit int64) *pairAcc {
	return &pairAcc{unit: unit, idx: map[string]uint32{}, rows: map[uint64]uint32{}}
}

func (a *pairAcc) id(s string) (uint32, bool) {
	if i, ok := a.idx[s]; ok {
		return i, true
	}
	if len(a.strs) >= pairMaxStrs {
		return 0, false
	}
	i := uint32(len(a.strs))
	a.idx[s] = i
	a.strs = append(a.strs, s)
	return i, true
}

// add counts n queries of the pair; false when the unit is full and the pair is new.
func (a *pairAcc) add(kind int, client, name string, n uint32) bool {
	ci, ok1 := a.id(client)
	ni, ok2 := a.id(name)
	if !ok1 || !ok2 {
		return false
	}
	key := uint64(kind)<<pairKindSh | uint64(ci)<<28 | uint64(ni)
	if _, have := a.rows[key]; !have && len(a.rows) >= pairMaxRows {
		return false
	}
	a.rows[key] += n
	return true
}

func (a *pairAcc) each(fn func(kind int, client, name string, n uint32)) {
	for key, n := range a.rows {
		fn(int(key>>pairKindSh), a.strs[key>>28&pairIdMask], a.strs[key&pairIdMask], n)
	}
}

func (a *pairAcc) encode() []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	bw := bufio.NewWriterSize(zw, 1<<16)
	bw.WriteString(pairMagic)
	var tmp [binary.MaxVarintLen64]byte
	put := func(v uint64) { bw.Write(tmp[:binary.PutUvarint(tmp[:], v)]) }
	put(uint64(len(a.strs)))
	for _, s := range a.strs {
		put(uint64(len(s)))
		bw.WriteString(s)
	}
	put(uint64(len(a.rows)))
	for key, n := range a.rows {
		bw.WriteByte(byte(key >> pairKindSh))
		put(key >> 28 & pairIdMask)
		put(key & pairIdMask)
		put(uint64(n))
	}
	bw.Flush()
	zw.Close()
	return buf.Bytes()
}

// decodePairs reads a unit's file, calling fn for each row.  A damaged file gives what was read before the damage and the error.
func decodePairs(b []byte, fn func(kind int, client, name string, n uint32)) error {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return err
	}
	r := bufio.NewReaderSize(io.LimitReader(zr, pairMaxRead), 1<<16)
	magic := make([]byte, len(pairMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != pairMagic {
		return errors.New("not a pairs file")
	}
	ns, err := binary.ReadUvarint(r)
	if err != nil || ns > pairMaxStrs {
		return errors.New("bad pairs file")
	}
	strs := make([]string, ns)
	for i := range strs {
		l, err := binary.ReadUvarint(r)
		if err != nil || l > 1024 {
			return errors.New("bad pairs file")
		}
		sb := make([]byte, l)
		if _, err := io.ReadFull(r, sb); err != nil {
			return err
		}
		strs[i] = string(sb)
	}
	nr, err := binary.ReadUvarint(r)
	if err != nil || nr > pairMaxRows {
		return errors.New("bad pairs file")
	}
	for i := uint64(0); i < nr; i++ {
		kind, err := r.ReadByte()
		if err != nil {
			return err
		}
		ci, err1 := binary.ReadUvarint(r)
		ni, err2 := binary.ReadUvarint(r)
		n, err3 := binary.ReadUvarint(r)
		if err1 != nil || err2 != nil || err3 != nil || ci >= ns || ni >= ns || int(kind) >= qcCount {
			return errors.New("bad pairs file")
		}
		fn(int(kind), strs[ci], strs[ni], uint32(n))
	}
	return nil
}

// pairStore keeps the pairs: the open unit in memory, the finished ones in files (dir) or, with no directory, in memory too
// (tests, and the time before the daemon has told it where its state is).
type pairStore struct {
	mu  sync.Mutex
	dir string
	mem map[int64][]byte
	cur *pairAcc
}

func newPairStore() *pairStore { return &pairStore{mem: map[int64][]byte{}} }

func (p *pairStore) file(unit int64) string {
	return filepath.Join(p.dir, "p"+strconv.FormatInt(unit, 10)+".gz")
}

// setDir moves the store to a directory (private to root).  Units kept in memory until now are written to it.
func (p *pairStore) setDir(dir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		warnf("statistics: pairs directory %s: %v (kept in memory)", dir, err)
		return
	}
	p.dir = dir
	for u, b := range p.mem {
		p.put(u, b)
	}
	p.mem = nil
}

func (p *pairStore) put(unit int64, b []byte) {
	if p.dir == "" {
		p.mem[unit] = b
		return
	}
	tmp := p.file(unit) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		warnf("statistics: saving pairs: %v", err)
		return
	}
	if err := os.Rename(tmp, p.file(unit)); err != nil {
		warnf("statistics: saving pairs: %v", err)
		os.Remove(tmp)
	}
}

func (p *pairStore) get(unit int64) []byte {
	if p.dir == "" {
		return p.mem[unit]
	}
	b, err := os.ReadFile(p.file(unit))
	if err != nil {
		return nil
	}
	return b
}

func (p *pairStore) has(unit int64) bool {
	if p.dir == "" {
		_, ok := p.mem[unit]
		return ok
	}
	_, err := os.Stat(p.file(unit))
	return err == nil
}

// prune deletes the units before oldest.
func (p *pairStore) prune(oldest int64) {
	if p.dir == "" {
		for u := range p.mem {
			if u < oldest {
				delete(p.mem, u)
			}
		}
		return
	}
	ents, err := os.ReadDir(p.dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		n := e.Name()
		if strings.HasSuffix(n, ".tmp") {
			continue
		}
		if u, ok := parsePairName(n); ok && u < oldest {
			os.Remove(filepath.Join(p.dir, n))
		}
	}
}

func parsePairName(n string) (int64, bool) {
	if !strings.HasPrefix(n, "p") || !strings.HasSuffix(n, ".gz") {
		return 0, false
	}
	u, err := strconv.ParseInt(n[1:len(n)-3], 10, 64)
	return u, err == nil
}

// open makes the accumulator of a unit, starting from its file if there is one (the daemon was restarted inside it).
func (p *pairStore) open(unit int64) *pairAcc {
	a := newPairAcc(unit)
	if b := p.get(unit); b != nil {
		decodePairs(b, func(kind int, c, n string, v uint32) { a.add(kind, c, n, v) })
	}
	return a
}

// add counts n queries of a pair at time t (unix seconds).  A finished unit is written out when the first pair of a later one arrives.
func (p *pairStore) add(t int64, kind int, client, name string, n uint32) {
	unit := t / 60 / pairUnitMin
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur == nil || unit > p.cur.unit {
		if p.cur != nil {
			p.put(p.cur.unit, p.cur.encode())
			p.prune(unit - int64(qsRetain/60/pairUnitMin/1e9)) // qsRetain is a time.Duration
		}
		p.cur = p.open(unit)
	}
	p.cur.add(kind, client, name, n) // an event for an earlier unit (rare, at a boundary) is counted in the open one
}

// saveOpen writes the open unit to its file, so a restart loses nothing but the last few minutes' worth.
func (p *pairStore) saveOpen() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur != nil && len(p.cur.rows) > 0 {
		p.put(p.cur.unit, p.cur.encode())
	}
}

// scan calls fn for every pair counted in the units from..to (inclusive).
func (p *pairStore) scan(from, to int64, fn func(kind int, client, name string, n uint32)) {
	for u := from; u <= to; u++ {
		p.mu.Lock()
		if p.cur != nil && p.cur.unit == u {
			p.cur.each(fn)
			p.mu.Unlock()
			continue
		}
		b := p.get(u)
		p.mu.Unlock()
		if b != nil {
			decodePairs(b, fn)
		}
	}
}

// importUnit adds pairs saved by an older version (in the statistics file) as the unit's file, unless the unit has one already.
func (p *pairStore) importUnit(unit int64, tops []pKind) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.has(unit) || (p.cur != nil && p.cur.unit == unit) {
		return
	}
	a := newPairAcc(unit)
	for ci := range tops {
		for c, dm := range tops[ci].Pairs {
			for n, v := range dm {
				a.add(ci, c, n, v)
			}
		}
	}
	if len(a.rows) > 0 {
		p.put(unit, a.encode())
	}
}

// clear forgets every pair, on disk too.
func (p *pairStore) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cur = nil
	p.mem = map[int64][]byte{}
	if p.dir == "" {
		return
	}
	p.mem = nil
	ents, _ := os.ReadDir(p.dir)
	for _, e := range ents {
		if _, ok := parsePairName(e.Name()); ok || strings.HasSuffix(e.Name(), ".tmp") {
			os.Remove(filepath.Join(p.dir, e.Name()))
		}
	}
}
