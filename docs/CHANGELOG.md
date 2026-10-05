# Changelog

## [v3] - 2026-10-05 — The web interface loads nothing from the internet; DoH targets checked (CodeQL alert); sidebar reordered

### Changed
- **The UI is self-contained.** Bootstrap, Bootstrap Icons and Chart.js, which the pages used to fetch from a CDN, are replaced by code written for this project and embedded in the binary: `web/ui.css` (base element styles, the utility classes, buttons, form controls, the sign-in alert, the status badge, the spinner, the schedule dialog, README/License typography, and 34 hand-drawn icons painted as CSS masks) and `web/charts.js` (a canvas bar chart with axes, rounded bars, 20 ms / 50 ms guide lines, error markers and hover tooltips). They are served at `/ui.css` and `/charts.js` without a login, like `/theme.js`. No third-party code was added, so the standard-library-only rule stands.
- The schedule editor is a native `<dialog>`: Esc and a click on the backdrop close it.
- Comparison charts round the value axis up to the next tick, so the tallest bar no longer touches the top.
- Sidebar: Results now sits above Schedules, and ReadMe and License sit at the foot of the list, just above the separator.
- The theme attribute is now `data-theme` (was `data-bs-theme`), and the palette tokens that pages map onto the shared stylesheet are `--body-bg`, `--body-color`, `--secondary-bg`, `--tertiary-bg` and `--border-color` (were `--bs-*`). README and License pages use `doc-table` and `doc-code`; code blocks there follow the page palette and are slightly lighter than before.
- **Server strings are checked before anything is sent** (`checkTarget`): the port must be a number from 1 to 65535 (`dns.example:0053` is now `53`); the host must be an IP address or a name of at most 253 bytes with no space or control character and none of `@ / \ ? # : [ ] % " < >` or a backtick; a DoH path must start with `/` and contain no control character. A malformed target is refused with HTTP 400 (`invalid port`, `invalid server`, `invalid path`) from `/api/start-job` and the schedule endpoints, and a stored schedule with one now ends in the same error instead of a failed lookup. Targets that were valid before are unchanged.
- **DoH connects by address, not by the typed text.** The request URL now names the address resolved at the start of the run (the same one every worker already dialled); the name the user typed is sent as the `Host` header and as the TLS server name, so SNI, certificate checks and virtual hosting are as before (an IP target still sends no SNI). Nothing else about DoH changed.

### Added
- `contrib/icons/gen.py` regenerates the icon block of `ui.css` from the shapes drawn in it (`--sheet` prints a contact sheet).
- `assets_test.go`: no page, style or script refers to another host or a CDN name, every icon the pages use has a rule, `/ui.css` and `/charts.js` are served without login, README/License markup uses the project's own classes, and every utility class the pages rely on is defined.
- `contrib/uitest/ui.js` now needs no CDN files, refuses every request that does not go to the server and fails if the page makes one, and also checks the dialog (Esc, backdrop) and the charts (drawn, hover tooltip, removed on close).

### Fixed
- **CodeQL alert #1, `go/request-forgery` ("Uncontrolled data used in network request", critical, `stream.go:220`).** The DoH worker built its request URL from the host and port the user typed. Reproduced locally with CodeQL 2.27.1 and the `go-code-scanning` suite on the v2 source (one finding, severity 9.1, at that line), and gone after the two changes above (zero findings in Go and JavaScript). That any signed-in user can aim the benchmark at any host is the purpose of the tool and is unchanged (it is why sign-in is limited to a group); what changed is that the target is validated and the request is built from the checked values.
- The UI did not render on a network that cannot reach `cdn.jsdelivr.net` (listed as a known limitation below). The server and the REST API never needed it; the browser pages did.

