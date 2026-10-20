//go:build !(linux && cgo)

package main

import "errors"

// Built without cgo (e.g. cross-compiled): there is no PAM backend, so the web
// GUI refuses to start.  It fails closed — it never falls back to "no auth".
const pamAvailable = false

func pamAuthenticate(service, user, password string) error {
	return errors.New("this build has no PAM support (needs a native cgo build with libpam0g-dev)")
}
