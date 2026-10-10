#!/usr/bin/env bash
# Anyname DNS Director installer / upgrader.
#
# Builds Anyname from the source tree this script sits in and installs it as a
# systemd service. Supports Ubuntu, Debian, Fedora, RHEL, Rocky, Alma, Arch and
# Manjaro (and derivatives that declare them in ID_LIKE).  If Anyname is already
# installed it is upgraded in place: config, certificate, group membership and
# PAM service file are kept, the previous binary is saved, and the upgrade is
# rolled back automatically if the new version fails to start.
#
#   sudo ./install.sh [options]        see --help
#
# Test hooks (not for normal use): DDGW_OS_RELEASE, DDGW_SYSTEMCTL,
# DDGW_FORCE_SYSTEMD, DDGW_ASSUME_MISSING, DDGW_FORCE_GO_DOWNLOAD,
# DDGW_GO_BASE_URL, DDGW_SETTLE.

set -euo pipefail
umask 022

# ── fixed layout (the daemon's own defaults) ─────────────────────────────────
BIN_DIR=/usr/local/sbin
BIN=$BIN_DIR/ddgw
SHARE=/usr/local/share/ddgw
MANIFEST=$SHARE/install.manifest
LEGACY_CONF_DIR=/etc/liras   # where releases before v15 kept the config
UNIT_NAME=ddgw.service
UNIT=/etc/systemd/system/$UNIT_NAME
PAM_FILE=/etc/pam.d/ddgw
NM_FILE=/etc/NetworkManager/conf.d/90-ddgw-unmanaged.conf
GROUP=ddgw
GUI_PORT=53853
CLUSTER_PORT=53854
STATE_DIR=/var/lib/ddgw
CONF_DIR=$STATE_DIR   # the config lives with everything else ddgw keeps

GO_MIN=1.24
GO_VERSION=1.24.7
GO_BASE=${DDGW_GO_BASE_URL:-https://go.dev/dl}
SYSTEMCTL=${DDGW_SYSTEMCTL:-systemctl}
OS_RELEASE=${DDGW_OS_RELEASE:-/etc/os-release}
SETTLE=${DDGW_SETTLE:-3}

SRC=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# ── output ───────────────────────────────────────────────────────────────────
if [ -t 1 ]; then B=$'\033[1m'; R=$'\033[31m'; Y=$'\033[33m'; G=$'\033[32m'; N=$'\033[0m'; else B=; R=; Y=; G=; N=; fi
step() { printf '%s==>%s %s%s%s\n' "$G" "$N" "$B" "$*" "$N"; }
info() { printf '    %s\n' "$*"; }
warn() { printf '%swarning:%s %s\n' "$Y" "$N" "$*" >&2; }
die()  { printf '%serror:%s %s\n' "$R" "$N" "$*" >&2; exit 1; }

usage() {
  cat <<EOF
Anyname DNS Director installer

Usage: sudo ./install.sh [options]

  --add-user NAME   add NAME to the '$GROUP' group so they can log in to the
                    web GUI (repeatable). The user running the installer
                    (the one behind sudo) is always added.
  --no-start        install and enable the service but do not start/restart it
  --force           reinstall even if this version is already installed, and
                    allow a downgrade
  --skip-deps       don't install build prerequisites (you provide gcc, the PAM
                    development headers, iproute2, FRR and Go >= $GO_MIN)
  --go PATH         use this 'go' binary instead of searching / downloading
  --no-keep-go      remove a downloaded Go toolchain after the build. By default
                    it is kept in $SHARE/go because the daemon rebuilds
                    itself with it when you apply an update from the GUI/CLI.
  -y, --yes         don't ask questions
  --dry-run         show what would be done, change nothing
  -h, --help        this help

Supported: Ubuntu, Debian, Fedora, RHEL, Rocky, Alma, Arch, Manjaro.
EOF
}

# ── arguments ────────────────────────────────────────────────────────────────
ADD_USERS=(); KEEP_GO=1; NO_START=0; FORCE=0; SKIP_DEPS=0; ASSUME_YES=0; DRY=0; OPT_GO=""
while [ $# -gt 0 ]; do
  case $1 in
    --add-user)    [ $# -ge 2 ] || die "--add-user needs a user name"; ADD_USERS+=("$2"); shift 2;;
    --add-user=*)  ADD_USERS+=("${1#*=}"); shift;;
    --no-start)    NO_START=1; shift;;
    --no-keep-go)  KEEP_GO=0; shift;;
    --force)       FORCE=1; shift;;
    --skip-deps)   SKIP_DEPS=1; shift;;
    --go)          [ $# -ge 2 ] || die "--go needs a path"; OPT_GO=$2; shift 2;;
    --go=*)        OPT_GO=${1#*=}; shift;;
    -y|--yes)      ASSUME_YES=1; shift;;
    --dry-run)     DRY=1; shift;;
    -h|--help)     usage; exit 0;;
    *)             usage >&2; die "unknown option: $1";;
  esac
