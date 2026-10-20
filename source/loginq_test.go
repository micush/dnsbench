package main

import (
	"reflect"
	"testing"
)

func TestSplitGroups(t *testing.T) {
	for in, want := range map[string][]string{
		"a":            {"a"},
		"a,b":          {"a", "b"},
		" a , b ,a,,c": {"a", "b", "c"},
		"a b":          {"a", "b"},
		"":             nil,
	} {
		if got := splitGroups(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitGroups(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGuiGroupIsTheFirstLocalOne(t *testing.T) {
	m, _ := usersFixture(t, "root:x:0:\nsvc-local:x:1001:alice\n", fxPasswd, "")
	m.webPolicy.Store(&WebConfig{Group: "svc-ad, svc-local"})
	if g := m.guiGroup(); g != "svc-local" {
		t.Fatalf("a directory group first, then the local one: %q", g)
	}
	m.webPolicy.Store(&WebConfig{Group: "nothere,alsonot"})
	if g := m.guiGroup(); g != "nothere" {
		t.Fatalf("none is local: %q", g)
	}
}
