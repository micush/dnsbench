#!/usr/bin/env bash
# dnsbench installer.
#
# Installs what is needed to build (a C compiler, the PAM headers and Go via the
# system package manager), compiles the source in ./source, and installs the
# result as a systemd service. Safe to re-run to upgrade; a failed upgrade rolls back.
#
#   sudo bash install.sh [--dry-run] [--no-start] [--allow-downgrade]
#
# Environment overrides (mainly for testing): DNSBENCH_INSTALL_DIR,
# DNSBENCH_CONF_DIR, DNSBENCH_STATE_DIR, DNSBENCH_PAM_DIR, DNSBENCH_UNIT_DIR,
# DNSBENCH_SYSTEMCTL, DNSBENCH_FORCE_SYSTEMD=1, DNSBENCH_OS_RELEASE,
# DNSBENCH_ASSUME_MISSING=gcc,pam,go,curl,tar, DNSBENCH_SETTLE (seconds to wait for the service),
# DNSBENCH_SKIP_DEPS=1, DNSBENCH_INSTALL_USER (the user to add to the login group instead of the one who ran sudo).
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

RED=$'\033[0;31m'
YELLOW=$'\033[1;33m'
GREEN=$'\033[0;32m'
BLUE=$'\033[0;34m'
NC=$'\033[0m'

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

run() {
  if [[ $DRY -eq 1 ]]; then
    echo "  + $*"
  else
    "$@"
  fi
}

[[ -f "$SRC_DIR/go.mod" && -f "$SRC_DIR/VERSION" ]] ||
  die "source/go.mod or source/VERSION not found next to install.sh; run it from the extracted project."

NEW_VERSION="$(tr -d '[:space:]' < "$SRC_DIR/VERSION")"
[[ "$NEW_VERSION" =~ ^[0-9]+$ ]] ||
  die "source/VERSION is not a plain integer: '$NEW_VERSION'"

if [[ $DRY -eq 0 && $EUID -ne 0 ]]; then
  die "Run as root: sudo bash install.sh"
fi

echo
echo "  dnsbench installer  (version $NEW_VERSION)"
if [[ $DRY -eq 1 ]]; then
  echo "  DRY RUN: nothing will be changed"
fi
echo

# ── Platform ──────────────────────────────────────────────────────────────────
[[ "$(uname -s)" == "Linux" ]] || die "dnsbench runs on Linux only."

os_id=""
os_like=""

if [[ -r "$OS_RELEASE" ]]; then
  # shellcheck source=/dev/null
  os_id="$(. "$OS_RELEASE"; echo "${ID:-}")"

  # shellcheck source=/dev/null
  os_like="$(. "$OS_RELEASE"; echo "${ID_LIKE:-}")"
fi

FAMILY=""

for id in $os_id $os_like; do
  case "$id" in
    debian|ubuntu|linuxmint|raspbian|pop|neon|kali)
      FAMILY=apt
      break
      ;;
    fedora|rhel|centos|rocky|almalinux|ol|amzn)
      FAMILY=rpm
      break
      ;;
    arch|manjaro|endeavouros|artix)
      FAMILY=pacman
      break
      ;;
    alpine)
      die "Alpine is not supported: dnsbench needs glibc and PAM."
      ;;
  esac
done

if [[ -z "$FAMILY" && "${DNSBENCH_SKIP_DEPS:-0}" != "1" ]]; then
  die "Unrecognised distribution '${os_id:-unknown}'.
  Install a C compiler, the PAM development headers (security/pam_appl.h) and Go ${GO_MIN_MAJOR}.${GO_MIN_MINOR}+
  yourself, then re-run with DNSBENCH_SKIP_DEPS=1."
fi

USE_SYSTEMD=0

if [[ "${DNSBENCH_FORCE_SYSTEMD:-0}" == "1" || -d /run/systemd/system ]]; then
  USE_SYSTEMD=1
fi

