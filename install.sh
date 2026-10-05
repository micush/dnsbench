#!/usr/bin/env bash
# dnsbench installer.
#
# Installs what is needed to build (a C compiler, the PAM headers and, if the
# system's Go is missing or older than 1.22, a verified Go toolchain used only
# for this build), compiles the source in ./source, and installs the result as
# a systemd service. Safe to re-run to upgrade; a failed upgrade rolls back.
#
#   sudo bash install.sh [--dry-run] [--no-start] [--allow-downgrade]
#
# Environment overrides (mainly for testing): DNSBENCH_INSTALL_DIR,
# DNSBENCH_CONF_DIR, DNSBENCH_STATE_DIR, DNSBENCH_PAM_DIR, DNSBENCH_UNIT_DIR,
# DNSBENCH_SYSTEMCTL, DNSBENCH_FORCE_SYSTEMD=1, DNSBENCH_OS_RELEASE,
# DNSBENCH_ASSUME_MISSING=gcc,pam,go,curl,tar, DNSBENCH_GO_BASE_URL,
# DNSBENCH_SETTLE (seconds to wait for the service), DNSBENCH_SKIP_DEPS=1,
# DNSBENCH_INSTALL_USER (the user to add to the login group instead of the
# one who ran sudo).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC_DIR="$SCRIPT_DIR/source"
CONTRIB="$SCRIPT_DIR/contrib"

INSTALL_DIR="${DNSBENCH_INSTALL_DIR:-/opt/dnsbench}"
CONF_DIR="${DNSBENCH_CONF_DIR:-/etc/dnsbench}"
STATE_DIR="${DNSBENCH_STATE_DIR:-/var/lib/dnsbench}"
PAM_DIR="${DNSBENCH_PAM_DIR:-/etc/pam.d}"
UNIT_DIR="${DNSBENCH_UNIT_DIR:-/etc/systemd/system}"
SYSTEMCTL="${DNSBENCH_SYSTEMCTL:-systemctl}"
OS_RELEASE="${DNSBENCH_OS_RELEASE:-/etc/os-release}"
GO_BASE_URL="${DNSBENCH_GO_BASE_URL:-https://go.dev/dl}"
SETTLE="${DNSBENCH_SETTLE:-3}"
GO_MIN_MAJOR=1
GO_MIN_MINOR=22

CONF_FILE="$CONF_DIR/dnsbench.conf"
UNIT_FILE="$UNIT_DIR/dnsbench.service"
PAM_FILE="$PAM_DIR/dnsbench"
BIN="$INSTALL_DIR/dnsbench"

DRY=0
NO_START=0
ALLOW_DOWNGRADE=0

RED=$'\033[0;31m'; YELLOW=$'\033[1;33m'; GREEN=$'\033[0;32m'; BLUE=$'\033[0;34m'; NC=$'\033[0m'
info() { echo "${BLUE}[INFO]${NC}  $*"; }
ok()   { echo "${GREEN}[OK]${NC}    $*"; }
warn() { echo "${YELLOW}[WARN]${NC}  $*"; }
die()  { echo "${RED}[ERROR]${NC} $*" >&2; exit 1; }

usage() {
  sed -n '2,17p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY=1 ;;
    --no-start) NO_START=1 ;;
    --allow-downgrade) ALLOW_DOWNGRADE=1 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $arg (try --help)" ;;
  esac
done

# run CMD...: execute, or just print it in --dry-run mode.
run() {
  if [[ $DRY -eq 1 ]]; then echo "  + $*"; else "$@"; fi
}

[[ -f "$SRC_DIR/go.mod" && -f "$SRC_DIR/VERSION" ]] ||
  die "source/go.mod or source/VERSION not found next to install.sh; run it from the extracted project."
NEW_VERSION="$(tr -d '[:space:]' < "$SRC_DIR/VERSION")"
[[ "$NEW_VERSION" =~ ^[0-9]+$ ]] || die "source/VERSION is not a plain integer: '$NEW_VERSION'"

