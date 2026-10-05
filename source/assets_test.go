package main

import (
	"regexp"
	"strings"
	"testing"
)

// The UI is self-contained: pages, styles, icons and charts all ship inside the
// binary. These tests keep it that way.

var pageSources = map[string]string{
	"login.html": loginHTML,
	"bench.html": benchHTML,
	"doc.html":   docHTML,
	"theme.js":   themeJS,
	"ui.css":     uiCSS,
	"charts.js":  chartsJS,
}

func TestUILoadsNothingFromOtherHosts(t *testing.T) {
	// A reference to another host: a src/href with a scheme or "//", a CSS url()
	// or @import that is not a data: URI, or a script fetching an absolute URL.
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:src|href|action|poster|data)\s*=\s*["']?\s*(?:https?:)?//`),
		regexp.MustCompile(`(?i)url\(\s*["']?\s*(?:https?:)?//`),
		regexp.MustCompile(`(?i)@import\b`),
		regexp.MustCompile(`(?i)\b(?:fetch|import|XMLHttpRequest|WebSocket|EventSource)\s*\(\s*["'` + "`" + `]\s*(?:https?|wss?):`),
		regexp.MustCompile(`(?i)\.open\(\s*["'][A-Z]+["']\s*,\s*["']\s*https?:`),
	}
	for name, src := range pageSources {
		for _, re := range patterns {
			if m := re.FindString(src); m != "" {
				t.Errorf("%s refers to another host: %q", name, m)
			}
		}
		for _, host := range []string{"jsdelivr", "cdnjs", "unpkg", "googleapis", "gstatic", "bootstrapcdn"} {
			if strings.Contains(strings.ToLower(src), host) {
				t.Errorf("%s mentions %s", name, host)
			}
		}
	}
}

func TestPagesUseTheEmbeddedAssets(t *testing.T) {
	for name, src := range map[string]string{"login.html": loginHTML, "bench.html": benchHTML, "doc.html": docHTML} {
		if !strings.Contains(src, `href="/ui.css"`) {
			t.Errorf("%s does not load /ui.css", name)
		}
	}
	if !strings.Contains(benchHTML, `src="/charts.js"`) {
		t.Error("bench.html does not load /charts.js")
	}
	for _, gone := range []string{"bootstrap", "Chart.defaults", "new Chart(", "data-bs-"} {
		for name, src := range pageSources {
			if strings.Contains(src, gone) {
				t.Errorf("%s still contains %q", name, gone)
			}
		}
	}
}

func TestEveryIconUsedHasARule(t *testing.T) {
	used := regexp.MustCompile(`\bbi-([a-z0-9]+(?:-[a-z0-9]+)*)`)
	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\.bi-([a-z0-9-]+)\{`).FindAllStringSubmatch(uiCSS, -1) {
		defined[m[1]] = true
	}
	if len(defined) == 0 {
		t.Fatal("ui.css defines no icons")
	}
	for name, src := range pageSources {
		if name == "ui.css" {
			continue
		}
		for _, m := range used.FindAllStringSubmatch(src, -1) {
			if !defined[m[1]] {
				t.Errorf("%s uses icon bi-%s, which ui.css does not define", name, m[1])
			}
		}
	}
	// Icons are painted as a mask, so a rule without the data URI would render a solid square.
	for _, line := range strings.Split(uiCSS, "\n") {
		if strings.HasPrefix(line, ".bi-") && !strings.Contains(line, `--i:url("data:image/svg+xml,`) {
			t.Errorf("icon rule without an image: %.60s", line)
		}
	}
}

func TestAssetsAreServedWithoutLogin(t *testing.T) {
	s := newTestServer(t)
	for path, want := range map[string]struct{ ctype, contains string }{
		"/ui.css":    {"text/css", ".btn-close"},
		"/charts.js": {"text/javascript", "DnsCharts"},
	} {
		resp, body := s.do(t, "GET", path, "", nil)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), want.ctype) {
			t.Errorf("%s: %d %s", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if !strings.Contains(body, want.contains) {
			t.Errorf("%s: body lacks %q", path, want.contains)
		}
	}
	// The login page needs the stylesheet before anyone has signed in.
	_, login := s.do(t, "GET", "/login", "", nil)
	if !strings.Contains(login, `href="/ui.css"`) {
		t.Error("login page does not link /ui.css")
	}
}

