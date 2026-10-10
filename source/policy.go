package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// Policy-Based Resolution (the dns block's "policy", shared by every gateway): which servers a query goes to depends
// on who asks and what is asked.  The rows are read from the top; the first row whose client matches the asking
// client and whose name matches the queried name sends the query to its own servers instead of the pool's.  A query
// no row matches goes to the pool as before.
//
//	client         name          servers                    destination name
//	10.1.1.1       reddit.com    10.2.2.2, 10.3.3.3         zdnet.com
//	10.10.10.0/24  *.reddit.com  10.10.10.1, 10.10.10.10    *.hardocp.com
//	*              *.reddit.com  8.8.8.8, 8.8.4.4           (blank: the name asked)
//
// client is "*" (or "any"), an address, a network, or several of them separated by commas.  name is "*", a name
// (exactly that name), or "*.name" (that name and every name below it).  servers are written like the pool's
// destination name, when given, is the name the servers are asked for instead (rename.go): "zdnet.com", or
// "*.hardocp.com" for a "*.name" source, which keeps the labels in front of it.  The client's answer is turned
// back into one for the name it asked.
// servers (address, address:port, tls://host, https://host/path), tried in the order given; one that failed on its
// last query is tried after the others.  Those servers are not probed, so they are never marked down.
// When none of them answers, the client gets SERVFAIL: the query does not fall back to the pool.  Answers are cached
// per row's server list, so a client sent to one set never gets an answer cached for another.

// policyMaxRows is how many rows the table holds.
const policyMaxRows = 10000

// PolicyRule is one row of the table.
type PolicyRule struct {
	Client  string   `json:"client"`
	Name    string   `json:"name"`
	Servers []string `json:"servers"`
	// Dest is the name the servers are asked for instead of the client's ("" or "*": the client's own); "*.name" for
	// a "*.name" Name keeps the labels in front.  The answer is turned back to the client's name (rename.go).
	Dest string `json:"dest,omitempty"`
}

// policyRule is a row ready for matching.
type policyRule struct {
	anyClient bool
	nets      []netip.Prefix
	anyName   bool
	exact     string    // lower case, no trailing dot; empty when the row is a "*.name" one
	suffix    string    // ".name" of a "*.name" row
	glob      *nameGlob // a name with "*" inside its labels (nameglob.go); exact and suffix are empty then
	servers   []*Server
	destLit   []string // the name to ask for instead, when it is one name
	destSuf   []string // the labels after the "*" of a "*.name" destination (srcSuf: of the "*.name" it replaces)
	srcSuf    []string
	rename    bool
	rec       *localData // local records in place of servers (action polRecord)
	idx       int        // 1-based place in the table, for the log
	desc      string     // what the row does, for the log
	action    string     // a keyword in place of servers (policyAction): the row answers itself, or hands the query to the pool
	tag       string     // the servers and destination, as the cache key part for answers that came from them
}

func policyName(s string) (exact, suffix string, all bool, err error) {
	s = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
	if _, is, _ := parseGlob(s); is {
		return "", "", false, fmt.Errorf("%q: a pattern with * inside is allowed only as a source name", s)
	}
	switch {
	case s == "*" || s == "":
		return "", "", true, nil
	case strings.HasPrefix(s, "*."):
		if !validHostname(s[2:]) {
			return "", "", false, fmt.Errorf("%q is not a name like example.com or *.example.com", s)
		}
		return "", s[1:], false, nil
	}
	if !validHostname(s) {
		return "", "", false, fmt.Errorf("%q is not a name like example.com or *.example.com", s)
	}
	return s, "", false, nil
}

func policyClients(s string) (anyClient bool, nets []netip.Prefix, err error) {
	var parts []string
	for _, f := range strings.FieldsFunc(s, func(c rune) bool { return c == ',' || c == ' ' || c == '\t' }) {
		if f == "*" || strings.EqualFold(f, "any") {
			return true, nil, nil
		}
		parts = append(parts, f)
	}
	if len(parts) == 0 {
		return true, nil, nil
	}
	nets, err = parseClientNets(parts)
	return false, nets, err
}

func splitServers(list []string) []string {
	var out []string
	for _, s := range list {
		for _, f := range strings.FieldsFunc(s, func(c rune) bool { return c == ',' || c == ' ' || c == '\t' || c == '\n' }) {
			out = append(out, f)
		}
	}
	return out
}

