package main

import (
	"fmt"
	"os/user"
	"strings"
)

// defaultLoginGroup is the group whose members may sign in, unless LOGIN_GROUP
// says otherwise. The installer creates it and adds the installing user.
const defaultLoginGroup = "dnsbench"

// groupMember is the group check; tests replace it.
var groupMember = realGroupMember

// realGroupMember reports whether username belongs to group, counting both the
// user's primary group and supplementary groups. It goes through the system's
// name service (so LDAP or SSSD groups work) when built with cgo. Any failure,
// including the group not existing, is an error and means "no".
func realGroupMember(group, username string) (bool, error) {
	g, err := user.LookupGroup(group)
	if err != nil {
		return false, fmt.Errorf("cannot look up group %q: %w", group, err)
	}
	u, err := user.Lookup(username)
	if err != nil {
		return false, fmt.Errorf("cannot look up user %q: %w", username, err)
	}
	ids, err := u.GroupIds()
	if err != nil {
		return false, fmt.Errorf("cannot list the groups of %q: %w", username, err)
	}
	for _, id := range ids {
		if id == g.Gid {
			return true, nil
		}
	}
	return false, nil
}

// validGroupName rejects names that cannot be a real group: empty, over-long,
// or containing control characters or a colon (the /etc/group separator).
// Spaces are allowed because directory services have groups like "domain users".
func validGroupName(s string) bool {
	if s == "" || len(s) > 256 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == ':' {
			return false
		}
	}
	return true
}
