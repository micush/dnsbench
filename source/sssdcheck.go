package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// sssd on an Active Directory domain (realm join, Samba AD) enforces the domain's group policy at the account step of
// PAM, and refuses every PAM service it has no logon right for.  sshd and login are known; the GUI's own service is
// not, so a user who can ssh in gets "pam: Permission denied" at the GUI after the password was accepted.  The fix is a
// line in the domain's section of sssd.conf mapping the service; sssdGPOProblem finds the case so the log can say so.

var (
	sssdConfFile = "/etc/sssd/sssd.conf"
	sssdConfDir  = "/etc/sssd/conf.d"
)

// sssdSections reads sssd.conf and its conf.d snippets into section → option → value (later files override).
func sssdSections() map[string]map[string]string {
	files := []string{sssdConfFile}
	if ents, err := filepath.Glob(filepath.Join(sssdConfDir, "*.conf")); err == nil {
		sort.Strings(ents)
		files = append(files, ents...)
	}
	out := map[string]map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		sec := ""
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || line[0] == '#' || line[0] == ';' {
				continue
			}
			if line[0] == '[' && strings.HasSuffix(line, "]") {
				sec = line[1 : len(line)-1]
				if out[sec] == nil {
					out[sec] = map[string]string{}
				}
				continue
			}
			if k, v, ok := strings.Cut(line, "="); ok && sec != "" {
				out[sec][strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

// sssdGPOProblem returns advice when an AD domain of sssd would refuse the PAM service by group policy, "" otherwise
// (no sssd, no AD domain, policy not enforced, or the service is mapped).
func sssdGPOProblem(service string) string {
	var bad []string
	for sec, opts := range sssdSections() {
		if !strings.HasPrefix(sec, "domain/") {
			continue
		}
		if !sssdDomainBlocks(opts, service) {
			continue
		}
		bad = append(bad, sec)
	}
	if len(bad) == 0 {
		return ""
	}
	sort.Strings(bad)
	return "sssd enforces group policy (GPO) for " + strings.Join(bad, ", ") + " and does not know the PAM service \"" + service +
		"\", so logins of AD users are refused after the password is accepted. Add \"ad_gpo_map_interactive = +" + service +
		"\" to that domain's section of " + sssdConfFile + " and restart sssd"
}

// sssdRestart makes sssd read its changed configuration; with reset it first stops sssd and deletes its cache, which
// holds the IDs it made for the domain's users and groups (replaceable in tests).
var sssdRestart = func(reset bool) error {
	run := func(args ...string) error {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s sssd: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if !reset {
		return run("try-restart", "sssd")
	}
	if err := run("stop", "sssd"); err != nil {
		return err
	}
	for _, dir := range []string{"/var/lib/sss/db", "/var/lib/sss/mc"} {
		if ents, err := os.ReadDir(dir); err == nil {
			for _, e := range ents {
				os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	return run("start", "sssd")
}

// mapSSSDService adds "+service" to ad_gpo_map_interactive in the [domain/...] sections named in secs of the
// sssd.conf text, and returns the new text.  An existing ad_gpo_map_interactive line gets ", +service" added; a section
// without one gets the line right after its header.
func mapSSSDService(conf, service string, secs []string) string {
	want := map[string]bool{}
	for _, s := range secs {
		want[s] = true
	}
	lines := strings.Split(conf, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		out = append(out, l)
		t := strings.TrimSpace(l)
		if len(t) < 2 || t[0] != '[' || !strings.HasSuffix(t, "]") || !want[t[1:len(t)-1]] {
			continue
		}
		// the section runs to the next header
		j := i + 1
		found := false
		for ; j < len(lines); j++ {
			bt := strings.TrimSpace(lines[j])
			if strings.HasPrefix(bt, "[") && strings.HasSuffix(bt, "]") {
				break
			}
			if k, v, ok := strings.Cut(bt, "="); ok && !found && strings.ToLower(strings.TrimSpace(k)) == "ad_gpo_map_interactive" {
				found = true
				if strings.TrimSpace(v) == "" {
					lines[j] = "ad_gpo_map_interactive = +" + service
				} else {
					lines[j] = strings.TrimRight(lines[j], " \t") + ", +" + service
				}
			}
		}
		if !found {
			out = append(out, "ad_gpo_map_interactive = +"+service)
		}
	}
	return strings.Join(out, "\n")
}

// The IDs sssd makes for Active Directory users (ldap_id_mapping) start at 200000 by default.  In an unprivileged
// container (LXC, Proxmox) only the IDs of its uid_map exist, usually 0-65535, and an account with an ID outside it
// cannot sign in.  The range below is inside such a map and clear of the system accounts.
const (
	sssdRangeMin  = 10000
	sssdRangeMax  = 60000
	sssdRangeSize = 50000
	sssdDefaultID = 200000 // where sssd's own default range begins
)

var procUIDMap = "/proc/self/uid_map"

// containerMaxID is the highest user ID that exists in this machine's user namespace; ok is false when the
// namespace is the host's (or the map cannot be read), which is the normal case.
func containerMaxID() (max uint64, ok bool) {
	b, err := os.ReadFile(procUIDMap)
	if err != nil {
		return 0, false
	}
	any := false
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) != 3 {
			continue
		}
		in, e1 := strconv.ParseUint(f[0], 10, 64)
		n, e2 := strconv.ParseUint(f[2], 10, 64)
		if e1 != nil || e2 != nil || n == 0 {
			continue
		}
		any = true
		if in+n-1 > max {
			max = in + n - 1
		}
	}
	return max, any && max < sssdDefaultID
}

// sssdRangeDomains lists the AD domains whose IDs would fall outside the container's map: sssd makes the IDs itself
// (ldap_id_mapping is not off) and the domain sets no range of its own.
func sssdRangeDomains() []string {
	var out []string
	for sec, opts := range sssdSections() {
		if !strings.HasPrefix(sec, "domain/") {
			continue
		}
		if strings.ToLower(opts["id_provider"]) != "ad" || strings.ToLower(opts["ldap_id_mapping"]) == "false" {
			continue
		}
		has := false
		for k := range opts {
			if strings.HasPrefix(k, "ldap_idmap_range_") {
				has = true
			}
		}
		if !has {
			out = append(out, sec)
		}
	}
	sort.Strings(out)
	return out
}

// addIDRange puts the three range lines right after the header of each section in secs.
func addIDRange(conf string, secs []string) string {
	want := map[string]bool{}
	for _, s := range secs {
		want[s] = true
	}
	var out []string
	for _, l := range strings.Split(conf, "\n") {
		out = append(out, l)
		if t := strings.TrimSpace(l); len(t) > 2 && t[0] == '[' && strings.HasSuffix(t, "]") && want[t[1:len(t)-1]] {
			out = append(out, "ldap_idmap_range_min = "+strconv.Itoa(sssdRangeMin),
				"ldap_idmap_range_max = "+strconv.Itoa(sssdRangeMax),
				"ldap_idmap_range_size = "+strconv.Itoa(sssdRangeSize))
		}
	}
	return strings.Join(out, "\n")
}

// fixSSSD makes the changes to sssd.conf that sign-ins of Active Directory users need, keeps the old file beside it
// (sssd.conf.<service>.bak, once) and restarts sssd:
//   - maps the PAM service when group policy would refuse it (sssdGPOProblem);
//   - in an unprivileged container, sets an ID range inside the container's map for a domain that has none; sssd's cache
//     is deleted then, because the IDs it made are kept there.
//
// It reports what it did; "" and nil when there was nothing to do.  A domain defined only in a conf.d snippet is not
// edited, and what to add is returned as an error.
func fixSSSD(service string) (string, error) {
	var notes []string // things not done, for the error
	b, rerr := os.ReadFile(sssdConfFile)
	inFile := func(sec string) bool { return rerr == nil && strings.Contains(string(b), "["+sec+"]") }

	var gpoSecs []string
	if problem := sssdGPOProblem(service); problem != "" {
		for sec, opts := range sssdSections() {
			if strings.HasPrefix(sec, "domain/") && sssdDomainBlocks(opts, service) && inFile(sec) {
				gpoSecs = append(gpoSecs, sec)
			}
		}
		if len(gpoSecs) == 0 {
			notes = append(notes, problem+" (the domain is not in "+sssdConfFile+" itself, so it was not changed)")
		}
	}

	var rngSecs []string
	if max, ok := containerMaxID(); ok {
		if doms := sssdRangeDomains(); len(doms) > 0 {
			if max < sssdRangeMax {
				notes = append(notes, fmt.Sprintf("this container has user IDs up to %d only, and sssd makes IDs from 200000 up for %s, so its users cannot sign in: give the container a larger ID map, or set an ldap_idmap_range_* inside it", max, strings.Join(doms, ", ")))
			} else {
				for _, sec := range doms {
					if inFile(sec) {
						rngSecs = append(rngSecs, sec)
					} else {
						notes = append(notes, sec+" is not in "+sssdConfFile+" itself, so its ID range was not set: add ldap_idmap_range_min = 10000, ldap_idmap_range_max = 60000 and ldap_idmap_range_size = 50000 to it")
					}
				}
			}
		}
	}
	sort.Strings(gpoSecs)
	sort.Strings(rngSecs)
	fail := func() error {
		if len(notes) == 0 {
			return nil
		}
		return errors.New(strings.Join(notes, "; "))
	}
	if len(gpoSecs) == 0 && len(rngSecs) == 0 {
		return "", fail()
	}
	st, err := os.Stat(sssdConfFile)
	if err != nil {
		return "", err
	}
	bak := sssdConfFile + "." + service + ".bak"
	if _, err := os.Stat(bak); err != nil {
		if err := os.WriteFile(bak, b, 0o600); err != nil {
			return "", fmt.Errorf("saving %s: %v", bak, err)
		}
	}
	text := string(b)
	if len(gpoSecs) > 0 {
		text = mapSSSDService(text, service, gpoSecs)
	}
	if len(rngSecs) > 0 {
		text = addIDRange(text, rngSecs)
	}
	tmp := sssdConfFile + ".tmp-" + service
	if err := os.WriteFile(tmp, []byte(text), st.Mode().Perm()); err != nil {
		return "", err
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		os.Chown(tmp, int(sys.Uid), int(sys.Gid))
	}
	if err := os.Rename(tmp, sssdConfFile); err != nil {
		os.Remove(tmp)
		return "", err
	}
	var did []string
	if len(gpoSecs) > 0 {
		did = append(did, "added \"ad_gpo_map_interactive = +"+service+"\" to "+strings.Join(gpoSecs, ", "))
	}
	if len(rngSecs) > 0 {
		did = append(did, fmt.Sprintf("set the ID range %d-%d (this container only has IDs up to %d) for %s", sssdRangeMin, sssdRangeMax, func() uint64 { m, _ := containerMaxID(); return m }(), strings.Join(rngSecs, ", ")))
	}
	msg := strings.Join(did, "; ") + " in " + sssdConfFile + " (the old file is " + bak + ")"
	reset := len(rngSecs) > 0
	if err := sssdRestart(reset); err != nil {
		return msg, fmt.Errorf("%s, but sssd was not restarted: %v; restart it", msg, err)
	}
	if reset {
		msg += " and restarted sssd with its cache cleared"
	} else {
		msg += " and restarted sssd"
	}
	return msg, fail()
}

// sssdDomainBlocks says whether the options of a domain make sssd refuse the PAM service by group policy.
func sssdDomainBlocks(opts map[string]string, service string) bool {
	if strings.ToLower(opts["id_provider"]) != "ad" && strings.ToLower(opts["access_provider"]) != "ad" {
		return false
	}
	switch strings.ToLower(opts["ad_gpo_access_control"]) {
	case "disabled", "permissive":
		return false
	}
	if d := strings.ToLower(opts["ad_gpo_default_right"]); d != "" && d != "deny" {
		return false
	}
	for k, v := range opts {
		if !strings.HasPrefix(k, "ad_gpo_map_") {
			continue
		}
		for _, item := range strings.Split(v, ",") {
			if strings.TrimSpace(item) == "+"+service {
				return false
			}
		}
	}
	return true
}