done

# ── helpers ──────────────────────────────────────────────────────────────────
have() { command -v "$1" >/dev/null 2>&1; }

# run a state-changing command (printed, not executed, under --dry-run)
run() {
  if [ "$DRY" = 1 ]; then printf '    [dry-run] %s\n' "$*"; return 0; fi
  "$@"
}

confirm() { # confirm "question" -> 0 if yes; never prompts without a terminal
  [ "$ASSUME_YES" = 1 ] && return 0
  [ -t 0 ] || return 1
  local a; read -r -p "    $1 [y/N] " a || return 1
  [[ $a =~ ^[Yy] ]]
}

# write_file PATH MODE  (content on stdin); only touches the file if it changed.
# Echoes "created", "updated" or "unchanged".
write_file() {
  local path=$1 mode=$2 tmp
  tmp=$(mktemp "$path.XXXXXX.tmp" 2>/dev/null || mktemp)
  cat >"$tmp"
  if [ -f "$path" ] && cmp -s "$tmp" "$path"; then
    rm -f "$tmp"; echo unchanged; return 0
  fi
  local what=created; [ -f "$path" ] && what=updated
  if [ "$DRY" = 1 ]; then rm -f "$tmp"; echo "$what"; return 0; fi
  chmod "$mode" "$tmp"
  mv -f "$tmp" "$path"
  echo "$what"
}

sha() { sha256sum "$1" | awk '{print $1}'; }

# Treat a tool/header as missing when the test hook says so.
assumed_missing() { [[ ",${DDGW_ASSUME_MISSING:-}," == *",$1,"* ]]; }

# ── root ─────────────────────────────────────────────────────────────────────
if [ "$(id -u)" -ne 0 ] && [ "$DRY" = 0 ]; then
  if have sudo; then
    info "re-running with sudo…"
    exec sudo -E bash "${BASH_SOURCE[0]}" "$@"
  fi
  die "must be run as root (sudo ./install.sh)"
fi

[ -f "$SRC/source/go.mod" ] && [ -f "$SRC/source/VERSION" ] && [ -f "$SRC/source/main.go" ] ||
  die "run this script from the top of the Anyname tree (source/go.mod, source/VERSION and source/main.go not found next to it)"
NEW_VER=$(tr -d '[:space:]' <"$SRC/source/VERSION")
[[ $NEW_VER =~ ^[0-9]+$ ]] || die "bad VERSION file: '$NEW_VER'"

# ── distro ───────────────────────────────────────────────────────────────────
detect_family() {
  [ -r "$OS_RELEASE" ] || die "cannot read $OS_RELEASE — unsupported system"
  local id like name
  # shellcheck disable=SC1090
  eval "$(. "$OS_RELEASE"; printf 'id=%q like=%q name=%q' "${ID:-}" "${ID_LIKE:-}" "${PRETTY_NAME:-${ID:-linux}}")"
  OS_NAME=$name
  local tok
  for tok in $id $like; do
    case $tok in
      ubuntu|debian|linuxmint|pop|raspbian|kali|elementary|zorin|neon) FAMILY=debian; return;;
      fedora)                                                           FAMILY=fedora; return;;
      rhel|centos|rocky|almalinux|ol)                                   FAMILY=rhel;   return;;
      arch|manjaro|endeavouros|garuda|cachyos)                          FAMILY=arch;   return;;
    esac
  done
  die "unsupported distribution '$name' (ID=$id ID_LIKE=$like). Supported: Ubuntu, Debian, Fedora, RHEL, Rocky, Alma, Arch, Manjaro."
}
detect_family
case $FAMILY in
  debian) PM=apt;;
  fedora|rhel) if have dnf; then PM=dnf; elif have yum; then PM=yum; else die "neither dnf nor yum found"; fi;;
  arch)   PM=pacman;;
esac

if { [ -d /run/systemd/system ] && have "$SYSTEMCTL"; } || [ -n "${DDGW_FORCE_SYSTEMD:-}" ]; then
  HAVE_SYSTEMD=1
else
  HAVE_SYSTEMD=0
fi

step "Anyname v$NEW_VER on $OS_NAME ($FAMILY family, package manager: $PM)"
[ "$DRY" = 1 ] && info "dry run: nothing will be changed"

# ── existing installation? ───────────────────────────────────────────────────
OLD_VER=""; MODE=fresh
if [ -x "$BIN" ]; then
  OLD_VER=$("$BIN" --version 2>/dev/null | sed -n 's/^ddgw v\([0-9][0-9]*\).*/\1/p' | head -1 || true)
  OLD_VER=${OLD_VER:-0}   # present but too old to report a version
