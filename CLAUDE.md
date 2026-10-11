# Instructions for Claude working on this project

dnsbench is a Go daemon that serves a web UI and a REST API for benchmarking DNS servers (UDP, TCP,
DoT, DoH), with saved schedules. Logins use PAM. It is distributed as source: `install.sh` installs
the build dependencies, compiles it and installs it as a systemd service. This file is a standing
instruction from the project owner so it doesn't need repeating each conversation. If you (Claude)
are asked to change this project, follow the process below without being told to.

**Layout.** The tarball has `README.md`, `LICENSE.txt`, `install.sh`, `uninstall.sh` and this file at
the top, `docs/CHANGELOG.md`, `source/` (the Go module: `go.mod`, every `*.go`, `web/*.html`, and
`VERSION`, which must sit next to the Go files because `server.go` embeds it, plus the shared front-end files the pages load: `web/theme.js` (the one theme script), `web/ui.css` (all base styling and the icons) and `web/charts.js` (the comparison charts)), and `contrib/` (the
systemd unit, the example config, the PAM service, and the `perf/` and `uitest/` tools). The `.git`
directory is part of the archive exactly as received; never modify it. All Go commands below run in
`source/`. The installer expects `source/go.mod`, `source/VERSION`, `contrib/dnsbench.service`,
`contrib/dnsbench.conf` and `contrib/pam.d/dnsbench`.

## Release process — do this for every request that changes the project

1. **Do the requested work** (code, docs, config, whatever was asked).
2. **Build and test before packaging, not after.** From `source/` run:
   - `gofmt -l .` (must print nothing), `go vet ./...`, `go build -buildvcs=false -o /dev/null .`
     (`-buildvcs=false` is needed because `.git` is owned by another user and Go refuses to stamp it)
   - `go test -race -count=1 ./...`
   - `CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test -count=1 ./...` so the fail-closed
     no-PAM path stays healthy
   - the cross-compile list in step 3

   Then do whatever the area-specific checks below require. Don't skip straight to packaging on a
   change that could plausibly have broken something, and run the whole suite again on the final
   tree: a subset run after a late edit is not enough.
3. **Cross-compile every release** (cgo off, so the stub PAM is built): for each of `linux/amd64`,
   `linux/arm64`, `linux/arm`, `linux/386`, `linux/riscv64`, `linux/ppc64le`, `linux/s390x`,
   `linux/loong64` run `CGO_ENABLED=0 GOOS=linux GOARCH=<arch> go build -buildvcs=false -o /dev/null .`
   `linux/mips*` also compiles. dnsbench is Linux-only (`sendmmsg`/`recvmmsg`); don't add other OSes.
   This proves the code compiles, not that PAM works on those targets: only the native amd64 cgo
   build is verified with PAM, and the changelog must say so. Don't "fix" that with a build that serves
   the UI without PAM: the stub must keep failing closed. The raw-syscall layout in `mmsg_*.go` is the
   part that differs per architecture; `GOARCH=386 go test -c` produces a 32-bit test binary that
   runs on an amd64 kernel and exercises it for real.
4. **Bump `VERSION`.** A single plain integer (no `v`, no semver) in `source/VERSION`, embedded in the
   binary (`dnsbench --version` prints `dnsbench N`). Increment by exactly 1. `TestVersionIsPlainInteger`
   fails if it is malformed; fix the file, don't weaken the test.
5. **Append a new entry to the top of `docs/CHANGELOG.md`**, directly below the `# Changelog` header,
   in the style of the existing entries: `## [vN] - YYYY-MM-DD — <short summary>`, then `### Added` /
   `### Changed` / `### Fixed` / `### Removed` as appropriate, plus `### Verified` and
   `### Not verified`. Say what changed, why, and exactly what was run. Never rewrite or renumber
   older entries.
