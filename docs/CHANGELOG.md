# Changelog

## [v8] - 2026-10-05 — Update dnsbench from the web UI

### Added
- **An Updates page** (sidebar, between Schedules and ReadMe), after the updater in ddgw but for a single server: upload a release archive (`dnsbench_vN.tgz` or `.zip`), then press **Update to vN**. The page shows the running and staged versions, anything that blocks an update (no Go toolchain of the version the release asks for, no C compiler, the binary's directory not writable, updates switched off), the reason the last attempt failed with the compiler's output, and a history of uploads, installs, failures and rollbacks with who did each. It polls every 2 s while open, and when the server restarts it waits for it to answer and opens the sign-in page by itself (sessions are in memory and end with the restart).
  - **Archive checks** before anything is staged: `.tgz` or `.zip`, at most 32 MB uploaded and 64 MB / 2000 files / 16 MB per file unpacked; links, devices, absolute paths, `..` and backslashes refused; no duplicate names; one common top directory is stripped and `.git/`, a stray `dnsbench` binary and `*.tmp` dropped; it must be a dnsbench tree (`source/go.mod` with `module dnsbench`, `source/main.go`, a plain-integer `source/VERSION`). An archive that is not newer than the running version is refused with the pointer to `install.sh --allow-downgrade`; going back is not done from the page.
  - **Build** on the host with the settings `install.sh` uses (`CGO_ENABLED=1`, `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly`, `GOPROXY=off`, `-trimpath -buildvcs=false -ldflags='-s -w'`), scratch space under `STATE_DIR/update`. The Go version needed is read from the staged `go.mod` (never below 1.22, what `install.sh` needs), so a release that needs a newer Go says so instead of failing in the compiler. The result must report `dnsbench N`, be linked against PAM when the running one is, and have a plausible size.
  - **A smoke test, which ddgw does not have:** the new binary is started once on a spare loopback port with a scratch state directory and an empty environment, and its login page must answer 200, before the running binary is touched. A build that compiles but cannot start is found there, with the program's own output, rather than after a restart.
  - **Install:** the running binary is kept as `STATE_DIR/update/dnsbench.prev`, the new one is written next to the old and renamed over it, `README.md` and `LICENSE.txt` next to it are refreshed (the in-app ReadMe and License read them from there, as `install.sh` arranges), the SELinux label is set when SELinux is enforcing (as `install.sh` does), and the process re-execs itself in place.
  - **Boot guard:** if the installed version does not stay up, the previous binary is restored on the fourth start (three starts that do not survive 60 s are allowed) and the page says so. An update that stays up for 60 s is recorded as applied. A different binary being run than the guard was armed for (for example `install.sh` put one in place) disarms it.
  - **A running benchmark is not stopped without asking:** the page answers `409` with `needs_confirm` and asks "Update anyway?", and an update that is already installed waits for a running benchmark to finish before it restarts, showing why.
- **REST API:** `GET /api/update`, `POST /api/update/upload` (body is the archive), `POST /api/update/apply` (optional `{"force": true}`); all need a session, and the two writes are subject to the same-origin check. Documented in the README, with a new section "Updating from the web UI".
- **`ALLOW_UPDATES`** (default `true`; `--allow-updates`) switches the two writes off with `403`. It is parsed strictly: anything but true/false (`1 yes on` / `0 no off` also work) stops the daemon from starting, because a typo in a switch that decides whether signed-in users may run code as root must not leave it on by accident.
- **`ReadWritePaths=-/opt/dnsbench` in `contrib/dnsbench.service`.** The unit uses `ProtectSystem=strict`, which made `/opt` read-only, so the daemon could not have replaced its own binary. Nothing else became writable. Installs made before this release have the old unit: they need one `install.sh` run from this release, and until then the page says exactly that (it checks the directory is writable before building anything, not after). `ALLOW_UPDATES=true` is in the example config. The README's "can write only to its state directory" sentence is updated to match.
- **`contrib/uitest/update.js`**, an end-to-end test of the whole thing in Chromium with a real restart (upload, Update, the page riding out the restart, signing in again, the new version running, the docs refreshed, the update confirmed). It needs the daemon under something that restarts it, as systemd does.
- The `upload` icon for the Updates page, drawn in `contrib/icons/gen.py`; `ui.css` regenerated (38 icons).

### Security
- **Anyone who can sign in can now make the host build and run code as root** by uploading it. That is inherent in updating from the page, so the README says it plainly and `ALLOW_UPDATES=false` turns it off. Every upload and update is logged with the user's name (quoted) and address.
- The unit now lets the service write `/opt/dnsbench`. The test `TestShippedUnitLetsTheServiceReplaceItsOwnBinary` fails if that is widened to any other path.

### Fixed
- **The boot guard could not see a start that failed early.** Found by the end-to-end rollback test, before release: a release that crashed straight after starting was never rolled back, because the guard counted starts inside `run()` and the crash came first. The service crash-looped with `attempts: 0` in the guard file. The same applied to a release that rejects the existing configuration (exit code 2 in `main()`), which is the commonest way for a new version to fail to start. The guard now counts at the very top of `main()` (`guardAtStart`), including on the config-error path (using `STATE_DIR` from the environment when the configuration cannot be read, which is how the service is configured). `TestGuardAtStartCountsEveryStartAndRestoresOnTheFourth` and `TestMainCountsStartsBeforeAnythingThatCanFail` pin it down. ddgw's updater, which this follows, has the same structure.

### Verified
- Full release process on the final tree: `gofmt -l` (nothing), `go vet`, `go build -buildvcs=false`, `go test -race -count=1` three times in a row, `CGO_ENABLED=0` vet and `go test -count=1` (the fail-closed path), cross-compile for linux/amd64, arm64, arm, 386, riscv64, ppc64le, s390x, loong64 and mips, mipsle, mips64, mips64le, and the 32-bit (386) test binary run for real. The race run now takes about 32 s because the real-toolchain tests compile small programs.
- **The update, for real:** a daemon built from this tree with `VERSION` 7, started through the environment like the service (`STATE_DIR`, `LISTEN_*`, `NO_TLS`) under a restart loop standing in for systemd, was updated to the final v8 through the Updates page in Chromium (`update.js`): upload, build (29 s), smoke test, install, in-place restart (no supervisor restart needed), the page signed itself back in, the new version ran, the ReadMe was refreshed, and the update was confirmed as applied after a minute. Then `ui.js` ran all 250 checks in light and dark against that daemon, which is the binary the updater built.
- **The failure paths, for real** (through the HTTP API against the same setup): a release that does not compile is reported with the compiler's message and nothing changes; one that compiles but crashes at start is caught by the smoke test with the program's own message and nothing changes (no guard is written); one that passes the smoke test but crashes on the real state directory failed exactly three starts and was rolled back to v8 on the fourth, in 7 s, and the restored daemon served requests; one that rejects the configuration before `run()` was rolled back the same way. Before the guard fix the third of these crash-looped without ever rolling back.
- **CodeQL**, run locally as `CLAUDE.md` describes (`go/codeql-suites/go-code-scanning.qls`, 34 rules including `go/zipslip`, `go/path-injection` and `go/command-injection`): zero findings, with `update.go` and `update_web.go` in the analysed database.
- **Mutation checks:** eleven deliberate breakages of the safety checks (traversal check removed, links accepted, old versions accepted at upload, writability not checked before the build, no confirmation for a running benchmark, restart not waiting for it, a typo in `ALLOW_UPDATES` accepted, upload ignoring the off switch, and the guard not counting, counting late, or not counting on the config-error path) were each caught by a test; the sources were restored afterwards.
- **Installer**, `install.sh` and `uninstall.sh` byte-identical to v3: `bash -n` clean; a real run with every path redirected, `DNSBENCH_FORCE_SYSTEMD=1` and a shim `systemctl` (`--no-start`) installed binary version 8, the docs, the unit (byte-identical to `contrib/dnsbench.service`, with the `ReadWritePaths` line) and a config containing `ALLOW_UPDATES=true`; `uninstall.sh --yes` removed them and exited 0. `systemd-analyze verify` passes on the new unit and on v3's, and the unit's only difference from v3 is the three added lines.
- Screenshots of the Updates page in light and dark.

### Not verified
- **That the shipped unit really lets the service replace its binary.** The sandbox has no real systemd, so `ProtectSystem=strict` with `ReadWritePaths=-/opt/dnsbench` was not exercised; it rests on systemd's documented behaviour (`ReadWritePaths=` makes the path writable again, a leading `-` ignores a missing one). The update itself was tested with a plain directory that was writable. The first real update on a host is the real test, and the page checks writability before building anything and says what to do if it fails.
- SELinux labelling (`chcon`) was not run: there is no SELinux here.
- `shellcheck -x -S warning` reports two `SC2034` warnings (`LEGACY`, `added_user`) in `install.sh`. They are in the unchanged v3 script and are not touched here.
- The full installer matrix of `CLAUDE.md` (every distribution family in `--dry-run`, group scenarios, the rollback of a failing start, the PAM matrix of expired and locked accounts) was not re-run: neither script changed, and the unit and config files they install were checked as above. The browser test signs in through real PAM, and a member and a non-member are still told apart there.
- The 225k q/s performance check was not re-run (the benchmark engine is unchanged).
- Firefox and Safari. Chromium only.
- A real hand-over to a real systemd restart (`Restart=on-failure`): the restart loop used here restarts on every exit, as `Restart=always` would, and the rollback test relies on it. The in-place re-exec needs none.
- Updating a TLS-enabled daemon: the update and sign-in were exercised with `NO_TLS=true`; the update code does not depend on it, but the browser flow was not run over HTTPS.

## [v7] - 2026-10-05 — The comparison follows your ticks

### Changed
- **Once the comparison is open it updates itself.** Ticking a third or fourth result adds it to both pies and the legend, unticking removes it, and nobody has to press Compare again. Compare is only needed to open the panel the first time, so it steps aside while the panel is open.
  - **Below two results** the panel hides itself rather than showing a one-slice "comparison", and it comes back on its own when you tick up to two again. The X closes it for good (and clears the ticks, as before); after that, ticking two results offers Compare again instead of reopening it.
  - **A selected run whose numbers arrive later** (it was still running when ticked) is picked up the next time the list is drawn. The panel redraws only when the ticked runs or their numbers actually change, so an unrelated redraw of the list does not make the charts flicker.
- **Each run keeps its colour while it stays ticked.** Colours used to be spread evenly over however many runs were ticked, so adding a run would have recoloured the others in a live view. Now a run holds a palette slot while ticked: the first ten are fixed (blue and orange first, as before), then golden-angle hues. An unticked run frees its slot and a newly ticked one takes the lowest free one.
- The hint under Results says the comparison follows the selection. README bullet updated to match.

### Fixed
- **The race-detector run of `go test` failed intermittently, and did so on v3 as well.** Run against the unmodified v3 archive, `go test -race` failed in 6 of 12 runs (`TestNextRunDailyHourly`, `TestParseLocalTime`, `TestNextRunIsAlwaysStrictlyAfterNow`), so the clean race run reported for v3 was luck. The failures were a data race in the tests, not in the daemon: the time-zone tests replace the global `time.Local`, and a timer left over from an earlier test fired during them and read it (`time.Now()` inside `time.sendTime`).
  - Three test helpers waited with `select { case <-job.done: case <-time.After(N): }`. When the job finished first, the `time.After` timer was never stopped, so it kept running for up to 30 s and fired inside the later scheduler tests. They now use a `time.Timer` that is stopped on return (`runJobArgs` and the kill test in `bench_test.go`, the stop test in `server_test.go`).
  - `TestTLSConfigGeneratesReusesAndServes` started an accepting goroutine that could still be closing its TLS connection (which also reads `time.Local`) after the test returned. The test now waits for that goroutine.
  - No test was weakened or removed; the assertions are unchanged.

### Verified
- Full release process on the final tree: `gofmt -l` (nothing), `go vet`, `go build -buildvcs=false`, `go test -race -count=1` (cgo), `CGO_ENABLED=0` vet and `go test -count=1` (the fail-closed path), cross-compile for linux/amd64, arm64, arm, 386, riscv64, ppc64le, s390x, loong64 and mips, mipsle, mips64, mips64le, and the 32-bit (386) test binary run for real; all from the extracted archive as well as the working tree.
- `ui.js` against the real daemon (native amd64 cgo build, real PAM login with a throwaway user, UDP reflector alive before the run, fresh state directory) in Chromium, light and dark, every request outside the server refused (none attempted): all checks pass in both schemes. 14 new checks per scheme drive real clicks on the result checkboxes: no button or panel with one ticked, Compare appearing at two and opening the panel, the third and fourth ticks adding runs with no click, four distinct colours and a painted pie, an untick removing one while the others keep their colours, a re-ticked run taking the freed colour, the panel hiding below two and returning at two, the X closing it and clearing the ticks, and Compare (not an automatic panel) being offered again afterwards.
- The race suite after the test fixes: `go test -race -count=1 ./...` passed 40 runs out of 40 in a row (15, then 25), against 6 failures in 12 on the unmodified v3 archive and 5 in 12 on this tree before the fixes.
- Mutation check: with the redraw taken out of the list drawing, six of the new checks fail, including the ones that stay stuck on the original two runs; restored afterwards.
- Screenshots of a two-run comparison and the same panel after ticking a third and fourth, including a run with errors.

### Not verified
- Firefox and Safari. Chromium only.
- `startJob` in `server.go` has the same pattern as the test helpers (`case <-time.After(3 * time.Second)` while waiting for the job it just killed), so a 3 s timer can outlive the wait. It is harmless in the daemon and is not changed here, because `server.go` is in the area that calls for the full web-layer and PAM checks and nothing in this release touches it. In the tests it is the one remaining place a stray timer could fire late, and the race runs below did not hit it.
- Nothing in the daemon's Go code, `install.sh`, `uninstall.sh` or the other files in `contrib/` changed (the Go changes are test files only, plus `contrib/uitest/ui.js`), so the 225k q/s performance check, the installer suite, the full PAM matrix and CodeQL were not re-run. The browser test did sign in through real PAM.

## [v6] - 2026-10-05 — The best slice stands out on the comparison pies; Schedules actions move to a right-click menu

### Added
- **The winning slice is featured on each comparison pie.** On the throughput pie it is the run with the highest q/s; on the p95 pie it is the run with the *lowest* latency, so the smaller slice is the one pushed out there. A featured slice is shifted outward, drawn larger, outlined, given a glow and drawn on top, and carries a "BEST" label when it is wide enough to hold one; the others are slightly dimmed. The hover tooltip says "Highest throughput" or "Lowest p95". Nothing is featured when fewer than two runs have a value or when every run ties, because there is nothing to single out. The chart option is `winners: [index, ...]` on `DnsCharts.pie`; hover and hit-testing follow the shifted, larger shape.
- **Right-click menu on schedules** with Edit, Duplicate, Run now, Pause (Resume when paused) and Delete.
  - **Duplicate** opens the editor pre-filled from the schedule with no id and `(copy)` added to the name, so Save creates a new schedule and the original is untouched. It is a new item; the server API is unchanged (it uses `add`).
  - The menu follows the keyboard: rows are focusable, the Menu key or Shift+F10 opens it at the row, arrows/Home/End move, Escape closes it and returns focus to the row. It also closes on a click elsewhere, a scroll, a resize, a window blur and when you leave the page. It is clamped to the screen, and it uses the page palette tokens, so it follows light and dark.
  - Ticking several schedules still works: right-click one of the ticked rows and Pause, Resume and Delete act on all of them ("Delete 2 schedules"). Edit, Duplicate and Run now are greyed out for more than one.
  - The `copy` icon was drawn in `contrib/icons/gen.py` (37 icons).
- `ui.js` covers all of this: the featured slice's geometry for a small and a large winner (and no difference with no winner), which run is featured when throughput and p95 disagree, a tie featuring nothing, the menu items and order, Edit/Duplicate/Run now/Pause/Resume/Delete end to end, the multi-select menu, the keyboard path, closing, and that the old button bar is gone.

### Changed
- **The Schedules button bar is gone** (Run Now, Edit, Pause, Delete). A one-line hint under the heading says where the actions are. Add Schedule stays as a button.
- **Run now is for one schedule at a time.** The old bar let you run several ticked schedules at once, but starting a job stops the one already running, so only the last of them ever kept running. The menu greys Run now out when more than one is ticked instead of pretending.

### Fixed
- **A paused schedule row lost its layout** (checkbox on its own line above the title). The row's style string had `opacity:.7` with no semicolon, which made the browser drop the `display:grid` that follows. This was already in v3; it is fixed and tested (a paused row is a grid and dimmed).

### Verified
- Full release process on the final tree: `gofmt -l` (nothing), `go vet`, `go build -buildvcs=false`, `go test -race -count=1` (cgo), `CGO_ENABLED=0` vet and `go test -count=1` (the fail-closed path), cross-compile for linux/amd64, arm64, arm, 386, riscv64, ppc64le, s390x, loong64 and mips, mipsle, mips64, mips64le, and the 32-bit (386) test binary run for real; all from the extracted archive as well as the working tree.
- `ui.js` against the real daemon (native amd64 cgo build, real PAM login with a throwaway user, UDP reflector alive before the run, fresh state directory) in Chromium, light and dark, every request outside the server refused (none attempted): all checks pass in both schemes. The schedule actions run against the real server (add, duplicate, pause, resume, delete); only Run now is stubbed, so that a benchmark is not started behind the rest of the test.
- A bug found by the new tests while writing them, and fixed before release: the featured slice was never drawn larger, because the "is anything featured" flag was computed before the slice list was filled in. The geometry test failed on it and passes now; the tooltip test alone would not have caught it, since the tooltip text does not depend on the drawing.
- Screenshots in light and dark of the two pies (a two-run and a three-run comparison, including the run from the bug report) and of the Schedules page with the menu open, with a paused row.

### Not verified
- Touch devices: the menu opens on the browser's `contextmenu` event, which a long press produces on Android Chrome and not on iOS Safari. There is no separate touch gesture, so on iOS the actions are not reachable (the old button bar was). Not tested on any touch device.
- Firefox and Safari. Chromium only.
- Nothing in the Go code, `install.sh`, `uninstall.sh` or the other files in `contrib/` changed (only `contrib/icons/gen.py` and `contrib/uitest/ui.js`), so the 225k q/s performance check, the installer suite and CodeQL were not re-run.

## [v5] - 2026-10-05 — One timestamp format on the Results page; "better" captions on the comparison pies

### Added
- **Captions under the comparison pies:** "Larger is better" under the throughput pie and "Smaller is better" under the p95 latency pie. Without them the two pies read as opposites (the bigger slice is the better run in one and the worse run in the other).

### Fixed
- **Scheduled and manual runs showed their start times in two formats** (`2026-10-05T10:03:55Z` next to `2026-10-05 14:34:51`). Both are UTC; only the format differed. The server stamps scheduled runs as ISO 8601 (`nowISO()` in `job.go`, unchanged), and the browser stamps its own runs as `YYYY-MM-DD HH:MM:SS`. The browser now converts everything to its own format on the way in (`normStarted()` in `bench.html`), so the stored history, the CSV export and the on-screen times all agree and the server API is untouched. Zone-less ISO times are read as UTC, offsets (`+02:00`) and fractional seconds are converted, and text that cannot be parsed is left as it was.
- **The same mix broke ordering.** Scheduled runs are merged into the history and sorted by comparing the `started` strings, and a space sorts before a `T`, so a scheduled run could land in the wrong place relative to manual ones from the same day. With one format the string order is the time order.
- **History already saved in your browser is repaired on load**, so existing ISO-stamped scheduled runs are converted without clearing anything. The stored format is unchanged, so downgrading to v4 still works.

### Verified
- Full release process on the final tree: `gofmt -l` (nothing), `go vet`, `go build -buildvcs=false`, `go test -race -count=1` (cgo), `CGO_ENABLED=0` vet and `go test -count=1` (the fail-closed path), cross-compile for linux/amd64, arm64, arm, 386, riscv64, ppc64le, s390x, loong64 and mips, mipsle, mips64, mips64le, and the 32-bit (386) test binary run for real; all from the extracted archive as well as the working tree.
- `ui.js` against the real daemon (native amd64 cgo build, real PAM login with a throwaway user, UDP reflector alive before the run) in Chromium, light and dark, every request outside the server refused (none attempted): all checks pass in both schemes. New checks: the two captions, the normaliser on ISO, fractional-second, offset, zone-less, already-normal, junk and empty input, saved history repaired on load and ordered newest first, a scheduled run delivered by a stubbed `/api/scheduler-history` stored in the normal format through the real `syncSchedulerHistory()`, and the legend showing every run in one format.
- Mutation check: with `normStarted()` turned into a pass-through, four of the new checks fail (including the wrong ordering); restored afterwards.
- Screenshot of the comparison with one manual and one scheduled run (the numbers from the bug report), dark theme.

### Not verified
- The times are still shown in UTC without a label, as before; they are not converted to the viewer's local time. That is a possible follow-up, not changed here.
- Firefox and Safari. Chromium only.
- Nothing in the Go code, `install.sh`, `uninstall.sh` or the other files in `contrib/` changed (only `contrib/uitest/ui.js`), so the 225k q/s performance check, the installer suite and CodeQL were not re-run.

## [v4] - 2026-10-05 — Output on its own tab; Results opens with the new run highlighted; pie-chart comparison; cleaner sign-in page

### Added
- **Benchmark page has two tabs, Setup and Output.** The output card (the terminal with its copy and clear buttons and status badge) moved from under the form to its own Output tab. Starting a run switches to Output, and a pulsing dot on the tab shows while a run is in progress. Re-running a past job from Results lands on Setup so the restored settings can be reviewed.
- **When a benchmark finishes, the Results page opens and the new run is highlighted**: accent outline, a "New" badge, a tinted background and a one-time pulse, scrolled into view. A multi-server run highlights every server that completed. The highlight is dropped when you leave the Results page or start another run. Two deliberate limits: nothing happens if the run was only stopped (no run completed), and nothing happens if you have already moved to another page (for example into the schedule editor), so a finished run never pulls you out of what you were doing.
- `DnsCharts.pie` in `web/charts.js`: a dependency-free canvas pie (slices clockwise from 12 o'clock, percentage labels on slices that can hold them, hover lifts the slice and shows a tooltip, redraws on resize and theme change, `destroy()` like the bar chart).
- Two icons for `contrib/icons/gen.py`: `sliders` (Setup tab) and `pie-chart` (Compare button); `ui.css` regenerated by the script (36 icons).
- `ui.js` checks for all of the above: the tabs and which pane shows what, Output opening on start, the jump to Results on completion, exactly one highlighted card and that it is on screen, the highlight clearing after leaving the page, no ReadMe/License links on the sign-in page, and the pies (both drawn, side by side, legend with error counts, hover tooltip, tooltip removed on close).

### Changed
- **Comparing runs now shows side-by-side pie charts** instead of a throughput bar chart over a p95 line chart: one pie of each run's share of combined throughput (q/s) and one of each run's share of combined p95 latency, one slice per selected run, the same colour for a run in both pies. Below them a shared legend lists each run with its q/s, p95, error count and rate, and start time, so the error information the bar chart marked is still there. Runs without a throughput or p95 value are left out of that pie only. The pie colours are evenly spaced hues, and the chart reads its background from the theme tokens at draw time, so it follows light/dark live. The canvas ids `compareQpsCanvas` and `compareP95Canvas` are unchanged. The bar chart code stays in `charts.js`, unused for now.
- The Compare button uses the pie icon.
- README: the Live output and Results history bullets describe the above.

### Removed
- **ReadMe | License links on the sign-in page.** Both are still in the sidebar after sign-in, and `/readme` and `/license` still serve the documents (unchanged and still tested).

### Verified
- Full release process on the final tree: `gofmt -l` (nothing), `go vet`, `go build -buildvcs=false`, `go test -race -count=1` (cgo), `CGO_ENABLED=0` vet and `go test -count=1` (the fail-closed path), cross-compile for linux/amd64, arm64, arm, 386, riscv64, ppc64le, s390x, loong64 and mips, mipsle, mips64, mips64le, and the 32-bit (386) test binary run for real; all from the extracted archive as well as the working tree.
- `ui.js` against the real daemon (native amd64 cgo build, real PAM login with a throwaway user in the `dnsbench` group, UDP reflector alive before the run) in Chromium, light and dark, every request outside the server refused (none attempted): all checks pass in both schemes, including a real 3 s benchmark that ended on the Results page with the new run highlighted.
- `node --check` on both inline scripts of `bench.html` and on `charts.js`; `assets_test.go` (icons defined, no other hosts, classes defined) passes.
- Screenshots of the login page, Setup, Output mid-run, Results with the highlight, and the comparison pies with a hovered slice, in light and dark. The first screenshots showed the legend wrapping badly (swatch separated from its text, dates broken across lines); it was reworked and re-checked before release.

### Not verified
- The UDP engine, scheduler, installer and everything else in `contrib/` apart from `icons/gen.py` and `uitest/ui.js` were not touched, so the 225k q/s performance check, the installer/uninstaller suite and CodeQL were not re-run for this release (no request-sending or input-handling Go code changed; the pages' new code only builds DOM with `textContent` and fixed class names).
- PAM on any architecture but native amd64 (the cross-compiles use the fail-closed stub); runtime beyond amd64 and 386.
- Firefox and Safari. Chromium only.
- `snaps/results.png` in the README is the earlier screenshot of the Results page and was not retaken.

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