// policyDest reads a row's destination name: lit is the name to ask for, or suf the labels after the "*" of a
// "*.name" one (then src holds those of the source name, which must be a "*.name" too).  "" and "*" leave the name.
func policyDest(srcName, dest string) (lit, suf, src []string, rename bool, err error) {
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(dest), "."))
	if d == "" || d == "*" {
		return nil, nil, nil, false, nil
	}
	exact, dsuf, all, err := policyName(d)
	if err != nil {
		return nil, nil, nil, false, err
	}
	if all {
		return nil, nil, nil, false, nil
	}
	if exact != "" {
		return splitLabels(exact), nil, nil, true, nil
	}
	_, ssuf, sall, serr := policyName(srcName)
	if serr != nil || sall || ssuf == "" {
		return nil, nil, nil, false, fmt.Errorf("%q needs a source name like *.example.com (the part in front of it is kept)", d)
	}
	return nil, splitLabels(dsuf), splitLabels(ssuf), true, nil
}

// normalizePolicy checks the rows and returns them cleaned (nil for none).  A blank client or name means "*".
func normalizePolicy(rows []PolicyRule) ([]PolicyRule, error) {
	var out []PolicyRule
	for i, r := range rows {
		r.Client, r.Name = strings.TrimSpace(r.Client), strings.TrimSpace(r.Name)
		if ld, ok := localDest(r.Dest); ok { // the answer written in the destination name column
			if len(splitServers(r.Servers)) > 0 {
				return nil, fmt.Errorf("dns: policy row %d: a local answer goes in the destination name; leave the servers blank", i+1)
			}
			r.Servers, r.Dest = []string{ld}, ""
		}
		rec := len(r.Servers) == 1 && isRecordSyntax(r.Servers[0]) // local records, written out whole
		if !rec {
			r.Servers = splitServers(r.Servers)
		}
		if r.Client == "" && r.Name == "" && len(r.Servers) == 0 {
			continue
		}
		if r.Client == "" {
			r.Client = "*"
		}
		if r.Name == "" {
			r.Name = "*"
		}
		if _, _, err := policyClients(r.Client); err != nil {
			return nil, fmt.Errorf("dns: policy row %d: client: %w", i+1, err)
		}
		if _, is, err := parseGlob(r.Name); is {
			if err != nil {
				return nil, fmt.Errorf("dns: policy row %d: name: %w", i+1, err)
			}
		} else if _, _, _, err := policyName(r.Name); err != nil {
			return nil, fmt.Errorf("dns: policy row %d: name: %w", i+1, err)
		}
		destSet := strings.TrimSpace(r.Dest) != "" && strings.TrimSpace(r.Dest) != "*"
		if rec {
			if destSet {
				return nil, fmt.Errorf("dns: policy row %d: local records take no destination name", i+1)
			}
			if _, err := parseLocal(r.Servers[0]); err != nil {
				return nil, fmt.Errorf("dns: policy row %d: %w", i+1, err)
			}
			r.Servers = []string{strings.TrimSpace(r.Servers[0])}
			r.Dest = ""
			out = append(out, r)
			continue
		}
		if policyAction(r.Servers) == polPool && destSet { // "pool" with a destination name is the same as no servers
			r.Servers = nil
		}
		if len(r.Servers) == 0 && !destSet {
			return nil, fmt.Errorf("dns: policy row %d: no servers (or a destination name, to ask the gateway's own servers for it)", i+1)
		}
		if act := policyAction(r.Servers); act != "" { // a keyword instead of servers: answered here, or left to the pool
			if len(r.Servers) != 1 || strings.TrimSpace(r.Dest) != "" && strings.TrimSpace(r.Dest) != "*" {
				return nil, fmt.Errorf("dns: policy row %d: %q stands alone: no other servers, no destination name", i+1, act)
			}
			r.Servers = []string{act}
			r.Dest = ""
			out = append(out, r)
			continue
		}
		if _, _, _, _, err := policyDest(r.Name, r.Dest); err != nil {
			return nil, fmt.Errorf("dns: policy row %d: destination name: %w", i+1, err)
		}
		r.Dest = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Dest), "."))
		if r.Dest == "*" {
			r.Dest = ""
		}
		if len(r.Servers) > 16 {
			return nil, fmt.Errorf("dns: policy row %d: at most 16 servers", i+1)
		}
		for _, s := range r.Servers {
			if _, err := normalizeServer(s); err != nil {
				return nil, fmt.Errorf("dns: policy row %d: %w", i+1, err)
			}
		}
		out = append(out, r)
	}
	if len(out) > policyMaxRows {
		return nil, fmt.Errorf("dns: policy: at most %d rows", policyMaxRows)
	}
	return out, nil
}

