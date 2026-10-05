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
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "dnsbench:", err)
		os.Exit(1)
	}
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
	select {
	case err := <-serveErr:
		return err
	case s := <-sig:
		log.Printf("%v received, shutting down", s)
	}
	app.killAll()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		srv.Close() // streaming clients can hold Shutdown open
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
