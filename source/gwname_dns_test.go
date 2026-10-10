package main

import "testing"

func TestGroupNameForDNSPage(t *testing.T) {
	s := &Supervisor{dc: &DaemonConfig{Groups: []GroupConfig{{GroupID: 2, Name: "Office DNS"}, {GroupID: 3}}}}
	if got := s.groupName(2); got != "Office DNS" {
		t.Fatalf("groupName(2) = %q", got)
	}
	if got := s.groupName(3); got != "" {
		t.Fatalf("an unnamed gateway must give an empty name, got %q", got)
	}
	if got := s.groupName(9); got != "" {
		t.Fatalf("an unknown gateway must give an empty name, got %q", got)
	}
	if got := (&Supervisor{}).groupName(1); got != "" {
		t.Fatalf("no config must give an empty name, got %q", got)
	}
}
