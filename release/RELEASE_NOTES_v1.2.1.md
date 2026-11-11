# SpadeIDE Agent 1.2.1

## Changes

- **Remote access on by default** — new installs register with the rendezvous without `spadeide remote on`. Existing installs that were still on the old default (off) are flipped on once at startup; an explicit `spadeide remote off` (CLI or tray) still sticks.
- Docs/CLI help updated to match.

## Upgrade

```bash
./reinstall.sh                # Linux tarball
# or: sudo apt install ./spadeide-agent_1.2.1_amd64.deb
```

macOS — replace SpadeIDE Agent from the new DMG.

Build: `./scripts/build-release.sh 1.2.1`
