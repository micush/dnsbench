package main

import (
	_ "embed"
	"strings"
)

// source/VERSION (next to the Go files) is the single source of truth; go:embed
// compiles it into the binary so `ddgw --version` reports the build's release.
//
//go:embed VERSION
var embeddedVersion string

func version() string { return strings.TrimSpace(embeddedVersion) }
