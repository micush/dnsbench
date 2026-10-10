package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every page in the sidebar has a help topic (the "?" in the top right), the
// topology page's included, and the help script is served.
func TestEveryPageHasHelp(t *testing.T) {
	app, err := os.ReadFile("webui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	help, err := os.ReadFile("webui/help.js")
	if err != nil {
		t.Fatal(err)
	}
	nav := regexp.MustCompile(`\["([a-z]+)", "[A-Za-z]+"\]`).FindAllStringSubmatch(
		string(app[strings.Index(string(app), "const NAV_GROUPS"):strings.Index(string(app), "const TABS")]), -1)
	ids := []string{"topology"}
	for _, m := range nav {
		ids = append(ids, m[1])
	}
	if len(ids) < 9 {
		t.Fatalf("could not read the sidebar: %v", ids)
	}
	for _, id := range ids {
		if !regexp.MustCompile(`(?m)^  ` + id + `: \{`).Match(help) {
			t.Errorf("no help topic for the %q page", id)
		}
	}
	if strings.Contains(string(app), `"help", "Help"`) || strings.Contains(string(app), "VIEWS.help") {
		t.Error("the old Help page is still there")
	}
}

func TestHelpScriptServedAndOldEndpointGone(t *testing.T) {
	e := newWebEnv(t)
	if r := e.do("GET", "/help.js", nil); r.code != 200 || !strings.Contains(string(r.raw), "DDGW_HELP") {
		t.Fatalf("help.js: %d", r.code)
	}
	e.login("alice", "pw")
	if r := e.do("GET", "/api/help", nil, withAuth(e, false)); r.code != 404 {
		t.Fatalf("the old /api/help should be gone, got %d", r.code)
	}
}