6. **Repackage the whole project into `dnsbench_v<VERSION>.tgz`** with the project directory itself
   (`dnsbench/`) at the top, so it re-extracts to `dnsbench/`. Source only: no built binaries, no
   scratch files, no `.tmp` leftovers, no `__pycache__`; check `tar tzf` before handing it back. Then
   extract the archive somewhere clean and run `go vet` and `go test` from that copy: it must pass
   from the tarball, not just from your working tree.
7. **Hand the `.tgz` back** as the deliverable. Don't just describe the changes; the person expects a
   fresh archive every time, without having to ask.

Do steps 4–5 after every other file change (including this file and the README), so the version and
changelog describe the final tree.

## Area-specific verification

Unit tests are necessary but have missed real bugs here. A live run found each of these, so do the
live check for the area you touched.

- **Touched the UDP engine, stats or output (`bench.go`, `udp_linux.go`, `stream.go`, `mmsg_*.go`,
  `hist.go`)?** The standing performance requirement is that a single run can reach **225k q/s** on
  suitable hardware (the owner measured 225k with the old Rust engine). Measure, don't assume:
  build `contrib/perf/reflect.c`, run it detached, run dnsbench with a PAM test user, and use
  `contrib/perf/drive.py` for the same job before and after your change. Also read utime+stime from
  `/proc/<pid>/stat` around the run and report **CPU µs per query**: it is the number that transfers
  between machines. Reference in the sandbox (1 vCPU shared with the reflector): ~206k q/s at ~2.6 µs
  per query, flat across 1–4 workers; the old Rust engine needed ~5.2–5.9 µs. A regression of more than
  ~15% in µs per query must be called out in the changelog. Pitfalls that cost hours:
  - Check the reflector is alive before every run (`pgrep -x reflect`). A dead responder looks exactly
    like a slow or broken engine, and produces convincing 100%-error results.
  - Start long-lived processes with `setsid nohup … </dev/null >log 2>&1 &` and a pid file. A plain `&`
    keeps the tool's output pipe open and hangs the call until it times out.
  - Never `pkill -f <pattern>` when the pattern can appear in your own command line; it kills your
    shell. Kill by pid file.
  - Reporting invariant: every query sent is counted, and one that is never answered counts as an
    error. A dead server must never print "100% success" (there are tests; keep them).
- **Touched the web layer, sessions or login (`server.go`, `auth.go`, `pam*.go`, `config.go`)?**
  `server_test.go` must cover what you changed. Never weaken an auth, throttle, Origin or cookie test
  to make it pass. Then do a **real PAM check** (needs `libpam0g-dev`; `apt-get install` works in the
  sandbox): create a user with `useradd`/`chpasswd`, write `contrib/pam.d/dnsbench` to
  `/etc/pam.d/dnsbench`, run the daemon with `--no-tls --host 127.0.0.1 --port <p> --state-dir <scratch>`,
  and POST to `/api/login`. Login needs PAM **and** membership of the login group (`dnsbench`; create it
  with `groupadd --system dnsbench`). Expect: a member with the right password gets 200, **including a
  user whose only membership is their primary group** (`useradd -g dnsbench`); the right password for a
  non-member gets 401; a wrong password, an unknown user, an empty password, an **expired** member
  (`chage -E 0`) and a **locked** member (`passwd -l`) all get 401; with the **group deleted** nobody gets
  in (fail closed, and the log says why), and recreating it lets members in again **without a restart**.
  The log may name users but never passwords. Use a fresh daemon between sections, because eight failures
  from one address trips the throttle. Remove the test users, the group and the PAM file afterwards. **Anything touching headers, cookies, the Origin check, login or
  CSRF must also pass the Chromium test (next item), not just the Go tests**: a change here once made the
  browser's own login form return 403 (`Origin: null` under `Referrer-Policy: no-referrer`) while every Go
  test passed.
