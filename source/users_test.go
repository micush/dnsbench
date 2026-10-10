package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type usersCall struct {
	stdin, name string
	args        []string
}

func usersFixture(t *testing.T, group, passwd, shadow string) (*Mgmt, *[]usersCall) {
	t.Helper()
	d := t.TempDir()
	w := func(n, s string) string {
		p := filepath.Join(d, n)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldG, oldP, oldS, oldR, oldN, oldSh := usersGroupFile, usersPasswdFile, usersShadowFile, usersRun, usersNow, nologinShells
	t.Cleanup(func() {
		usersGroupFile, usersPasswdFile, usersShadowFile, usersRun, usersNow, nologinShells = oldG, oldP, oldS, oldR, oldN, oldSh
	})
	usersGroupFile, usersPasswdFile, usersShadowFile = w("group", group), w("passwd", passwd), w("shadow", shadow)
	nologinShells = []string{"/definitely/missing", "/bin/false"}
	usersNow = func() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) }
	var calls []usersCall
	usersRun = func(stdin, name string, args ...string) (string, error) {
		calls = append(calls, usersCall{stdin, name, args})
		return "", nil
	}
	m := &Mgmt{}
	m.webPolicy.Store(&WebConfig{Group: "ddgw"})
	return m, &calls
}

const (
	fxGroup  = "root:x:0:\nddgw:x:1001:alice,bob\n"
	fxPasswd = "root:x:0:0::/root:/bin/bash\nalice:x:1000:1000::/home/alice:/bin/bash\nbob:x:1002:1000::/:/usr/sbin/nologin\ncarol:x:1003:1001::/:/usr/sbin/nologin\n"
)

func TestValidUserName(t *testing.T) {
	for _, n := range []string{"alice", "_svc", "a-b_c9", strings.Repeat("a", 32)} {
		if !validUserName(n) {
			t.Errorf("%q should be valid", n)
		}
	}
	for _, n := range []string{"", "Alice", "-a", "9a", "a:b", "a b", "a;b", "a/b", "a\nb", "root:x", strings.Repeat("a", 33), "é"} {
		if validUserName(n) {
			t.Errorf("%q should be refused", n)
		}
	}
}

func TestUsersList(t *testing.T) {
	// bob expires on day 20000 (2024-10-04), long past; alice never; carol has the group as primary group.
	m, _ := usersFixture(t, fxGroup, fxPasswd, "alice:x:1::::::\nbob:x:1:::::20000:\ncarol:x:1:::::21000\n")
	v := m.UsersList()
	if v.Group != "ddgw" || len(v.Users) != 3 {
		t.Fatalf("view: %+v", v)
	}
	by := map[string]UserInfo{}
	for _, u := range v.Users {
		by[u.Name] = u
	}
	if by["alice"].Expires != 0 || by["alice"].Expired {
		t.Errorf("alice: %+v", by["alice"])
	}
	if !by["bob"].Expired || by["bob"].Expires != 20000*86400 {
		t.Errorf("bob: %+v", by["bob"])
	}
	if by["carol"].Expired || by["carol"].Expires != 21000*86400 { // 2027-05-... is in the future
		t.Errorf("carol: %+v", by["carol"])
	}
	if _, ok := by["root"]; ok {
		t.Error("root is not a member of the group and must not be listed")
	}
}

