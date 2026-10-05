package main

import "os"

// pamAuth is the login check; tests replace it.
var pamAuth = realPAMAuth

// pamServiceName picks the PAM service to authenticate against: the one
// configured, else dnsbench's own file, else the distribution's default stack.
func pamServiceName(configured string) string {
	if configured != "" {
		return configured
	}
	for _, name := range []string{"dnsbench", "common-auth", "system-auth"} {
		if _, err := os.Stat("/etc/pam.d/" + name); err == nil {
			return name
		}
	}
	return "dnsbench"
}
