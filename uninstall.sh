#!/usr/bin/env bash
# dnsbench uninstaller.
#
#   sudo bash uninstall.sh [--purge] [--yes]
#
# Removes the service and the program. Your configuration, certificate and
# saved schedules are kept unless you pass --purge. The PAM service file is
# removed only if it is still exactly the one the installer wrote. The login
# group is removed only with --purge, and only if the installer created it.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="${DNSBENCH_INSTALL_DIR:-/opt/dnsbench}"
CONF_DIR="${DNSBENCH_CONF_DIR:-/etc/dnsbench}"
STATE_DIR="${DNSBENCH_STATE_DIR:-/var/lib/dnsbench}"
PAM_DIR="${DNSBENCH_PAM_DIR:-/etc/pam.d}"
UNIT_DIR="${DNSBENCH_UNIT_DIR:-/etc/systemd/system}"
SYSTEMCTL="${DNSBENCH_SYSTEMCTL:-systemctl}"
PAM_FILE="$PAM_DIR/dnsbench"
UNIT_FILE="$UNIT_DIR/dnsbench.service"

PURGE=0
YES=0

RED=$'\033[0;31m'; YELLOW=$'\033[1;33m'; GREEN=$'\033[0;32m'; BLUE=$'\033[0;34m'; NC=$'\033[0m'
info() { echo "${BLUE}[INFO]${NC}  $*"; }
ok()   { echo "${GREEN}[OK]${NC}    $*"; }
warn() { echo "${YELLOW}[WARN]${NC}  $*"; }
die()  { echo "${RED}[ERROR]${NC} $*" >&2; exit 1; }

for arg in "$@"; do
  case "$arg" in
    --purge) PURGE=1 ;;
    --yes|-y) YES=1 ;;
    -h|--help) sed -n '2,9p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option: $arg (try --help)" ;;
  esac
done

[[ $EUID -eq 0 || -n "${DNSBENCH_INSTALL_DIR:-}" ]] || die "Run as root: sudo bash uninstall.sh"

if [[ $YES -eq 0 ]]; then
  echo
  echo "  This removes dnsbench from this system."
  [[ $PURGE -eq 1 ]] && echo "  --purge: its configuration, certificate and schedules will be deleted too."
  read -rp "  Continue? [y/N]: " reply
  [[ "$reply" =~ ^[Yy]$ ]] || { echo "Aborted."; exit 0; }
fi

# The PAM file the Rust-era installer wrote; also removable if untouched.
legacy_pam() {
  printf '%s\n' \
    '# PAM configuration for dnsbench' \
    '# Simple Unix password auth — no TTY or session requirements.' \
    'auth     required   pam_unix.so nodelay' \
    'account  required   pam_unix.so'
}

# The PAM file version 1 installed; also removable if untouched.
v1_pam() {
  cat <<'V1PAM'
# PAM service for dnsbench: who may sign in to the web UI and API.
#
# This is plain Unix password authentication (the accounts in /etc/passwd and
# /etc/shadow). Expired and locked accounts are refused. It deliberately avoids
# the "login" stack, whose TTY checks cannot work for a daemon.
#
# Anyone who can sign in can make this host send DNS traffic anywhere, so
# restrict it to a group if the host is shared. Create the group, add people,
# and uncomment the first line:
#     groupadd dnsbench && usermod -aG dnsbench alice
#
#auth     required   pam_succeed_if.so user ingroup dnsbench quiet
auth     required   pam_unix.so nodelay
account  required   pam_unix.so
#
# For LDAP/SSSD/etc. replace the two lines above with your site's stack, e.g.
# on Debian/Ubuntu:   @include common-auth   and   @include common-account
V1PAM
}

# Stop and remove the service
if "$SYSTEMCTL" is-active --quiet dnsbench 2>/dev/null; then
  info "Stopping dnsbench"
  "$SYSTEMCTL" stop dnsbench
fi
if "$SYSTEMCTL" is-enabled --quiet dnsbench 2>/dev/null; then
  "$SYSTEMCTL" disable dnsbench >/dev/null 2>&1 || true
fi
if [[ -f "$UNIT_FILE" ]]; then
  rm -f "$UNIT_FILE"
  "$SYSTEMCTL" daemon-reload 2>/dev/null || true
  ok "Service removed"
fi

# The program
if [[ -d "$INSTALL_DIR" ]]; then
  rm -rf "$INSTALL_DIR"
  ok "Removed $INSTALL_DIR"
fi

# PAM service: only if it is untouched
if [[ -f "$PAM_FILE" ]]; then
  if { [[ -f "$SCRIPT_DIR/contrib/pam.d/dnsbench" ]] && cmp -s "$PAM_FILE" "$SCRIPT_DIR/contrib/pam.d/dnsbench"; } ||
     cmp -s "$PAM_FILE" <(v1_pam) || cmp -s "$PAM_FILE" <(legacy_pam); then
    rm -f "$PAM_FILE"
    ok "Removed $PAM_FILE"
  else
    warn "Kept $PAM_FILE: it has been customised"
  fi
fi

# The login group: removed only with --purge, and only if the installer created it
GROUP_MARKER="$CONF_DIR/.created-login-group"
if getent group dnsbench >/dev/null 2>&1; then
  if [[ $PURGE -eq 1 && -f "$GROUP_MARKER" ]]; then
    if { command -v groupdel >/dev/null 2>&1 && groupdel dnsbench 2>/dev/null; } ||
       { command -v delgroup >/dev/null 2>&1 && delgroup dnsbench >/dev/null 2>&1; }; then
      ok "Removed group dnsbench (the installer created it)"
    else
      warn "Could not remove group dnsbench"
    fi
  elif [[ $PURGE -eq 1 ]]; then
    warn "Kept group dnsbench: it existed before dnsbench was installed"
  else
    warn "Kept group dnsbench and its members (use --purge to remove a group the installer created)"
  fi
fi

# Config, certificate, schedules
if [[ $PURGE -eq 1 ]]; then
  [[ -d "$CONF_DIR" ]] && { rm -rf "$CONF_DIR"; ok "Removed $CONF_DIR"; }
  [[ -d "$STATE_DIR" ]] && { rm -rf "$STATE_DIR"; ok "Removed $STATE_DIR"; }
else
  [[ -d "$CONF_DIR" ]] && warn "Kept $CONF_DIR (use --purge to delete it)"
  [[ -d "$STATE_DIR" ]] && warn "Kept $STATE_DIR: certificate and saved schedules (use --purge to delete it)"
fi

echo
echo "${GREEN}dnsbench has been removed.${NC}"
echo
exit 0