# Correctly test whether a command has been deliberately marked missing.
assumed_missing() {
  [[ ",${DNSBENCH_ASSUME_MISSING:-}," == *,"$1",* ]]
}

have_cmd() {
  ! assumed_missing "$1" &&
    command -v "$1" >/dev/null 2>&1
}

# ── Who gets to sign in ───────────────────────────────────────────────────────
INSTALL_USER="${DNSBENCH_INSTALL_USER:-}"

if [[ -z "$INSTALL_USER" ]]; then
  INSTALL_USER="${SUDO_USER:-}"

  if [[ -z "$INSTALL_USER" ]]; then
    INSTALL_USER="$(whoami)"
  fi
fi

if [[ -n "$INSTALL_USER" ]] &&
   ! id "$INSTALL_USER" >/dev/null 2>&1; then
  warn "The user '$INSTALL_USER' does not exist on this system; not adding anyone to the login group"
  INSTALL_USER=""
fi

# ── Build dependencies & Go check ─────────────────────────────────────────────
have_pam_headers() {
  ! assumed_missing pam &&
    [[ -f /usr/include/security/pam_appl.h ]]
}

have_libc_headers() {
  ! assumed_missing gcc &&
    [[ -f /usr/include/stdlib.h ]]
}

go_ok() {
  local gobin="$1"
  local v major rest minor

  [[ -x "$gobin" ]] || return 1

  v="$("$gobin" version 2>/dev/null | awk '{print $3}')" ||
    return 1

  v="${v#go}"
  major="${v%%.*}"
  rest="${v#*.}"
  minor="${rest%%.*}"

  [[ "$major" =~ ^[0-9]+$ && "$minor" =~ ^[0-9]+$ ]] ||
    return 1

  if (( major > GO_MIN_MAJOR )); then
    return 0
  elif (( major == GO_MIN_MAJOR && minor >= GO_MIN_MINOR )); then
    return 0
  fi

  return 1
}

SYSTEM_GO=""

if ! assumed_missing go; then
  for cand in \
    "$(command -v go 2>/dev/null || true)" \
    /usr/local/go/bin/go \
    /usr/bin/go
  do
    if [[ -n "$cand" ]] && go_ok "$cand"; then
      SYSTEM_GO="$cand"
      break
    fi
  done
fi

need_pkgs=()

case "$FAMILY" in
  apt)
    have_cmd gcc || need_pkgs+=(gcc)
    have_pam_headers || need_pkgs+=(libpam0g-dev)
    have_libc_headers || need_pkgs+=(libc6-dev)
    [[ -n "$SYSTEM_GO" ]] || need_pkgs+=(golang-go)
    ;;

  rpm)
    have_cmd gcc || need_pkgs+=(gcc)
    have_pam_headers || need_pkgs+=(pam-devel)
    have_libc_headers || need_pkgs+=(glibc-devel)
    [[ -n "$SYSTEM_GO" ]] || need_pkgs+=(golang)
    ;;

  pacman)
    have_cmd gcc || need_pkgs+=(gcc)
    have_pam_headers || need_pkgs+=(pam)
    [[ -n "$SYSTEM_GO" ]] || need_pkgs+=(go)
    ;;
esac

if [[ "${DNSBENCH_SKIP_DEPS:-0}" == "1" ]]; then
  info "DNSBENCH_SKIP_DEPS=1: not installing packages"

elif (( ${#need_pkgs[@]} )); then
  info "Installing build dependencies and Go via native package manager: ${need_pkgs[*]}"

  case "$FAMILY" in
    apt)
      if ! run env DEBIAN_FRONTEND=noninteractive apt-get update -qq; then
        warn "apt-get update reported problems; continuing"
      fi

      run env DEBIAN_FRONTEND=noninteractive \
        apt-get install -y --no-install-recommends "${need_pkgs[@]}"
      ;;

    rpm)
      if command -v dnf >/dev/null 2>&1; then
        run dnf install -y "${need_pkgs[@]}"
      else
        run yum install -y "${need_pkgs[@]}"
      fi
      ;;

    pacman)
      run pacman -S --needed --noconfirm "${need_pkgs[@]}"
      ;;
  esac

  if [[ $DRY -eq 0 ]]; then
    ok "Build dependencies and Go installed"
  fi

