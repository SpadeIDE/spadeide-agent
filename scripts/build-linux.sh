#!/usr/bin/env bash
# Build Linux packages (.deb + server tarball).
# Same contract as KubeSpade kubespade-desktop/build-linux.sh:
#   ./scripts/build-linux.sh              # host arch (arm64 on Linux ARM, else amd64)
#   ./scripts/build-linux.sh arm64
#   ./scripts/build-linux.sh arm64 v0.2.0
#   ./scripts/build-linux.sh amd64 0.2.0
#   ./scripts/build-linux.sh all 0.2.0    # both arches (Go cross-compiles)
#
# After a successful build, copies artifacts into github-release/ (append only:
# existing Mac / other-arch files stay). Does not clear that folder — only
# ./scripts/build-release.sh wipes it. SKIP_STAGE=1 skips the copy (build-release
# stages itself).
#
# Env:
#   SKIP_DEB=1 / SKIP_SERVER=1
#   KIND=deb|server|all
#   ARCH=amd64|arm64|all     used when the first argument is omitted
#   SKIP_STAGE=1
#   OUT_DIR=…                package output (default: dist/linux)
#
# Output:
#   dist/linux/spadeide-agent_<ver>_<arch>.deb
#   dist/linux/server/spadeide-agent-linux-<arch>.tar.gz
#   github-release/  (unless SKIP_STAGE=1)

set -euo pipefail
shopt -s nullglob

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=release-checks.sh
source "$ROOT/scripts/release-checks.sh"
release_load_env "$ROOT"

DEFAULT_VERSION="0.1.0"
OUT="$ROOT/github-release"
OUT_DIR="${OUT_DIR:-$ROOT/dist/linux}"
PKG="$ROOT/packaging/linux"
DEB_SRC="$PKG/debian"

default_linux_arch() {
  if [[ "$(uname -s)" == "Linux" ]]; then
    case "$(uname -m)" in
      aarch64|arm64) echo arm64 ;;
      *) echo amd64 ;;
    esac
  else
    echo amd64
  fi
}

normalize_arch() {
  case "$1" in
    all|both) echo all ;;
    arm64|aarch64) echo arm64 ;;
    amd64|x86_64|x64) echo amd64 ;;
    *) return 1 ;;
  esac
}

ARCH_ARG="${1:-}"
VERSION_ARG="${2:-}"
case "$ARCH_ARG" in
  -h|--help)
    sed -n '2,26p' "$0"
    exit 0
    ;;
  --version)
    VERSION_ARG="${VERSION_ARG:?version required}"
    ARCH_ARG=""
    ;;
esac

# ./scripts/build-linux.sh 0.2.0 → host arch at that version.
if [[ -n "$ARCH_ARG" && "$ARCH_ARG" =~ ^v?[0-9]+\.[0-9]+ ]]; then
  VERSION_ARG="$ARCH_ARG"
  ARCH_ARG=""
fi

if [[ -z "$ARCH_ARG" ]]; then
  raw="${ARCH:-$(default_linux_arch)}"
else
  raw="$ARCH_ARG"
fi

parsed=""
if ! parsed="$(normalize_arch "$raw")"; then
  echo "Usage: $0 [amd64|arm64|all] [version]" >&2
  echo "  version optional, e.g. 0.2.0 or v0.2.0" >&2
  echo "  omitted arch: arm64 on Linux ARM, otherwise amd64" >&2
  exit 1
fi
case "$parsed" in
  all) archs=(amd64 arm64) ;;
  *) archs=("$parsed") ;;
esac

VERSION="${VERSION:-$DEFAULT_VERSION}"
if [[ -n "$VERSION_ARG" ]]; then
  VERSION="$VERSION_ARG"
fi
VERSION="${VERSION#v}"
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.]+)?$ ]]; then
  echo "error: version must look like 0.2.0 or v0.2.0 (got: ${VERSION_ARG:-$VERSION})" >&2
  exit 1
