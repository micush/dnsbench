package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestServerNamesValidation(t *testing.T) {
	d := defaultDNS()
	d.Servers = []string{"8.8.8.8", "[2001:db8::53]:5353"}
	d.Queries = []DNSQuery{{Name: "example.com", Type: "A"}}
	d.ServerNames = map[string]string{"8.8.8.8:53": "  google-a  ", "[2001:db8::53]:5353": "", "8.8.8.8": "google-a"}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(d.ServerNames) != 1 || d.ServerNames["8.8.8.8:53"] != "google-a" {
		t.Fatalf("names are keyed by the normalised server, trimmed, and an empty one is dropped: %v", d.ServerNames)
	}
	// none left at all: the key disappears from the file
	d.ServerNames = map[string]string{"8.8.8.8": " "}
	if err := d.Validate(); err != nil || d.ServerNames != nil {
		t.Fatalf("empty names: %v %v", d.ServerNames, err)
	}
	if b, _ := json.Marshal(d); strings.Contains(string(b), "server_names") {
		t.Fatalf("an empty map is written out: %s", b)
	}
	for name, d2 := range map[string]map[string]string{
		"not a server": {"9.9.9.9": "x"},
		"too long":     {"8.8.8.8": strings.Repeat("a", 41)},
		"control char": {"8.8.8.8": "bad\x07name"},
	} {
		d.ServerNames = d2
		if d.Validate() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	d.ServerNames = map[string]string{"8.8.8.8": strings.Repeat("é", 40)}
	if err := d.Validate(); err != nil {
		t.Fatalf("40 characters (not bytes) are allowed: %v", err)
	}
	// round trip through the file
	d.ServerNames = map[string]string{"8.8.8.8": "google-a"}
	b, _ := json.Marshal(d)
	var back DNSConfig
	if err := back.UnmarshalJSON(b); err != nil || back.ServerNames["8.8.8.8"] != "google-a" {
		t.Fatalf("round trip: %v %v", back.ServerNames, err)
	}
}

func TestCanvasEditServerNames(t *testing.T) {
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{}
	do := func(e canvasEdit) string {
		t.Helper()
		msg, err := applyCanvasEdit(dc, e)
		if err != nil {
			t.Fatalf("%+v: %v", e, err)
		}
		return msg
	}
	do(canvasEdit{Action: "add", Kind: "gateway", VIP: "10.0.0.1/24", Interface: "eth0"})
	do(canvasEdit{Action: "add", Kind: "server", Group: 1, Server: "10.0.0.53", Name: "example.com", Label: "dns-a"})
	do(canvasEdit{Action: "add", Kind: "server", Group: 1, Server: "10.0.0.54", Name: "example.com"})
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	d := dc.Groups[0].DNS
	if d.ServerNames["10.0.0.53:53"] != "dns-a" || len(d.ServerNames) != 1 {
		t.Fatalf("a name given while adding: %v", d.ServerNames)
	}
	if g := dc.Groups[0]; g.Name != "" {
		t.Fatalf("the gateway must not take the server's name: %q", g.Name)
	}
	do(canvasEdit{Action: "set", Kind: "server", Group: 1, Server: "10.0.0.54", Label: "dns-b"})
	do(canvasEdit{Action: "set", Kind: "server", Group: 1, Server: "10.0.0.53", Label: "primary"})
	if err := dc.Validate(); err != nil || d.ServerNames["10.0.0.53:53"] != "primary" || d.ServerNames["10.0.0.54:53"] != "dns-b" {
		t.Fatalf("renamed: %v %v", d.ServerNames, err)
	}
	do(canvasEdit{Action: "set", Kind: "server", Group: 1, Server: "10.0.0.53", Label: "-"})
	if _, has := d.ServerNames["10.0.0.53:53"]; has {
		t.Fatalf("not cleared: %v", d.ServerNames)
	}
	for e, want := range map[canvasEdit]string{
		{Action: "set", Kind: "server", Group: 1, Server: "10.0.0.99", Label: "x"}: "not on gateway",
		{Action: "set", Kind: "server", Group: 1, Server: "10.0.0.53"}:             "--label",
		{Action: "set", Kind: "server", Group: 1, Label: "x"}:                      "--server",
	} {
		if _, err := applyCanvasEdit(dc, e); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%+v: want %q, got %v", e, want, err)
		}
	}
	// a name too long is refused when the config is validated
	do(canvasEdit{Action: "set", Kind: "server", Group: 1, Server: "10.0.0.54", Label: strings.Repeat("x", 60)})
	if dc.Validate() == nil {
		t.Fatal("a 60-character name was accepted")
	}
	do(canvasEdit{Action: "set", Kind: "server", Group: 1, Server: "10.0.0.54", Label: "dns-b"})
	// deleting the server takes its name along (a stale name would make the config invalid)
	do(canvasEdit{Action: "del", Kind: "server", Group: 1, Server: "10.0.0.54"})
	if err := dc.Validate(); err != nil || len(d.ServerNames) != 0 {
		t.Fatalf("after deleting: %v %v", d.ServerNames, err)
	}
}