fi
if [ -n "$OLD_VER" ]; then
  if [ "$OLD_VER" -lt "$NEW_VER" ]; then
    MODE=upgrade; info "installed: v$OLD_VER → upgrading to v$NEW_VER"
  elif [ "$OLD_VER" -eq "$NEW_VER" ] && [ "$FORCE" = 0 ]; then
    info "Anyname v$OLD_VER is already installed and up to date (use --force to reinstall/repair)."
    exit 0
  elif [ "$OLD_VER" -gt "$NEW_VER" ] && [ "$FORCE" = 0 ]; then
    die "installed v$OLD_VER is newer than this source (v$NEW_VER); use --force to downgrade"
  else
    MODE=reinstall; info "installed: v$OLD_VER — reinstalling v$NEW_VER (--force)"
  fi
else
  info "no existing installation found: fresh install"
fi
OTHER=$(command -v ddgw 2>/dev/null || true)
if [ -n "$OTHER" ] && [ "$OTHER" != "$BIN" ]; then
  warn "another ddgw exists at $OTHER and may shadow $BIN in PATH"
fi

# values remembered from a previous install (so upgrades keep them)
GROUP_CREATED=0; PAM_CREATED=0; NM_CREATED=0
if [ -f "$MANIFEST" ]; then
  # shellcheck disable=SC1090
  GROUP_CREATED=$( . "$MANIFEST"; echo "${GROUP_CREATED:-0}" )
  # shellcheck disable=SC1090
  PAM_CREATED=$( . "$MANIFEST"; echo "${PAM_CREATED:-0}" )
  # shellcheck disable=SC1090
  NM_CREATED=$( . "$MANIFEST"; echo "${NM_CREATED:-0}" )
fi

WAS_ACTIVE=0
if [ "$HAVE_SYSTEMD" = 1 ]; then
  "$SYSTEMCTL" is-active  --quiet "$UNIT_NAME" 2>/dev/null && WAS_ACTIVE=1  || true
fi

# ── build prerequisites ──────────────────────────────────────────────────────
TMP=""
cleanup() { [ -n "$TMP" ] && rm -rf "$TMP"; }
trap cleanup EXIT
TMP=$(mktemp -d /var/tmp/ddgw-build.XXXXXX)

pkg_install() {
  [ $# -gt 0 ] || return 0
  case $PM in
    apt)
      run env DEBIAN_FRONTEND=noninteractive apt-get update -qq
      run env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@";;
    dnf|yum)
      if ! run "$PM" install -y "$@"; then
        warn "install failed; the PAM headers can live in a disabled repo (CRB/PowerTools/CodeReady). Trying to enable it…"
        enable_crb
        run "$PM" install -y "$@"
      fi;;
    pacman)
      run pacman -S --needed --noconfirm "$@" ||
        die "pacman could not install: $* — if the package database is stale run 'pacman -Syu' first, then re-run this script";;
  esac
}

enable_crb() {
  local r
  if have dnf; then
    run dnf install -y dnf-plugins-core >/dev/null 2>&1 || true
    for r in crb powertools; do
      if run dnf config-manager --set-enabled "$r" >/dev/null 2>&1; then info "enabled repo $r"; return 0; fi
    done
  fi
  if have subscription-manager; then
    local rel arch; rel=$(rpm -E %rhel 2>/dev/null || echo 9); arch=$(uname -m)
    run subscription-manager repos --enable "codeready-builder-for-rhel-${rel}-${arch}-rpms" >/dev/null 2>&1 || true
  fi
  return 0
}

