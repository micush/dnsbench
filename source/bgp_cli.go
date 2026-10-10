package main

import (
	"fmt"
	"strconv"
	"strings"
)

// runBGP is the --bgp family:
//
//	--bgp                                   show the settings, the neighbors' BGP and BFD state, the announced addresses
//	--asn N|off [--router-id A.B.C.D|-]     set the local AS (BGP runs while one is set; off clears it)
//	--bgp-neighbor-add ADDR --remote-as N [--description T] [--password P]
//	--bgp-neighbor-del ADDR
//	--bgp-disable | --bgp-enable            stop / restart BGP on this node, keeping the settings
//	--bgp-neighbor-disable ADDR | --bgp-neighbor-enable ADDR   shut one neighbor down / bring it back
func runBGP(sock string, f *cliFlags) {
	var st BGPStatus
	decode(op(sock, "bgp.status", nil), &st)
	changed := false
	c := st.Config

	if *f.asn != "" {
		if v := strings.ToLower(*f.asn); v == "off" || v == "0" {
			c.ASN, c.RouterID = 0, "" // a router id means nothing without an AS
		} else {
			n, err := strconv.ParseUint(*f.asn, 10, 32)
			if err != nil || n == 0 {
				fatalf("--asn must be a number from 1 to 4294967295, or off")
			}
			c.ASN = uint32(n)
		}
		changed = true
	}
	if *f.routerID != "" {
		if *f.routerID != "-" && c.ASN == 0 {
			fatalf("--router-id needs a local AS number: give --asn N too (or set it first)")
		}
		if *f.routerID == "-" {
			c.RouterID = ""
		} else {
			c.RouterID = *f.routerID
		}
		changed = true
	}
	if *f.asPrepend != "" {
		switch strings.ToLower(*f.asPrepend) {
		case "on", "yes", "true":
			c.ASPrepend = true
		case "off", "no", "false":
			c.ASPrepend = false
		default:
			fatalf("--as-prepend takes on or off")
		}
		changed = true
	}
	for _, t := range []struct {
		flag, name string
		dst        *int
	}{{*f.keepalive, "keepalive", &c.Keepalive}, {*f.hold, "hold", &c.Hold}} {
		if t.flag == "" {
			continue
		}
		if t.flag == "-" {
			*t.dst = 0
		} else if v, err := strconv.Atoi(t.flag); err != nil || v < 1 {
			fatalf("--%s must be a number of seconds, or - for the default", t.name)
		} else {
			*t.dst = v
		}
		changed = true
	}
	if *f.nbrAdd != "" {
		n := BGPNeighbor{Peer: *f.nbrAdd, Description: *f.descr, Password: *f.passwd}
		as, err := strconv.ParseUint(*f.remoteAS, 10, 32)
		if err != nil || as == 0 {
			fatalf("--bgp-neighbor-add needs --remote-as N (the neighbor's AS number)")
		}
		n.RemoteAS = uint32(as)
		if *f.multihop != "" {
			mh, err := strconv.Atoi(*f.multihop)
			if err != nil || mh < 1 || mh > 255 {
				fatalf("--multihop must be a number from 2 to 255 (1 or empty: directly connected)")
			}
			n.Multihop = mh
		}
		c.Neighbors = append(c.Neighbors, n)
		changed = true
	}
	if *f.nbrDel != "" {
		kept := c.Neighbors[:0:0]
		found := false
		for _, n := range c.Neighbors {
			if n.Peer == strings.TrimSpace(*f.nbrDel) {
				found = true
				continue
			}
			kept = append(kept, n)
		}
		if !found {
			fatalf("there is no BGP neighbor %s (see --bgp)", *f.nbrDel)
		}
		c.Neighbors, changed = kept, true
	}
	if changed {
		decode(op(sock, "bgp.set", c), &st)
	}
	// switching things on and off (Operate ▸ Anycast) is separate from the settings
	switch {
	case *f.bgpDisable && *f.bgpEnable:
		fatalf("--bgp-disable and --bgp-enable cannot be used together")
	case *f.bgpDisable:
		decode(op(sock, "bgp.operate", BGPOperateArgs{Enabled: false}), &st)
	case *f.bgpEnable:
		decode(op(sock, "bgp.operate", BGPOperateArgs{Enabled: true}), &st)
	}
	if *f.nbrDisable != "" && *f.nbrEnable != "" {
		fatalf("--bgp-neighbor-disable and --bgp-neighbor-enable cannot be used together")
	}
	if *f.nbrDisable != "" {
		decode(op(sock, "bgp.operate", BGPOperateArgs{Peer: *f.nbrDisable, Enabled: false}), &st)
	}
	if *f.nbrEnable != "" {
		decode(op(sock, "bgp.operate", BGPOperateArgs{Peer: *f.nbrEnable, Enabled: true}), &st)
	}
	printBGP(st)
}

