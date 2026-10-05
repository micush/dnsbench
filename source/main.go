// dnsbench — DNS benchmark with a web UI and REST API.
//
// Login uses PAM, so this program needs cgo and the PAM development headers
// to build; install.sh at the top of the project takes care of that.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"strings"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(0) // journald adds its own timestamps
	cfg, err := loadConfig(os.Args[1:], os.Getenv, os.Stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return
	case errors.Is(err, errVersion):
		fmt.Println("dnsbench", version())
		return
	case err != nil:
		fmt.Fprintln(os.Stderr, "dnsbench:", err)
		// A release that rejects the existing configuration is the commonest way for a new
		// version to fail to start, and it fails here, before run(): count it, or the boot guard
		// would never see it and the service would crash-loop for good.
		guardAtStart(envOr(os.Getenv, "STATE_DIR", "/var/lib/dnsbench"), selfPath(), reexec)
		os.Exit(2)
	}
	guardAtStart(cfg.StateDir, selfPath(), reexec)
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "dnsbench:", err)
		os.Exit(1)
	}
}

// selfPath is the path of the running binary, taken before any update can replace it.
func selfPath() string {
	exe, _ := os.Executable()
	return strings.TrimSuffix(exe, " (deleted)")
}

func reexec(exe string) error { return syscall.Exec(exe, os.Args, os.Environ()) }

// guardAtStart counts this start against an update that has not yet proved itself, and, once
// that update has failed to stay up too many times, restores the previous binary and starts it.
// It runs first thing in main(), ahead of everything that can fail, so that every way of failing
// to start is counted. It reports whether it restored the previous version (the re-exec normally
// does not return).
func guardAtStart(stateDir, exe string, restart func(string) error) bool {
	if !newUpdater(stateDir, false).GuardOnStart() {
		return false
	}
	log.Printf("update: restarting into the restored previous version")
	if err := restart(exe); err != nil {
		fmt.Fprintln(os.Stderr, "dnsbench: cannot start the restored previous version:", err)
		os.Exit(1)
	}
	return true
}

func run(cfg *Config) error {
	log.Printf("dnsbench v%s starting", version())
	if !pamAvailable {
		log.Printf("warning: built without cgo, so PAM is unavailable and nobody can log in")
	}
	if cfg.NoTLS {
		log.Printf("warning: TLS is off; passwords and session tokens cross the network in clear text")
	}

	app := newApp(cfg)

	exe := selfPath()
	app.updater.exePath = func() (string, error) { return exe, nil }
	if n := app.updater.RolledBackNotice(); n != "" {
		log.Printf("update: %s", n)
	}
	restart := make(chan struct{}, 1)
	app.updater.restartFn = func() {
		select {
		case restart <- struct{}{}:
		default:
		}
	}
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-stopped:
		case <-time.After(bootConfirmAfter):
			app.updater.GuardConfirm()
		}
	}()
	if !cfg.AllowUpdates {
		log.Printf("updates from the web UI are switched off (ALLOW_UPDATES=false)")
	}

	app.schedules.load()
	log.Printf("pam: authenticating against service %q", app.pamSvc)
	if _, err := user.LookupGroup(cfg.LoginGroup); err != nil {
		log.Printf("warning: group %q does not exist, so nobody can sign in until it is created (groupadd %s)", cfg.LoginGroup, cfg.LoginGroup)
	} else {
		log.Printf("login: only members of group %q may sign in", cfg.LoginGroup)
	}
	go app.schedulerLoop()
	go app.keepAlive()

	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return listenError(addr, err)
	}
	srv := &http.Server{
		Handler:           app.routes(),
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       2 * time.Minute, // request bodies are small; streams are responses
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          log.New(os.Stderr, "http: ", 0),
	}
	scheme := "http"
	if !cfg.NoTLS {
		scheme = "https"
		if srv.TLSConfig, err = tlsConfig(cfg); err != nil {
			return err
		}
	}

	serveErr := make(chan error, 1)
	go func() {
		if cfg.NoTLS {
			serveErr <- srv.Serve(ln)
		} else {
			serveErr <- srv.ServeTLS(ln, "", "")
		}
	}()
	log.Printf("listening on %s://%s", scheme, addr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	reexec := false
	select {
	case err := <-serveErr:
		return err
	case s := <-sig:
		log.Printf("%v received, shutting down", s)
	case <-restart:
		log.Printf("restarting into the updated binary")
		reexec = true
	}
	app.killAll()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		srv.Close() // streaming clients can hold Shutdown open
	}
	if reexec {
		log.Printf("re-executing %s", exe)
		return syscall.Exec(exe, os.Args, os.Environ())
	}
	return nil
}

// listenError turns a bind failure into advice the operator can act on.
func listenError(addr string, err error) error {
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		return fmt.Errorf("cannot listen on %s: address already in use\n"+
			"  Another process is using this port (5353 is mDNS/avahi, 53 is a DNS server).\n"+
			"  Change LISTEN_PORT in /etc/dnsbench/dnsbench.conf, then: sudo systemctl restart dnsbench", addr)
	case errors.Is(err, syscall.EACCES):
		return fmt.Errorf("cannot listen on %s: permission denied (ports below 1024 need root or CAP_NET_BIND_SERVICE)", addr)
	}
	return fmt.Errorf("cannot listen on %s: %w", addr, err)
}