install_deps() {
  local pkgs=()
  if assumed_missing gcc || ! { have gcc || have cc; }; then
    case $FAMILY in debian) pkgs+=(gcc libc6-dev);; *) pkgs+=(gcc);; esac
  elif [ "$FAMILY" = debian ] && [ ! -f /usr/include/stdio.h ]; then
    pkgs+=(libc6-dev)
  fi
  if assumed_missing pam || [ ! -f /usr/include/security/pam_appl.h ]; then
    case $FAMILY in
      debian) pkgs+=(libpam0g-dev);;
      fedora|rhel) pkgs+=(pam-devel);;
      arch) pkgs+=(pam);;
    esac
  fi
  if assumed_missing ip || ! have ip; then
    case $FAMILY in debian|arch) pkgs+=(iproute2);; *) pkgs+=(iproute);; esac
  fi
  if assumed_missing tar || ! have tar; then pkgs+=(tar gzip); fi
  if [ "$NEED_DOWNLOAD" = 1 ] && ! { have curl || have wget; }; then
    pkgs+=(curl ca-certificates)
  fi
  if [ ${#pkgs[@]} -eq 0 ]; then info "build prerequisites already present"; return 0; fi
  info "installing: ${pkgs[*]}"
  pkg_install "${pkgs[@]}"
  if [ "$DRY" = 0 ]; then
    { have gcc || have cc; }                        || die "no C compiler after installing prerequisites"
    [ -f /usr/include/security/pam_appl.h ]         || die "PAM development headers still missing (package for $FAMILY: see README)"
    have ip                                         || die "'ip' (iproute2) still missing"
  fi
}

# FRR is what the BGP page configures (anycast addresses announced to routers).
# It is installed here so the page works at once, but ddgw does not touch FRR
# until a local AS number is set, so an install on a host that never uses BGP is
# unchanged.  A failure to install it is only a warning: everything else works.
install_frr() {
  if ! assumed_missing frr && { have vtysh || [ -x /usr/lib/frr/bgpd ]; }; then
    info "FRR already present"
  else
    info "installing FRR (used by the BGP page; idle until a local AS is set there)"
    if [ "$FAMILY" = rhel ] && [ "$DRY" = 0 ] && have dnf; then
      # on the RHEL family FRR comes from EPEL
      dnf list --available frr >/dev/null 2>&1 || run dnf install -y epel-release >/dev/null 2>&1 || true
    elif [ "$FAMILY" = rhel ]; then
      info "(the RHEL family gets FRR from EPEL: epel-release is installed first when frr is not available)"
    fi
    if ! (pkg_install frr); then
      warn "FRR could not be installed. Everything else works; install the 'frr' package yourself before using Configure → BGP."
      return 0
    fi
    [ "$DRY" = 1 ] || have vtysh || warn "FRR installed but 'vtysh' is not on the PATH"
  fi
  # FRR's reload (how ddgw applies a changed BGP setting without dropping the sessions)
  # needs frr-reload.py, which Debian and the RHEL family ship in a separate package;
  # on Arch it is part of frr.  Without it ddgw has to restart FRR for every change.
  if [ "$FAMILY" != arch ] && { assumed_missing frr-reload || [ ! -e /usr/lib/frr/frr-reload.py ]; }; then
    info "installing frr-pythontools (lets FRR reload a changed config without dropping BGP sessions)"
    (pkg_install frr-pythontools) || warn "frr-pythontools could not be installed; Anyname will restart FRR (briefly dropping BGP sessions) when the BGP setting changes."
  fi
  return 0
}

# nmap runs the "Scan" of a client on the Statistics page.  Only that feature needs it, so a failure is a warning.
install_nmap() {
  if ! assumed_missing nmap && have nmap; then
    info "nmap already present"
    return 0
  fi
  info "installing nmap (Statistics ▸ right-click a client ▸ Scan)"
  (pkg_install nmap) || warn "nmap could not be installed. Everything else works; install the 'nmap' package yourself before using Scan."
  return 0
}

# ── Go toolchain (>= $GO_MIN) ────────────────────────────────────────────────
go_ok() {
  local bin=$1 v
  [ -x "$bin" ] || return 1
  v=$("$bin" version 2>/dev/null | awk '{print $3}' | sed 's/^go//')
  [[ $v =~ ^[0-9]+\.[0-9]+ ]] || return 1
  [ "$(printf '%s\n%s\n' "$GO_MIN" "$v" | sort -V | head -1)" = "$GO_MIN" ]
}

find_go() {
  local c
  if [ -n "$OPT_GO" ]; then
    go_ok "$OPT_GO" || die "$OPT_GO is not a Go >= $GO_MIN"
    GO=$OPT_GO; return 0
  fi
  [ -n "${DDGW_FORCE_GO_DOWNLOAD:-}" ] && return 1
  for c in "$(command -v go 2>/dev/null || true)" /usr/local/go/bin/go /usr/lib/go/bin/go /usr/lib/golang/bin/go /snap/bin/go /usr/lib/go-*/bin/go "$SHARE/go/bin/go"; do
    [ -n "$c" ] && go_ok "$c" && { GO=$c; return 0; }
  done
  return 1
}

fetch() { # fetch URL DEST
  if have curl; then curl -fsSL --retry 3 -o "$2" "$1"; else wget -q -O "$2" "$1"; fi
}

download_go() {
  local arch file
  case $(uname -m) in
    x86_64|amd64)   arch=amd64;;
    aarch64|arm64)  arch=arm64;;
    armv7l|armv6l)  arch=armv6l;;
    i?86)           arch=386;;
    riscv64)        arch=riscv64;;
    *) die "no Go toolchain for $(uname -m); install Go >= $GO_MIN yourself and re-run with --go /path/to/go";;
  esac
  file=go${GO_VERSION}.linux-${arch}.tar.gz
  info "no Go >= $GO_MIN on this system; fetching the official go$GO_VERSION toolchain (kept in $SHARE/go for in-place updates unless --no-keep-go)"
  have curl || have wget || die "need curl or wget to download Go (or pass --go PATH)"
  if [ "$DRY" = 1 ]; then info "[dry-run] download $GO_BASE/$file and verify its sha256"; GO=$TMP/goroot/go/bin/go; return 0; fi
  fetch "$GO_BASE/$file" "$TMP/$file"        || die "could not download $GO_BASE/$file"
  fetch "$GO_BASE/$file.sha256" "$TMP/$file.sha256" || die "could not download the checksum $GO_BASE/$file.sha256"
  local want got
  want=$(awk '{print $1; exit}' "$TMP/$file.sha256"); got=$(sha "$TMP/$file")
  [ -n "$want" ] && [ "$want" = "$got" ] || die "checksum mismatch for $file (wanted '$want', got '$got') — refusing to use it"
  mkdir -p "$TMP/goroot"
  tar -xzf "$TMP/$file" -C "$TMP/goroot"
  GO=$TMP/goroot/go/bin/go
  go_ok "$GO" || die "downloaded toolchain does not run on this system"
}

