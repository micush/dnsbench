package main

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The palette lives in bench.html's CSS. These tests read it from there, so a
// future colour change cannot quietly make text unreadable or leave a dark-only
// colour in the light theme.

var cssVar = regexp.MustCompile(`--([a-z0-9-]+)\s*:\s*([^;]+);`)

// block returns the custom properties declared in the first CSS rule that
// starts with sel.
func block(t *testing.T, css, sel string) map[string]string {
	t.Helper()
	i := strings.Index(css, sel)
	if i < 0 {
		t.Fatalf("no %q rule", sel)
	}
	rest := css[i:]
	j := strings.Index(rest, "}")
	out := map[string]string{}
	for _, m := range cssVar.FindAllStringSubmatch(rest[:j], -1) {
		out[m[1]] = strings.TrimSpace(m[2])
	}
	return out
}

func hexRGB(t *testing.T, s string) (r, g, b float64) {
	t.Helper()
	if len(s) != 7 || s[0] != '#' {
		t.Fatalf("not a #rrggbb colour: %q", s)
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		t.Fatal(err)
	}
	return float64(v>>16&255) / 255, float64(v>>8&255) / 255, float64(v&255) / 255
}

func luminance(t *testing.T, s string) float64 {
	r, g, b := hexRGB(t, s)
	lin := func(c float64) float64 {
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func contrast(t *testing.T, a, b string) float64 {
	la, lb := luminance(t, a), luminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func lightness(t *testing.T, s string) float64 {
	r, g, b := hexRGB(t, s)
	return (math.Max(r, math.Max(g, b)) + math.Min(r, math.Min(g, b))) / 2
}

func palettes(t *testing.T) (dark, light map[string]string) {
	dark = block(t, benchHTML, "    :root {")
	light = block(t, benchHTML, `[data-theme="light"] {`)
	// The light block overrides the dark one: start from dark, apply light.
	merged := map[string]string{}
	for k, v := range dark {
		merged[k] = v
	}
	for k, v := range light {
		merged[k] = v
	}
	return dark, merged
}

func TestPaletteTextIsReadableInBothThemes(t *testing.T) {
	dark, light := palettes(t)
	for name, p := range map[string]map[string]string{"dark": dark, "light": light} {
		surfaces := []string{p["bg"], p["bg2"], p["bg3"]}
		min := func(fg string, bgs []string) float64 {
			m := 99.0
			for _, bg := range bgs {
				m = math.Min(m, contrast(t, fg, bg))
			}
			return m
		}
		mutedMin := 3.5
		if name == "light" {
			mutedMin = 4.5
		}
		for _, c := range []struct {
			what   string
			fg     string
			bgs    []string
			target float64
		}{
			{"text", p["text"], surfaces, 7},
			{"muted", p["muted"], surfaces, mutedMin},
			{"accent text", p["accent-text"], surfaces, 4.5},
			{"success", p["success"], surfaces, 4.5},
			{"danger", p["danger"], surfaces, 4.5},
			{"warn", p["warn"], surfaces, 4.5},
			{"code text", p["code-fg"], []string{p["bg3"]}, 4.5},
			{"terminal text", p["term-fg"], []string{p["term-bg"]}, 4.5},
			{"terminal ok", p["term-ok"], []string{p["term-bg"]}, 4.5},
			{"terminal warning", p["term-warn"], []string{p["term-bg"]}, 4.5},
			{"terminal error", p["term-err"], []string{p["term-bg"]}, 4.5},
			{"terminal heading", p["term-hdr"], []string{p["term-bg"]}, 4.5},
			{"terminal dim text", p["term-dim"], []string{p["term-bg"]}, 3},
		} {
			if got := min(c.fg, c.bgs); got < c.target {
				t.Errorf("%s theme: %s %s on its surfaces is %.2f:1, needs %.1f:1", name, c.what, c.fg, got, c.target)
			}
		}
	}
}

// The owner found the original dark theme too dark and asked for it to be
// three shades lighter; this keeps it from drifting back.
func TestDarkThemeIsNotTooDark(t *testing.T) {
	dark, _ := palettes(t)
	// The original page background #0f1117 had HSL lightness of about 7%;
	// three shades of 4 points each is 19%.
	for _, tok := range []string{"bg", "bg2", "bg3"} {
		if l := lightness(t, dark[tok]); l < 0.17 {
			t.Errorf("dark --%s %s has lightness %.0f%%, wanted at least 17%%", tok, dark[tok], l*100)
		}
	}
	if lightness(t, dark["bg"]) >= lightness(t, dark["bg2"]) || lightness(t, dark["bg2"]) >= lightness(t, dark["bg3"]) {
		t.Error("dark surfaces should step up from page to panel to field")
	}
	if lightness(t, dark["term-bg"]) >= lightness(t, dark["bg"]) {
		t.Error("the terminal should stay a shade darker than the page")
	}
}

func TestLightThemeOverridesEveryDarkColour(t *testing.T) {
	dark := block(t, benchHTML, "    :root {")
	light := block(t, benchHTML, `[data-theme="light"] {`)
	shared := map[string]bool{"accent": true, "accent2": true, "radius": true, "sidebar-w": true}
	for k := range dark {
		if _, ok := light[k]; !ok && !shared[k] {
			t.Errorf("--%s is defined for dark but not overridden for light", k)
		}
	}
}

func TestPagesUseTheSharedThemeScript(t *testing.T) {
	for name, page := range map[string]string{"login": loginHTML, "bench": benchHTML, "doc": docHTML} {
		if !strings.Contains(page, `<script src="/theme.js"></script>`) {
			t.Errorf("%s page does not load /theme.js", name)
		}
		if strings.Index(page, `/theme.js`) > strings.Index(page, "<body") {
			t.Errorf("%s page loads the theme script after the body starts (flash of the wrong theme)", name)
		}
		if !strings.Contains(page, `name="color-scheme"`) {
			t.Errorf("%s page lacks the color-scheme meta tag", name)
		}
		if strings.Contains(page, `data-theme="auto"`) {
			t.Errorf(`%s page still starts with data-theme="auto", which is not a valid theme`, name)
		}
		if strings.Contains(page, "localStorage.getItem('dnsbench-theme')") || strings.Contains(page, "function toggleTheme") {
			t.Errorf("%s page still carries its own copy of the theme code", name)
		}
		if !strings.Contains(page, "data-theme-toggle") {
			t.Errorf("%s page has no theme toggle button", name)
		}
	}
	// Dark-only colours must not be hard-coded where the palette should be used.
	for _, bad := range []string{"#7fa8cc", "#3d1a1a", "#7f2e2e", "#161920", "isDark"} {
		if strings.Contains(benchHTML, bad) {
			t.Errorf("bench.html still hard-codes %s", bad)
		}
	}
}
