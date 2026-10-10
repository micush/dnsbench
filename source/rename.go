package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// The destination name of a policy row (policy.go): the servers are asked for another name than the client asked
// for, and the answer is turned back so that it reads as the answer to the client's own question.
//
//	client asks www.reddit.com  → servers are asked www.zdnet.com (row "*.reddit.com" → "*.zdnet.com")
//	                            or zdnet.com                       (row "*.reddit.com" → "zdnet.com")
//	servers answer zdnet.com    → the client gets an answer for www.reddit.com
//
// In the answer the question is the client's own, and every name equal to the name that was asked (and, for a
// "*.name" destination, the name itself and every name below it) is written as the client's name.  The records are otherwise as the
// servers sent them.  A signed answer (DNSSEC) is not valid for the client's name.  A record of a type that is not
// known to carry no names is not passed through (the client gets SERVFAIL): its names could not be moved safely.

// renamer turns one query into the query for the servers and their answer back into the client's.
type renamer struct {
	sent, orig []string // labels of the name asked of the servers, and of the name the client asked (as it wrote it)
	dSuf, sSuf []string // labels after the "*" of a "*.name" destination, and of the "*.name" source it replaces
}

func splitLabels(s string) []string {
	s = strings.Trim(s, ".")
	if s == "" {
		return nil
	}
	return strings.Split(s, ".")
}

func labelsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

func hasLabelSuffix(l, suf []string) bool {
	return len(l) >= len(suf) && labelsEqual(l[len(l)-len(suf):], suf)
}

// unpackName reads the name at off (following compression pointers); next is the offset after it in place.
func unpackName(b []byte, off int) (labels []string, next int, ok bool) {
	next = -1
	for hops := 0; hops < 64; {
		if off >= len(b) {
			return nil, 0, false
		}
		l := int(b[off])
		switch {
		case l == 0:
			if next < 0 {
				next = off + 1
			}
			return labels, next, true
		case l&0xC0 == 0xC0:
			if off+2 > len(b) {
				return nil, 0, false
			}
			if next < 0 {
				next = off + 2
			}
			off = int(binary.BigEndian.Uint16(b[off:]) & 0x3FFF)
			hops++
		case l&0xC0 != 0:
			return nil, 0, false
		default:
			if off+1+l > len(b) {
				return nil, 0, false
			}
			labels = append(labels, string(b[off+1:off+1+l]))
			off += 1 + l
		}
	}
	return nil, 0, false
}

// msgWriter builds a message, compressing the names it writes against the ones already in it.
type msgWriter struct {
	buf  []byte
	seen map[string]int
}

func (w *msgWriter) name(labels []string, compress bool) {
	for i := range labels {
		key := strings.ToLower(strings.Join(labels[i:], "."))
		if compress {
			if at, ok := w.seen[key]; ok {
				w.buf = append(w.buf, 0xC0|byte(at>>8), byte(at))
				return
			}
			if len(w.buf) < 0x3FFF {
				w.seen[key] = len(w.buf)
			}
		}
		w.buf = append(w.buf, byte(len(labels[i])))
		w.buf = append(w.buf, labels[i]...)
	}
	w.buf = append(w.buf, 0)
}

func nameLen(labels []string) int {
	n := 1
	for _, l := range labels {
		n += 1 + len(l)
	}
	return n
}

// newRenamer works out the name to ask for.  q is the client's query (one question), r the matching policy row.
func (r *policyRule) newRenamer(q []byte, qi *qinfo) (*renamer, []byte, error) {
	if !qi.ok || len(q) < 12 || binary.BigEndian.Uint16(q[4:]) != 1 {
		return nil, q, nil // not a plain one-question query: sent as it is
	}
	orig, end, ok := unpackName(q, 12)
	if !ok || end != qi.nameEnd {
		return nil, q, nil
	}
	rn := &renamer{orig: orig, dSuf: r.destSuf, sSuf: r.srcSuf}
	switch {
	case r.destSuf != nil: // "*.name" for "*.name": the labels in front are kept, and the name itself maps to the name itself
		if !hasLabelSuffix(orig, r.srcSuf) {
			return nil, q, nil
		}
		rn.sent = append(append([]string(nil), orig[:len(orig)-len(r.srcSuf)]...), r.destSuf...)
	default:
		rn.sent = r.destLit
	}
	if nameLen(rn.sent) > 255 {
		return nil, q, errors.New("policy: the destination name is too long for this query")
	}
	w := &msgWriter{buf: append([]byte(nil), q[:12]...)}
	w.name(rn.sent, false)
	w.buf = append(w.buf, q[qi.nameEnd:]...)
	return rn, w.buf, nil
}