// buildPolicy makes the matchable rows; a server address used by several rows is one Server, so a failure shows to all.
func buildPolicy(rows []PolicyRule) []policyRule {
	var out []policyRule
	have := map[string]*Server{}
	for ri, r := range rows {
		pr := policyRule{idx: ri + 1}
		var err error
		if pr.anyClient, pr.nets, err = policyClients(r.Client); err != nil {
			continue // validated when the config was loaded
		}
		if g, is, _ := parseGlob(r.Name); is {
			if g == nil {
				continue
			}
			pr.glob = g
		} else if pr.exact, pr.suffix, pr.anyName, err = policyName(r.Name); err != nil {
			continue
		}
		servers := r.Servers
		if ld, ok := localDest(r.Dest); ok && len(splitServers(servers)) == 0 {
			servers = []string{ld}
		}
		if len(servers) == 1 && isRecordSyntax(servers[0]) {
			d, err := parseLocal(servers[0])
			if err != nil {
				continue
			}
			pr.rec, pr.action, pr.tag = d, polRecord, polRecord
			pr.desc = "answered " + strings.TrimSpace(servers[0])
			out = append(out, pr)
			continue
		}
		if d := strings.TrimSpace(r.Dest); policyAction(servers) == polPool && d != "" && d != "*" {
			servers = nil // "pool" with a destination name is the pool's servers asked for that name
		}
		if act := policyAction(servers); act != "" {
			pr.action, pr.tag = act, act
			pr.desc = "answered " + act
			if act == polPool {
				pr.desc = "left to the gateway's servers"
			}
			out = append(out, pr)
			continue
		}
		var addrs []string
		for _, s := range servers {
			a, err := normalizeServer(s)
			if err != nil {
				continue
			}
			sv := have[a]
			if sv == nil {
				sv = &Server{Addr: a, healthy: true, hist: srvhist.series(a)}
				have[a] = sv
			}
			pr.servers = append(pr.servers, sv)
			addrs = append(addrs, a)
		}
		pr.destLit, pr.destSuf, pr.srcSuf, pr.rename, _ = policyDest(r.Name, r.Dest)
		if len(pr.servers) == 0 && !pr.rename {
			continue
		}
		pr.tag = strings.Join(addrs, ",")
		if len(pr.servers) == 0 {
			pr.tag = "pool" // no servers of its own: the gateway's, asked for the destination name
		}
		pr.desc = "sent to " + pr.tag
		if pr.rename {
			pr.tag += "=>" + strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Dest), "."))
			pr.desc += ", asked as " + strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Dest), "."))
		}
		out = append(out, pr)
	}
	return out
}

func (r *policyRule) clientMatches(client netip.Addr) bool {
	if r.anyClient {
		return true
	}
	for _, n := range r.nets {
		if n.Contains(client) {
			return true
		}
	}
	return false
}

func (r *policyRule) matches(client netip.Addr, name string) bool {
	if !r.clientMatches(client) {
		return false
	}
	switch {
	case r.anyName:
		return true
	case r.glob != nil:
		return r.glob.match(strings.ToLower(name))
	case r.exact != "":
		return strings.EqualFold(name, r.exact)
	}
	// "*.name" is the name itself and every name below it
	return strings.EqualFold(name, r.suffix[1:]) || (len(name) > len(r.suffix) && strings.EqualFold(name[len(name)-len(r.suffix):], r.suffix))
}

// policyIndex finds the rows that can match a name without looking at every row: the rows are kept by the name they
// name (exact), by the name a "*.name" row starts at (suffix), or in a list of those for every name.
type policyIndex struct {
	exact, suffix map[string][]int
	gtail         map[string][]int // glob rows, by the literal labels at the end of the pattern
	all           []int
}

func buildPolicyIndex(rows []policyRule) *policyIndex {
	ix := &policyIndex{exact: map[string][]int{}, suffix: map[string][]int{}, gtail: map[string][]int{}}
	for i := range rows {
		r := &rows[i]
		switch {
		case r.anyName:
			ix.all = append(ix.all, i)
		case r.glob != nil && r.glob.tail != "":
			ix.gtail[r.glob.tail] = append(ix.gtail[r.glob.tail], i)
		case r.glob != nil:
			ix.all = append(ix.all, i) // no literal end: checked for every name
		case r.exact != "":
			ix.exact[r.exact] = append(ix.exact[r.exact], i)
		default:
			ix.suffix[r.suffix[1:]] = append(ix.suffix[r.suffix[1:]], i)
		}
	}
	return ix
}

func (p *Pool) pindex() *policyIndex {
	p.pidxOnce.Do(func() { p.pidx = buildPolicyIndex(p.policy) })
	return p.pidx
}

