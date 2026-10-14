package main

import (
	_ "embed"
	"os"
	"strings"
)

// The built-in preset list, used when DOMAINS_FILE is missing, unreadable or has no domains.
//
//go:embed domains.txt
var defaultDomains string

// defaultDomainsFile is where the installer puts the editable copy.
const defaultDomainsFile = "/etc/dnsbench/domains.txt"

// parseDomains reads one domain per line. Blank lines and lines starting with # are skipped,
// names are lower-cased, and repeats are dropped (first one wins).
func parseDomains(text string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		d := strings.ToLower(strings.TrimSpace(line))
		if d == "" || strings.HasPrefix(d, "#") || strings.ContainsAny(d, " \t") || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// presetDomains returns the list from the file (read fresh every call, so edits apply at once)
// and "file", or the built-in list and "built-in" when the file cannot be used.
func presetDomains(path string) ([]string, string) {
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if d := parseDomains(string(b)); len(d) > 0 {
				return d, "file"
			}
		}
	}
	return parseDomains(defaultDomains), "built-in"
}
