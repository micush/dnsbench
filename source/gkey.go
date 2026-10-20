package main

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// The gateway key signs every packet of the election protocol (wire.go).  Anyone who knows it can take the controller
// role of a gateway, so a key that is public knowledge protects nothing.  A new gateway therefore starts with a random
// key, and one that still has a well-known key (the old default, the example file's) is named in the log, by the
// command line and in Settings, but keeps working: changing the key restarts the gateway on every node.

// weakKeyMin is the shortest key that is not called weak.
const weakKeyMin = 12

var wellKnownKeys = map[string]bool{
	"": true, "dnsdir": true, "dgw": true, "ddgw": true, "change-me": true, "changeme": true, "secret": true,
	"password": true, "key": true, "test": true, "default": true,
}

// isWeakKey reports whether key is empty, well known or too short to resist guessing.
func isWeakKey(key string) bool {
	return len(key) < weakKeyMin || wellKnownKeys[strings.ToLower(strings.TrimSpace(key))]
}

// newGroupKey is a fresh random key: 128 bits as 32 hex characters.
func newGroupKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("no random numbers: " + err.Error()) // the system cannot give them; a guessable key must not be made instead
	}
	return hex.EncodeToString(b[:])
}