// lookup returns the first row, in table order, that applies to a query from client for the question in qi, whatever
// it does (a "pool" row included), or nil.  Queries the daemon makes itself (no client) never match.
func (p *Pool) lookup(client netip.Addr, qi *qinfo) *policyRule {
	if len(p.policy) == 0 || !client.IsValid() || !qi.ok {
		return nil
	}
	client = client.Unmap()
	name := strings.ToLower(strings.TrimSuffix(qi.name(), "."))
	ix := p.pindex()
	lists := make([][]int, 0, 8)
	if l := ix.exact[name]; len(l) > 0 {
		lists = append(lists, l)
	}
	for s := name; ; {
		if l := ix.suffix[s]; len(l) > 0 {
			lists = append(lists, l)
		}
		if l := ix.gtail[s]; len(l) > 0 {
			lists = append(lists, l)
		}
		i := strings.IndexByte(s, '.')
		if i < 0 {
			break
		}
		s = s[i+1:]
	}
	if len(ix.all) > 0 {
		lists = append(lists, ix.all)
	}
	pos := make([]int, len(lists))
	for { // the candidates in table order: each list is in order, so take the smallest head
		best := -1
		for k := range lists {
			if pos[k] < len(lists[k]) && (best < 0 || lists[k][pos[k]] < lists[best][pos[best]]) {
				best = k
			}
		}
		if best < 0 {
			return nil
		}
		i := lists[best][pos[best]]
		pos[best]++
		if g := p.policy[i].glob; g != nil && !g.match(name) {
			continue
		}
		if p.policy[i].clientMatches(client) {
			return &p.policy[i]
		}
	}
}

// policyFor is lookup, except that a row that leaves the query to the pool gives nil (no later row is looked at).
func (p *Pool) policyFor(client netip.Addr, qi *qinfo) *policyRule {
	if r := p.lookup(client, qi); r != nil && r.action != polPool {
		return r
	}
	return nil
}

// logPolicy writes one line for a query a row applied to (at most 100 a second; the rest are counted).
func (p *Pool) logPolicy(r *policyRule, client netip.Addr, qi *qinfo) {
	if !p.cfg.PolicyLog {
		return
	}
	now := time.Now().Unix()
	if p.logSec.Swap(now) != now {
		if s := p.logSupp.Swap(0); s > 0 {
			infof("dns: policy: %d more match(es) in the last second not logged", s)
		}
		p.logN.Store(0)
	}
	if p.logN.Add(1) > 100 {
		p.logSupp.Add(1)
		return
	}
	infof("dns: policy row %d: %s asked %s %s: %s", r.idx, client.Unmap(), strings.TrimSuffix(qi.name(), "."), qtypeName(qi.qtype), r.desc)
}

// A row whose servers are one of these words is not sent anywhere.
const (
	polPool     = "pool"     // leave the query to the gateway's own servers (an exception to a later row)
	polNull     = "null"     // A 0.0.0.0 and AAAA ::, "no data" for every other type
	polNXDomain = "nxdomain" // the name does not exist
	polNoData   = "nodata"   // the name exists but has no records of that type
	polRefused  = "refused"
)

// policyAction returns the keyword a row's servers consist of (lower case), or "" when they are servers.
func policyAction(servers []string) string {
	if len(servers) == 0 {
		return ""
	}
	for _, s := range servers {
		switch a := strings.ToLower(strings.TrimSpace(s)); a {
		case polPool, polNull, polNXDomain, polNoData, polRefused:
			return a
		}
	}
	return ""
}

// localAnswer is the row's own answer to query.
func (r *policyRule) localAnswer(ctx context.Context, p *Pool, query []byte, qi *qinfo) []byte {
	switch r.action {
	case polRecord:
		return r.recordAnswer(ctx, p, query, qi)
	case polNXDomain:
		return errorResponse(query, rcodeNXDomain)
	case polRefused:
		return errorResponse(query, rcodeRefused)
	case polNull:
		if qi.qtype == typeA || qi.qtype == typeAAAA {
			resp := errorResponse(query, rcodeNoError)
			if resp != nil && len(resp) > 12 {
				n := 4
				if qi.qtype == typeAAAA {
					n = 16
				}
				binary.BigEndian.PutUint16(resp[6:], 1)
				resp = append(resp, 0xC0, 0x0C, byte(qi.qtype>>8), byte(qi.qtype), 0, 1, 0, 0, 0, 60, 0, byte(n))
				return append(resp, make([]byte, n)...)
			}
			return resp
		}
	}
	return errorResponse(query, rcodeNoError) // nodata, and null for a type that has no null address
}

// order is the row's servers in the order to try them: as listed, those whose last query failed after the others.
func (r *policyRule) order() []*Server {
	out := make([]*Server, 0, len(r.servers))
	var failed []*Server
	for _, s := range r.servers {
		if s.failing.Load() {
			failed = append(failed, s)
		} else {
			out = append(out, s)
		}
	}
	return append(out, failed...)
}

func clonePolicy(rows []PolicyRule) []PolicyRule {
	if len(rows) == 0 {
		return nil
	}
	out := make([]PolicyRule, len(rows))
	for i, r := range rows {
		r.Servers = append([]string(nil), r.Servers...)
		out[i] = r
	}
	return out
}
