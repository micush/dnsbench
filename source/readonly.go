package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/user"
	"strings"
)

// Read-only accounts.  A member of web.read_group who is in none of the web.group groups signs in and sees
// everything the GUI shows, but cannot change anything: the one gate every request passes (authedCT) refuses
// every method but GET and HEAD, and keeps three things back that a GET would hand over whole: the
// configuration versions (the whole file, keys and passwords included) and the keys and passwords inside the
// configuration the Configure pages read.  Logging out stays possible.

const readOnlyMsg = "This account is read-only: it can look at everything but cannot change anything."

// access says whether user may sign in and whether only to look: a member of a login group has full access,
// otherwise a member of a read-only group is let in read-only.  err is the lookup error of the login groups.
func (w *WebServer) access(user string, pol *WebConfig) (ok, readOnly bool, err error) {
	ok, err = w.auth.InGroup(user, pol.Group)
	if ok {
		return true, false, nil
	}
	if pol.ReadGroup != "" {
		if ro, _ := w.auth.InGroup(user, pol.ReadGroup); ro {
			return true, true, nil
		}
		err = nil // not in either: the plain "not a member" answer, whether or not the login groups exist here
	}
	return false, false, err
}

// ro reports whether s is a read-only account.
func (w *WebServer) ro(s *session) bool {
	if s == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return s.readOnly
}

// readOnlyDenied is the rule: anything but a plain read is refused (logout excepted), and so are the pages
// that return a whole configuration file.
func readOnlyDenied(r *http.Request) bool {
	p := r.URL.Path
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return p != "/api/logout"
	}
	return p == "/api/versions/get" || p == "/api/versions/diff" || p == "/api/versions/download"
}

// secretKeys are the configuration keys whose values a read-only account is not shown.
var secretKeys = map[string]bool{"key": true, "password": true, "register_key": true, "secret": true}

// redactSecrets returns v (a configuration) as generic JSON with the values of secretKeys replaced.
func redactSecrets(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return map[string]any{}
	}
	var walk func(x any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			for k, e := range t {
				if s, ok := e.(string); ok && secretKeys[strings.ToLower(k)] && s != "" {
					t[k] = "(hidden)"
					continue
				}
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(out)
	return out
}

// ensureReadGroup makes the default read-only group (the one the installer creates) when it is in the setting and
// missing, so a daemon updated from the web page or by hand, which never ran install.sh, has it too and the Users
// page has a group to put people in.  Only the default name is ever created, and only by root; a directory group
// is never made here.
func ensureReadGroup(cfg WebConfig) {
	def := defaultWeb().ReadGroup
	if os.Geteuid() != 0 || def == "" {
		return
	}
	found := false
	for _, g := range splitGroups(cfg.ReadGroup) {
		if g == def {
			found = true
		}
	}
	if !found {
		return
	}
	if _, err := user.LookupGroup(def); err == nil {
		return
	}
	if out, err := usersRun("", "groupadd", "--system", def); err != nil {
		warnf("web: could not create the read-only group %q: %s", def, firstNonEmpty(out, err.Error()))
		return
	}
	infof("web: created the read-only group %q (add people on the Users page)", def)
}
