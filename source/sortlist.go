package main

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// Sort list (the dns block's "sortlist", shared by every gateway): per-client-network ordering of the address
// records in an answer.  Each entry is
//
//	client-network: preferred-network, preferred-network, ...
//
// e.g. "10.1.0.0/16: 10.1.0.0/16, 10.0.0.0/8".  The first entry whose client network contains the asking client is
// used; addresses inside the client network itself come first, then the A and AAAA records of the answer are put in the order of the preferred networks (an address in the first
// network first, then the second, ...); addresses in none of them keep their order after those.  The sort is stable.
// The client network may be "any".  An entry that is just a network ("10.20.0.0/16") is for every client: all such
// entries together are listed in the order given, and the one the client is in comes first.  Answers from the cache are sorted per client as they are sent, so the cache
// itself is not touched.

const typeCNAME = 5

type sortRule struct {
	client netip.Prefix // zero value with any set matches every client
	any    bool
	bare   bool // a plain network in the list: for every client, the networks of all such entries, the client's own first
	prefs  []netip.Prefix
}

// parseSortRule reads one sortlist entry.
func parseSortRule(s string) (sortRule, error) {
	var r sortRule
	if !strings.Contains(s, ": ") && !strings.HasPrefix(strings.ToLower(s), "any:") { // a plain network
		nets, err := parseClientNets([]string{strings.TrimSpace(s)})
		if err != nil {
			return r, fmt.Errorf("sortlist: %w (an entry is a network, or client-network: preferred-network, ...)", err)
		}
		return sortRule{any: true, bare: true, prefs: nets}, nil
	}
	i := strings.Index(s, ":")
	// an IPv6 client network contains colons: the separator is the last colon followed by a space or the end of a
	// network, so look for ": " first and fall back to the only-colon case for "any:" style entries
	if j := strings.Index(s, ": "); j >= 0 {
		i = j
	}
	cl := strings.TrimSpace(s[:i])
	rest := s[i+1:]
	if strings.EqualFold(cl, "any") {
		r.any = true
	} else {
		nets, err := parseClientNets([]string{cl})
		if err != nil {
			return r, fmt.Errorf("sortlist %q: %w", s, err)
		}
		r.client = nets[0]
	}
	var prefs []string
	for _, f := range strings.FieldsFunc(rest, func(c rune) bool { return c == ',' || c == ' ' || c == '\t' }) {
		prefs = append(prefs, f)
	}
	if len(prefs) == 0 {
		return r, fmt.Errorf("sortlist %q: no preferred networks after the colon", s)
	}
	nets, err := parseClientNets(prefs)
	if err != nil {
		return r, fmt.Errorf("sortlist %q: %w", s, err)
	}
	r.prefs = nets
	return r, nil
}

func (r sortRule) String() string {
	if r.bare {
		return r.prefs[0].String()
	}
	cl := "any"
	if !r.any {
		cl = r.client.String()
	}
	ps := make([]string, len(r.prefs))
	for i, p := range r.prefs {
		ps[i] = p.String()
	}
	return cl + ": " + strings.Join(ps, ", ")
}

// normalizeSortList checks the entries and returns them in canonical form (nil for none).
func normalizeSortList(list []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		r, err := parseSortRule(s)
		if err != nil {
			return nil, fmt.Errorf("dns: sortlist: %w", err)
		}
		if n := r.String(); !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) > 1000 {
		return nil, fmt.Errorf("dns: sortlist: at most 1000 entries")
	}
	return out, nil
}

func buildSortRules(list []string) []sortRule {
	var out []sortRule
	var bare []netip.Prefix
	for _, s := range list {
		r, err := parseSortRule(s) // validated when the config was loaded
		switch {
		case err != nil:
		case r.bare:
			bare = append(bare, r.prefs...)
		default:
			out = append(out, r)
		}
	}
	if len(bare) > 0 { // the plain networks together are one rule for every client, after the explicit ones
		out = append(out, sortRule{any: true, bare: true, prefs: bare})
	}
	return out
}

func (p *Pool) sortRuleFor(client netip.Addr) *sortRule {
	client = client.Unmap()
	for i := range p.sorts {
		r := &p.sorts[i]
		if r.any || r.client.Contains(client) {
			return r
		}
	}
	return nil
}

