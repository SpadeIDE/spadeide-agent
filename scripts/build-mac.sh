#!/usr/bin/env bash
# Pack SpadeIDE Agent.app into a KubeSpade-style DMG
# (.app + Applications shortcut + volume icon, never a naked CLI).
#
# Usage:
#   ./scripts/build-mac.sh                  # local: arm64, ad-hoc
#   ARCH=amd64 ./scripts/build-mac.sh       # explicit Intel (still ad-hoc unless MAC_RELEASE=1)
#   MAC_RELEASE=1 ./scripts/build-mac.sh    # release: arm64+amd64, Developer ID + notarize
#
# Requires the sibling app repo (menu-bar Agent.app):
#   SPADEIDE_DIR  default: ../spadeide
#
# Env:
#   VERSION                 default 0.1.0
#   ARCH                    arm64 (default local) | amd64 | all
#   MAC_RELEASE=1           both arches + Developer ID + notarize (set by build-release.sh)
#   SKIP_MAC_SIGN=1         force ad-hoc even when MAC_RELEASE=1
#   CSC_NAME / SIGN_ID      Developer ID identity
#   APPLE_ID / APPLE_APP_SPECIFIC_PASSWORD / APPLE_TEAM_ID
#   ALLOW_UNSIGNED_RELEASE=1
#
# Output:
#   dist/mac/spadeide-agent-macos-arm64.dmg
#   dist/mac/spadeide-agent-macos-amd64.dmg   (release / ARCH=all only)
#
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=release-checks.sh
source "$ROOT/scripts/release-checks.sh"
release_load_env "$ROOT"

OUT="${OUT_DIR:-$ROOT/dist/mac}"
VERSION="${VERSION:-0.1.0}"
APP_REPO="${SPADEIDE_DIR:-$ROOT/../spadeide}"
VOLNAME="SpadeIDE Agent"
APP_NAME="SpadeIDE Agent.app"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "error: macOS builds require a Mac host" >&2
  exit 1
fi
if [[ ! -x "$APP_REPO/scripts/build-mac.sh" ]]; then
  echo "error: SpadeIDE Agent.app sources not found at $APP_REPO" >&2
  echo "       Clone the app repo next to spadeide-agent, or set SPADEIDE_DIR." >&2
  exit 1
fi

if [[ "${MAC_RELEASE:-}" == "1" ]]; then
  raw="${ARCH:-all}"
else
  raw="${ARCH:-arm64}"
fi
case "$raw" in
  all|both) want=(arm64 amd64) ;;
  arm64|aarch64|"") want=(arm64) ;;
  amd64|x86_64|x64) want=(amd64) ;;
  *) echo "Unsupported ARCH=$raw (use arm64, amd64, or all)" >&2; exit 1 ;;
esac

MAC_SIGN_IDENTITY="-"
MAC_NOTARIZE=0

if [[ "${SKIP_MAC_SIGN:-}" == "1" ]]; then
  echo "==> SKIP_MAC_SIGN=1 — ad-hoc sign"
  MAC_SIGN_IDENTITY="-"
elif [[ "${MAC_RELEASE:-}" == "1" ]]; then
  echo "==> Release build (MAC_RELEASE=1) — Developer ID + notarize DMG"
  MAC_SIGN_IDENTITY="$(release_mac_developer_id)"
  if [[ -z "$MAC_SIGN_IDENTITY" ]]; then
    if [[ "${ALLOW_UNSIGNED_RELEASE:-}" == "1" ]]; then
      echo "    WARN: no Developer ID — ad-hoc (ALLOW_UNSIGNED_RELEASE=1)"
      MAC_SIGN_IDENTITY="-"
    else
      echo "ERROR: no Developer ID Application in Keychain" >&2
      exit 1
    fi
  else
    echo "    Mac signing: $MAC_SIGN_IDENTITY"
    MAC_NOTARIZE=1
    app_pw="${APPLE_APP_SPECIFIC_PASSWORD:-${APPLE_ID_PASSWORD:-}}"
    if [[ -z "${APPLE_ID:-}" || -z "$app_pw" || -z "${APPLE_TEAM_ID:-}" ]]; then
      if [[ "${ALLOW_UNSIGNED_RELEASE:-}" == "1" ]]; then
        echo "    WARN: notarization creds missing — sign only (ALLOW_UNSIGNED_RELEASE=1)"
        MAC_NOTARIZE=0
      else
        echo "ERROR: set APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD, APPLE_TEAM_ID in .env" >&2
        exit 1
      fi
    fi
  fi
