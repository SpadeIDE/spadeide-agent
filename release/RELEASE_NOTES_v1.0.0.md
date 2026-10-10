# SpadeIDE Agent 1.0.0

First stable host agent for SpadeIDE (Linux + macOS). Pair with the iPad app on the same network or via rendezvous when remote access is on. Matching iPad + rendezvous builds recommended for remote and QUIC.

## Highlights

- **Faster terminal output** — PTY reads coalesce (32 KiB / ~5 ms) so large dumps no longer crawl the UI or drop the session; typing still flushes within one window.
- **QUIC on the LAN** — multiplexed streams on UDP `:7422` (control, file/git RPC, terminal). Open UDP alongside TCP; WebSocket over TLS remains the automatic fallback. Keepalive + idle timeout match the iPad so a dead UDP path reconnects instead of sitting green and frozen.
- **Remote access** — off by default (`spadeide remote on|off|status`). Blinded hourly handles; Direct · QUIC when punch works, otherwise Relay. Lookup no longer waits on UDP reflex before handing out a relay ticket; late Direct can still win.
- **User vs system install (Linux)** — default install stays under `$HOME`. `sudo ./install.sh --system` is a privileged broker: each iPad Unix-logs in once (`host.login`), then Explorer / editor / PTY stay in that user’s home (never as root). Unix `root` cannot log in. Interactive `sudo spadeide` refuses to start a second root copy; use `sudo spadeide pair --no-start` for the QR.
- **`reinstall.sh`** — upgrade from a new tarball without losing pairings; detects user vs system layout.
- **Pairing** — stable agent identity (`agentId`), device public key at pair time, per-device unpair (`spadeide devices` / `unpair`), pair codes that last until used or 10 minutes.
- **Git & search without freezing the session** — slow `fs.*` / `git.*` handlers run off the connection read loop; repo discovery skips heavy dirs and is time-capped so a huge tree cannot stall keystroke echo.
- **Sandbox** — symlink escape rejection, 32 MiB file caps, VPN-safe LAN IP in the pair URL, loopback control API (`:7423`) bearer-gated, single-instance lock on `serve`.
- **macOS** — Developer ID–signed Agent.app (arm64 by default), folder-access prompt once per signing identity, native Bonjour from the menu bar app.
- **Linux packages** — Ubuntu/Debian `.deb` (desktop tray + CLI) and headless tarballs for amd64 and arm64; one host command `spadeide`.

## Install

**Ubuntu / Debian desktop**

```bash
sudo apt install ./spadeide-agent_1.0.0_amd64.deb   # or arm64
```

**Server / LTS**

```bash
tar xzf spadeide-agent-1.0.0-linux-amd64.tar.gz
cd spadeide-agent-linux-amd64
./install.sh                  # this user
# sudo ./install.sh --system  # shared host
spadeide
```

**Upgrade**

```bash
./reinstall.sh                # or: sudo ./reinstall.sh
```

**macOS** — open the DMG for your architecture and drag SpadeIDE Agent to Applications.

## Notes

- Firewall: allow **7422/tcp** and **7422/udp** on the host for LAN QUIC.
- Remote: pair (or re-pair) so the iPad has an `rvSecret`; then `spadeide remote on`.
- See `SHA256SUMS.txt` in this release. Build with `./scripts/build-release.sh 1.0.0`.
