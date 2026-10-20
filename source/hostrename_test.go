package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanHostname(t *testing.T) {
	for in, want := range map[string]string{"ns5": "ns5", " NS5 ": "ns5", "dns-1.corp.example": "dns-1.corp.example", "a": "a"} {
		if got, err := cleanHostname(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	long := strings.Repeat("a", 64)
	for _, in := range []string{"", " ", "-ns", "ns-", "n s", "ns_1", "ns..1", ".ns", "ns.", "ns;reboot", "n\ns", long, strings.Repeat("a.", 130)} {
		if got, err := cleanHostname(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

func TestRewriteHosts(t *testing.T) {
	body := "127.0.0.1\tlocalhost\n127.0.1.1 ns1.corp.example ns1 # this host\n10.0.0.9 other ns1x\n# ns1 stays in a comment\n::1 localhost ip6-localhost\n"
	out, ok := rewriteHosts(body, "ns1.corp.example", "ns7.corp.example")
	want := "127.0.0.1\tlocalhost\n127.0.1.1 ns7.corp.example ns7 # this host\n10.0.0.9 other ns1x\n# ns1 stays in a comment\n::1 localhost ip6-localhost\n"
	if !ok || out != want {
		t.Fatalf("ok=%v\n%q\nwant\n%q", ok, out, want)
	}
	out, ok = rewriteHosts("127.0.1.1 ns1\n10.0.0.1 ns1\n", "ns1", "box")
	if !ok || out != "127.0.1.1 box\n10.0.0.1 box\n" {
		t.Fatalf("short name: %v %q", ok, out)
	}
	if out, ok := rewriteHosts("127.0.0.1 localhost\n", "ns1", "box"); ok || out != "127.0.0.1 localhost\n" {
		t.Fatalf("no entry: %v %q", ok, out)
	}
	// the address column is never touched, even when it looks like the name
	if out, _ := rewriteHosts("ns1 ns1\n", "ns1", "box"); out != "ns1 box\n" {
		t.Fatalf("address field: %q", out)
	}
}

func hostRenameSandbox(t *testing.T, name, hosts string) (ran *[]string, current *string) {
	t.Helper()
	dir := t.TempDir()
	o1, o2, o3, o4, o5 := hostsPath, hostnamePath, hostRenameRun, hostnameNow, ""
	_ = o5
	t.Cleanup(func() { hostsPath, hostnamePath, hostRenameRun, hostnameNow = o1, o2, o3, o4 })
	hostsPath, hostnamePath = filepath.Join(dir, "hosts"), filepath.Join(dir, "hostname")
	if err := os.WriteFile(hostsPath, []byte(hosts), 0o640); err != nil {
		t.Fatal(err)
	}
	cur, log := name, []string{}
	hostnameNow = func() string { return cur }
	hostRenameRun = func(n string, a ...string) error {
		log = append(log, n+" "+strings.Join(a, " "))
		if n == "hostnamectl" {
			cur = a[len(a)-1]
		}
		return nil
	}
	return &log, &cur
}

func TestRenameHost(t *testing.T) {
	ran, cur := hostRenameSandbox(t, "ns1", "127.0.0.1 localhost\n127.0.1.1 ns1\n")
	m := &Mgmt{}
	if _, _, err := m.RenameHost("NS7", "test"); err != nil {
		t.Fatal(err)
	}
	if *cur != "ns7" || len(*ran) != 1 || (*ran)[0] != "hostnamectl set-hostname ns7" {
		t.Fatalf("name %q, ran %v", *cur, *ran)
	}
	b, _ := os.ReadFile(hostsPath)
	if string(b) != "127.0.0.1 localhost\n127.0.1.1 ns7\n" {
		t.Fatalf("hosts: %q", b)
	}
	if st, _ := os.Stat(hostsPath); st.Mode().Perm() != 0o640 {
		t.Fatalf("the file's mode changed: %v", st.Mode().Perm())
	}
	if msg, _, err := m.RenameHost("ns7", "test"); err != nil || !strings.Contains(msg, "already") || len(*ran) != 1 {
		t.Fatalf("same name: %q %v %v", msg, err, *ran)
	}
	for _, bad := range []string{"", "a b", "x;y"} {
		if _, _, err := m.RenameHost(bad, "test"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if len(*ran) != 1 {
		t.Fatalf("a refused name reached the system: %v", *ran)
	}
}

// A host with no line for its name keeps /etc/hosts as it is; a failing hostnamectl falls back to hostname + /etc/hostname,
// and a failure of both changes nothing.
func TestRenameHostFallbacksAndFailures(t *testing.T) {
	ran, _ := hostRenameSandbox(t, "ns1", "127.0.0.1 localhost\n")
	m := &Mgmt{}
	if _, _, err := m.RenameHost("ns2", "test"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(hostsPath); string(b) != "127.0.0.1 localhost\n" {
		t.Fatalf("hosts changed without an entry: %q", b)
	}
	_ = ran

	ran2, cur := hostRenameSandbox(t, "ns1", "127.0.1.1 ns1\n")
	hostRenameRun = func(n string, a ...string) error {
		*ran2 = append(*ran2, n+" "+strings.Join(a, " "))
		if n == "hostnamectl" {
			return errors.New("no systemd")
		}
		*cur = a[0]
		return nil
	}
	if _, _, err := m.RenameHost("ns3", "test"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(hostnamePath); string(b) != "ns3\n" || *cur != "ns3" {
		t.Fatalf("fallback: file %q name %q", b, *cur)
	}

	hostRenameSandbox(t, "ns1", "127.0.1.1 ns1\n")
	hostRenameRun = func(string, ...string) error { return errors.New("denied") }
	if _, _, err := m.RenameHost("ns4", "test"); err == nil {
		t.Fatal("a failed rename was reported as done")
	}
	if b, _ := os.ReadFile(hostsPath); string(b) != "127.0.1.1 ns1\n" {
		t.Fatalf("hosts changed although the name did not: %q", b)
	}
}

// If /etc/hosts cannot be written the old host name is put back.
func TestRenameHostRollsBack(t *testing.T) {
	ran, cur := hostRenameSandbox(t, "ns1", "127.0.1.1 ns1\n")
	old := hostsWrite
	defer func() { hostsWrite = old }()
	hostsWrite = func(string, []byte) error { return errors.New("read-only file system") }
	if _, _, err := (&Mgmt{}).RenameHost("ns5", "test"); err == nil {
		t.Fatal("no error")
	}
	if *cur != "ns1" || len(*ran) != 2 {
		t.Fatalf("not rolled back: name %q ran %v", *cur, *ran)
	}
}

// The rename is aimed at another node through the relay like the other node actions.
func TestHostRenameIsRelayable(t *testing.T) {
	if err := proxyAllowed("POST", "/api/host/rename"); err != nil {
		t.Fatal(err)
	}
}

type regCall struct{ name, old, addr string }

func regSandbox(t *testing.T, fail bool) (*Mgmt, *[]regCall) {
	t.Helper()
	ipn := func(s string) net.Addr { ip, n, _ := net.ParseCIDR(s); n.IP = ip; return n }
	o := nodeIfacesFn
	t.Cleanup(func() { nodeIfacesFn = o })
	nodeIfacesFn = func() []nodeIfaceRaw {
		return []nodeIfaceRaw{{Name: "eth0", Ethernet: true, Addrs: []net.Addr{ipn("fe80::1/64"), ipn("2001:db8::5/64"), ipn("10.0.0.5/24"), ipn("10.0.0.6/24")}}}
	}
	var calls []regCall
	m := &Mgmt{regNameFn: func(_ context.Context, name, old string, a netip.Addr) ([]string, error) {
		calls = append(calls, regCall{name, old, a.String()})
		if fail {
			return []string{"A of " + name + ": no zone found"}, errors.New("no zone found for the name")
		}
		return []string{"ok"}, nil
	}}
	return m, &calls
}

func TestRenameHostRegistersAFullNameInDNS(t *testing.T) {
	hostRenameSandbox(t, "ns1", "127.0.1.1 ns1\n")
	m, calls := regSandbox(t, false)
	msg, err := renameOK(t, m, "NS7.Corp.Example")
	if err != nil || !strings.Contains(msg, "registered ns7.corp.example in DNS") {
		t.Fatalf("%q %v", msg, err)
	}
	want := []regCall{{"ns7.corp.example", "ns1.corp.example", "10.0.0.5"}, {"ns7.corp.example", "ns1.corp.example", "2001:db8::5"}}
	if len(*calls) != 2 || (*calls)[0] != want[0] || (*calls)[1] != want[1] {
		t.Fatalf("one update for the first v4 and the first global v6, the old short name removed in the new domain: %+v", *calls)
	}
	// the old name was a full name too: that one is removed
	*calls = nil
	hostRenameSandbox(t, "ns7.corp.example", "")
	if _, err := renameOK(t, m, "ns8.other.example"); err != nil || (*calls)[0].name != "ns8.other.example" || (*calls)[0].old != "ns7.corp.example" {
		t.Fatalf("%+v %v", *calls, err)
	}
}

func renameOK(t *testing.T, m *Mgmt, name string) (string, error) {
	t.Helper()
	msg, _, err := m.RenameHost(name, "test")
	return msg, err
}

func TestRenameHostShortNameSendsNothing(t *testing.T) {
	hostRenameSandbox(t, "ns1", "")
	m, calls := regSandbox(t, false)
	if msg, err := renameOK(t, m, "ns2"); err != nil || len(*calls) != 0 || strings.Contains(msg, "DNS") {
		t.Fatalf("%q %+v %v", msg, *calls, err)
	}
}

func TestRenameHostDNSFailureKeepsTheRename(t *testing.T) {
	_, cur := hostRenameSandbox(t, "ns1", "127.0.1.1 ns1\n")
	m, _ := regSandbox(t, true)
	msg, bad, err := m.RenameHost("ns2.corp.example", "test")
	if err != nil || *cur != "ns2.corp.example" || bad == "" || !strings.Contains(msg, "DNS was not fully updated") {
		t.Fatalf("%q %q %v cur=%s", msg, bad, err, *cur)
	}
	// the same full name again only registers, without touching the host
	m2, calls := regSandbox(t, false)
	if msg, err := renameOK(t, m2, "ns2.corp.example"); err != nil || len(*calls) != 2 || !strings.Contains(msg, "already called ns2.corp.example") {
		t.Fatalf("%q %v %+v", msg, err, *calls)
	}
}
