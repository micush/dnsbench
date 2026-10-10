package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTshootRedactsSecrets(t *testing.T) {
	var v any
	json.Unmarshal([]byte(`{"groups":[{"key":"dgw","priority":100,"neighbors":[]}],"bgp":{"neighbors":[{"peer":"192.0.2.1","password":"hunter2","multihop":0}]},
		"web":{"key_file":"/x/key.pem","listen":"127.0.0.1:1"},"cluster":{"join_code":"ddgw-join-v1:abc","token":"zzz","code":""},"rcode":"NOERROR"}`), &v)
	out, n := redactJSON(v)
	raw, _ := json.Marshal(out)
	s := string(raw)
	for _, leak := range []string{"dgw\"", "hunter2", "ddgw-join-v1:abc", "zzz", "/x/key.pem"} {
		if leak == "dgw\"" {
			if strings.Contains(s, `"key":"dgw"`) {
				t.Errorf("the gateway key leaked: %s", s)
			}
			continue
		}
		if strings.Contains(s, leak) {
			t.Errorf("%q leaked: %s", leak, s)
		}
	}
	for _, keep := range []string{"192.0.2.1", "NOERROR", "127.0.0.1:1", `"priority":100`} {
		if !strings.Contains(s, keep) {
			t.Errorf("%q must stay: %s", keep, s)
		}
	}
	if n < 5 {
		t.Errorf("counted %d removals", n)
	}
}

func TestTshootRedactsText(t *testing.T) {
	in := "router bgp 65001\n neighbor 192.0.2.1 password hunter2\n neighbor 192.0.2.2 password 7 0822455D0A16\n ip prefix-list x\nsnmp community public\n"
	out, n := redactText(in)
	if strings.Contains(out, "hunter2") || strings.Contains(out, "0822455D0A16") || n < 2 {
		t.Fatalf("secrets left (%d): %s", n, out)
	}
	if !strings.Contains(out, "router bgp 65001") || !strings.Contains(out, "ip prefix-list x") {
		t.Fatalf("too much removed: %s", out)
	}
}

func TestTshootTarRoundTrip(t *testing.T) {
	in := map[string][]byte{"a/b.txt": []byte("one"), "README.txt": []byte("two"), "../evil": []byte("x")}
	tgz, err := tarFiles(in, "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := untarFiles(tgz)
	if err != nil || string(out["a/b.txt"]) != "one" || string(out["README.txt"]) != "two" {
		t.Fatalf("%v %v", out, err)
	}
	if _, bad := out["../evil"]; bad || string(out["evil"]) != "x" {
		t.Fatalf("a name above the node's folder must be kept inside it: %v", out)
	}
}

func TestTshootNodeBundle(t *testing.T) {
	n := newTNode(t, "t1")
	tgz, err := n.mg.TshootNode(false, "tester")
	if err != nil {
		t.Fatal(err)
	}
	files, err := untarFiles(tgz)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"README.txt", "ddgw/config.json", "ddgw/cluster.json", "system/system.txt", "net/ip-addr.txt", "ddgw/virtual-mac-tests.json", "ddgw/goroutines.txt"} {
		if _, ok := files[want]; !ok {
			t.Errorf("%s is missing; have %d files", want, len(files))
		}
	}
	if g := string(files["ddgw/goroutines.txt"]); !strings.Contains(g, "goroutine ") || !strings.Contains(g, "TestTshootNodeBundle") {
		t.Fatalf("the goroutine dump is missing or empty: %.200s", g)
	}
	if c := string(files["ddgw/config.json"]); strings.Contains(c, `"key": "dgw"`) {
		t.Errorf("the gateway key is in the bundle: %s", c)
	}
	if !strings.Contains(string(files["README.txt"]), "secrets were removed") {
		t.Error("the README does not say what was removed")
	}
	// the same through the op (what the CLI calls)
	if _, err := n.mg.Op("tshoot.node", nil, "tester"); err != nil {
		t.Fatal(err)
	}
}

// Two clustered nodes: one bundle with a folder per node, and a node that cannot be reached has a note, not a failure.
func TestTshootClusterBundle(t *testing.T) {
	a, b := twoNodeCluster(t)
	withWeb(t, a)
	withWeb(t, b)
	tgz, err := a.mg.TshootCluster(false, "tester")
	if err != nil {
		t.Fatal(err)
	}
	files, err := untarFiles(tgz)
	if err != nil {
		t.Fatal(err)
	}
	var mine, theirs int
	for n := range files {
		switch {
		case strings.Contains(n, "-this-node/ddgw/config.json"):
			mine++
		case strings.HasSuffix(n, "/ddgw/config.json"):
			theirs++
		}
	}
	if mine != 1 || theirs != 1 || !strings.Contains(string(files["README.txt"]), "collected") {
		t.Fatalf("one folder per node expected: %d %d, files %d", mine, theirs, len(files))
	}
	// the other node answers 503 to the relay: noted, and this node's own bundle is still there
	b.mg.webH = nil
	tgz, err = a.mg.TshootCluster(false, "tester")
	if err != nil {
		t.Fatal(err)
	}
	files, _ = untarFiles(tgz)
	lost := false
	for n := range files {
		if strings.HasSuffix(n, "/NOT-COLLECTED.txt") {
			lost = true
		}
	}
	if !lost || !strings.Contains(string(files["README.txt"]), "NOT COLLECTED") {
		t.Fatalf("an unreachable node must be noted: %s", files["README.txt"])
	}
}