# Prefer the distribution's own Go package when it is recent enough; the
# official tarball (below) is the fallback for distros that ship an older Go.
pkg_version_ok() { # pkg_version_ok VERSION-STRING -> 0 if >= GO_MIN
  local v=${1#*:}; v=${v%%[~+-]*}
  [[ $v =~ ^[0-9]+\.[0-9]+ ]] && [ "$(printf '%s\n%s\n' "$GO_MIN" "$v" | sort -V | head -1)" = "$GO_MIN" ]
}
try_distro_go() {
  local p cand
  [ -z "${DDGW_FORCE_GO_DOWNLOAD:-}" ] || return 1
  case $FAMILY in
    debian) set -- golang-1.25-go golang-1.24-go golang-go;;
    fedora|rhel) set -- golang;;
    arch) set -- go;;
    *) return 1;;
  esac
  for p in "$@"; do
    if [ "$FAMILY" = debian ] && [ "$DRY" = 0 ]; then
      cand=$(apt-cache policy "$p" 2>/dev/null | awk '/Candidate:/{print $2}')
      { [ -n "$cand" ] && [ "$cand" != "(none)" ] && pkg_version_ok "$cand"; } || continue
    fi
    info "installing Go from the distribution package: $p"
    if (pkg_install "$p") && { [ "$DRY" = 1 ] || find_go; }; then return 0; fi
    info "package $p did not give a usable Go >= $GO_MIN"
  done
  return 1
}

GO=""; NEED_DOWNLOAD=0
if [ "$SKIP_DEPS" = 0 ] || [ -z "$OPT_GO" ]; then
  find_go >/dev/null 2>&1 || NEED_DOWNLOAD=1
fi
step "Build prerequisites"
if [ "$SKIP_DEPS" = 1 ]; then
  info "skipped (--skip-deps)"
  { have gcc || have cc; } || die "--skip-deps given but no C compiler found"
  [ -f /usr/include/security/pam_appl.h ] || die "--skip-deps given but PAM headers (security/pam_appl.h) are missing"
else
  install_deps
  install_frr
  install_nmap
fi
step "Go toolchain"
if find_go; then info "using $GO ($("$GO" version | awk '{print $3}'))"
elif [ "$SKIP_DEPS" = 0 ] && [ -z "$OPT_GO" ] && try_distro_go && [ "$DRY" = 0 ]; then
  find_go; info "using $GO ($("$GO" version | awk '{print $3}'))"
else
  [ "$DRY" = 1 ] && [ "$SKIP_DEPS" = 0 ] && [ -z "$OPT_GO" ] && [ -z "${DDGW_FORCE_GO_DOWNLOAD:-}" ] && info "[dry-run] (if the package gives no Go >= $GO_MIN, the official toolchain is downloaded instead)"
  download_go
fi

# ── build (before touching the installed system) ─────────────────────────────
step "Building Anyname v$NEW_VER (with PAM)"
NEWBIN=$TMP/ddgw
if [ "$DRY" = 1 ]; then
  info "[dry-run] CGO_ENABLED=1 $GO build -trimpath -o ddgw ."