else
  echo "==> Local build — arm64, ad-hoc (Developer ID + Intel: MAC_RELEASE=1 / ./scripts/build-release.sh)"
  MAC_SIGN_IDENTITY="-"
fi

mkdir -p "$OUT"

ensure_dmg_icon() {
  local icns="$APP_REPO/build/icons/dmg-icon.icns"
  if [[ ! -f "$icns" ]]; then
    echo "==> Building DMG / app icons"
    "$APP_REPO/scripts/make-icon.sh"
  fi
  [[ -f "$icns" ]] || { echo "ERROR: $icns missing after make-icon.sh" >&2; exit 1; }
  printf '%s' "$icns"
}

# KubeSpade recipe — see docs/MAC_DMG.md. Order is load-bearing.
pack_app_dmg() {
  local app="$1" dmg="$2"
  local icns stage rw mount attach_out final_tmp
  icns="$(ensure_dmg_icon)"
  stage="$(mktemp -d "${TMPDIR:-/tmp}/spadeide-dmg.XXXXXX")"
  rw="$(mktemp "${TMPDIR:-/tmp}/spadeide-rw.XXXXXX").dmg"
  final_tmp="${dmg%.dmg}-packed.dmg"

  shopt -s nullglob
  for stale in /Volumes/SpadeIDE*; do
    echo "    Ejecting leftover $stale …"
    hdi_detach_retry "$stale"
  done
  shopt -u nullglob
  if [[ -f "$dmg" ]]; then
    hdi_detach_retry "$dmg"
  fi
  if [[ -d "/Volumes/$VOLNAME" ]]; then
    echo "ERROR: could not eject /Volumes/$VOLNAME — close the window and retry" >&2
    exit 1
  fi

  xattr -cr "$app" 2>/dev/null || true
  cp -R "$app" "$stage/$APP_NAME"
  ln -s /Applications "$stage/Applications"

  hdiutil create -volname "$VOLNAME" \
    -srcfolder "$stage" \
    -ov -format UDRW \
    "$rw" >/dev/null

  attach_out=$(hdiutil attach -readwrite -noverify -noautoopen "$rw")
  mount="$(hdi_mount_point "$attach_out")"
  if [[ -z "$mount" || ! -d "$mount" ]]; then
    echo "ERROR: could not mount $rw" >&2
    hdiutil detach -force "$rw" 2>/dev/null || true
    rm -rf "$stage" "$rw"
    exit 1
  fi

  osascript <<OSA >/dev/null 2>&1 || echo "    WARN: Finder layout failed"
tell application "Finder"
  tell disk "$VOLNAME"
    open
    set current view of container window to icon view
    set toolbar visible of container window to false
    set statusbar visible of container window to false
    set the bounds of container window to {200, 120, 740, 500}
    set viewOptions to the icon view options of container window
    set arrangement of viewOptions to not arranged
    set icon size of viewOptions to 128
    set position of item "$APP_NAME" of container window to {130, 220}
    set position of item "Applications" of container window to {410, 220}
    close
    open
    update without registering applications
    delay 1
  end tell
end tell
OSA

  cp "$icns" "$mount/.VolumeIcon.icns"
  SetFile -c icnC "$mount/.VolumeIcon.icns"
  SetFile -a C "$mount"
  if [[ "$(GetFileInfo -aC "$mount" 2>/dev/null)" != "1" ]]; then
    echo "ERROR: custom-icon bit not set on $mount — volume would show the generic disk icon" >&2
    hdiutil detach -force "$mount" >/dev/null 2>&1 || true
    rm -rf "$stage" "$rw"
    exit 1
  fi

  sync
  hdi_detach_retry "$mount"

  rm -f "$final_tmp" "$dmg"
  hdiutil convert "$rw" -format UDZO -imagekey zlib-level=9 -o "$final_tmp" >/dev/null
  mv "$final_tmp" "$dmg"
  rm -f "$rw"
  rm -rf "$stage"
  xattr -cr "$dmg"

  if command -v Rez >/dev/null 2>&1; then
    local rsrc
    rsrc=$(mktemp -d)
    cp "$icns" "$rsrc/icon.icns"
    if sips -i "$rsrc/icon.icns" >/dev/null 2>&1 &&
       DeRez -only icns "$rsrc/icon.icns" > "$rsrc/icon.rsrc" 2>/dev/null &&
       Rez -append "$rsrc/icon.rsrc" -o "$dmg" 2>/dev/null; then
      SetFile -a C "$dmg"
      echo "    DMG file icon applied"
    else
      echo "    WARN: could not brand the .dmg file icon"
    fi
    rm -rf "$rsrc"
  fi

  local verify_out verify_mount ok=1
  verify_out=$(hdiutil attach "$dmg" -noverify -noautoopen -nobrowse)
  verify_mount="$(hdi_mount_point "$verify_out")"
  if [[ -z "$verify_mount" || ! -d "$verify_mount" ]]; then
    echo "ERROR: could not mount final DMG $dmg" >&2
    hdi_detach_retry "$dmg"
    exit 1
  fi
  [[ -d "$verify_mount/$APP_NAME" ]] || { echo "ERROR: $APP_NAME missing in final DMG" >&2; ok=0; }
  [[ -f "$verify_mount/.VolumeIcon.icns" ]] || { echo "ERROR: .VolumeIcon.icns missing in final DMG" >&2; ok=0; }
  [[ -L "$verify_mount/Applications" ]] || { echo "ERROR: Applications symlink missing in final DMG" >&2; ok=0; }
  [[ "$(GetFileInfo -aC "$verify_mount" 2>/dev/null)" == "1" ]] || { echo "ERROR: custom-icon bit lost in final DMG" >&2; ok=0; }
  [[ ! -e "$verify_mount/README.txt" ]] || { echo "ERROR: README.txt must not be in the DMG" >&2; ok=0; }
  [[ ! -f "$verify_mount/spadeide" ]] || { echo "ERROR: naked CLI must not be in the DMG" >&2; ok=0; }
  if [[ -f "$verify_mount/$APP_NAME/Contents/Resources/Assets.car" ]] &&
     /usr/libexec/PlistBuddy -c "Print :CFBundleIconName" "$verify_mount/$APP_NAME/Contents/Info.plist" >/dev/null 2>&1; then
    :
  else
    echo "WARN: Assets.car or CFBundleIconName missing in $APP_NAME — Dock may show a generic icon" >&2
  fi
  hdi_detach_retry "$verify_mount"
  hdi_detach_retry "$dmg"
  [[ "$ok" == "1" ]] || exit 1
  echo "    OK (app + volume icon + Applications link verified)"
}

