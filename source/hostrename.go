package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Renaming the host (the node menu's Rename, `dnsdir --hostname NAME`): the operating system's host name and its entry in
// /etc/hosts.  The name the Topology drawing shows for a node is the host name the node reports, so it follows within a
// few seconds.

var (
	hostsPath    = "/etc/hosts"
	hostnamePath = "/etc/hostname"
	// hostRenameRun runs a command (replaceable in tests)
	hostRenameRun = func(name string, args ...string) error {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	hostsWrite  = writeFileAtomic
	hostnameNow = func() string { h, _ := os.Hostname(); return h }
	hostLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// cleanHostname lowercases and checks a host name: letters, digits and hyphens in labels of 1–63 characters separated by
// dots, at most 253 characters in all (RFC 1123).
func cleanHostname(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", errors.New("give a host name")
	}
	if len(s) > 253 {
		return "", errors.New("a host name can be at most 253 characters")
	}
	for _, l := range strings.Split(s, ".") {
		if !hostLabelRe.MatchString(l) {
			return "", errors.New("a host name uses letters, digits and - in parts of up to 63 characters (no leading or trailing -), separated by dots")
		}
	}
	return s, nil
}

// rewriteHosts replaces the old host name by the new one on every address line of an /etc/hosts body: the full name, and the
// short name (the part before the first dot) when the old name had one.  Comments, spacing and every other name stay.  ok says
// whether any line named the old host.
func rewriteHosts(body, old, name string) (out string, ok bool) {
	short := func(s string) string { return strings.SplitN(s, ".", 2)[0] }
	tok := regexp.MustCompile(`\S+|\s+`)
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		code, comment := ln, ""
		if k := strings.Index(ln, "#"); k >= 0 {
			code, comment = ln[:k], ln[k:]
		}
		parts := tok.FindAllString(code, -1)
		n := 0 // index of the non-blank field: the first is the address
		for j, p := range parts {
			if strings.TrimSpace(p) == "" {
				continue
			}
			if n > 0 {
				switch {
				case p == old:
					parts[j], ok = name, true
				case strings.Contains(old, ".") && p == short(old):
					parts[j], ok = short(name), true
				}
			}
			n++
		}
		lines[i] = strings.Join(parts, "") + comment
	}
	return strings.Join(lines, "\n"), ok
}

// RenameHost changes this host's name and its /etc/hosts entry.  If the entry cannot be written the old host name is put
// back, so the two never disagree.  The old name must be on a line of /etc/hosts for that line to change; a host with no
// entry gets none added.  A full name (it has a dot) is then also registered in DNS (RFC 2136, like a client name) and the
// old name's address record is removed; a short name is only a local rename.  A DNS failure is reported but does not undo
// the rename.
func (m *Mgmt) RenameHost(name, actor string) (msg, dnsErr string, err error) {
	name, err = cleanHostname(name)
	if err != nil {
		return "", "", err
	}
	old := hostnameNow()
	if name == old {
		if !strings.Contains(name, ".") {
			return "the host is already called " + name, "", nil
		}
		reg, bad := m.registerHost(name, old, actor)
		return "the host is already called " + name + "; " + reg, bad, nil
	}
	hosts, herr := os.ReadFile(hostsPath)
	newHosts, hasEntry := rewriteHosts(string(hosts), old, name)
	if herr != nil && !os.IsNotExist(herr) {
		return "", "", fmt.Errorf("cannot read %s: %v", hostsPath, herr)
	}
	setName := func(n string) error {
		if err := hostRenameRun("hostnamectl", "set-hostname", n); err == nil {
			return nil
		}
		// no systemd-hostnamed: the kernel name and the file that sets it at boot
		if err := hostRenameRun("hostname", n); err != nil {
			return err
		}
		return os.WriteFile(hostnamePath, []byte(n+"\n"), 0o644)
	}
	if err := setName(name); err != nil {
		return "", "", fmt.Errorf("could not change the host name: %v", err)
	}
	if hasEntry {
		if err := hostsWrite(hostsPath, []byte(newHosts)); err != nil {
			_ = setName(old)
			return "", "", fmt.Errorf("could not update %s (the host name was put back): %v", hostsPath, err)
		}
	}
	warnf("host: %q renamed the host from %s to %s", actor, old, name)
	msg = "the host is now called " + name
	if strings.Contains(name, ".") {
		reg, bad := m.registerHost(name, old, actor)
		msg, dnsErr = msg+"; "+reg, bad
	}
	return msg, dnsErr, nil
}

// registerHost puts this host's full name into DNS: the first IPv4 and the first IPv6 address of the node (the
// address and PTR records), and removes the old name's records.  It returns one sentence saying how that went and, when something failed, the reason alone.
func (m *Mgmt) registerHost(fqdn, old, actor string) (msg, failure string) {
	if m.regNameFn == nil {
		f := "registration is not available on this node"
		return "DNS was not updated: " + f, f
	}
	// the old record: the old host name when it was a full name, else its short name in the new name's domain
	oldFqdn := ""
	switch {
	case old == fqdn:
	case strings.Contains(old, "."):
		oldFqdn = old
	case old != "" && old != strings.SplitN(fqdn, ".", 2)[0]:
		oldFqdn = old + "." + strings.SplitN(fqdn, ".", 2)[1]
	}
	var addrs []netip.Addr
	var have4, have6 bool
	for _, s := range addrList(ethernetAddrs()) {
		a, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		a = a.Unmap()
		if a.Is4() && !have4 {
			have4 = true
			addrs = append(addrs, a)
		} else if a.Is6() && !a.IsLinkLocalUnicast() && !have6 {
			have6 = true
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		f := "this node has no address to register"
		return "DNS was not updated: " + f, f
	}
	var fails []string
	for _, a := range addrs {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		lines, err := m.regNameFn(ctx, fqdn, oldFqdn, a)
		cancel()
		warnf("host: %q registered %s as %s in DNS: %s", actor, a, fqdn, strings.Join(lines, "; "))
		if err != nil {
			fails = append(fails, err.Error())
		}
	}
	if len(fails) > 0 {
		f := strings.Join(fails, "; ")
		return "DNS was not fully updated for " + fqdn + ": " + f, f
	}
	return "registered " + fqdn + " in DNS", ""
}

// writeFileAtomic replaces path with data through a temporary file next to it, keeping the file's mode.
func writeFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dnsdir-hosts-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
