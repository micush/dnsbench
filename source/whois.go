package main

import (
	"fmt"
	"io"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"
)

// WHOIS for the Statistics page: hovering a domain asks the daemon for its registration data.
// Nothing is looked up until someone asks (the name goes to the registry's whois server, on
// TCP port 43, from this node), answers are cached, and at most whoisParallel lookups run at once.
//
// The registry is found through whois.iana.org (the TLD's "refer:" line) and asked about the
// registered name: the last two labels, or three under a few well-known second-level suffixes
// (co.uk, com.au, ...).  This is a heuristic, not a public-suffix-list lookup.

const (
	whoisIANA     = "whois.iana.org"
	whoisTimeout  = 6 * time.Second
	whoisMaxBytes = 64 << 10
	whoisParallel = 4
	whoisHitTTL   = 24 * time.Hour
	whoisMissTTL  = time.Hour
	whoisCacheMax = 2000
)

// WhoisInfo is the part of a whois answer worth a tooltip.
type WhoisInfo struct {
	Domain      string   `json:"domain"` // the registered name that was asked about
	Server      string   `json:"server"` // the whois server that answered
	Found       bool     `json:"found"`
	Registrar   string   `json:"registrar,omitempty"`
	Registrant  string   `json:"registrant,omitempty"`
	Registered  string   `json:"registered,omitempty"`
	Updated     string   `json:"updated,omitempty"`
	Expires     string   `json:"expires,omitempty"`
	NameServers []string `json:"name_servers,omitempty"`
	Note        string   `json:"note,omitempty"` // why there is nothing, when there is nothing
	// Name is the name that was hovered, and IPv4 / IPv6 the first address found for it (looked up by the daemon
	// when whois.get is answered; they are not part of the cached whois data).
	Name string `json:"name,omitempty"`
	IPv4 string `json:"ipv4,omitempty"`
	IPv6 string `json:"ipv6,omitempty"`
	// For an IP address (Kind "ip"): the address block, its name, the organisation holding it, the country and the
	// origin AS, from the regional registry.
	Kind    string `json:"kind,omitempty"`
	Network string `json:"network,omitempty"`
	NetName string `json:"netname,omitempty"`
	Org     string `json:"org,omitempty"`
	Country string `json:"country,omitempty"`
	Origin  string `json:"origin,omitempty"`
}

// whoisHostAddr maps a whois host to host:port; tests replace it.
var whoisHostAddr = func(host string) string { return net.JoinHostPort(host, "43") }

var secondLevel = map[string]bool{"co.uk": true, "org.uk": true, "ac.uk": true, "gov.uk": true, "me.uk": true, "ltd.uk": true,
	"com.au": true, "net.au": true, "org.au": true, "edu.au": true, "gov.au": true, "co.nz": true, "org.nz": true, "net.nz": true,
	"co.jp": true, "ne.jp": true, "or.jp": true, "co.kr": true, "or.kr": true, "com.br": true, "net.br": true, "org.br": true,
	"com.cn": true, "net.cn": true, "org.cn": true, "com.mx": true, "com.tr": true, "co.in": true, "net.in": true, "org.in": true,
	"co.za": true, "org.za": true, "com.sg": true, "com.hk": true, "com.tw": true, "com.ar": true, "com.co": true, "com.ua": true,
	"co.il": true, "co.id": true, "com.my": true, "com.ph": true, "com.vn": true, "co.th": true, "com.pl": true}

var privateTLDs = map[string]bool{"local": true, "lan": true, "home": true, "internal": true, "localdomain": true, "arpa": true,
	"corp": true, "intranet": true, "private": true, "invalid": true, "test": true, "example": true, "localhost": true, "home.arpa": true}

// whoisName reduces a queried name to the registered name to ask about, or says why not.
func whoisName(name string) (string, string, error) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if name == "" || name == "(others)" || len(name) > 253 || strings.ContainsAny(name, " \t\r\n/:") {
		return "", "", fmt.Errorf("%q is not a domain name", name)
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return "", "", fmt.Errorf("%s is an address, not a domain", name)
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return "", "", fmt.Errorf("%q has no public suffix, so it is not in whois", name)
	}
	tld := labels[len(labels)-1]
	if privateTLDs[tld] {
		return "", "", fmt.Errorf(".%s is a private or reserved suffix, so there is no whois record", tld)
	}
	if secondLevel[name] {
		return "", "", fmt.Errorf("%q is a public suffix, not a registered name", name)
	}
	n := 2
	if len(labels) >= 3 && secondLevel[strings.Join(labels[len(labels)-2:], ".")] {
		n = 3
	}
	if len(labels) < n {
		return "", "", fmt.Errorf("%q is a public suffix, not a registered name", name)
	}
	return strings.Join(labels[len(labels)-n:], "."), tld, nil
}

type whoisEntry struct {
	info *WhoisInfo
	err  string
	at   time.Time
	ttl  time.Duration
}

