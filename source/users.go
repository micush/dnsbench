package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Users: the local accounts that may sign in to the web GUI, which are the
// members of the GUI group (web.group, "dnsdir" by default).  They are managed
// with the standard shadow-utils tools (useradd, usermod, userdel, chpasswd),
// so PAM and the operating system stay the one source of truth.  New accounts
// have no shell and no home directory: they exist only to sign in here.
//
// Accounts are per node (they are operating-system accounts); in a cluster the
// Node menu picks the node whose accounts you manage.  Passwords never appear
// in a command line: they go to chpasswd on its standard input.

// UserInfo is one account.
type UserInfo struct {
	Name     string `json:"name"`
	ReadOnly bool   `json:"read_only,omitempty"` // in the read-only group only: may look, not change
	Expires  int64  `json:"expires"`             // Unix seconds, 0 = never
	Expired  bool   `json:"expired"`             // the operating system refuses it now
}

// UsersView is what the Users page shows.
type UsersView struct {
	Group string `json:"group"`
	// ReadGroup is the local read-only group the page manages; empty when the read-only setting is empty.
	ReadGroup string     `json:"read_group"`
	Users     []UserInfo `json:"users"`
	// Others are the local accounts that could be added to the group (people, not system accounts).
	Others []string `json:"others"`
}

var (
	usersGroupFile  = "/etc/group"
	usersPasswdFile = "/etc/passwd"
	usersShadowFile = "/etc/shadow"
	nologinShells   = []string{"/usr/sbin/nologin", "/sbin/nologin", "/bin/false"}
	// usersRun runs a shadow-utils command with the given standard input
	// (replaceable in tests).
	usersRun = func(stdin string, name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return strings.TrimSpace(out.String()), err
	}
	usersNow = time.Now
)

// validUserName is deliberately stricter than useradd: lower-case letters,
// digits, "_" and "-", 1-32 characters, starting with a letter or "_".
func validUserName(u string) bool {
	if u == "" || len(u) > 32 {
		return false
	}
	for i, c := range []byte(u) {
		switch {
		case c >= 'a' && c <= 'z', c == '_':
		case (c >= '0' && c <= '9' || c == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

// validAccountName is what the group page accepts for an account that already exists, which may come from a directory
// (sssd: Active Directory, LDAP): upper-case letters, "." "@" and "\\" are common there.  It never holds what would break
// a line of /etc/group.
func validAccountName(u string) bool {
	if u == "" || len(u) > 64 {
		return false
	}
	for i, c := range []byte(u) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case (c >= '0' && c <= '9' || c == '-' || c == '.' || c == '@' || c == '\\') && i > 0:
		default:
			return false
		}
	}
	return true
}

// usersLookup says whether the name system (NSS: files, sssd, ...) knows the account (replaceable in tests).
var usersLookup = func(name string) bool {
	_, err := user.Lookup(name)
	return err == nil
}

// guiGroup is the local group the Users page manages: the first of the login groups (a comma-separated list; the
// later ones may be directory groups, which are managed in the directory) that is in /etc/group, else the first one.
func (m *Mgmt) guiGroup() string {
	list := splitGroups("dnsdir")
	if p := m.webPolicy.Load(); p != nil && p.Group != "" {
		list = splitGroups(p.Group)
	}
	if len(list) == 0 {
		return "dnsdir"
	}
	return firstLocalGroup(list)
}

// roGroup is the local group the Users page keeps the read-only accounts in: the first of web.read_group that is in
// /etc/group, else the first one; empty when the setting is empty.
func (m *Mgmt) roGroup() string {
	if p := m.webPolicy.Load(); p != nil {
		if list := splitGroups(p.ReadGroup); len(list) > 0 {
			return firstLocalGroup(list)
		}
	}
	return ""
}

func firstLocalGroup(list []string) string {
	if b, err := os.ReadFile(usersGroupFile); err == nil {
		for _, g := range list { // keep the order of the setting when several are local
			for _, l := range strings.Split(string(b), "\n") {
				if name, _, _ := strings.Cut(l, ":"); name == g {
					return g
				}
			}
		}
	}
	return list[0]
}

// groupMembers lists the members of the group: the names on its line in
// /etc/group, and accounts whose primary group it is.
func groupMembers(group string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		if n = strings.TrimSpace(n); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	gid := ""
	if b, err := os.ReadFile(usersGroupFile); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Split(l, ":")
			if len(f) >= 4 && f[0] == group {
				gid = f[2]
				for _, n := range strings.Split(f[3], ",") {
					add(n)
				}
			}
		}
	}
	if gid != "" {
		if b, err := os.ReadFile(usersPasswdFile); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if f := strings.Split(l, ":"); len(f) >= 7 && f[3] == gid {
					add(f[0])
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// userExists: the account is known to this machine, in /etc/passwd or in a directory.
func userExists(name string) bool { return localAccount(name) || usersLookup(name) }

// localAccount: the account is in /etc/passwd, so the tools of shadow-utils can manage it.
func localAccount(name string) bool {
	b, err := os.ReadFile(usersPasswdFile)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.SplitN(l, ":", 2)[0] == name {
			return true
		}
	}
	return false
}

// UsersList returns the group's accounts with their expiry dates.
func (m *Mgmt) UsersList() UsersView {
	g := m.guiGroup()
	exp := map[string]int64{}
	if b, err := os.ReadFile(usersShadowFile); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Split(l, ":"); len(f) >= 8 && f[7] != "" {
				if d, err := strconv.ParseInt(f[7], 10, 64); err == nil && d >= 0 {
					exp[f[0]] = d
				}
			}
		}
	}
	today := usersNow().Unix() / 86400
	rg := m.roGroup()
	v := UsersView{Group: g, ReadGroup: rg, Users: []UserInfo{}, Others: otherAccounts(g, rg)}
	full := map[string]bool{}
	for _, n := range groupMembers(g) {
		full[n] = true
	}
	names := groupMembers(g)
	if rg != "" {
		for _, n := range groupMembers(rg) {
			if !full[n] {
				names = append(names, n)
			}
		}
		sort.Strings(names)
	}
	for _, n := range names {
		if n == "root" {
			continue // never listed, never managed
		}
		u := UserInfo{Name: n, ReadOnly: !full[n]}
		if d, ok := exp[n]; ok {
			u.Expires = d * 86400
			u.Expired = today >= d
		}
		v.Users = append(v.Users, u)
	}
	return v
}