### Verified
- Full release process: `gofmt -l`, `go vet`, `go build -buildvcs=false`, `go test -race -count=1` (cgo), `CGO_ENABLED=0` vet and `go test -count=1` (the fail-closed path), cross-compile for linux/amd64, arm64, arm, 386, riscv64, ppc64le, s390x, loong64 and mips, mipsle, mips64, mips64le, and the 32-bit (386) test binary run for real; all from the extracted archive as well as the working tree.
- UDP engine before/after on the same machine (reflector alive before and after each run, 10 s, 2 workers, queue 64): original 337k and 313k q/s at 1.51 and 1.64 CPU µs per query, this release 321k and 327k q/s at 1.61 and 1.56 µs, all 100% success; no regression (the UDP path is untouched).
- DoH live against a separate stdlib TLS/HTTP/2 responder, original and this release, 4 s each: POST/HTTP 1.1 by IP, POST/HTTP 1.1 by name, GET/HTTP 2 by name, and a full `https://` URL with POST/HTTP 2; 100% success in all eight runs, throughput within run-to-run noise (9-16k q/s on one shared vCPU), and the responder saw the same Host, SNI, protocol and method for both builds (the typed name for a hostname, no SNI for an IP).
- New tests: malformed and well-formed targets (`TestCheckTarget`), and the Host header and TLS server name for HTTP/1.1 and HTTP/2 (`TestDoHKeepsTypedNameForHostAndSNI`, shown to fail when `req.Host` or `ServerName` is removed).
- gofmt, vet, `go test`, and `ui.js` against the real daemon with PAM login in Chromium, light and dark, with every request outside the server refused (none attempted).
- Before/after screenshots of every screen (login with and without error, benchmark, results, comparison charts, schedules, schedule dialog, README and License in the app and standalone) in both colour schemes; charts also with errors, a run without a result, hover, a live theme change and closing.
- Mutation checks: adding a CDN link or an undefined icon fails the new tests.

### Not verified
- GitHub's own CodeQL run on `main` (its version and configuration may differ from the local 2.27.1 bundle and the `go-code-scanning` suite used here). The alert should close by itself once this release is pushed and scanned.
- The installer and uninstaller: `install.sh`, `uninstall.sh` and the three `contrib/` files they install are byte-identical to v2 (only a new `contrib/icons/` tool and `contrib/uitest/ui.js` changed), so their dry-run, upgrade, rollback and login-group suite was not re-run for this release.
- PAM on any architecture but native amd64 (the cross-compiles use the fail-closed stub); runtime beyond amd64 and 386.
- Firefox and Safari. Chromium only. The new code uses CSS masks, `<dialog>` and `ResizeObserver`, which current versions of both support.
- The icons are drawn for this project, so they are close to, but not pixel-identical with, the ones they replace.

## [v2] - 2026-10-04 — Login group; light and dark themes follow the system; lighter dark theme

### Added
- Sign-in requires membership of `LOGIN_GROUP` (default `dnsbench`; `--login-group`), checked after PAM, primary or supplementary groups, any lookup failure refuses. The installer creates the group and adds the user who ran it (never root automatically); a custom `LOGIN_GROUP` is left alone; `uninstall.sh --purge` removes the group only if the installer created it.
- `web/theme.js`: theme follows the system setting, live; the toggle cycles Auto, Light, Dark.
- Tests: login group, palette contrast and "not too dark", theme script use; browser test checks theme behaviour and measured contrast.

### Changed
- **Existing installs: only the user running the installer is added to the group; add everyone else with `usermod -aG dnsbench NAME`.**
- Dark theme lightened by three shades (4 HSL lightness points each); muted text lifted to stay legible; light-theme text colours darkened for contrast.
- Charts, the stop button, the query list and Bootstrap components now use the palette instead of hard-coded dark colours.

### Fixed
- Light-OS users got a dark app with light widgets (`data-bs-theme="auto"` is not a valid value), a flash of the wrong theme, and no reaction to system changes.

### Verified
- gofmt, vet, `go test -race`, no-cgo tests, cross-compile (10 targets), 32-bit UDP runtime tests.
- Real PAM plus group against the live daemon (member, primary-group member, non-member, expired, locked, group deleted fails closed, recreated recovers without restart).
- Chromium test, both themes, twice. Installer scenarios (group, upgrade from v1, earlier scenarios, Rust upgrade, Go download) passed.