- **Touched the UI (`web/*.html`)?** `node --check` every inline `<script>` (replace
  `__SERVER_CPU_THREADS__` first), grep for dangling references to anything you removed, and run
  `contrib/uitest/ui.js` in real Chromium (light and dark). Its header says how: Playwright and
  Chromium are installed in the sandbox, and nothing else is needed, because the UI loads nothing from
  other hosts (the test refuses every such request and fails if one is made). A syntax check alone
  missed nothing here, but a dangling reference to a removed element would only show at runtime. Look at a screenshot when layout changed.
  **Themes:** the pages follow the system light/dark setting by default (an explicit choice is kept in
  `localStorage` `dnsbench-theme`); `web/theme.js` is the only theme code, loaded from `<head>` so the
  theme is set before the first paint, and it always sets `data-theme` to `light` or `dark`
  (never `auto`, which matches no rule and once left light-OS users with a dark page and light
  widgets). All colours come from the palette tokens in `bench.html` (`:root` is dark, the
  `[data-theme="light"]` block overrides it); the light block must override every colour the dark one
  defines, charts read the tokens at draw time, and no theme-specific colour may be hard-coded in markup
  or script. `theme_test.go` reads the palette out of the page and enforces contrast ratios and that the
  dark theme is not too dark; `ui.js` additionally checks the rendered page (nothing half-dark or
  half-light, live following of an OS change, the Auto/Light/Dark toggle, measured text contrast). When
  you change colours, run both and look at screenshots under **both** emulated colour schemes. "A shade"
  of the dark theme means 4 points of HSL lightness; the dark theme was lifted by three (12 points), and
  the muted text colours were lifted further so labels stay legible. Measure contrast with CSS transitions
  switched off, because the controls fade over 0.12 s and a reading taken right after a theme change
  lands mid-fade.
  Front-end rules: build DOM text with `esc()` or `textContent`, never raw `innerHTML` with
  server-supplied or user-supplied strings; keep `/api` paths and the `§tag§` output markers unchanged
  (the UI parses `q/s`, `sent`, `errors`, `p95` from the output text; `TestOutputFormatMatchesWhatTheUIParses`
  guards it).
- **Touched the scheduler (`scheduler.go`)?** `TestNextRunIsAlwaysStrictlyAfterNow` runs every schedule
  type through six time zones for a year, including DST changes and Lord Howe's 30-minute shift; it
  found a real double-fire bug. Keep it passing. `schedules.json` keys are a compatibility contract with
  earlier releases (`TestScheduleFileCompatibleWithEarlierReleases`): never rename or drop one.
- **Touched TLS (`tlsutil.go`)?** The generated certificate, its key mode (0600), reuse across restarts
  and hot reload are tested; keep them.
- **Touched `install.sh`, `uninstall.sh` or anything in `contrib/`?** They run as root on other people's
  machines. Required: `bash -n` and `shellcheck -x -S warning` clean; `--dry-run` for each family using
  `DNSBENCH_OS_RELEASE` with fake os-release files (ubuntu, debian, linuxmint, fedora, rocky, almalinux,
  centos, arch, manjaro, endeavouros; alpine and an unknown ID must be refused) plus
  `DNSBENCH_ASSUME_MISSING=gcc,pam,go,curl,tar` (the Fedora family needs a stub `dnf` in `PATH` on a
  Debian sandbox); then real runs in the sandbox with every path redirected by the `DNSBENCH_*_DIR`
  variables, a shim `systemctl` (`DNSBENCH_SYSTEMCTL`, `DNSBENCH_FORCE_SYSTEMD=1`, `DNSBENCH_SETTLE=0`)
  that really starts and stops the installed binary, and assertions for: fresh install; same-version
  reinstall keeping a customised config; upgrade; **a failing upgrade rolling back** (build a copy whose
  `run()` exits immediately); downgrade refusal and `--allow-downgrade`; `--no-start`; uninstall with and
  without `--purge`; a customised PAM file surviving uninstall; **upgrade from the old Rust binary and
  its old-format config** (the old binary ignores `--version` and starts its web server, so the installer
  must only ever run it under `timeout`); the **login group**, with real `groupadd`/`usermod` in the
  sandbox (a fresh install creates a system group and adds `DNSBENCH_INSTALL_USER`, and a marker records
  that the installer made it; re-running duplicates nothing; no determinable user warns loudly, still
  creates the group and never adds root; `SUDO_USER` is used and `SUDO_USER=root` is ignored; an unknown
  user is refused politely; a custom `LOGIN_GROUP` in the config is left alone entirely; upgrading from the
  previous package adds the user and keeps the old config and PAM file; `uninstall` keeps the group,
  `--purge` removes it only if the marker says the installer created it, and a group that pre-existed
  survives `--purge`), with sign-in checked against the running daemon after each; and the Go download
  path against a local `python3 -m http.server`
  mirror (`DNSBENCH_GO_BASE_URL`) including a wrong checksum, a garbage checksum, and an unreachable
  mirror, each of which must refuse and install nothing. `systemd-analyze verify` the unit. Never let the
  uninstaller delete config, state, a group or a PAM file it didn't create or that the admin changed. Undo
  every sandbox change afterwards (test users and groups, `/etc/pam.d/dnsbench`, `/opt/dnsbench`, temp dirs).