else
  ok "Build dependencies and Go already present"
fi

# Re-check Go after package installation.
if [[ -z "$SYSTEM_GO" ]]; then
  for cand in \
    "$(command -v go 2>/dev/null || true)" \
    /usr/local/go/bin/go \
    /usr/bin/go
  do
    if [[ -n "$cand" ]] && go_ok "$cand"; then
      SYSTEM_GO="$cand"
      break
    fi
  done
fi

if [[ -z "$SYSTEM_GO" ]]; then
  if [[ $DRY -eq 1 ]]; then
    warn "Dry run: Go ${GO_MIN_MAJOR}.${GO_MIN_MINOR}+ is missing; it would be installed by the package-manager step above."
  else
    die "Go ${GO_MIN_MAJOR}.${GO_MIN_MINOR}+ not found after package installation."
  fi
fi

if [[ -n "$SYSTEM_GO" ]]; then
  GO_BIN="$SYSTEM_GO"
  info "Using Go toolchain: $("$GO_BIN" version)"
fi

if [[ $DRY -eq 0 ]]; then
  have_cmd gcc ||
    die "No C compiler (gcc) found; cgo is needed for PAM."

  have_pam_headers ||
    die "PAM headers (security/pam_appl.h) not found after installing dependencies."
fi

# ── Scratch space ─────────────────────────────────────────────────────────────
BUILD_DIR=""

cleanup() {
  if [[ -n "$BUILD_DIR" && -d "$BUILD_DIR" ]]; then
    chmod -R u+w "$BUILD_DIR" 2>/dev/null || true
    rm -rf "$BUILD_DIR"
  fi

  return 0
}

trap cleanup EXIT

if [[ $DRY -eq 0 ]]; then
  BUILD_DIR="$(mktemp -d /tmp/dnsbench-build.XXXXXX)"
fi

# ── Build ─────────────────────────────────────────────────────────────────────
info "Building dnsbench $NEW_VERSION from source"

NEW_BIN="${BUILD_DIR:-/tmp/dnsbench-build}/dnsbench"

if [[ $DRY -eq 1 ]]; then

  echo "  + (cd source && CGO_ENABLED=1 go build -trimpath -buildvcs=false -ldflags='-s -w' -o <tmp>/dnsbench .)"

else

  (
    cd "$SRC_DIR"

    export CGO_ENABLED=1
    export GOTOOLCHAIN=local
    export GOFLAGS=-mod=readonly
    export GOPROXY=off
    export GOCACHE="$BUILD_DIR/gocache"
    export GOPATH="$BUILD_DIR/gopath"

    "$GO_BIN" build \
      -trimpath \
      -buildvcs=false \
      -ldflags='-s -w' \
      -o "$NEW_BIN" .
  ) || die "Build failed. The messages above say why; nothing has been changed on this system."

  built="$("$NEW_BIN" --version 2>/dev/null | awk '{print $2}')" || built=""

  [[ "$built" == "$NEW_VERSION" ]] ||
    die "The built binary reports version '$built', expected '$NEW_VERSION'."

  ldd_out="$(ldd "$NEW_BIN" 2>&1 || true)"

  [[ "$ldd_out" == *libpam* ]] ||
    die "The built binary is not linked against PAM; refusing to install a build nobody could log in to."

  ok "Build complete"
fi

# ── Existing installation ─────────────────────────────────────────────────────
OLD_VERSION=""
LEGACY=0

