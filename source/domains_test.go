package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestParseDomains(t *testing.T) {
	got := parseDomains("# comment\n\n  Example.COM \nexample.com\nb.example\n# another\nhas space.example\r\nc.example\r\n")
	if want := []string{"example.com", "b.example", "c.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuiltInDomainsAreUsable(t *testing.T) {
	domainRe := regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	d := parseDomains(defaultDomains)
	if len(d) < 100 {
		t.Fatalf("built-in list has only %d domains", len(d))
	}
	for _, x := range d {
		if !domainRe.MatchString(x) {
			t.Errorf("built-in list has a bad name: %q", x)
		}
	}
}

func TestPresetDomainsFileThenFallback(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "domains.txt")
	os.WriteFile(f, []byte("# mine\none.example\ntwo.example\n"), 0o644)
	if d, src := presetDomains(f); src != "file" || !reflect.DeepEqual(d, []string{"one.example", "two.example"}) {
		t.Fatalf("file: %v %s", d, src)
	}
	// edits apply on the next call, with no restart
	os.WriteFile(f, []byte("three.example\n"), 0o644)
	if d, _ := presetDomains(f); !reflect.DeepEqual(d, []string{"three.example"}) {
		t.Fatalf("edit not picked up: %v", d)
	}
	// missing, empty and comment-only files fall back to the built-in list
	os.WriteFile(f, []byte("# nothing here\n\n"), 0o644)
	for _, p := range []string{filepath.Join(dir, "missing.txt"), f, ""} {
		if d, src := presetDomains(p); src != "built-in" || len(d) < 100 {
			t.Errorf("%q: %d domains from %s", p, len(d), src)
		}
	}
}

func TestPresetDomainsEndpoint(t *testing.T) {
	s := newTestServer(t)
	f := filepath.Join(t.TempDir(), "domains.txt")
	os.WriteFile(f, []byte("only.example\n"), 0o644)
	s.app.cfg.DomainsFile = f
	hdr := map[string]string{"X-Session-Token": s.apiToken(t)}
	_, body := s.do(t, "GET", "/api/preset-domains", "", hdr)
	if !strings.Contains(body, `"only.example"`) || !strings.Contains(body, `"source":"file"`) {
		t.Fatalf("body %s", body)
	}
}
