package main

import (
	"context"
	"net"
	"testing"
	"time"
)

// With name service down (the DNS gateway paused on every node) a peer must still be reached at the address
// its name last resolved to, and without waiting long for the lookup.
func TestPeerDialUsesLastKnownAddressWhenLookupHangs(t *testing.T) {
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
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	old := lookupPeerHost
	defer func() { lookupPeerHost = old }()
	hang := false
	lookupPeerHost = func(ctx context.Context, h string) ([]string, error) {
		if hang {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []string{"127.0.0.1"}, nil
	}
	if c, err := dialPeerTCP(context.Background(), "peer-x.test:"+port); err != nil {
		t.Fatal(err)
	} else {
		c.Close()
	}
	hang = true
	t0 := time.Now()
	c, err := dialPeerTCP(context.Background(), "peer-x.test:"+port)
	if err != nil {
		t.Fatalf("dial with the lookup hanging: %v", err)
	}
	c.Close()
	if d := time.Since(t0); d > peerLookupTimeout+time.Second {
		t.Fatalf("took %v", d)
	}
	if _, err := dialPeerTCP(context.Background(), "never-seen.test:"+port); err == nil {
		t.Fatal("a name that never resolved must fail")
	}
}