func TestCanvasCarriesServerNames(t *testing.T) {
	a, b := newFakeDNS(t), newFakeDNS(t)
	d := testDNSCfg(a.addr, b.addr)
	d.ServerNames = map[string]string{a.addr: "dns-a"}
	g := defaultGroup()
	g.DNSProxy, g.DNS = true, &d
	dc := newDaemonConfig()
	dc.Groups = []GroupConfig{g}
	if err := dc.Validate(); err != nil {
		t.Fatal(err)
	}
	s := NewSupervisor(context.Background(), dc)
	s.refreshPool(dc, true)
	t.Cleanup(func() { s.StopAll() })
	cv := buildCanvas(dc, nil, s.poolList())
	got := map[string]string{}
	for _, sv := range cv[0].Servers {
		got[sv.Addr] = sv.Name
	}
	if got[a.addr] != "dns-a" || got[b.addr] != "" {
		t.Fatalf("names on the canvas: %v", got)
	}
	js, _ := json.Marshal(cv[0].Servers)
	var raw []map[string]any
	if err := json.Unmarshal(js, &raw); err != nil {
		t.Fatal(err)
	}
	withName := 0
	for _, m := range raw {
		if _, ok := m["name"]; ok {
			withName++
		}
	}
	if withName != 1 {
		t.Fatalf("only the named server carries a name field: %s", js)
	}

	// a rename is only a label: the pool keeps running (probe history, cache) and is not rebuilt
	p := s.poolFor(1)
	nu := *dc
	d2 := d
	d2.ServerNames = map[string]string{a.addr: "renamed", b.addr: "dns-b"}
	g2 := g
	g2.DNS = &d2
	nu.Groups = []GroupConfig{g2}
	s.refreshPool(&nu, true)
	if s.poolFor(1) != p {
		t.Fatal("renaming a server restarted its pool")
	}
	if cv := buildCanvas(&nu, nil, s.poolList()); cv[0].Servers[0].Name != "renamed" && cv[0].Servers[1].Name != "renamed" {
		t.Fatalf("the new name is not drawn: %+v", cv[0].Servers)
	}
}

func TestServerNamesInHistoryAndClone(t *testing.T) {
	d := defaultDNS()
	d.ServerNames = map[string]string{"8.8.8.8:53": "a"}
	c := cloneDNS(d)
	c.ServerNames["8.8.8.8:53"] = "changed"
	if d.ServerNames["8.8.8.8:53"] != "a" {
		t.Fatal("cloneDNS shares the names map")
	}
	a, b := newDaemonConfig(), newDaemonConfig()
	a.DNS, b.DNS = d, d
	b.DNS.ServerNames = map[string]string{"8.8.8.8:53": "b"}
	found := false
	for _, s := range configSections(a, b) {
		if s.Label == "DNS server names" {
			found = true
		}
	}
	if !found {
		t.Fatal("a rename is not in the history entry")
	}
}