// otherAccounts lists the accounts on this machine that are not in the group and could be: ordinary accounts (a user
// id from 1000 up, not "nobody"), by name.  A system account can still be added by typing its name.
func otherAccounts(groups ...string) []string {
	in := map[string]bool{"root": true}
	for _, g := range groups {
		if g == "" {
			continue
		}
		for _, n := range groupMembers(g) {
			in[n] = true
		}
	}
	out := []string{}
	b, err := os.ReadFile(usersPasswdFile)
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) < 7 || in[f[0]] || !validUserName(f[0]) {
			continue
		}
		if uid, err := strconv.Atoi(f[2]); err != nil || uid < 1000 || uid >= 65534 {
			continue
		}
		out = append(out, f[0])
	}
	sort.Strings(out)
	return out
}

// listedInGroup says whether name is named on the group's line in /etc/group (as opposed to being a member only
// because the group is its primary group, which gpasswd cannot undo).
func listedInGroup(group, name string) bool {
	b, err := os.ReadFile(usersGroupFile)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Split(l, ":"); len(f) >= 4 && f[0] == group {
			for _, n := range strings.Split(f[3], ",") {
				if strings.TrimSpace(n) == name {
					return true
				}
			}
		}
	}
	return false
}

func userDate(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format("2006-01-02")
}

func checkPassword(p string, min int) error {
	switch {
	case p == "":
		return errors.New("a password is required")
	case len(p) > 256:
		return errors.New("the password is too long")
	case strings.ContainsAny(p, "\r\n"):
		return errors.New("the password may not contain a line break")
	case utf8.RuneCountInString(p) < min:
		return fmt.Errorf("the password must have at least %d characters", min)
	}
	return nil
}

// checkPassword applies the rules of the Users page, with the minimum length of the web settings.
func (m *Mgmt) checkPassword(p string) error {
	min := defaultMinPassword
	if pol := m.webPolicy.Load(); pol != nil {
		min = pol.minPassword()
	}
	return checkPassword(p, min)
}

// endUserSessions signs name out of every browser session on this node.  It is called when the account's
// password changes, when it is deleted and when it has expired: a session that was open must not outlive that.
func (m *Mgmt) endUserSessions(name string) {
	if fn := m.endSessions.Load(); fn != nil {
		(*fn)(name)
	}
}

