#!/usr/bin/env bash
# Build SpadeIDE agent release artifacts and stage them in github-release/
# for a GitHub Release drag-and-drop (same workflow as KubeSpade desktop).
#
# Usage:
#   ./scripts/build-release.sh              # version 0.1.0 (default)
#   ./scripts/build-release.sh 0.2.0
#   ./scripts/build-release.sh v0.2.0       # leading v stripped
#   ./scripts/build-release.sh --version 0.2.0
#
# Env:
#   SKIP_LINUX=1 / SKIP_DEB=1 / SKIP_SERVER=1
#   SKIP_MAC=1 / SKIP_MAC_SIGN=1
#   ARCH=amd64|arm64|all          Linux arches (default: all)
#   MAC_ARCH=arm64|amd64|all   (default: all)
#   MAC_RELEASE=1                 set by this script — Developer ID + notarize
#   ALLOW_UNSIGNED_RELEASE=1      soft-fail Gatekeeper / signing checks (dev)
#   APPLE_* / CSC_NAME            see .env.example
#
# Output (wipe + refill each run):
#   github-release/
#     spadeide-agent_${VERSION}_amd64.deb
#     spadeide-agent_${VERSION}_arm64.deb
#     spadeide-agent-${VERSION}-linux-amd64.tar.gz
#     spadeide-agent-${VERSION}-linux-arm64.tar.gz
#     spadeide-agent-${VERSION}-macos-arm64.dmg
#     spadeide-agent-${VERSION}-macos-amd64.dmg
#     SHA256SUMS.txt
#
set -euo pipefail
shopt -s nullglob

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=release-checks.sh
source "$ROOT/scripts/release-checks.sh"
release_load_env "$ROOT"

DEFAULT_VERSION="0.1.0"
OUT="$ROOT/github-release"
DIST_LINUX="$ROOT/dist/linux"
DIST_MAC="$ROOT/dist/mac"

VERSION="${DEFAULT_VERSION}"
case "${1:-}" in
  "") ;;
  --version)
    VERSION="${2:?version required}"
    ;;
  -h|--help)
    sed -n '2,32p' "$0"
    exit 0
    ;;
  *)
    VERSION="$1"
    ;;
esac

VERSION="${VERSION#v}"
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.]+)?$ ]]; then
  echo "error: version must look like 0.2.0 or v0.2.0 (got: ${1:-$VERSION})" >&2
  exit 1
fi

stage() {
  release_stage "$OUT" "$@"
}

echo "=== SpadeIDE agent release ${VERSION} ==="
echo "    Staging dir: $OUT"
echo ""

rm -rf "$OUT"
mkdir -p "$OUT"

export VERSION
export MAC_RELEASE=1

did_something=0

# ----- Linux -----
if [[ "${SKIP_LINUX:-}" != "1" ]]; then
  kind=all
  if [[ "${SKIP_DEB:-}" == "1" && "${SKIP_SERVER:-}" == "1" ]]; then
    echo "==> Linux skipped (SKIP_DEB + SKIP_SERVER)"
  else
    if [[ "${SKIP_DEB:-}" == "1" ]]; then
      kind=server
    elif [[ "${SKIP_SERVER:-}" == "1" ]]; then
      kind=deb
    fi
    echo "=== Build Linux packages (KIND=${kind}, ARCH=${ARCH:-all}) ==="
    SKIP_STAGE=1 KIND="$kind" "$ROOT/scripts/build-linux.sh" "${ARCH:-all}" "$VERSION"
    did_something=1

    raw="${ARCH:-all}"
    case "$raw" in
      all|both|"") archs=(amd64 arm64) ;;
      arm64|aarch64) archs=(arm64) ;;
      amd64|x86_64|x64) archs=(amd64) ;;
      *) echo "Unsupported ARCH=$raw" >&2; exit 1 ;;
    esac

    echo ""
    echo "=== Stage Linux → github-release/ ==="
    for arch in "${archs[@]}"; do
      if [[ "${SKIP_DEB:-}" != "1" ]]; then
        stage "spadeide-agent_${VERSION}_${arch}.deb" \
          "$DIST_LINUX/spadeide-agent_${VERSION}_${arch}.deb" \
          "$DIST_LINUX"/spadeide-agent_*_"${arch}".deb
      fi
      if [[ "${SKIP_SERVER:-}" != "1" ]]; then
        stage "spadeide-agent-${VERSION}-linux-${arch}.tar.gz" \
          "$DIST_LINUX/server/spadeide-agent-linux-${arch}.tar.gz"
      fi
    done
  fi
else
  echo "==> Linux skipped (SKIP_LINUX=1)"
fi

# ----- macOS -----
if [[ "${SKIP_MAC:-}" == "1" ]]; then
  echo "==> macOS skipped (SKIP_MAC=1)"
elif [[ "$(uname -s)" != "Darwin" ]]; then
  echo "==> macOS skipped (not running on Darwin)"
else
  echo ""
  release_preflight_mac
  echo "=== Build SpadeIDE Agent.app DMGs (MAC_ARCH=${MAC_ARCH:-all}) ==="
  ARCH="${MAC_ARCH:-all}" "$ROOT/scripts/build-mac.sh"
  did_something=1

  echo ""
  echo "=== Stage macOS → github-release/ ==="
  mac_raw="${MAC_ARCH:-all}"
  case "$mac_raw" in
    all|both|"") macs=(arm64 amd64) ;;
    arm64|aarch64) macs=(arm64) ;;
    amd64|x86_64|x64) macs=(amd64) ;;
    *) echo "Unsupported MAC_ARCH=$mac_raw" >&2; exit 1 ;;
  esac
  for arch in "${macs[@]}"; do
    stage "spadeide-agent-${VERSION}-macos-${arch}.dmg" \
      "$DIST_MAC/spadeide-agent-macos-${arch}.dmg"
  done
fi

if [[ "$did_something" != "1" ]]; then
  echo "error: nothing to build (all platforms skipped)" >&2
  exit 1
fi

echo ""
echo "=== SHA256SUMS.txt ==="
release_write_sha256sums "$OUT"

echo ""
echo "=== Done ==="
echo "    Drag everything from:"
echo "      $OUT"
echo "    onto the GitHub Release v${VERSION} assets."
echo ""
ls -lh "$OUT"
echo ""
echo "    Next:  ./scripts/build-release.sh 0.2.0"
