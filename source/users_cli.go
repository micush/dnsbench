package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// parseExpiry reads "YYYY-MM-DD" (or "never") for --expires; it returns Unix
// seconds at noon UTC so the date is the same in every time zone, 0 for never.
func parseExpiry(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "never" || s == "-" {
		return 0, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return 0, errors.New("--expires takes a date as YYYY-MM-DD, or never")
	}
	return t.Add(12 * time.Hour).Unix(), nil
}

// readSecret asks for a password without echoing it; when standard input is
// not a terminal it reads one line instead (so a script can pipe it in).
func readSecret(prompt string) string {
	fd := os.Stdin.Fd()
	var old syscall.Termios
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&old)))
	isTTY := e == 0
	if isTTY {
		fmt.Fprint(os.Stderr, prompt)
		noEcho := old
		noEcho.Lflag &^= syscall.ECHO
		syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&noEcho)))
		defer func() {
			syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&old)))
			fmt.Fprintln(os.Stderr)
		}()
	}
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

func askNewPassword() string {
	p := readSecret("New password: ")
	if p == "" {
		fatalf("a password is required")
	}
	if stdinIsTerminal() && readSecret("Again: ") != p {
		fatalf("the two passwords differ")
	}
	return p
}

func stdinIsTerminal() bool {
	var t syscall.Termios
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return e == 0
}

// runUsers is --users, --user-add, --user-passwd, --user-expiry and --user-del.
func runUsers(sock string, f *cliFlags) {
	switch {
	case *f.userAdd != "":
		exp, err := parseExpiry(*f.expires)
		if err != nil {
			fatal(err)
		}
		pw := askNewPassword()
		var r struct {
			Message string `json:"message"`
			Partial bool   `json:"partial"`
		}
		decode(op(sock, "users.add", map[string]any{"username": *f.userAdd, "password": pw, "expires": exp}), &r)
		report(r.Message, r.Partial)
	case *f.userPasswd != "":
		pw := askNewPassword()
		var r struct {
			Message string `json:"message"`
			Partial bool   `json:"partial"`
		}
		decode(op(sock, "users.password", map[string]any{"username": *f.userPasswd, "password": pw}), &r)
		report(r.Message, r.Partial)
	case *f.userExpiry != "":
		if *f.expires == "" {
			fatalf("--user-expiry NAME needs --expires YYYY-MM-DD, or --expires never")
		}
		exp, err := parseExpiry(*f.expires)
		if err != nil {
			fatal(err)
		}
		var r struct {
			Message string `json:"message"`
			Partial bool   `json:"partial"`
		}
		decode(op(sock, "users.expiry", map[string]any{"username": *f.userExpiry, "expires": exp}), &r)
		report(r.Message, r.Partial)
	case *f.userGrant != "":
		var r struct {
			Message string `json:"message"`
			Partial bool   `json:"partial"`
		}
		decode(op(sock, "users.grant", map[string]any{"username": *f.userGrant}), &r)
		report(r.Message, r.Partial)
	case *f.userRevoke != "":
		askYes("Remove "+*f.userRevoke+" from the group? The account is kept.", *f.yes)
		var r struct {
			Message string `json:"message"`
			Partial bool   `json:"partial"`
		}
		decode(op(sock, "users.revoke", map[string]any{"username": *f.userRevoke}), &r)
		report(r.Message, r.Partial)
	case *f.userDel != "":
		askYes("Delete the account "+*f.userDel+"?", *f.yes)
		var r struct {
			Message string `json:"message"`
			Partial bool   `json:"partial"`
		}
		decode(op(sock, "users.delete", map[string]any{"username": *f.userDel}), &r)
		report(r.Message, r.Partial)
	default:
		var v UsersView
		decode(op(sock, "users.list", nil), &v)
		if len(v.Users) == 0 {
			fmt.Printf("No account is a member of the %s group, so nobody can sign in to the web GUI.\nAdd one with --user-add NAME.\n", v.Group)
			return
		}
		fmt.Printf("Accounts that may sign in to the web GUI (group %s):\n", v.Group)
		for _, u := range v.Users {
			exp := "never expires"
			if u.Expires > 0 {
				exp = "expires " + userDate(u.Expires)
				if u.Expired {
					exp = "EXPIRED " + userDate(u.Expires)
				}
			}
			fmt.Printf("  %-32s %s\n", u.Name, exp)
		}
	}
}

// report prints the outcome; a change that did not reach every node exits non-zero.
func report(msg string, partial bool) {
	if partial {
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(2)
	}
	fmt.Println(msg)
}
