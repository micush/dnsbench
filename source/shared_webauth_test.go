package main

import "testing"

func TestWebAuthIsShared(t *testing.T) {
	dc := newDaemonConfig()
	dc.Groups = nil
	plain := sharedOf(dc)
	if plain.WebAuth != nil {
		t.Fatalf("the defaults stay out of the document (hash unchanged for a rolling update): %+v", plain.WebAuth)
	}
	h0 := plain.hash()
	dc.Web.LockoutMinutes = 30
	dc.Web.MinPasswordLength = 14
	sh := sharedOf(dc)
	if sh.WebAuth == nil || sh.WebAuth.LockoutMinutes != 30 || sh.WebAuth.MinPasswordLength != 14 || sh.hash() == h0 {
		t.Fatalf("changed rules are shared: %+v", sh.WebAuth)
	}
	if err := sh.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	rep := newDaemonConfig()
	rep.Groups = nil
	changed, err := mergeShared(rep, sh, nil)
	if err != nil || !changed || rep.Web.LockoutMinutes != 30 || rep.Web.MinPasswordLength != 14 || rep.Web.MaxFailedLogins != 3 {
		t.Fatalf("merge: %v %v %+v", changed, err, rep.Web)
	}
	if changed, _ = mergeShared(rep, sh, nil); changed {
		t.Fatal("merging the same document again changes nothing")
	}
	if _, err := mergeShared(rep, plain, nil); err != nil || rep.Web.LockoutMinutes != 30 {
		t.Fatalf("no value from the primary keeps the node's own: %v %+v", err, rep.Web)
	}
	// 0 and the default length are the same rule
	dc2 := newDaemonConfig()
	dc2.Groups = nil
	dc2.Web.MinPasswordLength = defaultMinPassword
	if sharedOf(dc2).WebAuth != nil {
		t.Fatal("the default length, written out, is still the default")
	}
}
