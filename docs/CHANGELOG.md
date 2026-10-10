# Changelog

## [v304] - 2026-10-10 — Statistics: Rewrite… on a domain

### Added
- Right-click a domain in Top domains ▸ **Rewrite…**: a small dialog asks for the Destination name (an address, records such as `A 10.5.5.5; TTL 300`, or another name to ask the servers for) and adds a row (any client, that exact name, no servers) at the top of the Policy-Based Resolution table. The same rules as in the table apply to what is written; a name that already has a row of its own is left alone and named in the message.

- A rewritten name (a row of its own with a Destination name, any client) has a ✏️ after it in Top domains, and its menu item reads **Remove rewrite**, which removes that row.

### Verified
cgo build, `node --check`. In a browser: Rewrite… on a domain with an address adds the row and the ✏️ appears (a row already in the table shows it too); Remove rewrite removes both. The Go tests were not re-run (only the web UI and docs changed). Not checked: that the proxy answers the rewritten name with the address (same row as one typed in the table). Not verified: rewriting to another name or to records from this dialog (the same cell as in the table, so the same code), and an invalid Destination name (the server answers with its message).

## [v303] - 2026-10-10 — More than one address per family on a gateway

### Added
- A gateway can hold further shared addresses in the subnet of its VIP: Edit gateway ▸ **More IPv4 addresses** / **More IPv6 addresses** (also Configure ▸ Gateways, `more_vip4` / `more_vip6` in the file; bare addresses, up to 32 per family, checked to be inside the VIP's subnet, not the network or broadcast address, not repeated and not used by another gateway). They fail over with the VIP: put on the active node's macvlan (on `lo` in real-MAC mode, and on `lo` of the other nodes), announced by gratuitous ARP / unsolicited neighbor advertisement, answered in ARP and neighbor discovery with the same slot MAC as the VIP, and each has its own DNS / DoT / DoH listener on the same pool. The hello packets still carry the VIP only, so the election and the protocol are unchanged and a node of an older version keeps electing with the others (it just does not answer for the further addresses). The gateway's tooltip lists them. Replicated in the cluster; the hash of a cluster that uses none is unchanged. Changing the list restarts that gateway's engines.

### Verified
gofmt, vet, `go test -race -count=1 .` (new: the checks on the list, the addresses with prefix, ARP answered for every address and not for others, a clash between gateways). In a browser against real-MAC mode: adding two addresses put them on `lo`, started a listener for each and answered DNS on them; removing them took them off again. Not verified: virtual-MAC mode (macvlan, gratuitous ARP and the answers on a real network), IPv6 neighbor discovery on a network, a failover between two nodes.

## [v302] - 2026-10-10 — Domains: same menu and 🚫 as clients

### Changed
- The Top domains right-click menu is like the clients': **Block** (a NODATA row for that name, any client) or, on a name that already has a keyword row of its own, **Unblock** (removes it). The three "Create PBR NODATA / NXDOMAIN / REFUSED" items are gone.

### Added
- In Top domains a name that has a row of its own in the Policy-Based Resolution table (any client, answered NODATA, NXDOMAIN, REFUSED or null) has the same 🚫 as a blocked client; its tooltip says which answer it gets. The icon goes when the row does. Wildcard rows (`*.example.com`) and rows that send the name to servers are not marked.

### Verified
cgo build, `node --check`. In a browser: Create PBR NODATA on a domain shows the icon with its tooltip (a row that was already there shows it too). Block then Unblock from the new menu checked (icon appears, then goes). Deleting a row by hand was not tried. The Go tests were not re-run (only the web UI and docs changed).

## [v300] - 2026-10-10 — A blocked client shows a 🚫

### Added
- In Top clients a client with a Block row has a 🚫 after its address (tooltip: why, and how to undo it). It goes when the row is removed, by Unblock or by editing the Policy-Based Resolution table.

### Verified
cgo build, `node --check`. In a browser: Block shows the icon, Unblock removes it, a row that was already there shows it. The Go tests were not re-run (only the web UI and docs changed).

## [v299] - 2026-10-10 — Sign-in survives an upgrade, shorter scan results

### Changed
- A client scan now shows only what matters in the tooltip: up or down, the operating system, the MAC address with its maker, and the open ports with service and version (twelve at most, then "and N more"), instead of the whole nmap report. If nmap prints something else entirely, it is shown shortened to 600 characters.
- Sessions were only in memory, so every restart of the daemon (an upgrade, a reboot) signed out everyone on that node; in a cluster you only noticed it on the node you were signed in to. They are now also saved to `<state-dir>/sessions.json` (mode 0600; only a SHA-256 of each cookie is stored, so the file cannot be used to take a session over) once a minute and at every sign-in and sign-out, and read back at start. The idle timeout and the 12 hour limit still apply to a restored session and the group is checked again on its first request. Still ending a session at once: sign-out, a changed or deleted account, a changed GUI listen address. The first upgrade to this version still signs you in again (the old version has nothing saved); the ones after it do not. Up to a minute of recent activity can be missing from the saved idle times after a crash.

### Verified
gofmt, vet, `go test -count=1` for the session tests (new: the scan summary, a session survives a restart, the file holds no cookie value, mode 0600, Stop keeps the file, sign-out removes it) and the full `go test -race -count=1 .`. Not verified: a real upgrade through the GUI, a real nmap run.

## [v298] - 2026-10-10 — Statistics: Unblock

### Changed
- Right-click a client that is already blocked: the item reads **Unblock** and removes that client's Block row (this client, any name, NODATA) from the Policy-Based Resolution table. Other rows for the client are left alone.

### Verified
cgo build, `node --check`. In a browser: right-click shows Block, after Block it shows Unblock, Unblock removes the row and the menu reads Block again. The Go tests were not re-run (only the web UI and docs changed).

## [v297] - 2026-10-10 — Statistics: client menu is Scan and Block, sidebar gateway Rename and Delete

### Added
- Right-click a gateway in the sidebar (under Topology) ▸ **Rename…** (a small form with just the name) and **Delete** (the same confirmation as deleting it from the drawing).

### Changed
- Right-click a client ▸ **Block** replaces the three "Create PBR … for all its queries" items: one NODATA row for that client (any name) at the top of the table. NXDOMAIN and REFUSED are gone from the client menu (the domain menu keeps all three).
- Right-click a client ▸ **Scan** (was "Scan (nmap)"). Docs and help updated.

### Verified
gofmt, cgo-off and cgo builds, `node --check`. In a browser: right-click a gateway in the sidebar, Rename… (new name shown in the sidebar and on the drawing), Delete (confirmation shown, Cancel keeps it). The Go tests were not re-run (only the web UI and docs changed).

## [v294] - 2026-10-10 — Statistics: Clear, silence a client, scan a client

### Added
- **Clear** next to the range buttons on the Statistics page: forgets the counts and top lists (every node's when the Node menu says Cluster) and rewrites `stats.json.gz` at once. CLI: `--stats-clear [--all-nodes]`. Written to the log with who did it.
- Right-click a client in Top clients ▸ **Create PBR NODATA / NXDOMAIN / REFUSED for all its queries**: a row for that client (name `*`) at the top of the Policy-Based Resolution table. A row that already exists for the same client is left alone.
- Right-click a client ▸ **Scan (nmap)**: nmap runs on the node you are looking at (`-Pn -T4 -F -sV --version-light -O`, 120 s host timeout, 150 s overall, two scans at a time, only an IP address is accepted and it follows `--`); the report shows in the client's tooltip. CLI: `--scan ADDR`. A node without nmap says so.
- The installer installs **nmap** (a failure is a warning: only Scan needs it).

### Verified
gofmt, vet, `go test -race -count=1 .` (new tests: address checking, the nmap command line, the report tidying, a fake nmap run end to end, no nmap, Clear), cgo-off and arm64 builds, `node --check`, `bash -n install.sh` and `install.sh --dry-run` (nmap listed). In a browser with a stand-in nmap: Clear, the client menu, a scan and its tooltip, and a client row written to the config. CLI: `--stats-clear`, `--stats-clear --all-nodes`, `--scan`, and a bad address refused.

### Not verified
A real nmap run (the test machine has none; the options are standard nmap options); Clear on a cluster of several nodes (one node tested); the installer's nmap step on a real package manager.

## [v293] - 2026-10-09 — Statistics: the tooltip bug, for real

### Fixed
- A name in Top clients / Top domains could show the tooltip of another name. The real cause: the lists are redrawn in place and a row's button is reused for another name, but its hover handler had been added with `addEventListener`, so it stayed on the button and looked up the name the button had before. The handlers are now set the way every other handler on these pages is, so the redraw replaces them. (v292's check on the late whois answer is kept.)
- The tooltip no longer goes back to "Hover for whois" at each refresh when the answer is already known.

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check`. In a browser, with live traffic re-sorting the lists: 85 hovers over five refreshes, every tooltip was either its own name's data or the "Hover for whois" prompt; none showed another name. Every other list that is redrawn in place (Statistics, Host, Gateways, Cluster, Users, Anycast) was searched for the same pattern; this was the only one.

### Not verified
Real whois answers (the demo has no outbound port 43).

## [v292] - 2026-10-09 — Statistics: tooltip showed another domain's data

### Fixed
- A name's tooltip (whois or reverse-DNS data) could show the data of a different name. The lists are redrawn in place every few seconds and a row's button can end up showing another name; a whois lookup that finished after that wrote its text into the button's tooltip. The lookup now writes only when the button still shows the name it was started for.

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check`.

### Not verified
The race itself (it needs a slow whois answer and a re-sorted list at the same moment); the fix is a check that the button still shows the same name.

## [v291] - 2026-10-09 — Statistics: long names are cut with an ellipsis

### Changed
- Top clients and Top domains: a long name is cut with an ellipsis, so Queries and Share are always in view without scrolling sideways. Hovering the name still shows the full name (and the whois or reverse-DNS data).

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check`. The page at 900 and 520 px wide; a long name in a plain test page with the real stylesheet is cut with an ellipsis.

### Not verified
A long name on the live page (none of the demo's names was long enough to reach the top lists).

## [v290] - 2026-10-09 — Statistics: create a policy row from a domain

### Added
- Right-click a name in **Top domains** ▸ **Create PBR NODATA**, **Create PBR NXDOMAIN** or **Create PBR REFUSED**: a row (any client, that exact name, that keyword) is put at the top of the shared Policy-Based Resolution table and saved (it shows in the config history and goes to the cluster like any other save). If a row for that name already exists, nothing is added and the message names it; if Policy-Based Resolution is switched off, the message says so.

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check`. In a browser: right-click a domain, Create PBR NODATA; the row was written to the config file and recorded in the history.

### Not verified
The NXDOMAIN and REFUSED items (same code path with a different keyword); gateways that carry their own `dns` block (the row goes in the top-level one).

## [v289] - 2026-10-09 — Statistics: click QPS to graph it

### Added
- The **QPS** tile is a button: it draws queries per second in the chart (one line, y axis in queries per second, tooltip with the rate). With a kind picked (NX Domain, say) it is that kind's rate. Clicking it again or any other tile returns to counts.

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check`. In a browser with about 25 queries per second of test traffic: the tile read 24.3 (peak 25.8) and the graph showed the same ramp.

### Not verified
The QPS graph combined with a client or domain drill-down; ranges longer than an hour.

## [v288] - 2026-10-09 — Statistics: QPS tile

### Added
- A **QPS** tile on the Statistics page: queries per second in the last finished interval (the newest one is still filling), with the highest interval of the range underneath and the average in the tooltip. The tiles are slightly narrower so nine fit in a row.

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check`. The tile and the one-row layout in a browser (with no traffic: 0.0).

### Not verified
The tile's numbers with traffic (the demo had none during the check); it divides each interval's count by the interval length.

## [v287] - 2026-10-09 — Users: Save button; install example trimmed

### Changed
- Users page: the password and expiry no longer save by themselves. A **Save** button at the right of the form sends what changed (a typed password, a different date; blank date = never expires); **Disable** and **Delete** are at the left.
- README: the `get.sh` example lines with `--add-user` and `--version` are removed.

### Fixed
- Users page: after **Add user** or **Add an existing account…** the form stayed on screen instead of showing the new account.

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check`. In a browser: Add user then Save with a new date and password.

### Not verified
Disable and Delete from the new position; Add an existing account.

## [v286] - 2026-10-09 — Policy table: normal row height

### Fixed
- Policy rows were about twice as tall as they need to be because the row number and the drag handle were stacked in the first cell. They now sit side by side, so a row is one input high. The README's policy screenshot is retaken.

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check`. Row height checked in a browser.

### Not verified
Dragging a row by the handle after the change (only the handle's layout changed).

## [v285] - 2026-10-09 — Users page: list and detail

### Changed
- Users page redesigned: the accounts in a list on the left (with a filter box), the chosen account on the right. **+ New** and **Add an existing account…** open their forms in the right-hand panel.
- New password and Expires save by themselves (the password when the field is left or Enter is pressed, the date when it is picked); a blank date means the account never expires. The Set password, Save and Never expires buttons are gone.
- "Remove from group" is now **Disable**. Help and README follow.
- The Users screenshot in the README is retaken.

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check` (app.js, help.js). In a browser: selecting a user, saving a password with Enter, picking and clearing a date, a poll not clearing what is being typed.

### Not verified
+ New, Add an existing account, Disable and Delete in a browser (code paths unchanged; only the buttons that call them moved).

## [v284] - 2026-10-09 — Users menu fix; no legend on an empty Topology

### Fixed
- Users page: right-click > Password and Expiry did nothing (the menu edited a copy of the row that had been replaced by the live one); the editor now opens in the live row.
- Topology page: the colour legend is hidden while no gateway is defined.

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check`. In a browser: right-click > Password and > Expiry on a Users row open the password and date fields.

### Not verified
The empty-Topology legend in a browser (small change: toggles a `hidden` class once the config has loaded).

## [v283] - 2026-10-09 — README rewritten, with screenshots

### Changed
- README.md rewritten to read as plain prose, with 18 screenshots of the matching GUI pages (Topology, DNS proxy, Policy, Gateways, Statistics, Host, Capture, Log, Cluster, Anycast, Users, History, Web certificate, Node, Upgrade, login) in `snaps/`. The old `topology.png` and `statistics.png` (v170, old branding) are replaced.
- "Build from source" moved to the end, just before "License".
- "A web GUI (PAM login)" is now "A web GUI".

### Fixed
- Policy table toolbar: the per-page select was full width and the filter box unstyled; both now match the rest of the bar.

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check`; every image link and contents anchor in the README resolves.

### Not verified
Screenshots come from a demo setup (stub servers on this sandbox), not a production node.

## [v282] - 2026-10-09 — Paused probe domains: amber with one left, down with none

### Changed
- A server with all but one of its probe domains paused is **amber** (warn) on the Topology page, saying that only one domain is being checked.
- A server with **every** probe domain paused is **red** and marked down: it is left out of the pool like a paused server (no queries, no probes) until a domain is resumed. Before, the pool quietly went on probing it with all its domains, so it looked fine.
- The last active domain of a server can now be paused (it was refused with "pause the server instead"); that is how a server is brought to the down state this way.
- The rest of the gateway's state follows as it does for any down server (the fallback servers are used when no server is left).

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check`. New tests: the pool leaves the server out and keeps the others' domains; the canvas shows ok, warn (one domain left) and bad (none) from a real probed pool.

### Not verified
The drawing in a browser.

## [v281] - 2026-10-09 — Policy: `?` in a source name

### Added
- `?` in a source name pattern stands for exactly one character (never a dot): `*host?.sub?.xyzzy.com` matches `myhost1.sub2.xyzzy.com`, not `host.sub2.xyzzy.com` or `host12.sub2.xyzzy.com`. It combines with `*` and is indexed like the v280 patterns.
- Tests: `?` cases in the pattern table; `?` rows in the index-versus-scan test.

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check`.

### Not verified
Through a running daemon and the GUI.

## [v280] - 2026-10-09 — Policy: `*` inside a source name

### Added
- A source name may hold `*` inside its labels: `*host*.sub*.xyzzy.com`, `ad-*.example.com`, `host*`. A `*` is any run of characters within one label (never a dot), so the pattern has as many labels as the name; a leading `*.` as a whole label still means any labels in front, or none. A pattern is only a source name: as a destination it is refused, and a `*.name` destination needs a plain `*.name` source.
- Patterns are found through the index by the literal labels at their end (`xyzzy.com`), so a table of them costs about what exact rows cost; a pattern with no literal end (`host*`) is tried for every name.
- Tests: the pattern matcher on a table of cases, the refusals, and the index-versus-scan test now includes patterns.

### Verified
gofmt, vet, `go test -race -count=1 .`, cgo-off and arm64 builds, `node --check`.

### Not verified
Through a running daemon and the GUI.

## [v279] - 2026-10-09 — Help: one page for each DNS proxy tab

### Changed
- The DNS proxy page's help is six pages now, one per tab (Servers, Health & balancing, Listeners, Resolution, Cache & ECS, Clients). Opening Help shows the page of the tab you are on, and switching tabs with Help open switches the page. Each page opens with a short description of the tab and then its fields; the fields were moved from the one long Configure page, which now points to the six. No text of the fields was changed.

### Verified
`node --check` on app.js and help.js; the help data loads and has the six pages; every DNS proxy field of the old page is on exactly one of them (checked by the script that moved them).

### Not verified
The panel following the tabs in a browser.

## [v278] - 2026-10-09 — Policy: local answers in the destination name, paging and a filter

### Added
- **Local answers in *Destination name*.** With the servers blank, the destination name can be the answer: `10.5.5.5` (or `10.5.5.5, 2001:db8::5`) makes the name those addresses, and the record syntax of v277 (`A …; TTL 300`, `CNAME …`, `TXT "…"`) works there too. Servers together with a local answer are refused. The servers-cell form of v277 still works; the GUI shows either in the Destination name column and saves the text as `dest`, which the daemon stores in its normal form.
- **Paging and a filter** on the policy table: 25 to 500 rows per page (50 by default), first/previous/next/last and a page number, and a *Filter rows* box that keeps the rows with that text in any cell. Rows are numbered by their place in the whole table (the number in the policy log lines). Add and Paste clear the filter so the new row shows. Right-click ▸ Move to top / Move to bottom added; Move up / Move down follow the filtered view; dragging works within a page.

### Changed
- The table is built from a list of rows and draws one page, so 10000 rows no longer mean 10000 sets of inputs.

### Verified
Not verified in a browser: the table was checked with `node --check` only; try it with a long table, a filter, paging, drag and right-click. Go: gofmt, vet, tests (new: local answers in the destination name, refusal with servers, a hostname destination still a rename), race run, cgo-off and arm64 builds.

### Not verified
The GUI behaviour above, in any real browser.

## [v277] - 2026-10-09 — Policy: local records, a log of matches, 10000 rows

### Added
- **Local records.** The *Destination servers* cell can hold the answer itself: `A 10.5.5.5, 10.5.5.6`, `AAAA 2001:db8::5`, `TXT "some text"`, several kinds separated by `;`, `TTL 300` (seconds, default 60) and `CNAME host.example.com`. A type the row has no records of gets "no data", ANY gets everything, the answer is marked authoritative. A CNAME row answers CNAME queries with the CNAME and any other type with the CNAME followed by what the gateway's own servers answer for the target (a CNAME cannot be combined with other records). Records take no destination name. The GUI sends such a cell whole instead of splitting it into servers.
- **Log of matches.** Each query a policy row applied to (a `pool` exception too) writes `dns: policy row 3: 10.1.1.1 asked www.example.com A: answered null` (or `sent to …`, `asked as …`, `answered A …`), at most 100 a second, the rest counted in one line. Switched by *Log policy matches* (`policy_log`, on by default).
- **10000 rows** (was 1000). Rows are found through an index by the name they name, then taken in table order for the first whose client matches, so a long table costs about what a short one does; a test checks the index against a plain scan of 3000 random rows.
- Tests: the record syntax and its refusals, the answers through the frontend (A with TTL, AAAA no data, TXT, CNAME and the chase, no server reached), the index against a scan, the log lines and the switch.

### Changed
- The record-copying part of the renaming code is one function now, shared with the CNAME answer.

### Verified
- gofmt, `go vet`, `node --check`, builds, the whole suite with `-race`, cgo-off and arm64 builds. Not looked at in a browser; a table of 10000 rows in the GUI (50000 inputs) is not tried and may be slow to draw.

## [v276] - 2026-10-09 — Policy table: reorder rows

### Added
- Rows of the policy table can be dragged by the dots at their left to a new place (a bar shows where the row will land), and right-click has **Move up** and **Move down** (not offered at the ends of the table). The order matters because the first row that matches wins.

### Verified
- `node --check`, `go build`, `go vet`, the Help tests. Not run: the full suite and a browser (page script only, no Go change), so dragging is not tried by hand.

## [v275] - 2026-10-09 — Policy: `*.name` includes `name`

### Changed
- In a policy row, a source name `*.example.com` now matches `example.com` itself as well as every name below it (it used to need a row of its own). With a `*.name` destination, `example.com` is asked as the destination's own name (`*.example.com` → `*.other.net` asks `other.net`), and `other.net` in the answer is written as `example.com`.
- A row whose servers cell is `pool` and that has a destination name is read as "no servers" when the pool is built, not only when it is saved. A shared DNS block that no gateway uses (every gateway has its own) is not checked when it is saved, so such a row could keep the word `pool` and be treated as "the pool answers, no renaming".
- The log line written when a DNS pool starts now says how many policy rows it has, and whether policy is off, so that a node that is not applying the rows can be told apart from one that is (a node running a version before the table, or with policy off, shows none).

### Verified
- gofmt, `go vet`, `node --check`, builds, the whole suite with `-race`, cgo-off and arm64 builds. Not looked at in a browser. A reported case where no row had any effect was not reproduced here: the same rows, as saved by the GUI, answer correctly through the saved config, the pool and a real UDP query, with the gateways using the shared DNS settings and with their own.

## [v274] - 2026-10-09 — Policy table spacing

### Changed
- A little space between the "Policy rows" title and the table.

### Verified
- `node --check`, `go build`, `go vet`. Not run: the full suite and a browser (one CSS line).

## [v273] - 2026-10-09 — Configure ▸ DNS proxy in tabs

### Changed
- The DNS proxy page is six tabs instead of one long page: **Servers** (upstream servers), **Health & balancing** (load balancing, timing), **Listeners**, **Resolution** (policy-based resolution, sort list), **Cache & ECS** and **Clients** (rate limiting). The settings, their saving and their Help entries are the same; the tab you were on is kept while the page is open, and the arrow keys move between tabs. The documentation names the tab where it names a section.

### Verified
- `node --check`, `go build`, `go vet`, the Help tests. Not run: the full suite and a browser (page layout only, no Go change), so the tab strip is not looked at.

## [v272] - 2026-10-09 — Policy-Based Resolution: a destination name without servers

### Changed
- A row with a *Destination name* and no *Destination servers* (or the word `pool` there) asks the gateway's own servers for that name and turns the answer back, as any other renaming row does. Before, such a row was dropped when saved because it had no servers, which left the name in the table but never saved or used. A row with neither servers nor a destination name is still not saved; its servers cell is outlined until it has one of them (the outline was on the wrong column since the fourth column was added).
- Tests: a rename through the pool, both spellings, and the refused empty row.

### Verified
- gofmt, `go vet`, `node --check`, builds, the whole suite with `-race`, cgo-off and arm64 builds. Not looked at in a browser.

## [v271] - 2026-10-09 — Policy-Based Resolution: rows that answer by themselves

### Added
- The *Destination servers* cell of a policy row may hold one keyword instead of servers, and the row then answers without asking anyone: `null` (A `0.0.0.0`, AAAA `::`, no data for other types), `nxdomain`, `nodata`, `refused`, or `pool` (the gateway's own servers answer, as if no row had matched; put it above a broader row as an exception for one client or name). A keyword stands alone: with other servers or a destination name the row is refused. Together with the wildcard source names this makes the table usable for blocking names, per client network. These answers are not cached and not counted against any server.
- Tests: the keywords through the frontend (each answer, the exception row, other clients still blocked, no server reached) and the refused combinations.

### Verified
- gofmt, `go vet`, `node --check`, builds, the whole suite with `-race`, cgo-off and arm64 builds. Not looked at in a browser.

## [v270] - 2026-10-09 — Policy-Based Resolution: DNSSEC note corrected

### Changed
- The README and the Help said a policy row's answer is not DNSSEC-valid without saying that this holds only for rows with a Destination name. A row without one passes the servers' answer through untouched, signatures included. Wording only; no code change.

### Verified
- `node --check`, `go build`, `go vet`, the Help tests. Not run: the full suite (documentation text only).

## [v269] - 2026-10-09 — Policy-Based Resolution: destination name

### Added
- A fourth column on the policy table, **Destination name** (`dest` in the row), after the servers: the name the row's servers are asked for instead of the one the client asked. Blank asks for the client's own name; `zdnet.com` asks for that name; `*.hardocp.com`, on a row whose source name is `*.something`, keeps the part in front (`www.reddit.com` is asked as `www.hardocp.com`). The client's answer is turned back into one for the name it asked: the question and every record named like the name that was asked (or below a `*.name` destination) carry the client's name, in the A/CNAME/NS/PTR/MX/SOA records the names inside them too. Other records are as the servers sent them. Answers are cached per row's servers and destination.
- Limits: a signed (DNSSEC) answer is not valid for the client's name; a record of a type that may hold names the daemon cannot move safely is answered with SERVFAIL.
- The table's columns are now Source client, Source name, Destination servers, Destination name (pasting tab-separated rows uses that order).
- Tests: destination validation, the query and answer rewritten both ways (wildcard, exact, CNAME target, SOA, refused type), and through the frontend with two rows.

### Verified
- gofmt, `go vet`, `node --check`, builds, the whole suite with `-race`, cgo-off and arm64 builds. Not looked at in a browser.

## [v268] - 2026-10-09 — Policy-Based Resolution

### Added
- **Configure ▸ DNS proxy ▸ Policy-Based Resolution**: send a query to servers of its own depending on who asks and what is asked. A table of rows (*Source client*, *Destination name*, *Destination servers*) edited like a spreadsheet; right-click a row for Edit, Copy, Paste, Add and Delete (right-click below the rows to add one at the end). The first row, from the top, that matches both the client (`*`, an address, a network or a comma-separated list) and the name (`*`, an exact name, `*.name` for the names below it) is used; anything no row matches goes to the gateway's servers as before. Shared by the cluster; a tick box switches the whole table on or off without deleting it (`policy`, `policy_on` in the `dns` block).
- Works for plain DNS, DoT and DoH clients (the client address is the connection's) and with plain, DoT and DoH servers in the rows. A row's servers are tried in the order given, one that failed its last query after the others; they are not probed, so never marked down. When none answers the client gets SERVFAIL; there is no fall-back to the gateway's servers. Answers are cached per row's server list, so a client sent to one set never gets an answer cached for another. The daemon's own queries (reverse lookups, update relays) ignore the table.
- Tests: row validation, matching (exact, wildcard, networks, mapped IPv6, first match wins), routing and per-row caching through the frontend, no leak to the pool when a row's servers are dead.

### Verified
- gofmt, `go vet`, `node --check`, builds, the whole suite with `-race`, cgo-off and arm64 builds. Not looked at in a browser, so the table editor and its right-click menu are untested by eye.

## [v267] - 2026-10-09 — Replica banner removed

### Changed
- The Configure pages no longer show the "This node is a replica: changes to settings marked shared are sent to the primary first and replicate back" banner. Behaviour is unchanged; the Help text still explains how shared settings replicate.

### Verified
- `node --check`, `go build`, `go vet`. Not run: the full test suite and a browser (text-only UI change).

## [v266] - 2026-10-09 — Add an existing account to the GUI group

### Added
- **Configure ▸ Users ▸ Add existing user**: type (or pick from the suggestions: ordinary accounts not yet in the group) the name of an account that is already on the machine and press *Add to group*; it can then sign in with the password it already has. Nothing else about the account is changed. In a cluster the other nodes add it to the group where it exists and create it with the same password hash where it does not. CLI: `ddgw --user-grant NAME`.
- The Users table has no buttons any more: right-click a row for **Password**, **Expiry**, **Remove from group** and **Delete** (the last two not on your own row). **Remove from group** (CLI `ddgw --user-revoke NAME`): takes the account out of the group and keeps it, on every node. Refused for the signed-in user, for the last account that can sign in, and for an account that is a member only because the group is its primary group (the message says so). *Delete* is unchanged and still removes the account.
- Tests: the suggestions, the grant and revoke commands and their refusals, and the peer side (existing account, account created from the hash, no hash, revoke of a non-member).

### Verified
- gofmt, `go vet`, `node --check`, builds, the whole suite with `-race`. Not tried against a real `usermod`/`gpasswd` or looked at in a browser.

## [v265] - 2026-10-09 — The changelog, examples and tests carry no site names or addresses

### Changed
- Host names, domain names and addresses taken from a real site's reports are replaced by neutral ones in the changelog, the README, the help, the command-line usage and the tests (documentation-style addresses, `example.com`, `node2`). The v261 and v263 entries also said the two slow servers could not resolve external names; they can (their probes of an external name pass), they just time out on a few names, and the cause is unknown.
- No behaviour change.

### Verified
- gofmt, `go vet`, `node --check`, the whole suite with `-race`.

## [v264] - 2026-10-09 — A server's Loss graph counts probes only; failed queries get their own graph

### Changed
- **Topology ▸ server ▸ Statistics**: the *Loss* graph (live queries and probes added together) is now **Failed probes**, the share of the server's own test queries that failed, which is what decides whether it is up. A new **Failed queries** graph shows the share of client queries sent to it that failed (timeouts, SERVFAIL, REFUSED). A healthy server that times out on a few names no longer shows up as lossy.
- `--server-stats ADDR` shows both (columns `loss %` for probes and `query %`, and both totals); the JSON has `loss` (probes) and `query_loss` (queries). Gateway and domain graphs are unchanged. The two graphs are described in help and the README.

### Verified
- gofmt, `go vet`, `node --check`, builds, the whole suite with `-race`. Not looked at in a browser.

## [v263] - 2026-10-09 — Only the probes mark a server DOWN, not client queries

### Cause (v262 bundle)
- v261 cut the timeouts to about one a minute per name and server, but the servers still flapped DOWN: two timeouts in a row on different names were enough. In the bundle two of a gateway's four servers timed out on a small set of names asked by three clients, while the other two answered them and every probe passed; the gateways at other sites, which those clients use when this gateway is not announcing the address, have servers that answer them.

### Changed
- **A client query that times out on a server no longer marks it DOWN.** Only the server's own probe rounds (its test queries) do, as the request was. A failed query is still counted (the server's failures, history, last error), the same query is retried on the next server as before, and the name is tried last on that server for a minute (v261).
- A server that really dies is therefore marked DOWN by the next probe round (5 s by default, `fail_threshold` rounds in a row) rather than by the first failed queries; until then each query that lands on it waits its timeout and is retried elsewhere.
- Tests: `TestForwardFailuresNeverMarkAServerDown` (timeouts counted, server stays up, the probe takes a dead one down); two older tests now expect the probe, not the query, to demote.

### Not changed
- The two servers still time out on those names (the cause is on those servers and unknown; asking each of them for one of the names with `dig` and comparing the times shows it); removing them from the gateway's server list removes the timeouts. The first lookup of each such name per minute still waits one timeout.

### Verified
- gofmt, `go vet`, builds, the whole suite with `-race`.

## [v262] - 2026-10-09 — Sort list can be switched off

### Added
- **Sort list enabled** tick box (Configure ▸ DNS proxy ▸ Sort List; `sortlist_on` in the `dns` block, shared by every gateway). Off keeps the list and leaves answers in the order the servers gave. On is the default, so a config with a sort list and no `sortlist_on` keeps sorting. The description is in help.

### Verified
- gofmt, `go vet`, `node --check`, builds, the whole suite with `-race`. Not looked at in a browser.

## [v261] - 2026-10-09 — A server that times out on a name is tried last for that name

### Cause found (v260 bundle)
- It is not a loop. All 81 forwarding failures were timeouts of two servers on a small set of external names, asked by three clients, over UDP and TCP alike; the other two servers answered them. Those clients use the anycast address, which is why it starts when that address is announced at this gateway. The slow servers pass the health probes (so they do resolve external names) but time out on these particular names, so each such query waited out the 1.5 s query timeout on one of them before another server answered, and two in a row marked it DOWN until the next probe.

### Changed
- A server that times out on a name (and type) is asked that name last for the next minute, after the others; it stays in service for every other name. A client's second and later lookups of such a name no longer wait for the timeout, and the server stops flapping DOWN on them. The first lookup still pays one timeout.
- Not changed: the servers themselves. Why they time out on these names is unknown (they answer other external names); adding one of these names to a server's test queries (Topology ▸ server) would mark it down for good when it fails.

### Verified
- gofmt, `go vet`, the whole suite with `-race`.

## [v260] - 2026-10-09 — The troubleshooting bundle says who asked for what when a server fails

### Added
- **Bundle: `ddgw/dns-forward-failures.json`** lists the last 200 forwarding failures (time, client, name, type, server, transport, error), and **`ddgw/dns-queries-last-10-min.json`** the top clients, domains, types and transports of the last ten minutes. A server is marked DOWN by forwarding failures, and the log line cannot say which queries caused them.

### Notes
- The v259 bundle showed no "is sending queries" warning, so the queries that time out do not come from a server's own address; the loop theory of v259 is not confirmed. What the bundle does show: since the restart one server failed 9 of 63 queries and another 1 of 3, while the two others failed none of 1957, and no client got SERVFAIL (the next server answered). The new files show which client and names those queries are.

### Verified
- gofmt, `go vet`, the whole suite with `-race`.

## [v259] - 2026-10-09 — Queries are never sent back to the server they came from; sort list takes plain networks

### Fixed
- **Forwarding loop through an upstream server.** In a troubleshooting bundle, switching on an anycast address made two of the gateway's servers flap DOWN with UDP and TCP timeouts within seconds, and switching it off stopped it at once, while the probes (cached names) kept passing. That is the signature of a loop: a server whose forwarder is the gateway or an anycast address sends the gateway the very query the gateway just sent it. A query that arrives from one of the pool's own servers is now never forwarded to that server; the others answer it, so the original query completes. A query from this machine itself (loopback or one of its own addresses) is never taken for that, so a local program or a DNS server on the same host is not affected. The log says once a minute which server does it and what to fix. (The next bundle showed this was not the cause; see v260 and v261.)

### Changed
- **Sort list**: an entry that is just a network (`10.1.0.0/16`) is accepted and is for every client; all such entries together give the order, and the one the client is in comes first. A config in the field had plain networks, which the stricter format did not describe.
- Tests: the asker dropped from the candidates (also as an IPv4-mapped address), plain-network sorting.

### Verified
- gofmt, `go vet`, builds, the whole suite with `-race`.

### Not verified
- The loop itself was not reproduced against real servers; the cause is inferred from the timing in the bundle.

## [v258] - 2026-10-08 — Configure ▸ Settings split into one page per tab

### Changed
- Configure ▸ Settings is gone. Its tabs are now pages of their own under Configure: General, Gateway groups, DNS proxy, Web GUI and Cluster (then Anycast, Users, History as before). Each page saves as the old form did. Old links (`#config`, `#certificate`) open General and Web GUI. The help for all five pages is the former Settings help. Every "Settings ▸ …" path in the GUI text, help, README, QUICKSTART and the program's messages now reads "Configure ▸ …".

### Verified
- `node --check`, gofmt, `go vet`, `go build`, the help/field and web tests.

### Not verified
- Not looked at in a browser; the full suite was not re-run (web files and message text only).

## [v257] - 2026-10-08 — DNS sort list

### Added
- **Sort list** (`sortlist` in the shared `dns` block; Settings ▸ DNS proxy ▸ Sort List): per-client-network ordering of the A and AAAA records in an answer. Each entry is `client-network: preferred-network, preferred-network` (e.g. `10.1.0.0/16: 10.1.0.0/16, 10.0.0.0/8`); the first entry that contains the asking client is used, `any` matches every client. Addresses inside the client network itself always come first (it need not be repeated in the list), then those in the first preferred network, the second, and so on; others keep their order after those. The sort is stable and applied to each client's copy, so the cache is not changed. Error answers and answers carrying anything besides CNAMEs and addresses are left as they came. Empty (default) changes nothing. The option has no text on its card; the description is in help, the README and `--help`.
- Tests: ordering per client, the input message untouched, CNAME kept first, no rule / error answer / already-ordered left alone, entry validation and normalising.

### Verified
- gofmt, `go vet`, `go build` (amd64, CGO off, arm64, arm), `node --check`, the whole suite with `-race`.

### Not verified
- Not looked at in a browser, and not tried against a live resolver.

## [v256] - 2026-10-08 — Configure ▸ Anycast: no descriptions on the card, tighter fields

### Changed
- The BGP card has no description text any more (the line under the title and the lines under Local AS number and Router ID are gone; the help page has them), and its fields are narrower with less space between them.

### Verified
- `node --check`, `go build`, `go vet`. Not looked at in a browser and the test suite not re-run: a layout change in two web files.

## [v255] - 2026-10-08 — Configure ▸ Anycast: AS, router ID, timers and AS Prepend on one line

### Changed
- The BGP card's fields are on one line: Local AS number, Router ID, Keepalive, Hold time (no "(seconds)", short fields) and the **AS Prepend** tick box next to them. They wrap on a narrow window.

### Verified
- `node --check`, `go build`, `go vet`. Not looked at in a browser and the test suite not re-run: a layout change in two web files.

## [v254] - 2026-10-08 — The AS prepend tick box is just the box and "AS Prepend"

### Changed
- Configure ▸ Anycast: the tick box reads **AS Prepend** (it said "Prepend the local AS 3 times"). The hint text under it is gone too; the explanation is in the help page. Behaviour unchanged.

### Verified
- `node --check`, `go build`, `go vet`. Not re-run: the full test suite (a label change in two web files).

## [v254] - 2026-10-08 — The AS prepend tick box is just the box and "AS Prepend"

### Changed
- On Configure ▸ Anycast the tick box reads **AS Prepend** (it said "Prepend the local AS 3 times"). What it does is unchanged and is in the hint under it and in the help page.

### Verified
- `node --check`, `gofmt -l .`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles (built to /dev/null; archive checked for binaries).

## [v252] - 2026-10-08 — Configure ▸ Anycast: AS prepend

### Added
- **AS prepend** tick box on Configure ▸ Anycast (BGP card). Ticked, the node adds its local AS **three more times** to the AS path of every anycast route it announces (`set as-path prepend ASN ASN ASN` in the outbound route-maps for IPv4 and IPv6; the inbound route-map is unchanged), so routers prefer the nodes that do not have it ticked. It is a per-node setting like the rest of the card (`as_prepend` in the `bgp` block, not shared), applies at once through the usual FRR reload, and `ddgw --as-prepend on|off` does the same from the command line; `ddgw --bgp` shows it when on. Help and README describe it.
- Test: `TestRenderFRRASPrepend`.

### Verified live
- Headless Chromium against a real daemon: the tick box sits under the hold time, ticking it saved `as_prepend: true` in the configuration.

### Not verified
- Against a real router: the generated `frr.conf` was checked, FRR was not run here. A BGP session shows the longer path as `<AS> <AS> <AS> <AS>` (the three prepended plus the one FRR adds when sending to an eBGP neighbor).

### Verified
- `gofmt -l .`, `node --check`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles (built to /dev/null; archive checked for binaries).

## [v251] - 2026-10-08 — tshoot finishes again

### Fixed
- **tshoot (Monitor ▸ Log ▸ tshoot, `--tshoot --all-nodes`) never finished, so there was no download.** Collecting another node's bundle is one long cluster call (up to a minute: the 8 s capture, FRR, the journal). A peer has several addresses (its name and its IPs) and the call code cut the call short after **6 s on every address but the last** and started it again on the next one, so each node was asked again and again, up to once per address, and the whole request ran past the web server's 90 s write limit and was dropped. The cut-off was meant to give up on a dead address quickly; it now does that where it belongs, in making the connection (name lookup, TCP, TLS handshake: 6 s per address), and a call that is connected may take as long as its caller allows. Every long call to a peer (a bundle, an update upload) was affected, not only this one.
- The per-node wait for a bundle is 75 s (was 90 s, equal to the web server's write limit), so a node that does not answer gives a note in the bundle instead of the whole download being cut off.
- Test: `TestDialPinnedGivesUpOnASilentAddress`.

### Not verified
- tshoot on the real cluster (the long relayed call was not reproduced here; the cause is from reading the call path: the 6 s cap in `callRaw` against an 8 s capture).

### Verified
- `gofmt -l .`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles (built to /dev/null; archive checked for binaries).

## [v250] - 2026-10-08 — Anycast page: the "announced to … neighbors only, and there is none" banner is gone

### Removed
- The yellow banner on Monitor ▸ Anycast (and `ddgw --show-bgp`'s note) "IPv6 anycast addresses are announced to IPv6 neighbors only, and there is none." and its IPv4 twin. An address of one family with no neighbor of that family simply is not announced; the table of anycast addresses and the neighbors list already show that. The other notes (FRR cannot reload, nothing is announced yet) stay.

### Verified
- `gofmt -l .`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles (built to /dev/null; archive checked for binaries).

## [v249] - 2026-10-08 — A node that joins the cluster is not added to the gateways

### Changed
- **Joining no longer changes who answers for a gateway.** Until now a node that joined served every gateway at once, which meant a half-set-up node took client traffic the moment it was in. Now the primary, when it accepts a join, adds the new node's ID to every gateway's shared `excluded_nodes` list **before it answers the joiner**, so the joiner's first sync already carries it and it never starts serving. Put it to work where it should: right-click the gateway ▸ **Add node** ▸ the node (or `--canvas-add node --group N --node NODE`). In the Topology the new node is drawn as removed until then.
- A gateway created after the join is served by every node, as before. Nodes that are already members, and a member that reconnects with the same node ID, are not touched; so nothing changes on upgrade.
- Help, the README and the usage text say so.
- Test: `TestNewNodeIsNotAddedToTheGateways` (the primary's and the joiner's configuration both list the new node in every gateway).

- **Monitor ▸ Anycast no longer lists the anycast addresses of a gateway this node was removed from** (they showed as "withdrawn — gateway paused", though they were never this node's). A gateway that is merely paused on the node is still listed as paused.
- Test: `TestAllAnycastStatesSkipsAGatewayThisNodeWasRemovedFrom`. Two existing tests that assumed a joined node serves every gateway (`TestUpdateSafeForTheOnlyMember`, `TestRollingUpdateWithPausedNodes`) now add the joined nodes to the gateway first, and also check that a node that has only joined does not hold an update back.

### Not verified
- On a real cluster. The join itself is the same code path as before; the added step is one shared-settings save on the primary, done before it replies.

### Verified
- `gofmt -l .`, `node --check`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles (built to /dev/null; archive checked for binaries).

## [v248] - 2026-10-08 — Topology: right-click a node ▸ Make controller, Restart, Shut down

### Added
- **Right-click a node's shape in the Topology** (this node or any reachable one) now also offers **Make controller…**, **Restart…** and **Shut down…**, next to Pause/Resume node and Host statistics. They are the Node page's buttons aimed at the node you clicked: *Make controller* asks that node to take the controller role for all groups (the node that held it steps down afterwards); *Restart* and *Shut down* act on the host, now, after a confirmation that says what happens (and that you lose the page when it is this node). When a gateway would lose its last serving member the daemon refuses, and the dialog then offers "… anyway". Another node is reached through the cluster relay, with the same permissions and logging as the Node page; this node is called directly. A node that does not answer keeps only its Cluster page and removal entries. The help page for Topology lists the new items.
- Scheduling (in N minutes / at a time) and cancelling stay on the Node page.

### Verified
- Live, headless Chromium, a real daemon with PAM: the menu on a node shows Host statistics, Make controller…, Pause node…, Restart…, Shut down…, Remove from this gateway…; Restart… shows its confirmation and Cancel sends nothing; Make controller… confirmed sends `POST /api/assert-agc` to this node directly (not through the relay) and shows the answer under the title. `node --check`, `gofmt -l .`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles.

### Not verified
- Restart and Shut down on a real host (the confirmation and the request were checked; nothing was powered off), and the relay path to another node in a live cluster (the same relay the Pause item has used since earlier versions).

## [v247] - 2026-10-08 — Stopping the DNS proxy cannot hang; a steadier test suite; what changed since v237

### Fixed
- **`DNSFrontend.Stop()` no longer waits without limit** for queries still being answered. The sockets are closed and the context cancelled first, so queries end on their own; if one is stuck in an upstream call, Stop gives up after 3 s, logs a warning and leaves it to finish. A gateway restart (`RestartDNS`) holds the engine lock around Stop, so a stuck query could have blocked the engine. Test: `TestFrontendStopDoesNotWaitForeverForAQuery`.
- **A test that failed now and then:** `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` sends a body over the limit, the server answers 413 and closes, and the client sometimes saw "connection reset" while still sending. That is the refusal seen from the other side; the test helper now accepts it for a body over the limit (a request that should succeed still must). 15 runs in a row pass.
- README read through: the introduction, Real-MAC mode (the default, when to turn it off), the cluster addressing paragraph, the answer cache's warm start and the cloud note now agree with each other and with the current behaviour.

### Upgrading from v237: everything that changed in v238–v247
- **Defaults.** A server is marked Down at **100 %** of its queries failing (was 50 %), a new gateway starts with **real MAC addresses on**. Existing gateways keep their setting; a saved configuration is read as before.
- **Answer cache.** It survives adding, pausing, resuming and removing a server (the pool is rebuilt, the cache kept). A node that starts asks a cluster member for its most recent answers (warm start: up to 20,000 entries / 3 MB, with their ages, never replacing its own).
- **Cluster.** Nodes are reached by **IP address first**, names last (a name lookup is limited to 2 s) and each node saves its peers' addresses, so pausing the DNS gateway on every node no longer turns the others "not answering" in the Topology (this was the cause of the reports on 8 October: the status calls waited on a name lookup served by the paused gateway). A gateway paused on all nodes reads **paused** on every node that answers. Pausing or resuming restarts engines outside the supervisor lock.
- **GUI.** Topology rows are spaced evenly and the picture shrinks when a domain is removed. Monitor ▸ Log: Refresh, Download and **tshoot** are on a second line, the capture is always included.
- **Troubleshooting.** tshoot bundles hold a dump of every goroutine; `kill -USR1 $(pidof ddgw)` writes it to the journal.
- **Nothing to do on upgrade:** no settings files change format, no setting needs to be changed by hand. A cluster updates through its own update mechanism; the nodes restart one after another as before.

### Not verified
- On the real cluster: a restart of one node while the gateway is paused on all nodes.

### Verified
- `gofmt -l .`, `node --check`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles (built to /dev/null; archive checked for binaries).

## [v246] - 2026-10-08 — New gateways start with "Use real MAC addresses" on

### Changed
- A gateway created now starts with real MAC addresses on: the first gateway of a fresh install, **Add gateway** in the GUI, `--canvas-add gateway`, and `--configure` (its question defaults to yes). Most networks need it (a VMware port group that is not promiscuous, a cloud with one MAC per interface, a switch with port security), and the setting is still shared by the cluster and can be switched off per gateway.
- **Gateways that already exist are not touched.** A gateway in a saved configuration or in the cluster's shared settings that does not say `real_macs` still reads as off, so an upgrade restarts nothing and moves no MAC. Switch an existing gateway over in its Edit form, or with `--canvas-set gateway --group N --real-macs on`.
- Help, the field's hint and the README say so.
- Test: `TestNewGatewaysStartWithRealMACs` (new gateways on, groups read from a file that does not say stay off).

### Not verified
- Real-MAC mode itself is unchanged and was not re-run on hardware here; its tests (`realmac_test.go`) pass.

### Verified
- `gofmt -l .`, `node --check`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles (built to /dev/null; archive checked for binaries).

## [v245] - 2026-10-08 — Each node remembers its peers' IP addresses

### Changed
- The member list a node saves (`cluster.json` state) already carried each peer's alternative addresses, but they came from the join and were only ever replaced by gossip. Now every successful status poll compares the addresses a peer reports about itself (IPv4, IPv6 global and unique local) with the saved ones and, when they differ, saves the new ones: the IP alternatives are replaced, host-name alternatives are kept. After a restart with DNS down (the gateway paused on every node) a node therefore still has each peer's addresses, and v244 tries them before any name. No new files or settings.
- Test: `TestLearnPeerAddrs`.

### Not verified
- On the real cluster. `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` fails now and then in this sandbox ("connection reset by peer": the server answers 413 and closes while the test is still writing a large body); it does so on unchanged trees too, and passed on rerun.

### Verified
- `gofmt -l .`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles (built to /dev/null; archive checked for binaries).

## [v244] - 2026-10-08 — Nodes talk to each other by IP address first, by name last

### Changed
- A call to a cluster peer now tries its IP addresses (IPv4, IPv6, as the peer reports them) before any host name; the address that worked last leads within its group, and a name that worked never jumps ahead of the addresses. Names are the fallback, so cluster traffic (status polls, settings sync, updates, relays, tshoot) no longer depends on DNS, which is often this cluster's own gateway. v243's 2 s lookup limit and last-known-address fallback stay for the case where only a name is known.
- A stale address (a node that changed its IP) costs one failed try, at most 6 s, before the next address; the peer's pinned certificate is still checked on every connection, so a wrong host is never accepted.
- Test: `TestAddrsForTriesIPsBeforeNames`.

### Not verified
- On the real four-node cluster; a pause of the gateway on all nodes there is the test.

### Verified
- `gofmt -l .`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles (built to /dev/null; archive checked for binaries).

## [v243] - 2026-10-08 — Pausing the DNS gateway on every node no longer makes the nodes "not answering"

### Fixed
- **Root cause of "not answering … context deadline exceeded" after pausing a gateway on all nodes.** Found in the goroutine dump and tshoot from the v242 test: ns2, ns3 and ns4 were healthy and idle (no stuck goroutine, cluster port accepting), and the daemon on ns1 was waiting inside the name lookup for `ns2`, `ns3`, `ns4`. Peers are reached by name, and the host's resolver is this very DNS gateway; paused on every node, nobody answers the lookup, so every cluster call from ns1 timed out before it connected. Nothing was hung; the v240/v241 "hang" on ns2 fits this too.
- A peer name is now looked up for at most 2 s, and if that fails the address it last resolved to is used. Cluster calls, status polls, updates and relays all go through the one dial function. Peers reached by IP are unchanged.
- Test: `TestPeerDialUsesLastKnownAddressWhenLookupHangs`.

### Notes
- Each peer is known by its main address (the name typed at join, e.g. `ns2:53854`) and by alternatives (host name, IPv4, IPv6) that it reports; a call tries the one that worked last, then the others. The IP alternatives already existed, but the name came first and its lookup used up the whole call time, so they were never reached. Now the lookup gives up after 2 s.
- The v242 change (engines stopped outside the supervisor lock) was a guess at this symptom; the dumps show the lock was not the problem. It is harmless and stays.
- A node that was restarted while name service was already down has no remembered address yet, so it still waits for the name until one lookup succeeds (a peer can be given by IP in the join to avoid that).

### Not verified
- On the real four-node cluster. Here: the dial function with a hanging resolver in a test.

### Verified
- `gofmt -l .`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles (built to /dev/null; archive checked for binaries).

## [v242] - 2026-10-08 — Pausing or resuming a gateway no longer holds the supervisor lock while engines shut down

### Fixed
- **Likely cause of nodes turning "not answering" (context deadline exceeded) after pausing a gateway on all nodes.** Pause and resume restart the gateway's engines. `Supervisor.Reload` stopped the old engines (resign packets, the 400 ms leave grace, address and sysctl clean-up with `ip` commands) while holding the supervisor's main lock, and the cluster status handler needs that lock. A slow or stuck stop therefore made the node stop answering cluster calls. The engines are now stopped after the lock is released, in parallel, and the restarted group is started afterwards.
- Test: `TestRestartStopsEnginesOutsideTheLock` (fails on v241, passes now): while a restart is stopping engines, `engineList()` must still return promptly.

### Not verified
- That this is the cause. No goroutine dump from a stuck node has been seen; the lock hold fits the symptom and is fixed either way. If ns2/ns3/ns4 still hang: before rebooting, take a tshoot from a healthy node and on the stuck one run `kill -USR1 $(pidof ddgw)` then `journalctl -u ddgw -n 3000 --no-pager`.
- No live clustered pause/resume loop was run for this release.

### Verified
- `gofmt -l .`, `go build`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles.

## [v241] - 2026-10-08 — A gateway paused on every node reads "paused" on every node that answers

### Fixed
- **Topology: with a gateway paused on all nodes, only the node you looked from read "paused"; the others read "not serving"** (grey and dashed, but with the wrong word and the tooltip "paused or not set up on that node"). The picture knows the pause is shared (it says "Paused on all nodes" on the gateway itself), so a node that answers and does not serve the gateway now reads **paused** with the same tooltip. A node that does not answer stays "not answering", a removed one "removed", a node paused as a whole keeps its own words, and a host-load warning (disk, memory, CPU) is kept in front of the text. A gateway paused on one node only still reads "not serving" on that node, as before.
- Test: `TestLabelPausedAll`. Live, two real clustered daemons (native amd64, stub upstream): after `--canvas-pause gateway --group 1 --scope all` both nodes' lines read "Paused on all nodes — none of them serves it until it is resumed".

### Not verified
- The earlier screenshot where ns2, ns3 and ns4 all read "not answering" right after pausing: the second try worked, so it may have been the nodes restarting for an update, or the hang seen on ns2 earlier. If it returns, the v240 goroutine dump (`kill -USR1`) on one of the stuck nodes shows what they are waiting on.
- Not touched: why ns2 hung on 8 October.

### Verified
- `gofmt -l .`, `go build`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles.

## [v240] - 2026-10-08 — Log page: Refresh, Download and tshoot on their own line; the capture is always taken; a goroutine dump for a daemon that is stuck

### Changed
- **Monitor ▸ Log:** Refresh and Download moved to a second line with the button, which is now called **tshoot**. The "with capture" tick box is gone: the 8 s ARP and neighbor-discovery capture is always part of the bundle (`GET /api/tshoot/download` takes it unless `capture=0` is passed; `ddgw --tshoot` always takes it, and `--tshoot-capture` is still accepted and does nothing).

### Added
- **A dump of every goroutine** in each node's bundle (`ddgw/goroutines.txt`), and `kill -USR1 $(pidof ddgw)` writes the same dump to standard error (`journalctl -u ddgw`) with a line in the log. It is for the case in the 8 October bundle: a node whose gateway engine still runs but whose cluster port stopped answering. Which lock or call everything is waiting on is in the dump; nothing in it is request or configuration data.

### Investigated (no fix)
- The report "no reply from the VIP after pausing and resuming the gateway": in the bundle ns1, ns3 and ns4 paused and resumed correctly, and ns1's VIP listeners came back (DNS proxy on the VIP, anycast addresses announced). **ns2 stopped answering cluster calls at 14:54:42 UTC, never saw the pause or the resume (still on settings revision 289, the others on 291), kept its gateway engine running, and was elected controller by the others after the resume.** The VIP is answered through the controller, so a controller that is stuck leaves it dead for everyone. What made ns2 stop is not in the bundle (a node that does not answer cannot be collected); a single-node pause and resume with a filled cache, an anycast address and cluster mode on could not be made to fail here.
- Rebooting ns2 alone cleared it, which confirms ns2 was the stuck node and the others were fine.
- Next time: take the bundle, then on the stuck node `kill -USR1 $(pidof ddgw)` and send `journalctl -u ddgw -n 2000`, or run `ddgw --tshoot` on it.

### Verified
- `TestTshootNodeBundle` now also expects `ddgw/goroutines.txt` with the test's own stack in it. Live, headless Chromium (dark and light), a real daemon with PAM: the Log page shows the filter row (search, level, range, lines, Live) and a second row with Refresh, Download and tshoot, no tick box; clicking tshoot downloaded a bundle that holds `capture/lo-arp-nd.pcap` and `ddgw/goroutines.txt`; `kill -USR1` printed 17 goroutines and the daemon kept running. `gofmt -l .`, `go build`, `go vet ./...`, `node --check`, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles.

### Not verified
- What stopped ns2. The cause could be in v239 (the cache carry-over or warm start) or older; nothing in the bundle points at either, and I did not find a lock path that could do it by reading the code.

## [v239] - 2026-10-08 — The answer cache survives a server change, and a restarted node fills it from a peer

### Changed
- **Adding, pausing, resuming or removing a server no longer empties the answer cache.** Any of those rebuilt the pool and the new pool started with an empty cache, although the cached answers do not depend on which server is asked next. The rebuilt pool now takes over the old cache when the cache settings are the same (`cache`, `cache_entries`, `cache_max_ttl` and the client-subnet settings). Changing one of those, or switching the cache off and on, still starts it empty, and that is now the documented way to clear it. Hit and miss counters carry over with it.
  - Trade-off: a server removed because it gave wrong answers leaves those answers in the cache until their TTL ends (at most `cache_max_ttl`, 3600 s by default). Switch the cache off and on to drop them at once.

### Added
- **Warm start from a peer.** A node that has just started (a restart, or the re-exec after an update) asks reachable cluster members for each gateway's most recently used cache entries, with the age they have there so their TTLs keep counting down, and loads the first useful answer. New signed cluster call `POST /cluster/cache` (older nodes answer 404, which is treated as "no answer"). Limits: 20,000 entries and 3 MB of messages per gateway, entries with under 5 s left are skipped, an answer is used only if the peer's cache settings match, and an entry this node already has is never replaced or pushed out (taken-over entries also sit at the cold end of the LRU order). It runs in the background at 4 s, then 12 s, 27 s and 57 s after start-up while no peer has anything to give, never delays a gateway starting to serve, and logs one line per gateway ("cache filled from … N of M entries in T ms"). Nothing is replicated afterwards.

### Verified
- New tests: `TestCacheSurvivesAPoolRebuild` (server added, removed; size, TTL limit, ECS prefix, ECS off, and cache off/on each start empty), `TestCacheExportAndLoad` (counts, byte cap, ages, TTL countdown after the transfer, existing entries kept, full shard, cold-end placement, rubbish refused), `TestWarmStartFromAPeer` (two real cluster nodes over the signed channel, mismatching settings, no cache). `gofmt -l .`, `go build`, `go vet ./...`, `node --check` on the web files, `go test -race -count=1 ./...`, cgo-off vet and test, five cross-compiles. One cgo-off test run failed once in the full suite and passed in the two runs after it; the failing test was not captured, and the known flaky `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` (connection reset) is the likely one.
- Live, two real daemons (native amd64 with PAM, a stub DNS upstream, a gateway on `lo`): 40 names queried through node A (40 entries, then 40 hits); node B joined, restarted and logged "cache filled from … 40 of 40 entries in 3 ms" about 6 s after start with 40 entries in its cache. Adding a second server to A's pool rebuilt the pool ("shared pool started — 2 server(s)") and the cache kept its 40 entries and counters; the repeated queries after it were hits (80 hits in all). A node that joins a cluster while running is not warmed (it only happens after a start); restarting it is what does it. The test users, group, PAM file and daemons were removed afterwards.

### Not verified
- Warm-start timing on a real network and with a cache near 20,000 entries (the sandbox has loopback only); the estimate is 50 to 200 ms for 10,000 entries.

## [v238] - 2026-10-07 — Topology: the spare height is shared evenly between the rows

### Changed
- **The Topology drawing spaces its rows evenly.** In a window taller than the drawing, the spare height used to go 40 % between the circle and the servers, 10 % under the servers and 50 % between the domains, so with two domains per server they ended up far apart (about 220 px) while the other gaps stayed small. The gaps (circle to servers, servers to the first domain, domain to domain) now grow together, smallest first, and come out equal; a gap that is already larger than the rest (a tall column of nodes or anycast pills beside the circle) keeps its size. With no spare height the layout is exactly what it was.

### Verified
- Live, native amd64 build with PAM, headless Chromium (dark): four servers with two domains each. In a 1000 px window the three gaps measured 165, 166 and 165 px, in a 560 px window 80, 79 and 78 px. `gofmt -l .`, `go build`, `go vet ./...`, `node --check webui/app.js`, cgo-off vet, five cross-compiles. `go test -race -count=1 ./...` passes except `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` ("connection reset by peer"), which fails the same way on the unchanged v237 tree in this sandbox. The test user, group and PAM file were removed afterwards.

### Not verified
- Windows narrower than the drawing (it is scaled down, the gaps follow), and drawings with a node/pill column taller than the servers' row were not re-measured.

## [v237] - 2026-10-07 — New default: a server is down only when every test fails

### Changed
- **"Down at (% of tests failing)" now defaults to 100** (was 50). With two or more test domains, one bad name can no longer take a healthy server out of the pool; a server is down only when all of its tests fail, after "Failures before down" (default 2, unchanged) rounds in a row. With a single test domain nothing changes (50 and 100 behave the same). Updated: the built-in default, the gateway load-balancing form's starting values, the help text, README, `ddgw.conf.example`, the `--help` text and the default-value test.
- **Existing installs keep what they have.** A saved configuration already contains `down_percent`, so only a configuration without the key (a new install, or a pool never set) picks up 100. Nothing is rewritten on update.

### Verified
- `gofmt -l .`, `go build`, `go vet ./...`, `node --check` on `app.js` and `help.js`, `go test -race -count=1 ./...` (passes); cgo-off vet and test; five cross-compiles.

### Not verified
- The shipped value was not tried on a real pool with a half-failing server beyond the existing `TestDownPercentOnARealPool` cases.

## [v236] - 2026-10-07 — Version bump to test the update path

### Changed
- No code change from v235. The release number is raised so the two-site cluster can be updated from v235 through the Upgrade page (upload, auto-update) rather than by running `install.sh` on every node, to see the v235 hold-back rule work.

### Verified
- Same source as v235 (the full suite, cgo-off vet and five cross-compiles were run for it); `go build` and `go vet` re-run for this archive.

## [v235] - 2026-10-07 — The update hold-back names the reason, and a member removed from a gateway no longer counts as its cover

### Fixed
- **v232 did not release the update on the two-site cluster** (both nodes on v232, "gateway 1 would have no other cluster member serving it", still behind). v232 relied on the other node reporting that it cannot take part in the gateway (another subnet); I had guessed that was the reason and it was not enough. The node now also works it out for itself from the shared setting every node holds: a member that has been removed from a gateway (Topology ▸ the node ▸ remove, `--canvas-del node`) can never serve it, whatever version it runs, so it is not waited for. A node also reports its own removal. Test: `TestUpdateSafeForTheOnlyMember` now removes the other member from the gateway and expects the update to go ahead.

### Changed
- **The hold-back says why, per member**, instead of a generic list of guesses: "gateway 1 would have no other cluster member serving it (node2:53854 cannot serve it (another subnet, or removed from the gateway))", or "… is not reachable", "… has only just started serving it", "… is not serving it (paused, still recovering, or its DNS servers are down)".

### Verified
- `gofmt -l .`, `go build`, `go vet ./...`, the safety and update tests; `go test -race -count=1 ./...` passes except the known flaky `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` (connection reset); cgo-off vet; five cross-compiles.

### Not verified
- That removal from the gateway is what holds your two nodes. If the reason shown after this update is something else (for example "is not serving it (…)"), that line says what to look at; the troubleshooting bundle (v234) holds the gateway and DNS state of both nodes.
- Until the nodes run v235 the old message is shown. To get there, press **Update this node now** (or `--update-apply --yes`) on the node once.

## [v234] - 2026-10-07 — Troubleshooting bundle

### Added
- **Monitor ▸ Log ▸ Troubleshooting bundle** (and `ddgw --tshoot [--all-nodes] [--tshoot-capture] [--tshoot-file F]`): one .tgz with a folder per node holding what is needed to find out why something does not work: the log (newest 20000 lines), the configuration and its saved versions, cluster, gateway, DNS-pool, BGP, anycast, virtual-MAC-test and update state, host numbers for the last hour, `ip addr/link/route (all tables)/rule/neigh`, `/proc/net` ARP and routes, sockets, the ARP/routing sysctl settings, nftables and iptables, FRR's BGP and BFD output and configuration, service status, the Anyname journal, the kernel log tail and system information. `with capture` adds 8 s of ARP and neighbor-discovery capture per gateway interface.
- The node you are signed in to asks every other node itself (the node picker does not matter), all at once; a node that cannot be reached, or that is on a version without the bundle, has a `NOT-COLLECTED.txt` and a line in the top `README.txt`. One node's part is kept under 3 MB (the cluster channel's limit) by cutting the oldest log lines. Read-only; each use is logged with the user's name.
- **Secrets are removed before anything is written**: every JSON value whose key is a password, the gateway key, a token, a join code, a private key, a cookie or similar, and the same words in free text (FRR's `password …`, `secret`, `md5`, `community`). Each node's `README.txt` says how many were removed. Other programs' command lines are not collected (the process list has names only). Packet captures and query statistics are not included unless asked for / at all.
- Tests: redaction of JSON and text, tar round trip (including a path that tries to leave the node's folder), one node's bundle, and a two-node cluster bundle including an unreachable node.

### Verified
- Live, real PAM (scratch user, group and PAM file, removed afterwards), one real daemon: `--tshoot` wrote a 17 KB bundle with 32 files and no leftover test password; the GUI button downloaded `ddgw-tshoot-<host>-<time>.tgz` in headless Chromium and showed its note; no page errors.
- `gofmt -l .`, `go build`, `go vet ./...`, `node --check`, `go test -race -count=1 ./...` (all passed this time), cgo-off vet, five cross-compiles.

### Not verified
- A real FRR (vtysh is not installed in the sandbox, so those files say so), nftables rules, and a real cluster of two machines (the cluster path is tested with two in-process nodes).
- The capture option live.

## [v233] - 2026-10-07 — Scroll bars stay put on every polling page; a shared anycast address is no longer shown as "gateway paused"

### Fixed
- **The scroll bar on Monitor ▸ Anycast ▸ Neighbors blinked and a bar you had moved snapped back after a second.** v213 fixed only Gateways, Cluster-members and DNS; every other page that refreshes itself still emptied its container and built all of it again, destroying the scroll box under the pointer. Now fixed in the one place all of them share: the redraw helper (`morph`) also carries over event handlers, form state (checked, value, disabled; a field being typed in is left alone), and `fill()` replaces `clear(x).append(…)`. Converted: Monitor ▸ Anycast, Operate ▸ Anycast, Cluster, Upgrade (tiles, Nodes and History), Users, Statistics (top lists, dynamic updates) and Host (tables). The Log and Capture pages append lines and were never rebuilt; the Topology drawing keeps its own box (v136).
- **Monitor ▸ Anycast showed an anycast address as "withdrawn — gateway paused" while the gateway serving it was running.** Since v231 one address can be on several gateways, but the list showed it once, as the first gateway had it, and the first gateway was paused on this node. It now shows the address as its best gateway has it (announced, else running and why not, else not running, else paused). Test: `TestAllAnycastStatesSharedAddress`.

### Verified
- Live, real PAM (scratch user, group and PAM file, removed afterwards), headless Chromium at 520 px so the tables scroll: after moving each page's scroll box and waiting past two polls, the same element was still there and still scrolled, on Monitor ▸ Anycast, Operate ▸ Anycast, Cluster, Upgrade and Host (Users has nothing to scroll at that width; Statistics had no table to move); no page errors; the Operate neighbor's right-click menu and the Upgrade buttons still work after the polls.
- `gofmt -l .`, `go build`, `go vet ./...`, `node --check`; `go test -race -count=1 ./...` passes except the known flaky `TestClusterLegacyRequestsAreLimitedBeforeTheSignature`; cgo-off vet; five cross-compiles.

### Not verified
- A real overlay-scroll-bar desktop (the flashing is most visible there); the fix is that the scroll boxes are no longer rebuilt, which the element-identity check shows.
- The Statistics and Host pages with their tables wider than the window.

## [v232] - 2026-10-07 — Updates no longer wait for a member that can never cover the gateway

### Fixed
- **A two-node cluster with one gateway per site never updated.** The rule that keeps every gateway served during an update waited for another member to cover each gateway the node serves. When every other member is on another subnet (the gateway's address does not exist there, so it waits "off-net" and can never serve it), the cover never comes: the node said "gateway 1 would have no other cluster member serving it" for ever and stayed "behind" (v228 fixed only the node with no peers at all). Members now report which gateways they cannot take part in (`offnet` in the gateway report); a gateway whose every other member is reachable and off-net no longer holds the update back. A member that is paused, recovering, down or unreachable still counts as a possible cover, so the wait stays where it protects clients.
- Tests: four new `TestSafeToTakeDown` cases (the only other member is off-net; one off-net and one recovering still waits; off-net for one gateway but not another still waits; an unreachable member still waits).

### Verified
- `gofmt -l .`, `go build`, `go vet ./...`, the safety, update, subnet and off-net tests; `go test -race -count=1 ./...` passes except the known flaky `TestClusterLegacyRequestsAreLimitedBeforeTheSignature`; cgo-off vet; five cross-compiles.

### Not verified
- On the real two-site cluster. The fix assumes the other node is held off-net for gateway 1 (it is on the other site's subnet); if instead it is paused, excluded from the gateway or still recovering, the hold-back stays by design.
- Mixed versions: a node still on v231 does not report `offnet`, so until both nodes run v232 the older one keeps being treated as a possible cover. The first update to v232 therefore needs "Update this node now" (or `--update-apply --yes`) on each node once.

## [v231] - 2026-10-07 — The same anycast address on several gateways

### Changed
- **An anycast address may now be on more than one gateway** (the normal anycast setup: both sites announce one address). Before, the GUI said "Gateway … already uses that address" and the config validation refused it. Still refused: an anycast address equal to a gateway's shared address, and the same address twice on one gateway.
- The gateways carrying an address share **one listener and one `lo` entry** on each node. The address is on `lo` (so the routing daemon announces it) while **any** of them has a DNS server answering, and it is answered from the first (lowest group number) that can. It is withdrawn only when none can: all unhealthy, paused (this node or all nodes) or stopped. Pausing the address on one gateway only takes that gateway's claim away.
- A gateway that cannot answer while another keeps the address up shows it: the pill stays announced and its tooltip says "kept up by <gateway>, because this gateway's DNS cannot answer here (…)" (`carried` in the API).
- Per-gateway query statistics for a shared address are recorded under the gateway that opened the listener.

### Verified
- New `TestAnycastSharedByTwoGateways` (both healthy; one down keeps it up and names the carrier; both unavailable withdraws; one stopping keeps it, the last stopping removes it); the validation test now expects the same address on two gateways to be accepted and a shared-address clash refused. `go test -race -count=1 ./...` passes except the known flaky `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` (connection reset; also flaky on v228); `gofmt`, `go vet`, `node --check`, cgo-off vet, five cross-compiles.

### Not verified
- Real BGP announcement from both gateways on the same node (the sandbox has no routing daemon); the lo and listener handling is tested with the replaceable add/delete hooks and a real loopback listener.
- The GUI form and tooltip in a browser.

## [v230] - 2026-10-07 — One blurb for the virtual-MAC warning

### Changed
- The gateway circle's tooltip said the virtual-MAC warning twice (once as the status line, once as "Virtual MACs: …"), each with both the IPv4 and IPv6 results. It is now one blurb: "Virtual MACs: NOT delivered on this network — IPv4: …; IPv6: …. Use real MAC addresses for this gateway." The separate line shows only when the gateway is not marked by it (delivered, inconclusive).

### Fixed
- After **Use real MAC addresses** and the gateway working, the circle and node stayed amber while the sidebar dot went green: the node kept its last "not delivered" test result and kept marking the gateway with it. A result now counts only while the gateway uses virtual MACs (`markVmac` skips gateways whose real-MAC option is on). Test: `TestMarkVmacIgnoresTheResultOnceRealMACsAreOn`.

### Verified
- `gofmt -l .`, `go build`, `go vet ./...`, `node --check`, `go test -race -count=1 ./...` passes except the known flaky `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` (connection reset; passes on rerun alone), cgo-off vet, five cross-compiles.

### Not verified
- The tooltip in a browser (JavaScript syntax-checked only).

## [v229] - 2026-10-07 — Test whether virtual MACs work on this network

### Added
- **Virtual-MAC test.** A gateway can look healthy (the router even holds its virtual MAC in the ARP table, because the announcements got out) while no client traffic reaches the node, because the network drops frames addressed to a virtual MAC (a VMware port group that is not promiscuous, a cloud with one MAC per interface). The test checks the return path: from a throwaway macvlan with a virtual MAC it asks the router (default gateway, else another neighbour) a question that changes no neighbour cache (an RFC 5227 ARP probe for IPv4; a neighbor solicitation from a throwaway link-local address for IPv6) and listens on that interface for the unicast answer. The same question from the real MAC is the control: a router that does not answer it gives **inconclusive**, never a false "not delivered". Verdicts: **delivered**, **not delivered**, **inconclusive**, and **off** (real MACs already on).
- It runs on request (right-click the gateway circle ▸ **Test virtual MACs…**, `ddgw --test-vmac [--group N]`, `POST /api/vmactest`, relayable through the node picker) and once, in the background, a few seconds after a gateway starts. The last result is in the circle's tooltip, and a "not delivered" turns a healthy gateway amber with the reason.
- **Apply the suggestion.** When the dialog says "not delivered" it offers **Use real MAC addresses**, which sets the shared option; the CLI is `ddgw --canvas-set gateway --group N --real-macs on|off`.
- Nothing is enabled automatically.
- Tests: parsers for /proc/net route, arp and ipv6_route, the ARP/NA reply matchers, the verdict logic, `markVmac`, `--real-macs`. `TestVmacLive` (skipped unless `DDGW_LIVE_VMAC=1` as root) runs the real test against a veth pair with a namespace as the router.

### Verified
- `TestVmacLive` run for real here, IPv4 path: open network gives delivered; the router's ARP replies to the virtual MAC dropped (nftables, arp family) gives not delivered; router interface down gives inconclusive; the throwaway interface is gone afterwards; real MACs already on gives off.
- `gofmt -l .`, `go build`, `go vet ./...`, `go test -race -count=1 ./...`, cgo-off vet and test, cross-compiles for linux/amd64, arm64, arm, 386, riscv64 (all passed; the race run took about 130 s).

### Not verified
- The IPv6 path live: the sandbox kernel has no IPv6 at all. It is built the same way and covered by the matcher tests only.
- A real VMware or cloud network: the sandbox cannot reproduce a non-promiscuous port group; the drop was simulated at the router side only.
- The GUI dialog and tooltip in a browser, and the relay of the test through the node picker, were not exercised (JavaScript syntax-checked only).

## [v228] - 2026-10-07 — A single node now updates itself

### Fixed
- **Auto-update (and Update now) never updated a node that was the only member.** With a newer release uploaded the Upgrade page said "behind" and stayed there, because the rule that keeps every gateway served during an update waited for another member to cover the node's gateways, and a lone node has none, so it waited for ever (the log said "holding back — gateway 1 would have no other cluster member serving it (this is the only member…)"). A node that knows no other member is now never held back; it builds, installs and restarts, which is a brief interruption of its gateways, as an update of one node must be. With other members known the rule is unchanged: a node still waits while it is the only one serving a gateway. Change: `clusternet.go` (`updateSafeToApply`: the update paths; a power action on the only serving node stays refused); README ("is the only member").
- Test: `TestUpdateSafeForTheOnlyMember` (a lone serving node is safe; after another member joins, the serving one waits again).

### Verified
- `gofmt -l .` clean; `go build`; `go vet ./...`; `go test -race -count=1 ./...`; `CGO_ENABLED=0 go vet ./...` and `go test ./...`; cross-compiles for linux/amd64, arm64, arm, 386 and riscv64.
- Live, one real daemon with a gateway and no peers, real build with PAM: before the fix, v226 with v228 uploaded stayed "behind" and logged the hold-back; with the fix (run twice), v227 built v228 and v228 built v229 about 30 s after the upload, installed it, restarted and reported "Running v228".

### Not verified
- Light theme (no GUI change); the GUI's Update now button for a lone node (the CLI path is the same `updateSafe`); a lone node under systemd (the live run re-executed itself without a supervisor). `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` is flaky (also on the unmodified v215 tree).

## [v227] - 2026-10-07 — A lone node is drawn beside its gateway

### Changed
- **The Topology drawing always shows the node.** On an install with one node (no cluster) the gateway's circle had nothing beside it, which read as "no node is attached" even while this node was serving it. Now this node is drawn to the left of the circle, starred, with the same colours, tooltip and right-click menu (Host statistics, Pause node, Remove from this gateway) as a node in a cluster; "Remove from this gateway" still refuses to leave the gateway with no node. In a cluster nothing changes. A node that is not on the gateway's subnet shows "not serving" there instead of an unexplained empty drawing. The sidebar's gateway colour is now worked out from this node too. Change: `clusternodes.go` (a lone node, or no cluster at all, is a list of one), help and README ("just this node when it is alone").
- Tests: `TestCanvasClusterNodes` and `TestMarkNodesOnCanvas` now expect the lone node (with and without a cluster object) instead of nothing.

### Verified
- `gofmt -l .` clean; `go build`; `go vet ./...`; `node --check` on `help.js`; `go test -race -count=1 ./...`; `CGO_ENABLED=0 go vet ./...` and `go test ./...`; cross-compiles for linux/amd64, arm64, arm, 386 and riscv64.
- Live, one daemon with no cluster, real PAM login, headless Chromium (dark): the gateway with no DNS server shows one starred node shape beside the circle; right-click lists Host statistics, Pause node and Remove from this gateway; Remove from this gateway answers "That would leave no node serving gateway…".

### Not verified
- Light theme; a lone node that is off the gateway's subnet (the "not serving" shape), which is covered by the existing node-label test but was not looked at in the browser. `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` is flaky (also on the unmodified v215 tree).

## [v226] - 2026-10-07 — The product is now called Anyname DNS Director

### Changed
- **New product name, "Anyname DNS Director"** (short form "Anyname"), replacing "ddgw" and the tagline "DNS Distributed Gateway" wherever the name is prose a person reads: the browser tab title, the sign-in page, the sidebar header, the no-JavaScript notice, text on the pages and in the help ("?") panels, `README.md`, `QUICKSTART.md` and the messages and comments of `install.sh`, `uninstall.sh` and `get.sh`.
- **Not renamed**: the command-line program and every flag (`ddgw --cluster-status`; each "Command line" block in the help), the binary, the `ddgw.service` unit, `ddgw-uninstall`, the `ddgw` group and PAM file, `/var/lib/ddgw` and the other paths, release and download file names (`ddgw_vN.tgz`, `ddgw-capture.tgz`), the repository and `get.sh` URL (`micush/ddgw`), the join-code prefix, the wire protocol, the self-signed certificate's subject, the command-line usage banner and `CLAUDE.md`. Older changelog entries keep the name they were written under.

### Verified
- Chromium, dark and light: the tab title, sign-in page and sidebar header read "Anyname DNS Director" (one line in the sidebar). `bash -n` on the three scripts; `install.sh --dry-run` and `--help` print the new name; `gofmt -l .` clean; `go build`; `go vet ./...`; `node --check` on `app.js` and `help.js`; `go test -race -count=1 ./...`; `CGO_ENABLED=0 go vet ./...` and `go test ./...`; cross-compiles for linux/amd64, arm64, arm, 386 and riscv64.
- Every changed line of the README, QUICKSTART, help and page text was read in the diff: only prose changed, no command, flag, path, file or service name.

### Not verified
- `shellcheck` is not installed here. `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` is flaky (also on the unmodified v215 tree).

## [v225] - 2026-10-07 — Packet capture: no "Not capturing." line under the box

### Changed
- The "Not capturing." line under the capture box is gone (the box already says it). While a capture is running or stopped, the status line (interface, filter, packets kept) still shows. Change: `webui/app.js`.

### Verified
- `gofmt -l .` clean; `go build`; `go vet ./...`; `node --check webui/app.js`; `go test -race -count=1 ./...`; `CGO_ENABLED=0 go vet ./...` and `go test ./...`; cross-compiles for linux/amd64, arm64, arm, 386 and riscv64.

### Not verified
- Not looked at in a browser (a one-string change). `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` is flaky (also on the unmodified v215 tree).

## [v224] - 2026-10-07 — Row buttons move to right-click; Gateways and DNS list every gateway

### Changed
- **Packet capture:** the filter-syntax line above the capture box and the "This captures on the node chosen in the Node menu…" note below it are gone (also the filter line on the all-nodes view); both are in the help. The status line under the box stays.
- **Cluster page:** the Remove button on each member row is gone; right-click a member (not this node) ▸ **Remove**, same confirmation as before.
- **Operate ▸ Anycast:** the Disable/Enable button on each neighbor row is gone; right-click a neighbor ▸ **Disable** / **Enable**, same confirmation. Help says so; no text was added to the pages. (`rowMenu` in `webui/app.js`: the Topology menu's look, Escape and arrow keys; rows take focus, so the keyboard menu key works.)
- **Monitor ▸ Gateways and ▸ DNS list every gateway, whichever node is picked.** A node only runs the gateways it serves, so a node that serves only one of two showed one. For a gateway the picked node does not run, the table (Gateways) or pool and its answering addresses (DNS) is now the one a serving node has, asked over the cluster channel (`gatewaysvia.go`, `dnsvia.go`; `?own=1` is a node's own view and prevents chains). Nothing is starred for such a gateway; the DNS query totals stay the picked node's own. If no node serves it, or the one asked does not answer within 4 s, it is left out as before.

### Verified
- New tests: `TestTakeGatewayRowsAddsTheOtherGroupsWithoutStars`, `TestAddDNSPoolsTakesTheOtherGatewaysPool`.
- Live, two clustered daemons with real PAM login, headless Chromium (dark): no Remove buttons on the Cluster page, right-click ▸ Remove asks and posts `{addr}`, no menu on this node's own row; no Disable/Enable buttons on the neighbors, right-click ▸ Disable (with its confirmation) posts `{peer, enabled:false}` and a disabled neighbor offers Enable (BGP data mocked: FRR is not in the sandbox); with node B removed from gateway 2 and running neither, B's `/api/gateways` and `/api/dns` list both gateways (via A) while `?own=1` lists none.
- `gofmt -l .` clean; `go build`; `go vet ./...`; `node --check webui/app.js`; `go test -race -count=1 ./...`; `CGO_ENABLED=0 go vet ./...` and `go test ./...`; cross-compiles for linux/amd64, arm64, arm, 386 and riscv64.

### Not verified
- Light theme; `TestBGPOperateAPIAndStatus` failed once in a full race run with a port-in-use error while live test daemons were still shutting down, and passed on rerun; the Gateways and DNS pages were checked through their API, not looked at in the browser. `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` is flaky (also on the unmodified v215 tree).

## [v223] - 2026-10-06 — Install the latest tagged version with one curl

### Added
- **`get.sh`, a web installer.** `curl -fsSL https://raw.githubusercontent.com/micush/ddgw/HEAD/get.sh | sudo bash` finds the newest GitHub release (or, if there are none, the highest `v<number>` tag), downloads that tag's source archive, checks it is a ddgw tree (`install.sh`, numeric `source/VERSION`) and runs its `install.sh`, so it installs or upgrades in place exactly as the tarball does. Arguments after `bash -s --` go to `install.sh`; `--version TAG` installs a given tag instead. The whole script runs inside one function called on its last line, so a download cut short runs nothing; tag names are checked before use; the temporary tree is removed on exit; `install.sh` keeps the terminal for its questions and `sudo`. Needs `curl` or `wget`, and `tar`. README and QUICKSTART show it. To publish a version, push a tag `v<VERSION>`.

### Verified
- `bash -n get.sh`; `gofmt`/Go code untouched this release.
- Against a local fake GitHub (`python3 -m http.server` serving the tags list and tag archives, via `DDGW_GET_API` / `DDGW_GET_HOST`) with `install.sh --dry-run`: with no release, the highest tag wins (v222 over v9, v221, v215 and a non-version tag); a release's `tag_name` wins over the tag list; `--version v215` pins; run as `cat get.sh | bash -s -- --dry-run`; a bad tag name, a missing tag and a repo with no tags each stop with a clear error; no temporary directory is left behind.

### Not verified
- Against the real github.com (the sandbox cannot reach it): the API and archive URLs are GitHub's documented ones, the layout assumes the repository root is the `ddgw/` directory of the tarball (`install.sh` at the top). The GitHub rate limit for unauthenticated API calls (60/hour per address) is not handled beyond the error message. `shellcheck` is not installed here. No real (non-dry-run) install through `get.sh`.

## [v222] - 2026-10-06 — The gateway you picked stays picked when you change nodes

### Fixed
- **Switching the Node menu made the Topology page jump to the first gateway.** Changing nodes reset the sidebar's topology state, so the gateway you had selected was forgotten and the first one was shown. The sidebar list is the signed-in node's, whichever node you drive, so the selection and list are now kept; only a pending action is cleared and the list is refreshed. Change: `webui/app.js` (`setTarget`).

### Verified
- `gofmt -l .` clean; `go build`; `go vet ./...`; `node --check` on `app.js` and `help.js`.
- Live, two clustered daemons with real PAM login, two gateways, headless Chromium: picked the second gateway, switched the Node menu to B and back to A; the selection stayed on the second gateway each time.

### Not verified
- Light theme; `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` is flaky (also on the pristine tree) and fails intermittently in the cgo-off run.

## [v221] - 2026-10-06 — The Topology drawing shows the nodes that serve the gateway, not the selected node

### Fixed
- **A node that does not serve a gateway drew it grey and empty.** With the Node menu on a node that does not run the gateway (another subnet, removed from it, paused there), the circle was dashed grey and the servers and domains showed nothing, though other nodes were serving the gateway and probing its servers. Now the circle, servers, domains, anycast addresses, serving-node count and uptime are those of a node that serves it: this node asks one (the nodes that serve it well first, then those serving it degraded, in address order) for its picture over the cluster channel and draws that. The circle's tooltip says which node ("As NODE sees it (it serves the gateway; this node does not): …"). The node shapes still say what each node, this one included, is doing. Nothing local colours that picture: this node's own pauses, removal and node pause are not applied to it. If no node serves the gateway, or the one asked does not answer within 4 s, you get the selected node's own view as before.
- How: `/api/canvas` fills in such a gateway from `GET /api/canvas?own=1` on the serving node (`canvasvia.go`; `own=1` is a node's own view and is what the sidebar and the node-to-node ask use, so they never chain); the data carries `via` / `via_addr`. `ddgw --canvas` (the status socket) is unchanged: a node's own view. Change: `canvasvia.go`, `canvas.go`, `web.go`, `webui/app.js`; help and README.

### Verified
- `gofmt -l .` clean; `go build`; `go vet ./...`; `go test -race -count=1 ./...` natively with the PAM headers; `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test ./...`; `node --check` on `app.js` and `help.js`; cross-compiles for linux/amd64, arm64, arm, 386 and riscv64 (cgo off, so the fail-closed PAM stub).
- New tests (`canvasvia_test.go`): which nodes are asked (only when this node is not serving, serving before degraded, never an unreachable, removed, paused or not-serving one) and that taking a serving node's picture leaves this node's own entry and the node list alone.
- Live, two real clustered daemons with real PAM login and a stub DNS server: with the gateway served on A, `/api/canvas` on B (its own view `warn`) returned A's picture (`ok`, A's server and latency, `via` A) and `?own=1` on B its own; with B removed from the gateway (its own view: paused, the server idle), the Topology page signed in to B, headless Chromium, showed the circle, server (0.3 ms · #1) and domain green with the "As … sees it" tooltip, and B's node entry unchanged.

### Not verified
- The case in the report itself (second-site nodes on another subnet): the sandbox has one host, so "does not serve" was a removed node and a node that could not bind the shared DNS port.
- The light theme, the jsdom smoke test, a serving node that is on an older version, and the failure path (the serving node not answering) beyond the unit tests of which nodes are asked.

## [v220] - 2026-10-06 — The sidebar's gateway dot is the cluster's, not the selected node's

### Fixed
- **The dot beside each gateway under Topology followed the node picked in the Node menu.** It was that node's own colour for the gateway, so with a node selected that does not run the gateway (another subnet, say), or one that was still warming up, a gateway that the cluster is serving fine showed grey, dashed or amber, and it changed as you changed the Node menu. It is now the gateway for the cluster as a whole, built from its nodes' own states and the same whichever node is asked: **green** while any node serves it, **amber** while it is only served degraded, **dashed grey** when every node has it paused, **grey** while the nodes are all still starting, **red** when no node serves it. A node removed from the gateway does not count; a node that is up but does not run it (another subnet) does not count against it while another serves it. The tooltip says how many ("Served by 1 of 2 nodes"). With no cluster it is the gateway's own colour, as before. The circle in the drawing is unchanged: it is still how the selected node sees the gateway.
- How: each node's entry for the gateway now carries its own state apart from the host-load colour (`gw`: ok, degraded, bad, starting, notserving, paused, down, removed), worked out the same way for this node and the others from the same numbers every node reports; `clusterGateway` adds them up into `cluster_status` / `cluster_detail` in the Topology data (`/api/canvas`, `ddgw --canvas` is unchanged). The sidebar always asks the node you signed in to, so the Node menu cannot change it. Change: `clusternodes.go`, `canvas.go`, `webui/app.js`; help and README.

### Verified
- `gofmt -l .` clean; `go build`; `go vet ./...`; `go test -race -count=1 ./...` natively with the PAM headers; `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test ./...` (the first run hit `bind: address already in use` on a random test port in `updates_test.go`; it passed on the rerun); `node --check` on `help.js` and `app.js`; cross-compiles for linux/amd64, arm64, arm, 386 and riscv64 (cgo off, so the fail-closed PAM stub).
- New tests (`clusterstatus_test.go`): the roll-up over 13 mixes of node states (including nodes on another subnet, degraded, paused, starting, removed, and an old node that reports nothing); what each reported state means; and two real clustered nodes, one serving and one not, where the cluster reads "Served by 1 of 2 nodes" whichever node is asked while the gateway's own colour differs.
- Live, two real clustered daemons with real PAM login and a stub DNS server on 127.0.0.2: the gateway on `lo` served on A and not on B (the DNS port is shared). `/api/canvas` from A says own status `ok`, from B `warn`; both say `cluster_status` `ok`, "Served by 1 of 2 nodes". In headless Chromium, with the Node menu on A and then on B (whose own circle is amber), the sidebar dot stayed `st-ok` with the same tooltip.
- `TestClusterGatewayStatsAddUpNodes` fails when run twice in one process (`-count=2`), also on the unmodified v215 tree; not related, not investigated.

### Not verified
- The red and dashed-grey cases live (only in the unit tests), the light theme, the jsdom smoke test, and a node that is not running a version with this change (an older node reports the same numbers, so it should roll up the same).

## [v219] - 2026-10-06 — A removed node is no longer drawn; Add node is on the gateway

### Changed
- **A node removed from a gateway is not drawn on that gateway** (on any node's Topology page), instead of showing a dashed grey "removed" shape. The page goes by the saved list, so the shape goes at once.
- **Adding a node back is on the gateway:** right-click the gateway ▸ **Add node** ▸ the node, by name (a node that has left the cluster appears as "A node that left the cluster (id)", so its entry can still be cleared). The item is there only while a node is removed. The node's own menu now offers only **Remove from this gateway…**; "Add to this gateway" is gone. Removing, the last-node refusal, the CLI (`--canvas-del|--canvas-add node`) and the sharing are as in v218; `ddgw --canvas` still lists a removed node, as *removed*. Help and README updated. Change: `webui/app.js`, `webui/help.js`.

### Verified
- `gofmt -l .` clean; `go build`; `go vet ./...`; `go test -race -count=1 ./...` natively with the PAM headers; `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test ./...`; `node --check` on `app.js` and `help.js`; cross-compiles for linux/amd64, arm64, arm, 386 and riscv64 (cgo off, so the fail-closed PAM stub). All passed this time, including `TestClusterLegacyRequestsAreLimitedBeforeTheSignature`, which is intermittent.
- Live, two real clustered daemons with real PAM login, headless Chromium (dark): with one node removed only the other is drawn; right-click the gateway shows **Add node** with the removed node's name in its submenu; clicking it draws the node again; removing the other from its menu takes its shape away at once and it stays away after a refresh, and **Add node** is offered again.

### Not verified
- Light theme for this change (the menu is the existing one), the jsdom smoke test, a mixed-version cluster during an update and traffic leaving a removed node on a real wire (as in v218).

## [v218] - 2026-10-06 — Choose which nodes serve which gateway

### Added
- **A shared list of the nodes that do not serve a gateway.** Every node serves every gateway, as before, until it is removed from one. Topology ▸ right-click a node ▸ **Remove from this gateway…** (asks first) / **Add to this gateway**; CLI `--canvas-del node --group N --node NODE` / `--canvas-add node --group N --node NODE`, NODE being the node's address (with or without the port) or host name (a host name two nodes share is refused: use the address). It works from any node, with the Node menu on any node, and is a shared setting, so every node follows, it survives restarts and it is in the configuration history ("excluded_nodes null → […]").
- **What a removed node does:** the gateway is carried out on it as if paused there (it resigns, gives up the address, stops answering and probing, anycast addresses are withdrawn) and only that gateway: its other gateways run. Its shape reads **removed** (dashed grey) on every node's drawing, and on the removed node the gateway's circle says "This node was removed from the gateway". A node that joins the cluster later serves every gateway until it is removed from it. This is separate from *Pause ▸ This node*, which is kept on the node itself for maintenance.
- **The last node serving a gateway cannot be removed** (CLI and GUI): the error says to pause the gateway on all nodes instead.
- Storage: `excluded_nodes` in the gateway's block, a list of node IDs (they survive an address change), sorted and de-duplicated, omitted while empty — so a configuration that never used it is byte-for-byte what it was and older versions still read it. It is part of the shared settings, so the hash of a cluster that never used it is unchanged. An entry for a node that has left the cluster can still be cleared by its node ID. A cluster of mixed versions during an update: a node still on v217 or older rejects the unknown key until it is updated, so update every node before removing one (the list is empty until then).
- Help (a new section on the Topology page and the CLI block), README (Topology, the CLI/GUI table), `--help`.
- Tests (`nodeset_test.go`): a removed node stops serving only that gateway and others and later nodes do not (and the flag is never written), the list is shared, sorted, validated and absent when empty (hash unchanged), add/remove/last-node/stale entries, finding a node by address, host name or ID, every node's drawing says "removed", the circle says why, and two real clustered nodes: removing B from A reaches B's file and B's effective configuration, not A's; the last node is refused; B adds itself back and A follows.

### Fixed
- Saving the Settings page rebuilds each gateway from its fields; it carries `excluded_nodes` through like the pauses (checked live: renaming a gateway there kept the list on both nodes).

### Verified
- `gofmt -l .` clean; `go build`; `go vet ./...`; `go test -race -count=1 ./...` natively with the PAM headers; `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test ./...`; `node --check` on `app.js` and `help.js`; cross-compiles for linux/amd64, arm64, arm, 386 and riscv64 (cgo off, so the fail-closed PAM stub).
- Live, two real clustered daemons on 127.0.0.1 (own state dirs, a throwaway PAM user in group `ddgw`, a gateway on a subnet the host is not on): the CLI refused a shared host name and an unknown node; `--canvas-del node` on A reached B (its log: "Group 1 is paused on this node — not starting", history "excluded_nodes null → […]"), A's and B's `--canvas` both read removed, removing the last node was refused, `--canvas-add node` from B reached A and the key left both files.
- Live in headless Chromium (dark and light), against the daemons above: right-click a node shows **Remove from this gateway…**, the confirmation names the node and the gateway, the shape becomes a dashed grey "removed", the removed node's menu offers **Add to this gateway**, the last-node refusal stays on the page (it is shown in the page's message area, not the line the next redraw clears), and renaming the gateway on the Settings page kept the list on both nodes.
- `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` fails intermittently with `connection reset by peer` (also on the unmodified v215 tree); not related, not investigated.

### Not verified
- A mixed-version cluster during an update (the unknown-key note above is from how older versions read the file, not a run).
- Taking traffic off a removed node on a real wire (the same release path as pausing a gateway, which was not changed; this sandbox has no second host).
- The jsdom smoke test of `app.js` (Chromium was used instead).
- The removed node's own shape on a node that is also over 85% disk/CPU/memory: it stays yellow with the numbers, as a paused node does.

## [v217] - 2026-10-06 — Capture on one node downloads as a .tgz, not a .pcap

### Changed
- **Monitor ▸ Capture, one node: the button is now "Download .tgz" and the .pcap download is gone.** The archive holds the one `.pcap` of the node's buffer (`ddgw-<host>-<interface>-<time>.tgz` containing `ddgw-<host>-<interface>-<time>.pcap`), the same way the all-nodes download already bundles its files. Through another node (the Node menu) it is still the newest packets that fit the peer channel (about 5 MB, taken before compression). The route `GET /api/capture/pcap` is removed and `GET /api/capture/download` replaces it (relayable like the rest of `/api/capture`). Help text and README updated. Change: `capops.go` (`handleCaptureDownload`), `webui/app.js`, `webui/help.js`.
- The command line is unchanged: `--capture --capture-file FILE.pcap` still writes a plain `.pcap` (and `.tgz` with `--all-nodes`).

### Verified
- `gofmt -l .` clean; `go build`; `go vet ./...`; `go test -race -count=1 ./...` natively with the PAM headers; `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test ./...`; `node --check` on `app.js` and `help.js`; cross-compiles for linux/amd64, arm64, arm, 386 and riscv64 (cgo off, so the fail-closed PAM stub).
- `TestCaptureAPI` now unpacks the download (gzip, one `.pcap` in it, readable, at least the three packets sent) and checks the old route no longer answers; `TestCaptureRelayRules` covers the new route.
- Live, real PAM: a scratch daemon on `127.0.0.1`, a throwaway user in group `ddgw`, login over HTTPS, a capture on `lo` with filter `udp`, five UDP packets sent, `GET /api/capture/download` gave `content-type: application/gzip`, a `.tgz` filename, and an archive holding one `.pcap` that extracted; `/api/capture/pcap` answered 404 and the download without a session 401. The user, group, PAM file and daemon were removed afterwards.
- `TestClusterLegacyRequestsAreLimitedBeforeTheSignature` fails intermittently with `connection reset by peer` (also on the unmodified v215 tree); not related and not investigated.

### Not verified
- The page in Chromium (light and dark) and the download through the Node menu to a second clustered daemon; the button text and the relay of the new route are covered by `node --check`, the relay-rules test and the code path being the same as the old one.
- The jsdom smoke test of `app.js`.

## [v216] - 2026-10-06 — A node on another subnet says "not serving" about itself, not "starting"

### Fixed
- **Topology: a node held back because it has no address in the gateway's subnet called itself "starting".** On a two-site setup (gateway `208.67.131.248`, second-site nodes on `91.221.255.x`) the second-site nodes correctly do not run that gateway (`onGatewaySubnet`, grey "not running here"), and the nodes on the gateway's subnet already showed them as amber "not serving". But the node's own entry took its label from the gateway's idle colour, and idle reads "starting" (the word for the DNS warm-up), so picking such a node in the Node menu showed a grey "starting" that never ended, while every other node said "not serving" about it. Its own entry now reads "not serving" in amber, the same as the others say, with its own reason ("not running here — …; members on that subnet serve it") as the tooltip. The gateway's circle stays grey and the DNS warm-up still reads "starting". Change: `clusternodes.go` (`canvasNodes`).
- Test: `TestCanvasSelfNodeSaysWhatThePeersSay` now also checks an off-subnet node ("not serving", warn) and a warming one ("starting").

### Verified
- `gofmt -l .` clean; `go build`; `go vet ./...`; `go test -race -count=1 ./...` natively with the PAM headers (`libpam0g-dev`); `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test ./...`; `node --check webui/app.js`; cross-compiles for linux/amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the fail-closed PAM stub).
- Everything passed except `TestClusterLegacyRequestsAreLimitedBeforeTheSignature`, which fails intermittently with `connection reset by peer` (2 of 3 runs on the unmodified v215 tree, so it is not caused by this change; not investigated).

### Not verified
- No live run: not a two-daemon cluster with a gateway on a subnet the host is not on, and not the Topology page in Chromium (light and dark). The change is the label and colour of one node entry, covered by the unit test only.
- The PAM login check, update push and other live checks do not apply (nothing in those paths changed).

## [v215] - 2026-10-06 — Real-MAC mode: a gateway setting for networks where virtual MACs cannot work

### Added
- **A gateway setting, "Use real MAC addresses (no virtual MACs)"** (Configure ▸ Settings ▸ Gateway groups; `real_macs` in the configuration; `--configure` asks for it). **Off by default, and then nothing is different from before.** Shared by the cluster; turning it on or off restarts the gateway on each node (it is a restart setting, not a live one). It is for networks that do not deliver frames addressed to a MAC that is not the NIC's own: a VMware port group with promiscuous mode off, a cloud that allows one MAC per interface.
- **What it does when on:** there is no macvlan and no slot MAC. Every node, the controller too, holds the VIP on `lo` and has `arp_ignore=1` / `arp_announce=2` on the real interface (saved and put back when the gateway stops). The controller still answers ARP and IPv6 neighbor solicitations for the VIP and still picks the node by the load-balancing method, but **the answer names the real MAC of that node**, sent from the controller's own MAC: for its own slot, and for a node whose MAC it does not know yet, it names its own (it holds the VIP too, so that is always right). A node that expired is not named.
- **How the controller knows the real MACs, with no change to the gateway protocol** (so nodes of any version take part): it learns a node's MAC from an ARP the node sends and from any IPv6 frame from the node's link-local address (the hellos, for one), only for nodes of the group, and never from a multicast or empty source; and it asks, with an ARP request or a neighbor solicitation of its own, for the ones it has not heard (every 5 seconds until it knows, every minute after).
- **What replaces taking a MAC over:** when a node dies or leaves, the controller announces the VIP at its own MAC with an unsolicited ARP (IPv4) or neighbor advertisement (IPv6), at once and twice more within a second, and again when it becomes the controller. Neighbors that honor one repoint; the others keep sending to the dead node until their ARP entry ages out. This is the cost of the mode, and it is why it is not the default.
- **The Gateways page** (`--show-gateways`) shows real MAC addresses in the vMAC column in this mode: the controller knows all of them, a forwarder only its own; the others show nothing.
- Help text (the setting and what it costs), README (a "Real-MAC mode" section), `--help`.
- Tests (`realmac_test.go`): the answer names the virtual MAC by default and the real MAC in the new mode (own slot, unknown MAC, learned MAC, expired node, IPv6 option), what is learned and what is ignored (an address outside the group, a probe, a multicast or empty MAC), what the controller asks for and how often (IPv4 and IPv6, never as a forwarder or by default), no macvlan and no `lo` removal in the new mode and the announcements it makes, the default setup of virtual MACs unchanged, the setting in the file (not written when off), as a restart setting, and shared across the cluster (and not in the shared form when off, so a cluster's hash does not change), and the real MAC on the Gateways page. A test was checked to fail when the answer is made to always name the controller's own MAC.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check` on `app.js` and `help.js`.
- Live, a daemon with the setting on and a DNS server that answers: it won the election, logged `Real-MAC mode: slot 1, VIP on lo, no virtual MAC`, set no macvlan, started the ARP responder on the real interface, showed its real MAC in `--show-gateways`, set `arp_ignore=1` and `arp_announce=2` on `eth0` while running and put them back to 0 and 0 when stopped. In Chromium the setting is a checkbox on Settings ▸ Gateway groups.

### Not verified
- **No ARP exchange was seen on a real network.** The sandbox's one interface does not hand a frame sent by one socket to another, so a real ARP request could not be sent to the daemon; the answer is checked by the tests on the frames the responder builds, not on a wire. The VIP could not be added to `lo` either (the sandbox has no `ip` command), so that step is checked by the commands the tests record.
- **Failover was not tried on any network**, and how a given firewall or router treats an unsolicited ARP is not known here: that is the thing to test (stop a forwarder, watch whether the firewall's ARP entry for the VIP moves). Nothing was run with more than one node in this mode, so the learning of the other nodes' MACs from real frames, and the controller's requests being answered, were tested on made-up frames only.
- A cluster with some nodes on an older version cannot use the setting (an older node would build virtual MACs); turn it on when every node runs v215 or later.
- Everything listed as not verified or not changed under v194 to v214 still applies.

## [v214] - 2026-10-06 — The cluster-wide capture works on nodes with several addresses; nodes no longer advertise the VIPs as their own

### Fixed
- **Monitor ▸ Capture on every node failed on nodes that have more than one address.** A node is asked at each address it is known by in turn, and an address that has not answered in 6 seconds is given up for the next (so a dead address does not hold things up). The capture asked each node to hold one request open for the whole capture, so with a capture longer than 6 seconds every address "timed out", the same request was sent again to the next address, and the peer started a capture for each of them. Now the capture is **started with one quick request and then asked for** (`POST /api/capture/job`, `GET /api/capture/job?id=…`, each answered at once), every node's result is fetched when it is done, and the page shows each node's state as it goes. A start is identified by the job's id: the same id again is the same capture, so a request the peer channel sends twice starts only one. A node holds at most four timed captures (a finished one waits 10 minutes to be fetched). The old blocking `/api/capture/run` web endpoint is gone (the command line's `--capture` on this node still waits, since it asks only this node).
- **A node advertised its gateways' VIPs and anycast addresses as addresses it can be reached on.** A node tells the others every address of its interfaces (its "alternative addresses", also put in a join code), and that included the VIPs (on the `ddgwN.M` interface) and the anycast addresses (on `lo`), which every node answers on. A peer that tried one of them reached whichever node held it at that moment and was refused there for its certificate (`x509: certificate signed by unknown authority`, as in `208.67.131.248:53854` in the failure list). The list now leaves out every gateway's VIPs and anycast addresses, the loopback interface, the virtual-MAC interfaces and link-local addresses. A node still on an older version keeps advertising them until it is updated.

### Added
- Tests: a capture of 8 seconds on two cluster nodes with several addresses each (more than the 6 seconds one address is given), that both finish and that each node starts exactly one capture; a start with the same id twice is one capture, ids and the limit on held captures are checked, a missing interface is refused when the capture starts, and the job is fetched over HTTP; the advertised addresses with the VIPs on a physical interface, on `ddgwN.M` and on `lo`; and that the shared addresses are read from the gateways in any spelling.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check` on `app.js` and `help.js`.
- Live with real PAM and two daemons joined into a cluster: `ddgw --capture lo --capture-seconds 8 --all-nodes` (longer than 6 seconds) finished on both nodes with the same packets on each, wrote the `.tgz` with a `.pcap` per node, and each node logged exactly one capture start.

### Not verified
- Not on your clusters: the nodes of the test cluster share one machine and have few addresses, so the failure with IPv6 "no route to host" and "permission denied" addresses across sites was reproduced only by the test that gives each node a second address, not with the real mix of addresses and a firewall between sites.
- The Palo Alto problem itself (VIP traffic from the firewall not reaching the nodes) is not addressed by this release; it is a different question that needs a capture from the nodes that failed above, now possible.
- Everything listed as not verified or not changed under v194 to v213 still applies.

## [v213] - 2026-10-06 — Monitor ▸ Gateways, Cluster and DNS: the horizontal scroll bars no longer flash or jump back

### Fixed
- **The scroll bars on Monitor ▸ Gateways, Cluster and DNS flashed on every refresh, and a bar that had been moved sprang back at once.** Each of the three pages, on every poll, emptied its container and built every card and table again, which destroys the scroll boxes in it: the new box starts at the left and its bar is drawn afresh (the flash). It was always so, but the tables used to fit their cards, so nobody could see it; the columns added in v209 and v210 made them wider than the card, and the bars appeared. The pages now build the new markup aside and **patch it into the page in place** (a small `morph`): the cards, scroll boxes and tables stay the same elements and only the text and attributes that changed are updated, so the bar neither flashes nor moves, and a text selection survives a refresh too.
- **The tables are less likely to need a bar at all.** A node's name in the Gateways and Cluster tables is cut at 10 rem with an ellipsis (the whole name is the tooltip) instead of widening the table, those two tables use a little less space between columns, and the DNS table is 62 rem wide at least instead of 71 (its columns are narrower, the name is still cut with an ellipsis).

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check` on `app.js` and `help.js`.
- Live with real PAM (scratch user, group and PAM file, removed afterwards), two clustered daemons, headless Chromium (dark), with very long node names and IPv6 link-local addresses: in a 900 px window each of the three tables was scrolled sideways (150 px), left alone for 8.5 s (several refreshes), and each time the scroll box was **the same element** (tagged before), at the same position, and had received **no scroll event**, while the data in the table still changed (the age went from 102 to 106). The tables fit their cards, with no bar, at 1920, 1500 and 1366 px.

### Not verified
- At 1280 px the Gateways table with IPv6 rows and names that long is still 61 px too wide, and at 1100 px all three need a bar (it then scrolls, and keeps its position). Firefox and a desktop with overlay scroll bars (the flashing is most visible there) were not tried; the fix does not touch the scroll box on a refresh, so no bar should be shown or moved, but I could not see that.
- The other pages that rebuild their contents on a timer (Statistics, Host, Log, the Topology drawing) were not changed; they have no scroll box that is rebuilt as these did, apart from the Statistics tables, which were not looked at.
- Everything listed as not verified or not changed under v194 to v212 still applies.

## [v212] - 2026-10-06 — Monitor ▸ Capture: packet captures on one node or on every node at once

### Added
- **Monitor ▸ Capture** (`--capture`, `--capture-interfaces`): a packet capture, modelled on the one in gravinet. It listens on a raw socket on one interface (no libpcap; it only listens, nothing is sent), keeps the newest 5000 packets or 32 MB, lists them live with one-line summaries, and saves the buffer as a standard `.pcap`. The Node menu picks the node: through the cluster relay any member can be captured on from any node's GUI (`/api/capture` can now be relayed; a download through another node is the newest part that fits about 5 MB). A node has one capture of its own on its Capture page, and starting another replaces it.
- **Capture on every node** (the Node menu's **Cluster (N)** entry, now also on the Capture page; `--all-nodes`): the same capture on all nodes at the same moment for 5, 10, 30 or 60 seconds (the API takes 1 to 60), each into a private buffer (so no node's own Capture page is disturbed; about 4 MB each, the newest packets), bundled into one `.tgz`: a `.pcap` per node (this node's ends in `-this-node`), `summary.txt`, and `errors.txt` for nodes that could not capture, with the reason; a node on a version without capture is said to need updating, and the others still make the bundle. One such capture runs at a time; the page lists each node's result, packets and size.
- **Summaries that help with the virtual MACs and DNS:** TCP flags; DNS names, types and answers (`DNS A? example.com`, `DNS NXDOMAIN 0 ans`), over UDP and TCP; ARP and IPv6 neighbor discovery with the target and the MAC announced and the Ethernet addresses the frame was sent from and to (`ARP, 192.0.2.5 is-at 00:1a:7c:01:02:00 (eth 02:… > …)`): whether the MAC in an ARP reply is the one on the frame shows at once.
- **A filter** (not in gravinet): a small tcpdump-like language evaluated in the daemon before a packet is kept: `host`, `src host`, `dst host`, `net`, `port`, `src port`, `dst port`, `portrange`, `tcp`, `udp`, `icmp`, `icmp6`, `arp`, `ip`, `ip6`, `dns`, `ether host|src|dst MAC`, with `and`, `or`, `not` and brackets, a protocol before host/net/port narrowing it. Errors are reported when the capture starts. It runs in the daemon and not in the kernel, so a busy interface still delivers every packet to it.
- On the loopback each packet is kept once (it is delivered twice, sent and received; tcpdump keeps one).
- Every start is logged with the user's name, the interface and the filter. Operations `capture.*`; web `GET /api/capture/interfaces`, `POST /api/capture/start|stop|clear|run`, `GET /api/capture/packets|pcap`, and the not-relayable `POST /api/clustercapture/start`, `GET /api/clustercapture/status|download`.
- Help, README (a Capture section and the CLI-to-GUI table) and `--help` describe it.
- Tests: `capture_test.go` (summaries of DNS over UDP and TCP, TCP flags, ARP, NDP, ICMP, VLAN, bare IP and short frames; the filter against sixteen frames with 30 expressions and 14 mistakes, fragments and truncated frames that must not panic; the buffer's limits, the epoch and the filter's counts; the `.pcap` written and read back, with a limit and a cut-off file; **real captures on the loopback**, filtered and timed; the HTTP endpoints; the relay rules; the capture on two real cluster nodes through the relay, a node that cannot answer, a missing interface, and the refusals).

### Changed
- The cluster fan-out used by cluster Statistics and Host (`clusterGather`) now runs every node, this one included, at the same time, and can send any request (a POST with a body) with a longer time allowed, which the cluster capture needs. Its answers are unchanged; the existing tests pass.
- `--all-nodes` is accepted with `--capture` as well as `--stats` and `--host`.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check` on `app.js` and `help.js`.
- Live, with real PAM (scratch user, group and PAM file, removed afterwards) and two daemons joined into a cluster: `--capture-interfaces`; `--capture lo` with a filter printed the packets (3 kept of 54 seen); `--capture-file` wrote a valid `.pcap` (header, records, ends on a record boundary, read back with Python); `--all-nodes` wrote a `.tgz` with a `.pcap` per node and `summary.txt`; the refusals (no file with `--all-nodes`, an unknown interface, a bad filter) read clearly. In headless Chromium (dark) the Capture page offered the interfaces (the gateway's marked), refused a bad filter with its message, listed five live packets, showed the kept and seen counts, downloaded the `.pcap` (five records) and, after Cluster (2) was picked, ran the all-nodes capture, listed both nodes as done and downloaded the `.tgz`; the Cluster entry went back to this node on leaving the page, and a clean all-nodes run shows no notice.

### Not verified
- Captures were on the loopback only (the sandbox's one real interface carries no traffic); a gateway's interface with real DNS traffic, ARP, IPv6 neighbor discovery, VLAN-tagged frames and the `ddgwN.M` interfaces are covered by the summary and filter tests on made-up frames, not captured live. A very busy interface (the daemon reads every packet to filter it) was not tried.
- Through the relay only about 4 to 5 MB per node can be carried; a larger capture is cut to its newest packets (the page and the help say so), and a capture of many megabytes through another node's menu was not tried. A node on an older version cannot capture and says so; mixed versions were not run.
- The light theme of the page, and the dialog-less flows for a node reached through the Node menu on a real second machine (both test nodes share one host).
- Everything listed as not verified or not changed under v194 to v211 still applies.

## [v211] - 2026-10-05 — DNS page: long server names are cut short

### Fixed
- **Monitor ▸ DNS: a long server name ran into the Server IP column.** The table has fixed column widths, and a long fully qualified name has nowhere to break, so it was drawn over the next column. The Server name column is now wider (13 rem, and the table 5 rem wider), and a name that still does not fit is cut with an ellipsis at the end, so the host part, which tells the servers apart, stays in view. The whole name is the cell's tooltip.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check webui/app.js`.
- Live with real PAM (scratch user, group and PAM file, removed afterwards), headless Chromium (dark) with eight servers with long fully qualified names: in a 1500 px and in a 700 px window no name reaches the Server IP column; the long ones are cut with an ellipsis and carry the full name as a tooltip.

### Not verified
- The light theme. The Node name columns of Monitor ▸ Gateways and Monitor ▸ Cluster were not changed: those tables size their columns to the content, so a long name widens the column instead of overlapping.
- Everything listed as not verified or not changed under v194 to v210 still applies.

## [v210] - 2026-10-05 — Gateways: the node name comes first and carries the star; DNS: the server name comes first

### Changed
- **Monitor ▸ Gateways:** the columns are now **Node name**, **Node IP**, Pri, … (name first). The ★ that marks this node is after the node's name, not after the address.
- **Monitor ▸ DNS:** the columns are now Rank, **Server name**, **Server IP**, State, … (the name before the address). The table's column widths follow.
- The command line follows: `--show-gateways` lists `NODE NAME` (with the `*` that marks this node) before `NODE IP`, and `--show-dns` lists `SERVER NAME` before `SERVER IP`. The help text of both pages says the same.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check` on `app.js` and `help.js`.
- Live with real PAM (scratch user, group and PAM file, removed afterwards), headless Chromium (dark): the Gateways table reads `Node name | Node IP | Pri | …` with `ns1 ★` and the address without a star (rows injected into the answer, as for v209), the DNS table reads `Rank | Server name | Server IP | State | …`; `--show-gateways` and `--show-dns` printed the new order.

### Not verified
- The light theme and a window narrow enough to scroll the Gateways table sideways were not looked at.
- Everything listed as not verified or not changed under v194 to v209 still applies.

## [v209] - 2026-10-05 — Node names and addresses in the Gateways, Cluster and DNS tables

### Changed
- **Monitor ▸ Gateways:** the first column is now **Node IP**, followed by a new **Node name** column with the name of the node that has the address (its host name). A node's name is found from the cluster: each node now also reports the addresses it uses in the gateway protocol (the IPv4 address and the IPv6 link-local address of each group), and its interface addresses fill in what those do not cover. A row whose node the cluster cannot name shows `–`: a node that is not in the cluster, or one still on an older version that does not report its addresses (its IPv4 address is still matched through its interface addresses, which versions from v190 send; its IPv6 link-local one is not). Two nodes with one host name are told apart by their cluster address.
- **Monitor ▸ Cluster:** the first column is now **Node name** (the host name; the cluster address, such as `ns1:53854`, is its tooltip, and the name when a node reports none), followed by a new **Node IP** column with the node's IPv4 and IPv6 addresses, one per line: its IPv4 addresses, then IPv6 global, then IPv6 unique local (fc00::/7), without prefix lengths. Operate ▸ Cluster keeps its single **Node** column (the cluster address), since it is where nodes are removed by that address.
- **Monitor ▸ DNS:** the first column is now **Server IP** (the server as configured), followed by a new **Server name** column with the name given to the server on the Topology page (right-click the server ▸ Edit server…, `server_names` in the configuration, per gateway or shared); `–` for a server without a name. The table's column widths follow.
- **Command line:** `--show-gateways` has `NODE IP` and `NODE NAME`, `--show-dns` has `SERVER IP` and `SERVER NAME`, and `--cluster-status` has `NODE NAME`, `NODE IP` and `ADDRESS` (the cluster address, as the first column was).
- Interface addresses now also list the **unique local IPv6 addresses** (`ula`), kept apart from the global ones: the Topology tooltip still shows only IPv4 and global IPv6 addresses (an interface that has only unique local ones is not listed there), while the Node IP column shows all three.
- Help text for the three pages describes the columns.

### Added
- New cluster-status fields (all optional, so nodes on older versions are unaffected): `gw_ips` (the node's gateway-protocol addresses) in the status message and the cluster view, and `ips` (its addresses) in the cluster view; `name` on the members of `/api/gateways` and on the servers of `/api/dns`; `names` (address to node name) in the status socket's `snapshot` answer.
- `namecolumns_test.go`: address lists with unique local addresses kept apart, the lookup of a node by address (gateway address before interface address, link-local IPv6, a node that does not report its gateway addresses, two nodes with one host name, the first node to claim an address keeps it), the names on the rows, two real cluster nodes telling each other their addresses, and the DNS page's servers carrying their names for the shared pool and a gateway's own.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check` on `app.js` and `help.js`.
- Live with real PAM (scratch user, group and PAM file, removed afterwards): two daemons joined into a cluster with a running gateway on eth0 and eight named DNS servers, headless Chromium (dark): the Cluster table has `Node name | Node IP | Role | …` with the address, the DNS table has `Rank | Server IP | Server name | State | …` with `ns1` to `ns4` (and its column widths hold with the down servers' long errors), the Gateways table has `Node IP | Node name | Pri | …`; `/api/cluster`, `/api/dns` and `/api/gateways` and the three CLI tables printed the new fields.

### Not verified
- The Gateways page was looked at with rows injected into the answer (the sandbox is one machine with one address, so every real row belongs to the same node); the lookup by address that fills the names is covered by the unit tests and by the API check with two real nodes sharing an address, not with several machines.
- IPv6 link-local gateway addresses are matched through what each node reports, so on a cluster with nodes still on v208 or older a node's IPv6 row shows `–` until that node is updated. The Operate ▸ Cluster table and the Topology tooltip were not changed.
- Everything listed as not verified or not changed under v194 to v208 still applies.

## [v208] - 2026-10-05 — Gateway controller hand-over without interruption: in-place step-down, make before break, repeated announcements

### Changed
- **A controller that steps down no longer takes its virtual MAC and its DNS down.** Giving up the role (the "Make this node the gateway controller" button on another node, a higher-ranked controller appearing, a coup) used to delete this node's macvlan (its own virtual MAC, with the VIP on it), close its DNS listener, and build all of it again as a forwarder: for that moment the node answered nothing on the MAC that clients which resolved the VIP to it were sending to, and those clients lost packets. Now the step-down is **in place**: the ARP/NS responder stops, the VIP goes onto `lo` **before** it comes off the macvlan (so the node answers for it at every instant), and the macvlan, the MACs the node covered for other nodes and the DNS listener stay up. A MAC the node no longer needs (slot 1, which belongs to the controller, or one it covered) stays up a while longer, twice the hold time but between 2 and 10 seconds, and is then released (also at once when the engine stops). A MAC on two nodes for that long costs nothing, since either can answer for the VIP; a MAC on none drops packets.
- **"Make this node the gateway controller" is make before break.** The node takes the role first (VIP on its macvlan, ARP/NS answered, announced) and takes over slot 1's MAC if the incumbent held it; only then is the incumbent asked to step down. It used to ask first.
- **A MAC that a node takes over is announced again and again for about two seconds** (ten more announcements, at 0.1 to 2.2 s), not once and then every two seconds. The node handing a MAC over keeps answering on it until it has gone, and each frame it sends from that MAC can move the switch's entry back to its port; one announcement at the start was undone that way, and nothing put it right until the periodic one. This applies to every take-over: a controller that stops, a forwarder that stops, a node that dies.
- **A controller that stops (restart, update, Pause this node) says so twice**, as a forwarder already did: one lost multicast would leave the others to find out after the hold time, by which time the leaving controller (400 ms grace) has gone.
- A controller that dies is still noticed after the hold time. That part is not changed, and a shorter hello and hold time (Settings ▸ Gateway) is what shortens it.
- README, the Node page help and the confirmation text no longer say clients "may notice a brief interruption".

### Added
- `handover_test.go`: the order of the commands is checked with a hook (`cmdHook`) that records them: the VIP onto `lo` before it leaves the macvlan and no macvlan removed on a step-down; slot 1's MAC kept at first and released after the grace; the asking node's VIP up and slot 1 covered before the resign request is sent; the burst of announcements and that it stops when the MAC is let go; the resign sent twice. The earlier `assert_agc_test.go` mesh (which now also records the packets) still passes.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check` on `app.js` and `help.js`.

### Not verified
- **No packet loss was measured, and nothing ran on a real network.** The sandbox has no `ip`, macvlan or switch: the changes follow from reading the code, and the tests check the order of the commands and packets the engines would issue, not what a switch or a client does with them. Whether the loss you saw is gone, and whether any is left, has to be measured on the cluster (a client pinging or querying the VIP every 10 to 50 ms while the controller is moved, stopped and paused).
- A controller that fails (power, crash) is not helped, except by the announcements after the take-over.
- A client's ARP entry that points to the old controller's MAC is not changed by any of this; the point is that the MAC keeps being answered.
- Everything listed as not verified or not changed under v194 to v207 still applies.

## [v207] - 2026-10-05 — Cluster view: no blue bar, and the menu entry reads "Cluster (N)"

### Changed
- **The blue "The whole cluster, added together: ns1, ns2, …" bar above the tiles on Statistics and Host is gone.** The Node menu already says it. The line comes back, in amber, only when a node could not answer, and then names it and says why (`Not included in these numbers: ns3 (not reachable)`), since the numbers are then missing that node's share.
- **The menu entry is `Cluster (N)`** (N is the number of nodes), instead of `Cluster · all N nodes added together`.
- The Statistics and Host help say the same. The command line is unchanged: `--stats --all-nodes` and `--host --all-nodes` still print which nodes were added, on their first line.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check` on `app.js` and `help.js`.
- Live with real PAM (scratch user, group and PAM file, removed afterwards), headless Chromium: with two clustered daemons up, the menu reads `Cluster (2)` on Statistics and Host and the page has no notice; after one daemon was stopped, both pages showed the amber `Not included in these numbers: 127.0.0.1:53864 (not reachable)`.

### Not verified
- Everything listed as not verified or not changed under v194 to v206 still applies.

## [v206] - 2026-10-05 — Monitor ▸ Statistics and Host: a "Cluster" entry in the Node menu adds the nodes together

### Added
- **"Cluster · all N nodes added together" in the Node menu, on Monitor ▸ Statistics and Monitor ▸ Host only** (it is the menu's last entry there, and not offered on any other page; choosing another page goes back to this node). The node you are logged in to asks every reachable node (itself directly, the others through the same cluster relay the Node menu already uses) over one absolute time range and adds the answers; a line above the tiles names the nodes added and, in amber, any that could not answer and why. The other pages' requests, and the dynamic-updates list on Statistics ("the last N of this node"), still go to this node. The cluster entry is not the page of "another node", so the amber frame is not drawn.
- **Statistics, added together:** each line of the chart point by point (the nodes align their points to the same step; a node that answers with another time resolution is left out and named), all totals and the cache hit and miss counts, the record-type and transport donuts, and the Top clients and Top domains by name (the top 100 of the sum, "(others)" last, a client's reverse-DNS names kept). The **Clients** tile cannot be a sum (a client that asked two nodes is one client): it is the larger of the busiest node's count and the clients seen in the nodes' lists, a lower bound. "Counting since" is when the last node began; the memory guard's drops are added.
- **Host, added together:** network rates, load averages, memory and swap sizes, disk sizes and cores are **summed**; percentages are those of the whole cluster taken as one machine: CPU weighted by cores (a 12-core node counts three times a 4-core one), memory and each filesystem by size, disk-busy the mean. A peak is the busiest node's (CPU, disk) and, for network rates, the sum of the nodes' peaks (an upper bound, since they need not coincide). The filesystem table sums sizes per mount and says `(several)` when the devices differ; the interface table lists every node's interfaces under the node's name (the address when two nodes share a host name).
- **Command line:** `--stats --all-nodes` and `--host --all-nodes` (the same as the menu entry); `--all-nodes` alone is refused. They name the nodes added, and any that were not.
- API: `GET /api/clusterstats` and `GET /api/clusterhost` (same arguments as `/api/qstats` and `/api/host`; the answers have the same shape plus a `cluster` object listing the nodes). They ask the other nodes themselves, so they cannot be relayed through `/api/proxy` (that is tested). Daemon operations `qstats.cluster` and `host.cluster`.
- Help (Statistics, Host), `--help` and the README (including the CLI-to-GUI table) describe it.
- Tests: `clusterstats_test.go`: the merge of statistics (series aligned by time, not by index, sums, lists, ties, the 100-row limit, another resolution left out), the merge of host load (every kind of quantity above, a missing point, a node with no sample yet), the whole path through two real cluster nodes and the relay (the sum is twice a node's here, since both nodes of the test share one process; a node that cannot answer is named and the rest still added), no cluster, node naming, and that the endpoints are not relayable. The merge test was checked to fail when the series alignment is broken.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check` on `app.js` and `help.js`.
- Live with real PAM (scratch user, group and PAM file, removed afterwards): two daemons joined into a cluster; the API answers `clusterstats` and `clusterhost` for both nodes (cores and memory twice one node's), a relayed `/api/clusterstats` is refused, `--stats --all-nodes` and `--host --all-nodes` print the note and the numbers, and in headless Chromium the Node menu offers "Cluster" on Statistics and Host and not on Node, picking it sends `/api/clusterstats` and `/api/clusterhost`, the page names the nodes, the choice stays from Statistics to Host and goes back to this node on another page.

### Not verified
- Not on nodes with different loads, memory sizes or histories: the sandbox's two nodes share one machine and the daemons had just started, so the charts were empty and only the tiles and tables showed numbers. The weighting is checked by the unit tests with made-up figures, not against real nodes. The light theme and the Statistics page's tiles and top lists in cluster mode were not looked at.
- With one node of the cluster slow or down the page waits up to 12 seconds for it before showing the others; a node on a version older than this one answers (the relay and the two endpoints it asks are old), so mixed versions work, but that was not tried.
- Everything listed as not verified or not changed under v194 to v205 still applies.

## [v205] - 2026-10-05 — "Make this node the gateway controller" works when the node ranks lower

### Fixed
- **Operate ▸ Node ▸ "Make this node the gateway controller" (and `ddgw --assert-agc`) did nothing unless this node outranked the sitting controller.** It told the controller to resign and then ran an election on this node, which a node ranking below the controller (equal priority and a smaller address, as in a group of nodes at priority 100) lost, so it stayed a forwarder; and the controller, on the request, stepped down and ran its own election, which it won again, since it was the highest of the group (a brief bounce of the role and nothing else). The IPv6 table could show the controller as FORWARD for a moment while this happened. Now:
  - this node takes the role at once (no election), and the sitting controller, on the request, steps down **in this node's favour**, with no election of its own;
  - until the old controller stops saying it is the controller, this node does not give way to it (a controller outranking it would otherwise take the role straight back) and asks again with each of its hellos, for three hold times at least three seconds, so a lost request does not undo it;
  - the other nodes follow the new controller's hellos; those that still have the old controller's last hello with the flag set ignore the new one until the old controller's next hello says it is no longer one (a hello interval).
  The role stays with this node afterwards, until something that changes the group changes it (a restart, the controller leaving, a node with preemption that outranks it). Priority and preemption in Settings are still the way to prefer a node permanently.
- **The button said nothing.** The page now shows what the daemon did, for each group and address family, for example `group 1 v4: asserting AGC (was AFN, asked 192.0.2.4 to step down)`; `already AGC — no change` on the controller itself. The same text is printed by `--assert-agc`.

### Added
- `assert_agc_test.go`: an in-memory mesh of three engines (the controller with the greatest address, a forwarder, and the node that asks, with the smallest) delivering each other's packets. Tests: the request from a node that ranks lower, shown failing before the fix (the controller stayed ACTIVE) and passing after, and stable through six more rounds of hellos; a lost request; the request on the controller itself changes nothing.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check webui/app.js`.

### Not verified
- Not on real nodes: the hand-over is exercised only by the in-memory mesh (IPv4 engines, hellos sent by hand), with no ARP/NS responder, virtual MAC or client in it; nothing here shows what clients see during the hand-over (the confirmation still warns of a brief interruption). IPv6 uses the same engine code but has no test of its own. The new message on the page was not looked at in a browser.
- Everything listed as not verified or not changed under v194 to v204 still applies.

## [v204] - 2026-10-05 — This node's tooltip says what the other nodes' tooltips say

### Changed
- **The tooltip of this node's parallelogram on Topology now has the same lines, in the same words, as another node's.** Before, the first line of this node's tooltip was the gateway's own status (`ns1 (this node) — IPv4: running and answering; IPv6: running and answering`) while another node's said `ns2 — Serving this gateway`, and this node had no `Last seen` line. Now, when the gateway is healthy on this node, the first line is `ns1 (this node) — Serving this gateway`, and there is a `Last seen: just now` line where the others show how long ago the node was last heard from. The rest was already the same: the address, the interface addresses, the role, the version, the settings and the host load. In any other state of this node (degraded, down, starting, paused, or the node itself paused) the first line is still the gateway's own reason on this node, which says more than the other nodes' wording would. The words `(this node)` stay in the name.

### Added
- Test `TestCanvasSelfNodeSaysWhatThePeersSay`; the strain test now expects the new wording after "Over 85%: …".

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check webui/app.js`.
- Live with real PAM (scratch user, group and PAM file, removed afterwards): two daemons joined into a cluster, headless Chromium; the tooltip texts of this node and the other node read from the page have the same lines in the same order (name and status, address, interface address, role, last seen, version, settings, host).

### Not verified
- The healthy wording ("Serving this gateway" for this node) comes from the unit test only: the test gateway was paused, so the live tooltips showed the paused texts. Not looked at as rendered tooltips.
- Everything listed as not verified or not changed under v194 to v203 still applies.

## [v203] - 2026-10-05 — The gateway tooltip: a short summary, and how many nodes serve it

### Changed
- **The gateway circle's tooltip on Topology is shorter.** It is now: the gateway's name; how long it has been up (or down) with its failures; the state of IPv4 and of IPv6; and **`Serving nodes: N`** (the count, or `none yet`). The list of the nodes' addresses and slots under it is gone (each node's own tooltip says what that node is), and the first line is no longer the whole status repeated (`Gateway X — IPv4: …; IPv6: …`). The overall status line is still shown, on its own line under the name, when it says more than the two family lines do: a pause (`Paused on all nodes — …`, `This node is paused …`), a gateway with a single address family, a start still waiting for the DNS servers. Nothing in the tooltip changed for the nodes' parallelograms, servers, domains or anycast pills.

### Correction
- The v199 entry said a veth shows up with `DEVTYPE=veth`. It does not: the kernel's veth driver sets no device type (checked in `drivers/net/veth.c`), which is why a container's `eth0` (type 1, virtual, no device type: the output from ns1) was not listed by v199 to v201. What lists it is the v202 fallback to the node's other interfaces; `veth` stays in the list of recognised device types, harmlessly.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check webui/app.js`.
- Live with real PAM (scratch user, group and PAM file, removed afterwards), headless Chromium: the circle's tooltip text read from the page for a dual-stack gateway whose canvas answer (and config) was rewritten to look healthy, with an uptime and eight serving nodes: `Gateway 127.0.0.9` / `Online for 4m 25s - 0 failures` / `IPv4 — running and answering` / `IPv6 — running and answering` / `Serving nodes: 8`. Also a degraded family (no repeated line), a single-family gateway (`running and answering` under the name, no family lines) and a paused one (the pause line, then the two family lines, `Serving nodes: none yet`).

### Not verified
- Not on a real healthy gateway: the canvas answer was rewritten in the browser to produce the healthy case (the test gateway was paused), so the real daemon's text for the other states was not seen; the tooltip was not looked at as a rendered tooltip, only its text read from the page.
- Everything listed as not verified or not changed under v194 to v202 still applies.

## [v202] - 2026-10-05 — Node tooltips list addresses even for interface kinds not known

### Changed
- **A node's tooltip falls back to the node's other interfaces.** The addresses go in the tooltip of the node's parallelogram on Topology (not in the gateway circle's; its "Serving nodes" list is the older v16 feature and is unchanged). They are the IPv4 and IPv6 global unicast addresses of the node's Ethernet interfaces (physical, bridge, bond, VLAN, veth: see v199). When none of those holds a usable address, the tooltip now lists the addresses of every other interface that is not the loopback and not ddgw's own `ddgwN.M`, instead of nothing. Link-local, ULA and 169.254 addresses are still left out, and an interface with no usable address is not listed.

### Added
- Test `TestEthernetAddrsFallsBackToOtherInterfaces`: the fallback lists an unknown kind of interface and keeps the loopback and ddgw's own out, and an Ethernet interface with a usable address still wins (the existing test).

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub).
- Live with real PAM (scratch user, group and PAM file, removed afterwards): two daemons joined into a cluster, headless Chromium; the tooltip text of both node parallelograms read from the page contains the line `eth0: 192.0.2.2/24` (this node and the other node, which sends it in its status message).

### Not verified
- Not run on the nodes where the tooltip was empty (containers, probably): their interface kinds were never seen here. If a tooltip is still empty there, the node reports no interface with an IPv4 address or a global IPv6 address at all, or the other nodes run a version older than v190 (which do not send the addresses).
- Everything listed as not verified or not changed under v194 to v201 still applies.

## [v201] - 2026-10-05 — The node picker stays put when the page scrolls

### Fixed
- **Zooming the Topology drawing in could push the node picker off the page.** The picker (the "Node" menu at the top right, shown when the cluster has two or more nodes) was part of the scrolling content, so wheel-zooming in, which scrolls the content, carried it away; the "?" button, which is fixed to the window, stayed. The picker is now sticky at the top of the content box: it stays where it was drawn (beside the "?" in a wide window, under it in a narrow one) however far the page is scrolled, on every page that shows it. The empty space of the bar lets clicks and the wheel through to what is under it, and the word "Node" has the page background behind it so it stays readable over the drawing.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check webui/app.js`.
- Live with real PAM (scratch user, group and PAM file, removed afterwards): two daemons joined into a cluster, headless Chromium (dark), the drawing zoomed in with the wheel until the content had scrolled 633 px (a 1900 px wide window) and 704 px (1300 px wide): the picker stayed at the same place (top 10 px beside the "?", and 48 px under it), was the element under its own centre (clickable), the empty part of the bar passed pointer events to the drawing, and after zooming out it was still in place.

### Not verified
- The light theme, a window narrower than 760 px (where the page itself scrolls, not the content box), the other pages' scrolling (Statistics, Log, …), and picking another node while scrolled.
- Everything listed as not verified or not changed under v194 to v200 still applies.

## [v200] - 2026-10-05 — Topology: the drawing sits on the page, without the lighter box

### Changed
- **No box around the Topology drawing.** The shapes are drawn straight on the page background; the lighter card (fill, border, rounded corners and padding) that framed them is gone. The legend is as before under the drawing. The drawing is still a scrolling box when it is wider than the window, and wheel zoom and drag-to-pan are unchanged.
- **The lines between shapes are darker** (the muted text colour at 40% instead of the border colour). The border colour was meant to be seen on the card's fill and is nearly invisible on the page itself in the light theme. Spread (active) lines and down lines keep their colours at full strength.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check webui/app.js`.
- Live with real PAM (scratch user, group and PAM file, removed afterwards), headless Chromium at 1380×900, **dark and light**: the drawing on the page background with no card, lines visible in both themes; the wheel-zoom test of v198 again (layout height unchanged through zooming in, polls, zooming out).

### Not verified
- `TestUpdateOverTCP` failed once in a `CGO_ENABLED=0` run ("address already in use": the test finds a free port and binds it afterwards, so another test can take it in between). It passed five times alone and in a full rerun; the test was not changed.
- Working, degraded and down shapes and the blue spread lines were not looked at on the page without the box (the test gateway was paused); no cluster nodes column; the "No gateway yet" message and the forms were not looked at after the change.
- Everything listed as not verified or not changed under v194 to v199 still applies.

## [v199] - 2026-10-05 — Node tooltips: a container's (veth) and VLAN interfaces are listed

### Fixed
- **A node's tooltip on Topology showed no interface addresses on a node whose network card is a virtual link.** The tooltip (added in v190) lists the node's *Ethernet* interfaces with their IPv4 and IPv6 global unicast addresses, and "Ethernet" meant a physical device, a bridge or a bond. A container's own `eth0` is a **veth** (that is what a Proxmox or LXC container has), and a VLAN interface (`eth0.5`) is virtual too; both were left out, so a node with nothing else listed got no address lines at all. Now an interface counts when it is ARPHRD_ETHER and a physical device, or a virtual link whose `DEVTYPE` is `veth`, `vlan`, `bridge` or `bond` (a bridge or bond is also still recognised by its sysfs directory). Still left out: ddgw's own `ddgwN.M` macvlans, other macvlans, vxlan, tap, dummy, tunnels and the loopback. The host's side of a container's veth has no address, so it is not listed anyway. An interface is listed only if it holds an IPv4 address or an IPv6 GUA (`2000::/3`), as before.
- This is the likeliest cause of the missing lines, inferred from the nodes being containers; it was not confirmed on those nodes. To see what a node has: `for i in /sys/class/net/*; do echo "$(basename $i) type=$(cat $i/type) $(grep DEVTYPE $i/uevent) virtual=$([ -e /sys/devices/virtual/net/$(basename $i) ] && echo yes || echo no)"; done`. An interface of another kind that holds an address is still not shown.

### Added
- Test `TestIsEthernetIfaceKinds` (`bgp_operate_test.go`) against a made-up `/sys/class/net`: physical, veth, bridge, bond, VLAN, bridge and bond by directory, macvlan, ddgw's own, anything named `ddgw*`, vxlan, tap, dummy, tunnel, loopback and a missing interface. The sysfs locations are now variables (`sysClassNet`, `sysVirtualNet`) so a test can point them elsewhere.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub).
- Live, native amd64 build with PAM: two daemons joined into a cluster, a real PAM sign-in over HTTPS, and `/api/canvas` returned `addrs` for both nodes (`eth0`, `192.0.2.2/24`, a physical device here). The user, group and PAM file were removed afterwards.

### Not verified
- No veth, VLAN or container interface was available in the sandbox, so those kinds are checked only against the made-up sysfs tree, not on a real container; and the tooltip was not looked at in a browser.
- Everything listed as not verified or not changed under v194 to v198 still applies.

## [v198] - 2026-10-05 — Topology: zooming no longer changes the layout

### Fixed
- **Zooming the Topology drawing with the wheel made it jump to another layout.** v197 spread the rows apart to use a tall window, but switched that off while the drawing was zoomed, so the next redraw (every poll) put the compact layout back: scrolling in or out left the drawing with its rows close together and the lines at other angles. The stretch no longer depends on the zoom. It was also measured from where the card was on the *screen*, and zooming in scrolls the page (the `.content` box), which changed the amount again; it is now measured from where the card is on the page. Zooming now only scales the drawing.
- Reproduced before the fix in headless Chromium against a real daemon (1380×900, wheel zoom in, a few polls): the drawing's height in its own units went from 812 to 406 and stayed there after zooming out. After the fix it stays 812 through zooming in, five seconds of polls, zooming out and five more seconds.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check webui/app.js`.
- Live with real PAM, as for v197 (scratch user, group and PAM file, removed afterwards): the wheel-zoom test above, v197 against v198.

### Not verified
- Light theme; a gateway with a node column or several rows of domains per server; a window resize while zoomed in; dragging in the new spacing by hand. The gateway was paused.
- Everything listed as not verified or not changed under v194 to v197 still applies.

## [v197] - 2026-10-05 — Topology: a taller drawing when the window has the room

### Changed
- **The Topology drawing uses the height of the window.** It was only ever scaled to fit the window's *width* (never larger than drawn), so in a tall window the card ended well above the bottom of the page. When the window is taller than the drawing at the size it will be shown, the rows are now spread apart to fill it: more space between the circle and the DNS servers (two fifths of the extra, four fifths when each server has one domain or none), a little between a server and its first domain, and the rest between the domains. The extra height is at most the drawing's own height (twice as tall), so a very tall window leaves some room, and nothing is stretched sideways; shapes keep their size. In a window that is not taller than the drawing the layout is exactly what it was. It does not apply while the drawing is zoomed with the mouse wheel, and a layout that would run a server's line through a node or an anycast pill is not used. The window is measured on every redraw, and the drawing is redrawn 200 ms after a window resize settles.
- Dragging a domain to another place in its column uses the new spacing.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check webui/app.js`.
- Live, native amd64 build with PAM: a real daemon with a scratch PAM user and group (`/etc/pam.d/ddgw` from `contrib/`), a gateway paused on `lo` with eight DNS servers, two domains each and three anycast addresses, driven in headless Chromium (dark) at 1380×900, 1920×1080 and 1000×560 (the sign-in over HTTPS worked with the new cookie name). In 1380×900 the drawing went from 285 to 571 px high; resizing the window to 600 and then 1100 px high redrew it to 505 and 571 px; in the 560 px window it fills the height and does not run past it. The user, group and PAM file were removed afterwards.

### Not verified
- Light theme, and a drawing with a node column or several rows of domains per server (the live gateway had three anycast pills, no cluster, two domains a server); dragging a domain or a server in the new spacing was not tried by hand; the gateway was paused, so no server was shown working, degraded or down.
- Not run: several daemons, a cluster, an update between versions.
- Everything listed as not verified or not changed under v194 to v196 still applies.

## [v196] - 2026-10-05 — Gateways and Neighbors show each peer's state

### Changed
- **A peer's STATE is what its hellos say, not "active" for everyone.** The row of another node used to say `active` whenever a hello had arrived within the hold time (and `expired` otherwise), so a forwarder looked different from the others only on the one row that showed its real state, the local one: on the controller's page the forwarders all said ACTIVE, on a forwarder's page its own row said FORWARD. Now, for both IPv4 and IPv6 (`Gateways`, `--show-gateways`, `--show-neighbors`, `/api/gateways`, `/api/neighbors`): **active** for the controller (its hello carries the controller flag, set only while it is ACTIVE), **forward** for a node that holds a forwarder slot, **standby** for a node with no slot yet, **expired** for one not heard from within the hold time. The local row is unchanged (its exact state).
- The wire format is unchanged, and the hello carries no state field, so this is inferred from the controller flag and the slot: a peer in the middle of an election (listen, speak) shows as `standby`, and a controller from before the flag existed (slot 1) shows as `active`, as the election already treats it. The Gateways help text says so.
- The GUI colours the new words as before (`active` and `forward` green, `expired` red, anything else amber). Nothing else reads a peer row's state as "alive": the topology drawing, the cluster's gateway status and the role column use other fields or only the local row.

### Added
- Tests in `engine_test.go`: `TestSnapshotShowsPeerStates` (controller, forwarder, no slot, expired forwarder, expired controller) and `TestSnapshotPeerStatesFromHellos` (from real hello packets).

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub), `node --check webui/help.js`. Only the native amd64 build was built with PAM headers.

### Not verified
- No live run: not on real nodes (the multi-node election on a real wire is not exercised here), and the Gateways page was not looked at in a browser.
- Everything listed as not verified or not changed under v194 and v195 still applies.

## [v195] - 2026-10-05 — Fewer allocations: statistics, DoH requests, cluster nonces

### Changed
- **`qstats.apply` no longer allocates per query.** The text of a client address (`netip.Addr.String()` allocated every time) is now made once and reused from a table of at most 4096 addresses, emptied when full so a flood of distinct addresses cannot grow it. The two-element slice literal the function also built per event was already on the stack (the benchmark showed one allocation, the string); it is unchanged. `BenchmarkQStatsApply`: 211 ns and 1 allocation became about 195 ns and 0.
- **DoH requests are read without `io.ReadAll`.** A POST body that fits a pooled buffer (2 KB, the one UDP queries use) is read into it, and a longer one, or one of unknown length, is read on after it, still held to 65535 bytes (over that is 400, as before). A GET's `dns` parameter is found in the raw query string (no map of every parameter is built) and decoded into a pooled buffer. `BenchmarkDoHBody`: POST 118 ns and 584 B (3 allocations) became 44 ns, GET 305 ns and 3 allocations became 141 ns and none (the one allocation shown for POST is the benchmark's own reader).
- **The cluster nonce table is swept for expired entries at most once a second**, or at once above 50,000 entries, not on every peer request (the sweep visits every entry). An entry that has expired but is not yet swept does not count as a replay; the timestamp check already refuses a request that old, and a replay inside the window is still refused.

### Added
- Tests in `perf195_test.go`: address strings reused and bounded, counts unchanged, DoH POST bodies of every size (announced, unknown length, past the pooled buffer, oversized), GET parameter forms (padding, escaped padding, other parameters first, the first `dns` wins, missing, empty, not base64), `dohQueryParam` against `url.ParseQuery`, 400 DoH requests answered each with its own question under concurrency, the nonce sweep (not per request, on time, above the limit) and that a replay is still refused; benchmarks `BenchmarkQStatsApply`, `BenchmarkDoHBody`.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub). Only the native amd64 build was built with PAM headers.

### Not verified
- No live run: no real DoH client (browser, curl, a stub resolver) was pointed at the listener, only the handler was driven in-process; no daemons, no real PAM, no browser.
- The figures above are micro-benchmarks of the changed functions on one machine, not throughput measurements of a running daemon.
- Everything listed as not verified or not changed under v194 still applies (open-resolver defaults, all-interface listening, shared cluster secret, unsigned update sources).

## [v194] - 2026-10-05 — Stream connection limits, cluster body checked after the signature, minimum password length, `__Host-` cookie, sessions end with the account

### Security
- **TCP and DNS over TLS: a connection could cost 64 KB for doing nothing, and there was no limit on connections.** A client announced a message length (up to 65535) and the whole buffer was allocated before a byte of the message arrived. The message is now read into a pooled buffer when it is small (up to 2 KB) and in pieces of 4 KB otherwise, so memory follows what has been received. A message shorter than a DNS header (12 bytes) closes the connection. TCP and DoT together are limited to 8192 connections and 256 per client (an IPv4 address or an IPv6 /64; the node itself is not limited per client); a connection over a limit is closed at once. Fixed constants, no new settings.
- **The cluster port read up to 8 MB before it checked the signature.** A request now carries the SHA-256 of its body in `X-Ddgw-Body`, which the signature (unchanged in format) already covered. The signature is checked first and the body is read only after, then held to that hash (a body other than the signed one is refused). A request without the header (a peer older than v194, which cannot be checked before its body is read) may carry at most 64 KB unless its TLS client certificate is a known member's, in which case up to 8 MB as before. So until every node runs v194, a node on v192 (no certificate) cannot relay an upload larger than 64 KB through a v194 node; v193 and v194 nodes are not affected.
- **The session cookie is now `__Host-ddgw_session`**, so a browser accepts it only if it is Secure, has Path=/ and no Domain. Everyone is asked to sign in again once after the update (the old cookie is not read).
- **A password change did not end the user's open sessions.** Changing a user's password, deleting the user, or setting an expiry date that has already passed now signs that user out of every session on the node, including one on the node where the change is made and, in a cluster, on every node that applies it. A future expiry date does not end a session. The person who changes their own password is signed out as well.

### Added
- **Setting `web.min_password_length`** (Settings ▸ Web GUI, "Minimum password length"): the fewest characters of a password set on the Users page or with `--user-add` / `--user-password`. 1–128; empty or 0 is the default, **8** (before there was no minimum). It is not written to the file when unset, so a config that never set it stays readable by older versions. Existing passwords are not checked. Help text and `--help` updated.
- Tests: `hardening2_test.go` (password length and its config, sessions ending for each kind of account change and for changes arriving from another node, the cookie rules, the stream limits and the incremental read, a short message, the body hash and the legacy size limit on the cluster port).

### Changed
- The Failed logins help text and README now say what v193 changed: that address, and that address together with that user name, are locked out, never a user name alone.
- Two existing user tests used passwords shorter than the new default and now use longer ones.

### Verified
- gofmt (clean), `go vet ./...`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cgo off, so these build the PAM stub). `node --check` on `app.js` and `help.js`. Only the native amd64 build was built with PAM headers.

### Not verified
- No live run: not several real daemons (in particular a v193 to v194 update, and a cluster with a v192 node), no real PAM login, no browser (the new Settings field and the sign-in with the new cookie name were not looked at in Chromium; the field is one line in `WEB_FIELDS`).
- The limit of 8192 connections and 256 per client is untested at that scale beyond the unit tests of the counters; a resolver farm behind one address that needs more than 256 connections will be refused (raise `maxStreamPerClient` in `dnsfront.go`).
- Still not changed: the open-resolver defaults (`allowed_clients` empty, no `client_rate`), the GUI and cluster ports listening on all interfaces by default, the shared cluster secret (no rotation), and uploaded update sources being built and run as root without a signature.

## [v193] - 2026-10-05 — Hardening: cluster identity, login lockout, upstream query IDs; faster cache path

### Security
- **A removed or rogue cluster node could get back in with the cluster secret alone.** The secret is shared by every member, and the identity a request claimed was only protected by it. The cluster listener now asks for a client certificate and `peerAuth` requires its fingerprint to be the identity the signed request claims. A removed member is also remembered by fingerprint (`removed_fp`), so it stays refused under another address. Once every member has been seen presenting its certificate (`mtls_fps`), the cluster becomes *strict* (`strict`, kept for good): only members already on the list are served, so holding the secret no longer makes a caller a member. Until then (a rolling update from v192 or older) members without a certificate still work; a member seen with a certificate may not drop it. A node that joins is announced to the other members at once (new `POST /cluster/peers/add`; older nodes answer 404, which is ignored), because a strict cluster refuses callers it has not been told about. The secret is still the same for all members and is not rotated when one is removed, and members still trust each other's gossip.
- **The login lockout could be beaten with parallel requests.** The lockout was checked before PAM and the failure recorded after, so any number of simultaneous guesses all passed. Attempts are now reserved before PAM is asked: failures so far plus attempts under way may not reach `max_failed_logins`.
- **Anyone could lock an administrator out.** The lockout was also keyed on the user name alone. It is now per address and per address+user name. Distributed guessing against one name is no longer slowed by a global per-user lock; PAM (faillock, or a delay) is the place for that.
- **Login capacity and memory.** At most 32 logins wait for PAM at once; more get 503 with `Retry-After` and PAM is not asked. The failure table holds at most 50,000 records; past that no new per-user record is made (the per-address one always is).
- **Client query IDs went upstream unchanged.** A client chooses its ID, and upstream sockets were reused for as long as they were busy, so a client that is also the attacker knew the ID and had only the source port left to guess. Each upstream exchange now carries a random ID and the client gets its own back; DNS UPDATE messages (which may be signed over the ID) are sent as they are. A busy upstream UDP socket is retired after 20 s or 1024 exchanges, so its source port changes.

### Changed
- **Clients waiting for the same question share the leader's result**, not only when it was cached. An answer that is not cached (SERVFAIL, truncated, a negative one without an SOA) or a failure is handed to every waiting client, as the cached answer already was, instead of each of them asking the upstream again.
- **The question of a query is read once** (`parseQuestion`), and the cache key is built in a stack buffer and looked up as bytes, so a cache hit allocates the answer only (5 allocations and 288 ns became 1 and 145 ns in `BenchmarkCacheHit`). The key format is unchanged; a test compares it with the previous function on about 3,000 mutated queries. Each entry keeps its question end instead of working it out on every hit.
- **The answer cache is also bounded in bytes**: `cache_entries` times 4096 bytes in all (an entry counts for its message, its key and 256 bytes of bookkeeping), oldest first, and one answer larger than a whole shard's share is not kept. No new setting. `/api/dns` cache statistics gain `bytes`.
- **Fewer allocations per packet.** Received UDP queries are copied into pooled buffers, and the length prefix and answer of a stream (TCP, DoT) are assembled in a pooled buffer and written at once (one TLS record for DoT).
- Forwarded queries are no longer byte-for-byte what the client sent (the ID differs). Four existing tests that asserted that now compare all but the ID.

### Added
- Tests: `cluster_identity_test.go` (certificate must match the claimed identity, no downgrade, legacy members, strict refusal of a non-member, removed node under a new address, re-join), `web_login_race_test.go` (parallel guesses bounded, no per-name lockout across addresses, table cap, full queue), `perf_test.go` (key equivalence, allocation-free hit, byte budget, shared uncacheable answer and failure, pooled buffers under load, TCP framing, `BenchmarkCacheHit`), and in `upstreamudp_test.go` random upstream IDs and socket retirement.

### Verified
- gofmt (clean), `go vet ./...`, `go build`, `go test -race -count=1 ./...` (passes), `CGO_ENABLED=0 go vet ./...` and `CGO_ENABLED=0 go test -count=1 ./...` (pass), `GOOS=linux go build` for amd64, arm64, arm, 386 and riscv64 (cross-compiles run with cgo off, so they build the PAM stub: they prove the code compiles, not that a PAM build works). Only the native amd64 build was built and tested with PAM headers present.

### Not verified
- No live run: not two or three real daemons (join, shared edit, promote, remove, update push, and above all a rolling update from v192, where strict mode must switch on by itself once every node has the new version), no real PAM login, no browser. Cluster, login and DNS changes were exercised by the in-process tests only.
- The v192 to v193 update path itself (an `--update-push` between mixed versions) was not run.
- Not changed: the 64 KB buffer a TCP/DoT connection allocates after its length prefix and the lack of a connection limit, the open-resolver defaults (`allowed_clients` empty, no `client_rate`), the shared cluster secret (no rotation), and the GUI and cluster ports listening on all interfaces by default.

## [v192] - 2026-10-05 — Updates and the installer find a snap-installed Go

### Fixed
- **A Go installed as a snap (`/snap/bin/go`) was not found by an update.** The daemon runs as a service whose `PATH` does not include `/snap/bin`, and the fixed list of places it tried did not either, so Operate ▸ Upgrade (and `--update-apply`/`--update-push`) stopped with "no Go toolchain >= 1.24 found". `/snap/bin/go` is now in that list (`update.go`, `findGo`), and in the installer's search (`install.sh`). It still has to be Go 1.24 or newer.

### Verified
- gofmt (clean), vet, `go test -count=1 -run 'Update|FindGo|Version'` (passes); `bash -n install.sh`.

### Not verified
- Not tried with a real snap Go: whether the snap's `go` wrapper runs when started by the daemon as root with `HOME` pointed at ddgw's state directory (the build sets `HOME` there), or under the service's systemd restrictions, is unchecked. If it does not work, install a distribution Go package (`/usr/lib/go-1.24`) or let `install.sh` keep one under `/usr/local/share/ddgw/go`.
- No full test run, cross-compile or live update for this version; `shellcheck` was not available here, and `install.sh` was not run.

## [v191] - 2026-10-05 — Anycast pill colours follow the neighbors; spacing on Operate ▸ Anycast

Rebuild of v190 so it can be applied over a v190 that was already installed. The v190 entry below was edited while v190 was being finished, so it also describes these changes.

### Changed
- **Anycast pill colours on Topology** (per address family, counting the configured neighbors): green when every neighbor is established, amber ("anycast · neighbor down") when at least one is and at least one is not, red when none is, when there is no neighbor of that family, or when BGP is disabled on the node ("anycast · BGP disabled"). A disabled or still-connecting neighbor counts as not established. The 30-second amber grace for a session that is still coming up is gone. New optional `bgp` field on each anycast state.
- **Operate ▸ Anycast:** space between the AS/State rows and the Disable/Enable BGP button.

### Verified
- Before this bump: gofmt, build, vet, `go test -race -count=1 ./...`, `CGO_ENABLED=0` vet and the five linux cross-compiles passed with the colour change. The spacing change was looked at in Chromium (dark).

### Not verified
- No tests were run for this version bump itself, by request; only `VERSION`, this entry and the archive name changed after the last run. The spacing CSS was not re-checked in light mode. Everything listed as not verified under v190 still applies.

## [v190] - 2026-10-05 — Anycast pages renamed, Operate ▸ Anycast, router ID needs an AS, node addresses in the tooltip

### Added
- **Operate ▸ Anycast.** A *BGP* card with **Disable BGP** / **Enable BGP**, and a neighbors table with **Disable** / **Enable** per neighbor. Disabling BGP renders `frr.conf` as if the AS were cleared (no BGP section, `bgpd`/`bfdd` off) but keeps the AS, router ID, timers and neighbors. Disabling a neighbor keeps it configured and renders `neighbor X shutdown`. New per-node config keys `disabled` (BGP and neighbor), written only when true. New `POST /api/bgp/operate` (relayable to another node), op `bgp.operate`, and CLI `--bgp-disable`, `--bgp-enable`, `--bgp-neighbor-disable ADDR`, `--bgp-neighbor-enable ADDR`. Saving the settings (`PUT /api/bgp`, `--asn` and friends) never changes these flags: they are carried over from the running config. Monitor ▸ Anycast shows a disabled neighbor as *disabled* and says so when BGP is disabled.
- **Topology: a node's tooltip lists its Ethernet interfaces** with their IPv4 and IPv6 global unicast (`2000::/3`) addresses and prefix lengths. "Ethernet" is a physical device, bridge or bond (type 1); veth, macvlan (including ddgw's own), tunnels and loopback are left out, as are link-local and ULA addresses. Carried in a new optional `addrs` field of the cluster status message, so nodes on an older version show nothing.

### Changed
- Operate ▸ Anycast: the Disable/Enable BGP button has space above it, below the AS and state (`.kv + .toolbar` in `style.css`).
- **Monitor ▸ BGP is now Monitor ▸ Anycast, Configure ▸ BGP is now Configure ▸ Anycast**, and the Configure card *This node* is now *BGP*. Old `#bgp` and `#bgpstatus` bookmarks still open the right page. Help topics, README, QUICKSTART and CLAUDE.md follow.
- **A router ID needs a local AS.** The field on Configure ▸ Anycast is disabled until an AS is set and clearing the AS clears it; the API and `--router-id` refuse one without an AS; `--asn off` clears it. A stored router ID next to no AS (written by an older version) is dropped when the config is read, rather than stopping it from loading.
- **Anycast pill colours on Topology:** per address family, counting the configured neighbors: **green** when every neighbor is established, **amber** ("anycast · neighbor down") when at least one is and at least one is not, **red** when none is, when there is no neighbor of that family, or when BGP is disabled on the node ("anycast · BGP disabled"). A disabled or still-connecting neighbor counts as not established. This replaces the old rule (green with any one session) and **removes the 30-second amber grace** for a session that is still coming up: it is red until a session is established. New optional `bgp` field on each anycast state (`disabled`, `none`, `down`, `partial`) words the label.
- **Behaviour note:** a config with `"disabled": true` is not readable by older versions (they reject unknown keys); one that never uses it is unchanged.

### Verified
- gofmt (clean), build, vet, `go test -race -count=1 ./...`; `CGO_ENABLED=0` vet and test; five linux cross-compiles (amd64, arm64, arm, 386, riscv64), on Go 1.24.13. `node --check` on `app.js` and `help.js`.
- New tests: the pill colour table (partial, connecting, disabled neighbor, all disabled, unknown to FRR, other family) and BGP disabled turning the pill red; router ID rule (set, API, old file), `shutdown` and disabled-process rendering, operate on process and neighbor, settings edits not changing the flags, the API route and its relay, the interface filter (link-local, ULA, 169.254, virtual links).
- Chromium (light and dark), the real `webui/` files against a mocked API: Operate ▸ Anycast disable/enable of BGP and of each neighbor sent the expected calls and redrew; neighbor buttons are off while BGP is disabled; `#bgp` and `#bgpstatus` redirect; the router ID field is disabled with no AS, cleared and saved as empty when the AS is cleared, enabled when one is typed; no page errors. A mocked two-node canvas showed the interface lines in the node tooltip.

### Not verified
- Not run against a real FRR: that `neighbor X shutdown` and a disabled process behave as intended, and the BFD state of a shut-down neighbor, were not checked live. No peer namespace was set up.
- The real daemon, real PAM login, and two clustered daemons were not run; the UI was checked against a mocked API only. The interface addresses were not carried between two real nodes, and the filter was tested with faked interfaces, not a host with real NICs, bridges and bonds.
- Cross-compiled non-amd64 builds were compiled, not run. PAM was not exercised.

## [v189] - 2026-10-05 — Cache and client limits now follow Settings on every gateway; four GUI fixes

### Fixed
- **Switching the cache off, and client rate limiting, had no effect on a gateway with its own DNS pool** (any gateway drawn on Topology with servers). A gateway's own pool kept the cache and client-rule values it was created with; only the six load-balancing values followed Settings (`DaemonConfig.poolFor`). Settings is the only place these are edited, so a change there never reached such gateways. `poolFor` now takes `cache`, `cache_entries`, `cache_max_ttl`, `allowed_clients`, `client_rate`, `client_burst`, `client_action` and `client_exempt` from Settings (`DNSConfig.followSettings`). The pool is rebuilt on a change as before, so it applies on save without a restart.
- **Login lockout message.** It now reads just "Too many failed attempts." The form still stays disabled until the lockout ends. (It used to add "Try again in m:ss.")
- **Monitor ▸ Statistics: y-axis labels were cut off** when the numbers were big. The chart's left margin now grows with the longest label. The same fixed margin was in the Host charts and in the per-server statistics dialog; both got the same fix.
- **Monitor ▸ Statistics: big numbers in the tiles (Total Queries, No Error, …) were cut off.** A number now shrinks to fit its tile; from 1,000,000,000,000 up it is shown compact (for example `600T`) with the full number in the tooltip.

### Changed
- **Configure ▸ Users:** the *Add user* card is now above the *Users* list (the empty-list text says "above").
- **Behaviour change:** the cache and client-rule keys inside a gateway's own `dns` block are now ignored, because Settings wins. They are still read and written back unchanged, so an older version can still open the file. README and the Settings help say so.
- README and help no longer say the lockout page shows a countdown.

### Verified
- gofmt (clean), build, vet, `go test -race -count=1 ./...`; `CGO_ENABLED=0` vet and test; five linux cross-compiles (amd64, arm64, arm, 386, riscv64). All of this on a Go 1.22 toolchain in a scratch copy with the `go` line of `go.mod` lowered to 1.22 (Go 1.24 could not be downloaded here); the tree's `go.mod` is unchanged.
- New test `TestGatewayPoolFollowsSettingsCacheAndClientRules`; it fails with the `followSettings` call removed and passes with it.
- Live, native cgo build, one daemon: a gateway with its own pool on a veth (VIP 10.99.0.1/24 on `v0`), a stub DNS server, and a client in a network namespace. v188 with the cache off in Settings: 20 identical queries, 1 upstream hit; with a rate of 5/s in Settings: 50 of 50 answered (both bugs reproduced). This version, same setup: cache off gives 20 upstream hits, cache on gives 1, switching back and forth by config edit applies without a restart; a rate of 5/s answers 32 of 50 from a client that waits 0.3 s per dropped query (burst 5 plus refill), a rate of 0 answers all 50.
- The same cache check through the real Settings page in Chromium (untick the box, wait, query): 20 of 20 queries reached the upstream while the gateway's own pool still said `"cache": true` in the file.
- Real PAM login (`contrib/pam.d/ddgw.debian`, throwaway group and users): the member gets in; a wrong password, a valid user outside the group and root get the same refusal. A locked-out address gets 429 and the real login page, in Chromium (light and dark), says "Too many failed attempts." and the text does not change while it waits.
- Users page against the real daemon in Chromium (light and dark): *Add user* first; adding a user through the form created the account. Statistics page loads (the only console message was the 401 of the session probe before sign-in). Charts and tiles with very large values checked against a mocked API in Chromium (light and dark, 1300 and 700 px wide): no label or tile number is clipped.
- `node --check webui/app.js` and `help.js`. Test users, group, PAM file, namespace and links removed afterwards.

### Not verified
- The loopback exemption was not checked live (the VIP is not reachable from loopback in this setup); it is covered only by the existing unit tests.
- The very large tile and axis values were checked with a mocked API, not with a node that really counted that many queries. The tile sizing was fitted using this sandbox's fallback bold font; another font with narrower or wider digits may leave more or less spare room.
- Cross-compiled non-amd64 builds were compiled, not run. PAM was checked only on native amd64.
- No cluster of two or more daemons was run (nothing here touched cluster code); no `install.sh`/`uninstall.sh` run. Only Chromium was used.

## [v188] - 2026-10-04 — Right-click menu: "Pause gateway" no longer stays highlighted

### Fixed
- In the topology's right-click menu, moving the pointer off **Pause gateway** onto another item closed its submenu but left the item highlighted, so two rows looked selected. The highlight is the `open` class that `openSub()` puts on the parent item; `closeSub()` removed the submenu but never the class. `closeSub()` now clears it too, so the parent is lit only while its submenu is showing. The Left-arrow key still returns focus to the parent item (it looks the item up before closing the submenu). Front end only (`webui/app.js`); no daemon, API, config or protocol change.

### Verified
- gofmt (clean), build, vet, `go test -race -count=1 ./...`; `CGO_ENABLED=0` vet and test; `node --check webui/app.js`; five linux cross-compiles (amd64, arm64, arm, 386, riscv64; all but amd64 build the fail-closed PAM stub).
- Real PAM login against a native (cgo) build on `127.0.0.1:53853` with a throwaway group and two users: the group member gets in; a wrong password, a valid user outside the group and root are all refused with the same message. Users, group and `/etc/pam.d/ddgw` removed afterwards.
- Real daemon, real GUI in headless Chromium (light and dark), driving the actual right-click menu on a gateway: with the v187 `app.js` the parent item keeps its highlight after the pointer moves to another item (reproduced); with this build it does not. Also checked: the parent stays lit while its submenu is open and while the pointer is inside the submenu, it is lit again when the submenu is reopened, Right/Left arrow keys open and close the submenu (Left refocuses the parent), Escape closes the menu, and there were no page errors.

### Not verified
- No new automated test: the menu is DOM code with no unit-test harness in the repo, so this is covered only by the manual Chromium run above (a throwaway script kept outside the repo).
- Only Chromium was used; Firefox and Safari were not tried (the change is a plain `classList` call). Touch input was not tried.
- The cross-compiled non-amd64 builds were compiled, not run, and the PAM-backed GUI was checked only on native amd64.

## [v187] - 2026-10-04 — Operate ▸ Node; Monitor ▸ Cluster (less confusing names)

### Changed
- **Operate ▸ Node** replaces Power and the short-lived Operate ▸ Gateway. Three cards: **Gateway controller** (Make this node the gateway controller; was "active gateway"), **Maintenance** (Pause / Resume this node) and **Host** (restart or shut down). The explanatory paragraph in the Host card is gone (it is in the help). Operate is now Node, Cluster, Upgrade. Old `#power` / `#gateway` bookmarks open Node.
- **Monitor ▸ Nodes is now Monitor ▸ Cluster** (it only shows the cluster members). Words used throughout: *node* = this machine, *cluster* = the group of nodes, *gateway* = the DNS service (the circle).
- Tooltips that said "(Power page)" now say "(Operate ▸ Node)". Help has a Node topic (the Power and Gateway topics are merged into it) and a Cluster title for the Monitor page; README, QUICKSTART and `--help` follow. CLI flags are unchanged.

### Verified
- `node --check` on app.js and help.js; gofmt, build, vet, `go test -race -count=1 ./...` (including `TestEveryPageHasHelp`); five linux cross-compiles; `CGO_ENABLED=0` vet and test.

### Not verified
- Not clicked in Chromium against a live daemon (the page, the three cards and the old-bookmark redirect); the endpoints are the unchanged ones.


## [v186] - 2026-10-04 — Operate ▸ Gateway; Monitor only watches

### Changed
- New page **Operate ▸ Gateway** with two cards: **Active gateway** ("Make this node the active gateway", moved off Monitor ▸ Gateways, where Monitor now only shows the tables) and **Take this node out of service** (Pause / Resume this node, moved from Power). Same calls, confirmations and CLI (`--assert-agc`, `--node-pause`, `--node-resume`, `--node-status`) as before; the explanatory line under the pause button is gone (it is in the help).
- **Operate ▸ Power** keeps only restart / shut down the host.
- Help has a Gateway topic and Power's was trimmed; README, QUICKSTART and `--help` follow.

### Verified
- `node --check` on app.js and help.js; gofmt, build, vet, `go test -race -count=1 ./...` (including `TestEveryPageHasHelp`); five linux cross-compiles; `CGO_ENABLED=0` vet and test.

### Not verified
- Not clicked in Chromium against a live daemon; the endpoints are the unchanged ones.


## [v185] - 2026-10-04 — Monitor ▸ Nodes: the duplicate "Gateway members" card is gone

### Removed
- The "Gateway members" card on Monitor ▸ Nodes. Monitor ▸ Gateways shows the same election data (one card per group and address family, with priority, slot, weight, role, state, preempt, age, vMAC and DNS state), so nothing is lost. Nodes now shows only the cluster members and no longer asks for `/api/neighbors`.

### Changed
- Help and README: `--show-neighbors` is listed under Gateways; the "nothing is listed on a network that should have peers" advice moved to the Gateways topic.

### Verified
- `node --check` on app.js and help.js; gofmt, build, vet, `go test -race -count=1 ./...` (including `TestEveryPageHasHelp`); five linux cross-compiles; `CGO_ENABLED=0` vet and test.

### Not verified
- Not looked at in Chromium against a live daemon.


## [v184] - 2026-10-04 — Topology: this node is marked with a star

### Changed
- On the Topology drawing, this node's parallelogram now shows a star after its name (`ns1 *`, as on the other pages) instead of "this node · " before its state; the second line is just the state. The tooltip still says "(this node)".

### Verified
- `node --check` on app.js and help.js; gofmt, build, vet, `go test -race -count=1 ./...`; five linux cross-compiles; `CGO_ENABLED=0` vet and test.

### Not verified
- Not looked at in Chromium against a live daemon.


## [v183] - 2026-10-04 — Topology: lines no longer run through boxes; thicker blue lines

### Changed
- The nodes (left) and anycast pills (right) stand four to a column as before, but when a line from the circle to a server would pass through one of those boxes with four rows, both sides use three rows instead. The check is geometric and done when the drawing is built, so it follows the number of nodes, pills and servers.
- The blue lines (servers within the load-balancing band) are twice as thick (4 px).

### Verified
- `node --check webui/app.js`; gofmt, build, vet, `go test -race -count=1 ./...`; five linux cross-compiles; `CGO_ENABLED=0` vet and test. The same geometry in a standalone script: 4 nodes, 4 pills and 8 servers (your picture) clashes with four rows and not with three; small drawings (1–3 nodes, up to 4 pills, 4 servers) keep four rows.

### Not verified
- Not looked at in Chromium against a live daemon (light and dark); only the geometry was checked outside the browser.


## [v182] - 2026-10-04 — A node that joins a cluster gets its users

### Added
- After a node joins a cluster it now copies the cluster's GUI-group accounts (name, password hash, expiry) from the node it joined through (new `list` op on `/cluster/users`). Before, only changes made after the join reached it, so it had no one who could sign in.
- An account that already exists on the new node (in the group or not) is left untouched, `root` and accounts with no usable password are never copied. If the copy fails the join still succeeds and the log says so.

### Verified
- gofmt, build, vet, `go test -race -count=1 ./...`; five linux cross-compiles; `CGO_ENABLED=0` vet and test; new tests `TestUsersSeedOnJoin`, `TestUsersExport`.
- Live: two real daemons (the second in its own mount namespace with a private `/etc`): alice and bob on the first, a different bob on the second, then `--cluster-join`: alice appeared on the second with the identical hash, the second's own bob kept its password and no expiry, the log named both counts. Test users, group and processes removed afterwards.

### Not verified
- Expiry of a copied account was covered by the unit test only, not live; joining an older-version cluster (it refuses the new op, so nothing is copied and the log says so); 3+ nodes; joining through a replica.


## [v181] - 2026-10-04 — Anycast pill: a session that never establishes is red

### Changed
- Amber ("BGP connecting") now lasts at most 30 seconds. An announced anycast address whose neighbor is still in Connect, Active, OpenSent or OpenConfirm after that turns red, and the tooltip says how long it has been waiting. The clock starts when the address is first seen without a session, restarts when a session establishes, and starts over if the address was withdrawn in between.

### Verified
- gofmt, build, vet, `go test -race -count=1 ./...`; five linux cross-compiles; `CGO_ENABLED=0` vet and test; `TestAnycastBGPGrace` steps through amber → red → green → amber → a gap.

### Not verified
- Not run against a live FRR peer or looked at in Chromium. The 30 s is a fixed value, not a setting.


## [v180] - 2026-10-04 — Anycast pills follow the BGP sessions

### Changed
- When this node manages BGP (a local AS is set), an announced anycast address is green only with an established session to a neighbor of its family, amber while a session is coming up (Connect, Active, OpenSent, OpenConfirm), and red when there is none (no neighbor of that family, all Idle, or FRR not answering). The pill reads "anycast · BGP connecting" / "anycast · no BGP session" and the tooltip says why. Without a local AS the colours are unchanged.
- Computed once in the daemon (`AnycastState.Status`/`Detail`, from `show bgp summary json`, cached 3 s), so the GUI and `ddgw --canvas` agree; a red or amber pill counts as down in the uptime tooltip.

### Verified
- gofmt, build, vet, `go test -race -count=1 ./...`; five linux cross-compiles; `CGO_ENABLED=0` vet and test; `node --check webui/app.js`; new tests for every state/family combination (`TestAnycastBGPStatus`) and through the supervisor with a fake `vtysh` (`TestMarkAnycastBGPColour`).

### Not verified
- Not run against a live FRR peer or looked at in Chromium; the FRR output is the existing parser's, the colours are covered by the unit tests only.

## [v179] - 2026-10-04 — Users: no success bar

### Changed
- The Users page no longer shows a notice after a successful add, password change, expiry change or delete; the list shows it. A notice still appears for an error, and a warning when a cluster node did not get the change.

### Verified
- `node --check webui/app.js`; help and version tests.

### Not verified
- Not looked at in Chromium.

## [v178] - 2026-10-04 — Users: cluster-wide changes, root never listed

### Changed
- **User changes apply to every cluster node.** Add, delete, password and expiry are made on the node you are on and sent to the other members over the cluster channel (`POST /cluster/users`, signed, pinned TLS) as the password *hash*, applied with `chpasswd -e` on standard input; the plaintext password never leaves the node that received it. The reply (GUI notice, CLI) says on how many nodes it was applied and names those that were not and why; the CLI then exits with 2 and the GUI shows a warning. A receiving node applies the same rules as a local change: it never takes over an existing account outside the GUI group, never touches root, accepts a delete of an account that is already gone, and keeps its own last-account guard. *Password* re-sends the whole account (hash and expiry) and creates it where it is missing, which repairs a node that was down; `--user-del` for an account that is not here still tells the other nodes.
- `root` is never listed and cannot be added, changed or deleted here, even when it is in the GUI group; it does not count as "another account that can sign in".

### Verified
- gofmt, vet, `go test -race ./...`, five cross-compiles, `CGO_ENABLED=0`, `node --check`; CodeQL (go-code-scanning): no alerts.
- Unit tests: root hidden and untouchable, a peer cannot touch root, exact `useradd`/`chpasswd -e`/`usermod` calls for an account arriving from another node (hash on stdin only), malformed hashes (plaintext, `!`, line break, colon), bad names, existing accounts never taken over, delete idempotent.
- Two real daemons joined into a cluster, the second in its own mount namespace with a private copy of `/etc` (so the two have separate account databases): add (account, hash and expiry identical on both), password change, expiry, delete all reached node 2; a user created on node 1 signed in on node 2 with the same password; with node 2 stopped an add reported "NOT applied on 127.0.0.1:53862" (exit 2); after node 2 was back, setting the password again created the missing account there; `root` in the group is not listed. Test users, group and PAM file removed afterwards.

### Not verified
- Nodes on different versions (a node without `/cluster/users` answers 404 and is reported as not applied; not tried). The GUI warning for a partial result was not looked at in Chromium. Three or more nodes were not tried.

## [v177] - 2026-10-04 — Users page, installer adds the installing user

### Added
- **Configure ▸ Users** (CLI `--users`, `--user-add NAME [--expires YYYY-MM-DD]`, `--user-passwd`, `--user-expiry NAME --expires DATE|never`, `--user-del`): list, add, re-password, set or clear the expiry of, and delete the accounts that may sign in to the GUI (members of `web.group`). Done with `useradd` (no shell, no home directory, primary group = the GUI group), `usermod --expiredate`, `userdel` and `chpasswd`; passwords go to `chpasswd` on standard input, never a command line, and the CLI asks for them without echo. Names are validated (1-32 of `a-z0-9_-`, starting with a letter or `_`) before any command runs. Only members of the group can be changed (never root or another system account); *Add* refuses an account that already exists; you cannot delete yourself or the last account that can sign in. Any member can manage the others; every change is logged with who made it. Accounts are per node and the Node menu picks the node (`/api/users` is relayed). Help topic, README section and CLI table row added.
- `users_test.go`: name validation, listing (group line, primary-group members, expiry from shadow), add (exact `useradd` arguments, password only on stdin, refusals run nothing, rollback when `chpasswd` fails), password/expiry/delete only for members, self and last-account guards.

### Changed
- `install.sh` now always adds the user who ran it (the one behind `sudo`, or root itself) to the `ddgw` group, at the end, instead of asking; `--add-user` still adds others. README and QUICKSTART updated.
- `input[type=date]` is styled like the other inputs.

### Verified
- gofmt, vet, `go test -race ./...`, `node --check`, help tests; the five cross-compiles and `CGO_ENABLED=0` vet/tests; CodeQL (go-code-scanning): no alerts.
- Real PAM run (group, `/etc/pam.d/ddgw`, real daemon): `--user-add` through the CLI (with piped password; bad names, an existing account and an empty password refused), sign-in over HTTPS as the new user, add / delete-self refused / password of root refused / expiry in the past (that account's sign-in refused) / expiry cleared / new password (old one refused, new one works) / delete over the API, a request without the CSRF token refused, log lines show who did what and no passwords. The page looked at in Chromium, light and dark (add through the form, password editor); test users, group and PAM file removed afterwards.
- `bash -n`, shellcheck and `install.sh --dry-run` (with and without `SUDO_USER`) show the `usermod -aG` for the installing user.

### Not verified
- The installer change was not run for real (no full install in the sandbox this time); the expiry editor, the delete button and the Node-menu relay of `/api/users` were not clicked through in Chromium. The CLI's no-echo password prompt was not tried on a real terminal (only the piped path).

## [v176] - 2026-10-04 — Cluster connections verified properly, CodeQL findings fixed

### Changed
- Cluster connections (`clusterpin.go`) no longer disable certificate checking. A node's identity certificate is still pinned by its SHA-256 fingerprint, but the connection now verifies against exactly that certificate through the normal TLS verification: the first connection to a fingerprint only learns the certificate the peer offers and accepts it only if the fingerprint matches; the real connection then verifies the peer (name, validity, key usage) against it. The certificate is remembered in memory, so later connections do one handshake.
- Requests to peers go to a fixed URL (`https://ddgw-node/...`) and the connection is dialled to the peer's address (still validated by `validHostPort`), so no peer-supplied text is ever part of a URL.
- A config version's file name is built from the version number, not from the id's text; `validVersionID` now accepts only the canonical decimal form of a positive number.
- This resolves the four CodeQL alerts (disabled TLS certificate check, two uncontrolled-data-in-network-request, uncontrolled-data-in-path-expression). The same four showed up in a local CodeQL run (go-code-scanning suite, CodeQL 2.27.1) before the change and none after.

### Added
- `clusterpin_test.go`: a pinned peer connects (twice, the second from the remembered certificate); a wrong pin, an impostor on the right address, a pin that matches a certificate for another name, and an invalid address are all refused. `TestVersionPathStaysInside` rewritten for the new path rule.

### Verified
- gofmt, build, vet, `go test -race ./...`, the five cross-compiles, `CGO_ENABLED=0` vet and tests; CodeQL on the final tree: no alerts.
- Two real daemons: join with a code, replica listed as reachable, a gateway added on the replica appeared on the primary and both agreed.

### Not verified
- Certificate install/revert, promotion and an update push through the new connection were not re-run live (they use the same call path). 

## [v175] - 2026-10-04 — No more references to the Python predecessor

### Changed
- CLAUDE.md, code comments, test messages and the install/uninstall scripts no longer mention the Python daemon. The wire format, the golden vectors and the text comparison of election ties are unchanged and are now described as the protocol's own rules.
- `install.sh` no longer warns about a `dgw` daemon or suggests copying its config.
- CLAUDE.md layout note corrected: only the CHANGELOG is under `docs/`.

### Verified
- A search for "python" finds only old changelog entries, `python3 -m http.server` in the installer test recipe and FRR's `frr-pythontools` package; `bash -n` and shellcheck on both scripts; `install.sh --dry-run`; gofmt, vet, `go test -race ./...`.

### Not verified
- Comment and message changes only; no behaviour change besides the removed installer warning.

## [v174] - 2026-10-04 — README brought up to date

### Changed
- README rewritten to describe ddgw as it is: a new "How it works" section (gateway, VIP, controller/forwarder, cluster) and a "Build from source" heading.
- Removed version history and compatibility notes (old config keys, the `/etc/liras` move, "before vNNN", "as before", older-node remarks) and a mention of another project.
- Fixed statements that were wrong: the proxy does cache (the statistics text said it did not); the cluster sync interval default is 5 s (the member-count paragraph said 8 s); BGP is under Configure, not Operate; a truncated DoT sentence; a duplicated whois sentence; the install example now says `ddgw_vN.tgz`.

### Verified
- Every removed phrase searched for afterwards; the sync interval checked against `config.go` and the sidebar entries against `app.js`. VERSION test run.

### Not verified
- Prose only; the "How it works" description of how the controller answers ARP/NS is from the code's design notes, not re-tested.

## [v173] - 2026-10-04 — README opening rewritten

### Changed
- The README intro now describes what ddgw is and does (shared gateway address, DNS proxy, anycast over BGP, web GUI and CLI cluster management). The reference to the older Python project is gone.

### Verified
- Each claim in the new text checked against the README body; VERSION test run.

### Not verified
- Wording only; no code changed.

## [v172] - 2026-10-04 — Screenshots in the README

### Added
- `snaps/` with `topology.png` and `statistics.png`; the README shows both under the intro so they appear on the GitHub page.

### Verified
- Both images open and the README links resolve (relative paths). Preview rendered in Chromium from a GitHub-styled stand-in page.

### Not verified
- Not viewed on GitHub itself.

## [v171] - 2026-10-04 — README and LICENSE at the archive root

### Changed
- `README.md` and `LICENSE` moved from `docs/` to the top of the archive; `docs/` keeps `CHANGELOG.md`.
- `install.sh` reads them from the new place (the installed copies are unchanged). CLAUDE.md, QUICKSTART.md and README references updated.

### Verified
- `bash -n` and `shellcheck -S warning` on `install.sh`; `--dry-run` lists README, LICENSE and CHANGELOG.

### Not verified
- No full install run.

## [v170] - 2026-10-04 — Load Balancing in 3 columns, "Queries per second"

### Changed
- Settings ▸ DNS proxy ▸ Load Balancing: the five number fields lay out 3 on the first row and 2 on the second (one column on narrow screens).
- "Queries per second per client" is now "Queries per second" (help text says what a client is).

### Verified
- `node --check webui/app.js`; gofmt, build, vet, `go test -race ./...`.

### Not verified
- Layout not looked at in Chromium.

## [v169] - 2026-10-04 — "Cache TTL"

### Changed
- Settings ▸ DNS proxy: *Longest an answer is kept (s)* is now **Cache TTL (s)**; the help explains it.

### Verified
`node --check`, help and web tests.

### Not verified
- Not looked at in a browser; the full suite was not re-run (a label only).

## [v168] - 2026-10-04 — Cluster card: "Advertised address"

### Changed
- Settings ▸ Cluster: *Address peers use to reach this node* is now **Advertised address** (one line, so the card stays short); the help describes it.

### Verified
`node --check`, help and web tests.

### Not verified
- Not looked at in a browser; the full suite was not re-run (a label only).

## [v167] - 2026-10-04 — Cluster card: "Binding address", no hints

### Changed
- Settings ▸ Cluster: *Peer listen address* is now **Binding address**, and the two hints under the address fields are gone; the help panel describes both fields.

### Verified
`node --check`, vet, help and web tests.

### Not verified
- Not looked at in a browser; full race suite and cross-compiles not re-run (a label and help text only).

## [v166] - 2026-10-04 — Certificate moved under Settings ▸ Web GUI; stale cluster text removed

### Changed
- The Certificate page is gone from the sidebar: the same card, upload, request and revert controls now sit under **Settings ▸ Web GUI**, below the web settings. An old `#certificate` link opens that tab. The page's help moved into the Settings help (a "Certificate (Web GUI tab)" section), and the `--tls-*` commands are listed there.
- Removed the explanatory paragraphs on the Settings ▸ Web GUI and Cluster cards. The Cluster one was also wrong: it said a group's neighbours stay specific to the node, which has not been true since v161 (the unicast list is shared). Other mentions of "the Certificate page" in the GUI, help, README, QUICKSTART and `--help` now say Settings ▸ Web GUI.

### Verified
gofmt, build, vet, help and web tests, `node --check`. In Chromium (light and dark) against a real daemon with a real PAM login: the sidebar has no Certificate item; Settings ▸ Web GUI shows the web settings and the certificate cards; the Cluster tab shows only its fields; an old `#certificate` link opens the Web GUI tab. The throwaway user, group and PAM file were removed afterwards.

### Not verified
- Installing, requesting and reverting a certificate from the new place was not clicked through (the code is the same as before). The full race suite and cross-compiles were not re-run: only JavaScript, help text and strings changed. The settings form autosaves on a redraw, which re-creates the certificate card, so a half-pasted certificate would be cleared by a settings save.

## [v165] - 2026-10-04 — Whois for client addresses

### Added
- Statistics ▸ Top clients: the hover tooltip lists the reverse-DNS names, then the address's whois data: block, name, organization, country and origin AS. IANA names the regional registry, which is asked about the address (one referral hop is followed; ARIN, RIPE-style and LACNIC answers are read). Private, loopback, link-local, carrier-grade, documentation and multicast addresses are never sent to a whois server; the tooltip says why there is nothing. Answers are cached like domain answers (a day; an hour for misses). `--whois ADDRESS` prints the same.

### Verified
gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and tests, cross-compiles, `node --check`; new `TestParseIPWhois` and `TestWhoisLookupIP` (against a fake whois server: IANA then ARIN, caching, non-public addresses never asked, the op wrapper).

### Not verified
- Never run against a real registry (no network to whois servers here), so ARIN, RIPE, APNIC, LACNIC and AFRINIC answers were parsed from sample text only. The tooltip was not looked at in a browser.

## [v164] - 2026-10-04 — Domain tooltip shows its addresses

### Added
- Statistics ▸ Top domains: the hover tooltip lists the first IPv4 and IPv6 address of the name above its whois data. They are asked of the DNS pools (the servers this node forwards to), then this machine's resolver for a family still missing. `--whois NAME` prints them too. A name with no whois record (an internal name such as `pbs1.cush.local`, or an unreachable registry) now still shows its addresses, with the reason below them; it was an error line before.

### Verified
gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and tests, cross-compiles, `node --check`; new `TestWhoisWithAddrs`. Live with a real daemon and a stub DNS server answering A and AAAA: `--whois` on an internal and on a public name printed both addresses, then the whois note (the sandbox cannot reach registries).

### Not verified
- The tooltip itself was not looked at in a browser, and real whois data was not fetched (no network to registries here).

## [v163] - 2026-10-04 — README: cloud use note

### Added
- README: a short "Cloud use (untested)" section under BGP: what can work in a cloud (anycast addresses over BGP, unicast neighbors, multihop), and what to set up. Docs only.

### Verified
Text only; the Go tests were not re-run.

### Not verified
- None of it has been tried in a cloud.

## [v162] - 2026-10-04 — Help text: unicast neighbors are shared

### Fixed
- `ddgw --help` and the join-code message still said a node's neighbours stay its own; the list has been shared since v161.

### Verified
Live, two real daemons in network namespaces: joined into a cluster, then (1) a hand edit of the neighbors list on the primary reached the replica, and both engines logged "Unicast mode: 2 neighbor(s)" and elected normally; (2) `--config-import` on the replica with a third address reached the primary and the replica's own file. Earlier the same day: two nodes in unicast mode with each node's own address in the list: election, queries to the VIP, SIGTERM handover and a restart with no failed queries, `kill -9` about 2 s. gofmt, vet, `node --check`. As documented, a hand edit of the config file on a replica is overwritten by the primary, so that path does not replicate.

### Not verified
- Three or more nodes, and unicast over a real routed network. The full test suite was not re-run for this text-only change.

## [v161] - 2026-10-04 — Unicast neighbors are shared

### Changed
- A gateway's **Neighbors (unicast mode)** list is now shared by the cluster: list every node, this one too, once, and it replicates to all nodes. A node skips its own address when sending. It was per node (each node listed the others). Empty still means multicast, and a multicast cluster's shared hash is unchanged. There was no migration step on purpose: no cluster uses unicast yet. A node on an older version keeps its own list and ignores the shared one.
- Changing the list restarts that gateway's engine on each node, as editing it always did.

### Verified
gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and tests, cross-compiles; new `TestNeighborsAreShared` (replication through `mergeShared`, the shared hash, the own-address check).

### Not verified
- No live run with real daemons in unicast mode; the skipping of a node's own address was only tested as a unit.

## [v160] - 2026-10-03 — Edit gateway fits on screen

### Changed
- The Edit / New gateway form lost its explanatory hints (they are in the help panel's field list), and the five own load-balancing values only show while *Load balancing* is set to "own values".
- Every popup form now has a maximum height with its own scrolling, and the Save / Cancel buttons stay at the bottom, so they never need scrolling to.

### Verified
`node --check`, vet, build, web and help tests.

### Not verified
- Not looked at in a browser (light or dark): the sticky button bar and the show/hide of the load-balancing fields are untested. Full race suite and cross-compiles were not repeated: only JavaScript and CSS changed.

## [v159] - 2026-10-03 — BGP keepalive and hold time are settings

### Added
- BGP ▸ Configure: **Keepalive** and **Hold time** fields next to the router ID, default 3 s and 9 s. CLI: `--keepalive S --hold S` (`-` for the default). Config: `keepalive`, `hold` (omitted when unset, so older configs are unchanged). The hold time must be 3 or more, and the keepalive shorter than it. The session still uses the lower hold time of the two ends.

### Changed
- The fixed `timers bgp 3 9` of v158 now comes from these settings.
- BGP ▸ Configure: removed the explanatory paragraph above the Neighbors table and the hints under the new timer fields; the explanations live in the help panel only. `CLAUDE.md` now says to keep GUI text short and put explanations in help.

### Verified
gofmt, build, vet, `go test -race`, `CGO_ENABLED=0` vet and tests, cross-compiles (amd64, arm64, arm, 386, riscv64), `node --check`; new `TestBGPTimerSettings`. The rendered `timers bgp` line is the one tested live with real FRR in v158.

### Not verified
- The new fields were not looked at in a browser, and no custom timers were run against a live peer.

## [v158] - 2026-10-03 — BGP: per-neighbor multihop, keepalive/hold 3 s / 9 s

### Added
- A per-neighbor **multihop** limit (2-255, eBGP only; empty = directly connected) for a peer that is not on a connected subnet, such as an AWS VPC Route Server endpoint. BGP page: a Multihop column; CLI: `--bgp-neighbor-add … --multihop N`; config: `neighbors[].multihop` (omitted when unset, so existing configs and older versions are unaffected). It is written as `ebgp-multihop N` and skipped for an iBGP neighbor.

### Changed
- `timers bgp 3 9` is written for every BGP configuration (FRR's default is 60/180). The session uses the lower hold time of the two ends, so a peer with longer timers agrees them down; a peer with a hold time minimum above 9 s would refuse the session.

### Why multihop is not on by default
With two real FRR instances on a direct link, multihop on one side only leaves BFD down and the session resetting: FRR switches a multihop neighbor's BFD to multihop mode, which a single-hop peer does not match. Both sides multihop: BFD up.

### Verified
gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and tests, cross-compiles (amd64, arm64, arm, 386, riscv64), `node --check`; new `TestBGPMultihopAndTimers`. Live with real FRR in two namespaces: a peer with 60/180 timers negotiated 3 s / 9 s, and multihop 2 on both sides gave BGP Established and BFD up.

### Not verified
- Not tried against an AWS VPC Route Server, so whether its BFD is single-hop or multihop is unknown.
- The BGP page's new column was not looked at in a browser.
- The full ddgw daemon was not run against FRR for this change; only the generated settings were fed to FRR by hand.

## [v157] - 2026-10-03 — Pause and Resume never show together

### Changed
- The right-click menu offers only *Resume ▸* (for the scopes it is paused in) when an item is paused, and only *Pause ▸* when it is not. Before, an item paused in one scope showed both.

### Verified
`node --check`, vet, build, web tests.

### Not verified
- Not looked at in a browser.

## [v156] - 2026-10-03 — Pause menus open beside the menu on hover

### Changed
- Right-click menus can now cascade: *Pause ▸* and *Resume ▸* open their *This node / All nodes* menu beside the first one while the pointer rests on the item (it also opens on click or the right arrow key; left arrow or Escape closes it). Before, clicking *Pause* replaced the menu.

### Verified
gofmt, build, vet, `node --check`, web tests.

### Not verified
- Not looked at in a browser (light or dark); the hover behaviour and its placement near the screen edge are untested.

## [v155] - 2026-10-03 — Pause ▸ This node / All nodes for anycast addresses, gateways, servers and domains

### Added
- Right-click an anycast address, gateway, server or domain ▸ *Pause ▸ This node | All nodes* (and *Resume ▸* the same way). *This node* lives in this node's own settings and is never replicated; *All nodes* is shared. The scopes are independent.
- Anycast: a paused address is withdrawn from `lo` (logged at info, not as a warning) while the gateway keeps serving. Gateway: *all nodes* is new. Server: *this node* is new. Domain: new, both scopes; a paused domain is not probed, and the last active domain of a server cannot be paused. Pausing a domain restarts the pool; it is not restarted otherwise.
- CLI: `--canvas-pause|--canvas-resume gateway|server|domain|anycast ... --scope node|all` (default node for a gateway, all for a server; required for a domain and an anycast address). Config: `paused_vips`, `paused_all`, `paused_queries` (shared) and `paused_vips_here`, `paused_servers_here`, `paused_queries_here` (local). The shared lists are omitted when empty, so a cluster that never pauses keeps its hash.
- Config History wording for domain pauses. Help and README updated.

### Changed
- The Settings page now carries the pause lists through a save untouched.

### Verified
gofmt, build, vet, `go test -race` (all pass), `CGO_ENABLED=0` vet and tests, cross-compiles for amd64, arm64, arm, 386 and riscv64, `node --check`; new tests in `pause_scope_test.go` and the anycast pause tests. One existing test was changed: `effective()` now always returns a copy, so it checks the content rather than that it is the same object.

### Not verified
- The new menus and the grey/dashed drawing were not looked at in Chromium (light or dark), and no live run with real daemons was done; a real second node has only been exercised by in-process tests.

## [v154] - 2026-10-03 — "★ marks this node" on the Cluster page too

### Changed
- Operate ▸ Cluster: the same line, "★ marks this node", under the Members table.

### Verified
gofmt, build, vet, tests, `node --check`.

### Not verified
- Not looked at in a browser.

## [v153] - 2026-10-03 — "★ marks this node" moved to the bottom of the page

### Changed
- Monitor ▸ Gateways and Monitor ▸ Nodes: the note "★ marks this node" is a line at the bottom of the page instead of beside the button (Gateways) or in the Gateway members header (Nodes, whose header now reads "Addresses of every node in a group.").

### Verified
gofmt, build, vet, tests, `node --check`.

### Not verified
- Not looked at in a browser.

## [v152] - 2026-10-03 — Operate ▸ Updates is now Operate ▸ Upgrade

### Changed
- The sidebar item, the page's help title and the Settings ▸ General card that holds the auto-update tick box are named **Upgrade** instead of Updates; the README and quick start follow. Commands (`--update-*`), the page's address (`#updates`) and the statistics **Updates** tile (dynamic DNS updates) keep their names.

### Verified
gofmt, build, vet, tests, `node --check`.

### Not verified
- Not looked at in a browser.

## [v151] - 2026-10-03 — the performance profile is gone

### Removed
- The Performance profile: the card at the bottom of Monitor ▸ Host (**Collect profile (20 s)**), `ddgw --profile [--profile-seconds N]`, the `POST /api/profile` endpoint, the timeout probe it ran, and their tests, code, help section and README section. It was added to chase lock contention, which is fixed. Nothing else used it, and the daemon no longer switches mutex or block profiling on at any time.

### Verified
gofmt, build, vet, tests, `node --check`.

### Not verified
- Host page not looked at in a browser.

## [v150] - 2026-10-03 — more room beside the circle on the drawing

### Changed
- Topology: the gap between the gateway circle and the cluster-node parallelograms on its left, and the anycast pills on its right, is 70 px instead of 40.

### Verified
gofmt, build, vet, tests, `node --check`.

### Not verified
- Not looked at in a browser.

## [v149] - 2026-10-03 — Statistics footer text removed

### Changed
- Monitor ▸ Gateways: the header note "★ marks this node. Refreshes every 2 s." is now "★ marks this node". (The Gateways help still says it refreshes every 2 seconds.)

- Monitor ▸ Nodes ▸ Gateway members: the header note "IPv4 / IPv6 link-local of every node in a group, this one ★. Every 2 s." is now "Addresses of every node in a group, ★ marks this node."

- Monitor ▸ Nodes ▸ Cluster members: the header note now reads "Nodes sharing these settings." (the help says adding, removing and promoting are on the Cluster page).

- Operate ▸ Cluster ▸ Members: the header notes "Refreshes every 2 s." and "Not part of a multi-node cluster yet." are gone; Monitor ▸ Nodes ▸ Cluster members always says "Nodes sharing these settings."

### Removed (also)
- Operate ▸ Power: the node status line ("This node is running." / "This node is PAUSED: …") and the blue message after pausing or resuming. The button already reads Pause this node / Resume this node; the Nodes and Topology pages show the paused state.

### Removed
- Monitor ▸ Statistics: the footer "Counted since … and kept for 30 days (saved to disk every 5 minutes, so a restart keeps it). Top lists are collected in 10-minute intervals and keep the busiest 300 clients and 600 domains." Both facts are in the page's help. The memory-guard warning still appears, alone, when the guard has dropped history.

### Verified
gofmt, build, vet, tests and `node --check`.

### Not verified
- Not looked at in a browser.

## [v148] - 2026-10-03 — host load over 85% is logged, and turning things on and off reads as enabled/disabled

### Added
- When a node's CPU, memory or fullest disk goes over 85% (the amber shape), the log gets a warning naming what is over, for example `host load: node ns1 is over 85%: memory 92%; it shows amber until all are back at 85% or below`; when it is back at 85% or below an info line follows (`host load: node ns1 is back at 85% or below (CPU 20%, memory 41%, disk 50%)`). It covers this node and every reachable cluster member, is checked every sync interval with no browser open, and a reading that only moves (CPU 91% to 93%) is not logged again; a further item going over is logged as "now over". A member that stops answering keeps its last state, so silence is not logged as recovery. Shown on the Log page.
- Turning something on or off now reads that way in the log line every configuration change already writes (and in History): `cache disabled`, `preempt enabled`, `tls_insecure enabled`, `dot_port enabled (853)`, `doh_port disabled`, `client_rate enabled (50)` instead of `cache true → false` or `dot_port 0 → 853`. Pausing and resuming a gateway, a DNS server or the whole node were already worded that way ("paused on this node", "servers resumed …", "Node: resumed").
- Turning automatic updates on or off writes a log line (`auto-update enabled by alice`); before it only went into the update history.
- `TestStrainLogTransitions`, `TestClusterLogsHostStrain`, `TestSummaryWordsEnableDisable`.

### Verified
See the checks run for this release.

### Verified live (also)
- A real daemon logged `host load: node vm is over 85%: disk 91% (/opt/claude-code)` (the sandbox disk) and `auto-update disabled by cli:root`.

### Not verified
- A real cluster member going over and recovering; the tests drive two in-process nodes.

## [v147] - 2026-10-03 — the node tooltip says amber, not red

### Fixed
- The tooltip of a cluster node's shape still ended its host line with "red above 85%"; since v145 the shape turns amber, and it now says "amber above 85%". No other text said red.

### Verified
See the checks run for this release (gofmt, build, vet, race tests, cross-compile, CGO_ENABLED=0, node --check).

### Not verified
- The tooltip was not looked at in a browser; it is one word in a string.

## [v146] - 2026-10-03 — every cluster node's shape shows the same kind of status

### Fixed
- On the Topology drawing, this node's parallelogram took the gateway's health (amber "degraded" while some DNS servers were down) but another node's only said whether it was serving, so the same trouble showed amber on one and green on the other. Each node now reports its gateway's health with its status (`health` and `health_why` in the peer status; absent from older nodes, which are drawn as before), and a node that is serving but degraded is drawn amber, with its tooltip saying why. Not serving, paused and not answering are unchanged.

### Verified
See the checks run for this release. `TestCanvasPeerShowsItsOwnHealth` covers degraded, healthy and an older node that reports no health.

### Not verified
- Not looked at on a live two-node cluster; the unit test drives the same code with simulated peers.

## [v145] - 2026-10-03 — a node over 85% is yellow, and the update history is paged

### Changed
- A cluster node whose CPU, memory or fullest disk is over 85% is now **yellow** (degraded) on the Topology drawing instead of red, because red means down and the node is still up. Everything else about it is as in v141: it names what is over, the tooltip shows the numbers, and it stays yellow until all are back at 85% or below. A node that is not answering is still red.
- `ddgw --update-history` prints every event kept (it printed the newest 100); `ddgw --update-status` prints the newest 50 of them.

### Changed (also)
- Domain names in the trapezoids on the Topology drawing are normal weight (were semibold); server and node names are unchanged.

### Added
- Updates ▸ History shows every update event kept (the newest 500; the page used to stop at 50), newest first, 25 to a page, with **‹ Newer** / **Older ›** and "Events 26–50 of 130". The page you are on stays across the 2-second refresh.
- `TestUpdateStatusCarriesWholeHistory`.

### Verified
See the checks run for this release (gofmt, build, vet, race tests, cross-compile, CGO_ENABLED=0, node --check).

### Not verified
- Pagination was seen with 60 seeded events (3 pages, page kept across the refresh, light and dark).
- The yellow node was covered by the unit test, not seen on a live drawing with two or more real nodes (the rig has one).

## [v144] - 2026-10-03 — every field of the application has a help description

### Added
- Each page's help panel (the **?**) now ends with a **Fields** list: every field on that page and in its popup forms, by its on-screen label, saying what it does, the values allowed, the default and (in Settings) whether it is **shared** with the cluster or belongs to this node. Covers the gateway, DNS server, domain and anycast forms and the statistics time range (Topology); all Settings tabs including the sign-in page's user name and password; Certificate; Cluster (the Node menu and join code); Updates; BGP; History; Statistics; Host; Log; and Power.
- `TestHelpCoversEveryField` fails if a labelled field in `webui/app.js` has no entry in its page's Fields list (the labels are read from the source, so a new field cannot be added without its help).

### Changed
- Updates ▸ Nodes card: the "Refreshes every 2 s." text is gone from the header; the help says the table refreshes every 2 seconds.

### Verified
See the checks run for this release (gofmt, build, vet, race tests, cross-compile, CGO_ENABLED=0, node --check).

### Not verified
- The wording of each default was read from the code, not exercised in a running cluster.

## [v143] - 2026-10-03 — text on the pages that only repeated the help is gone

### Removed
- Under the Topology drawing: the line "circle = gateway address, squares = DNS servers it forwards to, trapezoids = …, parallelograms = …, pills = …". The colour legend (working, degraded, down, not yet known, paused, active) stays.
- On Monitor ▸ Host: the footer "Sampled every 10 seconds and averaged per minute; kept since … for 30 days (saved to disk every 5 minutes)". The memory-guard warning that used to follow it still appears, on its own, when the guard has dropped history; otherwise the footer is empty and hidden.
- On Monitor ▸ Gateways: "The active gateway (AGC) is the one node that owns the shared address; the other nodes (AFN) forward traffic to it or share the load." The help panel explains AGC and AFN.

- On Settings ▸ Gateway groups: "Gateways are added and deleted on the Topology page; this is where their settings are edited." (the help says it).
- On Settings ▸ DNS proxy ▸ Client Rate Limiting: the note under **Over the limit** ("drop sends nothing …; truncate …; refused …; TCP, DoT and DoH clients are always answered REFUSED"). It is already in the help panel's Client Rate Limiting paragraph word for word in substance, so nothing was added there.
- On Configure ▸ History: the paragraph "Every change — from this page, the CLI, a cluster sync or a hand edit of the file — is recorded automatically (the newest 200 are kept). Restoring a version applies it immediately; what was live before is saved first." Most of it was in the help already; the two things that were not — the newest 200 versions are kept, and Restore applies at once — were added to the History help.

### Changed
- Operate ▸ Updates: the upload hint reads "Select a release archive. It is checked and staged on this node; other cluster members pull it from here." (was "Choose the ddgw_vN.tgz (or .zip) release archive. …").

### Verified
- gofmt, go vet, `go test -race -count=1 ./...`; cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`. Live daemon in headless Chromium (light and dark): the Topology legend is the colour dots only; the Host page has no sampling footer (its footer element is hidden); the Gateways page has no AGC/AFN sentence; Settings ▸ Gateway groups and ▸ DNS proxy open and the removed texts are gone; the History page opens without its paragraph; the Updates page shows the new upload hint; no page errors.

### Not verified
- The memory-guard warning on the Host page (needs a daemon that has trimmed history).
- The Statistics page footer ("Counted since … and kept for 30 days …") was left as it is; it says much the same as the Host one did.

## [v142] - 2026-10-03 — less explanation text on the Topology page

### Removed
- The sentence "Mouse wheel zooms the drawing; double-click empty space to fit it to the window again." under the drawing. The zoom, pan and double-click-to-fit behaviour is unchanged and stays described in the README and the help panel; the line under the drawing still names the shapes.
- The status line above the drawing ("Gateway cush.local · 192.168.5.56 · group 1 · IPv4+IPv6 — IPv4: running and answering; IPv6: running and answering"). The circle's colour and its tooltip already say the same (the tooltip still carries the reason and each address family). The place stays available for a message from an action, such as a node that could not be paused, and takes no room while empty.

### Changed
- The legend's blue entry reads **active** instead of "blue line: takes turns (spread)" (the blue line itself is unchanged: the servers currently taking turns under spread).

### Verified
- gofmt, go vet, `go test -race -count=1 ./...`; cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`. Live daemon in headless Chromium (light and dark): neither line is there, the blue legend entry reads "active", the legend no longer mentions the wheel or double-click, the circle's tooltip still gives the reason, no page errors.

### Not verified
- The error message that can still appear above the drawing was not provoked in this run (it was in v140 when pausing a node).

## [v141] - 2026-10-03 — a node turns red when its CPU, memory or disk is over 85%

### Added
- On the Topology drawing a cluster node's shape turns **red** while its **CPU, memory or fullest disk is over 85%** (the same numbers as Monitor ▸ Host: the latest 10-second CPU sample, memory in use, the fullest tracked filesystem) and goes back as soon as all three are at 85% or below. It overrides every other state except "not answering" (a paused node over the limit is red too) and says what is over in the shape ("CPU 91% · disk 88%"); the tooltip adds the disk's mount, and every node's tooltip now has a "Host: CPU …, memory …, disk …" line.
- Nodes report their load to each other in the cluster status message (`host`; older nodes simply do not send it, so they are never red for this). `GET /api/canvas` / `--canvas` carry `host` and `strain` for each node. No new flag or setting: the limit is fixed at 85%.

### Verified
- gofmt, go vet, `go test -race -count=1 ./...` (new: `TestHostLoadStrain` — the load read from a sample, exactly 85% is not over, unknown CPU ignored, short and long wording; `TestCanvasNodeRedWhenStrained` — quiet, 85%, over for this node and a peer, paused and over, back under, unreachable stays "not answering"); cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`.
- Two real daemons in a cluster, headless Chromium: with every CPU burning both shapes turned red and read "CPU 100% …"; after the load stopped the CPU part left the label. (The test machine's own disk was at 91%, so its shapes were red for the disk throughout; the full recovery to green is covered by the test, not seen live.)

### Not verified
- Memory over the limit on a live node (the memory guard in this daemon already trims at 85%, so it is rarely seen above that for long).
- The red follows the sample rate: up to about 10 s for this node, plus a sync interval (5 s by default) for another node.

## [v140] - 2026-10-03 — pause or resume any node from its shape

### Added
- Right-click **any reachable cluster node** on the Topology drawing ▸ **Pause node…** / **Resume node**. Before, only the node being viewed had it. Another node is paused through the cluster (the same call the Power page makes for the node picked in the Node menu, attributed "you via node"); its shape shows "pausing…" / "resuming…" at once and settles when that node reports back (within a sync interval). Pausing asks first; resuming does not.
- The nodes in `GET /api/canvas` / `--canvas` carry `node_paused` (the whole node is paused, as opposed to the gateway being paused or absent there).

### Verified
- gofmt, go vet, `go test -race -count=1 ./...` (`TestCanvasClusterNodes` extended: paused flag for this node, for a peer, and for a gateway the peer does not run); cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`.
- Two real daemons in a cluster, headless Chromium, light and dark: right-click the other node ▸ Pause node… ▸ confirm: "pausing…" then "paused", the menu then offers Resume node, and resuming brought it back; no page errors.

### Not verified
- Pausing a node while viewing a third node's topology (the relay path is the same).

## [v139] - 2026-10-03 — cluster nodes on the Topology drawing; four rows per side

### Added
- **Cluster nodes** are drawn as **parallelograms** to the left of the gateway circle (only with two or more nodes), this node first, each with its name (host name, or the address when two nodes share one) and how it stands for this gateway: green *serving*, amber *not serving*, dashed *paused* (node paused, or the gateway paused/absent there), red dashed *not answering* (its link to the circle is red and dashed too). Hover for address, role, last seen, version (flagged when it differs), whether it has the primary's settings and any update under way. Right-click this node ▸ *Host statistics…*, *Pause node…* / *Resume node*; another node ▸ *Open this node*, *Host statistics…*; an unreachable one ▸ *Cluster page…*.
- **Four rows per side:** anycast addresses (right) and nodes (left) stand four to a column; the fifth starts a new column further out. The drawing widens as needed.
- Peers now report whether they are paused (`node_paused` in the cluster status message; older nodes simply do not say). `GET /api/canvas` and `ddgw --canvas` carry `nodes` per gateway; `--canvas` prints `/_/ cluster node …` lines. No new CLI flag, so the parity table is unchanged.

### Changed
- Anycast addresses can be dragged to any slot of the grid, across columns (before: up and down one column). The legend line names the new shapes.

### Verified
- gofmt, go vet, `go test -race -count=1 ./...` (new: `TestCanvasClusterNodes` — self first with the gateway's colour, peer serving / not serving / gateway absent / node paused / unreachable, a lone node draws none; `TestMarkNodesOnCanvas`); cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`.
- Two real daemons joined into a cluster, headless Chromium, light and dark: parallelograms and their states, tooltips, no overlaps with 7 anycast addresses in two columns; six nodes (four invented through the API response, to see the second column and a long name) with no overlaps; right-click menus; *Pause node…* then *Resume node* from the shape (state and colours followed); *Open this node* switched the Node menu; dragging an anycast pill into the second column saved the new order; no page errors.

### Not verified
- Real clusters of more than two nodes (the extra nodes in the layout check were invented); the grid beyond two columns; touch screens.
- "This node" on the drawing means the node whose Topology you are looking at (the one picked in the Node menu).

## [v138] - 2026-10-03 — a gateway's statistics add up every node

### Added
- **Gateway statistics now cover the whole cluster.** Which node answers a client is up to the clients and the network, so one node's graph had holes whenever the traffic went to the others. The node you ask now sums its own history with that of every reachable peer (new signed peer call `POST /cluster/hist`; buckets are exchanged raw and added, so averages are weighted by their samples, the worst is the worst of all, and counts, cache hits and availability checks add up). The dialog says **"All 3 nodes counted together"**, or **"Nodes counted: 2 of 3 (1 not answering)"** beside the time buttons; `--gateway-stats` prints the same line. Without a cluster, or with clustering off, it is this node alone, as before. A peer on an older version counts as not answering. Servers and domains stay per node (each node probes them itself).
- A minute with data between two minutes without now shows as a **dot**: before, a lone point had no line to draw and looked like nothing was recorded.

### Changed
- `GET /api/serverstats` / `dns.serverstats` results carry `nodes` and `nodes_off`. No new CLI flag (`--gateway-stats` is the counterpart), so the parity table is unchanged. README and help panel updated.

### Verified
- gofmt, go vet, `go test -race -count=1 ./...` (new: `TestGatewayHistoryMergesNodes` — holes filled, weighted average, worst, totals, mismatched range or key refused; `TestClusterHistHandler` — gateways only, own buckets; `TestClusterGatewayStatsAddUpNodes` — two joined nodes over the real signed call, through the operation, and with one node gone); cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`.
- Two real daemons joined into a cluster: queries sent to one node showed identically on both with "All 2 nodes counted together"; after stopping the other, "Nodes counted: 1 of 2 (1 not answering)" in the dialog and the CLI; dialog height unchanged when switching ranges; light and dark; no page errors.

### Not verified
- Three real nodes with traffic on each (the two-node check had traffic on one, because both nodes share the gateway's address on one machine; the adding itself is covered by the tests). Gateways with different group numbers on different nodes are not matched up (the key is the group number, as everywhere else).

## [v137] - 2026-10-03 — drag the empty canvas to pan

### Added
- **Pan:** press on the empty background of the Topology drawing and drag to move around it in any direction (sideways scrolls the drawing's box, up and down scrolls the page). The cursor becomes a hand while it is held; the drag survives the refreshes, and a plain click on the background still deselects.

### Fixed
- Double-click on the empty drawing to fit it again did not always work, because the first click redraws the drawing and the browser then dropped the double-click. The two presses are now timed directly (400 ms, 6 px).

### Changed
- The drawing no longer lets text be selected by dragging over it (it made drags turn into native text drags).

### Verified
- gofmt, go vet, `go test -race -count=1 ./...`; cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`.
- Live daemon in headless Chromium (light and dark): two successive drags in all directions moved the scroll as expected, the panning state showed while held and cleared on release, the position survived a refresh, a plain click still deselected, double-click returned a zoomed drawing to fit with scroll 0.

### Not verified
- Touch screens (mouse only); none beyond that.

## [v136] - 2026-10-03 — mouse-wheel zoom, steady scrollbars, drag to reorder the drawing

### Added
- **Zoom:** the mouse wheel over the Topology drawing zooms it (20% to 300%) around the pointer, keeping the point under the pointer where it is. The zoom is kept across the refreshes and reset when another gateway is picked; a double-click on the empty drawing fits it to the window again. Zooming out does not shrink the box under the pointer.
- **Drag to reorder:** drag a **server** left or right (its domains go with it), a **domain** up or down, or an **anycast address** up or down. The shape follows the pointer, a dashed outline shows where it will land, releasing saves the new order at once, Escape puts it back, and a click without moving still selects. The order is cosmetic: servers are still chosen by speed. Works when zoomed.
- CLI counterpart (the GUI and CLI stay in step): `ddgw --canvas-move server|domain|anycast --group N [--server ADDR] [--name DOMAIN] [--address ADDR] --to N` (1 = leftmost / top). Moving a server or domain on a gateway that follows the shared pool gives the gateway its own copy first, like every other edit. README parity table, help panel and usage updated.

### Fixed
- The sideways scrollbar blinked at every refresh and could not be dragged: the whole scrolling box was rebuilt on each poll. The box is now kept and only the drawing inside it is swapped in one step, so the scrollbar and its position stay put (checked over two refreshes: no frame without the box, its content or its scroll range). The refresh is also held while a shape is being dragged.

### Verified
- gofmt, go vet, `go test -race -count=1 ./...` (new: `TestCanvasMove` — servers, domains, anycast, clamping, errors, shared pool untouched, configuration still valid); cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`.
- Live daemon in headless Chromium (light and dark): wheel zoom in/out with its limits and the pointer anchor (horizontal; vertical only as far as the page can scroll), zoom surviving a refresh, double-click fit; dragging a server, a domain and an anycast address saved the new order to `ddgw.conf`; Escape and a plain click changed nothing; dragging while zoomed landed in the right slot; scroll box identity kept across refreshes; `--canvas-move` for all three kinds and its refusals; no page errors.

### Not verified
- Touch screens (pointer events are used, but the drag was only tried with a mouse); keyboard-only reordering (not provided).
- Dragging in a cluster (the edit goes through the same save path as any other Topology edit).

## [v135] - 2026-10-03 — long server addresses and names fit their boxes

### Fixed
- A server's address and its name, when long, are now cut with "…" to a pixel width measured in the drawing's font, leaving at least 22 px of box on each side (the same treatment the domain trapezoids got in v134; the hover title still has the whole text). Before, they were cut at a fixed number of characters, which could still run over the edge.

### Verified
- gofmt, go vet, `go test -race -count=1 ./...`; cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`.
- Live daemon in headless Chromium (light and dark) with a 36-character server name and a long host name as a server: both cut with "…", 23-24 px margin each side; short ones unchanged; no page errors.

### Not verified
- Fonts other than the browser's default sans-serif and monospace on other platforms (the cut is measured in the font the browser actually uses, so it should adapt).

## [v134] - 2026-10-03 — Add DNS server looks up the name or the address; long domain names fit their trapezoid

### Added
- **Add / Edit DNS server**: fill in the IP address and the **Name** is filled in from its reverse (PTR) record; fill in a host name as the Name and the **address** is looked up (IPv4 preferred). It runs when you leave the field, on the node being configured (new `GET /api/dnslookup`, relayed by the node picker; `ddgw --dns-lookup IP|NAME` on the command line). A field is only filled when it is empty or still holds an earlier lookup, so a name or address you typed is never overwritten, a looked-up name can be changed to anything and is saved as typed, and a looked-up name is cleared again if you change the address to one with no reverse record. A `tls://` or `https://` server is not looked up. A note under the field says what was found or why not.

### Fixed
- A domain name too long for its trapezoid ran over the shape's edges. It is now cut to a pixel width measured in the drawing's font, ending in "…", leaving at least 26 px of shape on each side (the hover title still has the whole name).

### Verified
- gofmt, go vet, `go test -race -count=1 ./...` (new: lookup parsing, both directions via the hosts file, unknown names, refused inputs); cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`.
- Live daemon with a hosts entry, in headless Chromium (light and dark): address → name filled; a typed name kept when the address changes; name → address filled and kept after renaming; unknown address says so and clears a stale fill; saved name is the typed one and survives editing; the long domain name measured 104 px wide in a 156 px trapezoid; no page errors.

### Not verified
- Lookups against a real recursive resolver and real PTR zones (the test used /etc/hosts, same code path through Go's resolver); IPv6 reverse lookups.
- Long server names/addresses in the **squares** were not changed.

## [v133] - 2026-10-03 — Statistics popups: the summary paragraph is gone

### Changed
- The server, domain and gateway Statistics dialogs no longer show the paragraph of totals above the graphs (the graphs and their hover tooltips carry the numbers). The line only appears for "Loading…", "Nothing recorded yet" and load errors. `--server-stats` and `--gateway-stats` still print their totals; the command line has no graphs.

### Verified
- gofmt, go vet, `go test -race -count=1 ./...`; cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`.
- Live daemon in headless Chromium (light and dark): for a server, a gateway and a domain the paragraph is not shown, the charts are there (2, 4, 2), and the box keeps one size and position across the range buttons; no page errors.

### Not verified
- Nothing beyond the above.

## [v132] - 2026-10-03 — Statistics for the gateway and for each domain, with gateway availability

### Added
- Right-click the **gateway circle** ▸ **Statistics…**: four graphs of what clients get — **answer time** (average and worst, cache hits included), **errors** (SERVFAIL/REFUSED answers; clients turned away by the client list or rate limit are not counted), **availability** (the share of the 2-second checks that drive the circle's uptime that found the gateway working; paused and not-yet-started time is not counted) and **queries per second** with the part answered from the cache. One series per gateway covers all its addresses (IPv4, IPv6, anycast) on the node.
- Right-click a **domain trapezoid** ▸ **Statistics…**: latency and failed share of that domain's health probes on that server (probes only, about 12 a minute, so the failure curve is coarse).
- CLI: `ddgw --gateway-stats GROUP` and `ddgw --server-stats ADDR --name DOMAIN [--type T]`; `--server-stats ADDR` is unchanged. Same API route (`GET /api/serverstats`, the `addr` parameter now also takes a gateway or domain key, so the node picker relays it as before). README parity table, help panel and usage text updated.
- Same 7 days, same dialog (✕, range buttons, no resizing) and the same `stats.json.gz` records as the server history; the saved rows grew from 7 to 9 numbers (cache hits, availability samples). Older files load unchanged; older versions ignore the extra numbers.
- Cost on the DNS path: per client query two atomic adds (answered/failed, plus cache hit), and the clock is read for one query in eight.

### Fixed
- A server's history series was forgotten after 7 days with no traffic, while a paused server still held the old one, so its later data would have gone nowhere. Series are kept now; an empty one simply shows "nothing recorded".

### Changed
- The Statistics dialog's summary line has a fixed minimum height so the box does not change height with the wording.

### Verified
- gofmt, go vet, `go test -race -count=1 ./...` (new: gateway and domain series, availability, cache hits, key normalisation, saving and restoring the new counters, the front end feeding its gateway's series); cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`.
- Live daemon under load with lossy stub servers: `--gateway-stats 7`, `--server-stats ADDR --name example.com` and `--server-stats ADDR` print sensible numbers; in headless Chromium (light and dark) the circle's dialog shows 4 charts, a trapezoid's 2, with the range buttons switching without the box moving; no page errors.

### Not verified
- The cost of the extra counters on the DNS path at production query rates (the sandbox has 2 CPUs; the gateway series is one shared pair of counters, so a many-core node is where contention would show).
- Cache-hit graph with real cache traffic (the test load used random names, so the hit line was flat at 0 live; the counting is covered by a unit test).
- Availability across a real outage on a cluster node.

## [v131] - 2026-10-03 — Statistics dialog no longer "boings" when the range is changed

### Fixed
- Choosing Last hour / Last day / Last week closed the dialog and opened a new one, which was briefly short ("Loading…") and then grew again. The dialog is now built once; a range change only updates the pressed button and, when the data arrives, the summary and charts (the old charts stay until then). A late reply for a range that is no longer selected is ignored.

### Verified
- gofmt, go vet, `go test -race -count=1 ./...`; cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`.
- Live daemon in headless Chromium (light and dark): the dialog's top and height were sampled every animation frame while switching ranges four times — a single value throughout; pressed state and charts correct; no page errors.

### Not verified
- Nothing beyond the above.

## [v130] - 2026-10-03 — Statistics dialog: X in the corner, time range buttons like Monitor ▸ Statistics

### Changed
- The server Statistics dialog has no Close button any more: an **✕** sits in its top-right corner and stays there while the dialog scrolls.
- Its Last hour / Last day / Last week buttons use the same segmented style as the range buttons on Monitor ▸ Statistics.

### Verified
- gofmt, go vet, `go test -race -count=1 ./...`; cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`.
- Live daemon in headless Chromium (light and dark, short window so the dialog scrolls): the X stays at the same place after scrolling and closes the dialog; the range buttons switch the view and show the pressed state; no page errors.

### Not verified
- Nothing beyond the above.

## [v129] - 2026-10-03 — server latency/loss history is saved to disk

### Changed
- The per-server history behind Topology ▸ right-click ▸ Statistics… (v128) was memory only. It is now saved with the other statistics in `stats.json.gz` (every 5 minutes and when ddgw stops; same 0600 file, same atomic write) and read back at start-up, so a restart or an update keeps the 7 days; time ddgw was down shows as a gap. The file gets one extra record per server; older files load unchanged and older versions ignore the record. The partial minute under way at shutdown is not saved.
- Docs, help text and CLI comments no longer say "memory only".

### Verified
- gofmt, go build, go vet, `go test -race -count=1 ./...` (new: record round trip with retention cut-off and no duplicates on a second restore; save/load through the real `stats.json.gz`); cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check`.

### Not verified
- A live daemon restart (covered by the file round-trip test only); file size with many servers over a full 7 days (about 10,000 minute rows per busy server before compression).

## [v128] - 2026-10-03 — right-click a server on the Topology → Statistics: latency and loss graph

### Added
- Right-click a DNS server square → **Statistics…** opens graphs of that server's latency (average and worst) and loss over the last hour, day or week, with a one-line summary and a hover tooltip.
- History is kept per server address (so it survives pool rebuilds and is shared by every gateway using that address), fed by live queries and by the health probes. It is held in memory for 7 days and is lost on restart; buckets are 60 s / 5 min / 30 min for the three ranges.
- `ddgw --server-stats ADDR` prints the same summary on the command line; `GET /api/serverstats?addr=&from=&to=` (relayable through the node picker). README, help panel and the CLI/GUI parity table updated.

### Verified
- gofmt, go build, go vet, `go test -race -count=1 ./...`; cross-compile amd64/arm64/arm/386/riscv64; `CGO_ENABLED=0` vet and tests; `node --check` on app.js and help.js.
- Live daemon with a lossy stub: CLI output and the dialog (two charts, range buttons, tooltip, no page errors) in headless Chromium, light and dark.

### Not verified
- Behaviour across a restart (history is intentionally not persisted) and against a real production cluster; the node-picker relay of the new route was covered by the prefix list only, not a live two-node run.

## [v127] - 2026-10-03 — the Topology drawing fits the window and keeps its scroll position

On a gateway with several servers the drawing was wider than the window and scrolled sideways, and a second or two after scrolling it jumped back to the left: the drawing is rebuilt on every poll and the new copy started unscrolled.

### Fixed
- The drawing keeps its scroll position (both directions) across the refreshes, for the same gateway; choosing another gateway starts at the left.

### Changed
- The drawing is scaled to the width of the window it is in (never above its drawn size), and follows the window as it is resized. It shrinks down to 65% of full size; a gateway with more servers than fit at that scale scrolls sideways as before.

### Verified
- Chromium against a live daemon with a gateway of 8 servers: at 1900 px wide the drawing fits the card (about 71%), at 1300 px it fits (65%), at 900 px it holds at 65% and scrolls; scrolled to 200 px it was still at 200 px after 7 s of refreshes; resizing back widens it again; no page errors. Light theme only; the change touches no colours.
- gofmt, build, vet, `go test -race ./...`, cross-builds for amd64, arm64, arm, 386, riscv64, `CGO_ENABLED=0` vet and tests, `node --check webui/app.js`.

### Not verified
- Dark theme and touch devices were not looked at.

## [v126] - 2026-10-03 — a gateway's own pool follows the Settings load balancing; per-gateway override

A gateway drawn on the Topology page has its own DNS pool, and until now that pool ran at the built-in defaults (spread band 20, and so on) whatever Settings ▸ DNS proxy ▸ Load Balancing said: Settings only reached gateways without servers of their own, and the gateway had no controls for it. A spread band of 200 in Settings therefore did nothing for such a gateway, and a server 2× slower than the fastest stayed a fallback.

### Changed
- **The six load-balancing settings (spread, spread band, down at, failures before down, max servers tried, latency smoothing) now come from Settings for every gateway**, including one with its own pool, unless that gateway has its own (`poolFor`). The pool still has its own servers, probe queries, fallbacks, ECS and names.
- **Behaviour change on update:** a gateway pool sitting at exactly the built-in defaults now follows Settings, so a node whose Settings differ from the defaults starts using them for those gateways. A gateway pool that had other values (hand-edited, or copied from Settings when the pool was made) keeps them as its own and shows as "own values".
- Adding a server to a gateway or setting ECS on it (`--canvas-set`, the dialogs) no longer copies today's Settings values into the new pool, so it keeps following Settings.
- Settings ▸ Load Balancing says it applies to every gateway unless the gateway has its own.

### Added
- **Per-gateway load balancing**: Topology ▸ right-click the gateway ▸ *Edit gateway…* ▸ **Load balancing** (*follow Settings* / *own values* and the six values, prefilled with what the gateway runs with now), stored as `"lb": {…}` in the gateway's `dns` block (shared across the cluster; absent unless used, so an older version still reads the file). CLI: `--canvas-set gateway --group N [--spread on|off] [--spread-band PCT] [--down-percent PCT] [--fail-threshold N] [--max-attempts N] [--latency-alpha A]` (any of them makes the gateway use its own, starting from what it uses now) and `--lb settings` to follow Settings again. `--canvas` prints the gateway's own values; `--show-dns` and the DNS page already show what each pool runs with.
- The shared `dns` block may not carry `lb` (refused).

### Verified
- New `lb_test.go`: a pool at the defaults follows Settings, own values win and survive a Settings change, a legacy non-default pool keeps its values, `--canvas-set` flags and `--lb settings` (bad values refused, combination refused, the pool at the defaults again afterwards), a new own pool still follows Settings, validation, `lb` omitted when unused, deep copy, the canvas shows the values in use and whether they are own.
- gofmt, build, vet, `go test -race ./...`, cross-builds for amd64, arm64, arm, 386, riscv64, `CGO_ENABLED=0` vet and tests, `node --check`.
- Two real clustered daemons (native build with PAM): shared band 200 → the gateway pool on both nodes shows 200 (it showed 20 before); `--canvas-set` on the replica reached the primary and both nodes (80); bad values refused; a Settings change to 300 did not move an own gateway and did move one following Settings.
- Chromium (light and dark): the Edit gateway dialog shows own values 80/150, switching to *follow Settings* removes `lb` and resets the pool's values, own 150 saves, a band of 0 is refused in the dialog, no page errors. Test users, group and PAM file removed afterwards.

### Not verified
- Not run against your production cluster; gateways there that already have non-default values will show as "own values".
- Cache, rate limiting, ECS prefixes, timing and listener settings on the Settings page are still shared-pool only: a gateway with its own pool does not follow them (the Client Rate Limiting note says so; the others do not).

## [v125] - 2026-10-03 — the performance profile also tests for timeouts

Occasional client timeouts with all three members up needed a way to see, from a node, whether queries to an upstream server or to the node's own DNS address are lost. A hand test (pinning the VIP to one member's virtual MAC and querying one cached name) only covered the cached path.

### Added
- **`probe.txt` in every performance profile** (Host page ▸ Collect profile, `ddgw --profile`): for the length of the run, 20 queries a second per target and kind (each with a one-second timeout and no retry) go to every upstream server of the node's running gateways and to each running gateway's VIP on its DNS port. Two kinds per target: a random label under the probe domain (cannot be cached, so the upstream resolves it; NXDOMAIN counts as an answer) and the probe domain itself (a cache hit after the first). The report gives sent, answered, timed out (with the seconds into the run), other errors, queries slower than 200 ms and p50/p95/p99/max. A paused node, or one without a running gateway, says it had nothing to test.
- The Host page text, the help topic and the README describe it. No new setting, flag or button.

### Verified
- `TestProbeReportsTimeouts` (a stub that never answers every fifth query shows timeouts, a healthy one none, no targets still says so), `TestProbeTargetsFollowTheConfig` (paused servers and gateways left out, paused node has no targets), and the profile tests now require `probe.txt`.
- gofmt, build, vet, `go test -race ./...`, cross-builds for amd64, arm64, arm, 386, riscv64, `CGO_ENABLED=0` vet and tests, `node --check webui/app.js`.

- A live run of the built daemon (`ddgw --profile --profile-seconds 5` against a gateway on `lo` with a stub upstream): `probe.txt` listed the front end and the upstream, both kinds, all answered, with latencies.

### Not verified
- Not run on a production node or a cluster; whether it finds the cause of the real timeouts depends on the next profile taken there.

## [v124] - 2026-10-03 — the answer cache is split into 16 shards

The v123 profile from ddgw1 (113k q/s in the benchmark) showed the lock wait down to about a ninth of v122's (107 s against 943 s over 20 s), and most of what was left was the one lock around the answer cache (`respCache.get`, 44% of it), followed by the statistics hand-off (7%).

### Changed
- **The cache has 16 shards**, each with its own lock, map and LRU list; a key picks its shard by hash. A cache under 4096 entries stays one shard, so its LRU order is exact (the tests rely on that); a larger one evicts per shard, so it holds up to the limit rounded up to a multiple of 16.
- A cache hit copies the stored answer after the lock is released (a stored message is never modified).
- Memory trimming (`dropOldest`) takes the same fraction from every shard.

### Verified
- New `TestCacheShardedStaysBounded` (8 goroutines on an 8192-entry cache: shard count, stays near the limit, evicts, `dropOldest`); the existing cache and memory-guard tests pass unchanged.
- gofmt, build, vet, `go test -race ./...`, cross-builds for amd64, arm64, arm, 386, riscv64, `CGO_ENABLED=0` vet and tests.
- A loopback smoke run of the built daemon (cached A queries: 138k q/s here, answers correct). The sandbox has 2 CPUs, so it cannot show the gain.

### Not verified
- The throughput gain on the 16-CPU node; that needs a new benchmark run and profile.

## [v123] - 2026-10-03 — a rolling update no longer stalls with two of three nodes paused; UDP workers per reader

With two nodes paused, nothing updated. The one serving node cannot restart (no other member covers its gateways), and the paused nodes waited for it because it sorts earlier in the update order, so everyone waited for everyone.

### Changed
- **Each UDP reader has its own workers and hand-off channel** (4 readers × 16 workers) instead of one channel shared by 64 workers that also each waited on the shutdown channel. The v122 profile from ddgw1 (98k q/s, 16 CPUs) showed the largest remaining lock wait there (`selectgo`/`selunlock`, about 13% of CPU in `selectgo`); the workers now end when their reader closes the channel.

### Fixed
- `shouldWaitForOthers` skips an earlier-sorted member that is itself held back by the keep-every-gateway-served rule (`heldBySafety`, computed from the gateway reports every member already sends; no wire change, older nodes report nothing and are never treated as held). Paused nodes update one after another; the serving node keeps waiting until a member that has updated is resumed and settled.
- The waiting message on the serving node now says the others may be paused and how to proceed (resume an updated node, or `--update-apply --yes`).

### Verified
- New `TestRollingUpdateWithPausedNodes` (three clustered in-process nodes, the serving one sorts first): the first paused node does not wait, the second waits its turn, both are safe to take down, the serving node stays held. `TestHeldMemberDoesNotBlockPausedOnes` covers the rule directly.
- gofmt, build, vet, `go test -race ./...`, cross-builds for amd64, arm64, arm, 386, riscv64, `CGO_ENABLED=0` vet and tests.

### Not verified
- Whether the per-reader workers raise throughput: the sandbox has 2 CPUs, where the A/B (cached A queries, loopback generator) showed no difference beyond noise (about 125–140k q/s both). The gain, if any, needs the 16-CPU node.
- A live run of three real daemons with a serving gateway and two paused nodes updating through the real build and restart.

## [v122] - 2026-10-03 — ANY queries are cached; fewer shared locks on the forwarding path

The v121 profile (ddgw1, 85k q/s, 0 errors) showed the reply-socket wait gone and two things left. First, the cache had recorded no lookups in 11 million queries: a capture on the node showed that dns[bench] was asking type **ANY** (`ANY? google.com.`, type field `00ff`), which ddgw never cached, so every query went to an upstream. Technitium answers ANY from its cache, so the earlier comparison between the two was not like for like. Second, the forwarding path itself waited on shared locks: the server ranking (every query locked every server), the idle upstream sockets (one lock for all of them) and the per-query timeout context (registered with a parent context shared by all queries) were the biggest lock waits in the block profile.

### Changed
- **ANY is cached** like any other query type: it has a key of its own (an ANY answer never answers an A query), lives for the smallest record TTL (or the SOA minimum when there is no data) and is capped by `cache_max_ttl` like the rest. Zone transfers (AXFR, IXFR) are still never cached. Before this a repeated ANY query always went upstream.
- **The server ranking is kept as a snapshot** (`rankedSnap`) that the forwarding path reuses for up to 100 ms instead of taking every server's lock to rank them for every query; a server going up or down (a probe or a failed live query) bumps a generation and is seen at once. The ranking shown on the DNS page is still worked out fresh.
- **Live answers no longer lock the server on every query.** The count of answers is atomic; the latency sample and the failure-streak reset take the server's lock only when a streak has to be cleared or for one answer in eight (the latency is an average, so sampling one in eight at these rates is as good as all of them).
- **The idle upstream sockets are kept in 16 shards**, each with its own lock; a query takes from and gives to whichever shard it finds free first and looks in the others before it dials a new socket. The cap of 256 idle sockets per upstream holds across the shards.
- **No child context for a plain-UDP upstream query**: it needs only the socket's deadline (the one-second-scale timeout was already enforced that way), and the child context registered with the parent for every query. DoT, DoH and TCP exchanges are unchanged.

### Added
- Tests: ANY has its own cache key and is served from the cache, an ANY answer does not answer an A query; the ranking snapshot is reused, refreshed after 100 ms and invalidated by a server going down; the sharded sockets share across shards, keep the cap and never hand one socket to two callers (16 goroutines, `-race`). `BenchmarkRanking`.
- The two spread tests that flip a server's health directly now bump the ranking generation as the real code paths do.

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...`, cross-builds for amd64, arm64, arm, 386 and riscv64, `CGO_ENABLED=0` vet and tests.
- Loopback load test in the 2-core sandbox, v121 against v122 alternately twice (8 workers × 16 in flight, 6 s, a stub upstream, 0 wrong answers): **ANY queries 33–35k q/s (23–25 µs of CPU per query) with v121, 108–110k q/s (8.6–8.8 µs) with v122**; cached A 119–124k against 118–120k (7.8–7.9 µs either way); 50% uncached A 46.7–47.1k against 44.1–45.8k (18.0–18.3 µs against 18.1–18.7 µs). The uncached path is therefore not measurably faster here, as expected: two cores do not contend for those locks, and the saving is meant for a node with many busy cores.
- `BenchmarkRanking` (32 goroutines, 2 CPUs, 6 servers): 395 ns per query when ranking every time, 77 ns with the snapshot.
- Live smoke run (`lo`, three stub upstreams answering after 5, 30 and 80 ms, cache off): queries are answered by the fastest; with the fastest killed the next query is answered by the second at 31 ms and the DNS page shows the new order; a hot reload by atomic rename (one server removed, cache switched on) is picked up and the cache then reports 2 hits and 1 miss; `--show-dns` reads correctly throughout.

### Not verified
- The effect on the production node: the sandbox cannot show the lock contention of 16 busy cores. After installing, run the benchmark with query type **A** (and once with ANY), and click Collect profile during it: with the cache working the profile should be mostly cache hits and the block profile should show far less waiting in `Pool.Ranked`, `udpUpstreams` and `propagateCancel`.
- A benchmark that really needs every ANY query to reach the upstream (to test the upstream) must now use unique names or turn the cache off.
- Only the native amd64 build was run with PAM; the other builds prove only that the code compiles.

## [v121] - 2026-10-03 — Replies no longer queue behind one lock; pause a whole node

A profile taken on ddgw1 while it served about 49k queries a second (v120's Collect profile button) showed what held it at 40–50k while the node was far from out of CPU (about 5 of 16 cores busy): about 850 goroutine-seconds of waiting in 20 s for Go's per-socket write lock. Every reply goes out through the one port-53 socket, `net.UDPConn.WriteTo` holds that lock for the whole `sendto`, and on that kernel a send costs about 16 µs, so one socket cannot send more than roughly 60k replies a second however many cores are free. The same profile showed the statistics recorder's global lock at about 6% of the CPU. The earlier idle profile (ddgw3, 260 ms of CPU in 20 s, about 190 datagrams) contained nothing useful.

### Added
- **Pause a whole node.** Operate ▸ Power ▸ *Take this node out of service* (Pause this node / Resume this node), or `ddgw --node-pause`, `--node-resume`, `--node-status`. It pauses every gateway on the node in one step, including gateways added later: it resigns, stops answering and stops probing, and the other nodes carry the traffic. Nothing is shut down. Until now only a single gateway or server could be paused, so sending all traffic to one node meant shutting the others down. The flag is `node_paused` in the config file: per node, never replicated (not part of the shared settings), kept across restarts, and absent from a config that never used it (older versions reject unknown keys, so it is `omitempty`). The file keeps each gateway's own pause flag, so resuming the node restores exactly what was paused before. With the Node menu the page pauses any member (`/api/nodepause`, relayable like the other pages). The topology shows the gateways as paused with "This node is paused"; config history records "Node: paused / resumed"; the Settings page and the Topology page carry the flag through their saves untouched.
- `BenchmarkReplySend` (`go test -run xxx -bench ReplySend -cpu 2`): replies from many goroutines on one socket, `WriteTo` against the new replier.

### Changed
- **Replies are sent without Go's per-socket write lock.** The frontend sends them with `sendto` directly on a duplicate of the listening socket's descriptor (`udpreply.go`). The duplicate shares the socket (same port, buffers and non-blocking mode); the kernel handles concurrent sends on one UDP socket. A send that would block, a zone address or anything unexpected falls back to the ordinary `WriteTo`. The duplicate is closed, under a lock that waits for sends in progress, when the readers end, so it never outlives the socket or leaves the port bound.
- **Query statistics are counted in batches.** Workers append an event to one of 16 small buffers (each with its own lock) instead of taking the collector's single lock twice per query (once for the query, once for the cache lookup); the events are counted together every 200 ms, or at once when anything reads, saves or prunes the statistics. The numbers are the same; they reach the tables up to 200 ms late. A shard holds at most 32768 events and a full one is counted by the caller.

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...`, cross-builds for amd64, arm64, arm, 386 and riscv64, `CGO_ENABLED=0` vet and tests. New tests: the replier delivers from the listening address under 8 concurrent senders, falls back after close and for a non-UDP connection, and releases the port; batched statistics count every event (8 goroutines × 500 queries and cache lookups) before any read, and the timer counts a lone event with no read; node pause: effective config pauses every gateway and keeps each file flag, the flag is local and optional in the JSON, a paused node starts no engines or pools and the canvas says so, pause/resume through `Mgmt` and the op, history wording, and the new route needs a session and CSRF and is relayable.
- Load test on loopback in the 2-core sandbox (a pipelined generator, 8 workers × 16 in flight, 8 s, a stub upstream), v120 against v121 alternately, twice each: all cached 108–113k q/s against 103–112k, 50% misses 47–50k against 45–46k; CPU per answered query 8.2–8.4 µs against 8.3–8.4 µs cached and 17.4–18.1 µs against 18.3–18.6 µs with misses; 0 wrong answers either way. So there is no regression worth speaking of and also no gain here: on 2 cores the send lock is hardly contended. What the lock costs is in the microbenchmark (32 goroutines sending on one socket, 2 CPUs): 3011 ns per reply with `WriteTo`, 1280 ns with the replier.
- Live, node pause: a real daemon on `lo` answers, `--node-pause` stops the answers within 3 s and the `ddgw7.1`/VIP addresses go away, `--canvas` shows `[PAUS]` with the node wording, pausing twice says it already is, `--node-resume` brings the answers back; the config file gets and loses `node_paused`; history lists both. Two clustered daemons (join code): pausing one leaves the other's file untouched, and a shared edit made on the primary (a new probe domain) reaches the paused replica without clearing its flag.
- Real PAM check (group, two users, `/etc/pam.d/ddgw` from contrib): the member logs in; a wrong password and a valid user outside the group get 401; root hit the login throttle (429) after those failures; users, group and PAM file removed afterwards.
- Real Chromium (light and dark) against the live daemons: the Power page card shows the state, the button asks for confirmation and pauses, the topology shows the node as paused, a Settings change (log level) saved with the node paused keeps `node_paused` in the file, the Node menu reaches the other node, shows it paused and resumes it, no console errors besides the usual 401 of the session check before login.

### Not verified
- **That this lifts the production node's ceiling.** The sandbox cannot reproduce a send path of 16 µs with 16 cores contending; the gain is predicted by the profile (the lock wait) and the microbenchmark, not measured on the node. Run the benchmark again and click Collect profile during it: the block profile should no longer show `WriteToInet4` / `fdMutex.rwlock`, and if the node is still at 40–50k the next hot spot will show in the new CPU profile.
- The node shows 16 CPUs with no cgroup limit although it was given 8 cores; ddgw runs 16 scheduler threads there. Not changed.
- The cgo-off cross-compiles prove the code compiles for arm64, arm, 386 and riscv64, not that a PAM build works there. Only the native amd64 build was run with PAM.

## [v120] - 2026-10-02 — One button: a performance profile from the Host page

v119's profiling endpoint needed an environment variable, a restart and three `curl` commands. It is replaced by a button.

### Added
- **Host page ▸ Performance profile ▸ Collect profile (20 s).** Start the load, click, and 20 seconds later the browser downloads `ddgw-profile-<host>-<time>.tgz`. It holds the CPU profile, the lock-wait (mutex and block) profiles, heap and goroutine dumps, an `info.txt` (version, CPUs, GOMAXPROCS, CPU model, cgroup limit, load average) and the kernel's `/proc/net/snmp` (UDP drops), `softnet_stat`, `softirqs`, `stat`, `loadavg` and CPU pressure from before and after the run, which show kernel-side drops and which CPUs do the network work. No setting, no restart; one recording at a time; this node only (the Node menu does not apply); at most 60 seconds.
- **`ddgw --profile [--profile-seconds N]`** does the same from the command line and saves the file in the current directory (root only, like the other management commands; `profile.collect` over the status socket).

### Removed
- The `DDGW_PPROF` environment variable and its listener from v119 (nothing else used them).

### Fixed
- The Statistics and Host page footers showed the word "null" after the last sentence (the memory-guard note was appended as `null` when there was nothing to say, and as an array rendered as text when there was); both now show the note or nothing.

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...`, cross-builds, `CGO_ENABLED=0` vet and tests; tests: archive contents, one profile at a time, the web route needs a session and the CSRF token (added to the existing route table test) and returns the file, a profile is readable by `go tool pprof`.
- Real PAM check (group and two users, `/etc/pam.d/ddgw` from contrib): the group member logs in; a wrong password and a valid user outside the group get the same refusal; users, group and PAM file removed afterwards.
- Real Chromium (light and dark): login, Monitor ▸ Host, click the button, the file downloads after 20.4 s with the success message, the button is enabled again, no console errors beyond the usual 401 of the session check; `--profile --profile-seconds 3` from the command line saves a valid archive.

### Not verified
- That the profile explains the production node's throughput: that is what it is for. A profile taken during an idle run (as here) contains almost nothing.

## [v119] - 2026-10-02 — Optional profiling endpoint, to find out where a node spends its time

Benchmark run on the ddgw node itself against the VIP (so the traffic goes over loopback and not through the container's network path): 43.3k queries a second at p95 5.0 ms, 0 errors, against 37–42k from another host. The network path is therefore not what limits the node: the same build does about 90k a second on a 2-core sandbox, so something on the node, in the daemon or in how it runs there, costs about twice what it does here, and it cannot be seen from outside.

### Added
- **`DDGW_PPROF=127.0.0.1:6060` turns on Go's profiling endpoint** (CPU, heap, goroutines, and with it on, lock-wait profiles). Off unless the variable is set; only a loopback address is accepted (anything else is ignored with a warning); no authentication, its own listener and handler, nothing to do with the web GUI or the cluster port; it logs a warning while it is on. How to use it is in the README under DNS proxy.

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...`, cross-builds, `CGO_ENABLED=0` vet and tests; tests: only loopback addresses are accepted, nothing starts unless asked, and the endpoint answers when started.

### Not verified
- Nothing about throughput changes in this version; the profile from the node is what is needed next.

## [v118] - 2026-10-02 — Back to one UDP socket (v117's several sockets did not help)

### Changed
- **The VIP's UDP port is served by one socket again**, as in v116 (four reader goroutines on it, 64 long-lived workers, the 8 MiB receive buffer). v117 listened on up to eight sockets bound with `SO_REUSEPORT`, on the theory that Go's per-socket lock around every send was serialising the replies. On the production node the same 16-worker test gave 39.1k queries a second at p95 14.1 ms with 127 errors (0.01%) on v117, against 41.9k at p95 6.7 ms with no errors on v116: no gain, and worse latency and the first errors since v114. The hash that picks a socket by client address and port spreads a few clients unevenly, which is a likely reason, but that was not confirmed. Nothing else changed from v116; the Go lock is real, but it is evidently not what limits throughput here.

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...`, cross-builds, `CGO_ENABLED=0` vet and tests. The two v117 tests that remain: a port held by another socket still fails the start.

### Not verified
- That this restores v116's numbers on the production node (the code is v116's UDP path again, but it was not benchmarked there). What limits throughput at about 42k queries a second on the 8-core node is still unknown: the counters before and after a run (`nstat -az UdpInErrors UdpRcvbufErrors UdpSndbufErrors`), `top -H -p $(pidof ddgw)` during it, and the same benchmark run on the node itself against the VIP (loopback, skipping the container's network path) would show whether it is the daemon or the path to it.

## [v117] - 2026-10-02 — UDP replies can leave in parallel: several sockets on the same address and port

Benchmark on the 8-core node with v116 (16 workers, 16 queries in flight each, cache hits): 41.9k queries a second at p95 6.7 ms against 59.7k at p95 12.9 ms for the plain resolver. The resolver had stopped at about 53k in the earlier runs, so that was not a client limit. With 256 queries in flight, 41.9k a second means every query spends about 6 ms in the system and the latency stays tight, which is what a single queue being worked off at a fixed rate looks like, not a CPU running out (the daemon uses a fraction of one core at that rate).

### Changed
- **The VIP's UDP port is now served by several sockets** (one per CPU the process may use, 1 to 8), all bound to the same address and port with `SO_REUSEPORT`; the kernel spreads incoming queries over them by client address and port, and each socket is read by its own goroutine and answers through itself. Reason: Go's network library holds a per-socket lock for the whole of each `sendto` and `recvfrom` (`poll.FD.WriteToInet4` takes the write lock for the duration of the call). With one socket every reply therefore leaves one at a time, and the call includes the kernel's whole transmit path, which inside a container (veth, bridge, macvlan) is several times what it is on loopback. A resolver that sends from several threads at once does not have that limit.
- If `SO_REUSEPORT` cannot be used the frontend falls back to one ordinary socket, and if only some of the extra sockets can be opened it logs how many it got. A port already held by a socket without `SO_REUSEPORT` still makes the start fail, as before. TCP, DoT and DoH are unchanged. A client with a single source port still lands on one socket (so a benchmark with one worker sees no change); many clients, or one client with many source ports, spread.

### Measured (2 cores, shared with the load generator and the fake upstream, so it cannot show the parallel send)
- No change in this sandbox: 91–97k queries a second and 8.9–9.4 s of CPU per 0.92–0.97M queries for both versions (differences within the run-to-run noise). The gain this is meant to give needs several cores and a real transmit path, which the sandbox does not have.

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...`, cross-builds, `CGO_ENABLED=0` vet and tests; new tests: with four sockets, 200 clients on different source ports are all answered correctly and stopping releases the port; a taken port still fails Start.

### Not verified
- That this raises the production numbers: it is a conclusion from the profile and from reading the Go source, not something I could measure here. If the 8-core node still tops out near 42k queries a second, `top -H -p $(pidof ddgw)` during a run would show whether all of the daemon's threads are busy; if they are idle, the limit is further out (the macvlan interface or the hypervisor's network path).
- IPv6 VIP sockets use the same code path but were not run.

## [v116] - 2026-10-02 — Cache-hit path: fewer allocations and no goroutine start per query

With the production nodes raised to 8 cores the benchmark (8 workers, 32 queries in flight each, a small set of names, all cache hits) went from 25.8k to 36.6k queries a second with p95 6.6 ms (the plain resolver on another node: 53.1k, p95 21.5 ms), so v115's CPU-limit change was the main cap. This version trims what the daemon itself spends per answered query.

### Changed
- **The question of a query is read with one allocation instead of several.** `questionOf` ran for every query (cache key, statistics) and built the name with a growing buffer and then lower-cased it into a second string; it now fills a stack buffer, lower-cases as it copies and makes the string once. The profile had it at 16% of the CPU on a cache hit. A test compares it with the previous implementation on 200,000 random and malformed messages (compression pointers, over-long names, truncation).
- **UDP queries are answered by 64 long-lived workers** that are handed a query only when idle right now, and by a goroutine per query (as before, up to 8192) when they are all busy. A fresh goroutine had to grow its stack to the depth of the resolver for every query, 11% of the CPU at 90k queries a second. The hand-off is unbuffered on purpose: a buffer would queue queries behind workers stuck on a slow upstream, and a test (150 queries against a one-second upstream finish in about a second) fails if it is made one.

### Measured (2 cores shared with the load generator and upstream, same pipelined pattern, cache hits)
- CPU time per answered query 10.2–10.4 µs before, 9.5–9.7 µs after (about 8% less); 88–91k → 96–97k queries a second. Nearly half of what remains is the kernel's send and receive.

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...`, cross-builds, `CGO_ENABLED=0` vet and tests.

### Not verified
- On the production nodes. At 36.6k queries a second on 8 cores the daemon should be using well under one core, so something other than its CPU is limiting; that is not visible from here. With `top -H -p $(pidof ddgw)` and `vmstat 1 5` taken during a run, and the benchmark also run with more workers or from a second host, the limit shows up (a client that tops out at 53k would also explain why the other resolver always lands there).

## [v115] - 2026-10-02 — Respect a container's CPU limit

After v114 the benchmark showed no errors (25.8k q/s, 0 errors, p95 12.9 ms against a plain resolver's 52k on another node). Rebuilding the benchmark's pattern here (8 workers, 32 queries in flight each, a small set of names cycled, so every query is a cache hit) gave about 98k q/s from the same build on a 2-core sandbox that was also running the load generator and the upstream, so the daemon's own per-query cost does not explain 25k. A likely cause on the production nodes is the container: Go 1.24 sizes its scheduler by the host's CPU count and ignores a cgroup CPU quota, so in an LXC container (or a systemd unit, or Docker) limited to a few CPUs it runs one thread per host core, all of them share the quota, and the kernel throttles the group in bursts.

### Changed
- At start-up ddgw reads the CPU quota of its cgroup (`/sys/fs/cgroup/cpu.max`, or the v1 `cpu.cfs_quota_us`/`cpu.cfs_period_us`) and, when it allows fewer CPUs than the machine has, sets `GOMAXPROCS` to the quota rounded up (at least 1). The daemon logs `CPU limit of N in this cgroup: using M scheduler threads instead of K` once. A `GOMAXPROCS` set in the environment is left alone, and nothing changes where there is no quota. CLI commands do not log it.

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...`, cross-builds, `CGO_ENABLED=0` vet and tests; unit tests for parsing v2 and v1 limits, "max"/unlimited, and the rounding.

### Not verified
- That this is the cause on the production nodes: the sandbox has no CPU quota, so the effect itself was not measured, only the parsing and the decision. If the log line does not appear on a node, there is no quota there and the gap is elsewhere; `nproc`, `cat /sys/fs/cgroup/cpu.max` and `top -H` during a run would show where.

## [v114] - 2026-10-02 — DNS proxy: much faster on queries that go upstream, and fewer dropped queries under load

A client benchmark (8 workers, 30 s) against the VIP reached 19k queries a second with 43 errors, where a plain resolver on the same network did 51k with none. Profiling the proxy on queries that miss the cache (every query goes to an upstream) showed about half the CPU in one place: each forwarded UDP query opened a new socket, connected it, and closed it again, and allocated a 64 KiB read buffer; the garbage collector then spent another quarter of the time sweeping those buffers. Answers served from the cache were already fast (the time there is the kernel's send and receive).

### Changed
- **Upstream UDP sockets are kept and reused**, per upstream (up to 256 idle, dropped after 30 s idle). A socket that timed out or failed is closed, never reused, so a late answer cannot meet the next query; a reused socket that fails at once (an old "port unreachable") is retried once on a fresh one. A reply is accepted only if its ID *and* its question match the query (an answer without a question section, such as some FORMERR replies and update acknowledgements, is still accepted). Read buffers come from a pool.
- **Identical questions that miss the cache while the same one is already on its way upstream wait for that answer** instead of each sending their own. Each client still gets its own ID and the spelling of the name it used; if the first answer cannot be cached (SERVFAIL, TTL 0) the others send their own query as before. Counted as cache hits.
- **Four goroutines read the UDP socket** instead of one, and its receive buffer is raised to 8 MiB (the privileged call that ignores `net.core.rmem_max`, falling back to the ordinary one). A burst that arrives while the reader is busy waits in the buffer instead of being dropped by the kernel, which a client sees as a timeout.
- **Queries in flight at once: 1024 → 8192.** Beyond the limit the proxy drops queries silently; with an upstream that stalls for a second at 19k queries a second, 1024 is reached in about 50 ms.

### Measured (2 cores, load generator and fake upstream on the same machine, so the absolute numbers are low; the ratio is the point)
- Queries that go upstream, 8 workers: 9.1k → 16.9k a second; with 64 workers 9k → 24k. Cache hits: about 48k both before and after (limited by the generator on the shared cores).
- 16 workers against two upstreams, the fast one killed under load: 15 failures out of 49k queries while it is being marked down, then everything goes to the slow one; with the fast one back it ranks first again within ~12 s. Hot reload by atomic rename of the config: the server list changed with no failed queries.

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...`; new tests: socket reuse (8 queries, one source port), an answer to another question with the same ID is ignored, an error answer without a question is accepted, a timed-out socket is discarded, 20 identical clients cause one upstream query (fails with the sharing removed).
- Cross-builds for amd64, arm64, arm, 386, riscv64.

### Not verified
- On your servers: whether the 43 errors were dropped bursts (the larger buffer, more readers and higher limit address that), upstream stalls (they do not: a query to a stalled upstream still waits its `query_timeout_ms` before falling back), or something else. Run the same benchmark again; if errors remain, `ddgw --show-dns` (SERVFAIL count) and `nstat -az UdpInErrors UdpRcvbufErrors` on the node during the run say which.
- Real upstream resolvers (the test upstream answers instantly); TCP, DoT and DoH forwarding were not changed.

## [v113] - 2026-10-02 — A controller keeps the MACs it covers pointed at itself; a 16-node cluster tested

Found by running sixteen nodes in network namespaces on one bridge, all started together, then stopping the controller and killing a forwarder.

### Fixed
- **A controller that covered a dead node's MAC could lose it on the switch.** The announcement on takeover was sent once. A frame still in flight from the node that went away (the old controller was still answering queries sent to its MAC while it shut down) moved the switch entry back to the dead node's port, and nothing corrected it: queries to the old controller's MAC went to a stopped node (0 of 10 answered, the bridge entry still on the stopped node's port). The controller now repeats the announcement for every slot it covers every 2 s, like a forwarder does for its own (v112 said a controller did not). After the change, queries to the old controller's MAC are answered 10 of 10 after a controller stop.
- (Carried in this version from work after v112) a covering interface gets the same link-local address and loose `rp_filter` as a forwarder's, so queries from clients that cached a dead node's MAC are not dropped by the reverse-path check (`TestTakeoverInterfaceAcceptsQueries`).

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...`; `TestForwarderRepeatsItsAnnouncement` now also covers the controller repeating for covered slots.
- Live, 16 nodes with production-like `rp_filter`: all started together, one controller, 16 distinct virtual MACs and each answers 10 of 10; a controller stop leaves the old controller MAC answering 10 of 10; a forwarder that disappears with its host (interface removed, process killed) is answered through its MAC again after about 1.5 s.
- A forwarder process killed with `kill -9` while its interface stays up on a live host took 6–9 s to recover: the leftover interface keeps answering (ICMP port unreachable from the MAC) and moves the switch entry back. That is a test artifact of leaving the interface behind; a restarted daemon removes it.

### Not verified
- IPv6 at this size, a real switch, the production cluster. A node that becomes controller covers only slots it sees go missing after it is controller; a slot that was lost earlier stays unanswered until clients re-ARP.

## [v112] - 2026-10-02 — More than three nodes: a joining node no longer takes the controller role, and answers no longer move a forwarder's MAC

Found by running six and then eight nodes in network namespaces (all started together, then two more joining one at a time) after v111 had fixed the three-node cluster. Each bug below also shows up with three nodes.

### Fixed
- **A joining node took the controller role from the incumbent whenever it ranked highest.** Hellos heard while a node was LISTENING were stored as peers but never read for "this one is the controller"; only hellos heard in SPEAK were. At the end of SPEAK the election therefore saw no incumbent, and a newcomer with the highest address (or priority) won and the incumbent yielded — every restart of the highest node moved the controller and its MAC twice. The README has always said a joining node becomes a forwarder; it now does (`stateListen` records the controller like the other states). This is also what produced the "Won AGC election" lines right after the restarts in the production logs; I had put them down to late multicast, which was wrong.
- **ARP and neighbour answers moved a forwarder's MAC to the controller's switch port.** The controller answered with the slot's virtual MAC as the Ethernet *source* of the frame, so every answer taught the switch that the forwarder's MAC lives on the controller's port, and queries sent to that forwarder were delivered to the wrong node until it next transmitted (up to minutes: bridge/switch entries age out at about 300 s). The answer still names the slot's MAC in its payload (ARP sender hardware address, neighbour advertisement target link-layer option, which is what clients cache) but is now sent from the node's own interface MAC. Seen in the six-node run as three of five forwarder MACs pointing at the controller's port and 0 of 20 queries answered through them; after the fix all six MACs answer 20 of 20 and, with real ARP resolution and the neighbour entry flushed before each query (the controller handing out different slots), 60 of 60.
- **A forwarder repeats its virtual-MAC announcement every 2 s** (an ARP probe with sender 0.0.0.0, which no host caches) so a switch entry that does go stale is corrected in seconds instead of when it ages out. A controller does not.

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...`; new tests `TestControllerHeardWhileListeningIsRespected` (fails with the listen case removed), `TestARPAnswerIsSentFromTheNodesOwnMAC`, `TestNAAnswerIsSentFromTheNodesOwnMAC`, `TestForwarderRepeatsItsAnnouncement`.
- Live, with `rp_filter` as on the production nodes: six nodes started together converge to one controller with distinct slots and no duplicate MAC, and every virtual MAC answers 20 of 20; eight nodes (the last two joining one at a time, both ranking above the controller) leave the original controller in place (no yield logged on it), slots 1-8 each on one node, and every MAC answers 15 of 15.

### Not verified
- IPv6 with more than three nodes (the sandbox has none; the same code paths apply), a real switch, and the production cluster.

## [v111] - 2026-10-02 — Forwarders check and set the reverse-path and ARP settings on start-up

### Changed
- When a forwarder (or controller) comes up for IPv4, ddgw now checks `rp_filter` on the virtual-MAC interface, its parent interface and `net.ipv4.conf.all`: wherever it is strict (1) it is set to loose (2), and what was changed is logged once (`rp_filter was strict on [all eth0]: set to loose (2)…`). v110 only changed the virtual-MAC interface, and its changelog and a warning wrongly said a strict global value could not be overridden: the kernel uses the larger of the interface's value and the global one, so the per-interface 2 already won (checked in a network namespace: global 1 + interface 2 + link-local address answers; global 1 + interface 1 drops). The global and parent changes are made so an administrator reading `sysctl` does not find a strict value that looks like the cause. They are not undone when ddgw stops, like `arp_ignore`/`arp_announce`, which it also sets on the parent, the virtual-MAC interface and `all`.
- **Every one of these settings is now read back after it is written**, and ddgw logs a warning when one did not take (a container that may not write sysctls, say), instead of failing silently: `could not set arp_ignore/arp_announce on …` (the node might then answer ARP for the VIP) and `rp_filter is strict on … and could not be changed`.
- Never set to 0 (off): loose is enough once the interface has an address, and strict-to-loose is the smallest change that works.

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...` (`TestRelaxReversePath`: strict becomes loose on all three, anything else is untouched, a setting that cannot be written is reported); the kernel behaviour above in a network namespace.

### Not verified
- On the production cluster (it has the global value at 2, where v110 already worked), and in an unprivileged container, where the new warnings are what you would see.

## [v110] - 2026-10-02 — Forwarders answer: the kernel dropped every query that arrived on their virtual MAC

Found with a per-MAC test against the production cluster: queries sent to the controller's MAC were answered, queries sent to a forwarder's MAC all timed out, and `ddgw --show-dns` on that forwarder showed 0 queries ever received although its listener, its VIP on `lo` and its interface were all in place. This is why the cluster seemed fine with two nodes and broke with three: a routed client uses whichever MAC the controller's ARP answer last gave the router, and any change to a forwarder's MAC meant a total outage for it until the router's entry changed again.

### Fixed
- **A forwarder's virtual-MAC interface dropped every query.** A packet for the VIP arrives on that interface, and the kernel's reverse-path check rejects anything that arrives on an interface with no IPv4 address whenever `rp_filter` is not 0 for it — loose mode (2) included, and the effective value is the larger of the interface's and `net.ipv4.conf.all.rp_filter`. The forwarder's interface has no address (the VIP is on `lo`), and the system had `all` = 2. Nothing was logged or counted as an error. The interface now gets a link-local `/32` (`169.254.<group>.<slot>`, no route; `arp_ignore` is on, so it is never advertised), which is enough for loose mode to accept the packet.
- **A newly created interface can inherit strict filtering** (`net.ipv4.conf.default.rp_filter` = 1 on some systems), which dropped the controller's queries too. The interface of every virtual MAC (controller and forwarder) is now set to loose when it is strict. If `net.ipv4.conf.all.rp_filter` itself is 1, nothing per interface can undo it, so ddgw logs a warning saying so.

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...`; new `TestRelaxReversePath`.
- Live, a controller and a forwarder in network namespaces with `rp_filter` all = 2 and default = 1 (as on the production nodes), a client pinned to each MAC in turn: v109 answered 10/10 through the controller's MAC and 0/10 through the forwarder's; this build answered 10/10 through both. Checked the kernel's behaviour directly first: with no address on the interface, `rp_filter` 2 and 1 dropped, 0 passed; with an address, 2 passed and 1 dropped.

### Not verified
- On the production cluster itself, and IPv6 (the reverse-path rule above is IPv4 only; IPv6 forwarders have no equivalent filter unless an ip6tables `rpfilter` rule is configured).
## [v109] - 2026-10-02 — IPv4 and IPv6 controllers on different nodes no longer share slot 1

Follow-up to v108 from the same cluster: after v108 it answered 998 of 1000 queries. The nodes' `--show-gateways` showed why: node 3 was the IPv4 controller and node 2 the IPv6 controller, both in slot 1, which is one MAC (`00:1a:7c:01:01:00`) answering on two switch ports. The two families rank nodes by their own addresses, so after a restart they often elect different nodes; the switch kept moving the MAC between the two ports and a share of the IPv4 queries went to a node with no IPv4 VIP on it.

### Fixed
- **The IPv6 controller moves to a free slot when the node's IPv4 side hears a live node in the slot it holds** (`resolveCrossFamilyLocked`, checked on every packet and every reap tick). The IPv4 controller keeps slot 1, so exactly one side moves; the VIP is re-announced on the new slot's MAC. A user of the slot that has gone quiet (not heard for half the hold time) does not count, so a controller that is on its way out does not make the other family move.
- **A MAC covered for a dead peer is released when the other family hears that slot's owner alive** (it had been announced from a second port, taking the real owner's traffic).

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...`; new tests `TestV6ControllerMovesOffSlotTheV4ControllerUses` (moves to a free slot with the VIP on it; stays when the other controller has gone quiet; the IPv4 side never moves) and `TestTakeoverReleasedWhenOtherFamilyHearsOwner`; each fix removed in turn failed its test.
- Live, three namespaces (IPv4 only, as in v108): the same controller-conflict scenario still ends with one controller, distinct slots and MACs on every node, and the VIP answering 20/20 through each of the three virtual MACs.

### Not verified
- The new rule itself on a live wire: it needs IPv6, which the sandbox lacks, so it is covered by unit tests only. Not run on the production cluster or a real switch.
## [v108] - 2026-10-02 — Three nodes: a controller that yields no longer keeps slot 1; shared MAC interfaces; slot clashes

Found from a production cluster that worked with one or two nodes and broke when the third joined. The `--show-gateways` output of the three nodes showed two nodes in slot 1 (the same virtual MAC on two switch ports), nodes disagreeing about who the controller was, and an IPv6 controller whose interface had just been deleted by the IPv4 side of the same node.

### Fixed
- **A controller that yielded kept slot 1.** It went on sending hellos with slot 1, which every other node reads as "I am a controller", and it kept the virtual MAC `…:01:00` that the new controller now owns. It now gives slot 1 up and takes a free forwarder slot (also when a slot it kept from an earlier takeover is used by someone else).
- **The IPv4 engine deleted the interface the IPv6 engine was still using.** Both engines of a group share the macvlan of a slot; yielding the controller role in one family ran `ip link del` on it, which removed the other family's VIP too. Each slot's interface now has users (a registry in `vmacreg.go`); it is deleted only by the last one, and an engine that stops being controller takes only its own VIP (and route) off it.
- **Forwarders could follow the wrong controller.** `agcIP` was set once or by an election among the peers heard, and never corrected, so after a missed hello a forwarder could name a node that is not the controller and then not notice when the real one went away. Every controller hello now corrects it; with two controllers the higher-ranked is followed.
- **Two nodes on one slot.** Slots were chosen from what one family's peers used; a node's IPv4 slot could equal another node's IPv6 slot (one MAC, two ports). A joining engine now avoids slots in use in the node's other family and prefers the slot its sibling already has, and two forwarders that end up on the same slot settle it by rank (the lower one moves; anyone moves off a controller's slot).

### Verified
- `gofmt -l`, `go build`, `go vet`, `go test -race -count=1 ./...` (new `engine_slot_test.go`: yielding controller leaves slot 1 and takes a free one, macvlan deleted only by its last user and the yielder's VIP removed, join avoids the other family's slots and prefers the sibling's, forwarder moves off a clashing slot (higher-ranked, lower-ranked, controller), forwarder follows controller hellos incl. two controllers, free-slot range). Each fix was removed in turn and its test failed.
- Live, three network namespaces on a bridge: nodes 1 and 2 up (controller slot 1, forwarder slot 2), node 3 started with multicast blocked inbound so it won by default, then unblocked: node 1 yielded, took slot 3 (`ddgw1.3` only, `ddgw1.1` gone, VIP only on `lo`), all three nodes then showed the same roles and slots, and the VIP answered 5/5 through each of the three virtual MACs.
- Cross-compiles (amd64, arm64, arm, 386, riscv64) and `CGO_ENABLED=0 go vet/test`.

### Not verified
- IPv6 (the sandbox has none), a real switch with IGMP/MLD snooping, and the production cluster itself. Why the third node's IPv4 side did not hear the controller before its hold time ran out is not known; if it keeps happening, unicast mode (list the other nodes under Neighbors) takes multicast out of the picture. IPv4 and IPv6 elections are still independent: one family can still elect a different controller node than the other.

## [v107] - 2026-10-02 — Section names; client subnet (ECS) is on by default

### Changed
- Settings ▸ DNS proxy sections renamed: **Choosing a server** is now **Load Balancing**, **Listening for clients** is now **Listeners**. Names only.
- **Client subnet (ECS) is on by default** (`defaultDNS`, the "Pass the client's network to the servers" box, and a new gateway on the Topology page starts from the shared setting instead of "off"). It is safe to default now: a server that answers FORMERR or REFUSED to a query with ECS is asked again without it and then left alone (v100). What this means for an upgrade: a `dns` block that has an `"ecs"` key keeps it (the GUI always wrote one, so a setup saved from the GUI keeps whatever it had); a block with no key — a hand-written config, or a gateway's own `dns` block that never mentioned it — now gets ECS on. Two side effects to know: upstream servers now see the client's /24 (IPv4) or /56 (IPv6), and with ECS on the cache keeps separate entries per client network, so its hit rate is lower than with it off. Turn it off in Settings ▸ DNS proxy ▸ Client network, or per gateway.
- README, `ddgw --help` and `--show-dns` wording follow.
- Tests that assumed ECS off now say so explicitly (`TestFrontendAnswersFromCache` turns it off because it shares one answer between clients on different networks; the canvas test sets the shared pool to off); new `TestECSOnByDefaultInFiles` (no key → on, `"ecs": false` stays off).

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`.
- Chromium with a real PAM login on a config that has no `ecs` key: the box is checked and `--show-config` says `"ecs": true`.

### Not verified
- The new section names were read from the page's heading list, not looked at in a screenshot.
- ECS on by default against real upstream servers other than the REFUSED case behind v100.

## [v106] - 2026-10-02 — The "Who may ask" settings section is now "Client Rate Limiting"

### Changed
- Renamed the Settings ▸ DNS proxy section (and the help topic and README entry) from "Who may ask" to **Client Rate Limiting**. Names only; the settings, keys and behaviour are as in v105.

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`.

### Not verified
- Not opened in a browser: a text change in one section heading.

## [v105] - 2026-10-02 — Who may ask, and how often: allowed clients and a per-client rate limit

### Added
- **Allowed clients** (`allowed_clients` in the `dns` block, shared or a gateway's own): networks (`10.0.0.0/8`, `192.168.1.5`, `2001:db8::/32`) that may use the proxy. Empty, the default, is everyone, so nothing changes for an existing setup. Any other client is answered REFUSED over UDP, TCP, DoT and DoH, and dynamic DNS updates are held to the list too (they were relayed to the zone's primary for any client before).
- **Per-client rate limit**: `client_rate` queries per second per client (an IPv4 address or an IPv6 /64; 0 = off), `client_burst` queries at once (0 = twice the rate, at least 10), `client_action` for a client over its rate: `drop` (default, nothing is sent, so a forged source cannot be used to bounce answers at someone), `truncate` (UDP: a short answer with the TC bit, a real resolver retries over TCP) or `refused`. Over TCP, DoT and DoH the answer is always REFUSED (the address is real, and a truncated answer makes no sense there). `client_exempt` lists networks never limited. The node itself (loopback) is always allowed and never limited.
- Both checks run first, before the cache and before updates are handled. Token bucket per client in 16 shards, at most 8192 clients per shard (about 130,000 in all; when full, clients whose bucket has refilled are forgotten, then a few arbitrary ones, who simply start with a full burst), so a flood of new addresses cannot grow memory. Counters are per node: a client that reaches several nodes gets the limit at each.
- Visibility: the DNS page's pool header shows `limit N/s per client · X turned away` and `allowed clients · N networks · Y refused` (amber once anything was turned away); `--show-dns` prints the same; the log gets one line at the first refusal and then at most one a minute. Settings ▸ DNS proxy ▸ **Who may ask** has the fields (a bad network is refused with a message and nothing is saved); help, README and `ddgw --help` describe them.
- The new keys are written only when used (`omitempty`, the default action is stored as absent), so a config that does not use them stays readable by older versions, which refuse unknown keys.
- Tests (`clientlimit_test.go`): burst then refill and the cap at the burst after a long pause; the IPv6 /64 and IPv4-mapped keys; list, exemptions and loopback; no limiter when nothing is configured; bounded memory (2 × the bound of distinct clients); validation, normalisation and that unused keys are not written; through the frontend (REFUSED over UDP/TCP and nothing reaching the server, updates refused, drop/truncate/refused over UDP and REFUSED over TCP, counters); log throttling. Mutation checks: removing the call in `resolve`, or the list check, makes them fail.

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`.
- Live (gateway on eth0 with a stub upstream, hot reload by atomic rename, clients on 192.0.2.2): allowed list `10.0.0.0/8` only → 3 of 3 queries REFUSED, `--show-dns` counts them; rate 5/s burst 5 with drop → 5 answered at once, the rest silent, and after a pause the burst is back; truncate → 5 answers then TC=1 replies; refused → 5 then REFUSED; the log line appears once. Chromium (real PAM login, light and dark, no page errors): the pool header pills, the Settings fields, a bad network refused with the message, and a valid list saved (`client_action: drop` is not written).

### Not verified
- Behaviour with many real clients or at high query rates (no load test); the locking is a mutex per shard.
- DoT and DoH clients over the limit (covered by the TCP path in the unit test, not run live).
- Several nodes behind one anycast address: each node counts on its own.

## [v104] - 2026-10-02 — Space between a text box and the button under it

### Fixed
- On the Cluster page the "Join cluster" button sat right against the join-code box. A button row that directly follows a text area now has a gap above it (`textarea + .toolbar`, .9 rem), which also applies to the same pattern elsewhere (join code shown after "Create join code", the certificate and key boxes). Measured in Chromium: 21 px between box and button.

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`.
- Chromium, light and dark, real PAM login: the join form on a fresh node, no page errors.

### Not verified
- The Certificate page and the "Create join code" card were not opened after the change; they use the same rule.

## [v103] - 2026-10-02 — Gateway members: one line per node, IPv4 / IPv6 link-local

### Changed
- **Nodes ▸ Gateway members shows each node once**, as `IPv4 / IPv6 link-local` (for example `192.168.5.55 / fe80::be24:11ff:fe3d:2dd9`) instead of one line for each address family. The two engines of a node share the forwarder slot (AFN), which is how the page pairs them (same group, same slot, same local/remote); a node running only one family shows just that address, and a row with no partner (no slot yet) stays on its own line. Age is the older of the two hellos; if the two families disagree on state, both states are shown (hover for which is which). The hello carries only the link-local address and the wire format is unchanged, so no global IPv6 address is shown. Page change only: API, CLI and protocol are as in v102.

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`.
- Chromium (real PAM login, light and dark, no page errors) with the page's `/api/neighbors?all=1` answered by a mock: your four-row example becomes two lines; an IPv4-only node, and an IPv6 row without a slot (expired), stay on their own lines.

### Not verified
- Real two-node dual-stack data (the sandbox has no IPv6); the pairing was tested with mocked rows shaped like the ones in your screenshot.

## [v102] - 2026-10-02 — Cluster page: identity tile removed; Nodes page lists every gateway member

### Changed
- **Nodes page: "Gateway neighbors" is now "Gateway members"** and lists every node taking part in each group and address family, this node included (★, age "local"), not only the remote ones. The column that said Peer IP says Node. `/api/neighbors?all=1` returns all rows (sorted by group, family, address); without `all` it is unchanged, and so is `--show-neighbors`. New test `TestSortRowsAndPeersOnly`.
- The Cluster page no longer shows the "Identity (pinned by peers)" tile (a truncated fingerprint nobody needs to read). Nothing else changed: peers still pin each other's identity, `--cluster-status` still prints it, and the Certificate page's own fingerprint is untouched.

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`.
- Live (real PAM login, Chromium light and dark, no page errors): a gateway on eth0 became the controller; the Nodes page lists its own row (★, age "local") under Gateway members; the cluster card renders as before.

### Not verified
- A second gateway node in the members table (this sandbox ran one gateway node); remote rows use the same code path as the local one, minus the ★.
- The Cluster page without the identity tile was not opened in a browser (a one-line removal).

## [v101] - 2026-10-02 — Monitor ▸ Neighbors is now Monitor ▸ Nodes, with the cluster members

### Changed
- **The page is called Nodes** (sidebar, help, README table; an old `#neighbors` bookmark still opens it). It watches two things, one card each: **Gateway neighbors** (what the old page showed: who this node hears gateway hellos from, per group) and **Cluster members** (node, role, reachable, epoch, running, source, last seen, any error, "updating" while an update runs), the same table as the Cluster page's Members card but read-only — no Remove button; adding, removing and promoting stay on the Cluster page. Both refresh every 2 s. If the cluster call fails the neighbors card still shows.
- The member table is one shared helper (`membersTable`) used by both pages, so they cannot drift apart.
- CLI is unchanged (`--show-neighbors`, `--cluster-status`); the page's help and the README's CLI/GUI table name both.

### Verified
- gofmt, build, vet, `go test -race -count=1` (including `TestEveryPageHasHelp`), CGO-off vet/test, five linux cross-compiles, `node --check`.
- Live: two clustered daemons (joined with `--cluster-join`), page opened in Chromium light and dark: sidebar reads Nodes, the two cards show, the members card has no Remove button, no page errors; an old `#neighbors` URL opens the page; the Cluster page still shows Members with Remove. PAM login used the real `ddgw` PAM service; test user, group and PAM file removed afterwards.

### Not verified
- A cluster with a node that is down or updating, in the new card: only the table code is shared with the Cluster page, which showed those states before.

## [v100] - 2026-10-02 — Fallback servers that refuse ECS queries now answer

Reported: with every server paused or down, `dig example.com @<gateway>` returned REFUSED (flags qr rd ra, EDNS udp 512 — the signature of Google 8.8.8.8, the fallback), although `dig @8.8.8.8` from the same node worked.

### Fixed
- **REFUSED from a server because of the client subnet (ECS).** The gateway attaches ECS (the client's /24) to forwarded queries; the clients here are private 192.168.x addresses, which Google answers with REFUSED. Only FORMERR was retried without ECS, so the REFUSED went straight to the client. Now a FORMERR *or* REFUSED answer to a query carrying ECS is retried once without it; if that gets a different answer, the server is remembered (`noECS`, until restart or reload) and not sent ECS any more, with one warning in the log (`… not sending it ECS any more`). A server that refuses the plain query too is treated as before.
- New test `TestForwardRetriesWithoutECSOnRefused` (two round trips the first time, the original query on the retry, one round trip afterwards); with `rcodeRefused` removed from the condition it fails.

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`.
### Not verified
- Against real Google: the sandbox has no outbound DNS. The cause is inferred from the reply signature; if it still REFUSES, send `dig @8.8.8.8 example.com +subnet=192.168.5.0/24` and `ddgw --show-dns`.

## [v99] - 2026-10-02 — IPv4 gateway address no longer lost when a node becomes the controller

Reported: after an update the node answered DNS on the anycast addresses but not on the gateway address, from the network or from itself; the other node answered. The log and `ip addr` of the failing node (the controller) showed why.

### Fixed
- **The IPv4 gateway address vanished from the controller.** The v4 and v6 engines of a group share one macvlan (`ddgwG.S`). When a node becomes the controller both engines go active a moment apart (here 18:59:40.849 and 18:59:41.276) and each called `addVmac`, which *deleted and re-created* the macvlan. The IPv6 engine's call, coming second, destroyed the macvlan the IPv4 engine had just put the IPv4 address on, so the node kept the IPv6 gateway address but not the IPv4 one (`ip addr` showed only `fdf5:168:5::56` on `ddgw1.2`, `ip neigh` for 192.168.5.56 was FAILED, and the DNS listener was bound to an address the node no longer owned). Clients that were handed this node's virtual MAC timed out; only a restart or a pause/resume of the gateway repaired it. `addVmac` now keeps a macvlan that already exists with the right MAC on the right parent (`vmacCurrent`) and only brings it up; one that is wrong or missing is re-created as before. It is an old race in the engine, not caused by the DNS changes of v95–v98, but a rolling restart (auto-update is on by default since v93) makes a node take over as controller far more often.
- New test `TestAddVmacKeepsExistingMacvlanAndItsAddresses` (needs root, builds a macvlan on a throwaway veth, skipped otherwise); with the fix removed it fails with "the address was lost".

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`.
- Live, IPv4 only: a gateway on a veth with a real macvlan became the controller with the VIP on `ddgw1.1`, as before the change.

### Not verified
- The dual-stack takeover itself (v4 and v6 becoming active together): this sandbox has no IPv6, so only the shared-macvlan helper is tested, not the two engines racing on one machine, and not two nodes.
- "MAC takeover: failed to create macvlan for slot 3" warnings seen in that node's log; the slot-3 macvlan belongs to the other node and the cause was not established.

## [v98] - 2026-10-02 — Fallback servers are never probed

Asked for: fallback servers should not be probed; they are a last resort and count as always up. (A fallback was probed with the domains of the normal servers, which can be internal names a public resolver such as Google DNS knows nothing about, so it could fail its probes or look healthy for the wrong reasons.)

### Changed
- Fallback servers are **not probed** and always count as up. While no normal server is in service (down or paused) they take the queries; the first normal server that passes a probe again takes over and the fallbacks get nothing. A failed forward to a fallback does not mark it down. They need no probe queries any more, so the "need a domain to be tested with" refusal and the probe-query pinning from v96 are gone. The DNS page, canvas and `--show-dns` show them as up with no probe counts; help, README and the Settings hint are rewritten.

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`. Tests: a fallback with a server that answers NXDOMAIN to everything is never probed (0 queries) and is used when the normal servers go down; it gets no traffic while one is up; validation without any queries; the paused-servers and canvas tests still pass.
- Live: every normal server paused, a stub fallback that logs every name: the fallback saw only the client's query (no probes), `dig` was answered, the DNS page/`--show-dns` list it as up.

### Not verified
- The REFUSED you saw for a name through the gateway: with a fallback that no longer depends on probes this should not recur with public resolvers, but I could not reproduce your setup, so it is not confirmed.

## [v97] - 2026-10-02 — DNS proxy intro in its card, compact Updates headers

### Changed
- Settings ▸ DNS proxy: the sentence "The default settings for every gateway that has no servers of its own…" now sits inside the first card (Upstream servers) instead of above it. The fallback list's hint says "down or paused" like the rest of the fallback text.

- Updates page: the Upload and Update buttons in the card headers are compact (`.btn.hdr`), and the Nodes header's note no longer adds height, so the Upload, Nodes and History headers are all the same height (46 px, measured).

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`; looked at in Chromium (light and dark) against a live daemon.

### Not verified
- Nothing beyond the above.

## [v96] - 2026-10-02 — Cache hit percentage on the Statistics page

Asked for: the cache hit percentage next to the query tiles.

### Added
- **Cache Hits tile** (Monitor ▸ Statistics): the share of cacheable lookups answered from the answer cache in the chosen range, with "N of M" (for example 66.7%, 10 of 15). It follows the range like the other tiles; it shows "–  off or unused" when the cache is off or nothing cacheable was asked. It is not a kind of answer, so it is not clickable and the other tiles ignore it.
- The statistics collector counts cache hits and misses per minute (`QStats.RecordCache`), kept in the saved statistics (two more numbers per minute; a file from before this version still loads, with zero cache counts, and an older version reading the new file ignores the extra numbers). Help updated.

### Fixed
- **Fallback servers are used when every normal server is paused**, not only when they are down. The paused servers are left out of the running pool, which had left the fallbacks with no probe queries (so they never counted as healthy) — their queries are now pinned first — and the canvas showed red instead of amber. A paused or stopped gateway still serves nothing.

### Changed
- The tile grid's minimum width is a little smaller so eight tiles fit one row on a normal window.

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`. New tests: sums over a range (a later range sees only its minutes), save/reload keeps the counts, an older nine-number file loads.
- Live: cache on, 8 identical + 3 distinct queries → 63.6% (7 of 11), correct in Chromium light and dark; after a daemon restart the counts were kept and added to (10 of 15).

### Not verified
- The tile with the cache switched off, in the browser (the "off or unused" text).

## [v95] - 2026-10-02 — Fallback DNS servers

Asked for: a fallback list in the gateway definition, used when every configured server is down, dropped again as soon as a configured server is back.

### Added
- **`fallback_servers`** in the `dns` block (shared, or a gateway's own; omitted when empty, so older readers are unaffected). Same address forms as `servers` (`tls://`, `https://` included). They are probed all the time with the same queries, so one is known to work when needed, but they take **no client queries** while any normal server is healthy. When every normal server is down only the healthy fallbacks are ranked and used; the first probe round in which a normal server passes again puts the pool back on the normal servers. The log says when it switches either way (and when the fallbacks are down too).
- A fallback with no common `queries` (a gateway drawn on the canvas keeps its domains per server) is probed with the union of the normal servers' domains; fallbacks with nothing to be tested with are refused. A fallback cannot also be a normal server or be listed twice.
- **GUI:** Topology ▸ Edit gateway has a *Fallback DNS servers* field (comma separated, refused client-side when it clashes with a server of the gateway); Settings ▸ DNS proxy has a *Fallback servers* list for the shared pool; the DNS page marks fallbacks and says when they are in use; the canvas shows amber "every DNS server is down — answering from the fallback servers" instead of red while a fallback answers.
- **CLI:** `--canvas-add fallback --group N --server ADDR`, `--canvas-del fallback …`; `--canvas` and `--show-dns` show the fallbacks and when they are in use. History describes fallback changes. Help and README updated.

### Fixed
- The test DNS stub (`newFakeDNS`) retries when another process in the sandbox already holds the TCP side of the UDP port it picked (that caused the occasional "address already in use" failures noted in v94).

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`. New tests: fallback unused while a normal server is up, takes over when all are down, dropped when one is back (a mutant that always included fallbacks failed it), no fallback means the old failure, validation/normalisation, clone, canvas status, canvas/CLI edit, union of domains.
- Live: three stub servers, one a fallback. Normal operation: the fallback got only its probes. Both normal servers killed: amber canvas, `--show-dns` ranks the fallback, client queries answered by it. One normal server restarted: back to normal within a probe round, the fallback got no more queries. Chromium (light and dark): Edit gateway field (saved, refused when it clashes with a server), DNS page marker, Settings ▸ DNS proxy list.

### Not verified
- Fallback in a cluster (the field is part of the shared DNS block that already replicates, but two nodes were not run for this change).
- DoT/DoH fallback servers live (the code path is the same as for normal servers).

## [v94] - 2026-10-02 — Upload button in the card header

### Changed
- Updates page: the **Upload** button moved into the "Upload a release" card header, right-aligned like the Nodes card's button; the file chooser stays in the card body.

- With auto-update on, the Nodes card's update button and the node tick boxes are disabled (every node updates itself); the button's tooltip says where to turn it off.

- `TestClusterUpdateIntentAndSourceDistribution` now switches auto-update off first (it is on by default since v93 and the test is about hand-queued updates).

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`. Two tests (`TestUpdateFallsBackToNameServers`, `TestLatencyReordersAfterChange`) failed once each with "address already in use" on a port another process in the sandbox held; they pass on rerun and are unrelated to this change.

### Not verified
- Not looked at live in Chromium.

## [v93] - 2026-10-02 — Auto-update on by default, Updates button placement

### Changed
- **Auto-update defaults to on** for a node with no saved update settings (fresh install); a saved on/off choice is never overridden.
- **Button placement:** the Nodes-card button is pushed to the right edge of the header; the Upload button sits right next to the file chooser.
- `TestWebClusterAndUpdateBasics` now switches auto-update off and on again (on is the default, so a lone "on" records nothing).

### Verified
- gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles, `node --check`.
### Not verified
- Not looked at live in Chromium.

## [v92] - 2026-10-02 — Updates page simplified

Asked for: the Updates page felt overly complicated; move auto-update on/off to Settings ▸ General in its own card, drop the other buttons if not needed, keep the node tick boxes, put the update button in the Nodes card header, and move the four status boxes to the top.

### Changed
- **Updates page:** the Running / Staged source / Auto-update / This node boxes are now the first thing on the page. The toolbar is gone; one button sits in the Nodes card header: **Update this node now**, or **Update N selected** when members are ticked (cluster only; uses the existing push). The not-safe confirmation for the local node is unchanged.
- **Auto-update** is a switch in its own **Updates** card under Settings ▸ General (confirms before turning on).
- Removed the **Queue selected nodes**, **Cancel queued** and auto-update buttons from the Updates page. `--update-cancel` is now command-line only (README table, help and QUICKSTART updated).


### Verified
- `node --check`; gofmt, build, vet, `go test -race -count=1`, CGO-off vet/test, five linux cross-compiles.

### Not verified
- Not clicked through live in Chromium this release.

## [v91] - 2026-10-02 — Node picker aligned with the page content

Asked for: right-align the node dropdown with the end of the page content.

### Changed
- **Node picker (cluster only):** its right edge now sits exactly on the right edge of the page content (the cards below it). It used to stop 4 rem short of it, to stay clear of the fixed "?" help button. In windows narrow enough for that button to reach it (content area under about 1,480 px wide) the picker instead moves down a row, below the button, so the two never overlap; wider windows keep it on the top row.

### Verified
- Headless Chromium with a real PAM login at window widths 900, 1100, 1300, 1470, 1490, 1520 and 1700 px: the picker's right edge equals the first card's right edge in every case and it never overlaps the help button. The picker was injected the way the page builds it (a real one needs a joined cluster), so its look in a real cluster at those widths was not repeated. Full Go gates (`gofmt`, build, `go vet`, `go test -race`, `CGO_ENABLED=0` vet and test, five cross-compiles).

### Not verified
- A real two-node cluster in the browser (the picker's content, "unreachable" entries, the amber border when another node is picked).
- Phone width.
- The cgo/PAM build was only run natively on amd64; the cross-compiles use the fail-closed stub.

## [v90] - 2026-10-02 — DNS proxy sections as cards

Asked for: in DNS proxy, make the different sections different cards under the same tab.

### Changed
- **Settings ▸ DNS proxy tab** now shows six cards, one per group: *Upstream servers*, *Choosing a server*, *Listening for clients*, *Client network (ECS)*, *Cache* and *Timing* (each with the title in its header and, where there is one, its note above the fields), under the tab's one-line intro. The other tabs keep their single card.
- The Settings form builder can now make a card per section (`fieldset(specs, obj, true)`); the older heading-inside-one-card form is still there for any other use. No setting, key or default changed, and a save still reads every field of every tab.

### Verified
- Headless Chromium with a real PAM login, light and dark: the DNS proxy tab shows the six cards in that order; a change to the probe timeout was written to the config file (1357 and 1246 in the two runs) and the page stayed on the DNS proxy tab; General, Gateway groups, Web GUI and Cluster still show one card each; no page errors. `node --check`, the help-topic test and the full Go gates (`gofmt`, build, `go vet`, `go test -race`, `CGO_ENABLED=0` vet and test, five cross-compiles).

### Fixed
- **A second flaky test** (not a daemon bug), found while gating this version: `TestCanvasMarksServersInBand` failed once when the machine was busy, because its two "equal" 20 ms stub servers could drift outside the default 20% spread band. The test now uses a 150% band and a slow server that is 20 times slower, so the in-band/out-of-band outcome no longer depends on scheduling. 30 runs under CPU load passed; as with the v83 flake I could not make the old version fail on demand here other than under load.

### Not verified
- Phone width was not looked at for the new cards.
- The cgo/PAM build was only run natively on amd64; the cross-compiles use the fail-closed stub.

## [v89] - 2026-10-02 — Settings in tabs, one card per tab

Asked for: break Settings into tabs, one card per tab.

### Changed
- **Settings page** now has a tab bar: *General*, *Gateway groups*, *DNS proxy*, *Web GUI*, *Cluster*; each tab shows one card (the Gateway groups tab holds the per-gateway cards under a one-line note). The tab you are on is kept when a change is saved and the page redraws (and when a refused change is put back), arrow keys move between tabs, and the bar uses `role=tablist`/`tab`, `aria-selected` and a visible focus ring.
- The tabs not shown stay in the page, only hidden, so one save still reads every field and writes the whole config exactly as before; no setting, key or default changed.
- Help: the Settings topic lists the tabs; its Spread sentence used a field name that v87 renamed ("Spread band (% of fastest)") and is corrected.

### Verified
- Headless Chromium with a real PAM login, light and dark: five tabs; one card visible per tab; clicking each tab selects it; ArrowRight moves from General to Gateway groups; a change on the DNS proxy tab (probe timeout) was written to the config file and the page stayed on that tab; no page errors. `node --check`, the help-topic test and the full Go gates (`gofmt`, build, `go vet`, `go test -race`, `CGO_ENABLED=0` vet and test, five cross-compiles).

### Not verified
- Phone width was not looked at again (the tab bar wraps onto a second line when the width is short; that is by design but unchecked).
- Whether a tab choice should survive leaving the Settings page: it is remembered only until the page is reloaded.
- The cgo/PAM build was only run natively on amd64; the cross-compiles use the fail-closed stub.

## [v88] - 2026-10-02 — A server's name goes under its address

Asked for: the name should not replace the IP address on a server's square; it should show under it, the way a gateway shows its name.

### Changed
- **Topology page:** a named DNS server's square now shows the address in bold, the name beneath it in the smaller muted style the gateway circle uses, and the latency and rank line below that. When any server of a gateway has a name, every square in that row is a little taller (62 instead of 46) so the squares and the domains below them stay level; a gateway with no named server looks as before. The tooltip and the aria label still read "name (address)".
- The Edit server form hint, the help panel and the command-line usage text say "under the address" instead of "instead of the address". No key, flag or default changed.

### Verified
- Headless Chromium with a real PAM login, light and dark: the named square's text is [address, name, latency] and the unnamed square's [address, latency]; both squares are the same height, the domain shapes sit below them, no page errors. `node --check`, the help-topic test and the full Go gates (`gofmt`, build, `go vet`, `go test -race`, `CGO_ENABLED=0` vet and test, five cross-compiles).

### Not verified
- A name of the maximum 40 characters is cut to 24 on the square (the tooltip has all of it); long names, many servers in a row and the narrow phone layout were not looked at again.
- The cgo/PAM build was only run natively on amd64; the cross-compiles use the fail-closed stub.

## [v87] - 2026-10-02 — Settings page tidied

Asked for: clean up the DNS proxy settings ("this looks terrible"), and no add or remove of gateways on that page, since that is done from Topology.

### Changed
- **DNS proxy card** is split into titled groups instead of one grid of 22 mixed fields: *Upstream servers* (servers, probe queries, the accept-any-certificate switch), *Choosing a server* (spread, spread band, down percentage, failures before down, servers tried, latency smoothing), *Listening for clients* (DNS port, DNS over TLS port, DNS over HTTPS port, forward dynamic updates), *Client network (ECS)*, *Cache* and *Timing*. A checkbox now sits on its own line above the numbers it controls, so a row no longer mixes boxes and inputs at different heights; fields start at the top of their row. Labels are shorter ("Spread band (% of fastest)", "Down at (% of tests failing)"), the DoT/DoH hints became one note under the *Listening* heading, and the intro no longer talks about "all (or any)" probe queries (stale since v76).
- **Gateway groups:** the *+ Add group* and each card's *Remove* button are gone; the heading says gateways are added and deleted on the Topology page. The cards still edit every per-gateway field. The help text says the same.
- The section headings are a new field kind in the Settings form (`{ section, note }`) and a few lines of CSS; no setting, key or default changed.

### Verified
- Headless Chromium with a real PAM login, light and dark, at 1220 px and at phone width (420 px): the DNS proxy card reads top to bottom in the six groups; the Gateway groups card, Web GUI and Cluster cards are unchanged in look; no page errors. `node --check` on `app.js` and `help.js`; the help-topic test; full Go gates (`gofmt`, build, `go vet`, `go test -race`, `CGO_ENABLED=0` vet and test, five cross-compiles).

### Not verified
- Saving a changed value from the new layout was not repeated (the fields and their save path are the same code as before).
- The test rig's config had no spread band or cache size, so those showed the rig's values (0, 250), not the defaults.
- The cgo/PAM build was only run natively on amd64; the cross-compiles use the fail-closed stub.

## [v86] - 2026-10-02 — DNS over HTTPS served to clients

Asked for: "next", after DoH to servers: DoH for clients.

### Added
- **`dns.doh_port`** (0 = off, the default; usually 443): the gateway address, and any anycast address of the gateway, also answers DNS over HTTPS (RFC 8484) at `https://<address>:<port>/dns-query`. POST with an `application/dns-message` body, or GET with the message as the base64url `dns` parameter; HTTP/2 or HTTP/1.1, TLS 1.2 or later, the web GUI's certificate; the same pool, cache, spread and statistics as plain DNS (counted as TCP). Shared block or a gateway's own `dns` block; not written when 0. It is on the Settings page (*DNS over HTTPS port (0 = off)*), in `--show-dns` and as a "DoH · port" pill on the DNS page.
- **Responses:** only `/dns-query` is served (404 elsewhere); a wrong method gets 405, a content type other than `application/dns-message` on POST 415, a missing or bad message 400. Answers carry `Cache-Control: no-store`.
- **Rules:** `doh_port` must be 0 or 1–65535 and differ from `listen_port` and `dot_port` (all TCP). Changing it moves the listener without restarting the gateway; if the port cannot be opened the error is logged and the other listeners keep running.

### Verified
- `doh_test.go`: POST over HTTP/2 and GET (correct answers, IDs kept), 404/415/400/405 refusals, ddgw's own DoH client against it end to end, plain DNS unaffected, the port closed after Stop, no listener when unset, validation (443 ok; 70000, -1, `listen_port` and `dot_port` refused; the off value not written). Mutation check: dropping h2 from the offered protocols fails the HTTP/2 assertion.
- `gofmt`, build, `go vet`, `go test -race`, `CGO_ENABLED=0` vet and test, five cross-compiles, `node --check` on `app.js` and `help.js`, the help-topic test.
- Live run with `curl`: after a hot edit adding `doh_port` the listener appeared with no restart; POST over HTTP/2 returned 200 `application/dns-message` with the right answer; GET returned the identical bytes; a wrong path gave 404 and DELETE 405; `--show-dns` and the log showed it; setting `doh_port` back to 0 closed the port within seconds.

### Not verified
- The DNS page pill and the new Settings row were not looked at in Chromium (same components as the DoT ones).
- A browser or OS using this as its DoH resolver (Chrome, Firefox, Android) was not tried.
- The anycast-address path is wired the same way as DoT's but was not run live.
- No access control: anyone who can reach the port can use it, as with plain DNS on the same address. There is no per-request logging beyond the statistics.
- As for TCP and DoT, a client gets SERVFAIL when the chosen upstream does not speak TCP.
- The cgo/PAM build was only run natively on amd64; the cross-compiles use the fail-closed stub.

## [v85] - 2026-10-02 — Third-party protocol and vendor names removed

Asked for: remove all references to the vendor and the two first-hop-redundancy protocols you named, if there are any. (This entry does not spell them out, so the tree stays free of them.)

### Changed
- A case-insensitive search of the whole tree (source, tests, GUI files, docs, scripts, this changelog, `CLAUDE.md`) for those three names found nothing in the documentation and nothing about the vendor or the other protocol anywhere. The only hits were three internal Go names (a port, a version and an OUI constant in `wire.go`, `netutil.go` and `engine.go`) that were spelled from one of the protocol names. They are now `dgwPort`, `dgwVersion` and `dgwOUI`. Nothing visible changed.
- The wire protocol is untouched: the port (4729), the version byte, the virtual-MAC prefix, the packet magic and the default key have the same values, and the golden-vector test passes unchanged.

### Left as it is
- "Distributed Gateway Load Balancing Protocol" in `main.go`, the usage text and `CLAUDE.md` is this project's own name for its protocol (the `dgw` daemon's name for it), not a reference to anyone else's. It stays; say if you want it renamed too.

### Verified
- The search for the three names over the final tree finds nothing; `gofmt`, build, `go vet`, `go test -race`, `CGO_ENABLED=0` vet and test, five cross-compiles.

### Not verified
- The cgo/PAM build was only run natively on amd64; the cross-compiles use the fail-closed stub.

## [v84] - 2026-10-02 — DNS over HTTPS to upstream servers

Asked for: DoH ("doh now"), after DoT. This version is DoH to the servers ddgw forwards to; serving DoH to clients is the next one.

### Added
- **`https://host[:port][/path]` servers** (port 443 and path `/dns-query` unless given; `https://1.1.1.1` becomes `https://1.1.1.1:443/dns-query`). Each probe and query is an HTTPS POST of the DNS message with `Content-Type`/`Accept: application/dns-message` (RFC 8484), HTTP/2 when the server offers it. The message ID on the wire is 0, as the RFC asks, and the caller's ID is put back on the answer. Only a 200 with an `application/dns-message` body of at most 64 KB that parses as a response counts; redirects are not followed and no proxy from the environment is used.
- **Certificates:** checked like `tls://` (system authorities, host name or IP from the URL). The v80 **`tls_insecure`** setting now covers `https://` servers too; its Settings label, the DNS page pill, `--show-dns` line and log warning say "tls:// and https://".
- Works wherever a server is entered (shared `dns.servers`, a gateway's own block, the Topology forms, `--canvas-add server`, the Settings list); the key keeps the scheme and the normalised form, so a server's name, probe queries and pausing work as for any other. A `tls://`, `https://` and plain entry for one host are three servers.

### Verified
- `doh_test.go`: normalisation (defaults, upper case, IPv6, custom path/port, idempotent) and refusals (empty host, query, fragment, space or control character in the path, bad port); probe and forward through an HTTP/2 responder (all requests HTTP/2, ID 0 on the wire, caller's ID returned); custom path, wrong path (404 reported), 503; untrusted certificate refused with no request sent, accepted with `tls_insecure`; a wrong content type and a redirect refused. Mutation checks: forcing `InsecureSkipVerify` fails the certificate test; not restoring the ID fails the forward test.
- `gofmt`, build, `go vet`, `go test -race`, `CGO_ENABLED=0` vet and test, five cross-compiles, `node --check` on `app.js` and `help.js`, the help-topic test.
- Live run: a Python HTTPS responder on 127.0.0.61:443 (certificate installed in the system store): `https://127.0.0.61` healthy at about 4 ms and `https://127.0.0.61/nope` down with "https: 404 Not Found"; three client queries through the VIP were answered and all arrived with ID 0.

### Not verified
- Against a real public DoH resolver (no outside network here), and with GET (only POST is used).
- The live responder was HTTP/1.1; HTTP/2 was exercised by the unit-test server only.
- Each probe and query opens a new connection (TLS handshake plus request), so a DoH server's latency is higher than the same server over UDP; connection reuse is not built.
- Serving DoH to clients is not implemented yet.
- The cgo/PAM build was only run natively on amd64; the cross-compiles use the fail-closed stub.

## [v83] - 2026-10-02 — A GUI-first quick start guide

Asked for: a getting-started guide centred on the GUI, in the root of the tarball.

### Added
- **`QUICKSTART.md`** at the top of the tarball: install (`install.sh --add-user`), the sign-in rules and the certificate warning, drawing the first gateway on the Topology page step by step (New gateway…, Add DNS server…, what the colours mean), testing it with `dig` and the Monitor pages, adding a second node through the Cluster page, replacing the certificate, and a short list of what else to know and what to check when something looks wrong. It uses the GUI's own labels and menu names.
- `docs/README.md` points to it from the install section; `CLAUDE.md` lists it in the tarball layout and asks that it be kept true when the GUI changes.

### Fixed
- **A flaky test, not a daemon bug:** `TestBuildCanvasColours` failed once in about nine runs ("all fine => green, got warn"). The pool's own probe loop runs next to the test's `ProbeNow`, so a round that started before the test flipped a stub could land after it. The test now probes again until the colour settles (up to 2 s) instead of trusting the first round. 300 runs under CPU load and 15 with `-race` passed; I could not make the old version fail on demand, so that it was the cause is the evident reading, not a reproduction.

### Verified
- The labels, form fields and menu paths in the guide were checked against `webui/app.js` (Configure, Monitor and Operate groups, the gateway and server forms); one wrong path (Cluster is under Operate) was found and fixed. No code changed; the Go gates were still run for the release (see the v82 entry for the last code change).

### Not verified
- The guide's steps were not followed end to end on a clean machine: the installer was not run here for this version, and the two-node join was not repeated. The steps come from the README and the GUI source.

## [v82] - 2026-10-02 — DNS over TLS served to clients

Asked for: "go" on the next step, DoT to clients, "with the same tls_insecure functionality".

### Added
- **`dns.dot_port`** (0 = off, the default; usually 853): the gateway address, and any anycast address of the gateway, also answers DNS over TLS (RFC 7858) on that TCP port, through the same pool, cache, spread and statistics as plain DNS. Shared block or a gateway's own `dns` block; not written when 0. It is on the Settings page (*DNS over TLS port (0 = off)*), in `--show-dns` and as a "DoT · port" pill on the DNS page.
- **Certificate:** the web GUI's (the Certificate page: the self-signed one until a real one is installed, and a replacement is picked up without a restart). TLS 1.2 or later.
- **Rules:** `dot_port` must be 0 or 1–65535 and different from `listen_port` (both are TCP). Changing it moves the listener without restarting the gateway. If the port cannot be opened the error is logged and plain DNS keeps running.

### About "the same tls_insecure functionality"
- `tls_insecure` (v80) is about checking the certificate of a server ddgw forwards to, and stays as it is. A DoT listener has no client certificate to skip: it always presents the GUI certificate, and whether a client checks it is the client's choice (a client that skips the check is served exactly like one that does not). If you meant something else here, tell me.

### Verified
- `dot_test.go`: a DoT client served through the frontend (answer correct); an untrusted certificate refused by a checking client and served to one that skips the check; plain TCP on the DoT port gets nothing; plain DNS unaffected; the port is closed after Stop; no listener with `dot_port` unset; validation (853 ok, 70000, -1 and `listen_port` refused; off value not written). Mutation check: serving plain TCP instead of TLS fails the frontend test.
- `gofmt`, build, `go vet`, `go test -race`, `CGO_ENABLED=0` vet and test, five cross-compiles, `node --check` on `app.js` and `help.js`, the help-topic test.
- Live run: a TLS 1.3 client got correct answers on 127.0.0.9:8853 with the GUI certificate after a hot edit of the config (the listener appeared within seconds, no restart); setting `dot_port` equal to `listen_port` was refused ("Config reload skipped"), leaving the running one in place; `--show-dns` showed the line; headless Chromium (light, dark), real PAM login: the DNS page showed the "DoT · 8853" pill, no page errors.

### Not verified
- The anycast-address path is wired the same way (and restarts its listener when the port changes) but was not run live.
- A DoT (or plain TCP) client gets SERVFAIL when the chosen upstream does not speak TCP, as before for TCP; my first live attempt showed it with a UDP-only stub.
- DoT queries are counted as TCP in the statistics (no separate DoT line).
- No ALPN requirement, no client certificates, no idle timeout other than the existing 10 s.
- The cgo/PAM build was only run natively on amd64; the cross-compiles use the fail-closed stub.

## [v81] - 2026-10-02 — The gateway's name on the DNS page

Asked for: the gateway name should be on the DNS page (the card read only "Gateway 1").

### Changed
- **DNS page:** a gateway with its own DNS pool is headed "Gateway 1 · Office DNS" when it has a name; without one it reads "Gateway 1" as before. `--show-dns` prints "gateway 1 (Office DNS)". The status data (`/api/dns` pools) has a `name` on each pool, empty for the shared pool.

### Verified
- `TestGroupNameForDNSPage` (named, unnamed, unknown gateway, no config); `gofmt`, build, `go vet`, `go test -race`, `CGO_ENABLED=0` vet and test, five cross-compiles, `node --check webui/app.js`.
- Live run with a real PAM login in headless Chromium (light and dark): the card heading read "Gateway 1 · Office DNS", no page errors; `--show-dns` showed the name.

### Not verified
- A gateway that uses the shared pool still reads "Shared pool (group N)"; it does not list the gateway names.
- The cgo/PAM build was only run natively on amd64; the cross-compiles use the fail-closed stub.

## [v80] - 2026-10-02 — Option to accept any certificate from tls:// servers

Asked for: an option to accept all certificates ("I know it's bad, but it helps sometimes"). The gateway name on the DNS page is the next version, kept separate.

### Added
- **`tls_insecure`** in the `dns` block (shared, or a gateway's own): a `tls://` server is accepted whatever certificate it presents: self-signed, expired, unknown authority, wrong name. Off by default and not written when off. The traffic is still encrypted, but the server is not authenticated, so anyone on the path could answer as it.
- It is on the Settings page (*Accept any certificate from tls:// servers (insecure)*). While it is on, the DNS page shows an amber "tls:// certificates not checked" pill on that pool, `--show-dns` prints a warning line, and the log warns when the pool starts. Changing it rebuilds the pool like any other DNS change.

### Verified
- `dot_test.go`: with a pool that trusts nothing, `tls_insecure` on accepts the server and forwards a query; the same server with it off is refused; the key is off by default, omitted when off, and round-trips.
- Gates: `gofmt`, build, `go vet`, `go test -race`, `CGO_ENABLED=0` vet and test, five cross-compiles, `node --check` on `app.js` and `help.js`, the help-topic test.
- Live run: two TLS stubs with certificates the machine does not trust were both down with "certificate signed by unknown authority"; adding `"tls_insecure": true` by an atomic config edit brought both up within about 7 s with no restart, and each received its probe queries; `--show-dns` and the log showed the warnings.

### Not verified
- The DNS page pill and the new Settings row were not looked at in Chromium this time (they use the existing pill and bool-field components; the daemon needs a PAM login for that).
- It is one switch for the whole pool, not per server.
- The cgo/PAM build was only run natively on amd64; the cross-compiles use the fail-closed stub.

## [v79] - 2026-10-02 — DNS over TLS to upstream servers

Asked for: forward DoH and DoT to the monitored servers. Agreed order, one per version: DoT first, then DoH; serving DoT/DoH to clients is separate work. This version is DoT to the servers.

### Added
- **`tls://host[:port]` servers.** A server written `tls://dns.example.com`, `tls://1.1.1.1` or `tls://[2001:db8::53]:853` (port 853 by default) is probed and forwarded to over TLS (RFC 7858): a TCP connection, a TLS 1.2+ handshake, then the same length-prefixed messages as DNS over TCP. Clients can still ask ddgw over UDP or TCP; the upstream leg is TLS either way.
- **The certificate is always checked**, against the system's authorities and the host written after `tls://` (an IP address needs an IP name in the certificate). A server whose certificate fails shows as down with the TLS reason and is sent no query. There is no option to skip the check.
- It works everywhere a server is entered: the shared `dns.servers` and a gateway's own `dns` block, the Topology page forms, `--canvas-add server`, and the Settings list. The key keeps the scheme, so `tls://1.1.1.1:853` and `1.1.1.1:53` are two servers; names, probe queries and pausing work as for any server.

### Changed
- `normalizeServer` accepts the `tls://` prefix; the error for a malformed one reads "need tls://host or tls://host:port".

### Verified
- New `dot_test.go`: normalisation of `tls://` forms (and refusals), probe plus forward through a TLS responder to a UDP client, an untrusted certificate refused (the server receives no query), a certificate that names a different host refused. Mutation check: forcing `InsecureSkipVerify` fails exactly the two refusal tests.
- `gofmt`, build, `go vet`, `go test -race`, `CGO_ENABLED=0` vet and test, five cross-compiles, `node --check webui/app.js`.
- Live run: Python TLS stubs on 127.0.0.51 (certificate installed into the system store) and 127.0.0.52 (not trusted) plus a plain server. `--canvas` showed the first healthy at about 42 ms (the TLS handshake and a fresh connection per query), the second down with "failed to verify certificate: x509: certificate signed by unknown authority" and no query sent to it; with the plain server stopped, three queries through the VIP were answered and all three reached the TLS stub; `--canvas-add server` with a bad `tls://` name was refused.

### Not verified
- Against a real public DoT resolver (no outside network here).
- Connections are not reused: each probe and each query pays a full TLS handshake, so a TLS server's latency is higher than the same server over UDP and the speed ranking reflects that. Reuse is not built.
- A self-signed or private-CA server needs its authority installed on the machine running ddgw.
- The cgo/PAM build was only run natively on amd64; the cross-compiles use the fail-closed stub.
- DoT/DoH to clients and DoH to servers are not implemented.

## [v78] - 2026-10-02 — Uptime tooltips read "Online for 1m 1s - 0 failures"

Asked for: tooltips that just say "Online for 1m 1s - 0 failures".

### Changed
- **Topology tooltips** (servers, gateways, the VIP) now read `Online for <time> - N failures` (`1 failure` for one). A node that is down reads `Down for <time> - N failures`.
- **Failure counter:** the in-memory uptime tracker counts each up-to-down change instead of the old yes/no `Failed` flag. The `--canvas` text uses the same wording in lower case (`[online for 6s - 0 failures]`), and the canvas data's `failed` field is now `failures` (a number).

### Verified
- Unit tests in `uptime_test.go` (including the new `TestUpTextCountsFailures`), `gofmt`, build, `go vet`, `go test -race`, `CGO_ENABLED=0` vet and test, five cross-compiles, `node --check webui/app.js`.
- Live run with stub servers: killed and restarted one. `--canvas` printed `[online for 6s - 0 failures]`; Chromium tooltips read "Online for 22s - 0 failures", "Down for 22s - 1 failure" and "Online for 4s - 1 failure", with no page errors.

### Not verified
- Counts reset when the daemon restarts (they are not saved).
- The "Down for ... - N failures" wording is my choice; you only gave the online example.
- The cgo/PAM build was only run natively on amd64; the cross-compiles use the fail-closed stub.

## [v77] - 2026-10-02 — DNS servers on the Topology page can have a name

Asked for: a name field on the canvas server object, like the gateway has one.

### Added
- **`dns.server_names`**: an optional name per server, keyed by the server as written (like `server_queries`), shared block or a gateway's own `dns` block. Same rules as a gateway's name: trimmed, at most 40 characters, no control characters; an empty name is dropped, an entry for a server that is not in the list is refused, and the key is not written when there are no names.
- **Topology page:** the square of a named server shows the name instead of the address; the tooltip and the aria label read "name (address)". The *Add DNS server* / *Edit server…* form has a **Name (optional)** field; changing the address moves the name with the server, deleting the server deletes its name, and the Settings page keeps the names when it saves the DNS block.
- **Command line:** `--canvas-set server --group N --server ADDR --label NAME|-` (new) and `--label NAME` on `--canvas-add server`; `--canvas` prints `server NAME (ADDR)`. The canvas data (`/api/canvas`) has a `name` on a server only when it has one.
- The history lists a change as "DNS server names changed".

### Changed
- A name is a label only: it is left out of the pool's own configuration (`live()`), so renaming a server does **not** restart the pool (no new probe round, the answer cache and the latency estimates stay).
- Older ddgw versions refuse a config file with the new key (unknown key); a cluster with mixed versions should be updated together if names are used.
- Not changed: the DNS page and `--show-dns` still list servers by address (the daemon's status reads the running pool, which does not carry the labels).

### Verified
- gofmt, build, vet, `go test -race -count=1 ./...`, CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js. New tests (`servername_test.go`): names normalised, trimmed, emptied, unknown server, too long (41), control characters, 40 characters counted as characters, JSON round trip and omission; the add/set/clear/delete edits with their errors (not on the gateway, no label, no server, 60 characters refused by validation, a deleted server leaves no stale name); the canvas carries the name only for the named server; a rename keeps the same pool object; `cloneDNS` does not share the map; the history says names changed. Mutation checks: names left in the pool config (pool restarted on rename) and the delete not removing the name each failed a test. My first edit of the add/set/delete code had not applied and the tests said so.
- Live (real daemon, PAM login): `--canvas-set server` named two of three servers (a bad address and a missing label were refused with clear messages), `--canvas` showed the names, `server_names` was written to the config, and the log showed one pool start only (no restart on naming). In Chromium, light and dark: the squares read dns-a / Resolver B / dns-c, the tooltip reads "dns-a (127.0.0.41:5353) — ...", the Edit server form showed an empty name for the unnamed server and saving "dns-c" wrote it to the config; no script errors.

### Not verified
- The message shown for a 45-character name in the form (the check is in the page and the daemon refuses it too, tested in the daemon); I did not capture the dialog text.
- Renaming a server's address in the form was not re-run in a browser (the name moves with it in the code that changes `server_queries`, the same place).
- A name on the DNS page or in `--show-dns` (see above).

## [v76] - 2026-10-02 — A server is down when half its domains fail, otherwise degraded

Reported: with the same dead domain (jalopnik1.com) on every server, every server was down although the other domains still answered. Asked for: down at 50 % of the domains failing, degraded otherwise.

### Changed
- **New rule:** a server is **down** when at least `down_percent` (default **50**) of its probe queries fail in a round (one of two, two of four, two of three...), otherwise it **stays in use**, and with some queries failing shows **amber/degraded** ("N test(s) failing, server still eligible"). One dead domain among three no longer takes a server out, and the gateway is not red while any server is in use. `down_percent` is 1-100: 100 means down only when every query fails (the old `require: "any"`), 1 down on any failing query (the old `"all"`). It is a setting on the Settings page (Server is down when this % of its tests fail), a key of the `dns` block (shared or per gateway) and a prompt of the setup wizard; `--show-dns` and the DNS page header say what it is.
- **`require` is retired.** An old config file with `"require": "all"` or `"any"` still loads; the key is ignored (every old file has `"all"`, which is exactly what caused the report, so it could not be mapped to anything) and is no longer written back. The status data's `require` field is now `down_percent`. **Behaviour change on upgrade:** a server with some domains failing but under half now stays in use and shows amber instead of red; one with half or more failing is down, as before.
- `ddgw.conf.example`, README, `--help` and the DNS help topic describe the new rule.

### Verified
- gofmt, build, vet, `go test -race -count=1 ./...`, CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js. New tests: the pass rule over 16 (ok, total, percent) cases including the exact-half boundary and 0 of 0; a real UDP server that refuses names starting with "bad": 1 of 3 failing keeps the server in use (reported, rank 1, answers a client query), 1 of 2 and 2 of 4 take it down, 100 % keeps a 1-of-2 server, 1 % drops a 1-of-3 server, nothing answering is down at any setting; the canvas shows a 1-of-3 server amber and the gateway not red; the config default, range check and an old `require` file (all, any, anything) loading, ignored and not written back. Mutation checks: `<` to `<=` in the rule made two tests fail; removing the `ok == 0` guard is equivalent code (it cannot change a result at 100 % or less), so no test fails, and it stays as a safeguard. The old `TestRequireAllVsAny` was replaced.
- Live (real daemon, three stub servers that refuse "bad1.example", the old file's `"require": "all"` in the config): with domains good1, good2, bad1 on all three servers, `--canvas` showed every server WARN (1 test failing, still eligible), the gateway OK, 20 of 20 client queries answered; changing the list to good1 and bad1 (50 %) made all three DOWN and the gateway DOWN, as designed. Chromium, light and dark, with a PAM login: the drawing has green servers' circle, amber servers, a red bad domain; the DNS header reads "9 tests per round, down at 50% failing"; the Settings field is there, "Probes required" is gone, and saving 75 wrote `down_percent: 75` into the shared block with no `require` key.

### Not verified
- A cluster with nodes on older versions: the new key and the missing `require` are read by older versions as an unknown key and the default, respectively; mixed-version behaviour was not run.
- Per-server probe lists of different lengths (each server's percentage is of its own queries; tested with one list, not with a mix on the same pool).
- The probe interval is unchanged, so a domain that flaps can move a server between amber and red from one round to the next (`fail_threshold` still smooths the drop to down).

## [v75] - 2026-10-02 — Topology tooltips no longer vanish every two seconds

Reported: sometimes the tooltips on the canvas items don't show; the pointer has to go over them a few times.

### Fixed
- The canvas was redrawn from scratch on every status poll (every 2 s), which replaces every shape. The tooltip is the browser's own (the shape's `<title>`), and it only appears after the pointer has rested on the same element for about a second, so whenever a redraw landed in that second the timer started over; with a poll every 2 s that was roughly every other attempt. The page now leaves the drawing alone while a shape is hovered and catches up as soon as the pointer leaves all shapes (a `mouseout`), or after 10 s at the latest, so the colours cannot stay stale under a pointer that is simply parked on the canvas. Moving from one shape to another does not trigger a redraw. Clicks, edits and the right-click menu still redraw at once, as before.

### Verified
- gofmt, build, vet, `go test -race -count=1 ./...`, CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js (the Go tests do not exercise this JavaScript; the check below does).
- Reproduced first, in Chromium against a real daemon with PAM login: hovering a shape, the element was already replaced after half a second, on every poll. After the change, over 15 s of hovering the same element stayed for 10 s and was refreshed after that; 4.5 s on one shape and then 2.5 s on a second kept all three shapes; leaving the shapes redrew them within 0.4 s; a click still selected a shape; no script errors.

### Not verified
- The tooltip itself (a native browser popup) cannot be photographed in headless Chromium, so what was checked is the thing that broke it, that the hovered element survives long enough, not the popup's appearance. Other pages that draw SVG titles were not changed.
- A shape whose status changes while it is hovered keeps its old colour for up to 10 s, or until the pointer leaves; that is the price of letting the tooltip appear.

## [v74] - 2026-10-02 — Monitor menu: Statistics first, Host under it

Asked for: move Monitor > Statistics to the top of the section, and Host under it.

### Changed
- The Monitor group of the sidebar now lists Statistics, Host, Gateways, Neighbors, DNS, BGP, Log (it was Gateways, Neighbors, DNS, BGP, Statistics, Host, Log). Only the order changed: no page, route, help topic or command-line flag moved, and the Topology group and the first page after login are as before.

### Verified
- gofmt, build, vet, `go test -race -count=1 ./...`, CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js (the tests that check every page has a help topic still pass).
- Live (real daemon, PAM login, Chromium light and dark): opening Monitor shows Statistics | Host | Gateways | Neighbors | DNS | BGP | Log in that order, Statistics opens and draws, no script errors.

### Not verified
- Anyone who had the Statistics or Host page open keeps it until they navigate; no stored "last page" exists, so nothing else depends on the old order.

## [v73] - 2026-10-02 — Client names: reverse lookups now ask the servers ddgw forwards to

Reported: "reverse DNS isn't working, this definitely has a PTR record" (tooltip: "No reverse DNS name known (yet)").

### Fixed
- The names under the clients on the Statistics page were looked up only with the node's own resolver (`/etc/resolv.conf`, hosts file). When that resolver cannot reach the servers that hold the PTR records (for instance after the DNS servers moved to port 54, which `resolv.conf` cannot express) every client stayed nameless. A lookup now asks the DNS servers of ddgw's own pools first (a PTR query through the pool: fastest healthy server, with the usual failover), pool after pool, and only if none of them has a name falls back to the node's resolver (which still supplies `localhost` and hosts-file names).
- A client with no name found is asked again after 2 minutes instead of 10 (a lookup that failed while a server was down no longer hides the name for ten minutes); names found are still kept 10 minutes.
- The daemon's own reverse lookups are not counted as client queries in the pool's query and answered counters (the Served column of the server that answered does include them).

### Added
- `rdns.go`: `reverseName` (in-addr.arpa and the 32-nibble ip6.arpa form, an IPv4-mapped address as IPv4), `ptrNames` (reads PTR targets out of a response, compressed names included, damaged messages safe), `Pool.lookupPTR`, `Supervisor.ptrViaPools`.

### Verified
- gofmt, build, vet, `go test -race -count=1 ./...`, CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js. New tests (`rdns_test.go`): reverse names for IPv4, IPv6, mapped and invalid input; PTR parsing with and without name compression, no answer, NXDOMAIN, every truncation of a response and a bad pointer; a real UDP stub answering PTR through a pool (found, not found, not an address, counters untouched); the client list asks the pools first, retries a miss after the short time but not before, and does not re-ask a known name early. The first run of the test caught a retry rule that had not been applied.
- Live (real daemon, PAM login): a stub upstream with a PTR for 127.0.0.77 and NXDOMAIN for 127.0.0.78, clients sending from those two addresses: `getent hosts 127.0.0.77` finds nothing on this machine, and the Statistics API reported `client77.cush.local` for .77 on the second refresh, nothing for .78, and `localhost` for 127.0.0.1 (hosts-file fallback).
- Chromium was not used: no page layout changed, only a help sentence.

### Not verified
- The reporter's own setup: I cannot see their resolver or servers, so the cause (the node's resolver no longer reaching the servers) is inferred from the code path, not confirmed on their network. If a name is still missing, `dig -x 192.168.5.101 @<server> -p <port>` from the ddgw node will show whether the servers return the PTR; the server must be reachable at the address and port ddgw is configured with, and the reverse zone must be served by it (or be forwarded by it).
- PTR lookups over TCP (a response too large for UDP): only UDP is used.
- Lower-case: names come back lower-cased, as the DNS parser here normalises them.

## [v72] - 2026-10-02 — The login page no longer tells a guesser how many tries it has

Reported: the failed-login message said "2 attempts left before a 15-minute lockout"; that gives an attacker a target.

### Fixed
- A failed login now returns one fixed message, "Login failed: wrong username or password, or not authorized.", with **no attempts-left count** (`attempts_left` is gone from the response and from the page), **no lockout length** and **no group name** (the old text ended "not authorized for ddgw", which named the group a login needs; the group is configurable, and the name is exactly what someone guessing would like to know). The cause is still only in the log.
- `GET /api/login/state`, which anyone can call before logging in, used to return `max_failures`, `window_minutes` and `lockout_minutes`: the whole lockout policy (how many tries, in what window, for how long), so a guesser could pace itself just under the limit. It now returns only `locked` (and `retry_after` while this address is locked), which the page needs for its countdown. The policy values are still on the Settings page for a logged-in admin.
- `noteFailure` no longer computes the number of tries left at all.

### Changed
- README security notes say what the page does and does not tell.
- Kept as they were: the lockout itself (3 failures in 1 minute lock the address and the user name for 15 minutes), the countdown shown to a locked-out address, the 429 "Too many failed attempts." that appears on the failure that triggers the lock, and the log line per failed login. The first lockout still shows a patient guesser where the limit is; hiding that would mean never telling a locked-out address that it is locked.

### Verified
- gofmt, build, vet, `go test -race -count=1 ./...`, CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js. Tests changed: the throttle test now fails if a failed login's body has `attempts_left`, a digit, "left", "lockout" or the group name; the login-state test fails if any of the three policy fields is present; the rules test checks the same for all four kinds of failure. The lockout, the PAM-not-consulted-while-locked and the configured-policy tests still pass unchanged.
- Live (real daemon, real PAM, `tester1` in group ddgw, `tester2` outside it): `/api/login/state` returns `{"locked":false,"ok":true}`; a wrong password and a valid user outside the group get byte-identical messages; the third failure returns the lockout and the state then shows `locked` with `retry_after`. In Chromium (light and dark) the page shows exactly the new message after a refused login, with no script errors, and a member can still log in. Test users, group and PAM file removed afterwards.

### Not verified
- Timing differences between failure causes (all failures sleep the same `failDelay`, but PAM itself may answer a missing user and a wrong password at slightly different speeds); not measured.
- The lockout countdown screen was not re-captured; that code is unchanged except for the removed `policy` variable.

## [v71] - 2026-10-01 — Blue lines on the Topology page for the servers that take turns

Asked for: turn the lines blue that meet the relative band.

### Added
- With spread on, the line from a gateway to a DNS server is drawn **blue** while that server is within the band of the fastest healthy one (a lone fastest server counts, since it is the one taking the queries); every other line stays grey. The legend has a "blue line: takes turns (spread)" entry and the Topology help topic explains it.
- As the project rules require, the decision is made once in `buildCanvas`, not in JavaScript: `ServerStat.in_band` (from the pool, using the same `bandCount` as the query order) becomes `CanvasServer.in_band`, which the page turns into the `spread` class on the edge. `ddgw --canvas` shows `[spread: takes turns]` after the rank for the same servers. With spread off, or for a down, paused or slower-than-band server, nothing is marked.

### Verified
- gofmt, build, vet, `go test -race -count=1 ./...`, CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js. New tests: the canvas marks the two fast servers (not the slow one, not the dead one) with spread on and nothing with it off; a lone fastest server is in the band. A mutation (the canvas not copying `in_band`) made the canvas test fail.
- Live (real daemon, PAM login, stubs at 20/20/20/100 ms): `--canvas` marked the three fast servers; in Chromium, light and dark, 3 of the 8 lines have the `spread` class and are drawn in the accent blue (`rgb(36, 87, 214)` light, `rgb(127, 168, 255)` dark) against the grey of the others, the legend shows the new entry, no script errors.

### Not verified
- The line colour follows the latest ranking every refresh, so with latencies that sit near the edge of the band a line can flip between blue and grey from one refresh to the next; that was not provoked here.
- Colour-blind contrast of blue against the grey lines was not assessed; the legend and the `--canvas` marker carry the same information in text.

## [v70] - 2026-10-01 — Spread is on by default

Asked for: turn it on by default.

### Changed
- `spread` now defaults to **true** (`spread_band` stays 20). A config that does not mention the keys, and any gateway or pool created from now on, spreads queries over the servers within 20 % of the fastest; `"spread": false` (or Settings > DNS proxy) brings back fastest-first. **Behaviour change on upgrade:** an existing pool with servers whose latencies are within 20 % of each other now shares its queries between them instead of sending everything to one. A pool with one clearly fastest server behaves as before.
- README, `--help` text, the DNS help topic and the Spread description say "on by default"; `--show-dns` and the DNS header are unchanged.
- Tests: the default is checked (on, band 20), `"spread": false` in a file is honoured, and a config without the keys spreads; the fastest-first test now switches spread off explicitly.

### Verified
- gofmt, build, vet, `go test -race -count=1 ./...` (the only failures after the change were the two tests of the old default, updated as above), CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js.
- Live (real daemon, four stub servers, three at 20 ms and one at 100 ms), config with no `spread` key in the gateway's block: `--show-dns` printed `spread: on`, 90 queries went 34/34/22 to the three fast servers and none to the slow one. The 22 is a server that was briefly outside the 20 % band while the pool was warming up; it does not catch up with a burst (see v69).
- Chromium was not re-run: no page changed except one help sentence.

### Not verified
- Upgrading a real cluster: the setting is shared config, so every node of a cluster gets it with the new version; mixed v69/v70 clusters were not run (v69 nodes keep fastest-first until they update, as the key is simply absent there).

## [v69] - 2026-10-01 — Spread: round-robin over the servers within a latency band of the fastest

Asked for: round-robin across servers within a latency band (say, within 20 % of the fastest) instead of the fastest server taking every query.

### Added
- **`spread`** and **`spread_band`** in the `dns` block (shared block or a gateway's own): with `spread` on, the healthy servers whose smoothed latency is within `spread_band` percent (default 20, 1-1000) of the fastest one's take turns at the front of the try order; slower servers stay behind them in rank order as fallbacks, and a server that times out or answers SERVFAIL/REFUSED hands the query to the next one in the order as before. **Off by default**, so nothing changes for an existing config.
- How the turns are taken (`spreadOrder` in `dns.go`): the band member that was put first longest ago goes first (ties by address), not a plain `turn % n`. A live run with a plain rotation over the rank order gave an uneven 20/23/17 and then 19/47/24 at a 20 % band, because the rank order and even the membership of a tight band move with every jittery latency sample; least-recently-picked stays even when members come and go and gives a server that was out of the band one turn on its return, not a burst.
- The band is relative: 0.2 ms against 0.3 ms is not "within 20 %" and does not spread. The Served column shows the real split.
- Visibility: `--show-dns` prints a `spread:` line per pool (`spread` / `spread_band` in the status data), the DNS page header shows "spread within N%" when on, Settings > DNS proxy has the two fields (`spread` checkbox, band), the DNS help topic has a "Spreading queries" section, the README a Spread entry, `ddgw --help` the keys. Changing either value restarts that pool (an empty cache and a fresh probe round), like any change to the pool's settings.
- No new CLI flag: like `cache` and `forward_updates`, these are keys in the config and fields in Settings, which the GUI and the file both reach.

### Verified
- gofmt, build, vet, `go test -race -count=1 ./...`, CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js. New tests (`spread_test.go`): band membership and the inclusive limit, a lone fastest server leaves the order alone, the input slice is not modified, huge turn counters; spread off keeps fastest-first; spread on gives each of three in-band servers exactly a third and keeps the slow one last; an unhealthy server drops out of the turns; even shares while the three latencies swap places every call; a server leaving the band for 20 of 60 queries gets 13-14, the others 22-24; 30 real UDP queries against four fake servers (three at 20 ms, one at 120 ms): each fast server about a third, the slow one only its probes; failover inside the band (a dead server, then a SERVFAILing one); config defaults, parsing, validation (0, -5, 1001 refused, 1000 allowed). Mutation checks (`<=` to `<` in the band test, rotation start fixed at 0, the forwarder using `Ranked` instead of `Candidates`) each made tests fail.
- Live (real daemon, four stub servers on 127.0.0.41-44, three with a 20 ms delay and one with 100 ms, per-server query logs): at a 20 % band 150 queries split 50/50/50 and the slow server got none; at a band of 100 90 queries split 30/30/30; with one fast stub killed, 30 of 30 queries were still answered and the other two shared them; switching `spread` off in the file (hot reload) made `--show-dns` print `spread: off` and the queries went to the fastest only (it moves between near-identical servers as their latencies jitter, as before).
- Chromium, light and dark: the DNS page header shows "spread within 20%", Settings shows both fields and saved `spread: true`, `spread_band: 35` into the shared block while the gateway's own block stayed untouched, the DNS help panel shows the new section.

### Not verified
- A setting of `spread_band` below the jitter of the servers' latency does not spread evenly by design (members leave and enter the band as latencies move); this was seen live and is why the least-recently-picked rule exists, but very tight bands on noisy links are not guaranteed to share evenly.
- Real upstream resolvers over a real network; only loopback stubs were used. Per-gateway pools (a gateway's own `spread`) go through the same code and config path as the shared block but were not run separately beyond the config test.
- The page's response to an out-of-range band (0) in the Settings form: the daemon refuses it (tested), but the exact message shown in the browser was not captured.

## [v68] - 2026-10-01 — A memory guard: drop the oldest history before memory runs out

Asked for: keep an eye on memory utilization every so often; at 85 % flush the oldest statistics, cache items and host history to hold that threshold and prevent out-of-memory.

### Added
- **Memory guard** (`memguard.go`). Every 30 s it reads memory use: the machine's (`/proc/meminfo`, MemTotal minus MemAvailable, the same figure as the Host page) and, inside a container with a memory limit
  (cgroup v2 `memory.max`, or v1 `memory.limit_in_bytes`), the container's use minus its inactive file cache against that limit; the higher of the two counts. At or above the limit (85 %) it runs rounds: each drops
  **the oldest tenth of the time span** covered by the statistics (minute counters and both top-list tiers) and the host history, and **a tenth of the answer cache's entries, least recently used first**; then it returns the
  freed memory to the system (`debug.FreeOSMemory`), waits a second and measures again, until use is below the limit or 5 rounds are done; the next check carries on if still above. Statistics and host history are cut at
  the same moment, rounded up to a whole hour (the top lists are kept in hours) so charts and lists agree; newer data always outlives older, and **the newest hour is never dropped** (found in the live run: with only
  minutes of history a round would otherwise have wiped everything, and a machine short of memory for another reason would keep losing its recent history every 30 s).
- Visibility: a WARN line per episode (memory before and after, what was dropped); the footers of Statistics and Host show "Memory guard: the oldest history was dropped N times ... Last: ..." once it has acted, and
  `ddgw --stats` / `ddgw --host` print the same line (`guard` in `/api/qstats` and `/api/host`).
- `DDGW_MEMORY_LIMIT_PERCENT` (1-99) in the daemon's environment changes the 85; anything else is ignored with a warning. No config key and no off switch.

### Verified
- gofmt, build, vet, `go test -race`, CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js. New tests: nothing happens at 84.9 %; at 90 % with readings 90/88/86/84 exactly three rounds run and the oldest ~27 % of
  30 days of statistics and host history and ~27 % of a 100-entry cache are gone, the oldest day is empty, the newest data is there, the least recently used cache entry went first; the 5-round cap and the next check
  carrying on; nothing to drop does not claim a trim; the newest hour kept at 99 % (and older data going once it is older than an hour); the guard racing four goroutines that record, query and use the cache (under `-race`); meminfo, cgroup v2, cgroup v1, "max"/unlimited and a broken meminfo; the environment
  variable. Mutation checks (host not trimmed, cache not trimmed, no stop under the limit) each made a test fail.
- Live (real daemon, `DDGW_MEMORY_LIMIT_PERCENT=1` so the 7 % the sandbox uses counts as too much): with a saved file holding 70 hours of statistics and host history (one minute per hour, 700 queries, loaded at start-up), the next check logged
  `memory guard: memory was 7.0% (limit 1%): dropped the oldest history (28 statistics minutes and 27 top-list slots, 28 host minutes) and 0 cached answers; now 6.9%` (five rounds, about 41 % of the span), the 7-day totals went from 700 to 420,
  the footer text and `ddgw --stats` / `--host` printed the guard line; with only minutes of history the guard left the statistics alone (the newest-hour rule) and trimmed the cache (42 → 23 entries in one check, 12 after the next).
  The first live run is what showed the need for that rule. At the cut the top lists can hold one hour-slot more than the counters (430 against 420 above) because the slots are whole hours.
- Chromium was not used for this change: the only page change is the footer line, covered by `node --check`.

### Not verified
- Real memory pressure: the live run lowers the limit with the environment variable instead of filling the machine, and a container with a memory limit was not available (the cgroup readers are tested with files).
- The guard can only free what ddgw holds (up to about 100 MB); if another program fills the memory it keeps trimming and cannot help.

## [v67] - 2026-10-01 — Statistics: the Updates tile is a filter, and shows the recent updates

Asked for: the recent-updates card should not always be visible; clicking the Updates tile should show it, like NX Domain and Server Failure.

### Changed
- **The Updates tile is a button** like the other answer tiles. Clicking it limits the chart to the updates line, the donuts and the top lists to updates (Top clients: who sent them; Top domains: the zones;
  clicking a client or zone drills down as before) and **shows the Recent dynamic updates card**. Without it the card is not on the page (and is not fetched). Total Queries clears it.
- To make the lists possible, updates are **a kind of their own** (`update`, next to noerror/servfail/nxdomain/refused/other): the top lists keep them apart, `--stats-rcode update` and `rcode=update` select them, and the
  minute counters no longer count an update a second time under No Error, Refused, ... (Total = the five answer kinds + Updates). The Updates tile's failed count is unchanged.
- Files saved by v64–v66 still load (their top-list records have one kind fewer). Updates seen by those versions stay counted under the answer kind they had in the old minute counters.
- Help, README and usage text updated.

### Verified
- gofmt, build, vet, `go test -race`, CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js. New tests: updates counted in the total and as Updates but not under NoError/Refused, `rcode=update` lists the zones and
  senders and the transports, the NoError list no longer holds updates, client drill-down inside the update kind, the filter accepts `update`; a file with five kinds per record loads.
- Live (real daemon, three `nsupdate` runs plus query traffic): `--stats` showed 107 = 104 No Error + 3 Updates (1 failed); `--stats --stats-rcode update` listed the senders, the zones and the transport. Chromium light and dark: at the start the card is not on the page and Total is pressed; clicking Updates presses it, leaves one chart line, titles the lists "— Updates" and shows the card; clicking Total Queries removes the card again; no page errors.

### Not verified
- Nothing beyond the above.

## [v66] - 2026-10-01 — A response cache in the DNS proxy

Asked for: response caching (third of three requests, one version each).

### Added
- **Answer cache** (`cache.go`), per pool, on by default. A repeated query is answered from memory while the records' TTLs allow; the upstream servers are asked once per name
  and TTL instead of once per client. Rules, in short: NOERROR answers (with data or "no data") and NXDOMAIN are kept; a negative answer needs the zone's SOA in the
  authority section and lives for its MINIMUM (RFC 2308), with the SOA's own TTL shown as that minimum; an entry lives for the smallest TTL of its records, at most
  `cache_max_ttl`; the TTLs clients see count down with the entry's age and are capped to `cache_max_ttl`; LRU eviction at `cache_entries`. Never cached: SERVFAIL, REFUSED,
  truncated answers, IXFR/AXFR/ANY, a response with TSIG/TKEY, a TTL of zero, queries that are not plain (opcode, more than one question, extra records, EDNS options other
  than a client cookie — this includes ECS sent by the client), answers whose OPT record carries anything but cookie/padding/NSID (e.g. Extended DNS Errors).
- The key is lower-cased name, type, class, RD/AD/CD, EDNS present, DO, the UDP size the client allows, the transport, and with ECS on the client's /24 (/56) so each network gets its own answer.
  Each hit gets the client's ID and its own spelling of the name (0x20); cookies are dropped from the stored answer. A hit counts as a handled query (pool counters, Statistics).
- It lives in the pool, so any change that makes a new pool (servers added, paused or removed, ECS, timing) starts with an empty cache. No stale answers are served: with all upstreams
  down, a name whose TTL has run out gets SERVFAIL (serve-stale is a possible later addition).
- Settings: `dns.cache` (true), `dns.cache_entries` (10000, 100-1000000), `dns.cache_max_ttl` (3600 s, 1-604800); Configure ▸ Settings ▸ DNS proxy has the three fields (shared in a cluster).
- Visibility: `ddgw --show-dns` prints an `answer cache:` line per pool (entries of max, hits, misses, hit rate, not cacheable, evicted, max TTL); the DNS page shows "cache N% hits · M kept" in each
  pool's header with the details as a tooltip. Help (DNS and Settings topics), README and the usage text updated.

### Changed
- `TestFrontendUDPAndTCP` now runs with the cache off: it checks the SERVFAIL for a dead pool, which a cached answer would hide.
- A config saved by v66 contains the three new keys (like every DNS setting); a version before v66 refuses a file with unknown keys, so downgrading after saving a config needs those keys removed.
  An update rolled back by the boot guard right after installing is not affected unless the config was saved in between.

### Verified
- gofmt, build, vet, `go test -race`, CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js. New tests: key variations (case, type, name, transport, EDNS, DO, size, CD, RD, cookie value
  ignored, ECS networks /24 and /56), everything that must bypass the cache, aged TTLs and the client's ID and name spelling, expiry at the shortest TTL, the max-TTL cap shown and enforced, negative answers
  (no SOA not stored, SOA minimum, NODATA, referral not stored), what is never stored, cookie/padding dropped cleanly from the stored OPT, LRU order and eviction count, 8 goroutines under `-race`,
  config defaults and bounds, and through the real frontend: the second identical query (other case, other client, other ID) never reaches the upstream, another type does, SERVFAIL is not cached,
  statistics and pool counters include hits, the cache switched off. Mutation checks (no aging, ECS network left out of the key, negative SOA TTL, name spelling, zero TTL stored) each made a test fail.
- Live (real daemon, a stub upstream that counts queries, TTL 20 s, NXDOMAIN with an SOA of minimum 15): a repeated name reached the stub once; a case-changed repeat was answered with the TTL counting down (20 → 17 after 3 s) and the client's spelling; after the TTL the stub was asked again; NXDOMAIN was asked once and the repeat showed the SOA TTL as 15; SERVFAIL was asked twice (not cached); 130 distinct names into a 100-entry cache: 100 kept, 30 evicted, hits and misses as counted by hand; editing the config file (atomic rename) to `cache:false` started a new pool that sent every query upstream and `--show-dns` said "off", then back on with 100 entries (a fresh, empty cache); the Settings page showed the three fields and saved `cache_entries` to the file. Chromium light and dark: the pool header shows "cache 4% hits · 100 kept" with the tooltip, no page errors.

### Not verified
- Behaviour against real recursive resolvers and DNSSEC-validating clients (only a stub upstream was used); the hit rate on real traffic.
- Memory use at the 1,000,000-entry limit.

## [v65] - 2026-10-01 — Statistics: a list of recent dynamic updates

Asked for: the recent updates list (second of three requests, one version each; response caching is next).

### Added
- **Recent dynamic updates** (`updatelog.go`): the proxy keeps the last 200 dynamic DNS updates it handled — time, client, zone, the primary named in the SOA
  (name and the address the message went to), the answer (NOERROR, REFUSED, NOTAUTH, SERVFAIL, ...), and **what the message changed**, read from its update
  section in the words of RFC 2136 (`add host1.example.com A 192.0.2.7`, `add alias.example.com CNAME host1.example.com`, `delete old.example.com A`,
  `delete one.example.com A 192.0.2.5`, `delete everything at gone.example.com`; the data is shown for A, AAAA, CNAME, PTR and NS; at most 8 records, then
  "and N more"). An update that never reached a primary says why (no SOA found, the primary did not answer). The list is saved with the statistics
  (`stats.json.gz`, new record kind `upd`; files of v64 still load) so a restart keeps it.
- Where: a **Recent dynamic updates** card below the top lists on Monitor ▸ Statistics (newest first, green/red result, independent of the range); `GET /api/dnsupdates`
  (relayed to the picked node, in `proxyPrefixes`); `ddgw --dns-updates`. Help, README table and usage text updated.

### Fixed
- Help text typo ("hide or show that line., and hover").

### Verified
- gofmt, build, vet, `go test -race`, CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js. New tests: every change form above including
  a prerequisite that must not be listed, the 8-record cap, truncated messages at every length (no panic, never a nil list), the 200 ring (order, cap, restore
  drops bad and future entries), a delivered and a refused update through `handleUpdate` (primary, address, note, TCP flag, changes), and a save/load round trip of the list.
- Live (real daemon, stub resolver and stub primary): three `nsupdate` runs (a single add; delete/add/CNAME/delete-all; an unknown zone): `ddgw --dns-updates` and the Statistics card listed all three with the right changes, primary, address and results, the failed one with its reason; after a SIGTERM and restart the list came back ("restored 8 entries"). Chromium light and dark: the card renders, no page errors; a first version had the Result column pushed out of view by a long reason, fixed (compact cells, the reason wraps).

### Not verified
- The prerequisites of an update (the "only if ..." conditions) are not listed; only the changes are.

## [v64] - 2026-10-01 — Statistics and Host history survive a restart

Asked for: persistent statistics (first of three requests, one version each: this, then the recent updates list, then response caching).

### Added
- **Statistics and Host are saved to disk** (`persist.go`). The query statistics (minute counters, the 10-minute and hourly top lists with their
  client/domain pairs, update counters) and the host statistics (per-minute CPU, memory, disk, network, filesystems and the tracked mounts) are
  written to `<state-dir>/stats.json.gz` every 5 minutes and once more when ddgw stops (also before an update re-executes it), and read back at
  start-up before the first query is counted. A restart or an update no longer empties the 30 days; the time ddgw was not running is a gap in the
  charts. "Counting since" is the very first start. The file is a gzip stream of JSON values, written to a temporary file, synced and renamed, mode 0600
  (the directory 0700). Saving copies one slot at a time under the collector's own lock and encodes outside it, so queries are not held up.
  A damaged file keeps whatever was read before the damage and logs a warning; an unknown format or a stream without header is refused; slots older
  than 30 days or in the future are dropped.
- Each node has its own file; nothing is shared in a cluster. A hard kill (`kill -9`, power loss) loses at most the last 5 minutes.

### Changed
- **Privacy:** until now nothing was written to disk. The file holds client addresses and the names they asked for. It is readable by root only; delete
  `stats.json.gz` (ddgw stopped) to forget it. There is no switch to turn the saving off.
- Wording: the Statistics and Host footers, Help topics, README and the `--stats` / `--host` headings no longer say "in memory only / starts from zero".
  The uptime tracker (topology tooltips) is still in memory and still restarts from zero.

### Verified
- gofmt, build, vet, `go test -race`, CGO-off vet and test, five cross-compiles, `node --check` app.js and help.js. New tests: full round trip (queries of every
  kind, client/domain pairs, update counters, host rows, mounts, "since"), then identical `Query` results after loading into fresh collectors;
  missing file, stale data after 40 days, a truncated file, not-gzip, wrong format, no header; file mode 0600 and no temporary file left; the saver
  creating the directory and writing at stop. A mutation check (dropping a restored field) made the round-trip test fail.
- Live (real daemon, stub resolver): traffic, `ddgw --stats` totals, SIGTERM, file present at 0600, restart: log "restored 3 entries", identical totals and
  the original "counting since"; `--host` heading shows the same.
- Live, hard kill: more traffic, waited for the 5-minute timer (the file was rewritten at the tick, 385 → 722 bytes), `kill -9`, restart: "restored 9 entries" and a total of 208 = both rounds of traffic.
- Chromium, light and dark: the new Statistics and Host footer texts show, no page errors.

### Not verified
- Very large files (the 65 MB worst case of memory) were not timed; the save of a small network is a few milliseconds.

## [v63] - 2026-10-01 — Statistics: an Updates tile

Asked for: an Updates box on the Statistics page.

### Added
- **Updates tile** (Monitor ▸ Statistics, between Refused and Clients): the number of dynamic DNS updates in the range, with a small line saying
  how many the primary did not accept ("2 failed" / "none failed"). Updates were already counted in Total and listed as type UPDATE under the zone
  name; they now also have their own per-minute counter (`updates`, `updates_failed`), so `/api/qstats` returns `updates` (series) and
  `sums.updates` / `sums.updates_failed`. The chart gets an **Updates** line (teal, light and dark) once the range has any. The tile is not a
  filter (the type UPDATE already shows in the lists). `ddgw --stats` prints an `Updates` line with the failed count. Help and README updated.
  Memory: 8 more bytes per minute slot (about 350 KB for the 30 days).

### Verified
- gofmt, build, vet, `go test -race`, CGO-off vet and test, five cross-compiles, `node --check` on app.js and help.js; new `TestQStatsUpdateCounter`.

- Live: real daemon with a stub primary and stub resolver, three `nsupdate` runs (two accepted, one NOTAUTH): the tile read "3 / 1 failed / Updates", `ddgw --stats` agreed, and after a restart one update read "none failed". Chromium light and dark: the tile and the teal legend/line show, no page errors.
- A single CGO-off `go test` run failed once (a timing-based DNS test; the output was not captured) and passed in five reruns afterwards, as did the race run. The cause was not found.

### Changed
- The tile row's minimum tile width went from 9.5rem to 8.25rem so seven tiles stay on one row at a normal window width (Clients wrapped to a second row otherwise).

- The footer of Monitor ▸ Host is shortened to "Sampled every 10 seconds and averaged per minute; kept in memory since <date> for 30 days" (the notes on peaks, disk I/O, link use and the interfaces marked * are gone from the page; the Help topic still explains them).

### Not verified
- The shortened Host footer was not looked at in Chromium (a one-line text change; `node --check` only).
- Clicking the Updates tile does nothing by design; there is no per-update filter. A list of recent updates (zone, client, primary, result) was not built; it is in the log.

## [v62] - 2026-10-01 — Dynamic DNS updates are forwarded to the zone's primary server

Asked for: look for update messages, look up the SOA of the zone, forward the update to the primary server listed in the SOA so it
handles the registration.

### Added
- **RFC 2136 UPDATE forwarding** (`updates.go`). A message with opcode UPDATE arriving on the VIP (UDP or TCP) is no longer sent to a
  resolver: ddgw asks the pool for the SOA of the zone in the message (an SOA in the authority section counts too), takes the primary
  (`MNAME`), resolves it (A and AAAA through the pool; the zone's NS records when it does not resolve or is "."), and forwards the
  message **byte for byte** to port 53 of the first address that answers — ID and TSIG/SIG(0) stay valid — and returns the primary's
  reply as it came. TCP in, TCP out; a truncated UDP answer is retried over TCP. Primaries are cached for the SOA TTL (30 s – 5 min)
  and looked up again once when the cached one fails.
- Replies ddgw makes itself (opcode UPDATE): NOTAUTH (no SOA), SERVFAIL (lookup or delivery failed), FORMERR (not exactly one zone, or
  zone type not SOA, or truncated), NOTIMP (class other than IN), REFUSED (switched off, or the update looped back to this node).
- It never forwards to the VIP itself and ignores a retransmission of an update already being forwarded and refuses one whose (ID, zone) is in flight for another client, so a primary name that
  points back at a ddgw address cannot loop.
- Logging: every update logs one line (INFO when the primary accepted it, WARN otherwise) with zone, client, primary and result;
  Statistics counts it as type **UPDATE** under the zone name (so it also shows in the top lists and `ddgw --stats`).
- Setting `forward_updates` (default **true**, shared; Configure → Settings → DNS proxy, or `dns.forward_updates` in the file, also
  per gateway pool). Off: updates are answered REFUSED.

### Changed
- The gateway circle's tooltip no longer lists the anycast addresses (they are drawn right next to it; each pill has its own tooltip,
  with its uptime). It keeps the status, uptime, per-family state and serving nodes.
- Before this, an UPDATE was passed to the first upstream like a query and answered by the resolver (usually REFUSED/NOTIMP).
- Security note (README): anyone who can reach the VIP can now have updates delivered to the zone's primary; its own authorisation
  (TSIG, ACLs) decides. A primary that filters by source address sees the ddgw node.

### Verified
- `gofmt -l .`, `go build`, `go vet ./...`, `go test -race -count=1 ./...` (new `updates_test.go`: byte-for-byte delivery and relayed
  reply, cache, TCP, SOA in the authority section, NS fallback, NOTAUTH, never-to-itself, malformed/NOTIMP/switched off, through
  `resolve` and the statistics, `readName` with compression), CGO-off vet and test, cross-compiles for amd64, arm64, arm, 386, riscv64;
  `node --check` on app.js and help.js.
- Live: a real daemon with a stub resolver (SOA/A) and a stub primary on 127.0.0.1:53; the real `nsupdate` (bind9-dnsutils) sent an
  update to the VIP — delivered to the primary (opcode 5), the client got NOERROR; the same with a TSIG key (the primary saw the TSIG
  record); an unknown zone gave NOTAUTH; the log lines and `ddgw --stats` (type UPDATE) matched; in Chromium (light and dark) the
  Settings checkbox is there, saves to the config file and reloads, and the gateway tooltip no longer has the Anycast lines while
  each anycast pill keeps its own.

### Not verified
- Against a real primary (BIND, Windows DNS, Knot): only stub primaries were used, and the stub did not validate the TSIG. A primary
  that requires the update to come from the client's address will refuse it.
- GSS-TSIG (Windows secure updates) is only passed through untouched like any other additional record; not tried.
- Cross-compiles run with cgo off (PAM stub); only the native amd64 build was checked with PAM.

## [v61] - 2026-10-01 — Monitor → Host: CPU, memory, disk and network, kept 30 days

Asked for: Monitor → Host showing CPU, memory, disk and network utilization, kept for 30 days in memory like Statistics.

### Added
- **Monitor → Host** page (and `ddgw --host [--host-range 1h|1d|7d|30d]`): six tiles with the latest sample (CPU and load
  average, memory in use, fullest disk, disk I/O busy, network in, network out), four charts (CPU utilization, Memory utilization,
  Disk utilization — space per filesystem plus I/O busy —, Network utilization in and out) over the same ranges as Statistics (Last
  Hour / Day / Week / Month / custom; hover gives value and peak), and tables of filesystems and network interfaces (state, link
  speed, in/out, link use). The picked node (Node menu) is the one shown.
- `host.go`: a sampler reads `/proc/stat`, `/proc/meminfo`, `/proc/loadavg`, `/proc/diskstats`, `/proc/net/dev`, `/proc/mounts`
  (+ statfs) and `/sys/class/net/*` every 10 s and keeps per-minute sums and peaks for 30 days in a fixed ring (about 4 MB), in
  memory only, starting from zero when ddgw restarts. Nothing to install.
- `GET /api/host?from=&to=` (relayable to other nodes); a help topic; README section.

### Notes on what is measured
- CPU: busy share of all cores (everything but idle and iowait). Memory: total minus MemAvailable (caches count as free).
- Disk I/O: share of time the busiest whole disk was busy. Disk space: up to six disk filesystems (ext*, xfs, btrfs, zfs, f2fs,
  vfat, ntfs, …), one per device, percent as `df` counts it; the tile shows the fullest one of at least 1 GiB.
- Network: bytes per second summed over the counted interfaces; loopback, ddgw's `ddgwN.M` links, `veth*`/`docker*`/`br-*`/`virbr*`/
  `cni*`/… and ports of a bridge or bond are left out so traffic is not counted twice. Link use = the busier direction as a share of
  the speed the driver reports (shown as "–" when it reports none, as most VMs do).
- A counter that goes backwards (reboot, interface reset) gives a gap, not a spike; a minute with no samples is a gap.

### Verified
- `gofmt -l .`, `go build`, `go vet ./...`, `go test -race -count=1 ./...` (new `TestParseProc`, `TestHostSampleAndQuery`,
  `TestHostRetentionAndSteps`, `TestHostMountsTracked`; the new route is in the session and proxy tests), CGO-off vet and test,
  cross-compiles for amd64, arm64, arm, 386, riscv64; `node --check` on app.js and help.js.
- Live: a real daemon under a CPU busy-loop and `dd` writes: `ddgw --host` showed CPU ≈ 50 % on 2 cores, memory, the three
  filesystems and the interfaces; in Chromium (light and dark, logged in as a PAM user) the page drew the tiles, charts and tables,
  switched ranges, the hover tooltip showed value and peak, and the "?" help panel carried the Host topic and its command lines, with no script errors.

### Not verified
- Link-use percentages (the VM reports no link speed), swap, bonded or bridged NICs, and btrfs/zfs space accounting were only
  covered by the parsing and unit tests, not on real hardware. No two-node cluster run (same `/api/proxy` path as the other pages).
- Cross-compiles run with cgo off (PAM stub); only the native amd64 build was checked with PAM.

## [v60] - 2026-10-01 — Topology uptime tooltips; Statistics: client names and domain whois on hover

Asked for: the mouseover of each Topology item shows how long it has been online since its last
failure; hovering a client shows its DNS names; hovering a domain shows its whois information.

### Added
- Tooltips of the gateway circle, each anycast address, each DNS server and each domain end with
  "Online for 2h 5m since its last failure", "Online for … (no failure seen since ddgw started)" or
  "Down for 7m 12s". Green and yellow count as online, red as failing; grey (not known yet) and
  paused items show nothing. The circle's tooltip also adds the uptime to each anycast line.
- `ddgw --canvas` prints the same in brackets after each item.
- `uptime.go`: an in-memory tracker (`upTracker`) fed by a sampler that rebuilds the picture every
  2 s while the daemon runs, so an outage is noticed with no browser open. JSON: an `uptime`
  object `{up, since, failed}` on gateways, servers, tests and anycast entries of `/api/canvas`.

- **Client tooltip** (Statistics): every reverse-DNS name the daemon has for the client (up to 5), or
  "No reverse DNS name known (yet)". The daemon now looks names up for **every** client in the list, not
  only the top 25 (8 lookups at a time, cached 10 min), and the name shows under the address for all rows.
- **Domain tooltip** (Statistics): whois data — registrar, registrant when not redacted, registered /
  updated / expires dates, name servers, which server answered. Fetched on the first hover, never before:
  `whois.go` finds the registry through whois.iana.org (TCP 43), asks it about the registered name (last
  two labels, three under a built-in list of second-level suffixes such as co.uk — a heuristic, not the
  public suffix list), parses the common thin/thick formats (Verisign-style and Nominet-style tested),
  caches hits 24 h and misses 1 h (2000 entries), and runs at most 4 lookups at once. Private or reserved
  suffixes (.lan, .local, .arpa, …), addresses and single labels are refused with a reason.
  **Privacy:** the domain name (and nothing about the client) is sent to its registry's whois server from
  the picked node; a node without outbound port 43 shows "not reachable".
- `GET /api/whois?domain=NAME` (relayable to other nodes) and `ddgw --whois NAME`.

### Changed
- None to config. The uptime times are not saved: a restart of ddgw starts counting again
  (and says so in the tooltip), and each node tracks its own view of the picture.

### Verified
- `gofmt -l .`, `go build`, `go vet ./...`, `go test -race -count=1 ./...` (new `TestUpTrackerTransitions`,
  `TestUpTrackerAnnotate`, `TestDurText`, `TestParseWhois`, `TestWhoisName`, `TestWhoisLookupCachesAndRefers`,
  `TestWhoisUnreachable`; the new routes are in the session and proxy tests), CGO-off vet and test, cross-compiles for amd64, arm64, arm, 386,
  riscv64; `node --check` on app.js and help.js.
- Live: a real daemon with a stub upstream and an anycast address, no browser open: `--canvas` showed
  "online for 6s (no failure seen since ddgw started)", after stopping the stub (16 s) "down for 14s" on the
  gateway, server and domain, and after restarting it "online for 10s since its last failure"; in Chromium
  (light and dark, logged in as a PAM user) the tooltips of the circle, anycast pill, server and domain
  carry the line, with no script errors.
- Live (tooltips): with /etc/hosts pointing whois.iana.org and the registry at a fake whois server on
  127.0.0.43:43 and two names for 127.0.0.5: the client tooltip listed both names and the address; hovering a
  domain gave "Registrar … Registered … Expires … Name servers …" and a not-registered one gave the reason;
  `ddgw --whois` printed the same; no script errors. /etc/hosts restored afterwards.

### Not verified
- Whois against the real registries and real reverse-DNS servers: the sandbox cannot reach them, so only the
  fake server above and the format samples in the tests were used. Registries whose format differs from the
  Verisign and Nominet styles may show fewer fields (or "nothing this page can read").
- Not run against two clustered daemons; cross-compiles run with cgo off (PAM stub), only the native amd64
  build was checked with PAM.

## [v59] - 2026-10-01 — Statistics: which client asked for which domain

Asked for: see which client is asking for what domains.

### Added
- **Drill-down on Monitor → Statistics.** Click a client in Top clients and Top domains lists what
  that client asked for; click a domain in Top domains and Top clients lists who asked for it.
  One at a time; **Show all** or clicking the row again clears it. It combines with the answer-kind
  tiles (NX Domain + a client = the names that client fails on) and the range. Shares are of the
  picked client's or domain's queries.
- `GET /api/qstats?client=ADDR` / `?domain=NAME` and `ddgw --stats --stats-client ADDR` /
  `--stats-domain NAME` (not both). Bad values are refused with a message.

### Changed
- The collector now also counts (client, domain) pairs per slot and answer kind, at most 500
  (No Error), 200 (NX Domain), 100 (Server Failure), 50 (Refused), 30 (Other) distinct pairs per
  slot. A pair beyond the cap is not kept; the reply shows the difference to the client's or
  domain's exact total as "(others)". Memory in a test of 30 days at 25 queries/min over 20,000
  names: 39 → 62 MB with 5 clients, 45 → 85 MB with 60 clients; the caps bound the worst case at
  about 55 MB more. `QStats.Query` takes a `QFilter{Rcode, Client, Domain}`.
- Statistics top-list rows are buttons; a note line above each list says when it is filtered.

### Verified
- `gofmt -l .`, `go build`, `go vet ./...`, `go test -race -count=1 ./...` (new
  `TestQStatsClientDomainPairs`, `TestQStatsFilterArg`), CGO-off vet and test, cross-compiles for
  amd64, arm64, arm, 386, riscv64; `node --check` on app.js and help.js.
- Live: a real daemon (PAM user, stub upstream with NOERROR/SERVFAIL/NXDOMAIN/REFUSED by name,
  three client addresses): `--stats-client`, `--stats-domain` (with `--stats-rcode`) and a bad
  client; in Chromium (light and dark) client and domain picks, the NX Domain combination,
  Show all and toggling a row off, with matching counts and shares and no script errors; the two
  cards stay aligned.

### Not verified
- Not run against two clustered daemons (same `/api/proxy` path as the rest of the page). Memory
  was measured with a synthetic load, not a real network.
- Cross-compiles run with cgo off (PAM stub); only the native amd64 build was checked with PAM.

## [v58] - 2026-10-01 — Statistics: click a tile to see which domains and clients

Asked for: find out which domains give NXDOMAIN; the same for every tile, with Total Queries
restoring the line chart.

### Added
- **Answer-kind filter.** On Monitor → Statistics the No Error, Server Failure, NX Domain and
  Refused tiles are buttons. Picking one draws only that line in the chart and makes the
  Query types and Transport donuts and the Top clients / Top domains lists count only those
  queries (titles say "— NX Domain", shares are of that kind). **Total Queries** clears the
  filter. The Clients tile is not a filter.
- `GET /api/qstats?rcode=noerror|servfail|nxdomain|refused|other` and
  `ddgw --stats --stats-rcode KIND` (the CLI lists then count only that answer).
- Help topic and README describe it.

### Changed
- The daemon now keeps the types, transports, clients and domains **per kind of answer** in
  every slot instead of one set. Distinct-name caps per slot and kind: No Error 300 clients /
  600 domains (as before), NX Domain 150/300, Server Failure 100/200, Refused 100/200, Other
  50/100; the rest of a kind counts as "(others)". The unfiltered lists are the sum of the
  kinds, so they can now show more than 300 clients / 600 domains in total. Worst-case memory
  rises (a flood in several kinds at once); the usual case, where failures are a small share,
  barely changes. The page footer still says 300 and 600 (the No Error limit).
- `QStats.Query` takes the answer kind; the time series and the sums always cover everything.

### Verified
- `gofmt -l .`, `go build`, `go vet ./...`, `go test -race -count=1 ./...` (new
  `TestQStatsByAnswerKind`, `TestQStatsRcodeArg`; the cap test now looks at the No Error set),
  CGO-off vet and test, cross-compiles for amd64, arm64, arm, 386, riscv64; `node --check`
  on app.js and help.js.
- Live: a real daemon with a PAM user and a stub upstream answering NOERROR / SERVFAIL /
  NXDOMAIN / REFUSED by name; `ddgw --stats --stats-rcode nxdomain` and a bad kind; in Chromium
  (light and dark) clicking NX Domain, Server Failure and Total Queries gave the expected
  lists, shares, donut titles and single-line chart, and no script errors.

### Not verified
- Not run against two clustered daemons (the filter rides the same `/api/proxy` call as the rest
  of the page). Memory with several kinds under flood was not measured.
- Cross-compiles run with cgo off (PAM stub); only the native amd64 build was checked with PAM.

## [v57] - 2026-10-01 — Statistics: the page follows the Node menu only

Asked for: delete the page's own scope menu; the top-bar Node menu is used to look at each
member's statistics individually.

### Removed
- The **This node / Cluster (all members)** menu on Monitor → Statistics and the code that added
  up every member's answer (the v55/v56 "Cluster" view). The page now shows the node picked in
  the top-bar Node menu (through `/api/proxy`, like every other page); each node counts only
  the queries it answered itself.

### Changed
- Help topic and README Statistics section no longer mention the Cluster view; the "top lists
  add up each member's top 100" caveat is gone with it. No Go, CLI or API change.

### Verified
- `gofmt -l .`, `go build`, `go vet ./...`, `go test -race -count=1 ./...`, CGO-off vet and test,
  cross-compiles for amd64, arm64, arm, 386 and riscv64; `node --check` on app.js and help.js.

### Not verified
- Not looked at in Chromium or against two live clustered daemons this time: the change only
  deletes the menu and the merge code, and the page uses the same `api()` call as before.
- Cross-compiles run with cgo off (PAM stub); only the native amd64 build was checked with PAM.

## [v56] - 2026-10-01 — Statistics: 30 days of history, tidier page

Asked for: keep 30 days (not 96 hours); shorter footer text; fix the transport chart spacing;
fix the right-hand spacing of the node picker; drop the Show table button.

### Changed
- **30 days of statistics** instead of 96 hours, still only in memory. Ranges are now Last
  Hour, Last Day, Last Week, Last Month and Custom (any start and end inside the last 30 days).
  The line chart uses a point per minute (up to 2 h), 10 minutes (up to 36 h), hour (up to a
  week) or 3 hours. Per-minute totals are kept for the whole 30 days. The top lists, types and
  transports are kept in two tiers so memory stays modest: 10-minute steps for the last day
  (a range inside it is exact) and hourly steps for the 30 days (a longer range reads these,
  so its top lists start and end on a whole hour). The caps are unchanged: the busiest 300
  clients and 600 domains per step, the rest "(others)". Measured heap for a full 30 days: about
  27 MB (5 clients, ~400 names per hour), 53 MB (60 clients, ~1000 names), 66 MB with every cap
  hit in every step.
- **`--stats-range`** takes `1h`, `1d`, `7d` or `30d` (or unix seconds).
- **Footer** now reads "Counted in memory since <date> and kept for 30 days." and "Top lists are
  collected in 10-minute intervals and keep the busiest 300 clients and 600 domains."
  (Beyond a day the intervals are hourly; see above.)
- **Donuts.** Each chart sits beside its legend, so the Query types and Transport cards are the
  same height and the two donuts line up.
- **Node picker.** The scope menu no longer stretches over the whole row and keeps clear of
  the help button at the top right.
- **Show table removed** (and its code).
- The Clients tile lines up with the others.

### Verified
- gofmt, build, vet, `go test -race`, CGO-off vet and tests, cross-compiles (amd64, arm64, arm,
  386, riscv64), `node --check`.
- Tests updated for the 30-day retention and the new steps; a new test for the two tiers (a
  range inside the last day reads the exact slots, a week still sees a query from 3 days ago).
- Chromium (light and dark) against a live daemon: the five ranges, footer, donut and picker
  layout, tile alignment.

### Not verified
- The 30-day memory figures come from a synthetic run (real names repeat across steps more or
  less than my generator does); a daemon was not run for 30 days.

## [v55] - 2026-10-01 — Monitor → Statistics, `ddgw --stats`; Topology legend text

Asked for: a statistics dashboard (query totals and a chart over time, query-type and transport
charts, top clients and top domains, a cluster view); and a shorter Topology legend.

### Added
- **Query statistics.** The DNS proxy counts every client query it answers (all gateways of the
  node): response code (No Error, Server Failure incl. the SERVFAILs ddgw makes itself, NX
  Domain, Refused, other), record type, UDP/TCP, client address and queried name. Kept **in
  memory for 96 hours**; nothing is written to disk and the counters start again at a
  restart. Totals are per minute; types, transports, clients and domains per 10 minutes, with
  the busiest 300 clients and 600 domains per step (the overflow is "(others)").
- **Monitor → Statistics.** Last Hour / Last Day / Last 4 Days / Custom range; tiles for Total,
  No Error, Server Failure, NX Domain, Refused and Clients; a line chart (hover tooltip,
  clickable legend, **Show table**); donuts for query types and transport; Top clients (with
  reverse-DNS name, looked up in the background and cached 10 minutes) and Top domains, 10
  rows or **More** (100). With more than one node a menu switches between **This node** and
  **Cluster**, which adds up every member that answers (and names any that did not).
  Series colours are fixed per entity and were checked with the palette validator in light
  and dark. Week/month/year ranges from the model screenshot are not offered: only 96 hours
  are kept. Cached/Blocked/Authoritative/Recursive/Dropped tiles are left out: the proxy has
  no such notions.
- **`ddgw --stats [--stats-range 1h|1d|4d]`**: totals, query types, transports and the top 10
  clients and domains. `/api/qstats` is relayed to other members like the other pages.

### Changed
- **Topology legend.** The status dots stay on one line; the line below now reads "circle =
  gateway address, squares = DNS servers it forwards to, trapezoids = monitored domains". The
  right-click hint was dropped.
- Client addresses and queried names are now held in memory (not logged or saved), up to 96 h.

### Verified
- gofmt, build, vet, `go test -race`, CGO-off vet and tests, cross-compiles (amd64, arm64, arm,
  386, riscv64), `node --check`.
- New tests: question parsing, record and query (codes, folding of `::ffff:` addresses, protocols,
  types, top lists), step selection, 96 h retention and slot reuse, the cap on distinct names,
  range parsing; `/api/qstats` needs a session and is relayable.
- Real daemon with a stub upstream and 640 queries from 6 source addresses (UDP and TCP, A/AAAA/
  PTR/…, NXDOMAIN, REFUSED, and 40 TCP queries the UDP-only stub could not answer, counted
  as Server Failure): `--stats` and the page agree with what was sent. Chromium (light and dark):
  tiles, chart, hover, hide a series, table, all ranges, Custom, More, donuts, top lists.
  A second daemon joined as a cluster member: the Cluster scope fetched both and matched.
  The merge of several members' answers (offset points, sums, unions, host names) was run on
  synthetic data.

### Not verified
- Cluster totals with real traffic on two members at once (the second member answered no
  queries in the sandbox, so the sum was checked with synthetic data).
- Very high query rates (one global lock guards the counters); IPv6 clients (the sandbox has no IPv6).

## [v54] - 2026-10-01 — Log page, `ddgw --log`, and domain up/down lines in the log

Asked for: log a domain's up/down status, and a way to view and filter the whole log in the GUI.

### Added
- **Domain status in the log.** When a probe query changes result on a server, ddgw logs it:
  `dns: server S: domain D (A) fails: <error>` (warn) and `… answers again (1.2ms)` (info).
  A steady state logs nothing; a domain's first result is logged only if it fails. Server
  UP/DOWN lines are unchanged. So a single failing domain now shows in the log even while
  the server stays up.
- **Monitor → Log.** Filter by words (all must appear, any case, matches marked), level
  (all / info+ / warn+ / errors), time range (15 min … 7 days / all) and line count (200,
  1000, 5000), a **Live** mode that follows the newest line while scrolled to the bottom,
  Refresh, and Download of what is shown. It follows the Node menu like the other pages.
- **`ddgw --log [--log-min LEVEL] [--log-grep WORDS] [--log-since 6h] [--log-lines N]`**:
  the same filters on the command line (root, via the status socket); without `--log-lines`
  it prints the whole matching log.
- **`ddgw.log` in the state directory.** The log still goes to stderr (journal), and is now
  also written to a root-only file, rotated at 2 MB with `.1` and `.2` kept, so the GUI can
  show history that survives a restart without needing journal access. `/api/log` is relayed
  to other members like the other pages.

### Changed
- Disk: up to 6 MB of log per node. The text is duplicated in the journal and the file.

### Verified
- gofmt, build, vet, `go test -race`, CGO-off vet and tests, cross-compiles (amd64, arm64, arm,
  386, riscv64), `node --check`.
- New tests: rotation, line parsing, filters (level, words, time, newest-N, oldest first across
  rotated files), argument parsing, domain-flip logging, `/api/log` needs a session, the
  proxy allows it.
- Real daemon with stub DNS servers (one domain made to fail and recover): the flips are in
  the log, `ddgw --log` with `--log-min`, `--log-grep`, `--log-lines`. Chromium (light, dark,
  phone width): filters, mark-up, empty result, Download, the help panel.

### Not verified
- The Log page through the Node picker against a second real daemon (route allowed by unit test).
- Log rotation under a real 2 MB volume (unit-tested with a small limit).

## [v53] - 2026-10-01 — Topology: more room between the gateway circle and the DNS servers

Asked for: the servers should sit lower to make room for the anycast addresses, with more space
between the servers and the gateway circle.

### Changed
- **Topology drawing.** The row of DNS servers starts 28 px lower below the circle (80 px instead
  of 52) and, when the gateway has anycast addresses, 84 px below the last anycast pill instead
  of 36, so the connecting lines no longer run through or hug the pills. The position is computed from the
  number of anycast addresses, so each one added pushes every server and domain down with it.
  Nothing else moves.

### Verified
- gofmt, build, vet, `go test -race`, CGO-off vet and tests, cross-compiles (amd64, arm64, arm,
  386, riscv64), `node --check`.
- Real daemon in Chromium (light): a dual-stack gateway with four anycast addresses and four
  servers; the servers are clear of the circle and the pills.

### Not verified
- Dark theme screenshot of this change (the layout is the same in both themes).

## [v52] - 2026-10-01 — FRR: no more restart loop when frr-pythontools is missing; existing frr.conf is backed up

Reported from a live node (Debian-family LXC container, newer FRR): Configure → BGP showed
"starting FRR failed: Job for frr.service failed", bgpd "not answering".

### Fixed
- **Root cause.** `systemctl reload frr` printed "The frr-pythontools package is required for
  reload functionality" and failed. ddgw answered every failed reload with a restart, and
  frr.service allows only 3 starts in 3 minutes, so systemd soon refused to start it at all
  ("start request repeated too quickly") and that hid the original error.
- ddgw now reloads only when `/usr/lib/frr/frr-reload.py` exists; otherwise it restarts once,
  on purpose, and the BGP page carries a standing note saying to install `frr-pythontools`.
- `systemctl reset-failed frr` runs before every restart/start, so a unit that hit its start
  limit is started again.
- A failed start/restart now shows the last meaningful lines of FRR's journal (FRR's per-command
  vty lines are skipped) in "Last apply", not just "Job for frr.service failed".

### Added
- **install.sh** installs `frr-pythontools` (Debian family and RHEL family; Arch ships it in
  `frr`), also when FRR was already present. A failure only warns.
- The first time ddgw takes `/etc/frr/frr.conf` over, a file it did not write is saved as
  `frr.conf.pre-ddgw` (never overwritten afterwards). Not yet added: a confirmation before
  taking over, or merging into an existing FRR config.

### Verified
- gofmt, build, vet, `go test -race`, CGO-off vet and tests, cross-compiles (amd64, arm64, arm,
  386, riscv64), `node --check`, `bash -n` and shellcheck on install.sh.
- New tests: apply without the reload script (one restart, no reload, backup kept), the journal
  filter.
- Installer `--dry-run` for ubuntu, fedora, rocky and arch, and with FRR present but the reload
  script missing.
- Real FRR 8.4.4 in the sandbox, real daemon: a foreign frr.conf was saved as `.pre-ddgw`; with
  `frr-reload.py` hidden a neighbor change restarted FRR (`reset-failed`, `restart`, no
  `reload`) and the status note appeared; with it present the change used `reload`; with FRR
  stopped the next apply started it. The exact config from the failing node (4-byte AS,
  IPv4 + IPv6 neighbor and anycast) loads and runs under FRR 8.4.4.

### Not verified
- The failing node's own FRR version (it has `mgmtd`, so FRR 10.x) and LXC; the sandbox has
  FRR 8.4.4, no IPv6 and no real systemd, so the start limit itself was not reproduced.
- The installer's real `frr-pythontools` install on each distribution.

## [v51] - 2026-10-01 — DNS monitor: EWMA column aligned

Asked for: the EWMA column on Monitor → DNS is "quite off".

### Fixed
- **Monitor → DNS tables.** The EWMA header now sits over its values (numeric columns are
  right-aligned, headers included), the small bar comes before the number instead of pushing it
  around, a server that is down shows "–" rather than a stale figure, and Last (ms) shows "–"
  when there has been no answer. Column widths are fixed so every gateway card lines up.

### Verified
- gofmt, build, vet, `go test -race`, CGO-off vet and tests, cross-compiles (amd64, arm64, arm,
  386, riscv64), `node --check webui/app.js`.
- Live daemon with stub DNS servers (one down) in Chromium, light and dark: columns align across
  cards and the down server shows "–".

### Not verified
- Browsers other than Chromium.

## [v50] - 2026-10-01 — DNS monitor: the listening tile is replaced by a line on each gateway

Asked for: the "Listening" tile on Monitor → DNS is messy ("group 1 192.168.122.111:53,
group 1 fd01:dddd:…:53, group 2 …"); make it better or remove it.

### Changed
- **Monitor → DNS.** The Listening tile is gone, leaving Queries, Answered and SERVFAIL. Where
  a gateway answers is now one short line in that gateway's own card header ("answering on
  192.0.2.111:5300", addresses separated by ·), or "not answering on this node". A long IPv6
  address no longer breaks a tile. The API and `ddgw --show-dns` are unchanged.

### Verified
- gofmt, `go vet`, `go test -race -count=1 ./...`, CGO-off vet and test, the five
  cross-compiles, `node --check` on the web files.
- Live daemon with two gateways in Chromium (light and dark): three tiles, each card shows its
  own address; `--show-dns` still prints the listeners.

### Not verified
- The line with an IPv6 address (the sandbox has no IPv6, so only the IPv4 listener came up);
  the earlier lists still stand.

## [v49] - 2026-10-01 — Updates page: shorter hint

Asked for: remove the sentences "Nothing is installed until you update a node or turn on
auto-update. Each node builds the daemon itself, so it needs a Go toolchain, gcc and the
PAM headers (install.sh sets these up)." from the Updates page.

### Changed
- The hint above the upload field on **Operate → Updates** now ends after "other cluster
  members pull it from here." The Updates help panel still explains that nodes build the
  daemon themselves and what that needs.

### Verified
- gofmt, `go vet`, `go test -race -count=1 ./...`, CGO-off vet and test, the five
  cross-compiles, `node --check` on the web files. Text-only change, no page run.

### Not verified
- Nothing new; the earlier lists still stand.

## [v48] - 2026-10-01 — History: fewer controls, everything in one row

Asked for: remove the note field and the Download live config button on History, rename
Upload config… to Upload and put it next to Download.

### Changed
- **History page.** The note box, the *Download live config* button and the separate top
  toolbar are gone. One row of buttons sits at the top of the list (no card title, so it
  fits on one line): Snapshot now, View, Diff vs live, Diff vs previous, Compare ticked,
  Restore, Download, **Upload** (was *Upload config…*, now next to Download).
- A snapshot taken on the page no longer carries a note; `ddgw --version-snapshot --note
  TEXT` still does, and the README and the help topic say so. The live configuration is
  still exported with `ddgw --version-export` (no id); on the page, the *latest* version is
  the same file in practice.

### Verified
- gofmt, `go vet`, `go test -race -count=1 ./...`, CGO-off vet and test, the five
  cross-compiles, `node --check` on the web files.
- Live daemon in Chromium (light and dark): the eight buttons on one row; *Snapshot now*
  adds a version; *Upload* of a configuration file is accepted without an error (the
  identical file adds no version, as before).

### Not verified
- Nothing new; the earlier lists still stand.

## [v47] - 2026-10-01 — History: the version buttons live in the list's header

Asked for: the two rows of buttons on the History page are silly; clean it up.

### Changed
- **History page layout.** One toolbar on top (note, *Snapshot now*, *Upload config…*,
  *Download live config*), and the buttons that act on a version sit in the header of the
  *Versions* card they belong to: View, Diff vs live, Diff vs previous, Compare ticked,
  Download, Restore. They are disabled until a version is highlighted (*Compare ticked* needs
  two ticked rows). *Compare selected* is now called *Compare ticked* so it is not confused
  with the highlighted row. The extra row and its "tick two versions" note are gone.

### Verified
- gofmt, `go vet`, `go test -race -count=1 ./...`, CGO-off vet and test, the five
  cross-compiles, `node --check` on the web files.
- Live daemon in Chromium (light and dark): no buttons in the rows; six header buttons,
  only *Compare ticked* enabled before a selection and all six after; View, both diffs,
  Download and Restore work on the highlighted row; *Diff vs previous* is disabled on the
  oldest version.

### Not verified
- Nothing new; the earlier lists still stand.

## [v46] - 2026-10-01 — History: one set of buttons for the selected version

Asked for: no buttons on every history record.

### Changed
- **History page.** The rows no longer carry View, Diff vs live, Diff vs previous, Download
  and Restore. Click a version to highlight it (or Tab to it and press Enter/Space) and use
  the single row of buttons above the list; they are disabled until a version is selected,
  and *Diff vs previous* is disabled on the oldest one. The checkboxes remain only for
  *Compare selected* (two versions). With the buttons gone the "What changed" column has the
  room it needs instead of wrapping to a few characters.
- The History help topic describes the new way of working.

### Verified
- gofmt, `go vet`, `go test -race -count=1 ./...`, CGO-off vet and test, the five
  cross-compiles, `node --check` on the web files.
- Live daemon in Chromium (light and dark): no buttons in the table rows; the five buttons
  start disabled; clicking a row highlights it and enables them; View, Diff vs previous,
  Diff vs live, Download (file `ddgw-config-<id>.json`) and Restore (after its confirmation,
  no error) work on the selected row; *Diff vs previous* is disabled on the oldest version.

### Not verified
- Nothing new; the earlier lists still stand.

## [v45] - 2026-10-01 — DNS proxy is always on; ddgw.conf.example moves to source/

Asked for: remove the "Serve DNS proxy on this VIP" setting (serving DNS on the VIP is the
point of the application, so it is always on); move `ddgw.conf.example` into `source/`.

### Changed
- **Every gateway serves DNS on its VIP.** The setting is gone from the Settings page, the
  `--configure` wizard, the usage text, the README and the example config. The
  `dns_proxy` key in a config file is accepted and ignored; ddgw forces it on and keeps
  writing `"dns_proxy": true` so that versions before v45 (where a missing key meant off)
  read the same file the same way. It stays in the shared (cluster) group settings with a
  fixed value, so the cluster hash does not change.
- **A gateway with no DNS servers yet** is valid: it starts at once (nothing to wait for),
  its proxy answers SERVFAIL, and it shows amber ("running, but no DNS server is configured
  yet"). Before, "a gateway with no servers is a bare gateway" and a new gateway drawn on the
  Topology page was green with nothing behind it. An empty server list no longer fails
  validation. Deleting a gateway's last server now leaves an amber gateway instead of
  switching the proxy off.
- A config that had `dns_proxy: false` (a bare gateway) now serves DNS on the VIP, from the
  first start of v45.
- The Settings page's DNS hint no longer talks about "groups with Serve DNS proxy on", and
  the Gateways page shows *answering* or *not answering* per node instead of *off*.
- `ddgw --dns` without any gateway says there is no gateway rather than "DNS proxy is not
  enabled".
- `ddgw.conf.example` is in `source/` (installer, README and CLAUDE.md updated).

### Verified
- gofmt, `go vet`, `go test -race -count=1 ./...`, CGO-off vet and test, the five
  cross-compiles, `node --check` on the web files, `bash -n` and shellcheck on the scripts.
- Tests: an old file with `dns_proxy:false` loads and validates with it forced on; a
  gateway with no servers starts at once (supervisor) and is amber on the canvas; a server
  without probe queries is still rejected.
- Live daemon with an old-style config (`"dns_proxy": false`, no servers): the proxy
  listened on the VIP, `--canvas` and the Topology page showed amber with the message
  above; the Settings page has no switch (Chromium, light and dark); editing a field there
  rewrote the file with `"dns_proxy": true`.
- Installer dry-run reads the example from `source/`.

### Not verified
- A mixed v44/v45 cluster (the shared group still carries `dns_proxy: true`, so none is
  expected to differ).
- Nothing beyond the earlier lists (IPv6 BGP sessions, MD5 passwords, real systemd, a real
  FRR package install).

## [v44] - 2026-10-01 — tarball layout: docs/ and source/

Asked for: a cleaner tarball with a `docs` folder (license, readme, changelog) and a
`source` folder (the `*.go` files).

### Changed
- **New layout.** `docs/` has `README.md`, `CHANGELOG.md` and `LICENSE`. `source/` is the
  Go module: `go.mod`, every `*.go`, `webui/` and `testdata/` (the web files are embedded
  with `go:embed` and the golden vectors are read by the tests, so they have to sit with
  the Go files), and `VERSION` (also embedded, so it moved with them: `source/VERSION`).
  `contrib/`, `install.sh`, `uninstall.sh`, `ddgw.conf.example` and `CLAUDE.md` stay at
  the top. Go commands now run in `source/` (`cd source && go build -o ddgw .`).
- **install.sh** finds the tree by `source/go.mod`, `source/VERSION` and `source/main.go`,
  builds in `source/`, and installs README, CHANGELOG and LICENSE from `docs/` (the
  uninstaller removes LICENSE too).
- **The updater** (`update.go`) expects the new layout in an uploaded or pushed archive
  (`source/go.mod`, `source/main.go`, `source/VERSION`) and builds in the staged tree's
  `source/` folder. An archive in the old layout is refused with a message that says why.
- CLAUDE.md describes the layout and says where each command runs.

### Upgrade note
- **A node running v43 or older cannot take this release through the GUI/CLI updater**:
  its updater only accepts the old layout. Extract the v44 tarball on each such node and
  run `sudo ./install.sh` (it upgrades in place, keeping the config). From v44 on the
  updater works as before.

### Verified
- gofmt, `go vet`, `go test -race -count=1 ./...`, CGO-off vet and test, the five
  cross-compiles (all from `source/`), `node --check` on the web files; `bash -n` and
  `shellcheck -x -S warning` on both scripts.
- Update tests adapted (archive layout, a whole-repository tree in `realTree`, the
  old-layout refusal).
- Installer, real run with a shim `systemctl`: fresh install (docs including LICENSE in
  `/usr/local/share/ddgw`), same-version no-op, upgrade 44 to 45, downgrade refused, a
  tree that does not compile leaves the installed version untouched, `--purge` uninstall
  leaves nothing behind.
- Two real clustered daemons: join, `--update-upload` of a v45 tree in the new layout,
  `--update-push all`: both nodes built it and run v45; an old-layout (v43) archive is
  rejected with the message above.

### Not verified
- The update rollback path (`ddgw.prev`, boot guard) was not exercised again; the code
  around it is unchanged.
- Nothing beyond v42/v43's lists (IPv6 BGP sessions, MD5 passwords, real systemd, a real
  FRR package install).

## [v43] - 2026-10-01 — AS fields are plain text; the installer installs FRR; GPLv3 license

Asked for: the AS number field has up/down spin buttons it does not need; FRR as an
installer prerequisite; the GPLv3 as LICENSE.

### Added
- **`LICENSE`**: the unmodified GNU GPL version 3 text (sha256 3972dc97…, the same as
  gnu.org's gpl-3.0.txt), and a License line in the README. No per-file headers were
  added.
- **install.sh installs FRR** (`frr` package: apt, dnf/yum, pacman; on the RHEL family
  `epel-release` first when `frr` is not available) unless `vtysh` or `/usr/lib/frr/bgpd`
  is already there. Skipped with `--skip-deps` (help text updated). A failed FRR
  install is a warning, not an error: nothing else needs it, and ddgw does not touch
  FRR until a local AS is set. `DDGW_ASSUME_MISSING=frr` for tests.

### Changed
- The local AS field and the neighbors' AS fields on Configure → BGP are plain text
  fields (numeric keypad on phones, digits only in the value) instead of number
  inputs with spin buttons; in the table they keep their narrow width.

### Verified
- gofmt, `go vet`, `go test -race -count=1 ./...`, CGO-off vet and test, the five
  cross-compiles, `node --check webui/app.js`.
- Live daemon in Chromium (light and dark): no number input left on the page; a
  neighbor added with a typed AS is saved with that AS (checked in the config file).
- Installer: `bash -n`, `shellcheck -x -S warning` clean; `--dry-run` for ubuntu, debian,
  linuxmint, fedora, rocky, almalinux, centos, rhel, arch, manjaro, endeavouros with
  `DDGW_ASSUME_MISSING=frr,gcc,pam,ip,tar,go,curl DDGW_FORCE_GO_DOWNLOAD=1` (correct
  package command each; alpine still refused); with FRR present it says so, with
  `--skip-deps` it does not mention FRR; a real install with a fake `apt-get` that
  fails: warning only, install completes; same-version rerun is a no-op; uninstall
  `--purge` removes everything it made and leaves FRR files alone.

### Not verified
- A real FRR package install (the sandbox already has FRR; no network package source for
  dnf/pacman), the EPEL step on a real RHEL-family host.
- Nothing beyond v42's list (IPv6 sessions, MD5 passwords, real systemd, other FRR
  versions); this change touches only the page.

## [v42] - 2026-10-01 — BGP: runs when an AS is set, BFD always on, Monitor → BGP page, +/− neighbor table

Asked for: a BGP page under Monitor with neighbor status, BFD status and the
anycast addresses being advertised; no "Run BGP" checkbox (BGP runs when an AS is
defined); no BFD boxes (BFD is on for every neighbor); the "Add a neighbor" form
replaced by + and − buttons above the neighbor table; a shorter hint on the page.

### Added
- **Monitor → BGP** (`VIEWS.bgpstatus`, help topic `bgpstatus`): FRR/`bgpd` state and
  the last apply, one row per neighbor with BGP state, **BFD state**, uptime and
  prefixes sent, and every anycast address with *announced* / *withdrawn* and the
  reason (no DNS server answering, gateway paused or not running here, could not be
  added to `lo`). Refreshes every 5 s. `GET /api/bgp` gained `addresses` and a `bfd`
  field per peer (from `vtysh -c "show bfd peers json"`); `--bgp` prints both.
- `Supervisor.AllAnycastStates` (also feeds the CLI) and `parseBFDPeers`.

### Changed
- **BGP runs while a local AS number is set.** The "Run BGP on this node" checkbox
  and `--bgp-set on|off|keep` are gone; clearing the AS (`--asn off`, or an empty
  field) turns BGP off, keeps the neighbors and removes the BGP section from
  `frr.conf`.
- **BFD is on for every neighbor**, and ddgw now also sets `bfdd=yes/no` in
  `/etc/frr/daemons` together with `bgpd` (BGP's BFD goes through zebra to `bfdd`).
  The two BFD checkboxes and `--bgp-bfd` / `--neighbor-bfd` are gone.
- **Configure → BGP**: the Add-a-neighbor form is replaced by **+** and **−** above
  the neighbor table. **+** adds a blank row that becomes a neighbor as soon as it
  has an address and an AS; clicking a row highlights it and **−** removes it (a
  neighbor asks for confirmation, an unsaved row does not). The state column moved
  to Monitor. The hint now reads "When configured, BGP announces the gateways'
  anycast addresses and accepts no routes from its neighbors."
- A config written by v41 still loads: the old `enabled`, `bfd` and per-neighbor
  `bfd` keys are read, ignored and dropped on the next save. Note a v41 config that
  had an AS saved with BGP switched off now runs BGP.

### Verified
- gofmt clean, `go vet`, `go test -race -count=1 ./...`, CGO-off vet and test, the
  five cross-compiles (amd64, arm64, arm, 386, riscv64), `node --check` on `app.js`
  and `help.js`.
- New tests: Active() follows the AS, a v41 config loads under
  `DisallowUnknownFields` and its old keys are not written again, BFD on every
  neighbor in the rendered config, `bfdd=yes`, `parseBFDPeers`.
- Live against real FRR 8.4.4 (a peer instance with zebra+bgpd+bfdd in a network
  namespace over a veth): a v41-format config started BGP, session Established, BFD
  `up` on both sides, the peer received the `/32`; stopping the stub DNS servers
  withdrew it at the peer and the CLI showed "withdrawn (no DNS server is
  answering)"; restoring them re-announced it; `--asn off` removed the BGP section,
  set `bgpd=no`/`bfdd=no` and removed the marker; `--asn 64512` brought it back.
- In Chromium (light and dark): Monitor → BGP and its help panel; Configure → BGP has
  no checkbox and no BFD text; **+** adds a row that is not saved while half filled
  and is saved once complete; click-to-highlight; **−** with the confirm; **−** with
  nothing highlighted says to click a row; **−** drops an unsaved row; a bad address
  shows the error and keeps the row.

### Not verified
- IPv6 sessions (the sandbox has no IPv6), MD5 passwords, BFD with timers other than
  FRR's defaults, real systemd, other FRR versions.
- That a route the peer announces is not installed here was verified in v41, not
  re-run this time (the inbound route-map is unchanged).

## [v41] - 2026-10-01 — BGP: ddgw configures FRR and announces the anycast addresses

Asked for: FRR/BGP configuration in ddgw like parapet has, now that there are
anycast addresses. Parapet and ddgw run on different hosts, each owning its own
FRR config.

### Added
- **BGP page (Configure → BGP) and CLI** (`--bgp`, `--bgp-set on|off|keep
  [--asn N] [--router-id A.B.C.D] [--bgp-bfd on|off]`, `--bgp-neighbor-add ADDR
  --remote-as N [--description T] [--password P] [--neighbor-bfd on|off]`,
  `--bgp-neighbor-del ADDR`; API `GET|PUT /api/bgp`, relayable through the node
  picker). Local AS, router id, neighbors (IPv4 and IPv6) with description, MD5
  password and BFD, plus BFD for all.
- **Per node.** The settings live in an optional `bgp` block, never replicated;
  only the anycast addresses are shared. The key is absent for a config that
  never used BGP, so older versions (which reject unknown keys) still read it.
- **ddgw owns that node's `/etc/frr/frr.conf`** while BGP is on: it renders the
  whole file, sets `bgpd=yes` in `/etc/frr/daemons`, and reloads FRR (restart when
  a daemon is switched; restart as a fallback when the reload fails). A node that
  never enabled BGP never has its FRR files touched; turning it off removes the
  BGP section and leaves FRR running.
- **Announce-only posture.** Each anycast address is a `network` statement; an
  outbound route-map permits only those prefixes, an inbound route-map denies
  everything, so a peer cannot change the host's routing table. IPv4 addresses go
  to IPv4 neighbors and IPv6 to IPv6 neighbors; the page says when a family has
  no neighbor. Every value that reaches `frr.conf` is validated (no injected lines).
- **Live neighbor state** (Established / Active / Idle …, uptime, prefixes sent)
  from `vtysh -c "show bgp summary json"`, refreshed on the page every few seconds.

### Fixed
- **The Settings page would have dropped the anycast addresses** (and now the
  BGP block) when it saved: it rebuilt each gateway from its own fields only.
  Both are carried through untouched. Found while adding BGP; affects v37–v40.
- **The v4 / v6 boxes beside the circle are gone** (the circle shows both addresses
  already; per-family detail is in its tooltip).
- **Anycast pills are wide enough for a full-length IPv6 address** (sized to the
  longest address, no truncation).

### Verified
- Real FRR 8.4 in the sandbox with a real peer (`bgpd -n` in a namespace over a
  veth): session Established; the peer received `203.0.113.53/32` (AS path 64512)
  and, after IPv6 + IPv4 addresses were added, both announced prefixes counted;
  a route the peer announced was **not** installed on the ddgw side; stopping the
  stub DNS servers withdrew the route at the peer after 10 s, returning them
  restored it after 3 s; `kill -9` on ddgw withdrew it after 10 s; BGP off removed
  the section and set `bgpd=no`; a warm restart of ddgw with an unchanged config
  only checked that FRR was running.
- GUI in Chromium (light and dark): add/edit/remove a neighbor, a bad address shows
  the error, status and neighbor state update; Settings save keeps `extra_vips` and
  `bgp`; pills fit a 39-character IPv6 address.
- Two clustered daemons: the anycast address replicated, BGP settings stayed per
  node (an edit on one did not reach the other).
- Unit tests (`bgp_test.go`): validation incl. injection attempts, golden
  rendering, `/etc/frr/daemons` editing, apply lifecycle (never enabled → untouched,
  enable, unchanged, reload, fallback restart, restart failure, FRR missing, turn
  off), summary parsing, key omitted when unused, not in the shared config.
- gofmt, vet, `go test -race ./...`, CGO-off vet/test, five cross-compiles.

### Not verified
- IPv6 BGP sessions and IPv6 anycast end to end (no IPv6 in the sandbox); BFD and
  MD5 passwords were rendered and unit-tested but no session was run with them.
- A real systemd: a shim mapped `systemctl restart|reload|start frr` to
  `frrinit.sh`. Restarting FRR on first enable took about 8 s here.
- Other FRR versions (only 8.4 was run).

## [v40] - 2026-10-01 — anycast pills follow the gateway's colour

### Fixed
- A paused gateway showed its anycast pills amber ("withdrawn"). A pill is now
  green while announced and otherwise takes the gateway's own colour only when
  that is amber, red or paused (dashed grey, "anycast · paused"); in every other
  case (for example an address this node cannot serve while the gateway is
  green, or a gateway with no DNS server yet) it is plain grey, with the reason
  in its tooltip. Pills are never more alarming than the gateway.

### Verified
- Real daemon, Chromium (dark): running → green pill, address the node cannot
  listen on → grey; paused through the GUI → both pills dashed grey like the
  circle. gofmt, vet, `go test -race ./...`, CGO-off vet/test, five cross-compiles.

### Not verified
- The red case (gateway down) was reasoned from the code, not drawn.

## [v39] - 2026-10-01 — login form no longer blinks after an update; tidier one-field dialogs

### Fixed
- **The login form flickered after an update (or any session ending by itself).**
  When a session ended — the daemon restarted for an update, or the idle timeout
  — only the page poller was stopped; the sidebar and node-list pollers kept
  running, got 401 every few seconds and redrew the login form each time, wiping
  what was typed. All the shell's timers now stop together, the login form is not
  redrawn while it is already showing, and signing in again cannot stack a
  second set of timers.
- **One-field dialogs** (Add / Edit anycast address, Edit server) used half the
  dialog width; the field now spans it. The anycast hint is one short line
  instead of a paragraph.

### Verified
- Real daemon, old build vs new, Chromium on `#updates`: sign in, restart the
  daemon, wait on the login form 20 s while typing — old: form redrawn 9 times
  and the typed text lost; new: 0 times, text kept, hash unchanged.
- Dialog screenshots (dark) for the anycast dialog and Edit gateway.
- gofmt, vet, `go test -race ./...`, CGO-off vet/test, five cross-compiles.

### Not verified
- The full update flow end to end in the browser (only the restart that ends
  the session was reproduced, which is what triggers the fault).

## [v38] - 2026-10-01 — anycast addresses are items on the drawing, not a comma-separated field

Reported: typing a comma-separated list into the gateway form was a poor way to
manage them.

### Changed
- Each anycast address is now its own **pill beside the gateway circle**
  (green: announced from this node; amber: withdrawn, with the reason in its
  tooltip; grey: needs a DNS server first). Right-click the circle → *Add
  anycast address…*; right-click a pill → *Edit address…* / *Delete address*; the
  Delete key works too. One address per dialog, checked as you save (not a
  subnet, not loopback/link-local/multicast, not already used by a gateway).
- The *Anycast addresses* field is gone from Edit gateway; the status line no
  longer repeats the list (the pills show it).
- CLI parity: `--canvas-add anycast --group N --address ADDR` and
  `--canvas-del anycast …` (one address); `--canvas-set gateway --anycast A,B`
  still replaces the whole list.

### Verified
- `TestAnycastCanvasAddDel`; Chromium (light and dark): add via the menu, the
  subnet / loopback / shared-address refusals keep the dialog open, an IPv4 pill
  green and a pill the node cannot serve (no IPv6 here) amber, edit an address,
  delete it with the Delete key — the address left `lo` as well.
- gofmt, vet, `go test -race ./...`, CGO-off vet/test, five cross-compiles.

### Not verified
- Many addresses (more than about six) were not looked at; the layout pushes the
  servers down to make room.
- Everything listed as not verified under v37 still applies (FRR itself, IPv6).

## [v37] - 2026-10-01 — anycast addresses: extra addresses of any subnet, held on every node

Asked for: attach an address on a different subnet to the gateway, not checked
against an interface address, to redistribute into BGP (FRR).

### Added
- **Anycast addresses** (`extra_vips` per gateway; GUI: Edit gateway → *Anycast
  addresses*; CLI: `--canvas-set|--canvas-add gateway --anycast ADDR[,ADDR]`,
  `-` clears). IPv4 and IPv6, any subnet, no interface or subnet check, no
  election. Every node running the gateway holds them on `lo` and answers DNS on
  them (same pool, domains, failover, `listen_port`). Shared across the cluster.
- **Withdrawal.** An address is on `lo` only while the gateway runs here, is not
  paused, its listener is up and at least one DNS server is healthy; otherwise
  it is removed so the routing daemon withdraws the route.
- **Crash safety.** The address is added with a 10 s lifetime (`valid_lft`) that
  ddgw renews every 3 s: if ddgw is killed or hangs, the kernel removes it by
  itself within 10 s instead of leaving a route nothing answers.
- The drawing, its tooltip and `--canvas` show whether each address is announced
  from this node; README section "Anycast addresses (BGP)" with an FRR example;
  help topic; CLAUDE.md checklist.
- Refused: a subnet (anything but /32 or /128), loopback, link-local, multicast,
  unspecified; the same address on two gateways or equal to a shared address.

### Changed
- Changing the anycast list is applied in place; it does not restart the gateway
  or re-run the election.

### Verified
- Unit tests (`anycast_test.go`): normalisation and refusals, config rules,
  cluster sharing (hash unchanged without addresses), `--anycast` edit, health
  gating with renewal / withdraw / recovery / `lo` refusal / stop, supervisor
  start-stop-pause, reload swapping addresses without restarting the gateway.
- Live, real daemon: address on `lo` with `valid_lft 10sec`, answered DNS from
  it, withdrawn when the stub servers stopped and back when they returned,
  `kill -9` removed it after 10 s, SIGTERM removed it at once; `--canvas` and the
  GUI (light and dark) show it; GUI save with a bad and a good value.
- Two real clustered daemons: set on the primary reached the replica, an edit on
  the replica reached the primary.
- gofmt, vet, `go test -race ./...`, CGO-off vet/test, five cross-compiles.

### Not verified
- IPv6 anycast end to end (the sandbox has no IPv6; the failure path was seen).
- FRR itself: that `network <addr>/32` announces and withdraws the route as the
  address comes and goes. It follows from zebra watching `lo`, but was not run.
- Withdrawal is as fast as the DNS health check: about 11 s after the servers
  stopped here (probe interval plus the 3 s step); BGP timers add to that.

## [v36] - 2026-10-01 — the navigation menu stays put when you scroll or overshoot

Reported: the menu on the left wasn't pinned and moved when scrolling past the
end of a page (rubber-band overscroll).

### Fixed
- **The page itself no longer scrolls on wide screens.** The menu is a fixed
  column and only the content area scrolls, with overscroll contained to it, so
  overshooting cannot drag the menu (or the help button) along. On narrow
  screens (up to 760 px) the menu still stacks above the content and the page
  scrolls as before.

### Verified
- Headless Chromium, dark and light, 1000 px and 600 px wide: wheel-scrolling
  3000 px moved only the content (menu top stayed at 0, window scroll 0); at
  600 px the stacked layout scrolls as a page, as intended.
- Topology right-click menu and the help panel still open; gofmt, vet,
  `go test -race ./...`, CGO-off vet/test, five cross-compiles.

### Not verified
- Real touch/trackpad rubber-banding on macOS/iOS (emulated by wheel only);
  `overscroll-behavior` needs Safari 16+.

## [v35] - 2026-10-01 — restarts no longer black-hole clients (measured: 3 s → 0 s)

Reported: about 10 s of outage during an update. The journal of the controller
showed what happened, and a real two-node test (below) reproduced and fixed it.

### Fixed
- **The network kept the old MAC on the old port.** When a node stops, the node
  covering its virtual MAC (v27) now exists, but a switch or Linux bridge sends
  frames for a MAC to the port it last saw it on until something transmits from
  that MAC elsewhere. Nothing did, so clients that had cached the restarting
  node's MAC were black-holed until it came back (~3 s). Now whoever takes over a
  MAC, and a returning forwarder (once its DNS proxy and VIP are up), announce it
  with a neutral frame — an ARP probe from 0.0.0.0 / an IPv6 DAD solicitation from
  ::, sourced from the virtual MAC — that makes the switch relearn the port and
  changes no host's neighbour cache.
- **A restarted node took the controller role straight back.** A node that took over
  as controller keeps its old slot (2, 3…), and joiners recognised a controller only
  by slot 1, so the restarting node (greater IP, same priority) saw no controller,
  won the election and made the sitting controller yield: two role changes and a
  moment with none per restart. Hellos from a controller now carry a flag (bit 3,
  `flagController`); a controller yields only to another controller or to a peer
  that has **preemption** on (which is what the setting is documented to do — before,
  every higher-ranked hello made the controller yield and the flag did nothing).
- **A leaving node reacted to what it heard** (yielding the role, rebuilding its MAC
  just before exiting), putting the MAC back on its port as it disappeared. It now
  ignores packets once it has said it is leaving.
- **A stopping forwarder said nothing**, so the controller waited a whole hold time
  (1 s) to cover its slot. It now sends two hellos flagged "leaving" (bit 2) and
  the controller covers the slot at once. The controller hands a MAC back 600 ms
  after the returning node first reported it, not on its first hello.
- **The next node restarted too soon.** ddgw2 began its own restart 8 s after ddgw1
  came back. A gateway now counts as cover for a neighbour's restart only after it
  has been served continuously for 15 s (`fresh` in the cluster status; absent on
  older nodes = settled); the page says "waiting a few seconds for clients to move
  back". A rolling update takes ~15 s longer per node.

### Changed
- Controller changes are no longer forced by a restart: after a node restarts, the
  roles stay as they fell (set preemption and a higher priority on the node you want
  as controller). Documented in the README ("Who is controller").
- Wire: two flag bits in the hello header (0x04 leaving, 0x08 controller). Python
  `dgw` reads only bit 0, golden vectors are unchanged; Python joiners still
  recognise a controller by slot 1 only.

### Verified
- Real two-node test: a bridge, two network namespaces each running a daemon (the
  second node's address sorts higher as text, so it wins ties), a third namespace
  as a client with the VIP's neighbour entry pinned to the restarting node's MAC,
  querying the VIP every 100 ms through a SIGTERM + restart:
  v34: controller restart 3.0 s of failures, forwarder restart 3.3 s.
  v35: controller restart 0 s, forwarder restart 0 s (0.2 s before the announce was
  moved after the DNS proxy came up), and roles stay put.
- New unit tests: yield rules, a joiner recognising a taken-over controller, leave
  notice and immediate cover (+ announce), a leaving node ignoring packets, announce
  frames (ARP probe, DAD NS checksum), the fresh/settled gate. gofmt, go vet,
  `go test -race ./...`, CGO-off vet/test, five cross-compiles.

### Not verified
- Physical switches (a Linux bridge was the L2 device), IPv6 on the wire (no IPv6 in
  the sandbox; the frames are unit-tested), an update across real hosts end to end,
  and the 15 s gate in a live rolling update.
- The client in the test is pinned to one MAC; clients that re-ARP for the VIP during
  the restart get the AGC's answer and are served either way.

## [v34] - 2026-10-01 — Operate ▸ Power

### Added
- **Power page under Operate** (ported from parapet's power menu): restart or shut
  down the whole host now, in N minutes (1–10080) or at HH:MM; a pending scheduled
  action is shown with **Cancel it**. Scheduling is left to `shutdown(8)`, so it
  survives a restart of ddgw; an immediate action runs `systemctl reboot|poweroff`
  (falling back to `shutdown`) 0.8 s after the reply.
- CLI: `--power restart|shutdown [--in MIN | --at HH:MM]`, `--power cancel`,
  `--power status` (confirmation unless `--yes`).
- **Cover check** (not in parapet): an action started now is refused when this node
  is the only member serving one of its gateways — the check updates use; the GUI
  asks whether to go ahead anyway, the CLI needs `--yes`. Scheduled actions are not
  checked (the page says so).
- Works on any member through the Node menu (`/api/power` is relayable).
- The "would have no other member serving it" message now also says "this is the
  only member", the common single-node case.
- Help panel topic, README section and CLI table row.

### Verified
- New tests: HH:MM validation, every action/schedule form maps to the right command,
  invalid input refused, cancel, the cover refusal and its bypasses, reading systemd's
  scheduled-shutdown file, `/api/power` in the relay allow-list. gofmt, go vet,
  `go test -race ./...`, CGO-off vet/test, five cross-compiles.
- Live: real daemon + PAM user + stub DNS, **fake `shutdown`/`systemctl` first in
  PATH** (nothing was rebooted): CLI schedule/status/cancel, refusal without `--yes`,
  forced with `--yes`, bad input; Chromium dark and light: schedule a shutdown, the
  pending notice, cancel it, restart now -> refusal -> "go ahead anyway" -> executed;
  Help is gone from Operate; help panel shows the Power topic.

### Not verified
- A real reboot or shutdown (deliberately not done in the sandbox), systems without
  `shutdown`/`systemctl`, and the page acting on another member through the picker
  (the allow-list is tested, a relayed power call was not run).

## [v33] - 2026-10-01 — the subnet guard covers globally addressed IPv6

### Changed
- v32 left IPv6 gateways unchecked. With globally unique addressing a node on another
  network would still have started the gateway. Now each family the gateway has must
  match: IPv4 always; IPv6 when the interface has a global (non link-local, non
  loopback) address — a node with only link-local IPv6 has nothing to compare and is
  let through, as the election itself runs over link-local. A node whose prefix
  differs from the VIP's prefix on the same link is held back (documented).

### Verified
- Unit tests: link-local only passes, global in the VIP prefix passes, global or ULA
  in another prefix is refused, dual stack needs both. gofmt, go vet,
  `go test -race ./...`, CGO-off vet/test, five cross-compiles.

### Not verified
- Live IPv6 (the sandbox has no IPv6); the IPv4 path was run live in v32.

## [v32] - 2026-10-01 — a node off a gateway's subnet no longer runs it

### Fixed
- A cluster member on a different subnet used to start every gateway anyway: it
  heard no one, elected itself controller of a one-node group, claimed the VIP on
  the wrong network and reported "serving" — which the update safety check counted
  as cover for the nodes that really serve it. Now a node starts a gateway only if
  its interface has an IPv4 address inside the gateway's VIP subnet. Otherwise the
  gateway shows grey ("not running here — no address in the gateway's subnet …"),
  the node reports it as not serving, and it starts by itself (then through the DNS
  warm-up) within about 5 s of the node getting an address there. The node stays a
  full cluster member. Paused gateways and a missing interface are handled as before.
- IPv6-only gateways are not subnet-checked (their elections use link-local
  addresses); a gateway already running is not stopped if its address later changes.
- The status line above the drawing no longer runs under the ? button.

### Verified
- New tests: subnet check (same, other, missing interface, IPv6-only, loopback),
  held back / grey canvas / not serving / update gate not counting it / starts once
  addressed, cancel on removal. gofmt, go vet, `go test -race ./...`, CGO-off
  vet/test, five cross-compiles.
- Live, real daemon: VIP on a subnet eth0 is not in -> no macvlan, `--canvas` and the
  GUI show it grey with the reason; `ip addr add` an address there -> it started
  within 9 s, answering; Chromium (dark) screenshot read.

### Not verified
- A real multi-subnet cluster (one host here).

## [v31] - 2026-10-01 — name your gateways

### Added
- **Gateway names.** A gateway can carry an optional name (up to 40 characters) that
  is shown instead of its address: in the Topology sidebar, the drawing (under the
  address), the status line, confirmations, the Gateways page card heading,
  `--canvas` and `--show-gateways`. Set it in the gateway form (right-click ->
  Edit gateway, or New gateway), on the Settings page, or with
  `--canvas-add/--canvas-set gateway ... --label NAME` (`--label -` clears it).
- The name is a **shared** setting (JSON key `name`): it reaches every cluster node.
  It is left out of the shared document when empty, so a cluster without names
  keeps the same hash and members on mixed versions during a rolling update agree.
- README, the `--help` text and the Topology help panel describe it.

### Verified
- New tests: name validation, sharing and hash stability, canvas set/clear/add,
  `nameGateways`. gofmt, go vet, `go test -race ./...`, CGO-off vet/test, five
  cross-compiles.
- Live, one real daemon with PAM user and stub DNS servers, Chromium dark and light:
  rename through the gateway form (sidebar, drawing, status line, Settings field,
  Gateways card heading all show it; the file holds it), accented characters,
  `--canvas`, `--show-gateways`, `--label -` clearing it.

### Not verified
- Name propagation between two live cluster nodes (covered by the `mergeShared`
  unit test only; one host cannot run two real gateway nodes).

## [v30] - 2026-10-01 — the Cluster help explains the epoch

### Changed
- The Cluster help panel now explains the epoch (when it changes, why it exists,
  what a conflict means) and each column of the Members table. README has an
  "Epoch" section. No code change.

### Verified
- gofmt, go vet, `go test -race ./...` (including `TestEveryPageHasHelp`),
  `node --check` on both scripts, CGO-off vet/test, five cross-compiles.

### Not verified
- Not looked at in Chromium (text only).

## [v29] - 2026-10-01 — cluster scale measured; stale "Help" text removed

### Changed
- `ddgw --help` no longer lists a Help tab (it was removed in v27); it points at the ?.
- README: "How many members?" with the measurements below. No code change.

### Verified
- Scale test, real daemons (one scratch dir each, `"groups": []`, PAM user): 40
  members joined (~40 ms each, all 40 see 40 reachable); a shared edit through the
  HTTPS API on a replica and on the primary reached all 40 files in 6-12 s (one
  8 s sync interval); `/api/cluster` ~4 ms; with 5 members SIGSTOPped, they showed
  as unreachable, propagation to the other 35 took 12 s, pages stayed fast, and
  the 5 recovered after SIGCONT.
- gofmt, go vet, `go test -race ./...`, CGO-off vet/test, five cross-compiles.

### Not verified
- Real network latency, other hosts, more than 40 members, a GUI-driven rolling
  update across many nodes (would take ~20 s per node by design).
- Observed, unchanged: a hand edit of a replica's config file is reverted by the
  primary's next sync.

## [v28] - 2026-10-01 — no more redundant "v4"/"v6" labels

### Changed
- The address already shows its family, so the GUI no longer prints it: Members
  card headers read "Group 1" (the VIP is beside it), the Serving nodes tooltip
  drops the trailing "v4", the Neighbors table loses its AF column, and the
  Dashboard's Listening line reads "group 1 192.168.122.111:53" (same text from
  `ddgw --status`). Canvas notes that say "IPv4+IPv6"/"IPv6" for dual-stack and
  v6-only gateways are unchanged.

### Verified
- gofmt, go vet, `go test -race ./...`, `node --check webui/app.js`, CGO-off vet and test, the five cross-compiles.

### Not verified
- Not looked at in Chromium this time (string and column removal only).

## [v27] - 2026-10-01 — a help panel on every page; no big outage while a node updates

### Changed
- **Operate > Help is gone.** Every page has a **?** at the top right that opens a
  slide-out panel with help for that page, including its command-line
  equivalents (`webui/help.js`; Esc or a second click closes it). `GET /api/help`
  was removed; `TestEveryPageHasHelp` keeps a topic for each page.
- **Updates no longer cause a long outage.** Three parts, like the warm-up fix:
  1. A stopping controller (AGC) sends a resign packet and goes quiet; the best
     remaining node becomes controller immediately instead of after the hold time.
  2. The new controller takes over the old controller's virtual MAC (slot 1),
     which clients have cached for the VIP — also when the loss is detected by
     timeout.
  3. After building, the restart waits until the other members are serving
     (`waitUntilSafe`); `--update-apply --yes` / the GUI confirm forces it.
  Start-up was already gated by v25 (a node joins only once its pool answers).

### Verified
- gofmt, go vet, `go test -race ./...`, CGO-off vet and test, cross-compiles for
  amd64, arm64, arm, 386, riscv64.
- New unit tests: controller resign hand-over and MAC takeover, loss-by-timeout
  takeover, resign sent only by a stopping controller, `waitUntilSafe`.
- Help panel in Chromium, light and dark, against a live daemon.

### Not verified
- Real two-host controller hand-over and ARP/NS behaviour on a LAN (sandbox runs
  one gateway host).
- A relayed "Update now"/queue on another node; a relayed action whose target
  restarts before replying.
- With a permanently dead peer the restart waits indefinitely (use force).

## [v26] - 2026-10-01 — the Cluster and Updates pages follow the picked node

### Changed
- **The node picker now covers every page.** v22 kept Cluster and Updates on the
  node you logged in to ("This page always acts on the node you are logged in to");
  that notice is gone. With another node picked you can leave or promote it, make
  a join code on it, sync it, remove peers as seen from it, upload a release to it,
  update it now, queue nodes, cancel and switch its auto-update. Only sign-in,
  sign-out and the session stay on the login node. The picker's own list of nodes is
  always asked of the login node, so it cannot disappear when the picked node leaves.
- Uploads through another node are limited to 5 MB (the peer channel's body limit
  with its encoding); a release archive is a few hundred KB.

### Fixed
- Uploading a release archive to another node was refused ("content type must be
  application/json"): the relay route insisted on JSON for a raw upload.

### Verified
- gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and test, five
  cross-compiles; tests: the allow-list now admits cluster/update and still refuses
  login/logout/session, traversal and encoded slashes; a raw upload is not refused
  for its content type.
- Live, two clustered daemons, Chromium: picked the second node — Cluster page shows
  it as "this node" (★), uploading a release staged it on that node only, "Leave
  cluster" on it worked and the picker fell back to the login node.
- Not verified: "Update this node now" / "Queue selected nodes" relayed to another
  node (needs a Go build and a restart of the target); a relayed action whose target
  restarts before answering (the page may show a connection error although it worked).

## [v25] - 2026-10-01 — a resumed gateway waits until its DNS servers answer

### Fixed
- **Resuming a gateway (or restarting the daemon, or adding a gateway) caused an
  outage on that node's clients until every DNS server read green.** The node
  started its engines at once: it joined the election and the controller steered
  clients to it while its upstream servers had not been probed yet, so it could
  only answer SERVFAIL until the next probe round — even though the other nodes
  were serving fine. A gateway that serves DNS now starts in the background only
  once its DNS servers answer: all of them, or after 10 s at least one (an
  upstream that is down for maintenance cannot hold the node back forever), and
  after 60 s regardless, with a warning. The other nodes keep serving throughout.
  Pausing, removing or restarting the gateway while it waits cancels the wait.
  The circle shows "starting — waiting for the DNS servers to answer before this
  node serves" instead of an alarm. Gateways without a DNS proxy start at once.

### Verified
- gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and test, five
  cross-compiles; tests: no engine starts while the only server SERVFAILs, it
  starts when the server recovers, the canvas says "starting", the one-server floor,
  cancel by pause/stop, and a gateway with no DNS proxy starting immediately.
- Live (real daemon, two stub upstreams): paused, stopped both stubs, resumed —
  no macvlan and the circle read "starting" for the 4 s observed, then the stubs
  returned and the gateway came up within 5 s, answering; daemon start logs the same
  wait (1 s).
- Not verified: two real nodes handing over (the sandbox has one host); the
  election order after the wait on a cluster with preempt on.

## [v24] - 2026-10-01 — settings save themselves; clustering is not an option

### Changed
- **The Settings page has no "Save & apply" button.** Every committed edit — leaving
  a field, choosing an option, ticking a box, adding or removing a gateway group —
  is saved and applied at once, one save at a time. A value the daemon refuses
  ("Not saved: …") stays on screen until the next edit succeeds. The one guarded
  edit is the GUI listen address, which asks first because it restarts the GUI and
  ends every session; cancelling puts the old value back.
- **"Clustering enabled" and "Share the installed GUI certificate cluster-wide" are
  gone.** Both are always on. Config files that still contain `cluster.enabled` or
  `cluster.share_cert` load as before (the values are ignored) and the keys are no
  longer written back. Removed from the example config, `--help` and the README.

### Verified
- gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and test, five
  cross-compiles; new test: the old keys load and are forced on, are not written
  back, and unknown keys are still refused (the `share_cert=false` replication test
  was removed with the option).
- Live, Chromium: no Save/Apply button; log level, a number field, a refused value
  (message, file unchanged, then recovers and the message clears), add group
  without an address (refused with the reason), with an address (saved), remove
  (saved); listen-address change cancelled leaves the file and the field unchanged.
- Not verified: the listen-address change being accepted (it restarts the test GUI);
  two people editing Settings at once; a replica forwarding an autosave to an
  unreachable primary (the error shows, the edit is not kept).

## [v23] - 2026-10-01 — a node serving a gateway could no longer reach its peers

### Fixed
- **Two cluster nodes stopped reaching each other ("context deadline exceeded",
  ping lost) once one of them was serving gateways.** The gateway address was put
  on the macvlan with an ordinary `/24`, which makes the kernel add a second
  connected route for the LAN subnet at metric 0 — ahead of the parent interface's
  DHCP/NetworkManager route (metric 100). Everything the serving node sent into
  the subnet (cluster traffic, ping, DNS to servers) then left from the gateway
  address, and the other node, which holds that address on its loopback, took the
  replies for itself. The address is now added with `noprefixroute` (any existing
  one is removed and re-added so it applies at once) and the subnet route is added
  back at metric 1024, so the parent's route wins whenever it has one and the
  macvlan's route is only used when the parent has no address in the subnet.
  IPv6 is handled the same way.

### Verified
- gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and test, five
  cross-compiles. New test reproduces NetworkManager-style routing (address
  `noprefixroute`, route metric 100): fails on the old code (`route get` picks the
  macvlan and the VIP as source), passes now; also checks a parent with no address
  in the subnet still has a route.
- Live: the daemon serving a gateway (192.0.2.111/24 on a parent with a metric-100
  route): the VIP sits on the macvlan `noprefixroute`, the macvlan's route is at
  metric 1024, and `ip route get` into the subnet picks the parent and its own
  address. Reproduced on the old behaviour first. On the affected hosts `ip route
  get` chose `dev ddgw1.1 src <VIP>`, and deleting those macvlan routes restored
  the cluster.
- Not verified: two real hosts on one LAN (the sandbox has one host); the node
  holding the gateway needs a restart (or the gateway re-created) to pick the fix
  up. Replies from the gateway address to same-subnet clients now leave through
  the parent interface (its MAC) instead of the vMAC.

## [v22] - 2026-10-01 — choose which node to configure (top right)

### Added
- **Node picker in the top right of the GUI.** In a cluster, a drop-down lists
  every member (host name, `· primary`, `· unreachable`; the address too when two
  nodes share a name). Pick one and Topology, Monitor, Settings, History and
  Certificate all show and change *that* node; the page gets an amber stripe while
  you are looking at another node. Not clustered: no picker.
- **How it works.** You stay logged in to one node (PAM and the `ddgw` group are
  checked there only). That node relays the request over the existing pinned-TLS,
  HMAC-signed cluster channel (`POST /cluster/proxy`) and the target runs it through
  its own web handlers. History and the log record it as `alice via 10.0.0.7:53854`.
- **What is never relayed:** the Cluster and Updates pages, login/logout/session —
  they always act on the node you logged in to (the page says so while another node
  is picked). Only an allow-list of `/api/` areas can be relayed, with traversal and
  encoded-slash forms refused on both sides. If the picked node leaves the cluster
  the picker falls back to this node.
- Peers report their host name (`hostname` in the cluster status).

### Verified
- gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and test, five
  cross-compiles; tests for the allow-list (traversal, encodings, cluster/update
  routes, other methods) and for a relayed request being attributed "user via node",
  needing no cookie, and the bypass not being reachable from outside.
- Live, two real daemons clustered, Chromium dark and light: picker lists both,
  picking the other node shows its Topology, adding a gateway there is recorded on
  that node as `tester1 via <login node>` and replicates; Cluster/Updates show the
  "acts on the node you are logged in to" notice; picking "this node" restores the view.
- Not verified: relaying to a node that is mid-update or restarting; more than two
  nodes; a node served only by IPv6; the "node vanished" fall-back in a browser.

## [v21] - 2026-10-01 — nothing in /etc/liras, ever

### Fixed
- **A service unit that still said `--config /etc/liras/ddgw.conf` kept using
  `/etc/liras`.** An in-place update (the GUI/CLI updater) replaces the binary but
  not the unit, so a node updated from before v15 kept reading and writing its
  config, certificate and key in `/etc/liras` — the migration only ran when the
  default path was passed. The daemon now treats that path as
  `/var/lib/ddgw/ddgw.conf` on every start, whatever the unit says (it prints a
  one-line notice), so `/etc/liras` is never read or written again.
- **The one-time migration is now a real move.** It used to leave
  `*.migrated` copies behind. Each old file (config, certificate, key) is now
  removed once its copy in `/var/lib/ddgw` has been written and read back
  identically, and `/etc/liras` is removed when empty. The installer does the
  same (`cmp`, then remove, then `rmdir`). Nothing is ever deleted unless the
  copy is verified, and an existing config in `/var/lib/ddgw` is never
  overwritten.

### Verified
- gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and test, five
  cross-compiles, `shellcheck`; tests for the path redirect (`/etc/liras//ddgw.conf`
  and `..` forms too) and for the move leaving nothing behind.
- Live: the daemon started with `--config /etc/liras/ddgw.conf` and legacy files
  present: config, certificate and key ended up in `/var/lib/ddgw`, `/etc/liras`
  was gone; started again with no legacy directory: `/etc/liras` was not created.
  Installer (shim `systemctl`) with legacy files: moved, `/etc/liras` removed, unit
  points at `/var/lib/ddgw/ddgw.conf`; uninstall `--purge` left nothing.
- Not verified: a real systemd unit being edited by the in-GUI updater.

## [v20] - 2026-10-01 — updates keep the gateways served; Settings; fewer banners

### Changed
- **A rolling update never takes the last serving member down.** Each node now
  reports which gateways it is serving (it holds the address or forwards for it in
  every address family, and DNS is listening). A node that wants to update waits
  until, for every gateway it serves, at least one *other reachable* member serves
  it too; a member still recovering from its own update therefore holds the next
  one back. The reason is shown on the Updates page. Paused gateways are not
  counted. Not clustered, or serving nothing, never waits. Older nodes that cannot
  report are assumed fine, so a mixed-version cluster can still roll. A manual
  `--update-apply` (or the GUI's Build button) on a node whose gateway would be
  left unserved is refused with the reason; `--yes` (GUI: a confirmation) overrides.
- **Configure ▸ Configuration is now Configure ▸ Settings** (sidebar, hints, README,
  `--help`). The Settings/`#config` link is unchanged.
- **The version line under the title no longer repeats the product name**: it reads
  "v20" and appends the host name only when that is not "ddgw".

### Removed
- **Success banners after an action** ("Saved and applied.", "Version restored",
  "Snapshot saved", "Certificate installed", "Joined the cluster", "Queued …" and
  the take-over result on Gateways). The page simply updates. Errors, warnings and
  standing hints (an expired certificate, a replica notice, a failed sync) are
  still shown.

### Verified
- gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and test, five
  cross-compiles; new `TestSafeToTakeDown` (other member serves, split gateways,
  recovering peer, partly unserved, no reachable peer, older node, nothing to lose)
  and `TestGatewayStates` (paused left out, dual stack needs both families, DNS
  must be up).
- Chromium (dark), live daemon: header, Configure ▸ Settings, saving Settings
  shows no banner, a failing certificate action still shows its error.
- Two real daemons (join, then upload v21 and `--update-push all`): both built,
  restarted and confirmed v21, one after the other — the new status fields do not
  disturb an ordinary rolling update.
- Not verified: the gate across two real nodes that both serve a gateway — one
  host cannot run two nodes on the same vMAC names, so only the decision logic is
  unit-tested.

## [v19] - 2026-10-01 — a missing config is created, with no gateways

### Changed
- **If `ddgw.conf` does not exist the daemon now creates it** (defaults, no
  gateways, directory mode 0700) and starts, instead of starting without a file
  and waiting for one. Before, the file only appeared on the first change, and
  anything that read the config before that (the Topology page, the CLI, a
  config export) was shown a made-up default gateway `10.0.0.1/24` — drawing a
  gateway on a fresh or cloned VM could therefore save that phantom gateway too.
  A missing file now means no gateways everywhere (`loadConfig`). Resetting or
  cloning a node is: stop, `rm -rf /var/lib/ddgw/*`, start.

### Verified
- gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and test, five
  cross-compiles; new `TestMissingConfigMeansNoGateways`; two web tests that
  relied on the invented default gateway now set one up explicitly.
- Live: started with a config path whose directory did not exist: the file and
  directory were created (0600 / 0700), `groups` is `[]`, `--canvas` shows an
  empty topology, no macvlan was created.
- Not verified: the installer's fresh-install path after this change (the unit
  and install logic are unchanged).

## [v18] - 2026-10-01 — gateways under Topology in the sidebar, everything saves at once

### Changed
- **Gateways are items under Topology in the sidebar**, not tabs on the page:
  Topology ▸ 10.77.0.1, 10.77.1.1, ＋ New gateway…. Each item is the bare
  address (no /prefix) with its status dot; the sidebar refreshes every few
  seconds. The page heading says which gateway you are on.
- **No Apply button any more: every change is saved the moment you make it**
  (the "Unsaved changes" box and Discard are gone). To keep every saved state
  valid: a new gateway starts as a bare gateway (no DNS proxy) and gets the proxy
  with its first server; *Add DNS server* asks for the server's first domain; the
  last domain of a server cannot be deleted (a dialog says to delete the server or
  add another domain); deleting a gateway's last server leaves a bare gateway.
  A change the daemon refuses is undone on screen and the reason is shown.
  Saves are queued so quick successive edits cannot overwrite each other.
- Gateway names in dialogs and headings drop the /prefix too.

### Verified
- gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and test, five
  cross-compiles. Live daemon + stub DNS servers + Chromium (dark): empty
  sidebar, ＋ New gateway… creates and saves a bare gateway, adding a server with
  its domain saves (checked with `ddgw --canvas`), a second gateway, switching
  gateways from the sidebar, last-domain delete blocked, pause/resume saved at
  once, deleting the only server leaves a bare gateway, deleting a gateway
  removes it from the sidebar; no script errors.
- Not verified: the "daemon refused the change" revert path; light theme for
  the new sidebar items; several nodes editing at the same moment.

## [v17] - 2026-10-01 — the Canvas page is now called Topology

### Changed
- **"Canvas" is now "Topology"** everywhere a person sees it: the sidebar item,
  the page link (`#topology`; the old `#canvas` still opens it so bookmarks keep
  working), hints, the history note, `--help` and the README. The CLI flags keep
  their names (`--canvas`, `--canvas-add`, …) so existing scripts do not break.

### Removed
- **The green "Applied. In a cluster, …" banner** after Apply. The unsaved-changes
  box disappearing is the confirmation; errors are still shown.

### Verified
- gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and test, five
  cross-compiles. Chromium (dark), live daemon: opening the old `#canvas` link
  lands on Topology with the hash rewritten, drawing a gateway, server and domain,
  Apply leaves no banner and no script errors.
- Not verified: light theme for this particular change (no styling changed).

## [v16] - 2026-10-01 — pause / resume, serving nodes on hover, Canvas on top

### Added
- **Pause / resume for maintenance.** Right-click a gateway circle or a DNS server
  square. *Pause gateway* takes this node out of the gateway:
  it gives up the address and stops answering while the other nodes carry on (local
  to the node, never replicated). *Pause server* stops all queries and probes to
  that upstream (replicated, so every node leaves it alone). Paused items are drawn
  grey with a dashed outline. A paused server is not a fault, but if every server
  is paused the gateway turns red. The change applies at once when nothing else is
  waiting to be applied. CLI: `--canvas-pause|--canvas-resume gateway --group N`
  and `... server --group N --server ADDR`. Config: `paused` (group),
  `paused_servers` (dns). History reads "paused on this node" / "servers paused …".
- **Hover the circle to see the cluster nodes serving the gateway** (address,
  "this node", gateway controller, slot, address families). Nodes that went
  quiet or hold no slot are left out. Also in `/api/canvas` as `members`.

### Changed
- **The row of buttons above the drawing is gone.** It duplicated the right-click
  menu; every action (edit, add, pause/resume, delete) is in the menu now. The
  legend line says to right-click, and Delete still works on a selected shape.
- **Canvas is a top-level menu item** at the top of the sidebar (it configures as
  well as monitors), no longer under Monitor.

### Verified
- gofmt, build, vet, `go test -race ./...`, `CGO_ENABLED=0` vet and test, five
  cross-compiles, `shellcheck`; new tests `TestPauseServerAndGateway` (pool,
  colours, all-paused red, gateway pause starts no engines and is not replicated,
  history wording) and `TestCanvasMembers`.
- Live (daemon + stub DNS servers): CLI pause/resume of a server and a gateway
  (the address leaves and returns, history entries read correctly); Chromium,
  light and dark: pause/resume from the menus with and without staged changes,
  confirmation for a gateway, grey dashed look, hover text, sidebar navigation.
- Not verified: pausing a gateway with other cluster nodes taking over (single
  node only); IPv6 (the sandbox kernel has none); the removed button row was only
  checked by looking at the page, not with a dedicated test.

## [v15] - 2026-10-01 — everything lives in /var/lib/ddgw

### Added
- **Canvas circle shows the bare address** (no /prefix) and grows to hold a full
  39-character IPv6 address; a dual-stack gateway shows both lines.
- **Canvas: right-click menus and popup forms** for every item. Right-click a
  gateway circle or its tab (Edit gateway…, Add DNS server…, Delete gateway), a
  server square (Edit server…, Add domain…, Delete server), a domain trapezoid
  (Edit domain…, Add domain…, Delete domain) or the empty canvas (New gateway…,
  Add DNS server…). Add/Edit open a modal popup (Esc cancels); deleting a gateway
  or a server that has things under it asks in a popup that counts them. The
  menu works from the keyboard (arrow keys, Esc). The inline form and the
  browser's `confirm()` are gone from this page.

### Changed
- **The configuration moved from `/etc/liras/ddgw.conf` to
  `/var/lib/ddgw/ddgw.conf`**, next to the GUI certificate, config history,
  cluster identity and update data (one private directory, mode 0700). The
  control socket moved from `/run/liras/ddgw.sock` to `/run/ddgw/ddgw.sock`.
  Nothing uses `/etc/liras` any more.
- **Upgrading moves your config over**: the installer copies
  `/etc/liras/ddgw.conf` (and `ddgw-web.crt`/`.key`) to `/var/lib/ddgw`, keeps the
  old files as `*.migrated`, and rewrites the systemd unit to the new path. The
  daemon does the same at its first start when it is run without `--config` as
  root. An explicit `--config /etc/liras/ddgw.conf` is still honoured (an
  already-running daemon that is updated from inside the GUI keeps the path it
  was started with until the unit is rewritten by the installer).
- Uninstall keeps `/var/lib/ddgw` unless `--purge`; `--purge` also removes the old
  `/etc/liras` leftovers.
- A directory created for the config is now private (0700).

### Verified
- gofmt, build, vet, `go test -race` (new `migrate_test.go`), `shellcheck -x -S
  warning` on both scripts.
- Installer, with a shim `systemctl`, in the sandbox: fresh install with a
  pre-v15 `/etc/liras` (config, cert and key moved, mode 0600, `*.migrated`
  left, unit points at `/var/lib/ddgw/ddgw.conf`, manifest updated); same
  version = no-op; uninstall keeps `/var/lib/ddgw`; reinstall + `--purge` leaves
  neither `/var/lib/ddgw` nor `/etc/liras`. The daemon's own migration (run as
  root without `--config`) checked live.
- Chromium, light and dark: right-click on the empty canvas, circle, square,
  trapezoid and tab; add/edit/delete through popups; Esc closes menu and popup;
  apply, then delete the last gateway.
- Not verified: the in-GUI updater on an old daemon that was started with
  `--config /etc/liras/ddgw.conf` (it keeps that path until the installer
  rewrites the unit).

## [v14] - 2026-10-01 — client subnet (ECS) and IPv6 gaps in the Canvas

### Added
- **EDNS Client Subnet (RFC 7871)**: `dns.ecs` (default off), `ecs_prefix4`
  (24), `ecs_prefix6` (56). With it on, forwarded client queries carry the
  client's network so the DNS servers no longer see only the ddgw node's
  address. Per gateway (Canvas → edit gateway, or the group's `dns` block) or in
  the shared block (Configuration page). A client's own ECS is passed through;
  clients without EDNS get the option added and the OPT record stripped from the
  answer (payload 512 over UDP so their limit holds); a FORMERR answer is
  retried once without ECS; loopback/link-local clients and probes never carry
  it; TSIG-signed or otherwise unusual queries are forwarded unchanged. The DNS
  page and `--show-dns` show the state and a counter.
- `--canvas-set gateway --group N [--vip] [--vip6] [--interface] [--ecs on|off]
  [--ecs-v4 BITS] [--ecs-v6 BITS]`; `--canvas-add gateway` takes `--vip6` and
  `--ecs`, so a dual-stack gateway can be created in one command.

### Fixed (IPv6 in the Canvas)
- A gateway with both addresses showed only the IPv4 one: the circle now shows
  both and the tab says "IPv4+IPv6".
- The gateway colour ignored the address family: it is now computed per family
  (`families` in `/api/canvas`, chips on the circle, `--canvas` lists them) and
  the worst one wins; a family whose engine is missing counts as degraded.
- The GUI checks the IPv6 prefix; the CLI can set both addresses.

### Verified
- gofmt, build, vet, `go test -race` (new `ecs_test.go`: option encoding incl.
  masking and v4-mapped clients, add/strip with and without client EDNS, ECS
  already present, extra records, garbage input, real UDP server seeing the /24,
  FORMERR retry, validation; canvas tests for dual-stack status and `set`).
  A test found and I fixed a panic on IPv6 clients (`As4`).

## [v13] - 2026-10-01 — the Canvas: draw gateways, DNS servers and domains

### Added
- **Canvas page** (now the first page of the GUI): one tab per gateway. A circle
  is the gateway and its shared address, a square under it is each DNS server it
  forwards to, a trapezoid under each square is a domain that server must answer.
  Select a shape to add a server / domain, edit it or delete it (Delete key too).
  Deleting a trapezoid removes that test, a square removes the server and its
  domains, a circle removes the whole gateway (with a confirmation that counts
  what goes). Edits are staged and applied together ("Apply changes" / "Discard
  changes", with the reason shown while a gateway has no server or a server has
  no domain). Shapes are **green / yellow / red** (grey = not known yet) from the
  running daemon: a gateway is yellow while a server is down and red with no
  healthy server; a server is red when it fails its tests, yellow when it is
  eligible but a test fails.
- **Per-server probe domains**: `dns.server_queries` (`{"8.8.8.8": ["google.com"]}`)
  overrides the global `queries` for that server. Per-test results (`tests`) are
  in `/api/dns`.
- **Per-gateway DNS pools**: a group may carry its own `dns` block; groups
  without one share the top-level block as before. Each pool is probed and
  hot-reloaded on its own; the DNS page and `--show-dns` list every pool.
- **DNS servers may be host names** (resolved when probed), not only IPs.
- **CLI**: `--canvas` prints the drawing as a tree with the same status colours;
  `--canvas-add gateway|server|domain` and `--canvas-del …` edit it (new
  gateways/servers can take their first server/domain in the same step).
  `GET /api/canvas` serves the same view to the GUI.
- Cluster: gateway pools and per-server domains replicate with the rest of the
  shared settings; the interface stays per node.
- History describes canvas changes in words ("DNS pool: servers added …,
  domains removed …").

### Changed
- An explicit `"groups": []` now means **no gateways** (the daemon idles) instead
  of silently becoming the default group; a missing `groups` key still means the
  default group. Config import and the GUI accept an empty list; the shared
  cluster config may be empty.
- Two gateways may not use the same shared address (rejected on save).
- The Configuration page keeps `groups[].dns` and `dns.server_queries` when it
  saves, and says which groups have their own DNS servers.
- Each server needs at least one probe query (own or global) — checked per server.

### Verified
- gofmt, build, vet, `go test -race` (new tests in `canvas_test.go`: per-server
  probes, hostnames, empty-groups semantics, duplicate addresses, per-group
  pools in the supervisor, canvas colours, edit operations, cluster merge,
  history wording, `/api/canvas`).
- Live: real daemon + stub DNS servers + Chromium (light and dark): drew
  gateways, applied, colours green/yellow/red, deleted a domain with the Delete
  key, a server and a gateway, saved from the Configuration page without
  losing the drawing; `--canvas`, `--canvas-add/-del` against the daemon;
  two-node cluster: add on the primary, edit on a replica, delete.
- Not verified: a cluster-wide status overlay does not exist (status is per
  node); IPv6-only gateways were exercised in unit tests, not on a live network.

## [v12] - 2026-10-01 — login lockout settings, simpler Configuration page

### Added
- **Login lockout is configurable** and shown on the login page. New `web`
  settings (also on the Configuration page, Web GUI card): `max_failed_logins`
  (default **3**), `failed_login_window_minutes` (**1**), `lockout_minutes`
  (**15**) — that many wrong passwords within the window lock the address and
  the user name out for the lockout time (was a fixed 5 failures / 10 min window /
  5 min lock). The attempt that reaches the limit is answered with the lockout
  straight away (HTTP 429, `retry_after`); earlier failures report
  `attempts_left`. New unauthenticated `GET /api/login/state` tells the login
  page whether the caller's address is locked. The login page shows "N attempts
  left before a 15-minute lockout", and while locked it disables the form and
  counts down (also after a page reload).

### Changed
- **The Web GUI can no longer be disabled**: the "Web GUI enabled" checkbox and
  `web.enabled` are gone. Old config files that contain `"enabled"` still load;
  the key is ignored and dropped on the next save.
- Configuration page: the Form/JSON switch, "Reload from disk" and "Validate"
  buttons are removed; only "Save & apply" remains (the server still validates
  and shows the error). Raw JSON is still reachable through History →
  download/upload and `--config-import`.
- Spacing between the file chooser and the action button on the Certificate page.
- Help text, README and `ddgw.conf.example` updated.

### Verified
- gofmt, build, vet, `go test -race` (new/updated tests: lockout after exactly 3
  failures with attempts left 2 then 1, 15-minute `retry_after`, PAM not consulted
  while locked, `/api/login/state`, configured policy honoured, validation of the
  new settings, legacy `enabled` loads and is not written back);
  `CGO_ENABLED=0` vet/test; cross-compiles; `node --check`.

- Real daemon + PAM + headless Chromium: three wrong passwords showed "2 attempts
  left", "1 attempt left", then the form disabled with a 15:00 countdown, which
  survived a page reload; the Configuration page has no enable box, no
  Form/JSON/Reload/Validate buttons, shows the three lockout fields, and saving
  dropped the legacy `enabled` key from a config that had `"enabled": false`;
  the certificate page spacing was checked. Sandbox user/group/PAM file removed.

### Not verified
- One `go test -race` run early in this round failed once without a captured
  reason and then passed in 9 consecutive reruns; no cause was found.

## [v11] - 2026-10-01 — red only for things that cannot be undone

### Changed
- Button colours now follow a traffic-light rule: **red** is only for actions
  that are hard to undo — *Remove* a node from the cluster and *Leave cluster*
  (rejoining needs a new join code). **Amber** (new `btn warn`) is for
  disruptive but reversible actions: *Make this node the active gateway*,
  *Promote this node*, *Stop using the uploaded certificate*. Everything else is
  plain: *Remove* group (nothing changes until you save), *Restore* a version
  (the live config is saved first), *Discard* a pending certificate request, and
  the auto-update toggle (it was red while on, which read as an error).
  Confirmation prompts are unchanged.

### Verified
- `node --check`; gofmt, build, vet, `go test -race`, `CGO_ENABLED=0` vet/test and
  the five cross-compiles re-run. Only the class names on the buttons and one
  CSS rule changed.

### Not verified
- Not re-rendered in a browser this time (class/colour change only).

## [v10] - 2026-10-01 — cluster nodes are reachable by name, IPv4 and IPv6

(Includes the unreleased [v9] changes below.)

### Changed
- A join code already listed the node's configured address, host name and
  interface IPv4/IPv6 addresses and the joiner tried them in turn, but after
  joining, nodes only kept **one** address per peer. Now every node advertises
  its alternatives (`alts`: host name + all non-loopback, non-link-local IPv4 and
  IPv6 addresses, re-read at each sync) in its requests, the join request and
  response, and the gossiped peer list. Every call to a peer tries the address
  that last worked, then its main address, then its alternatives; only
  connection-level failures move on (an answer from the peer, even a refusal, is
  final); a non-final attempt is capped at 6 s so a dead address does not stall
  a sync. A peer keeps the address it calls itself as its identity (primary
  lookups depend on it); the address that worked is remembered separately.
  Errors name every address that was tried.

### Verified
- New test `TestClusterPeerAddressFallback` (dead main + dead alt + working alt;
  preferred address afterwards; error names all addresses; join-code addresses);
  full `go test -race`, `CGO_ENABLED=0` vet/test, cross-compiles, gofmt, vet.
- Live, two real daemons whose configured `self` names do not resolve
  (`nosuchN.invalid`) but which listen on all interfaces: the join code listed
  name, host name and interface IPs; the joiner got in via an alternative; both
  nodes showed each other reachable and replicated, with `alts` stored in
  `cluster.json`. Sandbox cleaned up.

### Not verified
- A real IPv6-only or dual-stack network (this sandbox has no global IPv6; IPv6
  addresses follow the same code path as IPv4 but were not exercised).
- Mixed-version clusters: v10 nodes understand peers without `alts`; older nodes
  ignore the field.

## [v9] - 2026-10-01 — simpler certificate handling in the GUI

### Changed
- **Configuration page**: the "TLS certificate file" / "TLS key file" fields are
  gone from the form (certificates are handled on the Certificate page). Saving
  the form keeps any `web.cert_file`/`web.key_file` already in the config file;
  the raw JSON editor and the config file itself still accept them.
- **Certificate page rewritten in plain language**: "Current certificate" (with
  "Not trusted by browsers" / "Custom", where it comes from, issued to/by, valid
  for), "Upload your own certificate" (paste or choose files, "Use this
  certificate"), and two collapsed sections — "Don't have a certificate yet?
  Create a request for one" and "Go back to the automatic certificate". Jargon
  (CSR, PEM, SAN, "revert", "regenerate", "self-signed") is out of the headings
  and buttons. When the certificate comes from files named in the config file,
  the page says so and hides the upload tools (those files override anything
  uploaded) with the one step to hand control to the page. Duplicate names in
  "Valid for" are removed; file paths are shown only for config-file certificates.
- No change to the CLI or API.

### Verified
- `node --check`; gofmt, build, vet, `go test -race`, `CGO_ENABLED=0` vet/test,
  five cross-compiles. Real daemon + PAM login + headless Chromium (dark): the
  Configuration form no longer shows the fields and saving keeps `cert_file`;
  Certificate page in the automatic case (tools visible, sections collapsed) and
  in the config-file case (tools hidden). Sandbox user/group/PAM file removed.

### Not verified
- Uploading/CSR flows were not re-clicked after the rewording (the endpoints and
  logic are unchanged; covered by `web_mgmt_test.go` and the v5 live run).

## [v8] - 2026-10-01 — left-hand navigation, lighter dark theme

### Changed
- The tab strip across the top is replaced by a **sidebar on the left**, in the
  style of umiss: product name and version/host at the top, collapsible groups
  with chevrons (Monitor: Gateways, Neighbors, DNS; Configure: Configuration,
  History, Certificate; Operate: Cluster, Updates, Help; one open at a time, the
  group of the current page opens itself), the active page marked with an accent
  bar, and "Signed in as … / Sign out" at the bottom. The sidebar stays in place
  while the page scrolls; below 760 px wide it stacks above the content. Page
  URLs (`#cluster` etc.) work as before.
- **Dark theme lightened** (about three steps): page `#0e1217 → #262f3d`, panels
  `#161b22 → #2f3a4b`, raised/hover `#1d232c → #3a4659`, borders `#2a323d →
  #4a566c`, muted text and status colours/backgrounds brightened to keep contrast.
  Light theme unchanged. The theme still follows `prefers-color-scheme` only.

- The Gateways button "Assert AGC on this node" now reads **"Make this node the
  active gateway"**, with a plainer confirmation and error text and a one-line
  explanation of AGC/AFN under it (the CLI help text is reworded too; the
  `--assert-agc` flag is unchanged).

### Verified
- `node --check`; gofmt, build, vet, `go test -race`, `CGO_ENABLED=0` vet/test,
  five cross-compiles. Real daemon + real PAM login, headless Chromium: sidebar
  in dark and light at 1180 px and at 420 px, group switching and `#cluster`
  deep link, no page errors. Sandbox user/group/PAM file removed afterwards.

### Not verified
- Contrast was judged by eye, not measured; browsers other than Chromium.

## [v7] - 2026-10-01 — login page wording

### Changed
- Login page: the heading is now "DNS Distributed Gateway" with the line "Sign in
  with your system account"; the "Your session ended. Please log in again."
  notice is gone (an expired session simply shows the login form). Failed-login
  messages are unchanged.

### Verified
- `node --check webui/app.js`; gofmt, build, vet, `go test -race`, `CGO_ENABLED=0`
  vet/test and the five cross-compiles re-run; login page rendered in headless
  Chromium (dark) for the wording.

## [v6] - 2026-10-01 — installer installs Go from the distro package

### Changed
- `install.sh` now installs Go like the other prerequisites: when no Go >= 1.24
  is found it first tries the distribution package (Debian/Ubuntu
  `golang-1.25-go`, `golang-1.24-go`, `golang-go` — only if apt's candidate
  version is new enough; Fedora/RHEL family `golang`; Arch/Manjaro `go`) and
  falls back to the checksummed go.dev tarball only if that gives nothing usable
  (e.g. an older Go in the repo). `DDGW_FORCE_GO_DOWNLOAD` still forces the
  download. A package Go stays under the system's control; only a downloaded one
  is kept in `/usr/local/share/ddgw/go`.
- The in-place updater also finds versioned distro toolchains
  (`/usr/lib/go-*/bin/go`, which are not on the service's `PATH`).

### Verified
- `bash -n`, `shellcheck -x -S warning`; `--dry-run` shows the package attempt
  then the download fallback; a real run on this Ubuntu 24.04 sandbox with no Go
  installed pulled `golang-1.24-go` (1.24.13) from apt, built ddgw with it and
  installed; uninstall `--purge` and removal of the package afterwards.
  `go vet`, `go test -race`, cross-compiles re-run.

### Not verified
- The package path on Fedora/RHEL/Arch/Debian (only Ubuntu was run; dnf/pacman
  branches were dry-run only).

## [v5] - 2026-09-30 — config versions, clustering, GUI certificate management, in-place updates

Modelled on how umiss does these four things, adapted to ddgw (single binary,
management-only clustering that is independent of the AGC/AFN election). Every
feature has a CLI command and a GUI page; both call the same code
(`Mgmt.Op`).

### Added
- **Config history** (`configver.go`, History tab, `--versions`,
  `--version-show/-diff/-snapshot/-restore/-export`, `--config-import`): every
  change — GUI, CLI, cluster sync or a hand edit of the file — is stored as a
  version in `<state-dir>/versions` (newest 200 kept, identical saves
  collapsed). Summaries name the sections that changed and hide secrets;
  line diff with context between any two versions or against the live config;
  restore (the live config is saved first), download, upload, snapshot with a
  note. The actor of an edit is attributed even when the file watcher notices
  it first.
- **Clustering** (`cluster.go`, `clusternet.go`, `shared.go`, Cluster tab,
  `--cluster-*`, config block `cluster`, TCP 53854): primary/replica
  management cluster. Join codes (`ddgw-join-v1:`, single use, 1 h) carry the
  primary's identity fingerprint; nodes pin each other's per-node certificates
  and sign every request with an HMAC (timestamp window + replay cache).
  Shared settings (DNS block; per group VIP, key, timers, lb_method, max_afns,
  dns_proxy) are edited anywhere, applied on the primary and replicated; per
  node settings (interface, priority, weight, preempt, neighbours, log level,
  web, cluster) stay local. Explicit, epoch-numbered promotion; remove /
  unremove / leave; removed nodes reset themselves.
- **GUI certificate management** (`certmgr.go`, Certificate tab, `--tls-*`):
  install a certificate (chain order, key match, validity, server auth and
  name checks with warnings), generate a CSR whose key never leaves the node,
  revert to self-signed, regenerate, expiry warnings in the GUI and log.
  Precedence: `web.cert_file/key_file` > installed/cluster > self-signed.
  With `cluster.share_cert` (default) the primary's certificate is replicated.
- **Updates** (`update.go`, Updates tab, `--update-*`): upload `ddgw_vN.tgz`
  or `.zip` (safe extraction, size caps, module and VERSION checks); nodes pull
  the source from the peer with the newest one, build it natively (Go, gcc,
  PAM headers), verify version and PAM linkage, keep `ddgw.prev`, swap the
  binary and re-exec. Queue / auto-update live on the primary and replicate;
  nodes update one at a time. Boot guard: three failed starts roll back
  automatically; a start is confirmed after 60 s. Per-node update history.
- `--state-dir` (default `/var/lib/ddgw`); `ddgw.conf.example` and the help
  text document the cluster block and the new commands; README sections for all
  four features.
- **install.sh / uninstall.sh**: create `/var/lib/ddgw` (0700); a downloaded
  Go toolchain is now kept in `/usr/local/share/ddgw/go` (needed for in-place
  updates; `--no-keep-go` removes it, an existing one is reused on upgrade);
  unit has `Restart=always` and `StartLimitIntervalSec=0` (the daemon
  re-executes itself on update); firewall hints include 53854; the manifest
  records `STATE_DIR` and `GO_KEPT`; uninstall removes the kept Go and, with
  `--purge`, the state directory.

### Changed
- **`assert-agc` and all management commands on the status socket are now
  root-only** (`SO_PEERCRED`); `snapshot`/`dns` stay open to whoever can reach
  the socket.
- Changing the GUI certificate or key no longer restarts the HTTPS listener or
  logs anyone out (certificates are served through `GetCertificate`); only
  `web.listen` restarts it. The GUI listener has read/write timeouts and no
  longer logs TLS handshake noise.
- `PUT /api/config` accepts an optional `note`, and the configuration form
  keeps the new `cluster` block (and tags shared fields) instead of dropping it.
- A single self-signed certificate marked as a CA (typical `openssl req -x509`
  output) can be installed (with a warning); a CA first in a longer chain is
  still refused.

### Verified
- `gofmt -l .` clean; `go build`; `go vet ./...`; `go test -race -count=1
  ./...` (55 tests, including 3-node in-process cluster tests, a real native
  build + apply + rollback-guard test, web auth/CSRF/origin tests for every new
  route); `CGO_ENABLED=0 go vet` and `go test`; cross-compile linux/amd64,
  arm64, arm, 386, riscv64; `node --check webui/app.js`.
- **Live, three real daemons on one host** (separate state dirs, loopback
  ports, real PAM login with a throwaway user): join by code; a shared edit on
  a replica reached the primary and all nodes while a local field stayed local;
  promotion (epoch 2); certificate install replicated to all three and revert;
  versions list/diff; `--update-upload` of a v99 copy of the tree and
  `--update-push all` — each node pulled, built, re-executed and its boot guard
  reached `ok`; wrong password, a valid user outside the group and root get the
  same refusal; every new endpoint is 401 without a session; a non-root user
  cannot use the socket. The History, Certificate, Cluster and Updates tabs
  were inspected in headless Chromium (light and dark) and clicked through
  (snapshot, diff, join code, certificate install).
- Installer: `bash -n`, `shellcheck -x -S warning`; `--dry-run` for ubuntu,
  debian, linuxmint, fedora, rocky, almalinux, centos, rhel, arch, manjaro,
  endeavouros (alpine refused) with missing-everything + forced Go download;
  real runs with a shim systemctl: fresh install (Go downloaded from a local
  server and kept, `/var/lib/ddgw` 0700, manifest, unit), same-version no-op,
  upgrade 5→6 and 5→8 with the kept Go found when the system has none, forced
  failure rolled back, downgrade refused, wrong Go checksum refused, uninstall
  without and with `--purge` (state kept / removed). Sandbox changes undone.

### Fixed (found by the live run, all with regression tests)
- Joining a cluster stored the primary twice in the peer list.
- `--cluster-join/-remove/-unremove/-leave` printed the status from before the
  change (evaluation order).
- A relative `--state-dir`/`--config` put a relative `GOPATH` into the update
  build, which Go rejects; paths are made absolute at start.
- A node with no source of its own dropped its update-queue entry before it
  could pull the new source from a peer, so "update all" only updated the node
  that had the upload.
- The History diff page crashed for two identical versions (`null` lists; the
  lists are now `[]`), `kv()` and nested arrays rendered `[object HTMLElement]`
  in the GUI, and the diff/table CSS did not match the markup.

### Not verified
- Multi-host behaviour over a real network (everything above ran on loopback):
  NAT'd or multi-homed `cluster.self`, firewalls, clock skew beyond the ±90 s
  window, partitions and a split-brain recovery by hand.
- A real systemd re-exec on update (the unit with `Restart=always` was only
  written, not run; the update path was exercised with plain processes), and
  updates on distros other than this sandbox's Ubuntu (Go/gcc/PAM-header
  availability on Fedora/RHEL/Arch nodes).
- Cross-compiled binaries have no PAM (stub); only the native amd64 build was
  run with PAM, as before.
- Browsers other than Chromium; concurrent edits on several replicas at once
  (last write at the primary wins).
- Updating from a ddgw older than v5 in place: v4 has no update endpoint, so
  use `install.sh` for that step.

## [v4] - 2026-09-30 — install.sh / uninstall.sh (install, upgrade, remove)

### Added
- **`install.sh`**: detects the distro from os-release (`ID`/`ID_LIKE`) —
  Ubuntu/Debian (apt), Fedora/RHEL/Rocky/Alma (dnf, yum fallback; CRB/PowerTools
  enabled and retried if `pam-devel` is unavailable), Arch/Manjaro (pacman).
  Installs only missing build prerequisites, finds Go >= 1.24 or downloads the
  official toolchain (sha256-verified, build-time only), builds into a temp dir
  *before* touching the system, checks PAM is linked and `--version` matches,
  then installs the binary, share dir, `ddgw` group, per-family
  `/etc/pam.d/ddgw` (only if absent), a NetworkManager unmanaged-devices
  drop-in, SELinux relabel, and the systemd unit.
- **Upgrade logic**: an existing install is detected via `ddgw --version`.
  Same version = no-op (`--force` to repair), downgrade refused without
  `--force`, previous binary kept as `ddgw.prev`, automatic rollback if the
  service doesn't come up, service restarted only if it was running. Config,
  certificate, group members and an existing PAM file are never overwritten.
  Users join the `ddgw` group only via `--add-user` or an interactive prompt.
- **`uninstall.sh`** (also installed as `ddgw-uninstall`): manifest-driven;
  removes service, binary, share dir, leftover `ddgw*` interfaces and the
  status socket. Keeps config and certificate unless `--purge`; removes the PAM
  file only if unchanged since install (or `--purge`); removes the group only
  if the installer created it and `--purge`/`--remove-group` is given.
- README install/uninstall section; CLAUDE.md installer verification recipe.

### Fixed
- Nothing in the daemon changed.

### Verified
- `bash -n` and `shellcheck -x -S warning` clean on both scripts.
- Ubuntu 24.04 sandbox with a shim `systemctl`: fresh install, same-version
  no-op, upgrade v3 to v4, forced start failure with rollback (binary and share
  VERSION restored), downgrade refused then allowed with `--force`, uninstall
  with and without `--purge`, customised PAM file kept, group kept when not
  created by the installer, all sandbox state removed afterwards.
- Go download path against a local HTTP server: good checksum builds, a wrong
  checksum is refused, temp dir cleaned.
- `--dry-run` package selection for ubuntu, debian, linuxmint, fedora, rocky,
  almalinux, centos, arch, manjaro, endeavouros; alpine is refused.

### Not verified
- No run on a real Fedora/RHEL/Rocky/Alma/Arch/Manjaro/Debian host: package
  names, the CRB retry and the PAM stacks (`system-auth`) are from knowledge,
  not tested. Real systemd behaviour (the shim only models state).
- The real go.dev download (unreachable from the sandbox), non-amd64 hosts,
  removal of `ddgw<g>.<s>` interfaces (the sandbox can't create dummy links).

## [v3] - 2026-09-30 — Web GUI on HTTPS :53853 (PAM login, ddgw group), light/dark theme

### Added
- **Web GUI** served by the daemon over HTTPS, default `:53853` (`web` config
  block: `enabled`, `listen`, `cert_file`, `key_file`, `group`, `pam_service`,
  `session_idle_minutes`). Hot-applied: group/PAM service/idle timeout take
  effect immediately; changing listen/cert/key restarts the listener.
- **Everything the CLI does**: Gateways tab (`--show-gateways`) with an
  "Assert AGC on this node" button (`--assert-agc`), Neighbors
  (`--show-neighbors`), DNS (`--show-dns`), Configuration (`--show-config` /
  `--configure`: structured form for log level, every group field, the whole
  `dns` block and the `web` block, plus a raw-JSON editor, Validate and
  Save & apply), Help (`--help`), version in the header.
- **PAM authentication** (`pam_linux.go`, cgo, `-lpam`): `pam_authenticate` +
  `pam_acct_mgmt` against `/etc/pam.d/ddgw`, then membership of the `ddgw`
  group is required (supplementary or primary). Examples in `contrib/pam.d/`.
  Builds without cgo get a stub that fails closed: the GUI refuses to start.
- TLS: configured certificate, or an auto-generated ECDSA self-signed one
  stored beside the config (key 0600), TLS >= 1.2.
- Theme: the stylesheet follows `prefers-color-scheme` (light default, dark when
  the system is dark); no toggle, no stored preference.
- Session handling: 256-bit random tokens, HttpOnly + Secure + SameSite=Strict
  cookie, idle (default 30 min) and absolute (12 h) expiry, per-session CSRF
  token required on every POST/PUT, same-origin check, JSON-only bodies,
  periodic group re-check (removal from the group ends the session), login
  lockout after 5 failures per address or user (5 min), one generic error for
  every login failure cause, strict CSP (no inline script/style), nosniff,
  frame-ancestors none.
- `view.go`: gateway/neighbor presentation shared by the CLI and the GUI.

### Changed
- `DaemonConfig` gained the `web` block (defaults: enabled, `:53853`, group and
  PAM service `ddgw`). A config with no `web` key therefore turns the GUI on;
  set `"web": {"enabled": false}` to keep it off.
- Config saves keep the existing file's mode and create new files 0600 (the
  file holds the groups' HMAC keys); previously 0644.
- The reload path returns an error so the GUI can report a failed apply.

### Fixed
- `--show-gateways` labelled **every live peer AGC** (it tested the peer's
  liveness string "active" instead of the elected AGC). The role is now the
  elected AGC, or this node while ACTIVE; fixed for both CLI and GUI.
- Saving from the GUI with zero groups is refused (an empty list used to
  silently load as the default group on eth0).

### Deliberately not done
- No HSTS header: HSTS is per host (not per port) and would pin every other
  service on the same hostname to HTTPS for a year.
- Sessions are in memory; a restart or a listener restart logs everyone out.

### Verified
- `gofmt` clean; `go build`, `go vet` (cgo on and `CGO_ENABLED=0`),
  `go test -race ./...` (cgo) and `CGO_ENABLED=0 go test ./...` pass.
- Web tests with a fake authenticator: login rules (wrong password, valid PAM
  user outside the group, malformed input), every endpoint refuses
  unauthenticated and forged-cookie requests, CSRF/Origin/content-type
  enforcement, logout, idle expiry, group-removal revocation, lockout (PAM not
  consulted while locked), CSP/security headers, static assets, config
  GET/PUT/dry-run/validation errors/unknown-field rejection/zero-group
  rejection/save mode/reload call, and a real TLS listener with a generated
  certificate (TLS >= 1.2, plain HTTP not served, disable stops it).
- **Real PAM** on the sandbox (libpam0g-dev, `/etc/pam.d/ddgw` from
  `contrib/pam.d/ddgw.debian`, real users via `useradd`): member of `ddgw`
  logged in over HTTPS on port 53853; a valid-password user outside the group,
  `root`, and a wrong password were all refused with the same message;
  gateways/neighbors/dns/config/assert-agc worked against the live daemon;
  saving a config enabling the DNS proxy from the GUI reloaded it and the proxy
  answered; logout invalidated the cookie; clean SIGTERM shutdown.
- UI: 44-check jsdom smoke test of the real `app.js` against a mocked backend
  (login, error display, tabs, assert-AGC with CSRF, DNS/neighbor/gateway
  rendering, config form edits sent as correctly typed JSON, add/remove group,
  JSON mode round-trip and syntax errors, server validation errors, 401 back to
  login, logout, server strings rendered as text not HTML).
- Headless Chromium against the live daemon in light and dark colour schemes:
  background switches (`rgb(244,246,249)` / `rgb(14,18,23)`), screenshots of
  login, gateways, DNS and configuration reviewed, no console errors other than
  the expected 401 from the unauthenticated session probe.
- Cross-compiles for linux/amd64, arm64, arm, 386, riscv64 (without cgo, i.e.
  the fail-closed PAM stub).

### Not verified
- A cgo/PAM build for any architecture other than the sandbox's amd64 (needs a
  target C toolchain and libpam headers).
- PAM stacks other than the stock Debian `common-auth` (LDAP/SSSD/2FA modules),
  and the RHEL `ddgw.rhel` example.
- Firefox and Safari (only Chromium and jsdom were run).
- The GUI reaching a multi-node deployment; it was exercised against one node.
- Browser behaviour across a self-signed certificate renewal (renewal only
  happens at start-up).

## [v2] - 2026-09-30 — Project renamed dgw → ddgw (DNS Distributed Gateway)

### Changed
- Project, Go module, binary, usage text and log lines are now **ddgw**.
  `dgw` remains the name of the Python original and of the wire protocol's
  default HMAC key.
- Paths and names that moved with the rename (**breaking for an existing
  install**; copy `/etc/liras/dgw.conf` to `/etc/liras/ddgw.conf` to migrate):
  - config file `/etc/liras/ddgw.conf` (was `dgw.conf`)
  - status socket `/run/liras/ddgw.sock` (was `dgw.sock`)
  - macvlan interfaces `ddgw<group>.<afn>` (was `dgw<group>.<afn>`), so a ddgw
    node and a Python dgw node on one host cannot delete each other's links
  - `dgw.conf.example` → `ddgw.conf.example`; archive is `ddgw_v<N>.tgz`
    containing `ddgw/`

### Deliberately unchanged
- Packet format, magic, version and the default HMAC key `"dgw"`: ddgw nodes
  still interoperate with Python dgw nodes (golden vectors still pass).
- Older changelog entries are not rewritten; `[v1]` refers to the project by
  its then-name.

### Verified
- `gofmt -l .` clean; `go build ./...`, `go vet ./...`, `go test -race ./...`
  pass; cross-compiles for linux/amd64, arm64, arm, 386, riscv64;
  `ddgw --version` prints `ddgw v2`; no stray `dgw` references remain outside
  the changelog history, the Python-compat notes, and the default key.

### Not verified
- Migration on a real host with the old daemon already running.

## [v1] - 2026-09-30 — Go port of dgw, DNS proxy on the VIP, release process

### Added
- **Go port of the Python `dgw` daemon** (no third-party dependencies):
  wire format, election/state machine, AFN slot assignment and the four LB
  methods, vMAC macvlans and MAC takeover, ARP/NS responders on the AGC,
  inotify hot reload, Unix-socket status server and the `--show-*`,
  `--assert-agc` and `--configure` CLI.
- **DNS proxy** (`dns.go`, `dnsfront.go`): a top-level `dns` config block
  (`servers`, `queries`, `require`, timing knobs) and a per-group
  `dns_proxy` flag. Each server is probed with every configured query; it
  receives traffic only while the probes pass (`require: all|any`). Response
  latency is an EWMA fed by probes and live queries; eligible servers are
  tried fastest first, falling through on timeout/SERVFAIL/REFUSED. No
  eligible server → immediate SERVFAIL. UDP and TCP are served on the VIP;
  truncated UDP answers are relayed so the client retries over TCP.
  `dgw --show-dns` shows ranking, health, latency and counters. The `dns`
  block hot-reloads (a changed pool is probed before it replaces the old one).
- AFN nodes with `dns_proxy` put the VIP on `lo` as /32 (/128) and set
  `arp_ignore=1`/`arp_announce=2` so only the AGC answers ARP/NS for it.
- `VERSION` (embedded into the binary, `dgw --version`), this changelog, and
  `CLAUDE.md` describing the release process.

### Fixed (relative to the Python original)
- A node with higher priority than the incumbent AGC set a local variable and
  returned, leaving it stuck in SPEAK; it now joins as an AFN as the code
  comment always said.
- `--assert-agc` reported the wrong "resigned" address (read after the
  election had overwritten it).
- Startup no longer blocks on an interface with no IP yet (the Python waited
  forever and delayed the status socket).
- Config reload now re-arms the file watch after rename-over saves and picks
  up in-place creates; events are debounced.
- Size comments in the original said 44/76 bytes; the real packet is 47/79.

### Kept deliberately
- Election ties compare IPs as text, exactly like the Python, so mixed
  Python/Go groups elect the same winner.
- Unicast mode with several groups on the one protocol port still lets
  SO_REUSEPORT hand each datagram to a single socket.
- The Python rewrote the config on startup; the Go daemon does not.

### Verified
- `go build ./...`, `go vet ./...`, `go test -race ./...` pass (19 tests).
- Wire compatibility: v4 and v6 packets generated by the Python daemon parse
  in Go and Go re-encodes them byte-for-byte (`testdata/*.hex`).
- DNS pool against fake UDP/TCP upstreams: fastest-first ranking, re-ranking
  after latency change, NXDOMAIN/timeout exclusion and re-admission, live
  failover and demotion, SERVFAIL fallthrough, immediate SERVFAIL with no
  eligible server, frontend over UDP and TCP.
- Manual end-to-end run on `lo` (unicast mode, one node): won election,
  answered on the VIP, all traffic to the fastest upstream, failover after
  killing it, hot reload via atomic rename, bad config rejected while
  running, clean SIGTERM shutdown.
- Cross-compiles for linux/amd64, arm64, arm, 386, riscv64.

### Not verified
- Anything needing more than one host or real macvlans: multi-node election
  on a wire, ARP/NS steering to an AFN's vMAC, and an AFN answering DNS for
  the VIP via the `lo` + `arp_ignore` arrangement. This is the first thing to
  test on real hardware.
- IPv6 VIP path (NS responder, v6 DNS listener) beyond unit-level checks.
- `SO_REUSEPORT` is hard-coded to 15; linux/mips* compiles but would use the
  wrong value there.