fi
export VERSION

KIND="${KIND:-all}"
if [[ "${SKIP_DEB:-}" == "1" && "${SKIP_SERVER:-}" == "1" ]]; then
  echo "error: SKIP_DEB + SKIP_SERVER leaves nothing to build" >&2
  exit 1
fi
if [[ "${SKIP_DEB:-}" == "1" ]]; then
  KIND=server
elif [[ "${SKIP_SERVER:-}" == "1" ]]; then
  KIND=deb
fi

case "$KIND" in
  all|both|""|deb|server) ;;
  *) echo "Unsupported KIND=$KIND (use deb, server, or all)" >&2; exit 1 ;;
esac

echo "=== SpadeIDE Linux ${VERSION} (${parsed}) ==="
echo "    Packages: $OUT_DIR"
if [[ "${SKIP_STAGE:-}" == "1" ]]; then
  echo "    Staging:  skipped (SKIP_STAGE=1)"
else
  echo "    Staging:  $OUT (append, no wipe)"
fi
echo ""

mkdir -p "$OUT_DIR/server"

# Pack a .deb without dpkg-deb so this works on a Mac build host.
pack_deb() {
  local arch="$1" bin="$2"
  local work="$OUT_DIR/.deb-build-$arch"
  local deb_name="spadeide-agent_${VERSION}_${arch}.deb"
  local out="$OUT_DIR/$deb_name"

  rm -rf "$work"
  mkdir -p "$work/data/usr/bin" \
    "$work/data/usr/lib/systemd/user" \
    "$work/data/lib/systemd/system" \
    "$work/data/usr/share/doc/spadeide-agent" \
    "$work/data/usr/share/applications" \
    "$work/data/usr/share/pixmaps" \
    "$work/data/etc/xdg/autostart" \
    "$work/control"

  install -m 0755 "$bin" "$work/data/usr/bin/spadeide"
  install -m 0755 "$PKG/desktop/spadeide-gui" "$work/data/usr/bin/spadeide-gui"
  install -m 0644 "$PKG/desktop/spadeide.desktop" \
    "$work/data/usr/share/applications/spadeide.desktop"
  install -m 0644 "$PKG/desktop/spadeide-autostart.desktop" \
    "$work/data/etc/xdg/autostart/spadeide.desktop"
  install -m 0644 "$PKG/desktop/icons/256x256.png" \
    "$work/data/usr/share/pixmaps/spadeide.png"
  install -m 0644 "$PKG/desktop/icons/panel-24x24.png" \
    "$work/data/usr/share/pixmaps/spadeide-panel.png"

  local size_px
  for size_px in 16 22 24 32 48 64 128 256 512; do
    mkdir -p "$work/data/usr/share/icons/hicolor/${size_px}x${size_px}/apps"
    install -m 0644 "$PKG/desktop/icons/${size_px}x${size_px}.png" \
      "$work/data/usr/share/icons/hicolor/${size_px}x${size_px}/apps/spadeide.png"
  done
  local panel_px
  for panel_px in 22 24 48 64; do
    mkdir -p "$work/data/usr/share/icons/hicolor/${panel_px}x${panel_px}/apps"
    mkdir -p "$work/data/usr/share/icons/hicolor/${panel_px}x${panel_px}/status"
    install -m 0644 "$PKG/desktop/icons/panel-${panel_px}x${panel_px}.png" \
      "$work/data/usr/share/icons/hicolor/${panel_px}x${panel_px}/apps/spadeide-panel.png"
    install -m 0644 "$PKG/desktop/icons/panel-${panel_px}x${panel_px}.png" \
      "$work/data/usr/share/icons/hicolor/${panel_px}x${panel_px}/status/spadeide-panel.png"
  done
  install -m 0644 "$DEB_SRC/spade-agent.user.service" \
    "$work/data/usr/lib/systemd/user/spade-agent.service"
  install -m 0644 "$DEB_SRC/spade-agent.service" \
    "$work/data/lib/systemd/system/spade-agent.service"
  install -m 0644 "$DEB_SRC/README.Debian" \
    "$work/data/usr/share/doc/spadeide-agent/README.Debian"
  gzip -9n -c "$DEB_SRC/README.Debian" > "$work/data/usr/share/doc/spadeide-agent/README.Debian.gz" 2>/dev/null \
    || gzip -9 -c "$DEB_SRC/README.Debian" > "$work/data/usr/share/doc/spadeide-agent/README.Debian.gz"
  rm -f "$work/data/usr/share/doc/spadeide-agent/README.Debian"

  cat > "$work/data/usr/share/doc/spadeide-agent/copyright" <<EOF
Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/
Upstream-Name: spadeide-agent
Source: https://github.com/SpadeIDE/spadeide-agent

Files: *
Copyright: 2026 SpadeIDE contributors
License: Apache-2.0

License: Apache-2.0
 Licensed under the Apache License, Version 2.0 (the "License");
 you may not use this file except in compliance with the License.
 You may obtain a copy of the License at
 .
     http://www.apache.org/licenses/LICENSE-2.0
 .
 Unless required by applicable law or agreed to in writing, software
 distributed under the License is distributed on an "AS IS" BASIS,
 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 See the License for the specific language governing permissions and
 limitations under the License.
EOF

  local size
  size="$(du -sk "$work/data" | awk '{print $1}')"

  cat > "$work/control/control" <<EOF
Package: spadeide-agent
Version: ${VERSION}
Architecture: ${arch}
Maintainer: SpadeIDE <agent@spadeide.dev>
Section: devel
Priority: optional
Installed-Size: ${size}
Depends: systemd, python3, python3-gi, gir1.2-gtk-3.0, gir1.2-gdkpixbuf-2.0, gir1.2-ayatanaappindicator3-0.1 | gir1.2-appindicator3-0.1, python3-qrcode, python3-pil
Recommends: avahi-daemon, avahi-utils, gnome-shell-extension-appindicator
Provides: spadeide
Replaces: spadeide
Conflicts: spadeide
Description: SpadeIDE host agent for Ubuntu and Debian
 Host backend for the SpadeIDE iPad IDE. On a desktop, SpadeIDE Agent appears
 in the app grid with a tray icon and pairing QR. From a terminal: spadeide.
 Several tablets can stay paired.
EOF

  install -m 0755 "$DEB_SRC/postinst" "$work/control/postinst"
  install -m 0755 "$DEB_SRC/prerm" "$work/control/prerm"
  install -m 0755 "$DEB_SRC/postrm" "$work/control/postrm"

  (
    cd "$work/data"
    COPYFILE_DISABLE=1 tar --format=ustar \
      --exclude='.DS_Store' --exclude='._*' \
      -czf "$work/data.tar.gz" usr lib etc
  )
  (
    cd "$work/control"
    COPYFILE_DISABLE=1 tar --format=ustar \
      --exclude='.DS_Store' --exclude='._*' \
      -czf "$work/control.tar.gz" control postinst prerm postrm
  )
  printf '2.0\n' > "$work/debian-binary"

  rm -f "$out"
  if command -v dpkg-deb >/dev/null 2>&1; then
    # Prefer native tool when building on Debian/Ubuntu.
    local stage="$work/root"
    mkdir -p "$stage/DEBIAN"
    cp -a "$work/data/." "$stage/"
    cp -a "$work/control/." "$stage/DEBIAN/"
    dpkg-deb --build "$stage" "$out"
  else
    # macOS BSD ar is not valid for dpkg — write the archive ourselves.
    python3 "$ROOT/scripts/pack-deb.py" "$out" \
      "$work/debian-binary" "$work/control.tar.gz" "$work/data.tar.gz"
  fi

  rm -rf "$work"
  echo "    $out"
}

