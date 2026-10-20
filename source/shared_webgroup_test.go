package main

import "testing"

func TestWebGroupIsShared(t *testing.T) {
	dc := newDaemonConfig()
	dc.Groups = nil
	dc.Web.Group = legacyWebGroup
	plain := sharedOf(dc)
	if plain.WebGroup != "" {
		t.Fatalf("the legacy single group must stay out of the document (hash unchanged for a rolling update): %q", plain.WebGroup)
	}
	h0 := plain.hash()
	dc.Web.Group = legacyWebGroup + "," + legacyWebGroup + "-users"
	sh := sharedOf(dc)
	if sh.WebGroup != dc.Web.Group || sh.hash() == h0 {
		t.Fatalf("a changed login group is shared and changes the hash: %+v", sh)
	}
	if err := sh.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// a replica takes the primary's value
	rep := newDaemonConfig()
	rep.Groups = nil
	rep.Web.Group = legacyWebGroup
	changed, err := mergeShared(rep, sh, nil)
	if err != nil || !changed || rep.Web.Group != sh.WebGroup {
		t.Fatalf("merge: changed=%v err=%v group=%q", changed, err, rep.Web.Group)
	}
	if changed, _ = mergeShared(rep, sh, nil); changed {
		t.Fatal("merging the same document again changes nothing")
	}
	// an older primary sends no value: the replica keeps its own
	if _, err := mergeShared(rep, plain, nil); err != nil || rep.Web.Group != sh.WebGroup {
		t.Fatalf("no value from the primary: %v %q", err, rep.Web.Group)
	}
}
