package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Releases before v367 were called ddgw.  The rename is complete (binary, service, directories,
// PAM service, group, interface names) except for what the cluster speaks on the wire, which
// stays as it was so old and renamed nodes keep working together during a rolling update.
// This file moves an old install's state to the new names the first time the daemon starts as
// root; every step only acts when the old thing exists and the new one does not, and nothing is
// ever overwritten.

// paths and commands are variables so the test can point them at a scratch tree
var (
	oldStateDir   = "/var/lib/ddgw"
	oldRunDir     = "/run/ddgw"
	oldPAMFile    = "/etc/pam.d/ddgw"
	newPAMFile    = "/etc/pam.d/dnsdir"
	oldNMFile     = "/etc/NetworkManager/conf.d/90-ddgw-unmanaged.conf"
	newNMFile     = "/etc/NetworkManager/conf.d/90-dnsdir-unmanaged.conf"
	groupFile     = "/etc/group"
	oldFRRBackup  = "/etc/frr/frr.conf.pre-ddgw"
	newFRRBackup  = "/etc/frr/frr.conf.pre-dnsdir"
	renameHostRun = func(name string, args ...string) error { return exec.Command(name, args...).Run() }
	oldLinkRe     = regexp.MustCompile(`^ddgw\d+\.\d+$`)
	listLinkNames = func() []string {
		var out []string
		if ifs, err := net.Interfaces(); err == nil {
			for _, i := range ifs {
				out = append(out, i.Name)
			}
		}
		return out
	}
)

var (
	oldGroupRe = regexp.MustCompile(`("group"\s*:\s*)"ddgw"`)
	oldPAMRe   = regexp.MustCompile(`("pam_service"\s*:\s*)"ddgw"`)
)

// migrateOldInstall moves /var/lib/ddgw into newDir (files whose names start with "ddgw" are
// renamed to start with "dnsdir"), then the PAM service, the GUI group, the NetworkManager
// and FRR files and any leftover ddgw<group>.<slot> interfaces.  It reports what it did.
func migrateOldInstall(newDir string) (did []string, err error) {
	if _, e := os.Stat(oldStateDir); e != nil {
		return nil, nil // nothing from an older release
	}
	if oldStateDir == newDir {
		return nil, nil
	}
	if err := os.MkdirAll(newDir, 0o700); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(oldStateDir)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		name := e.Name()
		to := name
		if strings.HasPrefix(name, "ddgw") {
			to = "dnsdir" + strings.TrimPrefix(name, "ddgw")
		}
		dst := filepath.Join(newDir, to)
		if _, e2 := os.Lstat(dst); e2 == nil {
			continue
		}
		if e2 := os.Rename(filepath.Join(oldStateDir, name), dst); e2 != nil {
			err = e2
		}
	}
	if err == nil {
		_ = os.Remove(oldStateDir) // only when it is empty
	}
	did = append(did, "state "+oldStateDir+" → "+newDir)

	// the moved config and the update guard still name the old paths and the old PAM service/group
	if p := filepath.Join(newDir, "dnsdir.conf"); fileExists(p) {
		if b, e := os.ReadFile(p); e == nil {
			nb := bytes.ReplaceAll(b, []byte("/var/lib/ddgw"), []byte("/var/lib/dnsdir"))
			nb = bytes.ReplaceAll(nb, []byte("ddgw-web."), []byte("dnsdir-web."))
			nb = oldGroupRe.ReplaceAll(nb, []byte(`${1}"dnsdir"`))
			nb = oldPAMRe.ReplaceAll(nb, []byte(`${1}"dnsdir"`))
			if !bytes.Equal(nb, b) {
				_ = os.WriteFile(p, nb, 0o600)
			}
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(newDir, "*", "guard.json")); true {
		matches = append(matches, filepath.Join(newDir, "guard.json"))
		for _, p := range matches {
			if b, e := os.ReadFile(p); e == nil {
				nb := bytes.ReplaceAll(b, []byte("/var/lib/ddgw"), []byte("/var/lib/dnsdir"))
				nb = bytes.ReplaceAll(nb, []byte("ddgw.prev"), []byte("dnsdir.prev"))
				if !bytes.Equal(nb, b) {
					_ = os.WriteFile(p, nb, 0o600)
				}
			}
		}
	}

	// PAM service
	if fileExists(oldPAMFile) && !fileExists(newPAMFile) {
		if e := os.Rename(oldPAMFile, newPAMFile); e == nil {
			did = append(did, "PAM service "+oldPAMFile+" → "+newPAMFile)
		} else if err == nil {
			err = e
		}
	}
	// GUI group (members stay members: the group is renamed, not recreated)
	if groupExists("ddgw") && !groupExists("dnsdir") {
		if e := renameHostRun("groupmod", "-n", "dnsdir", "ddgw"); e == nil {
			did = append(did, "group ddgw → dnsdir")
		} else if err == nil {
			err = fmt.Errorf("groupmod: %w", e)
		}
	}
	// NetworkManager must keep ignoring our interfaces
	if fileExists(oldNMFile) && !fileExists(newNMFile) {
		if b, e := os.ReadFile(oldNMFile); e == nil {
			nb := bytes.ReplaceAll(b, []byte("ddgw"), []byte("dnsdir"))
			if os.WriteFile(newNMFile, nb, 0o644) == nil {
				_ = os.Remove(oldNMFile)
				did = append(did, "NetworkManager file")
			}
		}
	}
	// the copy of the FRR config this daemon made before it took the file over
	if fileExists(oldFRRBackup) && !fileExists(newFRRBackup) {
		if os.Rename(oldFRRBackup, newFRRBackup) == nil {
			did = append(did, "FRR backup")
		}
	}
	// an old process that left its interfaces behind would hold the same virtual MACs
	for _, n := range listLinkNames() {
		if oldLinkRe.MatchString(n) {
			_ = renameHostRun("ip", "link", "del", n)
			did = append(did, "removed leftover interface "+n)
		}
	}
	_ = os.Remove(filepath.Join(oldRunDir, "ddgw.sock"))
	_ = os.Remove(oldRunDir)
	return did, err
}

func fileExists(p string) bool { _, err := os.Lstat(p); return err == nil }

func groupExists(name string) bool {
	b, err := os.ReadFile(groupFile)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, name+":") {
			return true
		}
	}
	return false
}