if [[ $DRY -eq 0 && $EUID -ne 0 ]]; then
  die "Run as root: sudo bash install.sh"
fi

echo
echo "  dnsbench installer  (version $NEW_VERSION)"
if [[ $DRY -eq 1 ]]; then echo "  DRY RUN: nothing will be changed"; fi
echo

# ── Platform ──────────────────────────────────────────────────────────────────
[[ "$(uname -s)" == "Linux" ]] || die "dnsbench runs on Linux only."

os_id="" os_like=""
if [[ -r "$OS_RELEASE" ]]; then
  # shellcheck source=/dev/null
  os_id="$(. "$OS_RELEASE"; echo "${ID:-}")"
  # shellcheck source=/dev/null
  os_like="$(. "$OS_RELEASE"; echo "${ID_LIKE:-}")"
fi

FAMILY=""
for id in $os_id $os_like; do
  case "$id" in
    debian|ubuntu|linuxmint|raspbian|pop|neon|kali) FAMILY=apt; break ;;
    fedora|rhel|centos|rocky|almalinux|ol|amzn)     FAMILY=rpm; break ;;
    arch|manjaro|endeavouros|artix)                  FAMILY=pacman; break ;;
    alpine) die "Alpine is not supported: dnsbench needs glibc and PAM." ;;
  esac
done

if [[ -z "$FAMILY" && "${DNSBENCH_SKIP_DEPS:-0}" != "1" ]]; then
  die "Unrecognised distribution '${os_id:-unknown}'.
  Install a C compiler, the PAM development headers (security/pam_appl.h) and Go ${GO_MIN_MAJOR}.${GO_MIN_MINOR}+
  yourself, then re-run with DNSBENCH_SKIP_DEPS=1."
fi

case "$(uname -m)" in
  x86_64|amd64)            GO_ARCH=amd64 ;;
  aarch64|arm64)           GO_ARCH=arm64 ;;
  armv6l|armv7l|armv8l)    GO_ARCH=armv6l ;;
  i386|i486|i586|i686)     GO_ARCH=386 ;;
  riscv64)                 GO_ARCH=riscv64 ;;
  ppc64le)                 GO_ARCH=ppc64le ;;
  s390x)                   GO_ARCH=s390x ;;
  loongarch64)             GO_ARCH=loong64 ;;
  *) GO_ARCH="" ;;
esac

USE_SYSTEMD=0
if [[ "${DNSBENCH_FORCE_SYSTEMD:-0}" == "1" || -d /run/systemd/system ]]; then USE_SYSTEMD=1; fi

assumed_missing() { [[ ",${DNSBENCH_ASSUME_MISSING:-}," == *",$1,"* ]]; }
have_cmd() { ! assumed_missing "$1" && command -v "$1" >/dev/null 2>&1; }

# ── Who gets to sign in ───────────────────────────────────────────────────────
# Sign-in is limited to members of a group. The person running the installer is
# added to it: the user behind sudo, or root if run directly as root.
INSTALL_USER="${DNSBENCH_INSTALL_USER:-}"
if [[ -z "$INSTALL_USER" ]]; then
  INSTALL_USER="${SUDO_USER:-}"
  if [[ -z "$INSTALL_USER" ]]; then
    INSTALL_USER="$(whoami)"
  fi
fi
if [[ -n "$INSTALL_USER" ]] && ! id "$INSTALL_USER" >/dev/null 2>&1; then
  warn "The user '$INSTALL_USER' does not exist on this system; not adding anyone to the login group"
  INSTALL_USER=""
fi

# ── Build dependencies ────────────────────────────────────────────────────────
have_pam_headers() {
  ! assumed_missing pam && [[ -f /usr/include/security/pam_appl.h ]]
}
have_libc_headers() {
  ! assumed_missing gcc && [[ -f /usr/include/stdlib.h ]]
}

