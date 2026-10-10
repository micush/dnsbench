package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every field the GUI labels must have an entry in its page's help (webui/help.js, a
// ["fields", [["Label", "..."]]] block).  Labels come from the `l: "…"` specs in app.js; the
// controls that are labelled another way are listed below.
func TestHelpCoversEveryField(t *testing.T) {
	appB, err := os.ReadFile("webui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	helpB, err := os.ReadFile("webui/help.js")
	if err != nil {
		t.Fatal(err)
	}
	app, help := string(appB), string(helpB)

	topic := func(name string) string {
		i := strings.Index(help, "\n  "+name+": {\n    title")
		if i < 0 {
			t.Fatalf("help.js has no topic %q", name)
		}
		rest := help[i+5:]
		if j := regexp.MustCompile(`\n  [a-z_]+: \{`).FindStringIndex(rest); j != nil {
			rest = rest[:j[0]]
		}
		return rest
	}
	has := func(tp, label string) bool {
		return strings.Contains(topic(tp), `["`+strings.ReplaceAll(label, `"`, `\"`)+`",`)
	}

	cfgAt := strings.Index(app, "const LB = [")
	if cfgAt < 0 {
		t.Fatal("cannot find the Settings schema in app.js")
	}
	found := 0
	for _, m := range regexp.MustCompile(`\bl: "([^"]+)"`).FindAllStringSubmatchIndex(app, -1) {
		label := app[m[2]:m[3]]
		tp := "topology"
		if m[0] > cfgAt {
			tp = "config"
		}
		found++
		if !has(tp, label) {
			t.Errorf("field %q (%s page) has no entry in the help's Fields list", label, tp)
		}
	}
	if found < 70 {
		t.Errorf("found only %d labelled fields in app.js; the label pattern probably changed", found)
	}

	extra := map[string][]string{
		"topology": {"Time range"},
		"config":   {"Username", "Password", "Update every node automatically when a newer release is staged (one node at a time)", "Certificate", "Private key", "Main name", "Other names (one per line)", "IP addresses (one per line, optional)", "Pending CSR"},
		"cluster":  {"Node", "Join code"},
		"updates":  {"Release archive"},
		"anycast":  {"Local AS number", "Router ID", "New neighbor address", "New neighbor AS", "New neighbor description", "New neighbor password"},
		"history":  {"Compare version", "Configuration file"},
		"stats":    {"Time range", "From", "To"},
		"host":     {"Time range", "From", "To"},
		"log":      {"Filter text", "Level", "Time range", "Lines", "Live"},
		"node":     {"Action", "When", "Minutes", "Time of day"},
		"users":    {"Name", "Password", "Expires"},
	}
	for tp, ls := range extra {
		for _, l := range ls {
			if !has(tp, l) {
				t.Errorf("control %q (%s page) has no entry in the help's Fields list", l, tp)
			}
		}
	}
}