pack_server() {
  local arch="$1" bin="$2"
  local stage="$OUT_DIR/server/spadeide-agent-linux-$arch"
  local tgz="$OUT_DIR/server/spadeide-agent-linux-$arch.tar.gz"

  rm -rf "$stage"
  mkdir -p "$stage"
  install -m 0755 "$bin" "$stage/spadeide"
  install -m 0755 "$bin" "$stage/spadeide-agent-linux-$arch"
  install -m 0755 "$PKG/install.sh" "$stage/install.sh"
  install -m 0755 "$PKG/reinstall.sh" "$stage/reinstall.sh"
  install -m 0644 "$PKG/spade-agent.user.service" "$stage/spade-agent.user.service"
  install -m 0644 "$PKG/spade-agent.service" "$stage/spade-agent.service"
  cat > "$stage/README.txt" <<EOF
SpadeIDE Agent — Linux server / LTS ($arch)
Version $VERSION

Which install?
  ./install.sh                 # only you — agent as your user, jailed to \$HOME
  sudo ./install.sh --system   # shared host — each iPad Unix-logs in once

First-time install:
  ./install.sh                 # ~/.local/bin/spadeide + systemd --user
  sudo loginctl enable-linger "\$USER"   # keep running after logout
  spadeide                     # QR + pair URL

  # or shared host:
  sudo ./install.sh --system
  sudo spadeide pair --no-start

Reinstall (same layout as before — keeps pairings):
  ./reinstall.sh               # user → user
  sudo ./reinstall.sh          # if it was --system

Or copy the binary only:
  sudo cp spadeide /usr/local/bin/
  spadeide

Day to day:
  spadeide                     # start + QR (user install)
  sudo spadeide pair --no-start  # QR only (system install — never start a 2nd agent)
  spadeide status
  spadeide stop

On the iPad after pair (system install): Unix Login sheet once
(username + password over TLS). Password change / deleted user → login again.
Unix root is refused. Terminal = that user’s normal groups (e.g. docker).

Several iPads can stay paired; run spadeide again for a new tablet.

See docs/LINUX.md and the site Linux agent docs.
EOF

  tar -C "$OUT_DIR/server" -czf "$tgz" "spadeide-agent-linux-$arch"
  rm -rf "$stage"
  echo "    $tgz"
}

