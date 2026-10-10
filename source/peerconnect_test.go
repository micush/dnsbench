package main

import (
	"context"
	"net"
	"testing"
	"time"
)

// An address that accepts the connection but never speaks TLS is given up on after peerConnectTimeout, not after the whole
// call's time.
func TestDialPinnedGivesUpOnASilentAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	old := peerConnectTimeout
	peerConnectTimeout = 300 * time.Millisecond
	defer func() { peerConnectTimeout = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t0 := time.Now()
	if _, err := dialPinned(ctx, ln.Addr().String(), "00"); err == nil {
		t.Fatal("a silent address was accepted")
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("took %v to give up on a silent address", d)
	}
}