// expiredNow: an expiry date (Unix seconds, 0 = none) that has already passed.
func expiredNow(expires int64) bool { return expires > 0 && expires <= usersNow().Unix() }

func (m *Mgmt) setUserPassword(name, password string) error {
	// chpasswd reads "name:password" lines; a line break would add a second
	// one (checked above), and the name cannot contain ":".
	if out, err := usersRun(name+":"+password+"\n", "chpasswd"); err != nil {
		return fmt.Errorf("chpasswd failed: %s", firstNonEmpty(out, err.Error()))
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// memberOf: the account is in the login group or in the read-only group (root is never one).
func (m *Mgmt) memberOf(name string) bool { return m.fullMember(name) || m.roMember(name) }

// fullMember: in the login group, so it can change things.
func (m *Mgmt) fullMember(name string) bool { return inList(groupMembers(m.guiGroup()), name) }

// roMember: in the read-only group and not in the login group.
func (m *Mgmt) roMember(name string) bool {
	rg := m.roGroup()
	return rg != "" && !m.fullMember(name) && inList(groupMembers(rg), name)
}

func inList(list []string, name string) bool {
	for _, n := range list {
		if n == name && n != "root" {
			return true
		}
	}
	return false
}

// ── cluster-wide ─────────────────────────────────────────────────────────────
//
// Every change is made here and then sent to the other members over the
// cluster channel (signed, pinned TLS) as the password hash, never the
// password.  A member applies it with the same rules as a local change.  The
// reply says on how many nodes it was applied and names any that were not
// (a node that was down misses the change; repeat it when the node is back).

// usersMsg is one change as it travels between members.
type usersMsg struct {
	Op      string `json:"op"` // apply | password | expiry | delete | grant | revoke | access
	Name    string `json:"name"`
	Hash    string `json:"hash,omitempty"`
	Expires int64  `json:"expires,omitempty"`
	By      string `json:"by,omitempty"`
	RO      bool   `json:"ro,omitempty"` // for the read-only group (apply, grant, access)
}

var cryptHashRe = regexp.MustCompile(`^\$[0-9a-z]+\$[A-Za-z0-9./$,=+-]{4,200}$`)

// shadowHash is the stored password hash of an account.
func shadowHash(name string) string {
	b, err := os.ReadFile(usersShadowFile)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Split(l, ":"); len(f) >= 2 && f[0] == name {
			return f[1]
		}
	}
	return ""
}

// localExpiry is the account's expiry in Unix seconds, 0 for none.
func localExpiry(name string) int64 {
	b, err := os.ReadFile(usersShadowFile)
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Split(l, ":"); len(f) >= 8 && f[0] == name && f[7] != "" {
			if d, err := strconv.ParseInt(f[7], 10, 64); err == nil && d > 0 {
				return d * 86400
			}
		}
	}
	return 0
}

// usersFan sends a change to the other members and returns the text to show.
func (m *Mgmt) usersFan(msg usersMsg, done string) (string, bool) {
	if m.cl == nil || !m.cl.Enabled() {
		return done, false
	}
	peers := m.cl.Snapshot().Peers
	if len(peers) == 0 {
		return done, false
	}
	type res struct {
		addr string
		err  error
	}
	ch := make(chan res, len(peers))
	for _, p := range peers {
		go func(p ClusterPeer) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			ch <- res{p.Addr, m.cl.call(ctx, p, "POST", "/cluster/users", msg, nil, 20*time.Second)}
		}(p)
	}
	var bad []string
	for range peers {
		if r := <-ch; r.err != nil {
			var pe *peerError
			if errors.As(r.err, &pe) {
				bad = append(bad, r.addr+": "+pe.Msg)
			} else {
				bad = append(bad, r.addr+": not reachable")
			}
		}
	}
	sort.Strings(bad)
	if len(bad) == 0 {
		return fmt.Sprintf("%s On all %d nodes.", done, len(peers)+1), false
	}
	return fmt.Sprintf("%s Applied here and on %d of %d other node(s); NOT applied on: %s", done, len(peers)-len(bad), len(peers), strings.Join(bad, "; ")), true
}