type whoisClient struct {
	mu     sync.Mutex
	cache  map[string]whoisEntry
	tlds   map[string]string // tld → whois server ("" = none)
	flight map[string]*sync.WaitGroup
	sem    chan struct{}
}

func newWhoisClient() *whoisClient {
	return &whoisClient{cache: map[string]whoisEntry{}, tlds: map[string]string{}, flight: map[string]*sync.WaitGroup{}, sem: make(chan struct{}, whoisParallel)}
}

var whois = newWhoisClient()

// raw asks one whois server one question.
func whoisRaw(server, query string) (string, error) {
	c, err := net.DialTimeout("tcp", whoisHostAddr(server), whoisTimeout)
	if err != nil {
		return "", fmt.Errorf("whois server %s is not reachable from this node (%v)", server, shortErr(err))
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(whoisTimeout))
	if _, err := io.WriteString(c, query+"\r\n"); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(c, whoisMaxBytes))
	if err != nil && len(b) == 0 {
		return "", fmt.Errorf("whois server %s: %v", server, shortErr(err))
	}
	return string(b), nil
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		return s[i+2:]
	}
	return s
}

var referRe = regexp.MustCompile(`(?mi)^\s*(?:refer|whois):\s*(\S+)`)

func (w *whoisClient) serverFor(tld string) (string, error) {
	w.mu.Lock()
	s, ok := w.tlds[tld]
	w.mu.Unlock()
	if !ok {
		resp, err := whoisRaw(whoisIANA, tld)
		if err != nil {
			return "", err
		}
		if m := referRe.FindStringSubmatch(resp); m != nil {
			s = strings.ToLower(m[1])
		}
		w.mu.Lock()
		w.tlds[tld] = s
		w.mu.Unlock()
	}
	if s == "" {
		return "", fmt.Errorf(".%s has no public whois server", tld)
	}
	return s, nil
}

// Lookup returns the whois data of a domain, from the cache when it is fresh.
func (w *whoisClient) Lookup(name string) (*WhoisInfo, error) {
	reg, tld, err := whoisName(name)
	if err != nil {
		return nil, err
	}
	return w.cached(reg, func() (*WhoisInfo, error) { return w.fetch(reg, tld) })
}

// cached answers from the cache when fresh, otherwise runs fetch once (concurrent callers wait for it), at most
// whoisParallel at a time, and caches the answer (hits for a day, misses for an hour).
func (w *whoisClient) cached(key string, fetch func() (*WhoisInfo, error)) (*WhoisInfo, error) {
	for {
		w.mu.Lock()
		if e, ok := w.cache[key]; ok && time.Since(e.at) < e.ttl {
			w.mu.Unlock()
			if e.err != "" {
				return nil, fmt.Errorf("%s", e.err)
			}
			return e.info, nil
		}
		if wg, busy := w.flight[key]; busy { // someone is asking already: wait for that answer
			w.mu.Unlock()
			wg.Wait()
			continue
		}
		wg := &sync.WaitGroup{}
		wg.Add(1)
		w.flight[key] = wg
		w.mu.Unlock()

		w.sem <- struct{}{}
		info, err := fetch()
		<-w.sem

		e := whoisEntry{info: info, at: time.Now(), ttl: whoisHitTTL}
		if err != nil {
			e.err, e.ttl = err.Error(), whoisMissTTL
		} else if !info.Found {
			e.ttl = whoisMissTTL
		}
		w.mu.Lock()
		if len(w.cache) >= whoisCacheMax {
			for k, v := range w.cache { // drop expired entries, then anything, to stay bounded
				if time.Since(v.at) >= v.ttl || len(w.cache) >= whoisCacheMax {
					delete(w.cache, k)
				}
			}
		}
		w.cache[key] = e
		delete(w.flight, key)
		w.mu.Unlock()
		wg.Done()
		return info, err
	}
}

func (w *whoisClient) fetch(reg, tld string) (*WhoisInfo, error) {
	server, err := w.serverFor(tld)
	if err != nil {
		return nil, err
	}
	q := reg
	if server == "whois.verisign-grs.com" {
		q = "domain " + reg
	}
	resp, err := whoisRaw(server, q)
	if err != nil {
		return nil, err
	}
	return parseWhois(reg, server, resp), nil
}

var noMatchRe = regexp.MustCompile(`(?i)no match|not found|no entries found|no data found|status:\s*(free|available)|is available|domain not found|no object found`)

