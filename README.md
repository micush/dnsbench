# dns[bench]

**A DNS benchmark with a browser UI and a REST API.**

dns[bench] is one daemon that generates DNS load against any resolver and reports throughput, latency percentiles and response codes. Drive it from the web UI or from curl, run benchmarks on a schedule, and compare runs side by side.

---
![Benchmarking results](snaps/results.png)
---

## Features

- **Four protocols**: UDP, TCP, DoT (DNS over TLS) and DoH (DNS over HTTPS, POST or GET, HTTP/1.1 or HTTP/2)
- **High-rate UDP engine**: batched `sendmmsg`/`recvmmsg`, one socket per worker, a sliding window of in-flight queries
- **Live output**: results stream line by line to their own Output tab on the Benchmark page while the run is going; when it finishes, the Results page opens with the new run highlighted
- **Results history**: past runs with q/s, errors and p95 latency; select two or more to compare them as side-by-side pie charts that update as you tick or untick more (throughput and p95 latency, one slice per run); the best run in each pie, the highest throughput or the lowest p95, is pushed out, enlarged and outlined
- **Schedules**: hourly, daily, weekly, monthly or one-off runs, in the server's local time zone; right-click a schedule to edit, duplicate, run, pause or delete it
- **Update from the web UI**: upload a newer release archive on the Updates page; the server builds it, starts it once to check it works, installs it and restarts, and rolls back by itself if it fails to stay up
- **Login with your system accounts** through PAM, limited to the members of one group
- **Light and dark themes** that follow your system setting automatically
- **REST API**: everything the UI does is available to curl
- **Built from source on the target machine**: no prebuilt binary to trust, standard library only

---

## Install

You need a Linux host with systemd. As root, from the extracted project directory:

```bash
sudo bash install.sh
```

The installer:

1. Installs what it needs to build: a C compiler and the PAM development headers through your package manager (apt, dnf/yum and pacman are supported), and, only if the system has no Go 1.22 or newer, the official Go toolchain, checksum-verified and used for this build alone
2. Compiles the source in `source/`
3. Installs the program to `/opt/dnsbench/`, a default config to `/etc/dnsbench/dnsbench.conf`, a PAM service to `/etc/pam.d/dnsbench`, and the systemd unit
4. Creates the `dnsbench` group and adds the user who ran the installer to it (the person behind `sudo`; root is never added automatically)
5. Starts the service and checks that it answers; if an upgrade does not come up, it puts the previous version back

Then open `https://your-server:8453` and sign in with your own account on that host, the one that ran the installer. The first start generates a self-signed certificate, so your browser warns once.

Useful options: `--dry-run` shows what would happen and changes nothing, `--no-start` installs without starting, `--allow-downgrade` permits installing an older version over a newer one. Run it again at any time to upgrade; your config is kept.

---

## Configuration

Edit `/etc/dnsbench/dnsbench.conf` (plain `KEY=value` lines), then `systemctl restart dnsbench`.

| Setting | Default | Meaning |
|---|---|---|
| `LISTEN_HOST` | `0.0.0.0` | Address the UI and API listen on |
| `LISTEN_PORT` | `8453` | Port for the UI and API |
| `TLS_CERT`, `TLS_KEY` | blank | PEM certificate and key; blank means a self-signed certificate is generated |
| `NO_TLS` | `false` | Serve plain HTTP (only behind a TLS-terminating proxy) |
| `STATE_DIR` | `/var/lib/dnsbench` | Where the generated certificate and the schedules are kept |
| `SCHEDULES_FILE` | blank | Schedules file; blank means `STATE_DIR/schedules.json` |
| `PAM_SERVICE` | blank | PAM service name; blank means `/etc/pam.d/dnsbench` |
| `LOGIN_GROUP` | `dnsbench` | Only members of this group may sign in |
| `ALLOW_UPDATES` | `true` | Let signed-in users update dnsbench from the Updates page; `false` switches that off (see the section Updating from the web UI). Anything but true or false stops the service from starting |

