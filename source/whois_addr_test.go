package main

import (
	"strings"
	"testing"
)

func TestWhoisAnswerIsAboutTheDomainOnly(t *testing.T) {
	// an internal name has no whois record: the reason, and nothing about the name's addresses
	w, err := whoisAnswer("pbs1.cush.local.")
	if err != nil {
		t.Fatal(err)
	}
	if w.Found || w.Note == "" {
		t.Fatalf("%+v", w)
	}
	if txt := w.Text(); strings.Contains(txt, "IPv4") || strings.Contains(txt, "IPv6") || strings.Contains(txt, "address found") {
		t.Fatalf("a name's addresses are shown:\n%s", txt)
	}
	// the name asked about is reduced to its registered domain
	for in, want := range map[string]string{"www.microsoft.com": "microsoft.com", "us.pool.ntp.org": "ntp.org", "a.b.example.co.uk": "example.co.uk"} {
		if got, _, err := whoisName(in); err != nil || got != want {
			t.Fatalf("%s: asked as %q (%v), want %q", in, got, err, want)
		}
	}
	if _, err := whoisAnswer(" "); err == nil {
		t.Fatal("empty name accepted")
	}
}