else
  ( cd "$SRC/source" &&
    GOTOOLCHAIN=local CGO_ENABLED=1 GOCACHE="$TMP/gocache" GOPATH="$TMP/gopath" GOFLAGS=-mod=mod \
    "$GO" build -trimpath -ldflags='-s -w' -o "$NEWBIN" . ) ||
    die "build failed — the installed version (if any) was not touched"
  grep -aq 'libpam\.so' "$NEWBIN" || die "built binary has no PAM support (cgo disabled?) — web GUI logins would not work"
  [ "$("$NEWBIN" --version)" = "ddgw v$NEW_VER" ] || die "built binary reports '$("$NEWBIN" --version)', expected 'ddgw v$NEW_VER'"
  info "built $(du -h "$NEWBIN" | cut -f1) binary, PAM linked"
fi

# ── install files ────────────────────────────────────────────────────────────
step "Installing files"
run mkdir -p "$BIN_DIR" "$SHARE"
run install -d -m 0700 "$STATE_DIR"   # config, history, cluster identity, certificates, update data

# an install from before v15 kept its config in /etc/liras: move it over (never
# overwrite a config that is already here; each old file is removed once its copy
# is in place, and /etc/liras goes when it is empty)
if [ -f "$LEGACY_CONF_DIR/ddgw.conf" ] && [ ! -f "$CONF_DIR/ddgw.conf" ]; then
  if [ "$DRY" = 1 ]; then
    info "[dry-run] move $LEGACY_CONF_DIR/ddgw.conf (and ddgw-web.crt/.key) to $CONF_DIR"
  else
    for f in ddgw.conf ddgw-web.crt ddgw-web.key; do
      if [ -f "$LEGACY_CONF_DIR/$f" ] && [ ! -e "$CONF_DIR/$f" ]; then
        cp -p "$LEGACY_CONF_DIR/$f" "$CONF_DIR/$f" && chmod 0600 "$CONF_DIR/$f" &&
          cmp -s "$LEGACY_CONF_DIR/$f" "$CONF_DIR/$f" && rm -f "$LEGACY_CONF_DIR/$f"
      fi
    done
    rmdir "$LEGACY_CONF_DIR" 2>/dev/null || true
    info "moved your configuration from $LEGACY_CONF_DIR to $CONF_DIR"
  fi
fi
run rm -f /run/liras/ddgw.sock   # control socket of an older release

# keep the previous binary for rollback, then replace atomically
HAD_BIN=0
if [ -x "$BIN" ]; then
  HAD_BIN=1
  run cp -p "$BIN" "$SHARE/ddgw.prev"
  info "previous binary saved to $SHARE/ddgw.prev"
fi
run install -m 0755 "$NEWBIN" "$BIN.new"
run mv -f "$BIN.new" "$BIN"
info "installed $BIN"

# keep a downloaded Go: in-place updates (GUI/CLI) compile with it
GO_KEPT=0
if [ "${GO#"$TMP"/}" != "$GO" ]; then
  if [ "$KEEP_GO" = 1 ]; then
    if [ "$DRY" = 1 ]; then info "[dry-run] keep the downloaded Go in $SHARE/go"; else
      rm -rf "$SHARE/go.new" && mv "$TMP/goroot/go" "$SHARE/go.new" && rm -rf "$SHARE/go" && mv "$SHARE/go.new" "$SHARE/go"
      GO_KEPT=1; info "kept the Go toolchain in $SHARE/go (used when applying updates; --no-keep-go to skip)"
    fi
  else
    info "downloaded Go not kept (--no-keep-go): applying updates from the GUI/CLI needs a Go >= $GO_MIN on this node"
  fi
elif [ "$GO" = "$SHARE/go/bin/go" ]; then
  GO_KEPT=1
fi

# docs, examples, and the uninstaller (so removal works without the source tree)
run install -m 0644 "$SRC/source/ddgw.conf.example" "$SHARE/ddgw.conf.example"
for f in README.md CHANGELOG.md LICENSE; do
  src="$SRC/docs/$f"; [ "$f" = CHANGELOG.md ] || src="$SRC/$f"
  [ -f "$src" ] && run install -m 0644 "$src" "$SHARE/$f"
done
run install -m 0644 "$SRC/source/VERSION" "$SHARE/VERSION"
if [ -f "$SRC/uninstall.sh" ]; then
  run install -m 0755 "$SRC/uninstall.sh" "$SHARE/uninstall.sh"
  run ln -sf "$SHARE/uninstall.sh" "$BIN_DIR/ddgw-uninstall"
fi

# group
step "Group '$GROUP' (members may log in to the web GUI)"
if getent group "$GROUP" >/dev/null 2>&1; then
  info "group exists"
else
  run groupadd --system "$GROUP"
  GROUP_CREATED=1
  info "created group $GROUP"
fi

for u in ${ADD_USERS[@]+"${ADD_USERS[@]}"}; do
  if id -u "$u" >/dev/null 2>&1; then
    run usermod -aG "$GROUP" "$u"; info "added $u to $GROUP"
  else
    warn "no such user '$u' — not added"
  fi
