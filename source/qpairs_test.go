package main

import (
	"os"
	"path/filepath"
	"testing"
)

func pairRows(p *pairStore, from, to int64) map[string]uint32 {
	got := map[string]uint32{}
	p.scan(from, to, func(kind int, c, n string, v uint32) { got[string(rune('0'+kind))+" "+c+" "+n] += v })
	return got
}

func TestPairsStayOnDisk(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pairs")
	p := newPairStore()
	p.setDir(dir)
	t0 := int64(10 * 60 * 600) // a unit boundary: unit 600
	p.add(t0, 0, "10.0.0.1", "a.example", 1)
	p.add(t0+5, 0, "10.0.0.1", "a.example", 2)
	p.add(t0+5, 1, "10.0.0.2", "b.example", 1)
	for i := 0; i < 3000; i++ { // far past the old in-memory cap
		p.add(t0+10, 0, "10.1."+fmtN(i/200)+"."+fmtN(i%200), "c.example", 1)
	}
	p.add(t0+600, 0, "10.0.0.3", "d.example", 1) // the next unit: the first is written out
	if _, err := os.Stat(filepath.Join(dir, "p600.gz")); err != nil {
		t.Fatalf("the finished unit has no file: %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "p600.gz")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode())
	}
	got := pairRows(p, 600, 601)
	if got["0 10.0.0.1 a.example"] != 3 || got["1 10.0.0.2 b.example"] != 1 || got["0 10.0.0.3 d.example"] != 1 || len(got) != 3003 {
		t.Fatalf("%d rows: %v", len(got), got["0 10.0.0.1 a.example"])
	}
	// a restart in the middle of a unit keeps what was saved
	p.saveOpen()
	q := newPairStore()
	q.setDir(dir)
	q.add(t0+601, 0, "10.0.0.3", "d.example", 1)
	if g := pairRows(q, 601, 601); g["0 10.0.0.3 d.example"] != 2 {
		t.Fatalf("after a restart: %v", g)
	}
	// clearing removes the files
	q.clear()
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("%d files left after clear", len(ents))
	}
	if g := pairRows(q, 600, 601); len(g) != 0 {
		t.Fatalf("pairs left after clear: %d", len(g))
	}
}

func TestPairsOldUnitsAreDeleted(t *testing.T) {
	p := newPairStore()
	p.add(60*10*1, 0, "10.0.0.1", "old.example", 1)
	p.add(60*10*2, 0, "10.0.0.1", "mid.example", 1)
	late := int64(60*10) * (int64(2) + int64(qsRetain/60/pairUnitMin/1e9) + 5)
	p.add(late, 0, "10.0.0.1", "new.example", 1)
	p.add(late+600, 0, "10.0.0.1", "newer.example", 1)
	if g := pairRows(p, 0, 10); len(g) != 0 {
		t.Fatalf("units past the retention are kept: %v", g)
	}
}

func TestPairsDamagedFile(t *testing.T) {
	if err := decodePairs([]byte("not gzip"), func(int, string, string, uint32) {}); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestPairsFromAnOlderFileMoveToFiles(t *testing.T) {
	s := NewQStats()
	unit := s.now().Unix() / 60 / pairUnitMin
	top := []pKind{{Clients: map[string]uint32{"10.0.0.1": 3}, Pairs: map[string]map[string]uint32{"10.0.0.1": {"a.example": 3}}}}
	if n := s.restore(pRec{K: "fine", Stamp: unit, Top: top}); n != 1 {
		t.Fatalf("restored %d", n)
	}
	if got := pairRows(s.pairs, unit, unit); got["0 10.0.0.1 a.example"] != 3 {
		t.Fatalf("the saved pairs are not in the unit: %v", got)
	}
}
