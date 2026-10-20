package main

import (
	"errors"
	"net/netip"
	"strings"
)

// whoisAnswer answers whois.get.  A domain name gets the whois data of its registered domain only (www.microsoft.com is
// asked as microsoft.com, us.pool.ntp.org as ntp.org); the hovered name itself is neither resolved nor shown.  A name with
// no whois record (an internal name, a registry that cannot be reached) gets the reason in Note.  An IP address gets the
// registry data of its block.
func whoisAnswer(name string) (*WhoisInfo, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" || len(name) > 255 {
		return nil, errors.New("no domain given")
	}
	if ip, err := netip.ParseAddr(name); err == nil {
		info, err := whois.LookupIP(ip) // an address: registry data of its block, no name to resolve
		if err != nil {
			return &WhoisInfo{Domain: ip.Unmap().String(), Kind: "ip", Note: err.Error()}, nil
		}
		return info, nil
	}
	info, err := whois.Lookup(name)
	if err != nil {
		return &WhoisInfo{Domain: name, Note: err.Error()}, nil
	}
	return info, nil
}
