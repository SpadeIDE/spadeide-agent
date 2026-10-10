#!/usr/bin/env bash
# Reinstall spadeide from this tarball using the same layout as before
# (user → user, system → system). Pairings and identity are left alone.
#
#   ./reinstall.sh
#   sudo ./reinstall.sh          # needed when the previous install was --system

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
INSTALL="$HERE/install.sh"

usage() {
  cat <<'EOF'
Usage: ./reinstall.sh

  Detects whether SpadeIDE was installed for this user or as a system
  broker, then reinstalls the binary and unit from this directory the
  same way. State (~/.spade-agent or /var/lib/spade-agent) is kept.

  System layout needs root:  sudo ./reinstall.sh
EOF
}

case "${1:-}" in
  -h|--help) usage; exit 0 ;;
  "") ;;
  *) echo "Unknown option: $1" >&2; usage; exit 2 ;;
esac

if [[ ! -x "$INSTALL" ]]; then
  echo "install.sh not found next to reinstall.sh" >&2
  exit 1
fi

effective_home() {
  if [[ -n "${SUDO_USER:-}" && "$SUDO_USER" != "root" ]]; then
    getent passwd "$SUDO_USER" | cut -d: -f6
  else
    printf '%s\n' "${HOME:-}"
  fi
}

user_ctl() {
  local home u uid
  home="$(effective_home)"
  if [[ -n "${SUDO_USER:-}" && "$SUDO_USER" != "root" ]]; then
    u="$SUDO_USER"
    uid="$(id -u "$SUDO_USER")"
    sudo -u "$u" XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$uid}" \
      systemctl --user "$@"
  else
    systemctl --user "$@"
  fi
}

system_unit_present() {
  [[ -f /etc/systemd/system/spade-agent.service ]] \
    || [[ -f /lib/systemd/system/spade-agent.service ]] \
    || [[ -f /usr/lib/systemd/system/spade-agent.service ]]
}

system_running() {
  command -v systemctl >/dev/null 2>&1 \
    && { systemctl is-active --quiet spade-agent 2>/dev/null \
         || systemctl is-enabled --quiet spade-agent 2>/dev/null; }
}

user_unit_present() {
  local home
  home="$(effective_home)"
  [[ -n "$home" && -f "$home/.config/systemd/user/spade-agent.service" ]]
}

user_bin_present() {
  local home
  home="$(effective_home)"
  [[ -n "$home" && -x "$home/.local/bin/spadeide" ]]
}

user_running() {
  command -v systemctl >/dev/null 2>&1 || return 1
  user_ctl is-active --quiet spade-agent 2>/dev/null \
    || user_ctl is-enabled --quiet spade-agent 2>/dev/null
}

detect_mode() {
  # Prefer whatever is actually running / enabled.
  if system_running; then
    echo system
    return 0
  fi
  if user_running; then
    echo user
    return 0
  fi
  # Then whatever is installed on disk.
  if system_unit_present || [[ -d /var/lib/spade-agent && -x /usr/local/bin/spadeide ]]; then
    echo system
    return 0
  fi
  if user_unit_present || user_bin_present; then
    echo user
    return 0
  fi
  return 1
}

MODE="$(detect_mode)" || {
  echo "No existing SpadeIDE install found." >&2
  echo "First-time install:" >&2
  echo "  ./install.sh                 # this user" >&2
  echo "  sudo ./install.sh --system   # privileged broker" >&2
  exit 1
}

if [[ "$MODE" == "system" && "$(id -u)" -ne 0 ]]; then
  echo "Detected system install — re-running with sudo…"
  exec sudo --preserve-env=SUDO_USER "$0"
fi

echo "==> Detected existing layout: $MODE"
echo "==> Stopping agent before replacing the binary…"

if [[ "$MODE" == "system" ]]; then
  systemctl stop spade-agent 2>/dev/null || true
  if command -v spadeide >/dev/null 2>&1; then
    spadeide stop 2>/dev/null || true
  fi
else
  user_ctl stop spade-agent 2>/dev/null || true
  home="$(effective_home)"
  if [[ -n "${SUDO_USER:-}" && "$SUDO_USER" != "root" ]]; then
    sudo -u "$SUDO_USER" "$home/.local/bin/spadeide" stop 2>/dev/null || true
  elif [[ -x "$home/.local/bin/spadeide" ]]; then
    "$home/.local/bin/spadeide" stop 2>/dev/null || true
  fi
fi

echo "==> Reinstalling from this archive…"
if [[ "$MODE" == "system" ]]; then
  "$INSTALL" --system
else
  if [[ -n "${SUDO_USER:-}" && "$SUDO_USER" != "root" ]]; then
    # Ran under sudo by mistake on a user install — install as that user.
    sudo -u "$SUDO_USER" -H "$INSTALL" --user
  else
    "$INSTALL" --user
  fi
fi

hash -r 2>/dev/null || true
echo
echo "Reinstall done ($MODE). Pairings were kept."
if [[ "$MODE" == "system" ]]; then
  echo "  sudo systemctl status spade-agent"
  echo "  sudo spadeide pair --no-start"
else
  echo "  spadeide status"
fi