func TestUserAdd(t *testing.T) {
	m, calls := usersFixture(t, fxGroup, fxPasswd, "")
	exp := time.Date(2027, 1, 2, 12, 0, 0, 0, time.UTC).Unix()
	if _, _, err := m.UserAdd("dave", "s3cret pass", exp, "alice"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[0].name != "useradd" || (*calls)[1].name != "chpasswd" {
		t.Fatalf("calls: %+v", *calls)
	}
	got := strings.Join((*calls)[0].args, " ")
	if got != "--no-create-home --home-dir /nonexistent --shell /bin/false --gid ddgw --expiredate 2027-01-02 dave" {
		t.Errorf("useradd args: %s", got)
	}
	if (*calls)[1].stdin != "dave:s3cret pass\n" {
		t.Errorf("chpasswd stdin: %q", (*calls)[1].stdin)
	}
	for _, c := range *calls {
		if strings.Contains(strings.Join(c.args, " "), "s3cret") {
			t.Error("the password appeared in a command line")
		}
	}
}

func TestUserAddRefusals(t *testing.T) {
	m, calls := usersFixture(t, fxGroup, fxPasswd, "")
	for _, c := range []struct{ name, pw string }{
		{"Dave", "x"}, {"-d", "x"}, {"a:b", "x"}, {"dave", ""}, {"dave", "a\nb"}, {"dave", "a\rb"},
		{"root", "x"},  // exists
		{"alice", "x"}, // exists: never re-password an existing account
	} {
		if _, _, err := m.UserAdd(c.name, c.pw, 0, "alice"); err == nil {
			t.Errorf("%q / %q should be refused", c.name, c.pw)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("nothing may run for a refused request: %+v", *calls)
	}
}

func TestUserAddRollsBackWithoutPassword(t *testing.T) {
	m, calls := usersFixture(t, fxGroup, fxPasswd, "")
	usersRun = func(stdin, name string, args ...string) (string, error) {
		*calls = append(*calls, usersCall{stdin, name, args})
		if name == "chpasswd" {
			return "bad password", errors.New("exit 1")
		}
		return "", nil
	}
	if _, _, err := m.UserAdd("dave", "long-enough-1", 0, "alice"); err == nil || !strings.Contains(err.Error(), "bad password") {
		t.Fatalf("err = %v", err)
	}
	last := (*calls)[len(*calls)-1]
	if last.name != "userdel" || last.args[0] != "dave" {
		t.Errorf("the half-made account was not removed: %+v", *calls)
	}
}

func TestUserPasswordExpiryDeleteOnlyForMembers(t *testing.T) {
	m, calls := usersFixture(t, fxGroup, fxPasswd, "")
	for _, who := range []string{"root", "nobody", "../x", ""} {
		if _, _, err := m.UserPassword(who, "x", "alice"); err == nil {
			t.Errorf("password of %q must be refused", who)
		}
		if _, _, err := m.UserExpiry(who, 0, "alice"); err == nil {
			t.Errorf("expiry of %q must be refused", who)
		}
		if _, _, err := m.UserDelete(who, "alice"); err == nil && who != "nobody" {
			t.Errorf("delete of %q must be refused", who) // "nobody" does not exist here: nothing to delete, nothing runs
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("nothing may run: %+v", *calls)
	}
	if _, _, err := m.UserPassword("bob", "newpw-12345", "alice"); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].name != "chpasswd" || (*calls)[0].stdin != "bob:newpw-12345\n" {
		t.Errorf("%+v", (*calls)[0])
	}
}

func TestUserExpiryArgs(t *testing.T) {
	m, calls := usersFixture(t, fxGroup, fxPasswd, "")
	if _, _, err := m.UserExpiry("bob", time.Date(2027, 3, 4, 0, 0, 0, 0, time.UTC).Unix(), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.UserExpiry("bob", 0, "alice"); err != nil {
		t.Fatal(err)
	}
	if strings.Join((*calls)[0].args, " ") != "--expiredate 2027-03-04 bob" || strings.Join((*calls)[1].args, "|") != "--expiredate||bob" {
		t.Errorf("%+v", *calls)
	}
}

func TestUserDeleteGuards(t *testing.T) {
	m, calls := usersFixture(t, fxGroup, fxPasswd, "")
	if _, _, err := m.UserDelete("alice", "alice"); err == nil {
		t.Error("deleting yourself must be refused")
	}
	if _, _, err := m.UserDelete("bob", "alice"); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].name != "userdel" || strings.Join((*calls)[0].args, " ") != "bob" {
		t.Errorf("%+v", *calls)
	}
	// Only one member left in a group that has just alice.
	m2, calls2 := usersFixture(t, "ddgw:x:1001:alice\n", "alice:x:1000:1000::/:/bin/bash\n", "")
	if _, _, err := m2.UserDelete("alice", "cli:root"); err == nil {
		t.Error("the last account must not be deletable")
	}
	if len(*calls2) != 0 {
		t.Errorf("%+v", *calls2)
	}
}

func TestRootIsNeverListedOrManaged(t *testing.T) {
	m, calls := usersFixture(t, "ddgw:x:1001:root,alice\n", "alice:x:1000:1000::/:/bin/bash\n", "")
	for _, u := range m.UsersList().Users {
		if u.Name == "root" {
			t.Fatal("root must never be listed")
		}
	}
	if _, _, err := m.UserPassword("root", "x", "alice"); err == nil {
		t.Error("root's password must not be changeable here")
	}
	if _, _, err := m.UserDelete("root", "alice"); err == nil {
		t.Error("root must not be deletable here")
	}
	if _, _, err := m.UserExpiry("root", 0, "alice"); err == nil {
		t.Error("root's expiry must not be changeable here")
	}
	// root does not count as "another account that can sign in".
	if _, _, err := m.UserDelete("alice", "cli:root"); err == nil {
		t.Error("alice is the last listed account and must not be deletable")
	}
	if _, _, err := m.UserDelete("root", "cli:root"); err == nil {
		t.Error("root must not be deletable")
	}
	if len(*calls) != 0 {
		t.Errorf("%+v", *calls)
	}
	if err := m.usersPeer(usersMsg{Op: "delete", Name: "root"}, "x"); err == nil {
		t.Error("a peer must not be able to touch root")
	}
}

const testHash = "$6$salt1234$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRS"

func TestUsersFromAnotherNode(t *testing.T) {
	m, calls := usersFixture(t, fxGroup, fxPasswd, "")
	exp := time.Date(2027, 1, 2, 12, 0, 0, 0, time.UTC).Unix()
	// A new account is created, given the hash (on stdin, with -e) and the expiry.
	if err := m.usersPeer(usersMsg{Op: "apply", Name: "dave", Hash: testHash, Expires: exp}, "alice via n1"); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range *calls {
		names = append(names, c.name+" "+strings.Join(c.args, " "))
		if strings.Contains(strings.Join(c.args, " "), "abcdefghij") {
			t.Error("the hash appeared in a command line")
		}
	}
	if len(*calls) != 3 || !strings.HasPrefix(names[0], "useradd ") || names[1] != "chpasswd -e" || names[2] != "usermod --expiredate 2027-01-02 dave" {
		t.Fatalf("calls: %q", names)
	}
	if (*calls)[1].stdin != "dave:"+testHash+"\n" {
		t.Errorf("chpasswd stdin: %q", (*calls)[1].stdin)
	}
	// An existing member just gets the new hash.
	*calls = nil
	if err := m.usersPeer(usersMsg{Op: "password", Name: "bob", Hash: testHash}, "x"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].name != "chpasswd" {
		t.Errorf("%+v", *calls)
	}
}

func TestUsersFromAnotherNodeRefusals(t *testing.T) {
	m, calls := usersFixture(t, fxGroup, fxPasswd, "")
	bad := []usersMsg{
		{Op: "apply", Name: "dave", Hash: "plaintext"},           // not a hash
		{Op: "apply", Name: "dave", Hash: "!"},                   // a locked entry is not a password
		{Op: "apply", Name: "dave", Hash: testHash + "\nroot:x"}, // line break
		{Op: "apply", Name: "dave", Hash: "$6$a:b$cccccccccc"},   // colon
		{Op: "apply", Name: "Dave", Hash: testHash},              // bad name
		{Op: "apply", Name: "../x", Hash: testHash},              // bad name
		{Op: "apply", Name: "carol2", Hash: ""},                  // no hash
		{Op: "password", Name: "nobody", Hash: testHash},         // not a member here
		{Op: "expiry", Name: "nobody"},                           // not a member here
		{Op: "bogus", Name: "bob"},
	}
	for _, b := range bad {
		if err := m.usersPeer(b, "x"); err == nil {
			t.Errorf("%+v should be refused", b)
		}
	}
	// An account that exists here but is not in the group is never taken over.
	if err := m.usersPeer(usersMsg{Op: "apply", Name: "bob2", Hash: testHash}, "x"); err != nil {
		t.Fatal(err) // a name that does not exist is created
	}
	*calls = nil
	if err := m.usersPeer(usersMsg{Op: "apply", Name: "root", Hash: testHash}, "x"); err == nil {
		t.Error("root must be refused")
	}
	for _, c := range *calls {
		if c.name == "chpasswd" {
			t.Errorf("a refused request ran %+v", c)
		}
	}
}

func TestPeerDelete(t *testing.T) {
	m, calls := usersFixture(t, fxGroup, fxPasswd, "")
	if err := m.usersPeer(usersMsg{Op: "delete", Name: "ghost"}, "x"); err != nil || len(*calls) != 0 {
		t.Errorf("deleting an account that is already gone must succeed quietly: %v %+v", err, *calls)
	}
	if err := m.usersPeer(usersMsg{Op: "delete", Name: "carol"}, "x"); err != nil || len(*calls) != 1 || (*calls)[0].name != "userdel" {
		t.Errorf("%v %+v", err, *calls)
	}
}

func TestPeerNeverTakesOverAnExistingAccount(t *testing.T) {
	m, calls := usersFixture(t, fxGroup, fxPasswd+"svc:x:999:999::/:/bin/bash\n", "")
	if err := m.usersPeer(usersMsg{Op: "apply", Name: "svc", Hash: testHash}, "x"); err == nil {
		t.Fatal("an existing account outside the group must not be taken over")
	}
	if len(*calls) != 0 {
		t.Errorf("nothing may run: %+v", *calls)
	}
}

func TestUsersSeedOnJoin(t *testing.T) {
	// this node already has alice (member), carol (not a member) and svc
	m, calls := usersFixture(t, fxGroup, fxPasswd+"svc:x:999:999::/:/bin/bash\n", "")
	exp := time.Date(2027, 1, 2, 12, 0, 0, 0, time.UTC).Unix()
	list := []usersMsg{
		{Op: "apply", Name: "alice", Hash: testHash},
		{Op: "apply", Name: "carol", Hash: testHash},
		{Op: "apply", Name: "svc", Hash: testHash},
		{Op: "apply", Name: "root", Hash: testHash},
		{Op: "apply", Name: "bad name", Hash: testHash},
		{Op: "delete", Name: "zed"},
		{Op: "apply", Name: "dave", Hash: testHash, Expires: exp},
	}
	added, skipped := m.usersSeed(list, "n1")
	if added != 1 || skipped != 6 {
		t.Fatalf("added %d skipped %d", added, skipped)
	}
	var names []string
	for _, c := range *calls {
		names = append(names, c.name+" "+strings.Join(c.args, " "))
	}
	if len(names) != 3 || !strings.HasPrefix(names[0], "useradd ") || !strings.Contains(names[0], "dave") || names[1] != "chpasswd -e" {
		t.Fatalf("only dave may be created: %q", names)
	}
}

func TestUsersExport(t *testing.T) {
	shadow := "alice:" + testHash + ":19000:0:99999:7:::\nbob:!:19000::::::\n"
	m, _ := usersFixture(t, fxGroup, fxPasswd, shadow)
	got := m.usersExport()
	if len(got) != 1 || got[0].Name != "alice" || got[0].Hash != testHash || got[0].Op != "apply" {
		t.Fatalf("%+v", got)
	}
}

const fxPasswd2 = fxPasswd + "dave:x:1004:1004::/home/dave:/bin/bash\nsvc:x:998:998::/:/usr/sbin/nologin\nnobody:x:65534:65534::/:/usr/sbin/nologin\n"

func TestUsersListSuggestsOrdinaryAccountsNotInTheGroup(t *testing.T) {
	m, _ := usersFixture(t, fxGroup, fxPasswd2, "")
	got := m.UsersList().Others
	if len(got) != 1 || got[0] != "dave" {
		t.Fatalf("others = %v, want [dave] (not members, not system accounts, not nobody, not root)", got)
	}
}

func TestUserGrantAddsAnExistingAccountToTheGroup(t *testing.T) {
	m, calls := usersFixture(t, fxGroup, fxPasswd2, "dave:$6$salt$abcdefghijklmnop:1::::::\n")
	if _, _, err := m.UserGrant("dave", "alice"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].name != "usermod" || strings.Join((*calls)[0].args, " ") != "-aG ddgw dave" {
		t.Fatalf("calls: %+v", *calls)
	}
	for _, c := range []struct{ name, why string }{{"nobody-here", "an account that does not exist"}, {"alice", "an account already in the group"}, {"root", "root"}, {"Bad Name", "an invalid name"}} {
		if _, _, err := m.UserGrant(c.name, "alice"); err == nil {
			t.Errorf("%s must be refused", c.why)
		}
	}
	if len(*calls) != 1 {
		t.Errorf("a refused grant ran a command: %+v", *calls)
	}
}

func TestUserRevokeKeepsTheAccount(t *testing.T) {
	m, calls := usersFixture(t, fxGroup, fxPasswd2, "")
	if _, _, err := m.UserRevoke("bob", "alice"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].name != "gpasswd" || strings.Join((*calls)[0].args, " ") != "-d bob ddgw" {
		t.Fatalf("calls: %+v", *calls)
	}
	if _, _, err := m.UserRevoke("alice", "alice"); err == nil {
		t.Error("removing yourself must be refused")
	}
	if _, _, err := m.UserRevoke("carol", "alice"); err == nil || !strings.Contains(err.Error(), "primary group") {
		t.Errorf("a member by primary group must be refused with the reason: %v", err)
	}
	if _, _, err := m.UserRevoke("dave", "alice"); err == nil {
		t.Error("a non-member must be refused")
	}
	m2, calls2 := usersFixture(t, "ddgw:x:1001:alice\n", "alice:x:1000:1000::/:/bin/bash\n", "")
	if _, _, err := m2.UserRevoke("alice", "cli:root"); err == nil || len(*calls2) != 0 {
		t.Errorf("the last account must stay: %v %+v", err, *calls2)
	}
}

func TestPeerGrantAndRevoke(t *testing.T) {
	m, calls := usersFixture(t, fxGroup, fxPasswd2, "")
	if err := m.usersPeer(usersMsg{Op: "grant", Name: "dave"}, "x"); err != nil || (*calls)[0].name != "usermod" {
		t.Fatalf("existing account: %v %+v", err, *calls)
	}
	*calls = nil
	hash := "$6$salt$abcdefghijklmnop"
	if err := m.usersPeer(usersMsg{Op: "grant", Name: "erin", Hash: hash}, "x"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) < 2 || (*calls)[0].name != "useradd" || (*calls)[1].name != "chpasswd" || !strings.Contains((*calls)[1].stdin, hash) {
		t.Fatalf("a missing account must be created with the hash: %+v", *calls)
	}
	if err := m.usersPeer(usersMsg{Op: "grant", Name: "frank"}, "x"); err == nil {
		t.Error("a missing account without a hash cannot be made")
	}
	*calls = nil
	if err := m.usersPeer(usersMsg{Op: "revoke", Name: "bob"}, "x"); err != nil || (*calls)[0].name != "gpasswd" {
		t.Fatalf("revoke: %v %+v", err, *calls)
	}
	*calls = nil
	if err := m.usersPeer(usersMsg{Op: "revoke", Name: "dave"}, "x"); err != nil || len(*calls) != 0 {
		t.Fatalf("revoking a non-member is a no-op: %v %+v", err, *calls)
	}
}