func printBGP(st BGPStatus) {
	c := st.Config
	if c.Configured() && c.Disabled {
		fmt.Printf("BGP is disabled on this node (AS %d, %d neighbor(s) kept); enable it with --bgp-enable\n", c.ASN, len(c.Neighbors))
		return
	}
	if !c.Active() {
		fmt.Println("BGP is off on this node (no local AS set).")
		if len(c.Neighbors) > 0 {
			fmt.Printf("  (%d neighbor(s) kept; turn BGP on with --asn N)\n", len(c.Neighbors))
		} else {
			fmt.Println("  Set it up with: ddgw --asn 64512 --bgp-neighbor-add 192.0.2.1 --remote-as 64500")
		}
		return
	}
	rid := orDefault(c.RouterID, "chosen by FRR")
	fmt.Printf("BGP is on: AS %d, router id %s, BFD on every neighbor\n", c.ASN, rid)
	if c.ASPrepend {
		fmt.Printf("  AS path prepend: on (the local AS %d three more times on every announcement)\n", c.ASN)
	}
	fmt.Printf("  FRR: %s", map[bool]string{true: "installed", false: "NOT installed"}[st.Installed])
	if st.Installed {
		fmt.Printf(", bgpd %s", map[bool]string{true: "answering", false: "not answering"}[st.Running])
	}
	fmt.Printf(" — %s\n", st.Applied.Detail)
	if len(st.Addresses) == 0 {
		fmt.Println("  Announces: nothing (add anycast addresses to a gateway)")
	}
	for _, a := range st.Addresses {
		if a.Up {
			fmt.Printf("  announced  %s\n", a.Addr)
		} else {
			fmt.Printf("  withdrawn  %s  (%s)\n", a.Addr, orDefault(a.Reason, "not held"))
		}
	}
	if len(c.Neighbors) == 0 {
		fmt.Println("  No neighbors yet: --bgp-neighbor-add ADDR --remote-as N")
	}
	live := map[string]BGPPeer{}
	for _, p := range st.Peers {
		live[p.Peer] = p
	}
	for _, n := range c.Neighbors {
		line := fmt.Sprintf("  neighbor %-39s AS %-10d", n.Peer, n.RemoteAS)
		if n.Multihop > 1 {
			line += fmt.Sprintf(" multihop %d", n.Multihop)
		}
		if p, ok := live[n.Peer]; ok {
			line += fmt.Sprintf(" %-12s", p.State)
			if p.State == "Established" {
				line += fmt.Sprintf(" up %s, %d prefix(es) sent", p.Uptime, p.Sent)
			}
		} else if st.Running {
			line += " (not known to FRR yet)"
		}
		if p, ok := live[n.Peer]; ok && p.BFD != "" {
			line += " bfd " + p.BFD
		}
		if n.Disabled {
			line += " disabled"
		}
		if n.Description != "" {
			line += "  " + n.Description
		}
		fmt.Println(line)
	}
	for _, note := range st.Notes {
		fmt.Println("  note:", note)
	}
}