done

# PAM service
step "PAM service $PAM_FILE"
case $FAMILY in
  debian)
    PAM_STACK=common-auth
    PAM_BODY='# /etc/pam.d/ddgw — installed by ddgw install.sh
# Password check for the ddgw web GUI. Group membership is enforced by ddgw.
@include common-auth
@include common-account';;
  fedora|rhel)
    PAM_STACK=system-auth
    PAM_BODY='#%PAM-1.0
# /etc/pam.d/ddgw — installed by ddgw install.sh
# Password check for the ddgw web GUI. Group membership is enforced by ddgw.
auth       substack     system-auth
account    include      system-auth';;
  arch)
    PAM_STACK=system-auth
    PAM_BODY='#%PAM-1.0
# /etc/pam.d/ddgw — installed by ddgw install.sh
# Password check for the ddgw web GUI. Group membership is enforced by ddgw.
auth       include      system-auth
account    include      system-auth';;
esac
if [ -f "$PAM_FILE" ]; then
  if [ "$PAM_CREATED" = 1 ] && [ "$(printf '%s\n' "$PAM_BODY" | sha256sum | awk '{print $1}')" != "$(sha "$PAM_FILE")" ]; then
    info "exists and has been customised — left untouched"
  else
    info "exists — left untouched"
  fi
else
  printf '%s\n' "$PAM_BODY" | write_file "$PAM_FILE" 0644 >/dev/null
  PAM_CREATED=1
  info "created (includes the system $PAM_STACK stack)"
fi
[ "$DRY" = 1 ] || [ -f "/etc/pam.d/$PAM_STACK" ] ||
  warn "/etc/pam.d/$PAM_STACK not found — web logins will fail until $PAM_FILE points at a valid stack"

# NetworkManager: keep it away from the virtual MAC interfaces
if [ -d /etc/NetworkManager ] && { [ "$HAVE_SYSTEMD" = 0 ] || "$SYSTEMCTL" is-active --quiet NetworkManager 2>/dev/null || [ -d /etc/NetworkManager/conf.d ]; }; then
  mkdir -p /etc/NetworkManager/conf.d 2>/dev/null || true
  res=$(printf '%s\n' '# installed by ddgw: the gateway creates macvlan interfaces named ddgw<group>.<slot>;' \
                      '# NetworkManager must not try to configure them.' \
                      '[keyfile]' 'unmanaged-devices=interface-name:ddgw*' | write_file "$NM_FILE" 0644)
  if [ "$res" != unchanged ]; then
    NM_CREATED=1; info "NetworkManager: marked ddgw* interfaces unmanaged ($res)"
    [ "$HAVE_SYSTEMD" = 1 ] && run "$SYSTEMCTL" reload NetworkManager >/dev/null 2>&1 || true
  fi
fi

# SELinux labels (RHEL family)
if have restorecon; then run restorecon -F "$BIN" "$PAM_FILE" "$SHARE" >/dev/null 2>&1 || true; fi

# ── systemd unit ─────────────────────────────────────────────────────────────
UNIT_BODY="[Unit]
Description=ddgw - DNS Distributed Gateway
Documentation=file://$SHARE/README.md
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart=$BIN --config $CONF_DIR/ddgw.conf
Restart=always
RestartSec=2
TimeoutStopSec=15
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target"

ROLLED_BACK=0
if [ "$HAVE_SYSTEMD" = 1 ]; then
  step "systemd service"
  res=$(printf '%s\n' "$UNIT_BODY" | write_file "$UNIT" 0644)
  info "unit $UNIT: $res"
  run "$SYSTEMCTL" daemon-reload
  run "$SYSTEMCTL" enable "$UNIT_NAME" >/dev/null 2>&1 || warn "could not enable $UNIT_NAME"

  start_wanted=1
  [ "$NO_START" = 1 ] && start_wanted=0
  # an upgrade keeps the service in the state it was found in
  if [ "$MODE" != fresh ] && [ "$WAS_ACTIVE" = 0 ]; then start_wanted=0; fi

  if [ "$start_wanted" = 1 ]; then
    if [ "$WAS_ACTIVE" = 1 ]; then run "$SYSTEMCTL" restart "$UNIT_NAME"; else run "$SYSTEMCTL" start "$UNIT_NAME"; fi
    if [ "$DRY" = 0 ]; then
      sleep "$SETTLE"
      if ! "$SYSTEMCTL" is-active --quiet "$UNIT_NAME"; then
        errmsg="ddgw v$NEW_VER failed to start"
        if [ "$HAD_BIN" = 1 ] && [ -x "$SHARE/ddgw.prev" ]; then
          warn "$errmsg — rolling back to the previous binary"
          cp -p "$SHARE/ddgw.prev" "$BIN.new" && mv -f "$BIN.new" "$BIN"
          [ "${OLD_VER:-0}" != 0 ] && echo "$OLD_VER" >"$SHARE/VERSION"
          "$SYSTEMCTL" restart "$UNIT_NAME" || true
          sleep "$SETTLE"
          ROLLED_BACK=1
          if "$SYSTEMCTL" is-active --quiet "$UNIT_NAME"; then
            die "$errmsg; the previous version was restored and is running. See: journalctl -u ddgw -n 50"
          fi
          die "$errmsg and the restored previous version is not running either. See: journalctl -u ddgw -n 50"
        fi
        die "$errmsg. See: journalctl -u ddgw -n 50"
      fi
      info "service is running"
    fi
  else
    info "service not (re)started"
  fi
