#!/usr/bin/env bash
# Cross-compile the spade-agent Go helper (build intermediate).
#
# Usage:
#   ./scripts/build-agent.sh                  # host GOOS/GOARCH (local CLI / tests)
#   ARCH=arm64 GOOS=darwin …                  # used by build-mac.sh → Agent.app
#   GOOS=linux ARCH=amd64 ./scripts/build-agent.sh
#
# Ship products (not these bare binaries):
#   macOS  → ./scripts/build-mac.sh     (SpadeIDE Agent.app)
#   Linux  → ./scripts/build-linux.sh   (.deb + server tarballs)
#
# Output:
#   dist/agent/spade-agent-<goos>-<goarch>[.exe]
#   dist/agent/spade-agent                  (host OS only — local CLI)
#
# Env:
#   GOOS / ARCH|GOARCH / VERSION / OUT_DIR / CGO_ENABLED (default 0)

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
AGENT_DIR="$ROOT/agent"
OUT_DIR="${OUT_DIR:-$ROOT/dist/agent}"
VERSION="${VERSION:-0.1.0}"

host_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$host_os" in
  darwin) host_goos="darwin" ;;
  linux)  host_goos="linux" ;;
  mingw*|msys*|cygwin*) host_goos="windows" ;;
  *) host_goos="$host_os" ;;
esac

host_arch="$(uname -m)"
case "$host_arch" in
  arm64|aarch64) host_goarch="arm64" ;;
  x86_64|amd64)  host_goarch="amd64" ;;
  *) echo "Unsupported host arch: $host_arch" >&2; exit 1 ;;
esac

# ARCH is the SpadeIDE-facing name (arm64 / amd64); GOARCH wins if both set.
raw_arch="${ARCH:-${GOARCH:-$host_goarch}}"
case "$raw_arch" in
  arm64|aarch64) goarch="arm64" ;;
  amd64|x86_64|x64) goarch="amd64" ;;
  *) echo "Unsupported ARCH/GOARCH: $raw_arch (use arm64 or amd64)" >&2; exit 1 ;;
esac

goos="${GOOS:-$host_goos}"
ext=""
[[ "$goos" == "windows" ]] && ext=".exe"

mkdir -p "$OUT_DIR"
if [[ "$goos" == "linux" ]]; then
  # Artifact name includes agent; installed CLI remains `spadeide`.
  out_named="$OUT_DIR/spadeide-agent-${goos}-${goarch}${ext}"
else
  out_named="$OUT_DIR/spade-agent-${goos}-${goarch}${ext}"
fi
out_latest="$OUT_DIR/spade-agent${ext}"

echo "==> Building ${out_named##*/} (${goos}/${goarch})"
(
  cd "$AGENT_DIR"
  CGO_ENABLED="${CGO_ENABLED:-0}" GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath \
    -ldflags "-s -w -X github.com/spadeide/spade-agent/internal/config.Version=${VERSION}" \
    -o "$out_named" ./cmd/spade-agent
)
echo "    $out_named"

# Only clobber the unsuffixed name when this is the host OS — otherwise a
# linux cross-build would replace the darwin helper macOS packaging expects.
if [[ "$goos" == "$host_goos" ]]; then
  cp -f "$out_named" "$out_latest"
  chmod +x "$out_named" "$out_latest" 2>/dev/null || true
  echo "    $out_latest"
else
  chmod +x "$out_named" 2>/dev/null || true
fi