# go_ok PATH: is this a Go >= 1.22?
go_ok() {
  local gobin="$1" v major rest minor
  [[ -x "$gobin" ]] || return 1
  v="$("$gobin" version 2>/dev/null | awk '{print $3}')" || return 1
  v="${v#go}"
  major="${v%%.*}"
  rest="${v#*.}"
  minor="${rest%%.*}"
  [[ "$major" =~ ^[0-9]+$ && "$minor" =~ ^[0-9]+$ ]] || return 1
  (( major > GO_MIN_MAJOR || (major == GO_MIN_MAJOR && minor >= GO_MIN_MINOR) ))
}

SYSTEM_GO=""
if ! assumed_missing go; then
  for cand in "$(command -v go 2>/dev/null || true)" /usr/local/go/bin/go; do
    if [[ -n "$cand" ]] && go_ok "$cand"; then SYSTEM_GO="$cand"; break; fi
  done
fi

need_pkgs=()
case "$FAMILY" in
  apt)
    have_cmd gcc || need_pkgs+=(gcc)
    have_pam_headers || need_pkgs+=(libpam0g-dev)
    have_libc_headers || need_pkgs+=(libc6-dev)
    ;;
  rpm)
    have_cmd gcc || need_pkgs+=(gcc)
    have_pam_headers || need_pkgs+=(pam-devel)
    have_libc_headers || need_pkgs+=(glibc-devel)
    ;;
  pacman)
    have_cmd gcc || need_pkgs+=(gcc)
    have_pam_headers || need_pkgs+=(pam)
    ;;
esac
if [[ -z "$SYSTEM_GO" ]]; then
  have_cmd curl || need_pkgs+=(curl)
  have_cmd tar || need_pkgs+=(tar)
  need_pkgs+=(ca-certificates)