if [[ -x "$BIN" ]]; then
  old_out=""

  if command -v timeout >/dev/null 2>&1; then
    old_out="$(
      timeout 5 "$BIN" --version 2>/dev/null </dev/null |
        head -n1 || true
    )"
  fi

  if [[ "$old_out" =~ ^dnsbench\ ([0-9]+)$ ]]; then
    OLD_VERSION="${BASH_REMATCH[1]}"
  fi

  if [[ -z "$OLD_VERSION" ]]; then
    LEGACY=1
    info "Found a previous (pre-Go) dnsbench installation; it will be replaced"

  elif [[ "$OLD_VERSION" =~ ^[0-9]+$ ]]; then

    if (( OLD_VERSION > NEW_VERSION )) &&
       [[ $ALLOW_DOWNGRADE -eq 0 ]]; then
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

if [[ $USE_SYSTEMD -eq 1 ]] &&
   "$SYSTEMCTL" is-active --quiet dnsbench 2>/dev/null; then

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
  if [[ -f "$SCRIPT_DIR/$doc" ]]; then
    install -m 644 "$SCRIPT_DIR/$doc" "$INSTALL_DIR/$doc"
  else
    warn "$doc not found; the in-app page will say so"
  fi
done

if command -v getenforce >/dev/null 2>&1 &&
   [[ "$(getenforce 2>/dev/null)" == "Enforcing" ]] &&
   command -v chcon >/dev/null 2>&1; then

  chcon -t bin_t "$BIN" 2>/dev/null ||
    warn "Could not set the SELinux label on $BIN"
fi

ok "Binary installed: $BIN"

rollback() {
  warn "Rolling back to the previous version"

  if [[ -n "$PREV" && -f "$PREV" ]]; then
    mv -f "$PREV" "$BIN"

    if [[ $WAS_ACTIVE -eq 1 ]]; then
      "$SYSTEMCTL" restart dnsbench || true
    fi

    warn "The previous version has been restored."

  else
    warn "There was no previous version to restore."
  fi
}

mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR"

mkdir -p "$CONF_DIR"

write_fresh_config() {
  local host="${1:-}"
  local port="${2:-}"

  install -m 640 "$CONTRIB/dnsbench.conf" "$CONF_FILE"

  if [[ -n "$host" ]]; then
    sed -i "s|^LISTEN_HOST=.*|LISTEN_HOST=$host|" "$CONF_FILE"
  fi

  if [[ -n "$port" ]]; then
    sed -i "s|^LISTEN_PORT=.*|LISTEN_PORT=$port|" "$CONF_FILE"
  fi

  return 0
}

conf_value() {
  sed -n "s/^$1=//p" "$CONF_FILE" | head -n1
}

if [[ ! -f "$CONF_FILE" ]]; then

  write_fresh_config
  ok "Config written: $CONF_FILE"

elif grep -qE '^(TDNS_API|TLS_PFX|TLS_PFX_PASS|SECRET_KEY|LOCAL_USER|LOCAL_PASS)=' "$CONF_FILE"; then

  keep_host="$(conf_value LISTEN_HOST)"
  keep_port="$(conf_value LISTEN_PORT)"

  if [[ "$keep_port" == "5353" ]]; then
    keep_port=8453
  fi

  cp -p "$CONF_FILE" "$CONF_FILE.pre-go"
  write_fresh_config "$keep_host" "$keep_port"

  ok "Old-format config replaced; original is at $CONF_FILE.pre-go"

else

  info "Config kept: $CONF_FILE"

fi

if [[ -f "$CONF_DIR/schedules.json" &&
      ! -f "$STATE_DIR/schedules.json" ]]; then

  cp -p "$CONF_DIR/schedules.json" "$STATE_DIR/schedules.json"
  ok "Copied your saved schedules to $STATE_DIR/schedules.json"
fi

LOGIN_GROUP="$(conf_value LOGIN_GROUP)"
LOGIN_GROUP="${LOGIN_GROUP:-dnsbench}"

GROUP_MARKER="$CONF_DIR/.created-login-group"
added_user=""

