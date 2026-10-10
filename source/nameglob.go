package main

import (
	"fmt"
	"strings"
)

// A policy source name can hold "*" and "?" inside its labels: "*host?.sub*.example.com".  A "*" stands for any run of
// characters inside one label (never a dot), a "?" for exactly one character.  A leading "*." as a whole label keeps its meaning from "*.name": any
// labels in front, or none.  A name without such a pattern is not a glob (an exact name, "*" and "*.name" are
// handled by policyName).
type nameGlob struct {
	any    bool     // leading "*.": any labels, or none, in front of the pattern's
	labels []string // the pattern, one entry per label
	tail   string   // the literal labels at the end ("example.com"), used to find the row through the index
}

// parseGlob reads s as a glob; is is false when s is not one.
func parseGlob(s string) (g *nameGlob, is bool, err error) {
	s = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
	if !strings.ContainsAny(s, "*?") || s == "*" || strings.HasPrefix(s, "*.") && !strings.ContainsAny(s[2:], "*?") {
		return nil, false, nil
	}
	g = &nameGlob{}
	rest := s
	if strings.HasPrefix(rest, "*.") {
		g.any, rest = true, rest[2:]
	}
	if len(rest) > 253 {
		return nil, true, fmt.Errorf("%q is too long", s)
	}
	g.labels = strings.Split(rest, ".")
	for _, l := range g.labels {
		if l == "" || len(l) > 63 {
			return nil, true, fmt.Errorf("%q: every label needs 1 to 63 characters", s)
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '*' || c == '?') {
				return nil, true, fmt.Errorf("%q: %q is not allowed in a name pattern", s, string(c))
			}
		}
	}
	k := len(g.labels)
	for k > 0 && !strings.ContainsAny(g.labels[k-1], "*?") {
		k--
	}
	g.tail = strings.Join(g.labels[k:], ".")
	return g, true, nil
}

// match reports whether the lower case name (no trailing dot) fits the pattern.
func (g *nameGlob) match(name string) bool {
	ls := strings.Split(name, ".")
	n := len(g.labels)
	if g.any {
		if len(ls) < n {
			return false
		}
		ls = ls[len(ls)-n:]
	} else if len(ls) != n {
		return false
	}
	for i, p := range g.labels {
		if !wildMatch(p, ls[i]) {
			return false
		}
	}
	return true
}

// wildMatch matches s against p, where "*" in p is any run of characters and "?" any one character.
func wildMatch(p, s string) bool {
	pi, si, star, mark := 0, 0, -1, 0
	for si < len(s) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, si
			pi++
		case pi < len(p) && (p[pi] == '?' || p[pi] == s[si]):
			pi++
			si++
		case star >= 0:
			mark++
			si, pi = mark, star+1
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
