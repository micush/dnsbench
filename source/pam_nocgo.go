//go:build !cgo

package main

// Built without cgo there is no PAM, and the web UI fails closed: nobody can
// log in. Build with CGO_ENABLED=1 and the PAM development headers installed.
func realPAMAuth(service, user, pass string) bool { return false }

const pamAvailable = false
