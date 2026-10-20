package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestWeakKeys(t *testing.T) {
	for _, k := range []string{"", "dnsdir", "DNSDIR", " dgw ", "change-me", "ddgw", "secret", "short", "elevenchars"} {
		if !isWeakKey(k) {
			t.Errorf("%q should be weak", k)
		}
	}
	for _, k := range []string{"twelve-chars", "a-long-random-pass-phrase", newGroupKey()} {
		if isWeakKey(k) {
			t.Errorf("%q should be fine", k)
		}
	}
}

func TestNewGatewayGetsRandomKey(t *testing.T) {
	a, b := newGatewayGroup(), newGatewayGroup()
	if a.Key == b.Key {
		t.Fatal("two new gateways share a key")
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(a.Key) || isWeakKey(a.Key) {
		t.Fatalf("key %q is not 32 hex characters", a.Key)
	}
	// the default group of a config with no groups at all keeps the old key, so bare nodes still find each other
	if d := defaultGroup(); d.Key != defaultGroupKey || !isWeakKey(d.Key) {
		t.Fatalf("default group key %q", d.Key)
	}
}

// The Settings page marks the same keys as the daemon (the JavaScript list is a copy of this one).
func TestWebKeyListMatches(t *testing.T) {
	js, err := func() (string, error) { b, err := os.ReadFile("webui/app.js"); return string(b), err }()
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`WELL_KNOWN_KEYS = \[([^\]]*)\]`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("WELL_KNOWN_KEYS not found in app.js")
	}
	got := map[string]bool{}
	for _, q := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[1], -1) {
		got[q[1]] = true
	}
	if len(got) != len(wellKnownKeys) {
		t.Fatalf("app.js lists %d keys, gkey.go %d", len(got), len(wellKnownKeys))
	}
	for k := range wellKnownKeys {
		if !got[k] {
			t.Errorf("app.js does not list %q", k)
		}
	}
	if !strings.Contains(js, "k.length < 12") || weakKeyMin != 12 {
		t.Error("the length limit differs between app.js and gkey.go")
	}
}
