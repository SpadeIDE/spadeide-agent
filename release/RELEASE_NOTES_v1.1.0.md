# SpadeIDE Agent 1.1.0

## Changes

- **Per-device remote secret** — each paired iPad gets its own `rvSecret`. Unpairing one tablet only drops that tablet’s remote handle; others keep working.
- **Migration** — upgrades from 1.0.0 move the old host-wide secret onto devices, then mint unique secrets so one unpair cannot affect another tablet.
- **Rendezvous** — the agent registers handles for every paired device and refreshes them after pair / unpair / reset.

After upgrading with remote access on, pair each tablet once (or rely on migration) so it has its own secret.

## Upgrade

```bash
./reinstall.sh                # Linux tarball
# or: sudo apt install ./spadeide-agent_1.1.0_amd64.deb
```

macOS — replace SpadeIDE Agent from the new DMG.

Build: `./scripts/build-release.sh 1.1.0`