for arch in "${archs[@]}"; do
  echo "==> Linux $arch"
  GOOS=linux ARCH="$arch" VERSION="$VERSION" "$ROOT/scripts/build-agent.sh"
  bin="$ROOT/dist/agent/spadeide-agent-linux-$arch"
  [[ -f "$bin" ]] || { echo "missing $bin" >&2; exit 1; }

  if [[ "$KIND" == "all" || "$KIND" == "both" || "$KIND" == "" || "$KIND" == "deb" ]]; then
    echo "==> .deb ($arch) — Ubuntu / Debian desktop"
    pack_deb "$arch" "$bin"
  fi
  if [[ "$KIND" == "all" || "$KIND" == "both" || "$KIND" == "" || "$KIND" == "server" ]]; then
    echo "==> server tarball ($arch) — LTS / headless"
    pack_server "$arch" "$bin"
  fi
done

echo "==> Done"
echo "Desktop (.deb):"
ls -lh "$OUT_DIR"/spadeide-agent_*.deb 2>/dev/null || true
echo "Server (LTS):"
ls -lh "$OUT_DIR"/server/spadeide-agent-linux-*.tar.gz 2>/dev/null || true

release_stage_linux "$OUT" "$OUT_DIR" "$VERSION" "$KIND" "${archs[@]}"

if [[ "${SKIP_STAGE:-}" != "1" ]]; then
  echo ""
  echo "    Ubuntu:"
  echo "      sudo apt install ./spadeide-agent_${VERSION}_*.deb"
  echo "      tar xzf spadeide-agent-${VERSION}-linux-*.tar.gz && cd spadeide-agent-linux-* && ./install.sh"
  echo "    Staged (no wipe): $OUT"
  ls -lh "$OUT"
fi