fi
if (( ${#need_pkgs[@]} )); then
  mapfile -t need_pkgs < <(printf '%s\n' "${need_pkgs[@]}" | awk '!seen[$0]++')
fi

if [[ "${DNSBENCH_SKIP_DEPS:-0}" == "1" ]]; then
  info "DNSBENCH_SKIP_DEPS=1: not installing packages"
elif (( ${#need_pkgs[@]} )); then
  info "Installing build dependencies: ${need_pkgs[*]}"
  case "$FAMILY" in
    apt)
      # An unrelated broken repository makes "update" fail; the install can still work.
      if ! run env DEBIAN_FRONTEND=noninteractive apt-get update -qq; then
        warn "apt-get update reported problems (often an unrelated third-party repository); continuing"
      fi
      run env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${need_pkgs[@]}"
      ;;
    rpm)
      if command -v dnf >/dev/null 2>&1; then run dnf install -y "${need_pkgs[@]}"
      else run yum install -y "${need_pkgs[@]}"; fi
      ;;
    pacman)
      run pacman -S --needed --noconfirm "${need_pkgs[@]}"
      ;;
  esac
  if [[ $DRY -eq 0 ]]; then ok "Build dependencies installed"; fi
else
  ok "Build dependencies already present"
fi

if [[ $DRY -eq 0 ]]; then
  have_cmd gcc || die "No C compiler (gcc) found; cgo is needed for PAM."
  have_pam_headers || die "PAM headers (security/pam_appl.h) not found after installing dependencies."
fi

# ── Scratch space ─────────────────────────────────────────────────────────────
BUILD_DIR=""
# shellcheck disable=SC2317  # runs via the EXIT trap
cleanup() {
  if [[ -n "$BUILD_DIR" && -d "$BUILD_DIR" ]]; then
    chmod -R u+w "$BUILD_DIR" 2>/dev/null || true # the Go module cache is read-only
    rm -rf "$BUILD_DIR"
  fi
  return 0
}
trap cleanup EXIT
if [[ $DRY -eq 0 ]]; then BUILD_DIR="$(mktemp -d /tmp/dnsbench-build.XXXXXX)"; fi

# ── Go toolchain ──────────────────────────────────────────────────────────────
GO_BIN=""
if [[ -n "$SYSTEM_GO" ]]; then
  GO_BIN="$SYSTEM_GO"
  info "Using system Go: $("$GO_BIN" version)"
else
  [[ -n "$GO_ARCH" ]] || die "No official Go build for $(uname -m); install Go ${GO_MIN_MAJOR}.${GO_MIN_MINOR}+ yourself and re-run."
  info "No Go ${GO_MIN_MAJOR}.${GO_MIN_MINOR}+ found; downloading the official toolchain (used for this build only)"
  if [[ $DRY -eq 1 ]]; then
    echo "  + fetch ${GO_BASE_URL%/dl}/VERSION?m=text, then <file>.tar.gz and <file>.tar.gz.sha256 from $GO_BASE_URL"
    echo "  + verify the SHA-256, unpack into a temporary directory"
  else
    ver_line="$(curl -fsSL --retry 3 "${GO_BASE_URL%/dl}/VERSION?m=text" | head -n1 | tr -d '[:space:]')" ||
      die "Could not reach ${GO_BASE_URL%/dl}. Check the network, or install Go ${GO_MIN_MAJOR}.${GO_MIN_MINOR}+ yourself and re-run."
    [[ "$ver_line" =~ ^go[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || die "Unexpected Go version string: '$ver_line'"
    tarball="${ver_line}.linux-${GO_ARCH}.tar.gz"
    curl -fsSL --retry 3 -o "$BUILD_DIR/$tarball" "$GO_BASE_URL/$tarball" || die "Download of $tarball failed."
    want="$(curl -fsSL --retry 3 "$GO_BASE_URL/$tarball.sha256" | awk '{print $1}' | head -n1)" ||
      die "Could not fetch the checksum for $tarball."
    [[ "$want" =~ ^[0-9a-f]{64}$ ]] || die "Checksum for $tarball is not a SHA-256 value: '$want'"
    got="$(sha256sum "$BUILD_DIR/$tarball" | awk '{print $1}')"
    [[ "$got" == "$want" ]] || die "SHA-256 mismatch for $tarball (expected $want, got $got). Refusing to use it."
    ok "Checksum verified for $tarball"
    tar -xzf "$BUILD_DIR/$tarball" -C "$BUILD_DIR" || die "Could not unpack $tarball"
    rm -f "$BUILD_DIR/$tarball"
    GO_BIN="$BUILD_DIR/go/bin/go"
    go_ok "$GO_BIN" || die "The downloaded Go does not run here: $("$GO_BIN" version 2>&1 || true)"
  fi
fi

# ── Build ─────────────────────────────────────────────────────────────────────
info "Building dnsbench $NEW_VERSION from source"
NEW_BIN="${BUILD_DIR:-/tmp/dnsbench-build}/dnsbench"
if [[ $DRY -eq 1 ]]; then
  echo "  + (cd source && CGO_ENABLED=1 go build -trimpath -buildvcs=false -ldflags='-s -w' -o <tmp>/dnsbench .)"
else
  (
    cd "$SRC_DIR"
    export CGO_ENABLED=1 GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOPROXY=off
    export GOCACHE="$BUILD_DIR/gocache" GOPATH="$BUILD_DIR/gopath"
    "$GO_BIN" build -trimpath -buildvcs=false -ldflags='-s -w' -o "$NEW_BIN" .
  ) || die "Build failed. The messages above say why; nothing has been changed on this system."
  built="$("$NEW_BIN" --version 2>/dev/null | awk '{print $2}')" || built=""
  [[ "$built" == "$NEW_VERSION" ]] || die "The built binary reports version '$built', expected '$NEW_VERSION'."
  ldd_out="$(ldd "$NEW_BIN" 2>&1 || true)"
  [[ "$ldd_out" == *libpam* ]] || die "The built binary is not linked against PAM; refusing to install a build nobody could log in to."
  ok "Build complete"
fi

# ── Existing installation ─────────────────────────────────────────────────────
OLD_VERSION=""
LEGACY=0
if [[ -x "$BIN" ]]; then
  # The pre-Go binary ignores unknown flags and starts its web server, so never
  # wait on it: a bounded run, and only a line of exactly "dnsbench N" counts.
  old_out=""
  if command -v timeout >/dev/null 2>&1; then
    old_out="$(timeout 5 "$BIN" --version 2>/dev/null </dev/null | head -n1 || true)"
  fi
  if [[ "$old_out" =~ ^dnsbench\ ([0-9]+)$ ]]; then OLD_VERSION="${BASH_REMATCH[1]}"; fi
  if [[ -z "$OLD_VERSION" ]]; then
    LEGACY=1
    info "Found a previous (pre-Go) dnsbench installation; it will be replaced"
  elif [[ "$OLD_VERSION" =~ ^[0-9]+$ ]]; then
    if (( OLD_VERSION > NEW_VERSION )) && [[ $ALLOW_DOWNGRADE -eq 0 ]]; then
      die "Installed version $OLD_VERSION is newer than this one ($NEW_VERSION). Pass --allow-downgrade to proceed."
    fi
    info "Installing version $NEW_VERSION over version $OLD_VERSION"
  fi
fi

if [[ $DRY -eq 1 ]]; then
  echo
  echo "Would install:"
  echo "  binary   $BIN"
  echo "  docs     $INSTALL_DIR/README.md, LICENSE.txt"
  echo "  config   $CONF_FILE  (kept if present)"
  echo "  state    $STATE_DIR"
  echo "  PAM      $PAM_FILE  (kept if present)"
  echo "  service  $UNIT_FILE"
  echo "  group    dnsbench, created if missing; ${INSTALL_USER:-nobody (no user to add)} would be added"
  exit 0
fi

# ── Install (with rollback) ───────────────────────────────────────────────────
WAS_ACTIVE=0
if [[ $USE_SYSTEMD -eq 1 ]] && "$SYSTEMCTL" is-active --quiet dnsbench 2>/dev/null; then
  WAS_ACTIVE=1
  info "Stopping the running service"
  "$SYSTEMCTL" stop dnsbench
fi

mkdir -p "$INSTALL_DIR"
PREV=""
if [[ -f "$BIN" ]]; then
  PREV="$INSTALL_DIR/dnsbench.prev"
  cp -p "$BIN" "$PREV"
fi
install -m 755 "$NEW_BIN" "$INSTALL_DIR/dnsbench.new"
mv -f "$INSTALL_DIR/dnsbench.new" "$BIN"
for doc in README.md LICENSE.txt; do
  if [[ -f "$SCRIPT_DIR/$doc" ]]; then install -m 644 "$SCRIPT_DIR/$doc" "$INSTALL_DIR/$doc"
  else warn "$doc not found; the in-app page will say so"; fi
done
# With SELinux enforcing, systemd may refuse to execute a binary labelled for /opt.
if command -v getenforce >/dev/null 2>&1 && [[ "$(getenforce 2>/dev/null)" == "Enforcing" ]] &&
   command -v chcon >/dev/null 2>&1; then
  chcon -t bin_t "$BIN" 2>/dev/null || warn "Could not set the SELinux label on $BIN"
fi
ok "Binary installed: $BIN"

rollback() {
  warn "Rolling back to the previous version"
  if [[ -n "$PREV" && -f "$PREV" ]]; then
    mv -f "$PREV" "$BIN"
    if [[ $WAS_ACTIVE -eq 1 ]]; then "$SYSTEMCTL" restart dnsbench || true; fi
    warn "The previous version has been restored."
  else
    warn "There was no previous version to restore."
  fi
}

# State directory (systemd's StateDirectory= creates it too; this covers manual runs).
mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR"

# Config
mkdir -p "$CONF_DIR"
write_fresh_config() {
  local host="${1:-}" port="${2:-}"
  install -m 640 "$CONTRIB/dnsbench.conf" "$CONF_FILE"
  if [[ -n "$host" ]]; then sed -i "s|^LISTEN_HOST=.*|LISTEN_HOST=$host|" "$CONF_FILE"; fi
  if [[ -n "$port" ]]; then sed -i "s|^LISTEN_PORT=.*|LISTEN_PORT=$port|" "$CONF_FILE"; fi
  return 0
}
conf_value() { sed -n "s/^$1=//p" "$CONF_FILE" | head -n1; }

if [[ ! -f "$CONF_FILE" ]]; then
  write_fresh_config
  ok "Config written: $CONF_FILE"
elif grep -qE '^(TDNS_API|TLS_PFX|TLS_PFX_PASS|SECRET_KEY|LOCAL_USER|LOCAL_PASS)=' "$CONF_FILE"; then
  keep_host="$(conf_value LISTEN_HOST)"
  keep_port="$(conf_value LISTEN_PORT)"
  if [[ "$keep_port" == "5353" ]]; then keep_port=8453; fi # 5353 is mDNS; the old default clashed with avahi
  cp -p "$CONF_FILE" "$CONF_FILE.pre-go"
  write_fresh_config "$keep_host" "$keep_port"
  ok "Old-format config replaced (your listen address was kept); the original is at $CONF_FILE.pre-go"
  warn "Technitium integration, local-user login and .pfx certificates are gone. Logins now use PAM,"
  warn "and a self-signed certificate is generated unless you set TLS_CERT and TLS_KEY (PEM files)."
else
  info "Config kept: $CONF_FILE"
fi

# Schedules saved by earlier versions lived beside the config
if [[ -f "$CONF_DIR/schedules.json" && ! -f "$STATE_DIR/schedules.json" ]]; then
  cp -p "$CONF_DIR/schedules.json" "$STATE_DIR/schedules.json"
  ok "Copied your saved schedules to $STATE_DIR/schedules.json"
fi

# Login group: only its members may sign in
LOGIN_GROUP="$(conf_value LOGIN_GROUP)"
LOGIN_GROUP="${LOGIN_GROUP:-dnsbench}"
GROUP_MARKER="$CONF_DIR/.created-login-group"
added_user=""
if [[ "$LOGIN_GROUP" != "dnsbench" ]]; then
  info "Login group is '$LOGIN_GROUP' (set in the config); the installer does not manage its members"
else
  if getent group dnsbench >/dev/null 2>&1; then
    info "Group 'dnsbench' already exists"
  else
    if command -v groupadd >/dev/null 2>&1; then groupadd --system dnsbench
    elif command -v addgroup >/dev/null 2>&1; then addgroup --system dnsbench
    else die "Cannot create the 'dnsbench' group: neither groupadd nor addgroup is available. Create it, then re-run."; fi
    : > "$GROUP_MARKER"
    ok "Created group 'dnsbench'"
  fi
  if [[ -n "$INSTALL_USER" ]]; then
    user_groups=" $(id -nG "$INSTALL_USER" 2>/dev/null || true) "
    if [[ "$user_groups" == *" dnsbench "* ]]; then
      info "'$INSTALL_USER' is already in group 'dnsbench'"
    else
      if command -v usermod >/dev/null 2>&1; then usermod -aG dnsbench "$INSTALL_USER"
      elif command -v gpasswd >/dev/null 2>&1; then gpasswd -a "$INSTALL_USER" dnsbench >/dev/null
      else die "Cannot add '$INSTALL_USER' to the 'dnsbench' group: no usermod or gpasswd. Add them yourself, then re-run."; fi
      ok "Added '$INSTALL_USER' to group 'dnsbench'"
    fi
    added_user="$INSTALL_USER"
  else
    warn "No user was added to the 'dnsbench' group, so nobody can sign in yet."
    warn "Add one with:  sudo usermod -aG dnsbench USERNAME"
  fi
fi

# PAM service
if [[ ! -f "$PAM_FILE" ]]; then
  mkdir -p "$PAM_DIR"
  install -m 644 "$CONTRIB/pam.d/dnsbench" "$PAM_FILE"
  ok "PAM service installed: $PAM_FILE"
else
  info "PAM service kept: $PAM_FILE"
fi

# systemd
if [[ $USE_SYSTEMD -eq 1 ]]; then
  mkdir -p "$UNIT_DIR"
  install -m 644 "$CONTRIB/dnsbench.service" "$UNIT_FILE"
  "$SYSTEMCTL" daemon-reload
  "$SYSTEMCTL" enable dnsbench >/dev/null 2>&1 || warn "Could not enable the service at boot"
  ok "Service installed: $UNIT_FILE"

  if [[ $NO_START -eq 1 ]]; then
    info "Not starting the service (--no-start)"
    rm -f "$PREV"
  else
    "$SYSTEMCTL" restart dnsbench || true
    healthy=0
    for ((i = 0; i < SETTLE * 2 + 1; i++)); do
      sleep 0.5
      if "$SYSTEMCTL" is-active --quiet dnsbench; then healthy=1; else healthy=0; fi
    done
    if [[ $healthy -eq 1 ]] && command -v curl >/dev/null 2>&1; then
      port="$(conf_value LISTEN_PORT)"; port="${port:-8453}"
      scheme=https; if [[ "$(conf_value NO_TLS)" == "true" ]]; then scheme=http; fi
      healthy=0
      for ((i = 0; i < 20; i++)); do
        if curl -sk -o /dev/null --max-time 2 "$scheme://127.0.0.1:$port/login"; then healthy=1; break; fi
        sleep 0.5
      done
      if [[ $healthy -eq 0 ]]; then warn "The service is running but did not answer on port $port"; fi
    fi
    if [[ $healthy -eq 1 ]]; then
      ok "dnsbench is running"
      rm -f "$PREV"
    else
      warn "The service did not start cleanly: journalctl -u dnsbench -n 40"
      rollback
      die "Installation failed and was rolled back."
    fi
  fi
else
  warn "systemd not detected; the binary is installed but no service was created."
  echo "  Run it with:  set -a; . $CONF_FILE; set +a; $BIN"
  rm -f "$PREV"
fi

# ── Done ──────────────────────────────────────────────────────────────────────
PORT="$(conf_value LISTEN_PORT)"; PORT="${PORT:-8453}"
HOST="$(hostname -f 2>/dev/null || hostname)"
scheme=https; if [[ "$(conf_value NO_TLS)" == "true" ]]; then scheme=http; fi
echo
echo "${GREEN}Installation complete (version $NEW_VERSION).${NC}"
echo
echo "  Open:     $scheme://$HOST:$PORT"
if [[ "$LOGIN_GROUP" == "dnsbench" ]]; then
  echo "  Sign in:  with the account and password of a member of the 'dnsbench' group${added_user:+ (you: $added_user)}"
  echo "  Add more: sudo usermod -aG dnsbench NAME   (takes effect at their next sign-in to dnsbench)"
else
  echo "  Sign in:  with the account and password of a member of the '$LOGIN_GROUP' group"
fi
echo "  Config:   $CONF_FILE"
echo "  Logins:   $PAM_FILE"
echo "  Logs:     journalctl -u dnsbench -f"
echo "  Service:  systemctl {start|stop|restart|status} dnsbench"
echo
if [[ -z "$(conf_value TLS_CERT)" && "$(conf_value NO_TLS)" != "true" ]]; then
  echo "  Your browser will warn about the self-signed certificate once; set TLS_CERT and"
  echo "  TLS_KEY in the config to use your own."
  echo
fi
if [[ $LEGACY -eq 1 ]]; then echo "  Upgraded from the Rust version: see the notes above about what changed."; fi
exit 0
