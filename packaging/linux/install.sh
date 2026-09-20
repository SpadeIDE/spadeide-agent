#!/usr/bin/env bash
# Install spadeide from this directory (tarball or repo packaging/linux).
#
# Default: user install → ~/.local/bin + systemd --user
#   ./install.sh
# System-wide (server):
#   sudo ./install.sh --system [--user-name NAME]

set -euo pipefail

MODE=user
SYS_USER="${SPADE_USER:-spade}"
HERE="$(cd "$(dirname "$0")" && pwd)"

usage() {
  cat <<'EOF'
Usage: ./install.sh [--user|--system] [--user-name NAME]

  --user         Install for this account (default): ~/.local/bin + systemd --user
  --system       Install for a dedicated user (needs root): /usr/local/bin + systemd
  --user-name    System account name (default: spade)
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --user) MODE=user; shift ;;
    --system) MODE=system; shift ;;
    --user-name) SYS_USER="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage; exit 2 ;;
  esac
done

host_arch="$(uname -m)"
case "$host_arch" in
  x86_64|amd64) want="amd64" ;;
  arm64|aarch64) want="arm64" ;;
  *) echo "Unsupported arch: $host_arch (need amd64 or arm64)" >&2; exit 1 ;;
esac

find_binary() {
  local c
  for c in \
    "$HERE/spadeide" \
    "$HERE/spadeide-linux-$want" \
    "$HERE/../../dist/agent/spadeide-linux-$want" \
    "$HERE/../dist/agent/spadeide-linux-$want" \
    "$HERE/spade-agent" \
    "$HERE/spade-agent-linux-$want" \
    "$HERE/../../dist/agent/spade-agent-linux-$want" \
    "$HERE/../dist/agent/spade-agent-linux-$want"
  do
    if [[ -f "$c" ]]; then
      echo "$c"
      return 0
    fi
  done
  return 1
}

BIN="$(find_binary)" || {
  echo "spadeide binary not found. Build first:" >&2
  echo "  GOOS=linux ARCH=$want ./scripts/build-agent.sh" >&2
  echo "or unpack the linux-$want tarball and run install.sh from there." >&2
  exit 1
}

UNIT_USER="$HERE/spade-agent.user.service"
UNIT_SYS="$HERE/spade-agent.service"
if [[ ! -f "$UNIT_USER" ]]; then
  UNIT_USER="$HERE/../../packaging/linux/spade-agent.user.service"
  UNIT_SYS="$HERE/../../packaging/linux/spade-agent.service"
fi

echo "==> Installing spadeide from $(basename "$BIN") ($want) [$MODE]"

if [[ "$MODE" == "user" ]]; then
  mkdir -p "$HOME/.local/bin" "$HOME/.config/systemd/user"
  install -m 0755 "$BIN" "$HOME/.local/bin/spadeide"
  # Drop old dual-name install if present.
  rm -f "$HOME/.local/bin/spade-agent"
  if [[ -f "$UNIT_USER" ]]; then
    install -m 0644 "$UNIT_USER" "$HOME/.config/systemd/user/spade-agent.service"
  fi
  if command -v systemctl >/dev/null 2>&1; then
    systemctl --user daemon-reload
    if systemctl --user enable --now spade-agent; then
      echo
      echo "Agent is running."
      echo "  spadeide                 # QR + pair URL + online tablets"
      echo "  spadeide status"
      echo "  spadeide stop"
      echo
      echo "If this is a headless box, linger so it survives logout:"
      echo "  sudo loginctl enable-linger $USER"
    else
      echo
      echo "systemd --user could not start the agent (no login session?)."
      echo "Run:  spadeide"
    fi
  else
    echo "No systemd — just run:"
    echo "  spadeide"
  fi
  echo
  echo "LAN discovery: sudo apt install avahi-daemon avahi-utils"
  echo
  if command -v spadeide >/dev/null 2>&1; then
    spadeide version
  else
    "$HOME/.local/bin/spadeide" version
  fi
else
  if [[ "$(id -u)" -ne 0 ]]; then
    echo "system install needs root (sudo ./install.sh --system)" >&2
    exit 1
  fi
  if ! id -u "$SYS_USER" >/dev/null 2>&1; then
    useradd --create-home --shell /bin/bash --system "$SYS_USER"
  fi
  home="$(getent passwd "$SYS_USER" | cut -d: -f6)"
  install -m 0755 "$BIN" /usr/local/bin/spadeide
  rm -f /usr/local/bin/spade-agent
  unit=/etc/systemd/system/spade-agent.service
  if [[ -f "$UNIT_SYS" ]]; then
    sed -e "s|^User=spade|User=$SYS_USER|" \
        -e "s|/home/spade|$home|g" \
        "$UNIT_SYS" > "$unit"
  fi
  systemctl daemon-reload
  systemctl enable --now spade-agent
  echo
  echo "Agent is running as $SYS_USER."
  echo "  sudo -u $SYS_USER spadeide"
  echo "  systemctl status spade-agent"
  echo
  echo "LAN discovery: sudo apt install avahi-daemon avahi-utils"
  echo
  /usr/local/bin/spadeide version
fi
