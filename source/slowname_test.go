package main

import "testing"

func TestServerThatTimedOutOnANameIsTriedLastForIt(t *testing.T) {
	a, b, c := &Server{Addr: "10.0.0.1:53"}, &Server{Addr: "10.0.0.2:53"}, &Server{Addr: "10.0.0.3:53"}
	var av nameAvoid
	r := []*Server{a, b, c}
	if got := av.order(r, "x.example", 1); got[0] != a {
		t.Fatal("nothing marked, order changed")
	}
	av.mark(a.Addr, "X.Example", 1)
	got := av.order(r, "x.example", 1)
	if got[0] != b || got[1] != c || got[2] != a {
		t.Fatalf("%v", got)
	}
	if got := av.order(r, "x.example", 28); got[0] != a {
		t.Fatal("another type was affected")
	}
	if got := av.order(r, "other.example", 1); got[0] != a {
		t.Fatal("another name was affected")
	}
	if r[0] != a {
		t.Fatal("the input slice was changed")
	}
	if got := av.order([]*Server{a}, "x.example", 1); len(got) != 1 || got[0] != a {
		t.Fatal("a lone server must stay")
	}
}
