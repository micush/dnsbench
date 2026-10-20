package main

import (
	"encoding/binary"
	"math/rand"
	"strings"
	"testing"
)

// questionOfRef is the straightforward reading questionOf replaced; the fast one must agree with it on every
// input, including malformed ones.
func questionOfRef(q []byte) (name string, qtype uint16, ok bool) {
	if len(q) < 12 || binary.BigEndian.Uint16(q[4:]) < 1 {
		return "", 0, false
	}
	var sb strings.Builder
	i := 12
	for {
		if i >= len(q) {
			return "", 0, false
		}
		l := int(q[i])
		if l == 0 {
			i++
			break
		}
		if l&0xC0 != 0 || i+1+l > len(q) {
			return "", 0, false
		}
		if sb.Len() > 0 {
			sb.WriteByte('.')
		}
		sb.Write(q[i+1 : i+1+l])
		i += l + 1
		if sb.Len() > 255 {
			return "", 0, false
		}
	}
	if i+2 > len(q) {
		return "", 0, false
	}
	name = strings.ToLower(sb.String())
	if name == "" {
		name = "."
	}
	return name, binary.BigEndian.Uint16(q[i:]), true
}

func TestQuestionOfMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for it := 0; it < 200000; it++ {
		q := make([]byte, 12)
		q[5] = byte(rng.Intn(3))
		for l := rng.Intn(9); l > 0; l-- {
			n := rng.Intn(70)
			if rng.Intn(20) == 0 {
				n = 0xC0 | rng.Intn(64)
			}
			q = append(q, byte(n))
			for k := 0; k < n && k < 70; k++ {
				c := byte('A' + rng.Intn(60))
				q = append(q, c)
			}
		}
		if rng.Intn(10) != 0 {
			q = append(q, 0)
		}
		q = append(q, byte(rng.Intn(3)), byte(rng.Intn(40)), 0, 1)
		q = q[:len(q)-rng.Intn(3)]
		n1, t1, ok1 := questionOf(q)
		n2, t2, ok2 := questionOfRef(q)
		if n1 != n2 || t1 != t2 || ok1 != ok2 {
			t.Fatalf("%x: got %q %d %v, want %q %d %v", q, n1, t1, ok1, n2, t2, ok2)
		}
	}
	if n, _, ok := questionOf(cacheQuery("WwW.Example.COM", 1, nil)); !ok || n != "www.example.com" {
		t.Fatalf("%q %v", n, ok)
	}
	if n, _, ok := questionOf(cacheQuery("", 1, nil)); !ok || n != "." {
		t.Fatalf("root: %q %v", n, ok)
	}
}
