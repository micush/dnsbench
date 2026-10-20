package main

import (
	"net"
	"sync"
	"testing"
	"time"
)

// Replies sent through the replier come from the listening socket's own address and port, whoever sends them.
func TestReplierSendsFromTheListeningSocket(t *testing.T) {
	srv, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	rep := newUDPReplier(srv)
	defer rep.close()
	if rep.fd < 0 {
		t.Fatal("expected a duplicated descriptor on a plain UDP socket")
	}
	cli, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	const senders, each = 8, 50
	var wg sync.WaitGroup
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				rep.WriteTo([]byte("hello"), cli.LocalAddr())
			}
		}()
	}
	wg.Wait()
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	got := 0
	for got < senders*each {
		n, from, err := cli.ReadFrom(buf)
		if err != nil {
			break // a few may be dropped by a full loopback buffer; most must arrive
		}
		if string(buf[:n]) != "hello" || from.String() != srv.LocalAddr().String() {
			t.Fatalf("got %q from %v, want hello from %v", buf[:n], from, srv.LocalAddr())
		}
		got++
	}
	if got < senders*each/2 {
		t.Fatalf("only %d of %d replies arrived", got, senders*each)
	}
}

// After close, and for a socket that is not a UDPConn, sending still goes through the ordinary path without a
// crash or a write to a recycled descriptor.
func TestReplierAfterCloseUsesTheSocket(t *testing.T) {
	srv, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer srv.Close()
	cli, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer cli.Close()
	rep := newUDPReplier(srv)
	rep.close()
	rep.close() // twice is fine
	rep.WriteTo([]byte("x"), cli.LocalAddr())
	cli.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 8)
	if n, _, err := cli.ReadFrom(buf); err != nil || n != 1 {
		t.Fatalf("fallback path did not deliver: n=%d err=%v", n, err)
	}
	if r := newUDPReplier(fakePacketConn{}); r.fd != -1 {
		t.Fatal("a non-UDP connection must use the plain path")
	}
}

type fakePacketConn struct{ net.PacketConn }

func (fakePacketConn) WriteTo(b []byte, _ net.Addr) (int, error) { return len(b), nil }

// The listener is closed for good once the frontend stops: the duplicate must not keep the port bound.
func TestFrontendStopReleasesThePort(t *testing.T) {
	l, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	addr := l.LocalAddr().String()
	l.Close()
	srv, err := net.ListenPacket("udp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	rep := newUDPReplier(srv)
	srv.Close()
	rep.close()
	again, err := net.ListenPacket("udp4", addr)
	if err != nil {
		t.Fatalf("port still held after close: %v", err)
	}
	again.Close()
}

// BenchmarkReplySend compares sending replies from many goroutines on one socket: through net.UDPConn.WriteTo
// (one send at a time) and through the replier.  go test -run xxx -bench ReplySend -cpu 2,8,16
func BenchmarkReplySend(b *testing.B) {
	srv, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer srv.Close()
	sink, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer sink.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, _, err := sink.ReadFrom(buf); err != nil {
				return
			}
		}
	}()
	rep := newUDPReplier(srv)
	defer rep.close()
	msg := make([]byte, 120)
	to := sink.LocalAddr()
	b.Run("WriteTo", func(b *testing.B) {
		b.SetParallelism(16)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				srv.WriteTo(msg, to)
			}
		})
	})
	b.Run("replier", func(b *testing.B) {
		b.SetParallelism(16)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				rep.WriteTo(msg, to)
			}
		})
	})
}