### Not verified
- The archive was not re-extracted and re-tested after packaging, and throughput was not re-measured after these changes (the engine was not touched).
- Real go.dev endpoints, a real systemd start, SELinux, runtime beyond amd64 and 386.

## [v1] - 2026-10-04 — Rewrite in Go; Technitium integration removed; PAM login; built from source at install

The Rust implementation is replaced by a Go one using only the standard library (plus libpam through
cgo). The web UI, the REST API, the output format and the `schedules.json` format are kept, so
existing browsers, scripts and saved schedules carry over.

### Changed
- **Language and distribution.** Go 1.22+, standard library only. The project ships as source;
  `install.sh` installs a C compiler and the PAM headers, installs a checksum-verified Go toolchain
  only when the system has none that is 1.22 or newer, builds, installs and starts the service, and
  rolls back an upgrade that does not come up. The glibc patching hack (`glibc_patch.py`, the shim
  library, the vendored OpenSSL) is gone with Rust.
- **Login is PAM only**, against `/etc/pam.d/dnsbench`. A login needs both authentication and an
  account check, so expired and locked accounts are refused. Failed logins are throttled per address
  (8 in 5 minutes, then HTTP 429). A failed form login now returns HTTP 401 (it returned 200).
- **TLS** uses PEM files (`TLS_CERT`, `TLS_KEY`) with hot reload, or a self-signed certificate
  generated in-process and kept in `STATE_DIR`. It no longer shells out to the `openssl` command.
- **Configuration** keys are `LISTEN_HOST`, `LISTEN_PORT`, `NO_TLS`, `TLS_CERT`, `TLS_KEY`, `STATE_DIR`,
  `SCHEDULES_FILE`, `PAM_SERVICE`. Schedules now live in `/var/lib/dnsbench` (the unit makes `/etc`
  read-only). The installer migrates an old config and copies the old schedules.
- **UDP engine** rewritten: batched `sendmmsg`/`recvmmsg`, transaction IDs that encode the in-flight
  slot (constant-time matching, stale and duplicate replies ignored), a token-bucket rate limiter, and
  a log-linear latency histogram (fixed memory, under 1% error at any run length). The old engine opened
  `/dev/urandom` for every query.
- **Benchmark options that the UI offered now work**: DoH method (POST or GET) and HTTP version
  (1.1 or 2) were previously ignored; PTR queries previously became A queries and now accept a bare IP;
  scheduled runs previously always queried type A and now use their configured type.
