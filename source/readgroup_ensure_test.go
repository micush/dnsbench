package main

import (
	"os"
	"strings"
	"testing"
)

func TestEnsureReadGroupMakesOnlyTheDefaultGroup(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root creates the group")
	}
	old := usersRun
	t.Cleanup(func() { usersRun = old })
	var calls []string
	usersRun = func(stdin, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}
	ensureReadGroup(WebConfig{ReadGroup: "dnsdir-ro"})
	if len(calls) != 1 || calls[0] != "groupadd --system dnsdir-ro" {
		t.Fatalf("default group: %v", calls)
	}
	calls = nil
	ensureReadGroup(WebConfig{ReadGroup: "EX\\viewers"}) // a directory group is never made here
	ensureReadGroup(WebConfig{ReadGroup: ""})
	if len(calls) != 0 {
		t.Fatalf("made a group it should not: %v", calls)
	}
}
