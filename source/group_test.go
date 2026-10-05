package main

import (
	"os/user"
	"strings"
	"testing"
)

// These run against the real system databases, using entries every Linux has:
// root (uid 0) in group root (gid 0).
func TestRealGroupMemberAgainstTheSystem(t *testing.T) {
	if _, err := user.Lookup("root"); err != nil {
		t.Skip("no root user in this environment")
	}
	if g, err := user.LookupGroup("root"); err != nil || g.Gid != "0" {
		t.Skip("no root group in this environment")
	}
	if ok, err := realGroupMember("root", "root"); !ok || err != nil {
		t.Fatalf("root is in group root (primary group): %v %v", ok, err)
	}
	if _, err := user.Lookup("nobody"); err == nil {
		if ok, err := realGroupMember("root", "nobody"); ok || err != nil {
			t.Fatalf("nobody is not in group root: %v %v", ok, err)
		}
	}
	if ok, err := realGroupMember("no-such-group-xyzzy", "root"); ok || err == nil {
		t.Fatalf("a missing group must be an error and a refusal: %v %v", ok, err)
	}
	if ok, err := realGroupMember("root", "no-such-user-xyzzy"); ok || err == nil {
		t.Fatalf("a missing user must be an error and a refusal: %v %v", ok, err)
	}
}

func TestValidGroupName(t *testing.T) {
	for _, ok := range []string{"dnsbench", "net-ops", "_x", "domain users", "CORP\\netops", strings.Repeat("a", 256)} {
		if !validGroupName(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", " a", "a ", "a:b", "a\nb", "a\x00b", "a\tb", strings.Repeat("a", 257)} {
		if validGroupName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