- **Touched the README or API?** Update both together, and keep `README.md` within the Markdown subset
  `docs.go` renders (headings, fenced code, flat lists, tables, `**bold**`, `` `code` ``, links); the
  in-app ReadMe page is that file, and `TestShippedReadmeRenders` checks it.

- **Touched code that sends network requests or reads user input (`stream.go`, `bench.go`, `server.go`,
  `docs.go`, the pages)?** Run CodeQL locally, because GitHub runs it on `main` and an alert there is a
  release blocker. The bundle downloads from `github.com` (about 700 MB; `curl -sL -o b.tgz
  https://github.com/github/codeql-action/releases/latest/download/codeql-bundle-linux64.tar.gz`, unpack
  it, `codeql/codeql` is the CLI). With `GOFLAGS=-buildvcs=false CGO_ENABLED=1` run
  `codeql database create DB --language=go --source-root=source --command="go build -o /dev/null ."`
  then `codeql database analyze DB codeql/go-queries:codeql-suites/go-code-scanning.qls
  --format=sarif-latest --output=out.sarif` (a few minutes; run it detached and poll, and not while
  measuring performance, it takes the only CPU). The expected result is zero findings. For the history:
  `go/request-forgery` (critical) fired on the DoH `client.Do`. The tool is meant to be aimed at any
  host by a signed-in user (see Notes), so the capability stays; what was fixed is how the target is
  handled: `checkTarget` returns the port as a number and `parseParams` stores `strconv.Itoa` of it (it
  must stay a **pure function with the result assigned afterwards**: an overwrite of `p.port` through a
  pointer does not clear the taint, which was tried and still alerted), the DoH request URL names the
  resolved address (`c.hostport`), and the name the user typed travels only as `req.Host` and
  `tls.Config.ServerName`. `TestDoHKeepsTypedNameForHostAndSNI` fails if either is dropped. Do not
  "fix" a future alert by weakening the target checks or by hiding the flow; reshape the code so the
  value really is the checked one, and re-run CodeQL.
- **Touched DoH?** Besides the unit tests, run a stdlib responder in its own process
  (`httptest.NewUnstartedServer`, replace its `Listener` with one on a fixed port, `EnableHTTP2 = true`,
  `StartTLS()`, answer with the query bytes and the QR bit set) and drive both an IP and a hostname
  (`localhost:PORT`) through `contrib/perf/drive.py` with `{"protocol":"doh","doh_method":"post"|"get",
  "doh_protocol":"2"}` on the old and new build; have the responder print each distinct
  (protocol, method, Host, SNI) it sees and compare. A dead responder looks like a broken engine here
  too: check it is alive first.

## Sandbox notes

- The tool runs commands with `sh`, not `bash`: put anything using arrays, `${PIPESTATUS}`, `[[`,
  or `local` into a script file and run it with `bash file`.