func (rn *renamer) mapName(l []string) []string {
	if labelsEqual(l, rn.sent) {
		return rn.orig
	}
	if rn.dSuf != nil && hasLabelSuffix(l, rn.dSuf) {
		return append(append([]string(nil), l[:len(l)-len(rn.dSuf)]...), rn.sSuf...)
	}
	return l
}

// rdata types that hold no domain names, which are passed through as they are
var plainRData = map[uint16]bool{1: true, 28: true, 16: true, 41: true, 43: true, 46: true, 47: true, 48: true, 50: true, 51: true,
	52: true, 44: true, 99: true, 257: true, 29: true, 13: true, 64: true, 65: true, 33: true, 39: true, 35: true, 256: true, 37: true, 59: true, 60: true}

// response turns the servers' answer into the answer to the client's question.
func (rn *renamer) response(resp []byte) ([]byte, error) {
	if len(resp) < 12 {
		return resp, nil
	}
	qd := int(binary.BigEndian.Uint16(resp[4:]))
	total := int(binary.BigEndian.Uint16(resp[6:])) + int(binary.BigEndian.Uint16(resp[8:])) + int(binary.BigEndian.Uint16(resp[10:]))
	w := &msgWriter{buf: append(make([]byte, 0, len(resp)+64), resp[:12]...), seen: map[string]int{}}
	off := 12
	for i := 0; i < qd; i++ {
		l, n, ok := unpackName(resp, off)
		if !ok || n+4 > len(resp) {
			return nil, errors.New("policy: unreadable answer")
		}
		w.name(rn.mapName(l), true)
		w.buf = append(w.buf, resp[n:n+4]...)
		off = n + 4
	}
	for i := 0; i < total; i++ {
		var err error
		if off, err = copyRR(w, resp, off, rn.mapName); err != nil {
			return nil, err
		}
	}
	if off != len(resp) {
		return nil, errors.New("policy: unreadable answer")
	}
	return w.buf, nil
}

// back is response for a possibly nil renamer (a query that was not renamed).
func (rn *renamer) back(resp []byte) ([]byte, error) {
	if rn == nil {
		return resp, nil
	}
	return rn.response(resp)
}

// copyRR writes the record at off of msg to w, naming it and the names in its data through mapName, and returns the
// offset after it.
func copyRR(w *msgWriter, resp []byte, off int, mapName func([]string) []string) (int, error) {
	l, n, ok := unpackName(resp, off)
	if !ok || n+10 > len(resp) {
		return 0, errors.New("policy: unreadable answer")
	}
	typ := binary.BigEndian.Uint16(resp[n:])
	rdl := int(binary.BigEndian.Uint16(resp[n+8:]))
	if n+10+rdl > len(resp) {
		return 0, errors.New("policy: unreadable answer")
	}
	rd := resp[n+10 : n+10+rdl]
	w.name(mapName(l), true)
	w.buf = append(w.buf, resp[n:n+8]...)
	lenAt := len(w.buf)
	w.buf = append(w.buf, 0, 0)
	start := len(w.buf)
	switch {
	case typ == 2 || typ == 5 || typ == 12: // NS, CNAME, PTR
		t, e, ok := unpackName(resp, n+10)
		if !ok || e != n+10+rdl {
			return 0, errors.New("policy: unreadable answer")
		}
		w.name(mapName(t), true)
	case typ == 15: // MX
		t, e, ok := unpackName(resp, n+12)
		if rdl < 3 || !ok || e != n+10+rdl {
			return 0, errors.New("policy: unreadable answer")
		}
		w.buf = append(w.buf, rd[:2]...)
		w.name(mapName(t), true)
	case typ == 6: // SOA
		m, e, ok := unpackName(resp, n+10)
		if !ok {
			return 0, errors.New("policy: unreadable answer")
		}
		r, e2, ok := unpackName(resp, e)
		if !ok || e2+20 != n+10+rdl {
			return 0, errors.New("policy: unreadable answer")
		}
		w.name(mapName(m), true)
		w.name(mapName(r), true)
		w.buf = append(w.buf, resp[e2:e2+20]...)
	case plainRData[typ]:
		w.buf = append(w.buf, rd...)
	default:
		return 0, fmt.Errorf("policy: a record of type %d cannot be moved to the client's name", typ)
	}
	if len(w.buf)-start > 0xFFFF {
		return 0, errors.New("policy: answer too large")
	}
	binary.BigEndian.PutUint16(w.buf[lenAt:], uint16(len(w.buf)-start))
	return n + 10 + rdl, nil
}
