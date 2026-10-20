package main

import (
	"strings"
	"testing"
)

const (
	roGroupFx  = "root:x:0:\ndnsdir:x:1001:alice,bob\ndnsdir-ro:x:1004:dave\n"
	roPasswdFx = "root:x:0:0::/root:/bin/bash\nalice:x:1000:1000::/home/alice:/bin/bash\nbob:x:1002:1000::/:/usr/sbin/nologin\ndave:x:1004:1000::/:/usr/sbin/nologin\ngina:x:1005:1000::/:/usr/sbin/nologin\n"
)

func roFixture(t *testing.T, group string) (*Mgmt, *[]usersCall) {
	m, calls := usersFixture(t, group, roPasswdFx, "")
	m.webPolicy.Store(&WebConfig{Group: "dnsdir", ReadGroup: "dnsdir-ro"})
	return m, calls
}

func callLines(calls []usersCall) string {
	var b []string
	for _, c := range calls {
		b = append(b, c.name+" "+strings.Join(c.args, " "))
	}
	return strings.Join(b, "\n")
}

func TestUsersListShowsReadOnlyAccounts(t *testing.T) {
	m, _ := roFixture(t, roGroupFx)
	v := m.UsersList()
	if v.ReadGroup != "dnsdir-ro" || len(v.Users) != 3 {
		t.Fatalf("view: %+v", v)
	}
	for _, u := range v.Users {
		if u.ReadOnly != (u.Name == "dave") {
			t.Errorf("%s read-only = %v", u.Name, u.ReadOnly)
		}
	}
	for _, o := range v.Others {
		if o == "dave" || o == "alice" {
			t.Errorf("%s is in a group already and must not be offered: %v", o, v.Others)
		}
	}
	m.webPolicy.Store(&WebConfig{Group: "dnsdir"})
	if v := m.UsersList(); v.ReadGroup != "" || len(v.Users) != 2 {
		t.Errorf("with no read-only setting: %+v", v)
	}
}

func TestUserAddAndGrantReadOnly(t *testing.T) {
	m, calls := roFixture(t, roGroupFx)
	if _, _, err := m.UserAdd("erin", "long-enough-1", 0, true, "alice"); err != nil {
		t.Fatal(err)
	}
	if got := callLines(*calls); !strings.Contains(got, "--gid dnsdir-ro") {
		t.Errorf("a read-only user must get the read-only group:\n%s", got)
	}
	*calls = nil
	if _, _, err := m.UserGrant("gina", true, "alice"); err != nil {
		t.Fatal(err)
	}
	if got := callLines(*calls); !strings.Contains(got, "usermod -aG dnsdir-ro gina") {
		t.Errorf("grant read-only:\n%s", got)
	}
	m.webPolicy.Store(&WebConfig{Group: "dnsdir"})
	if _, _, err := m.UserAdd("frank", "long-enough-1", 0, true, "alice"); err == nil || !strings.Contains(err.Error(), "no read-only group") {
		t.Errorf("read-only with no read-only group set: %v", err)
	}
}

func TestUserAccessMovesBetweenGroups(t *testing.T) {
	m, calls := roFixture(t, roGroupFx)
	if _, _, err := m.UserAccess("dave", false, "alice"); err != nil {
		t.Fatal(err)
	}
	got := callLines(*calls)
	if !strings.Contains(got, "gpasswd -d dave dnsdir-ro") || !strings.Contains(got, "usermod -aG dnsdir dave") {
		t.Errorf("promote:\n%s", got)
	}
	*calls = nil
	if _, _, err := m.UserAccess("bob", true, "alice"); err != nil {
		t.Fatal(err)
	}
	got = callLines(*calls)
	if !strings.Contains(got, "gpasswd -d bob dnsdir") || !strings.Contains(got, "usermod -aG dnsdir-ro bob") {
		t.Errorf("demote:\n%s", got)
	}
	if _, _, err := m.UserAccess("bob", true, "bob"); err == nil {
		t.Error("nobody can make their own account read-only")
	}
	if _, _, err := m.UserAccess("dave", true, "alice"); err == nil {
		t.Error("an account that already has that access is refused")
	}
}

func TestLastFullAccessAccountIsKept(t *testing.T) {
	m, _ := roFixture(t, "root:x:0:\ndnsdir:x:1001:alice\ndnsdir-ro:x:1004:dave\n")
	if _, _, err := m.UserAccess("alice", true, "dave"); err == nil || !strings.Contains(err.Error(), "last") {
		t.Errorf("demoting the last full account: %v", err)
	}
	if _, _, err := m.UserDelete("alice", "dave"); err == nil {
		t.Error("deleting the last full account must be refused")
	}
	// a read-only account is never the last one that can sign in to make changes
	if _, _, err := m.UserRevoke("dave", "alice"); err != nil {
		t.Errorf("removing a read-only account: %v", err)
	}
}
