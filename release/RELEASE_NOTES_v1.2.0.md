# SpadeIDE Agent 1.2.0

## Changes

- **Extract on the host** — new RPC `fs.extract`. Unpacks `.zip`, `.tar`, `.tar.gz` / `.tgz`, and `.tar.bz2` into a sibling folder named after the archive stem (`foo.tar.gz` → `foo/`). Uses host `unzip` / `tar`; members with path traversal are rejected before extract.
- **Compress on the host** — new RPC `fs.archive` with `format` `zip` or `tar.gz`. Writes a sibling archive next to the file or folder; collisions become `name copy.zip` / `name copy.tar.gz`.
- **Cancel in-flight archive work** — closing the agent (or starting another extract/compress) aborts the running host tool, same pattern as grep cancel.
- **Backward compatible** — SpadeIDE iPad **1.0.0** keeps working; it simply never calls the new methods. Extract / Compress in the Explorer need iPad **1.1.0+**.

## Upgrade

```bash
./reinstall.sh                # Linux tarball
# or: sudo apt install ./spadeide-agent_1.2.0_amd64.deb
```

macOS — replace SpadeIDE Agent from the new DMG.

Build: `./scripts/build-release.sh 1.2.0`
