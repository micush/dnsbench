package main

import (
	"net/netip"
	"regexp"
	"strings"
)

// Whois of an IP address: IANA names the regional registry (ARIN, RIPE, APNIC, LACNIC, AFRINIC), which is asked
// about the address; one "ReferralServer" hop is followed (ARIN hands transferred blocks to another registry).
// Private, loopback, link-local and other non-public addresses are not asked of anyone.

// publicIP reports whether an address can be in a registry, and if not why.
func publicIP(a netip.Addr) (bool, string) {
	a = a.Unmap()
	switch {
	case a.IsLoopback():
		return false, "a loopback address: no whois record"
	case a.IsLinkLocalUnicast():
		return false, "a link-local address: no whois record"
	case a.IsPrivate():
		return false, "a private address: no whois record"
	case a.IsMulticast(), a.IsUnspecified(), a.IsInterfaceLocalMulticast():
		return false, "not a unicast address: no whois record"
	}
	for _, p := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "198.18.0.0/15", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(p).Contains(a) {
			return false, "a reserved address: no whois record"
		}
	}
	return true, ""
}

var referralRe = regexp.MustCompile(`(?mi)^\s*(?:referralserver|refer):\s*(?:r?whois://)?([A-Za-z0-9.\-]+)`)

// LookupIP returns the registry data of an address, from the cache when it is fresh.
func (w *whoisClient) LookupIP(a netip.Addr) (*WhoisInfo, error) {
	a = a.Unmap()
	ip := a.String()
	if ok, why := publicIP(a); !ok {
		return &WhoisInfo{Domain: ip, Kind: "ip", Note: why}, nil
	}
	return w.cached("ip:"+ip, func() (*WhoisInfo, error) {
		resp, err := whoisRaw(whoisIANA, ip)
		if err != nil {
			return nil, err
		}
		m := referRe.FindStringSubmatch(resp)
		if m == nil {
			return &WhoisInfo{Domain: ip, Kind: "ip", Server: whoisIANA, Note: "no regional registry is named for this address"}, nil
		}
		server := strings.ToLower(m[1])
		ask := func(srv string) (string, error) {
			q := ip
			if srv == "whois.arin.net" {
				q = "n + " + ip
			}
			return whoisRaw(srv, q)
		}
		out, err := ask(server)
		if err != nil {
			return nil, err
		}
		if r := referralRe.FindStringSubmatch(out); r != nil && strings.ToLower(r[1]) != server && !strings.EqualFold(r[1], whoisIANA) {
			if o2, err := ask(strings.ToLower(r[1])); err == nil && strings.TrimSpace(o2) != "" {
				server, out = strings.ToLower(r[1]), o2
			}
		}
		return parseIPWhois(ip, server, out), nil
	})
}

// parseIPWhois picks the block, its name, the organisation, the country and the origin AS out of a registry's answer
// (ARIN's NetRange / NetName / OrgName, the RIPE-style inetnum / netname / descr, LACNIC's owner).
func parseIPWhois(ip, server, resp string) *WhoisInfo {
	info := &WhoisInfo{Domain: ip, Kind: "ip", Server: server}
	setOnce := func(dst *string, v string) {
		v = strings.TrimSpace(v)
		if *dst == "" && v != "" {
			*dst = v
		}
	}
	// A registry may answer with several objects (a covering block, then the specific one): the last network block
	// is the most specific, so take the fields of the object that holds the last "network" line.
	text := strings.ReplaceAll(resp, "\r", "")
	var objs [][]string
	var cur []string
	for _, ln := range strings.Split(text, "\n") {
		if strings.TrimSpace(ln) == "" {
			if len(cur) > 0 {
				objs = append(objs, cur)
				cur = nil
			}
			continue
		}
		if strings.HasPrefix(ln, "%") || strings.HasPrefix(ln, "#") {
			continue
		}
		cur = append(cur, ln)
	}
	if len(cur) > 0 {
		objs = append(objs, cur)
	}
	isNet := func(k string) bool {
		return k == "netrange" || k == "cidr" || k == "inetnum" || k == "inet6num"
	}
	for _, o := range objs {
		has := false
		for _, ln := range o {
			if k, _, ok := strings.Cut(ln, ":"); ok && isNet(strings.ToLower(strings.TrimSpace(k))) {
				has = true
			}
		}
		if !has {
			continue
		}
		var n IPNet
		for _, ln := range o {
			k, v, ok := strings.Cut(ln, ":")
			if !ok {
				continue
			}
			key, val := strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
			switch key {
			case "cidr":
				n.cidr = val
			case "netrange", "inetnum", "inet6num":
				setOnce(&n.rng, val)
			case "netname":
				setOnce(&n.name, val)
			case "orgname", "org-name", "owner":
				setOnce(&n.org, val)
			case "descr":
				setOnce(&n.descr, val)
			case "country":
				setOnce(&n.country, val)
			case "originas", "origin":
				setOnce(&n.origin, val)
			}
		}
		info.Network, info.NetName, info.Country, info.Origin = n.network(), n.name, n.country, strings.TrimPrefix(strings.ToUpper(n.origin), "AS")
		if info.Origin != "" {
			info.Origin = "AS" + info.Origin
		}
		info.Org = n.org
		if info.Org == "" {
			info.Org = n.descr
		}
	}
	// ARIN keeps the organisation and country in a separate object: fill what is still empty from anywhere in the answer
	for _, o := range objs {
		for _, ln := range o {
			k, v, ok := strings.Cut(ln, ":")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "orgname", "org-name", "owner":
				setOnce(&info.Org, v)
			case "country":
				setOnce(&info.Country, v)
			}
		}
	}
	info.Found = info.Network != "" || info.NetName != "" || info.Org != ""
	if !info.Found {
		info.Note = "the registry returned nothing this page can read"
		if noMatchRe.MatchString(resp) {
			info.Note = "no registration found for this address"
		}
	}
	return info
}

// IPNet is one network object of a registry answer.
type IPNet struct{ cidr, rng, name, org, descr, country, origin string }

func (n IPNet) network() string {
	if n.cidr != "" {
		return n.cidr
	}
	return n.rng
}