// sortAnswer returns resp with its A and AAAA answers ordered for the client, or resp itself when no rule applies or
// there is nothing to change.  resp is never modified (it may be a cached message).
func (p *Pool) sortAnswer(resp []byte, client netip.Addr) []byte {
	if len(p.sorts) == 0 || len(resp) < 12 || !client.IsValid() {
		return resp
	}
	client = client.Unmap()
	r := p.sortRuleFor(client)
	if r == nil {
		return resp
	}
	if binary.BigEndian.Uint16(resp[2:])&0x8000 == 0 || resp[3]&0x0F != 0 { // not a response, or an error
		return resp
	}
	rrs, addStart, ok := recordsAfterQuestion(resp)
	an := int(binary.BigEndian.Uint16(resp[6:]))
	if !ok || an < 2 || addStart < an {
		return resp
	}
	// movable: the A and AAAA records of the answer section.  Everything else after the question must be answer
	// CNAMEs, or an OPT record in the additional section, so that no compression pointer can aim into the moved area.
	var idx []int
	for i, rr := range rrs {
		switch {
		case i < an && (rr.typ == typeA || rr.typ == typeAAAA) && rr.class == 1 &&
			((rr.typ == typeA && rr.rdlen == 4) || (rr.typ == typeAAAA && rr.rdlen == 16)):
			idx = append(idx, i)
		case i < an && rr.typ == typeCNAME:
		case i >= an && rr.typ == typeOPT:
		default:
			return resp
		}
	}
	if len(idx) < 2 {
		return resp
	}
	lo, hi := rrs[idx[0]].start, rrs[idx[len(idx)-1]].end
	// the records between the first and last address record must all be addresses (so they can be permuted as a
	// block), and no name after the question may point into that block
	if idx[len(idx)-1]-idx[0] != len(idx)-1 {
		return resp
	}
	for _, rr := range rrs {
		if ptrInto(resp, rr.start, lo, hi) && !(rr.start >= lo && rr.end <= hi) {
			return resp
		}
		if rr.typ == typeCNAME && ptrInto(resp, rr.rdataOff, lo, hi) {
			return resp
		}
	}
	type item struct {
		rank int
		raw  []byte
	}
	items := make([]item, len(idx))
	for k, i := range idx {
		rr := rrs[i]
		a, _ := netip.AddrFromSlice(resp[rr.rdataOff:rr.end])
		items[k] = item{rank: r.rank(a.Unmap(), client), raw: resp[rr.start:rr.end]}
	}
	if sort.SliceIsSorted(items, func(a, b int) bool { return items[a].rank < items[b].rank }) {
		return resp
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].rank < items[b].rank })
	out := make([]byte, 0, len(resp))
	out = append(out, resp[:lo]...)
	for _, it := range items {
		out = append(out, it.raw...)
	}
	out = append(out, resp[hi:]...)
	return out
}

// rank is 0 for an address inside the client network itself, then 1 + the index of the first preferred network
// holding it, or len(prefs)+1 when none does.
func (r *sortRule) rank(a, client netip.Addr) int {
	if !r.any && r.client.Contains(a) { // the client's own network always comes first
		return 0
	}
	if r.bare { // the listed network the client is in comes first
		for _, p := range r.prefs {
			if p.Contains(client) {
				if p.Contains(a) {
					return 0
				}
				break
			}
		}
	}
	for i, p := range r.prefs {
		if p.Contains(a) {
			return i + 1
		}
	}
	return len(r.prefs) + 1
}

// ptrInto reports whether the name at off ends in a compression pointer aimed into [lo, hi).
func ptrInto(b []byte, off, lo, hi int) bool {
	for steps := 0; off < len(b) && steps < 130; steps++ {
		l := int(b[off])
		switch {
		case l == 0:
			return false
		case l&0xC0 == 0xC0:
			if off+2 > len(b) {
				return false
			}
			t := int(binary.BigEndian.Uint16(b[off:]) & 0x3FFF)
			return t >= lo && t < hi
		case l&0xC0 != 0:
			return false
		}
		off += 1 + l
	}
	return false
}