// usersPeer applies a change that another member sent.
func (m *Mgmt) usersPeer(msg usersMsg, by string) error {
	name := msg.Name
	okName := validUserName(name)
	if msg.Op == "grant" || msg.Op == "revoke" || msg.Op == "access" {
		okName = validAccountName(name)
	}
	if !okName || name == "root" {
		return errors.New("invalid user name")
	}
	switch msg.Op {
	case "apply", "password":
		if !cryptHashRe.MatchString(msg.Hash) {
			return errors.New("invalid password hash")
		}
		switch {
		case m.memberOf(name):
		case userExists(name):
			return fmt.Errorf("an account called %q exists here and is not in the %s group", name, m.guiGroup())
		case msg.Op == "password":
			return fmt.Errorf("%q is not a member of the %s group here", name, m.guiGroup())
		default:
			if err := m.createUser(name, msg.Expires, msg.RO); err != nil {
				return err
			}
		}
		if out, err := usersRun(name+":"+msg.Hash+"\n", "chpasswd", "-e"); err != nil {
			return fmt.Errorf("chpasswd failed: %s", firstNonEmpty(out, err.Error()))
		}
		if msg.Op == "apply" {
			if err := m.setExpiry(name, msg.Expires); err != nil {
				return err
			}
		}
		m.endUserSessions(name) // a changed password ends the sessions that were signed in with the old one
		infof("users: %s set up account %q here", by, name)
	case "grant":
		switch {
		case m.memberOf(name):
			return nil
		case userExists(name):
			if err := m.addToGroup(name, msg.RO); err != nil {
				return err
			}
		case cryptHashRe.MatchString(msg.Hash): // not here yet: made the same as on the node that added it
			if err := m.createUser(name, msg.Expires, msg.RO); err != nil {
				return err
			}
			if out, err := usersRun(name+":"+msg.Hash+"\n", "chpasswd", "-e"); err != nil {
				return fmt.Errorf("chpasswd failed: %s", firstNonEmpty(out, err.Error()))
			}
		default:
			return fmt.Errorf("there is no account %q here, and the one it was added from has no password to copy", name)
		}
		infof("users: %s added %q to the %s group here", by, name, m.guiGroup())
	case "revoke":
		if !m.memberOf(name) {
			return nil // already out
		}
		if m.fullMember(name) && m.countMembers() <= 1 {
			return errors.New("that is the last account that can sign in on this node")
		}
		if err := m.removeFromGroups(name); err != nil {
			return err
		}
		m.endUserSessions(name)
		infof("users: %s removed %q from the %s group here", by, name, m.guiGroup())
	case "access":
		if !m.memberOf(name) {
			return fmt.Errorf("%q is not a member of the %s group here", name, m.guiGroup())
		}
		if m.roMember(name) == msg.RO {
			return nil // already so
		}
		if err := m.setAccess(name, msg.RO); err != nil {
			return err
		}
		m.endUserSessions(name)
		infof("users: %s made %q %s here", by, name, accessWord(msg.RO))
	case "expiry":
		if err := m.requireMember(name); err != nil {
			return err
		}
		if err := m.setExpiry(name, msg.Expires); err != nil {
			return err
		}
		if expiredNow(msg.Expires) {
			m.endUserSessions(name)
		}
		infof("users: %s changed the expiry of %q here", by, name)
	case "delete":
		if !userExists(name) {
			return nil // already gone
		}
		if err := m.requireMember(name); err != nil {
			return err
		}
		if m.fullMember(name) && m.countMembers() <= 1 {
			return errors.New("that is the last account that can sign in on this node")
		}
		if out, err := usersRun("", "userdel", name); err != nil {
			return fmt.Errorf("userdel failed: %s", firstNonEmpty(out, err.Error()))
		}
		m.endUserSessions(name)
		infof("users: %s deleted account %q here", by, name)
	default:
		return errors.New("unknown request")
	}
	return nil
}

func (m *Mgmt) createUser(name string, expires int64, ro bool) error {
	g := m.guiGroup()
	if ro {
		if g = m.roGroup(); g == "" {
			return errors.New("there is no read-only group: set Read-only groups under Configure ▸ Web first")
		}
	}
	shell := "/bin/false"
	for _, s := range nologinShells {
		if _, err := os.Stat(s); err == nil {
			shell = s
			break
		}
	}
	args := []string{"--no-create-home", "--home-dir", "/nonexistent", "--shell", shell, "--gid", g}
	if expires > 0 {
		args = append(args, "--expiredate", userDate(expires))
	}
	args = append(args, name)
	if out, err := usersRun("", "useradd", args...); err != nil {
		return fmt.Errorf("useradd failed: %s", firstNonEmpty(out, err.Error()))
	}
	return nil
}