- The systemd unit runs with `StateDirectory`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`,
  `NoNewPrivileges` and related hardening, `UMask=0077`, and `LimitNOFILE=65536`.
- State-changing requests are refused when the browser's `Sec-Fetch-Site` says cross-site or same-site, or,
  without that header, when `Origin` does not match the `Host` (defence in depth beside `SameSite=Strict`).
  The referrer policy is `same-origin`. Behind a reverse proxy the original `Host` header must be passed through.
- README rewritten to match the code (the old one claimed defaults of 10 workers and queue 8, but the
  code auto-sizes them, and gave three different default ports).

### Removed
- **Technitium DNS Server integration**: credential proxying, 2FA, cluster node list, cached-domain
  list, the Server Info page, and detection of its certificate.
- **Local-user login** (`LOCAL_USER`, `LOCAL_PASS`) and **`.pfx`** certificates.
- **DoQ.** It was offered in the UI and README but never worked: the Rust worker dispatch ended in
  `_ => {}`, so a DoQ run did nothing. QUIC is not in Go's standard library and this project takes no
  other dependencies, so it is not offered rather than stubbed.

### Fixed
- A duration-mode run against a dead server reported "0 sent, 100% success" in an early version of this
  rewrite because unanswered queries at the end were dropped; every sent query is now counted and an
  unanswered one is an error (tests: closed port and black hole).
- Hourly schedules could compute a next run equal to the current instant when clocks go back (and miss
  the real next hour across a 30-minute DST shift). Found by the new property test; fixed.
- The installer never runs the old binary without a time limit: the Rust binary ignores `--version` and
  starts its web server, which would have hung an upgrade.
- `apt-get update` failing because of an unrelated broken repository no longer aborts the install.

- An early draft of the Origin check combined with `Referrer-Policy: no-referrer` locked every browser out
  of the login form: browsers send `Origin: null` for same-origin POSTs under that policy. The Go tests could
  not see it; the Chromium test did. Fixed by honouring `Sec-Fetch-Site` and using `same-origin`, with a
  regression test (`TestBrowserFetchMetadataIsHonoured`).

### Added
- `dnsbench --version`, `--help`, and flags for every setting.
- `uninstall.sh` (`--purge`, `--yes`): never deletes a config, state or PAM file it did not create or
  that has been customised.
- `contrib/perf/` (a batched UDP reflector and a driver script) and `contrib/uitest/` (a Chromium UI
  test) so future changes can be measured and checked the same way.
- A test suite: wire format, histogram accuracy, all four protocols end to end (loss, duplicates,
  timeouts, refused connections, reconnects, kill, rate limiting, slot reuse), the HTTP layer and auth
  (throttle, cookies, Origin, token header, SSE replay), the scheduler (six time zones over a year, old
  `schedules.json` compatibility), TLS (generation, reuse, hot reload, bad reload), config and Markdown.

### Verified
- `gofmt`, `go vet`, `go test -race -count=1 ./...`; with `CGO_ENABLED=0` vet and tests (PAM refuses
  every login); cross-compile (cgo off) for linux/amd64, arm64, arm, 386, riscv64, ppc64le, s390x,
  loong64 and mips/mips64. The UDP engine tests also pass as a real 32-bit linux/386 binary.
- **Real PAM** (`pam_unix`): right password accepted; wrong password, unknown user, empty password,
  expired account and locked account all refused.
- **Real Chromium** (light and dark, on the final build): login (error and success), protocol lists without DoQ, no Server
  Info page, default domains, auto worker placeholders, DoH options, a benchmark run from the UI with the
  live output, the results list, adding a schedule through the modal, and the ReadMe and License pages;
  no script or resource errors.
- **Performance** (1 vCPU shared with a batched C reflector, so absolute numbers are low): 200 to 206k q/s
  at 2.6 µs CPU per query, flat across 1 to 4 workers, zero errors; the old Rust engine measured 16k q/s
  at 2 workers, 117k at its best (32 workers) and 5.2 to 5.9 µs per query.
- **Installer**: `bash -n`, `shellcheck -S warning` clean; dry runs for ubuntu, debian, linuxmint, fedora
  (stub dnf), rocky, almalinux, centos, arch, manjaro, endeavouros, with alpine and an unknown distribution
  refused; real runs with every path redirected and a shim `systemctl` that starts the installed binary:
  fresh install, same-version reinstall keeping an edited config, upgrade, a failing upgrade rolling back,
  downgrade refusal and `--allow-downgrade`, `--no-start`, uninstall with and without `--purge`, a customised
  PAM file surviving uninstall, an upgrade from the real old Rust binary and its old-format config (no hang,
  config backed up and migrated, schedules copied, old PAM file recognised on uninstall), and the Go
  download against a local mirror (success, wrong checksum, garbage checksum, unreachable mirror, both
  checksum file formats). `systemd-analyze verify` passes on the unit.

### Not verified
- The real go.dev endpoints the installer uses (`/VERSION?m=text` and the `.sha256` files); only a local
  mirror was tested. If they differ, the installer fails safely and tells the user to install Go 1.22+.
- The unit under a real systemd (the sandbox has none): directives were checked statically only.
- The SELinux relabelling step in the installer (`chcon -t bin_t`), and the dnf, yum and pacman installs
  (only their command lines were checked).
- Runtime on any architecture other than amd64 and 386; arm64, arm, riscv64 and the rest are compile-only.
- Throughput on real multi-core hardware and a real network; the 225k q/s target was not reproduced here.
  The CPU-per-query ratio suggests headroom but is not a guarantee.
- UDP over IPv6, and reverse-proxy deployments.
- The UI still loads Bootstrap, Bootstrap Icons and Chart.js from a CDN, so it does not fully render on a
  network that cannot reach it.