notarize_dmg() {
  local dmg="$1"
  [[ "$MAC_NOTARIZE" == "1" ]] || return 0
  [[ -f "$dmg" ]] || return 0

  local app_pw="${APPLE_APP_SPECIFIC_PASSWORD:-${APPLE_ID_PASSWORD:-}}"
  echo "    Submitting $(basename "$dmg") to Apple notarization …"
  xcrun notarytool submit "$dmg" \
    --apple-id "$APPLE_ID" \
    --password "$app_pw" \
    --team-id "$APPLE_TEAM_ID" \
    --wait

  echo "    Stapling ticket …"
  xcrun stapler staple "$dmg"
  xcrun stapler validate "$dmg"
}

build_one() {
  local arch="$1"
  local dmg="$OUT/spadeide-agent-macos-${arch}.dmg"
  local app_dst="$OUT/$APP_NAME"
  echo ""
  echo "==> Agent.app ($arch)"
  SIGN_ID="$MAC_SIGN_IDENTITY" VERSION="$VERSION" \
    ARCH="$arch" OUT_DIR="$OUT" MAC_RELEASE="${MAC_RELEASE:-}" \
    "$APP_REPO/scripts/build-mac.sh"
  [[ -d "$app_dst" ]] || { echo "ERROR: $app_dst missing after app build" >&2; exit 1; }

  pack_app_dmg "$app_dst" "$dmg"
  notarize_dmg "$dmg"
  if [[ "${MAC_RELEASE:-}" == "1" ]]; then
    release_verify_mac_app "$app_dst"
    release_verify_mac_dmg "$dmg"
  fi
  echo "    $dmg"
}

for a in "${want[@]}"; do
  build_one "$a"
done

rm -f "$OUT"/spadeide-agent-macos-*.zip "$OUT"/spadeide-macos-*.zip \
  "$OUT"/spadeide-macos-universal* \
  "$OUT"/spadeide-macos-arm64 "$OUT"/spadeide-macos-amd64

echo ""
echo "==> Done ($VERSION)"
ls -lh "$OUT"/spadeide-agent-macos-*.dmg 2>/dev/null | sed 's/^/  /'
echo "    App: $OUT/$APP_NAME"
