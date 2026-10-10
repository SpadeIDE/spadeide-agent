#!/usr/bin/env bash
# Shared release preflight + checks for build-release.sh / build-linux.sh / build-mac.sh
#
# Set ALLOW_UNSIGNED_RELEASE=1 to skip hard failures (local/dev only).

# Last existing directory path in `hdiutil attach` output.
# Columns are tab-separated; the mount point may contain spaces
# (`/Volumes/SpadeIDE Agent`). macOS 26+ with -nobrowse can also mount
# under $TMPDIR. The APFS container line has no path — the inner volume does.
hdi_mount_point() {
  local line cand path=""
  while IFS= read -r line || [[ -n "$line" ]]; do
    cand="$(printf '%s' "$line" | awk -F'\t' '{ print $NF }')"
    cand="${cand#"${cand%%[![:space:]]*}"}"
    cand="${cand%"${cand##*[![:space:]]}"}"
    if [[ "$cand" == /* && -d "$cand" ]]; then
      path="$cand"
    fi
  done <<< "$1"
  printf '%s' "$path"
}

hdi_detach_retry() {
  local target="$1" i
  [[ -n "$target" ]] || return 0
  for i in 1 2 3 4 5; do
    hdiutil detach "$target" >/dev/null 2>&1 && return 0
    sleep 1
  done
  hdiutil detach "$target" -force >/dev/null 2>&1 || true
}

release_load_env() {
  local root="$1"
  if [[ -f "$root/.env" ]]; then
    set -a
    # shellcheck disable=SC1091
    source "$root/.env"
    set +a
  fi
}

release_mac_developer_id() {
  if [[ -n "${CSC_NAME:-}" ]]; then
    printf '%s' "$CSC_NAME"
    return 0
  fi
  if [[ -n "${SIGN_ID:-}" && "${SIGN_ID}" != "-" ]]; then
    printf '%s' "$SIGN_ID"
    return 0
  fi
  security find-identity -v -p codesigning 2>/dev/null \
    | awk -F'"' '/Developer ID Application/ { print $2; exit }'
}

release_preflight_mac() {
  [[ "${SKIP_MAC:-}" == "1" ]] && return 0
  [[ "${SKIP_MAC_SIGN:-}" == "1" ]] && return 0
  [[ "${ALLOW_UNSIGNED_RELEASE:-}" == "1" ]] && return 0

  local id
  id="$(release_mac_developer_id)"
  if [[ -z "$id" ]]; then
    echo "ERROR: Mac release needs Developer ID Application in Keychain." >&2
    echo "       See .env.example — or ALLOW_UNSIGNED_RELEASE=1 for dev." >&2
    exit 1
  fi

  local app_pw="${APPLE_APP_SPECIFIC_PASSWORD:-${APPLE_ID_PASSWORD:-}}"
  if [[ -z "${APPLE_ID:-}" || -z "$app_pw" || -z "${APPLE_TEAM_ID:-}" ]]; then
    echo "ERROR: Mac notarization creds missing." >&2
    echo "       Set APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD, APPLE_TEAM_ID in .env" >&2
    exit 1
  fi

  echo "    Mac release preflight OK (Developer ID + notarization creds)"
}

release_verify_mac_app() {
  local app="$1"
  [[ -d "$app" ]] || return 0

  if ! codesign --verify --strict --verbose=1 "$app" >/dev/null 2>&1; then
    echo "    ✗ codesign verify failed: $(basename "$app")" >&2
    if [[ "${ALLOW_UNSIGNED_RELEASE:-}" == "1" ]]; then
      echo "      ALLOW_UNSIGNED_RELEASE=1 — continuing anyway." >&2
      return 0
    fi
    exit 1
  fi

  local info
  info="$(codesign -dv --verbose=2 "$app" 2>&1 || true)"
  if echo "$info" | grep -q 'Signature=adhoc\|Authority=(adhoc)'; then
    echo "    ✗ Ad-hoc signature: $(basename "$app")" >&2
    if [[ "${ALLOW_UNSIGNED_RELEASE:-}" == "1" ]]; then
      echo "      ALLOW_UNSIGNED_RELEASE=1 — continuing anyway." >&2
      return 0
    fi
    exit 1
  fi
  if ! echo "$info" | grep -q 'Developer ID Application'; then
    echo "    ✗ Not Developer ID: $(basename "$app")" >&2
    if [[ "${ALLOW_UNSIGNED_RELEASE:-}" == "1" ]]; then
      echo "      ALLOW_UNSIGNED_RELEASE=1 — continuing anyway." >&2
      return 0
    fi
    exit 1
  fi

  if [[ -f "$app/Contents/Resources/Assets.car" ]] &&
     /usr/libexec/PlistBuddy -c "Print :CFBundleIconName" "$app/Contents/Info.plist" >/dev/null 2>&1; then
    echo "    ✓ Signed (Developer ID) + AppIcon: $(basename "$app")"
  else
    echo "    ✓ Signed (Developer ID): $(basename "$app")"
    echo "    WARN: Assets.car or CFBundleIconName missing — Dock may show a generic icon" >&2
  fi
}

release_verify_mac_bin() {
  local bin="$1"
  [[ -f "$bin" ]] || return 0

  if ! codesign --verify --strict --verbose=1 "$bin" >/dev/null 2>&1; then
    echo "    ✗ codesign verify failed: $(basename "$bin")" >&2
    if [[ "${ALLOW_UNSIGNED_RELEASE:-}" == "1" ]]; then
      echo "      ALLOW_UNSIGNED_RELEASE=1 — continuing anyway." >&2
      return 0
    fi
    exit 1
  fi

  local info
  info="$(codesign -dv --verbose=2 "$bin" 2>&1 || true)"
  if echo "$info" | grep -q 'Signature=adhoc\|Authority=(adhoc)'; then
    echo "    ✗ Ad-hoc signature: $(basename "$bin")" >&2
    if [[ "${ALLOW_UNSIGNED_RELEASE:-}" == "1" ]]; then
      echo "      ALLOW_UNSIGNED_RELEASE=1 — continuing anyway." >&2
      return 0
    fi
    exit 1
  fi

  if ! echo "$info" | grep -q 'Developer ID Application'; then
    echo "    ✗ Not Developer ID: $(basename "$bin")" >&2
    if [[ "${ALLOW_UNSIGNED_RELEASE:-}" == "1" ]]; then
      echo "      ALLOW_UNSIGNED_RELEASE=1 — continuing anyway." >&2
      return 0
    fi
    exit 1
  fi

  echo "    ✓ Signed (Developer ID): $(basename "$bin")"
}

# Same check KubeSpade uses for public DMGs.
release_verify_mac_dmg() {
  local dmg="$1"
  [[ -f "$dmg" ]] || return 0

  if ! xcrun stapler validate "$dmg" >/dev/null 2>&1; then
    echo "    ✗ Notarization ticket missing: $(basename "$dmg")" >&2
    echo "      DMG is not stapled — public downloads will be blocked by Gatekeeper." >&2
    if [[ "${ALLOW_UNSIGNED_RELEASE:-}" == "1" ]]; then
      echo "      ALLOW_UNSIGNED_RELEASE=1 — continuing anyway." >&2
      return 0
    fi
    exit 1
  fi

  local attach_out mount app ok=1
  attach_out=$(hdiutil attach "$dmg" -nobrowse -noverify -noautoopen 2>&1) || true
  mount="$(hdi_mount_point "$attach_out")"
  app=""
  if [[ -n "$mount" && -d "$mount" ]]; then
    app=$(find "$mount" -maxdepth 1 -name '*.app' -print -quit 2>/dev/null)
  fi

  if [[ -n "$app" && -d "$app" ]]; then
    if codesign --verify --strict "$app" >/dev/null 2>&1; then
      echo "    ✓ Notarized + signed: $(basename "$dmg") ($(basename "$app") inside)"
    else
      echo "    ✗ codesign failed for $(basename "$app") inside $(basename "$dmg")" >&2
      ok=0
    fi
    [[ -f "$mount/.VolumeIcon.icns" ]] || { echo "    ✗ .VolumeIcon.icns missing" >&2; ok=0; }
    [[ -L "$mount/Applications" ]] || { echo "    ✗ Applications symlink missing" >&2; ok=0; }
    [[ ! -f "$mount/spadeide" ]] || { echo "    ✗ naked CLI inside DMG" >&2; ok=0; }
  else
    echo "    ✗ No .app inside $(basename "$dmg")" >&2
    ok=0
  fi

  hdi_detach_retry "$mount"
  hdi_detach_retry "$dmg"

  if [[ "$ok" == "0" ]]; then
    if [[ "${ALLOW_UNSIGNED_RELEASE:-}" == "1" ]]; then
      echo "      ALLOW_UNSIGNED_RELEASE=1 — continuing anyway." >&2
      return 0
    fi
    exit 1
  fi
}

# Copy the first existing source file to dest_dir/dest_name.
release_stage() {
  local dest_dir="$1" dest_name="$2"
  shift 2
  local src="" f
  for f in "$@"; do
    [[ -f "$f" ]] || continue
    src="$f"
    break
  done
  if [[ -z "$src" ]]; then
    echo "    ERROR: no artifact for $dest_name (looked for: $*)" >&2
    return 1
  fi
  cp -f "$src" "$dest_dir/$dest_name"
  echo "    + $dest_name  ←  $(basename "$src")"
}

# SHA256SUMS.txt in dest_dir. sha256sum on Linux, shasum on macOS.
# Rewrites the sums file only — does not delete other artifacts.
release_write_sha256sums() {
  local dest_dir="$1"
  mkdir -p "$dest_dir"
  (
    cd "$dest_dir"
    tmp=".SHA256SUMS.txt.tmp"
    rm -f "$tmp"
    for f in *; do
      [[ -f "$f" && "$f" != SHA256SUMS.txt && "$f" != "$tmp" ]] || continue
      if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$f" >> "$tmp"
      else
        shasum -a 256 "$f" >> "$tmp"
      fi
    done
    if [[ -f "$tmp" ]]; then
      sort -k2 -o "$tmp" "$tmp"
      mv -f "$tmp" SHA256SUMS.txt
      echo "    SHA256SUMS.txt updated ($(wc -l < SHA256SUMS.txt | tr -d ' ') files)"
      cat SHA256SUMS.txt
    fi
  )
}

# Append Linux packages into github-release/ without wiping it (KubeSpade).
# SKIP_STAGE=1 no-ops — used by build-release.sh, which stages itself after a wipe.
# Usage: release_stage_linux dest dist version kind arch [arch...]
release_stage_linux() {
  local dest_dir="$1" dist_dir="$2" ver="$3" kind="$4"
  shift 4
  [[ "${SKIP_STAGE:-}" == "1" ]] && return 0
  mkdir -p "$dest_dir"
  echo ""
  echo "=== Staging → github-release/ (keep existing) ==="
  echo "    version ${ver}"
  local arch
  for arch in "$@"; do
    if [[ "$kind" == "all" || "$kind" == "both" || "$kind" == "" || "$kind" == "deb" ]]; then
      release_stage "$dest_dir" "spadeide-agent_${ver}_${arch}.deb" \
        "$dist_dir/spadeide-agent_${ver}_${arch}.deb" \
        "$dist_dir"/spadeide-agent_*_"${arch}".deb
    fi
    if [[ "$kind" == "all" || "$kind" == "both" || "$kind" == "" || "$kind" == "server" ]]; then
      release_stage "$dest_dir" "spadeide-agent-${ver}-linux-${arch}.tar.gz" \
        "$dist_dir/server/spadeide-agent-linux-${arch}.tar.gz"
    fi
  done
  echo ""
  echo "=== SHA256SUMS.txt ==="
  release_write_sha256sums "$dest_dir"
}
