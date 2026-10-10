package main

import (
	"errors"
	"strconv"
	"strings"
)

func errorsIs(err, target error) bool { return errors.Is(err, target) }
func indexByte(s string, c byte) int  { return strings.IndexByte(s, c) }
func lower(s string) string           { return strings.ToLower(s) }
func itoa(i int) string               { return strconv.Itoa(i) }

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// SO_REUSEPORT is missing from package syscall; 15 on amd64/arm64/386/arm/riscv.
const soReusePort = 15