// parseWhois picks the registrar, dates, registrant organisation and name servers out of the
// many formats registries use.  Fields it does not recognise are left out.
func parseWhois(reg, server, resp string) *WhoisInfo {
	info := &WhoisInfo{Domain: reg, Server: server}
	text := strings.ReplaceAll(resp, "\r", "")
	// several domains in one answer (a thin registry matching by prefix): keep the exact one
	if i := strings.Index(strings.ToLower(text), "domain name: "+reg+"\n"); i > 0 {
		text = text[i:]
		if j := strings.Index(text[10:], "Domain Name:"); j > 0 {
			text = text[:j+10]
		}
	}
	lines := strings.Split(text, "\n")
	redacted := func(v string) bool {
		l := strings.ToLower(v)
		return l == "" || strings.Contains(l, "redacted") || strings.Contains(l, "privacy") || strings.Contains(l, "data protected") || strings.Contains(l, "not disclosed") || strings.Contains(l, "withheld")
	}
	date := func(v string) string {
		v = strings.TrimSpace(v)
		if i := strings.IndexAny(v, "T "); i >= 8 && len(v) > i {
			v = v[:i]
		}
		return v
	}
	setOnce := func(dst *string, v string) {
		if *dst == "" && v != "" {
			*dst = v
		}
	}
	seenNS := map[string]bool{}
	addNS := func(v string) {
		f := strings.Fields(v)
		if len(f) == 0 {
			return
		}
		ns := strings.ToLower(strings.TrimSuffix(f[0], "."))
		if ns != "" && !seenNS[ns] && len(info.NameServers) < 6 {
			seenNS[ns] = true
			info.NameServers = append(info.NameServers, ns)
		}
	}
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		k, v, ok := strings.Cut(raw, ":")
		if !ok {
			continue
		}
		key, val := strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		next := func() string { // the value on the next non-blank line, for "Registrar:\n    Name [Tag = X]"
			for j := i + 1; j < len(lines); j++ {
				if t := strings.TrimSpace(lines[j]); t != "" {
					if p := strings.Index(t, " [Tag"); p > 0 {
						t = t[:p]
					}
					return t
				}
			}
			return ""
		}
		switch key {
		case "registrar", "registrar name", "sponsoring registrar":
			if val == "" {
				val = next()
			}
			setOnce(&info.Registrar, val)
		case "registrant organization", "registrant organisation", "registrant":
			if val == "" {
				val = next()
			}
			if !redacted(val) {
				setOnce(&info.Registrant, val)
			}
		case "creation date", "created", "created on", "registered on", "registered", "domain registration date":
			setOnce(&info.Registered, date(val))
		case "updated date", "last updated", "last modified", "changed", "last update of whois database":
			if !strings.Contains(key, "database") {
				setOnce(&info.Updated, date(val))
			}
		case "registry expiry date", "registrar registration expiration date", "expiry date", "expires", "expires on", "expiration date", "paid-till", "renewal date":
			setOnce(&info.Expires, date(val))
		case "name server", "nserver", "nameserver":
			addNS(val)
		case "name servers":
			if val != "" {
				addNS(val)
				break
			}
			for j := i + 1; j < len(lines) && strings.TrimSpace(lines[j]) != ""; j++ {
				addNS(lines[j])
			}
		}
	}
	info.Found = info.Registrar != "" || info.Registered != "" || info.Expires != "" || len(info.NameServers) > 0 || info.Registrant != ""
	if !info.Found {
		if noMatchRe.MatchString(resp) {
			info.Note = "no registration found: the name is not registered, or this suffix has no public whois"
		} else {
			info.Note = "the whois server returned nothing this page can read"
		}
	}
	return info
}

// Text is the tooltip / CLI text of an answer.
func (i *WhoisInfo) Text() string {
	var b strings.Builder
	if i.Kind == "ip" {
		b.WriteString(i.Domain)
		if !i.Found {
			b.WriteString(" — " + i.Note)
			return b.String()
		}
		add := func(k, v string) {
			if v != "" {
				b.WriteString("\n" + k + ": " + v)
			}
		}
		add("Network", i.Network)
		add("Name", i.NetName)
		add("Organization", i.Org)
		add("Country", i.Country)
		add("Origin AS", i.Origin)
		add("From", i.Server)
		return b.String()
	}
	if i.Name != "" {
		b.WriteString(i.Name)
		if i.IPv4 == "" && i.IPv6 == "" {
			b.WriteString("\nNo address found")
		}
		if i.IPv4 != "" {
			b.WriteString("\nIPv4: " + i.IPv4)
		}
		if i.IPv6 != "" {
			b.WriteString("\nIPv6: " + i.IPv6)
		}
		b.WriteString("\n\n")
	}
	if !i.Found {
		b.WriteString(i.Domain + " — " + i.Note)
		return b.String()
	}
	b.WriteString(i.Domain)
	add := func(k, v string) {
		if v != "" {
			b.WriteString("\n" + k + ": " + v)
		}
	}
	add("Registrar", i.Registrar)
	add("Registrant", i.Registrant)
	add("Registered", i.Registered)
	add("Updated", i.Updated)
	add("Expires", i.Expires)
	add("Name servers", strings.Join(i.NameServers, ", "))
	add("From", i.Server)
	return b.String()
}
