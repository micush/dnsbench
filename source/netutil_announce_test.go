package main

import "testing"

func TestAnnounceFramesAreNeutral(t *testing.T) {
	mac := vmacBytes(1, 2)
	a := buildARPProbe(mac, [4]byte{10, 77, 0, 111})
	if len(a) != 42 || string(a[6:12]) != string(mac[:]) || string(a[0:6]) != "\xff\xff\xff\xff\xff\xff" {
		t.Fatalf("ARP probe frame: % x", a)
	}
	if a[12] != 0x08 || a[13] != 0x06 || a[21] != 1 { // ARP, opcode request
		t.Fatalf("not an ARP request: % x", a)
	}
	if string(a[28:32]) != "\x00\x00\x00\x00" { // sender IP 0.0.0.0: no cache is updated
		t.Fatalf("sender IP must be 0.0.0.0: % x", a[28:32])
	}
	if string(a[38:42]) != "\x0a\x4d\x00\x6f" {
		t.Fatalf("target must be the VIP: % x", a[38:42])
	}
	vip := mustAddr("2001:db8::1").As16()
	n := buildDADNS(mac, vip)
	if string(n[6:12]) != string(mac[:]) || n[12] != 0x86 || n[13] != 0xdd {
		t.Fatalf("NS frame: % x", n[:20])
	}
	body := n[14+40:]
	var zero, dst [16]byte
	copy(dst[:], n[14+24:14+40])
	if body[0] != 135 || string(n[14+8:14+24]) != string(zero[:]) { // NS from ::
		t.Fatalf("not a DAD solicitation: % x", n[14:])
	}
	if icmp6Checksum(zero, dst, body) != 0 {
		t.Fatal("NS checksum does not verify")
	}
}
