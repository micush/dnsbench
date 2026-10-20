package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestHelloNotLargerThanHold(t *testing.T) {
	g := defaultGroup()
	if g.HelloMS != 150 || g.HoldMS != 750 {
		t.Fatalf("defaults %d/%d, want 150/750", g.HelloMS, g.HoldMS)
	}
	g.HelloMS, g.HoldMS = 800, 750
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	if err := dc.Validate(); err == nil || !strings.Contains(err.Error(), "hello_ms") {
		t.Fatalf("hello above hold accepted: %v", err)
	}
	dc.Groups[0].HelloMS = 750
	if err := dc.Validate(); err != nil {
		t.Fatalf("hello equal to hold refused: %v", err)
	}
}

func TestHelloGapStats(t *testing.T) {
	var p Peer
	t0 := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		p.noteHello(t0.Add(time.Duration(i) * 150 * time.Millisecond))
	}
	if math.Abs(p.HelloAvg-150) > 0.5 || math.Abs(p.helloMax()-150) > 0.5 {
		t.Fatalf("steady hellos: avg %.1f max %.1f, want 150", p.HelloAvg, p.helloMax())
	}
	p.noteHello(t0.Add(600*time.Millisecond + 400*time.Millisecond)) // a 400 ms gap: two hellos lost
	if p.helloMax() < 399 || p.HelloAvg <= 150 {
		t.Fatalf("a late hello is not seen: avg %.1f max %.1f", p.HelloAvg, p.helloMax())
	}
	// the maximum is forgotten after a minute without another late one
	now := t0.Add(time.Second)
	for i := 0; i < 500; i++ {
		now = now.Add(150 * time.Millisecond)
		p.noteHello(now)
	}
	if p.helloMax() > 151 {
		t.Fatalf("an old gap is still reported: %.1f", p.helloMax())
	}
}

func TestHelloMaxCountsSilence(t *testing.T) {
	var p Peer
	t0 := time.Unix(1000, 0)
	p.noteHello(t0)
	p.noteHello(t0.Add(150 * time.Millisecond))
	if m := p.helloMaxAt(t0.Add(200 * time.Millisecond)); m < 149 || m > 151 {
		t.Fatalf("max %.1f, want about 150", m)
	}
	if m := p.helloMaxAt(t0.Add(150*time.Millisecond + 600*time.Millisecond)); m < 599 {
		t.Fatalf("a silence of 600 ms is not in the maximum: %.1f", m)
	}
}
