# macOS public download = SpadeIDE Agent.app in a DMG

Never ship a naked Mach-O, a zip, or `README.txt` + generic `exec` for Mac.
The public asset is **SpadeIDE Agent.app** inside a branded `.dmg`, packed the
same way as KubeSpade (`kubespade-desktop/build-mac.sh`).

| Item | Role |
|------|------|
| `SpadeIDE Agent.app` | Menu-bar app. Dock / Finder icon from the AppIcon catalog (`Assets.car` + `CFBundleIconName=AppIcon`). |
| `Applications` → `/Applications` | Drag-to-install shortcut. |
| `.VolumeIcon.icns` + custom-icon bit | Volume icon when the DMG is open. |
| Finder icon-view layout | App left (`{130, 220}`), Applications right (`{410, 220}`). |

Filenames: `spadeide-agent-${VERSION}-macos-arm64.dmg` and `-amd64.dmg`. **No universal DMG.**

## Two different icons

- **App (Dock / Finder of the .app)** — opaque full-bleed AppIcon in
  `spadeide/macos/SpadeIDEAgent/Assets.xcassets`. Xcode compiles `Assets.car`.
  Do **not** bake transparent corners; macOS applies the squircle.
- **DMG volume (and the .dmg file in Downloads)** — `spadeide/build/icons/dmg-icon.icns`
  with **transparent rounded corners**. Built by `spadeide/scripts/make-icon.sh`.
  Do **not** reuse the opaque app icns.

## Who builds what

1. Sibling app repo `../spadeide` (`SPADEIDE_DIR`): `scripts/build-mac.sh` produces
   `SpadeIDE Agent.app`. Local: arm64, ad-hoc. Release (`MAC_RELEASE=1`): Developer ID,
   helper first then the bundle, `--options runtime --timestamp`. Do **not** `--deep`.
2. This repo: `scripts/build-mac.sh` calls that, then packs the DMG (recipe below).
   Local default is arm64 only. Both arches + notarize happen only in release.
3. `scripts/build-release.sh` sets `MAC_RELEASE=1` (arm64+amd64, Developer ID, notarytool, staple)
   and stages `github-release/`.

Same Developer ID / team as KubeSpade is fine.

## Pack recipe (order matters)

Source of truth in KubeSpade: after electron-builder, they **repack** from the
signed `.app`. Copy that sequence; do not invent a zip or a CLI disk.

1. Eject stale `/Volumes/SpadeIDE Agent` so hdiutil does not mount as
   `"SpadeIDE Agent 1"` (AppleScript would then talk to the wrong disk).
2. Stage: `xattr -cr` the app, copy `.app`, `ln -s /Applications Applications`.
   Nothing else (no README, no naked `spadeide` binary).
3. `hdiutil create -volname "SpadeIDE Agent" -srcfolder STAGE -format UDRW`.
4. Attach read-write. **Finder AppleScript first** (icon view, 128px, positions,
   close + open so `.DS_Store` sticks). Layout *before* icon bits: Finder rewrite
   of `.DS_Store` can clobber attributes set beforehand.
5. Copy `dmg-icon.icns` → `.VolumeIcon.icns`. `SetFile -c icnC` on the icns.
   `SetFile -a C` on the **volume**. Fail if `GetFileInfo -aC` is not `1`.
   Never wrap this in `|| true`.
6. `sync`, detach, convert **UDZO** (`zlib-level=9`).
7. `xattr -cr` the `.dmg` file. **Then** brand the document icon (resource fork):
   `sips -i` + `DeRez -only icns` + `Rez -append` + `SetFile -a C` on the **.dmg**.
   `xattr -cr` after Rez would strip the fork.
8. Re-attach the **final** UDZO and verify: `.app`, `.VolumeIcon.icns`,
   Applications symlink, custom-icon bit. Fail the build if any is missing.
9. Release only: `notarytool submit --wait`, `stapler staple`, `stapler validate`.
   Notarize the **DMG**, not a zip of a CLI.

## Pitfalls already paid for in KubeSpade

- Stale `/Volumes/Name` makes the next attach land on `"Name 1"` and AppleScript misses it. Eject every matching volume first.
- `hdiutil attach` columns are tab-separated; the mount path may contain spaces. Parse the last tab field, not the last space-separated word.
- Volume icon was applied, then Finder AppleScript rewrote `.DS_Store` and dropped it. Layout first, icon second.
- `SetFile` wrapped in `|| true` hid failures → generic disk icon.
- `xattr -cr` after Rez stripped the .dmg file icon.
- Stale volume of the same name → AppleScript targeted `"Name 1"`.
- Tahoe Dock uses `Assets.car` + `CFBundleIconName`, not a loose `.icns`.
- Missing `dmg-icon.icns` → run `../spadeide/scripts/make-icon.sh`.

## Commands

```bash
# local — arm64, ad-hoc
./scripts/build-mac.sh

# public — both arches, Developer ID + notarize
./scripts/build-release.sh 1.0.0
```
