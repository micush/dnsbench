package main

import "testing"

func TestFilterAndOr(t *testing.T) {
	cases := []struct {
		text string
		name string
		want bool
	}{
		{"ubuntu", "ntp.ubuntu.com", true},
		{"ubuntu AND ntp", "ntp.ubuntu.com", true},
		{"ubuntu AND pool", "ntp.ubuntu.com", false},
		{"pool OR ubuntu", "ntp.ubuntu.com", true},
		{"pool or debian", "ntp.ubuntu.com", false},
		{"pool AND org OR ubuntu AND com", "ntp.ubuntu.com", true},
		{"pool AND com OR ubuntu AND org", "ntp.ubuntu.com", false},
		{"ubuntu AND", "ntp.ubuntu.com", true},
	}
	for _, c := range cases {
		if got := parseMatch(c.text).matches(c.name); got != c.want {
			t.Errorf("%q on %q: %v, want %v", c.text, c.name, got, c.want)
		}
	}
	if parseMatch("   ") != nil || parseMatch("AND OR") != nil {
		t.Error("a text with no word is a filter")
	}
	m := map[string]uint64{"ntp.ubuntu.com": 3, "pool.ntp.org": 2, "ubuntu.com": 1}
	if got := keepMatching(m, "ntp AND ubuntu OR ubuntu.com"); len(got) != 2 {
		t.Errorf("%v", got)
	}
}