if [[ "$LOGIN_GROUP" != "dnsbench" ]]; then

  info "Login group is '$LOGIN_GROUP'"

else

  if getent group dnsbench >/dev/null 2>&1; then

    info "Group 'dnsbench' already exists"

  else

    if command -v groupadd >/dev/null 2>&1; then
      groupadd --system dnsbench

    elif command -v addgroup >/dev/null 2>&1; then
      addgroup --system dnsbench

    else
      die "Cannot create 'dnsbench' group."
    fi

    : > "$GROUP_MARKER"
    ok "Created group 'dnsbench'"
  fi

  if [[ -n "$INSTALL_USER" ]]; then

    user_groups=" $(id -nG "$INSTALL_USER" 2>/dev/null || true) "

    if [[ "$user_groups" == *" dnsbench "* ]]; then

      info "'$INSTALL_USER' is already in group 'dnsbench'"

    else

      if command -v usermod >/dev/null 2>&1; then
        usermod -aG dnsbench "$INSTALL_USER"

      elif command -v gpasswd >/dev/null 2>&1; then
        gpasswd -a "$INSTALL_USER" dnsbench >/dev/null

      else
        die "Cannot add user to group."
      fi

      ok "Added '$INSTALL_USER' to group 'dnsbench'"
    fi

    added_user="$INSTALL_USER"
  fi
fi

if [[ ! -f "$PAM_FILE" ]]; then

  mkdir -p "$PAM_DIR"
  install -m 644 "$CONTRIB/pam.d/dnsbench" "$PAM_FILE"

  ok "PAM service installed: $PAM_FILE"

else

  info "PAM service kept: $PAM_FILE"

fi

if [[ $USE_SYSTEMD -eq 1 ]]; then

  mkdir -p "$UNIT_DIR"

  install -m 644 "$CONTRIB/dnsbench.service" "$UNIT_FILE"

  "$SYSTEMCTL" daemon-reload
  "$SYSTEMCTL" enable dnsbench >/dev/null 2>&1 ||
    warn "Could not enable service at boot"

  ok "Service installed: $UNIT_FILE"

  if [[ $NO_START -eq 1 ]]; then

    info "Not starting service (--no-start)"
    rm -f "$PREV"

  else

    "$SYSTEMCTL" restart dnsbench || true

    healthy=0

    for ((i = 0; i < SETTLE * 2 + 1; i++)); do
      sleep 0.5

      if "$SYSTEMCTL" is-active --quiet dnsbench; then
        healthy=1
      else
        healthy=0
      fi
    done

    if [[ $healthy -eq 1 ]] &&
       command -v curl >/dev/null 2>&1; then

      port="$(conf_value LISTEN_PORT)"
      port="${port:-8453}"

      scheme=https

      if [[ "$(conf_value NO_TLS)" == "true" ]]; then
        scheme=http
      fi

      healthy=0

      for ((i = 0; i < 20; i++)); do

        if curl -sk \
          -o /dev/null \
          --max-time 2 \
          "$scheme://127.0.0.1:$port/login"; then

          healthy=1
          break
        fi

        sleep 0.5
      done
    fi

    if [[ $healthy -eq 1 ]]; then

      ok "dnsbench is running"
      rm -f "$PREV"

    else

      warn "Service did not start cleanly"
      rollback
      die "Installation failed and was rolled back."

    fi
  fi

else

  warn "systemd not detected."
  rm -f "$PREV"

fi

PORT="$(conf_value LISTEN_PORT)"
PORT="${PORT:-8453}"

HOST="$(hostname -f 2>/dev/null || hostname)"

scheme=https

if [[ "$(conf_value NO_TLS)" == "true" ]]; then
  scheme=http
fi

echo
echo "${GREEN}Installation complete (version $NEW_VERSION).${NC}"
echo
echo "  Open:     $scheme://$HOST:$PORT"
echo "  Config:   $CONF_FILE"
echo "  Service:  systemctl {start|stop|restart|status} dnsbench"

exit 0