func TestDocMarkupUsesOwnClasses(t *testing.T) {
	out := renderMarkdown("# T\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```\ncode\n```\n")
	for _, want := range []string{`class="doc-table"`, `class="doc-code"`} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered markdown lacks %s:\n%s", want, out)
		}
	}
	for _, gone := range []string{"table-bordered", "bg-body-secondary", "table-sm"} {
		if strings.Contains(out, gone) {
			t.Errorf("rendered markdown still uses %q", gone)
		}
	}
	for _, class := range []string{".doc-table", ".doc-code"} {
		if !strings.Contains(uiCSS, class) {
			t.Errorf("ui.css does not style %s", class)
		}
	}
}

// Every class the pages and the Go-generated markup apply from the utility set must
// exist in ui.css, so a typo or a deleted rule shows up here rather than as a plain page.
func TestUtilityClassesAreDefined(t *testing.T) {
	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`\.([A-Za-z_][\w-]*)`).FindAllStringSubmatch(uiCSS, -1) {
		defined[m[1]] = true
	}
	// Classes the pages style themselves in their own <style> blocks do not need rules in ui.css.
	own := map[string]bool{}
	for _, src := range []string{loginHTML, benchHTML, docHTML} {
		for _, block := range regexp.MustCompile(`(?s)<style[^>]*>(.*?)</style>`).FindAllStringSubmatch(src, -1) {
			for _, m := range regexp.MustCompile(`\.([A-Za-z_][\w-]*)`).FindAllStringSubmatch(block[1], -1) {
				own[m[1]] = true
			}
		}
	}
	// The utility and component names the pages rely on (they kept their familiar names).
	for _, c := range []string{
		"btn", "btn-sm", "btn-primary", "btn-outline-primary", "btn-outline-secondary", "btn-outline-danger",
		"btn-outline-warning", "btn-close", "form-control", "form-label", "input-group", "input-group-text",
		"alert", "alert-danger", "badge", "bg-primary", "bg-success", "bg-danger", "spinner-border",
		"spinner-border-sm", "modal", "modal-content", "modal-header", "modal-title", "modal-body", "modal-footer",
		"container", "d-flex", "d-none", "align-items-center", "justify-content-between", "gap-2", "position-fixed",
		"top-0", "end-0", "w-100", "m-3", "mx-2", "mt-2", "mt-4", "mb-0", "mb-1", "mb-3", "mb-4", "me-1", "me-2",
		"ms-auto", "p-4", "pt-3", "py-2", "py-4", "fw-bold", "text-center", "text-decoration-none", "text-muted",
		"text-primary", "border-top", "small",
	} {
		if !defined[c] && !own[c] {
			t.Errorf("ui.css has no rule for .%s", c)
		}
	}
	// And the reverse for what the pages actually use: every class in a class="..." attribute that
	// looks like one of these families must be defined somewhere.
	family := regexp.MustCompile(`^(?:btn|modal|input-group|form|alert|spinner|bg|text|border|d|m[xtbes]?|p[xtbyes]?|w|fw|gap|align|justify|badge)(?:-|$)`)
	for name, src := range map[string]string{"login.html": loginHTML, "bench.html": benchHTML, "doc.html": docHTML, "server.go markup": `class="alert alert-danger text-center py-2"`} {
		for _, m := range regexp.MustCompile(`class="([^"$]*)"`).FindAllStringSubmatch(src, -1) {
			for _, c := range strings.Fields(m[1]) {
				if family.MatchString(c) && !defined[c] && !own[c] {
					t.Errorf("%s uses class %q, which neither ui.css nor the page defines", name, c)
				}
			}
		}
	}
}