else
  step "systemd"
  warn "systemd is not running here — unit file not installed. Start it yourself: $BIN --config $CONF_DIR/ddgw.conf"
fi

# ── manifest ─────────────────────────────────────────────────────────────────
if [ "$DRY" = 0 ]; then
  {
    echo "VERSION=$NEW_VER"
    echo "INSTALLED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "FAMILY=$FAMILY"
    echo "BIN=$BIN"
    echo "SHARE=$SHARE"
    echo "UNIT=$UNIT"
    echo "PAM_FILE=$PAM_FILE"
    echo "PAM_CREATED=$PAM_CREATED"
    echo "PAM_SHA256=$([ -f "$PAM_FILE" ] && sha "$PAM_FILE" || echo)"
    echo "NM_FILE=$NM_FILE"
    echo "NM_CREATED=$NM_CREATED"
    echo "GROUP=$GROUP"
    echo "GROUP_CREATED=$GROUP_CREATED"
    echo "CONF_DIR=$CONF_DIR"
    echo "STATE_DIR=$STATE_DIR"
    echo "GO_KEPT=$GO_KEPT"
  } >"$MANIFEST"
  chmod 0644 "$MANIFEST"
fi

# ── summary ──────────────────────────────────────────────────────────────────
step "Done: Anyname v$NEW_VER ($MODE)"
HOST=$(hostname -f 2>/dev/null || hostname)
if [ ! -f "$CONF_DIR/ddgw.conf" ]; then
  info "no config yet ($CONF_DIR/ddgw.conf): the daemon runs with no gateway groups until you create one."
  info "  - in the web GUI: https://$HOST:$GUI_PORT  → Configuration"
  info "  - or by hand: cp $SHARE/ddgw.conf.example $CONF_DIR/ddgw.conf && chmod 600 $CONF_DIR/ddgw.conf  (edits apply live)"
fi
# The user who ran the installer (the one behind sudo, or root itself) can log in to the GUI.
INVOKER="${SUDO_USER:-${DOAS_USER:-}}"
[ -n "$INVOKER" ] || INVOKER="$(id -un)"
if id -u "$INVOKER" >/dev/null 2>&1 && ! id -nG "$INVOKER" | tr ' ' '\n' | grep -qx "$GROUP"; then
  run usermod -aG "$GROUP" "$INVOKER"; info "added $INVOKER to $GROUP (can log in to the GUI)"
fi
if [ -z "$(getent group "$GROUP" | cut -d: -f4)" ] && [ "$DRY" != 1 ]; then
  info "'$GROUP' has no members yet, so nobody can log in to the GUI. Add someone:  usermod -aG $GROUP <user>"
fi
info "web GUI: https://$HOST:$GUI_PORT (self-signed certificate until you set web.cert_file/key_file)"
if have firewall-cmd && firewall-cmd --state >/dev/null 2>&1; then
  info "firewalld is active; open what you need, e.g.: firewall-cmd --permanent --add-port=$GUI_PORT/tcp --add-port=$CLUSTER_PORT/tcp --add-service=dns && firewall-cmd --reload"
elif have ufw && ufw status 2>/dev/null | grep -q '^Status: active'; then
  info "ufw is active; open what you need, e.g.: ufw allow $GUI_PORT/tcp && ufw allow $CLUSTER_PORT/tcp && ufw allow 53 && ufw allow 4729/udp"
else
  info "ports used: GUI tcp/$GUI_PORT, cluster tcp/$CLUSTER_PORT (only if clustering is on), DNS proxy udp+tcp/53, gateway protocol udp/4729 (open them if a firewall is active)"
fi
info "status: ddgw --show-gateways | ddgw --show-dns     logs: journalctl -u ddgw -f"
info "uninstall: sudo ddgw-uninstall   (add --purge to remove config and certificate too)"
[ "$ROLLED_BACK" = 1 ] && warn "rolled back"
exit 0
