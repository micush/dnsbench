package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds the daemon's settings. Each can come from an environment
// variable (the systemd unit loads /etc/dnsbench/dnsbench.conf as the
// environment) and be overridden by a command-line flag.
type Config struct {
	Host          string
	Port          int
	NoTLS         bool
	TLSCert       string
	TLSKey        string
	StateDir      string
	SchedulesFile string
	PAMService    string
	LoginGroup    string
}

func envOr(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

// loadConfig parses args (without the program name). It returns flag.ErrHelp
// after printing usage when -h/--help was given, and errVersion for --version.
var errVersion = errors.New("version requested")

func loadConfig(args []string, getenv func(string) string, out io.Writer) (*Config, error) {
	port := 8453
	if v := strings.TrimSpace(getenv("LISTEN_PORT")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("LISTEN_PORT %q is not a valid port", v)
		}
		port = n
	}
	noTLS := false
	switch strings.ToLower(strings.TrimSpace(getenv("NO_TLS"))) {
	case "1", "true", "yes", "on":
		noTLS = true
	}
	cfg := &Config{
		Host:          envOr(getenv, "LISTEN_HOST", "0.0.0.0"),
		Port:          port,
		NoTLS:         noTLS,
		TLSCert:       envOr(getenv, "TLS_CERT", ""),
		TLSKey:        envOr(getenv, "TLS_KEY", ""),
		StateDir:      envOr(getenv, "STATE_DIR", "/var/lib/dnsbench"),
		SchedulesFile: envOr(getenv, "SCHEDULES_FILE", ""),
		PAMService:    envOr(getenv, "PAM_SERVICE", ""),
		LoginGroup:    envOr(getenv, "LOGIN_GROUP", defaultLoginGroup),
	}

	fs := flag.NewFlagSet("dnsbench", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&cfg.Host, "host", cfg.Host, "address to listen on (LISTEN_HOST)")
	fs.IntVar(&cfg.Port, "port", cfg.Port, "port to listen on (LISTEN_PORT)")
	fs.BoolVar(&cfg.NoTLS, "no-tls", cfg.NoTLS, "serve plain HTTP (NO_TLS)")
	fs.StringVar(&cfg.TLSCert, "tls-cert", cfg.TLSCert, "PEM certificate file (TLS_CERT)")
	fs.StringVar(&cfg.TLSKey, "tls-key", cfg.TLSKey, "PEM private key file (TLS_KEY)")
	fs.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "directory for the generated certificate and schedules (STATE_DIR)")
	fs.StringVar(&cfg.SchedulesFile, "schedules-file", cfg.SchedulesFile, "where schedules are stored (SCHEDULES_FILE)")
	fs.StringVar(&cfg.PAMService, "pam-service", cfg.PAMService, "PAM service name (PAM_SERVICE)")
	fs.StringVar(&cfg.LoginGroup, "login-group", cfg.LoginGroup, "only members of this group may sign in (LOGIN_GROUP)")
	version := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *version {
		return nil, errVersion
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("port %d is not valid", cfg.Port)
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return nil, errors.New("TLS_CERT and TLS_KEY must be set together")
	}
	if !validGroupName(cfg.LoginGroup) {
		return nil, fmt.Errorf("login group %q is not a valid group name", cfg.LoginGroup)
	}
	if cfg.SchedulesFile == "" {
		cfg.SchedulesFile = filepath.Join(cfg.StateDir, "schedules.json")
	}
	return cfg, nil
}