func (m *Mgmt) addToGroup(name string, ro bool) error {
	g := m.guiGroup()
	if ro {
		if g = m.roGroup(); g == "" {
			return errors.New("there is no read-only group: set Read-only groups under Configure ▸ Web first")
		}
	}
	if !localAccount(name) { // a directory account: usermod does not know it, gpasswd does
		if out, err := usersRun("", "gpasswd", "-a", name, g); err != nil {
			return fmt.Errorf("gpasswd failed: %s", firstNonEmpty(out, err.Error()))
		}
		return nil
	}
	if out, err := usersRun("", "usermod", "-aG", g, name); err != nil {
		return fmt.Errorf("usermod failed: %s", firstNonEmpty(out, err.Error()))
	}
	return nil
}

// removeFromGroups takes the account out of the login group and the read-only group, whichever it is listed in.
func (m *Mgmt) removeFromGroups(name string) error {
	for _, g := range []string{m.guiGroup(), m.roGroup()} {
		if g == "" || !inList(groupMembers(g), name) {
			continue
		}
		if !listedInGroup(g, name) {
			return fmt.Errorf("%q is in the %s group because it is that account's primary group; change that with usermod -g, or delete the account", name, g)
		}
		if out, err := usersRun("", "gpasswd", "-d", name, g); err != nil {
			return fmt.Errorf("gpasswd failed: %s", firstNonEmpty(out, err.Error()))
		}
	}
	return nil
}

func accessWord(ro bool) string {
	if ro {
		return "read-only"
	}
	return "full access"
}

// setAccess moves an account between the login group and the read-only group.  An account that has one of them as
// its primary group cannot be moved (gpasswd cannot take it out of that one).
func (m *Mgmt) setAccess(name string, ro bool) error {
	if err := m.removeFromGroups(name); err != nil {
		return err
	}
	if err := m.addToGroup(name, ro); err != nil {
		m.addToGroup(name, !ro) // put it back where it was
		return err
	}
	return nil
}

func (m *Mgmt) setExpiry(name string, expires int64) error {
	arg := ""
	if expires > 0 {
		arg = userDate(expires)
	}
	if out, err := usersRun("", "usermod", "--expiredate", arg, name); err != nil {
		return fmt.Errorf("usermod failed: %s", firstNonEmpty(out, err.Error()))
	}
	return nil
}

// UserAdd creates a sign-in-only account in the GUI group on every node.
// expires is Unix seconds, 0 for none.
func (m *Mgmt) UserAdd(name, password string, expires int64, readOnly bool, actor string) (string, bool, error) {
	if !validUserName(name) || name == "root" {
		return "", false, errors.New("a user name is 1-32 characters: lower-case letters, digits, _ or -, starting with a letter or _")
	}
	if err := m.checkPassword(password); err != nil {
		return "", false, err
	}
	if userExists(name) {
		return "", false, fmt.Errorf("an account called %q already exists on this machine", name)
	}
	if err := m.createUser(name, expires, readOnly); err != nil {
		return "", false, err
	}
	if err := m.setUserPassword(name, password); err != nil {
		usersRun("", "userdel", name) // do not leave an account without a password
		return "", false, err
	}
	infof("users: %s created account %q in group %q", actor, name, m.guiGroup())
	msg, partial := m.usersFan(usersMsg{Op: "apply", Name: name, Hash: shadowHash(name), Expires: expires, By: actor, RO: readOnly}, "User "+name+" added"+map[bool]string{true: " (read-only)", false: ""}[readOnly]+".")
	return msg, partial, nil
}