Each setting also exists as a command-line flag (`--host`, `--port`, `--tls-cert`, `--tls-key`, `--no-tls`, `--state-dir`, `--schedules-file`, `--pam-service`, `--login-group`), and flags win over the config file. `dnsbench --help` lists them and `dnsbench --version` prints the version.

---

## TLS

With `TLS_CERT` and `TLS_KEY` blank, dnsbench creates a self-signed certificate (valid ten years, covering localhost, the host name and the host's addresses) on first start and reuses it afterwards.

To use your own certificate, for example one from Let's Encrypt:

```ini
TLS_CERT=/etc/letsencrypt/live/bench.example.com/fullchain.pem
TLS_KEY=/etc/letsencrypt/live/bench.example.com/privkey.pem
```

A renewed certificate file is picked up within about ten seconds, with no restart. If the new files are unreadable or broken, the previous certificate keeps being served.

---

## Authentication

Signing in takes two things: a correct password, and membership of the login group.

1. **Password.** Checked by PAM, so any account that can authenticate on the host qualifies, and expired or locked accounts are refused. The service name is `dnsbench`; edit `/etc/pam.d/dnsbench` to change how passwords are checked, for example to use LDAP or SSSD by replacing the two `pam_unix` lines with your site's stack.
2. **Group.** The account must also belong to the group named by `LOGIN_GROUP` in the config, which is `dnsbench` by default. The installer creates that group and adds the user who ran it.

Anyone who can sign in can make this host send DNS traffic anywhere, which is why the group exists: being able to log in to the machine is not enough.

To let someone in, or to remove them:

```bash
sudo usermod -aG dnsbench alice        # allow
sudo gpasswd -d alice dnsbench         # remove
```

Membership is checked at every sign-in, so a change applies immediately, with no restart; sessions that are already open stay valid until they expire or the user signs out. Both a user's primary group and their supplementary groups count, and with LDAP or SSSD the group can come from the directory. If you set `LOGIN_GROUP` to a group of your own, the installer leaves it and its members alone, and you manage them. If the group does not exist, nobody can sign in, and the service log says why.

A person who has the right password but is not in the group sees the same "Invalid login attempt" message as for a wrong password; the reason is only in the log (`journalctl -u dnsbench`), and a wrong password never reveals whether the account is in the group.

How sessions and logins behave:

- Eight failed logins from one address within five minutes, counting refusals for group membership, block further attempts from it until the window passes (HTTP 429)
- Sessions last 60 minutes, and are extended automatically while one of that user's benchmarks is running
- The cookie is `HttpOnly`, `SameSite=Strict`, and `Secure` when TLS is on
- State-changing requests that the browser marks as cross-site, or that carry a foreign `Origin` header, are refused (HTTP 403). Behind a reverse proxy, pass the original `Host` header through
- Scripts can use the token from `POST /api/login` in an `X-Session-Token` header instead of the cookie

Also bind `LISTEN_HOST` to a management address if you can.

---

## Appearance

The pages follow your system's light or dark setting automatically, switch when it changes while the page is open, and have no flash of the wrong theme on load. The theme button (bottom left in the app, top right on the login page) cycles Auto, Light and Dark; a Light or Dark choice is remembered by that browser, and choosing Auto goes back to following the system.

## Internet access

dnsbench itself needs none. The web interface, including its icons and charts, is built into the binary and loads no fonts, scripts, styles or images from other sites, so it works on an isolated network; the REST API never needed one either. The installer uses the network only to install missing build packages (a C compiler, the PAM headers and Go) through your package manager. Install those first, or set `DNSBENCH_SKIP_DEPS=1`, and it builds offline.

---

## Protocols

| Protocol | Default port | Notes |
|---|---|---|
| UDP | 53 | Batched sends and receives; the `pipeline` setting is the in-flight window per worker |
| TCP | 53 | One persistent connection per worker; reconnects if the server closes it |
| DoT | 853 | TLS over TCP; the server name for certificate checks is the host you typed |
| DoH | 443 | POST or GET, HTTP/1.1 or HTTP/2, one connection per worker |

DoQ (DNS over QUIC) is not supported: QUIC is not in Go's standard library and this project deliberately has no other dependencies.

The server can be written as `host`, `host:port`, `[ipv6]:port`, or for DoH a full URL such as `https://dns.example/dns-query`. The name is resolved once at the start of a run and every worker connects to that address. For DoH the request goes to that resolved address, and the name you typed is sent as the `Host` header and as the TLS server name, so certificate checks and virtual hosting behave as if you had connected by name.

A server string is checked before anything is sent: the port must be a number from 1 to 65535, the host an IP address or a plain name (no `@`, `/`, `?`, `#`, spaces or other URL characters), and a DoH path must start with `/` and contain no control characters. Anything else is refused with HTTP 400 and a message such as `invalid port` or `invalid server`, for a job and for a schedule alike.

---

## REST API

Everything the browser does is an HTTP call, and all of them (except login) need a session: the cookie, or the `X-Session-Token` header.

### Login

```bash
BASE=https://localhost:8453
TOK=$(curl -sk $BASE/api/login -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"yourpassword"}' \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])")
```

A wrong password returns 401, and a blocked address returns 429. `POST /api/logout` ends the session.

### Start a benchmark

```bash
JOB=$(curl -sk $BASE/api/start-job -H 'Content-Type: application/json' \
  -H "X-Session-Token: $TOK" \
  -d '{
    "server":      "8.8.8.8",
    "protocol":    "udp",
    "query_type":  "A",
    "concurrency": 4,
    "pipeline":    64,
    "duration":    "30s",
    "queries":     "google.com\ncloudflare.com\ngithub.com"
  }' | python3 -c "import sys,json; print(json.load(sys.stdin)['job_id'])")
```

Only one benchmark runs at a time: starting a job stops any job that is still running. Numbers and booleans may be sent as JSON numbers and booleans or as strings.

| Field | Default | Description |
|---|---|---|
| `server` | required | Target address, `host:port`, or DoH URL |
| `protocol` | `udp` | `udp`, `tcp`, `dot` or `doh` |
| `query_type` | `A` | `A`, `AAAA`, `CNAME`, `MX`, `TXT`, `NS`, `SOA`, `SRV`, `PTR` or `ANY` |
| `concurrency` | auto | Workers: one less than the CPU count, between 1 and 32 (maximum 4096) |
| `pipeline` | auto | UDP in-flight queries per worker: twice the CPU count, between 1 and 64 (maximum 4096) |
| `count` | `100` | Queries per worker, used when `duration` is empty |
| `duration` | empty | Run for a fixed time: `30s`, `2m`, `1h`, or a bare number of seconds |
| `rate_limit` | `0` | Total queries per second across all workers; 0 means unlimited |
| `recurse` | `true` | Set the recursion-desired bit |
| `insecure` | `true` | Skip TLS certificate verification (DoT and DoH) |
| `doh_method` | `post` | `post` or `get` |
| `doh_protocol` | `1.1` | `1.1` or `2` |
| `queries` | three sample names | Domain names, one per line (commas also work). A `PTR` query accepts a bare IP address |

The rate limit is divided evenly across workers and rounded up, so the effective rate can be slightly higher; the run header shows the effective value.

### Follow a job

```bash
# Live plain text; blocks until the job finishes
curl -sN "$BASE/api/job/$JOB/output" -H "X-Session-Token: $TOK" -k

# Status as JSON
curl -sk "$BASE/api/job/$JOB" -H "X-Session-Token: $TOK"

# Stop it
curl -sk -X POST "$BASE/api/job/$JOB/kill" -H "X-Session-Token: $TOK"
```

`GET /api/job/{id}/stream` is the same output as server-sent events, which is what the browser uses. A late subscriber still receives every line from the start. `GET /api/jobs` lists the jobs the server still remembers.

### Updates

| Request | Purpose |
|---|---|
| `GET /api/update` | Running and staged versions, anything that blocks an update, and the history (newest 50) |
| `POST /api/update/upload` | Body: the release archive itself (`.tgz` or `.zip`, at most 32 MB). Checks and stages it; returns its `version` |
| `POST /api/update/apply` | Build the staged release and restart into it. Answers `409` with `needs_confirm` when a benchmark is running; repeat with `{"force": true}` to go ahead anyway |

```bash
curl -sk -H "X-Session-Token: $TOKEN" -H 'Content-Type: application/octet-stream' \
     --data-binary @dnsbench_v8.tgz https://host:8453/api/update/upload
curl -sk -H "X-Session-Token: $TOKEN" -X POST https://host:8453/api/update/apply
```

Both writes answer `403` when `ALLOW_UPDATES=false`. The server restarts a moment after `apply` succeeds, and every session ends with it, so sign in again to see the result.

### Schedules

| Request | Purpose |
|---|---|
| `GET /api/schedules` | List schedules |
| `POST /api/schedules/add` | Create one; returns its `id` |
| `POST /api/schedules/update` | Change fields of the schedule named by `id` |
| `POST /api/schedules/delete` | Delete by `id` |
| `POST /api/schedules/pause`, `/resume` | Pause or resume by `id` |
| `POST /api/schedules/run` | Run now by `id`; returns the `job_id` |
| `GET /api/scheduler-history` | Results of recent scheduled runs, newest first (last 500) |

A schedule takes `name`, `server`, `protocol`, `concurrency`, `pipeline`, `duration`, `queries`, `qtype`, `recurse`, and its timing: `freq` (`hourly`, `daily`, `weekly`, `monthly` or `once`) with `minute`, `hour`, `weekday` (0 is Sunday) and `monthday` as needed. For `once`, give `run_at` as server-local `YYYY-MM-DDTHH:MM`. A monthly schedule on day 31 runs on the last day of shorter months. Empty `concurrency` and `pipeline` mean auto. Times are in the server's local time zone, and daylight saving changes cannot make them drift.

---

## Concurrency model

Two settings control the load.

**`concurrency`** is the number of workers. Each worker owns its own socket or connection and runs its own loop; they share nothing during the run except counters that are merged every 100 ms.

**`pipeline`** (UDP only) is how many queries each worker keeps in flight. A UDP worker fills its window in batches of up to 32 packets per system call, collects replies in batches, and tracks each query by a transaction ID that encodes its slot, so matching a reply to its query is constant time and a late or duplicate reply is ignored. This is what lets one worker push very high rates.

The most queries in flight at once is `concurrency × pipeline`. TCP, DoT and DoH workers send one query and wait for its reply on a persistent connection, so for them `concurrency` alone sets the parallelism and `pipeline` has no effect.

Every query that is sent is counted. One that is never answered counts as an error, so a dead server shows up as errors rather than as an empty, successful-looking run.

| Goal | Adjust |
|---|---|
| Maximum UDP throughput | Raise `pipeline` first; add workers up to your core count |
| Realistic TCP, DoT or DoH load | Raise `concurrency` only |
| Cap the rate | Set `rate_limit` |
| Simulate N clients | `concurrency` N, `pipeline` 1 |

---

## Performance

On a single virtual CPU, sharing that core with a very fast loopback responder, one dnsbench worker sustained just over 200,000 queries per second with no errors, at about 2.6 microseconds of CPU per query. The previous Rust engine used about 5.2 to 5.9 microseconds per query on the same machine and peaked near 117,000 queries per second there. Absolute numbers depend entirely on your hardware, your network and, above all, the server under test; the CPU cost per query is the figure that carries over from one machine to another.

To measure your own, build the responder in `contrib/perf/reflect.c` and use `contrib/perf/drive.py` (both explain themselves in their headers).

---

## Service management

```bash
systemctl start|stop|restart|status dnsbench
journalctl -u dnsbench -f
```

The service runs as root because PAM must read the shadow file; the unit restricts it otherwise (`ProtectSystem=strict`, private `/tmp`, no new privileges). On disk it can write only to its state directory, `/var/lib/dnsbench`, its private `/tmp`, and `/opt/dnsbench`, which is where the Updates page installs a new version (`ReadWritePaths=-/opt/dnsbench` in the unit).

---

## Updating from the web UI

Sign in, open **Updates**, choose the `dnsbench_vN.tgz` (or `.zip`) archive of a newer release and press **Upload**. The archive is checked (it must be a dnsbench source tree with a plain-integer `VERSION` higher than the running one; links, absolute paths and `..` are refused) and staged under `STATE_DIR/update`. Then press **Update to vN**. The server:

1. builds the staged source on this host with the same settings as `install.sh` (a minute or two; the first build is slower while Go's cache fills),
2. starts the new binary once on a spare loopback port, with a scratch state directory, and checks that its login page answers,
3. keeps the running binary as `STATE_DIR/update/dnsbench.prev`, installs the new one in `/opt/dnsbench`, refreshes `README.md` and `LICENSE.txt` next to it, and restarts.

If anything before the install fails, nothing has changed, and the page shows why. If the new version is installed but does not stay up (three starts without surviving 60 seconds), the previous binary is put back at the next start, and the page says so. The Updates page keeps a history of uploads, installs, failures and rollbacks with who did each.

What to expect:

- **The restart ends every session**, so you sign in again; the page does that for you once the server answers. A benchmark that is running is stopped, so the page asks first, and an update that has already been installed waits for a running benchmark to finish before it restarts.
- **It needs what `install.sh` needs**: Go 1.22 or newer (or whatever the new release's `go.mod` asks for), a C compiler and the PAM headers. The installer leaves them in place. The page lists anything missing.
- **It only moves forward.** Going back to an older release is `install.sh --allow-downgrade`.
- **Installs made before this feature need one `install.sh` run** from a release that has it. That installs the unit with `ReadWritePaths=-/opt/dnsbench`; until then the page says it cannot write there.
- **Anyone who can sign in can make this host build and run code as root** by uploading it. If the sign-in group is wider than the people you would trust with that, set `ALLOW_UPDATES=false` in `/etc/dnsbench/dnsbench.conf` and restart. Every upload and update is written to the service log with the user's name and address.

---

## Upgrading from version 1

Run `install.sh` over the old install. Your config and schedules are kept. **Sign-in now requires membership of the `dnsbench` group**, and the installer adds only the user who ran it, so add everyone else who used dnsbench before:

```bash
sudo usermod -aG dnsbench alice
```

The dark theme is also lighter than before, and light mode now shows when your system is set to light.

---

## Upgrading from the Rust version

Run `install.sh` over the old install. It detects the old version and:

- Replaces the old binary and the systemd unit
- Moves an old-format config aside as `dnsbench.conf.pre-go`, writes a new one and keeps your listen address (the old default port 5353 clashes with mDNS and becomes 8453)
- Copies your saved schedules to `/var/lib/dnsbench`
- Keeps your existing PAM service file
- Creates the `dnsbench` group and adds the user who ran the installer; everyone else who should sign in must be added (see Authentication)

What changed for users: the Technitium DNS Server integration is gone, as are the local-user login and the `.pfx` certificate (use PEM files, or let dnsbench generate one), and DoQ was removed because it never worked. Logins now always use PAM.

---

## Uninstall

```bash
sudo bash uninstall.sh            # keeps config, certificate and schedules
sudo bash uninstall.sh --purge    # removes those too
```

The PAM service file is removed only if it is still exactly the one the installer wrote. The `dnsbench` group is removed only with `--purge`, and only if the installer created it; a group that already existed is left alone.

---

## Building and testing by hand

```bash
cd source
go vet ./... && go test -race -count=1 ./...
go build -trimpath -buildvcs=false -o dnsbench .
```

You need Go 1.22 or newer, a C compiler and the PAM headers (`libpam0g-dev` or `pam-devel`). Built without cgo, dnsbench still compiles but refuses every login.

---

## How it works

```
Browser / curl ──HTTPS──▶ dnsbench (:8453)
                              │
        ┌─────────────────────┼──────────────────────────┐
   POST /api/login      POST /api/start-job        /api/schedules/*
        │                     │                          │
        ▼                     ▼                          ▼
   PAM + group check   worker goroutines × N      scheduler (every 30 s)
                       UDP: sendmmsg/recvmmsg
                       TCP / DoT / DoH: one connection each
                              │
   GET /api/job/{id}/output   (plain text, blocking)
   GET /api/job/{id}/stream   (server-sent events, for the browser)
```

---

## License

GNU General Public License v3, see `LICENSE.txt`.