- `go.dev` is unreachable, `apt` works (`golang-go`, `libpam0g-dev`, `shellcheck`), and `npm pack`
  works. `apt-get update` exits non-zero because an unrelated repo is broken; ignore it.
- `useradd`, `groupadd`, `chage` and `passwd -l` work (the sandbox runs as root), so login and group
  behaviour can be tested for real; curl against the TLS service needs `-k`.
- There is no real systemd, no `ss`, no IPv6, and one CPU: absolute throughput numbers are lower
  than real hardware, so compare before/after on the same machine.

## Notes

- Front end: **no third-party code and no CDN**, same rule as the Go side. Styling is `web/ui.css`
  (tokens at the top, `bench.html` maps them onto its palette), icons are `<i class="bi bi-NAME">` and
  are drawn in `contrib/icons/gen.py`, which rewrites the marked block of `ui.css`: add a shape there,
  run it, never hand-edit the block. Charts are `web/charts.js`; the schedule editor is a native
  `<dialog>` (`showModal()`). Class names like `btn`, `d-flex` or `mb-3` are just this project's own
  rules now, and `assets_test.go` fails if a page uses a class or icon that `ui.css` does not define,
  or refers to another host. Do not add a `<link>` or `<script>` to anything outside the daemon.
- Dependencies: **stdlib only** by design. The one non-Go dependency is libpam through cgo. Don't add a
  module for something small; if one is truly warranted, say so in the changelog. This is why DoQ is
  not supported (QUIC is not in the standard library); don't re-add it as a stub.
- Login = PAM success **and** `pam_acct_mgmt` success **and** membership of `LOGIN_GROUP` (default
  `dnsbench`, primary or supplementary, resolved through NSS so directory groups work). The group is
  checked only after PAM succeeds, so a wrong password never reveals membership, and any error looking
  the group up (including the group not existing) refuses the login. The group can never be switched
  off. The installer creates the default group, adds the person who ran it (never root automatically)
  and leaves a custom `LOGIN_GROUP` alone. One generic error for every login failure, with the real
  reason only in the log. Failed
  logins are throttled per address. Sessions are random tokens in `HttpOnly`, `SameSite=Strict`
  cookies (`Secure` when TLS is on). State-changing requests with a foreign `Origin` are refused. The
  daemon fails closed without cgo. Don't regress any of these without saying so in the changelog.
- Anyone who can sign in can make the host send DNS traffic anywhere. That is why sign-in is limited to
  a group; keep the README's advice to bind a management address.
- One benchmark runs at a time: starting a job stops the running one. Finished jobs are kept for a
  short while (`keepJobs`); schedules and the run history are separate.
- The default port is 8453 (5353 is mDNS and clashes with avahi). Config comes from environment
  variables (the systemd unit loads `/etc/dnsbench/dnsbench.conf`) and may be overridden by flags.
- Keep the changelog honest about what wasn't verified. Things the sandbox cannot exercise: a real
  go.dev download, SELinux, a real systemd start, non-amd64 runtime beyond 386, and hardware faster than
  one shared vCPU. Say "not verified" rather than implying otherwise.
- The project is named `dnsbench` (the UI shows `dns[bench]`). Don't rename the cookie
  (`dnsbench_session`), the `localStorage` keys (`dnsbench-*`) or the JSON keys of `schedules.json`:
  existing browsers and installs depend on them.
- The process applies to substantive changes (features, fixes, docs, refactors). Pure Q&A that changes
  nothing on disk needs no version bump. A pure packaging fix that changes nothing in the project's
  code, tests or behaviour doesn't need a bump: note it under the current version's changelog entry in
  a short `### Packaging fix` subsection and keep the archive name matching. If a request splits into
  several changelog-worthy pieces in one sitting, one bump, one entry and one `.tgz` is fine.
- If you are a future Claude, in any session type, asked to work on this project and this file's
  process hasn't been mentioned, read this file before starting anyway.