// UserGrant puts an account that already exists on this machine into the GUI group, so it can sign in with the
// password it already has.  On the other nodes the account is added to the group when it exists there and created
// with the same password hash when it does not.
func (m *Mgmt) UserGrant(name string, readOnly bool, actor string) (string, bool, error) {
	if !validAccountName(name) || name == "root" {
		return "", false, errors.New("invalid user name")
	}
	if !userExists(name) {
		return "", false, fmt.Errorf("there is no account called %q on this machine; use Add user to create a local one", name)
	}
	if m.memberOf(name) {
		return "", false, fmt.Errorf("%q is already in the %s group (change its access on its own page)", name, m.guiGroup())
	}
	if err := m.addToGroup(name, readOnly); err != nil {
		return "", false, err
	}
	infof("users: %s added the existing account %q to the %s group", actor, name, m.guiGroup())
	hash := shadowHash(name)
	if !cryptHashRe.MatchString(hash) {
		hash = ""
	}
	done := "User " + name + " added to the group."
	if hash == "" {
		done += " It has no password yet: set one with Password."
	}
	msg, partial := m.usersFan(usersMsg{Op: "grant", Name: name, Hash: hash, Expires: localExpiry(name), By: actor, RO: readOnly}, done)
	return msg, partial, nil
}

// UserRevoke takes an account out of the GUI group without deleting it.  The signed-in user and the last member
// cannot be taken out.
func (m *Mgmt) UserRevoke(name, actor string) (string, bool, error) {
	if err := m.requireMember(name); err != nil {
		return "", false, err
	}
	if name == actor {
		return "", false, errors.New("you cannot remove the account you are signed in as")
	}
	if m.fullMember(name) && m.countMembers() <= 1 {
		return "", false, errors.New("that is the last account that can sign in; add another one first")
	}
	if err := m.removeFromGroups(name); err != nil {
		return "", false, err
	}
	m.endUserSessions(name)
	infof("users: %s removed %q from the %s group", actor, name, m.guiGroup())
	msg, partial := m.usersFan(usersMsg{Op: "revoke", Name: name, By: actor}, "User "+name+" removed from the group; the account is kept.")
	return msg, partial, nil
}

// UserPassword sets a member's password on every node.
func (m *Mgmt) UserPassword(name, password, actor string) (string, bool, error) {
	if err := m.requireLocalMember(name); err != nil {
		return "", false, err
	}
	if err := m.checkPassword(password); err != nil {
		return "", false, err
	}
	if err := m.setUserPassword(name, password); err != nil {
		return "", false, err
	}
	m.endUserSessions(name)
	infof("users: %s changed the password of %q", actor, name)
	msg, partial := m.usersFan(usersMsg{Op: "apply", Name: name, Hash: shadowHash(name), Expires: localExpiry(name), By: actor}, "Password changed for "+name+".")
	return msg, partial, nil
}

// UserExpiry sets (or, with 0, clears) the date an account stops working, on every node.
func (m *Mgmt) UserExpiry(name string, expires int64, actor string) (string, bool, error) {
	if err := m.requireLocalMember(name); err != nil {
		return "", false, err
	}
	if err := m.setExpiry(name, expires); err != nil {
		return "", false, err
	}
	what := name + " no longer expires."
	if expires > 0 {
		what = name + " expires " + userDate(expires) + "."
	}
	if expiredNow(expires) {
		m.endUserSessions(name)
	}
	infof("users: %s set the expiry of %q to %s", actor, name, firstNonEmpty(userDate(expires), "never"))
	msg, partial := m.usersFan(usersMsg{Op: "expiry", Name: name, Expires: expires, By: actor}, what)
	return msg, partial, nil
}

// UserDelete removes an account from every node.  The signed-in user and the
// last member of the group cannot be removed (nobody could sign in afterwards).
func (m *Mgmt) UserDelete(name, actor string) (string, bool, error) {
	if validUserName(name) && name != "root" && !userExists(name) {
		// not here (perhaps a node that was down missed the delete): still tell the others
		msg, partial := m.usersFan(usersMsg{Op: "delete", Name: name, By: actor}, "There is no account "+name+" on this node.")
		return msg, partial, nil
	}
	if err := m.requireLocalMember(name); err != nil {
		return "", false, err
	}
	if name == actor {
		return "", false, errors.New("you cannot delete the account you are signed in as")
	}
	if m.fullMember(name) && m.countMembers() <= 1 {
		return "", false, errors.New("that is the last account that can sign in; add another one first")
	}
	if out, err := usersRun("", "userdel", name); err != nil {
		return "", false, fmt.Errorf("userdel failed: %s", firstNonEmpty(out, err.Error()))
	}
	m.endUserSessions(name)
	infof("users: %s deleted account %q", actor, name)
	msg, partial := m.usersFan(usersMsg{Op: "delete", Name: name, By: actor}, "User "+name+" deleted.")
	return msg, partial, nil
}

