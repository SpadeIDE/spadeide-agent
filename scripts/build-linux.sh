#!/usr/bin/env bash
# Build SpadeIDE Linux agents for:
#   • Ubuntu / Debian desktop  → .deb (amd64 + arm64)
#   • Server / LTS headless    → tarball + install.sh (amd64 + arm64)
#
# Usage:
#   ./scripts/build-linux.sh              # both arches, deb + server
#   ARCH=amd64 ./scripts/build-linux.sh
#   ARCH=arm64 ./scripts/build-linux.sh
#   KIND=deb ./scripts/build-linux.sh     # packages only
#   KIND=server ./scripts/build-linux.sh  # server tarballs only
#
# Output:
#   dist/linux/spadeide_<ver>_<arch>.deb
#   dist/linux/server/spadeide-linux-<arch>.tar.gz
#
# macOS ships Agent.app only (./scripts/build-mac.sh) — not bare darwin binaries.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="${OUT_DIR:-$ROOT/dist/linux}"
VERSION="${VERSION:-0.1.0}"
PKG="$ROOT/packaging/linux"
DEB_SRC="$PKG/debian"
KIND="${KIND:-all}"

raw="${ARCH:-all}"
case "$raw" in
  all|both|"") archs=(amd64 arm64) ;;
  arm64|aarch64) archs=(arm64) ;;
  amd64|x86_64|x64) archs=(amd64) ;;
  *) echo "Unsupported ARCH=$raw (use amd64, arm64, or all)" >&2; exit 1 ;;
esac

case "$KIND" in
  all|both|""|deb|server) ;;
  *) echo "Unsupported KIND=$KIND (use deb, server, or all)" >&2; exit 1 ;;
esac

mkdir -p "$OUT_DIR/server"

# Pack a .deb without dpkg-deb so this works on a Mac build host.
pack_deb() {
  local arch="$1" bin="$2"
  local work="$OUT_DIR/.deb-build-$arch"
  local deb_name="spadeide_${VERSION}_${arch}.deb"
  local out="$OUT_DIR/$deb_name"

  rm -rf "$work"
  mkdir -p "$work/data/usr/bin" \
    "$work/data/usr/lib/systemd/user" \
    "$work/data/lib/systemd/system" \
    "$work/data/usr/share/doc/spadeide" \
    "$work/control"

  install -m 0755 "$bin" "$work/data/usr/bin/spadeide"
  install -m 0644 "$DEB_SRC/spade-agent.user.service" \
    "$work/data/usr/lib/systemd/user/spade-agent.service"
  install -m 0644 "$DEB_SRC/spade-agent.service" \
    "$work/data/lib/systemd/system/spade-agent.service"
  install -m 0644 "$DEB_SRC/README.Debian" \
    "$work/data/usr/share/doc/spadeide/README.Debian"
  gzip -9n -c "$DEB_SRC/README.Debian" > "$work/data/usr/share/doc/spadeide/README.Debian.gz" 2>/dev/null \
    || gzip -9 -c "$DEB_SRC/README.Debian" > "$work/data/usr/share/doc/spadeide/README.Debian.gz"
  rm -f "$work/data/usr/share/doc/spadeide/README.Debian"

  cat > "$work/data/usr/share/doc/spadeide/copyright" <<EOF
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
Package: spadeide
Version: ${VERSION}
Architecture: ${arch}
Maintainer: SpadeIDE <agent@spadeide.dev>
Section: devel
Priority: optional
Installed-Size: ${size}
Depends: systemd
Recommends: avahi-daemon, avahi-utils
Description: SpadeIDE host agent for Ubuntu and Debian
 Host backend for the SpadeIDE iPad IDE. One command: spadeide — starts the
 agent if needed and prints a pair QR / URL. Several tablets can stay paired.
EOF

  install -m 0755 "$DEB_SRC/postinst" "$work/control/postinst"
  install -m 0755 "$DEB_SRC/prerm" "$work/control/prerm"
  install -m 0755 "$DEB_SRC/postrm" "$work/control/postrm"

  (
    cd "$work/data"
    COPYFILE_DISABLE=1 tar --format=ustar \
      --exclude='.DS_Store' --exclude='._*' \
      -czf "$work/data.tar.gz" usr lib
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
  local stage="$OUT_DIR/server/spadeide-linux-$arch"
  local tgz="$OUT_DIR/server/spadeide-linux-$arch.tar.gz"

  rm -rf "$stage"
  mkdir -p "$stage"
  install -m 0755 "$bin" "$stage/spadeide"
  install -m 0755 "$bin" "$stage/spadeide-linux-$arch"
  install -m 0755 "$PKG/install.sh" "$stage/install.sh"
  install -m 0644 "$PKG/spade-agent.user.service" "$stage/spade-agent.user.service"
  install -m 0644 "$PKG/spade-agent.service" "$stage/spade-agent.service"
  cat > "$stage/README.txt" <<EOF
SpadeIDE — Linux server / LTS ($arch)
Version $VERSION

Install:
  ./install.sh                 # ~/.local/bin/spadeide + systemd --user
  sudo ./install.sh --system   # dedicated user, starts on boot

Or copy the binary only:
  sudo cp spadeide /usr/local/bin/
  spadeide                     # starts agent + shows QR / pair URL

  spadeide status
  spadeide stop

Several iPads can stay paired; run spadeide again for a new tablet.

Headless user install — survive logout:
  sudo loginctl enable-linger "\$USER"

See docs/LINUX.md.
EOF

  tar -C "$OUT_DIR/server" -czf "$tgz" "spadeide-linux-$arch"
  rm -rf "$stage"
  echo "    $tgz"
}

for arch in "${archs[@]}"; do
  echo "==> Linux $arch"
  GOOS=linux ARCH="$arch" VERSION="$VERSION" "$ROOT/scripts/build-agent.sh"
  bin="$ROOT/dist/agent/spadeide-linux-$arch"
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
ls -lh "$OUT_DIR"/spadeide_*.deb 2>/dev/null || true
echo "Server (LTS):"
ls -lh "$OUT_DIR"/server/spadeide-linux-*.tar.gz 2>/dev/null || true
