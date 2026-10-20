package main

import (
	"fmt"
	"strings"
)

// A policy source name can hold "*" and "?" inside its labels: "*host?.sub*.example.com".  A "*" stands for any run of
// characters inside one label (never a dot), a "?" for exactly one character.  A leading "*." as a whole label keeps its meaning from "*.name": any
// labels in front, or none.  A name without such a pattern is not a glob (an exact name, "*" and "*.name" are
// handled by policyName).
//
// A label that is exactly "**" stands for any number of whole labels, none included, anywhere in the pattern:
// "**.marc.fr.ssg.**" fits every name that has marc.fr.ssg in it, however many labels come before and after.  At most
// three of them, and the pattern needs a label with no wildcard in it, so a row cannot match everything by accident.
type nameGlob struct {
	multi  bool     // some label is "**"
	any    bool     // leading "*.": any labels, or none, in front of the pattern's
	labels []string // the pattern, one entry per label
	tail   string   // the literal labels at the end ("example.com"), used to find the row through the index
}

const maxGlobStars = 3

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
	stars, literal := 0, false
	for _, l := range g.labels {
		if l == "**" {
			stars++
		} else if !strings.ContainsAny(l, "*?") {
			literal = true
		}
	}
	g.multi = stars > 0
	if g.multi && stars > maxGlobStars {
		return nil, true, fmt.Errorf("%q: at most %d ** labels", s, maxGlobStars)
	}
	if g.multi && !literal {
		return nil, true, fmt.Errorf("%q: a pattern with ** needs at least one label without * or ?, or it would match every name", s)
	}
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
	if g.multi {
		return g.matchMulti(ls)
	}
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

// matchMulti is match for a pattern with "**" labels: a table over (pattern label, name label), so the work is at most
// the product of the two lengths whatever the pattern looks like.
func (g *nameGlob) matchMulti(ls []string) bool {
	pat := g.labels
	if g.any { // the leading "*." is a "**"
		pat = append([]string{"**"}, pat...)
	}
	// ok[i][j]: the first i pattern labels match the first j name labels
	ok := make([][]bool, len(pat)+1)
	for i := range ok {
		ok[i] = make([]bool, len(ls)+1)
	}
	ok[0][0] = true
	for i := 1; i <= len(pat); i++ {
		star := pat[i-1] == "**"
		for j := 0; j <= len(ls); j++ {
			switch {
			case star:
				ok[i][j] = ok[i-1][j] || (j > 0 && ok[i][j-1])
			case j > 0:
				ok[i][j] = ok[i-1][j-1] && wildMatch(pat[i-1], ls[j-1])
			}
		}
	}
	return ok[len(pat)][len(ls)]
}