// countMembers is the number of listed accounts (root is not one).
func (m *Mgmt) countMembers() int {
	n := 0
	for _, u := range groupMembers(m.guiGroup()) {
		if u != "root" {
			n++
		}
	}
	return n
}

// requireLocalMember is requireMember for a change that only a local account can take: a password or an expiry date, or
// deleting the account.  A directory account's are kept in the directory.
func (m *Mgmt) requireLocalMember(name string) error {
	if err := m.requireMember(name); err != nil {
		return err
	}
	if !localAccount(name) {
		return fmt.Errorf("%q is a directory account (not in /etc/passwd): its password and expiry are kept in the directory; remove it from the group to stop it signing in", name)
	}
	return nil
}

func (m *Mgmt) requireMember(name string) error {
	if !validAccountName(name) {
		return errors.New("invalid user name")
	}
	if !m.memberOf(name) {
		return fmt.Errorf("%q is not a member of the %s group", name, m.guiGroup())
	}
	return nil
}

// ── joining a cluster ────────────────────────────────────────────────────────

// usersExport is every account that can sign in here, as the changes that would
// create it elsewhere (accounts with no usable password hash are left out).
func (m *Mgmt) usersExport() []usersMsg {
	out := []usersMsg{}
	names := groupMembers(m.guiGroup())
	if rg := m.roGroup(); rg != "" {
		names = append(names, groupMembers(rg)...)
	}
	done := map[string]bool{}
	for _, n := range names {
		if n == "root" || !validUserName(n) || done[n] {
			continue
		}
		done[n] = true
		if h := shadowHash(n); cryptHashRe.MatchString(h) {
			out = append(out, usersMsg{Op: "apply", Name: n, Hash: h, Expires: localExpiry(n), RO: m.roMember(n)})
		}
	}
	return out
}

// usersSeed creates the accounts a cluster already has on a node that has just
// joined.  An account that already exists here, in the group or not, is left
// exactly as it is.  It returns how many were created and how many were skipped.
func (m *Mgmt) usersSeed(list []usersMsg, from string) (added, skipped int) {
	for _, u := range list {
		if u.Op != "apply" || !validUserName(u.Name) || u.Name == "root" || userExists(u.Name) {
			skipped++
			continue
		}
		if err := m.usersPeer(u, "the cluster ("+from+") on joining"); err != nil {
			warnf("users: could not create %q from the cluster: %v", u.Name, err)
			skipped++
			continue
		}
		added++
	}
	return added, skipped
}

// usersPull fetches the accounts of a member and seeds them here.  A failure is
// only logged: the node is in the cluster either way, and re-setting a password
// on the Users page sends the account to every node.
func (m *Mgmt) usersPull(ctx context.Context, from ClusterPeer) {
	var res struct {
		Users []usersMsg `json:"users"`
	}
	if err := m.cl.call(ctx, from, "POST", "/cluster/users", usersMsg{Op: "list"}, &res, 20*time.Second); err != nil {
		warnf("users: could not get the accounts from %s: %v (the users of that cluster were not copied)", from.Addr, err)
		return
	}
	added, skipped := m.usersSeed(res.Users, from.Addr)
	infof("users: copied %d account(s) from %s on joining (%d already existed or were refused)", added, from.Addr, skipped)
}

// UserAccess moves a member between full access and read-only, on every node.  The signed-in user cannot move
// themselves, and the last full-access account cannot be made read-only.
func (m *Mgmt) UserAccess(name string, readOnly bool, actor string) (string, bool, error) {
	if err := m.requireMember(name); err != nil {
		return "", false, err
	}
	if m.roMember(name) == readOnly {
		return "", false, fmt.Errorf("%q already has %s", name, accessWord(readOnly))
	}
	if readOnly {
		if name == actor {
			return "", false, errors.New("you cannot make the account you are signed in as read-only")
		}
		if m.countMembers() <= 1 {
			return "", false, errors.New("that is the last account that can make changes; add another one first")
		}
	}
	if err := m.setAccess(name, readOnly); err != nil {
		return "", false, err
	}
	m.endUserSessions(name)
	infof("users: %s made %q %s", actor, name, accessWord(readOnly))
	msg, partial := m.usersFan(usersMsg{Op: "access", Name: name, By: actor, RO: readOnly}, name+" now has "+accessWord(readOnly)+".")
	return msg, partial, nil
}
