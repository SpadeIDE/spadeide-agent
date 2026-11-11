# SpadeIDE agent

Host agent for [SpadeIDE](https://github.com/SpadeIDE) — the Go daemon that the iPad app pairs with. One command on the host starts it and prints a pairing QR / URL.

**License:** Apache License 2.0 (see [LICENSE](LICENSE)).

## Documentation

End-user install, CLI, ports, and remote access live on the product site:

| Topic | URL |
|---|---|
| Docs hub | https://www.spadeide.com/docs/ |
| Linux agent | https://www.spadeide.com/docs/agent-linux.html |
| macOS agent | https://www.spadeide.com/docs/agent-macos.html |
| Downloads | https://www.spadeide.com/download.html |

Prefer the `www` host (apex redirects there). In-repo guides are below if you want sources next to the code.

In this repo:

- [Linux](docs/LINUX.md) — `.deb` / tarball, user vs system, firewall, Avahi
- [macOS DMG](docs/MAC_DMG.md) — signed/notarized Agent.app packaging
- [Architecture](docs/ARCHITECTURE.md) · [Protocol](docs/PROTOCOL.md) · [C4 PTY throughput](docs/C4_PTY_THROUGHPUT.md)

## What this is

- Local TLS listener (WSS + QUIC) for paired tablets
- Filesystem sandbox, PTY, git helpers, optional remote access via a rendezvous introducer
- Linux: `.deb` (Ubuntu/Debian **desktop app** + CLI) and headless tarballs
- macOS: **SpadeIDE Agent.app** in a branded DMG

The iPad app lives in a separate repository. This repo is the open host backend.

## Requirements (runtime)

- **Linux** — Ubuntu/Debian (amd64 or arm64). Desktop wants a tray (AppIndicator); servers are fine headless.
- **macOS** — Apple silicon or Intel; download the matching DMG.
- Firewall: allow **7422/tcp** and **7422/udp** for LAN QUIC (WebSocket over TLS still works on TCP alone).
- LAN discovery (Linux): `avahi-daemon` + `avahi-utils` recommended.

## Install

Download artifacts from a [GitHub Release](https://github.com/SpadeIDE/spadeide-agent/releases) (or `github-release/` after `./scripts/build-release.sh 1.0.0`). Verify with `SHA256SUMS.txt`:

```bash
shasum -a 256 -c SHA256SUMS.txt
```

### macOS

1. Open `spadeide-agent-1.0.0-macos-arm64.dmg` (or `-amd64.dmg`).
2. Drag **SpadeIDE Agent** to Applications and launch it.
3. Grant folder access when asked (once per signing identity).
4. Scan the QR from the menu-bar window with the iPad app.

Optional: enable **Open at Login** in the agent window. Details: [docs site → macOS](https://www.spadeide.com/docs/agent-macos.html) · [docs/MAC_DMG.md](docs/MAC_DMG.md).

### Linux — Ubuntu / Debian desktop (`.deb`)

```bash
sudo apt install ./spadeide-agent_1.0.0_amd64.deb   # or arm64
```

Then open **SpadeIDE Agent** from the app grid (tray + QR), or:

```bash
spadeide desktop
```

Default layout is a **user** install (this account, `$HOME` only). See [docs/LINUX.md](docs/LINUX.md) if you need the system broker.

### Linux — server / LTS (tarball, one user)

```bash
tar xzf spadeide-agent-1.0.0-linux-amd64.tar.gz
cd spadeide-agent-linux-amd64
./install.sh
sudo loginctl enable-linger "$USER"   # keep the user service after logout
spadeide                              # QR + pair URL
```

### Linux — shared host (tarball, several Unix accounts)

Privileged broker: each iPad Unix-logs in once, then stays jailed to that user’s `$HOME` (PTY never runs as root).

```bash
tar xzf spadeide-agent-1.0.0-linux-amd64.tar.gz
cd spadeide-agent-linux-amd64
sudo ./install.sh --system
sudo spadeide pair --no-start         # QR only — do not start a second agent
```

Interactive `sudo spadeide` without `--no-start` is refused so you don’t bind a second copy as root.

### Which Linux install?

| Situation | Install |
|---|---|
| Only you, desktop | `.deb` → app grid |
| Only you, server | `./install.sh` |
| Several people / bind tablet to a Unix account | `sudo ./install.sh --system` |

## Reinstall / upgrade

Pairings and agent identity stay on disk. Replace the binary (and packages) with a newer release.

### Linux tarball

From the **new** unpacked release directory (same machine that already has an install):

```bash
./reinstall.sh                # user install
# sudo ./reinstall.sh         # if the previous install was --system
```

`reinstall.sh` detects user vs system layout, stops the agent, installs the new binary the same way, and keeps `~/.spade-agent` or `/var/lib/spade-agent`.

### Linux `.deb`

```bash
sudo apt install ./spadeide-agent_1.0.0_amd64.deb   # or arm64
```

`apt` upgrades in place. Open the app or run `spadeide` afterward.

### macOS

Download the new DMG, drag **SpadeIDE Agent** onto Applications (replace), quit the old agent from the menu bar if it’s still running, then launch again. Pairings under the app’s state directory are preserved.

### After upgrade

```bash
spadeide status               # Linux CLI
# or check the tray / menu-bar window
```

Re-pair only if you wiped identity (`~/.spade-agent` / `/var/lib/spade-agent` on Linux, or reset the Mac app). Remote access is **on by default** after install/upgrade (tray toggle or `spadeide remote off` to disable). Pair once so the iPad gets its own `rvSecret`. Unpairing one tablet drops only that tablet’s remote handle — others keep working. After upgrading from a host-wide secret, pair each tablet once so it receives a unique secret.

## Day-to-day (Linux CLI)

```bash
spadeide              # start if needed → QR + pair URL + tablet list
spadeide desktop      # tray + window (.deb)
spadeide status
spadeide devices
spadeide unpair NAME  # id prefix or name
spadeide remote on|off|status
spadeide stop
```

## Build (from source)

Requires Go 1.26+. For signed macOS DMGs, a Mac with Developer ID + notarization creds (see `.env.example`).

```bash
make agent          # this machine → dist/agent/spade-agent
make linux          # .deb + server tarballs for amd64 and arm64
make linux-deb
make linux-server
make mac            # arm64 Agent.app DMG (ad-hoc) → dist/mac/
make release        # Linux + arm64+amd64 signed/notarized macOS → github-release/
make test
```

Release cut:

```bash
cp .env.example .env          # APPLE_ID / app password / TEAM_ID for Mac
./scripts/build-release.sh 1.0.0
# → github-release/  (.deb, tarballs, DMGs, SHA256SUMS.txt)
```

Dev without Apple certs: `ALLOW_UNSIGNED_RELEASE=1 ./scripts/build-release.sh 1.0.0`  
Linux only: `./scripts/build-linux.sh amd64 1.0.0` (appends into `github-release/`).

See [docs/MAC_DMG.md](docs/MAC_DMG.md). On Linux the shipped binary name is `spadeide`; on macOS the download is **SpadeIDE Agent.app**.

## Module layout

Go module path: `github.com/spadeide/spade-agent`.

All library code lives under `internal/` **by design** — this repo is published for auditability and packaging, not as an importable SDK. Depend on the `spadeide` / SpadeIDE Agent.app binaries, not on Go packages from this module.

## Environment

| Variable | Purpose |
|---|---|
| `SPADE_AGENT_HOME` | Override config directory |
| `SPADE_AGENT_MODE` | `system` → `/var/lib/spade-agent` |
| `SPADE_AGENT_PORT` | WSS/QUIC listen port (default `7422`) |
| `SPADE_AGENT_CONTROL_PORT` | Loopback control API port |
| `SPADE_AGENT_DAEMON` | `1` when re-exec’d as background daemon |
| `SPADE_DISABLE_MDNS` | `1` skips LAN mDNS advertisement |
| `SPADE_CONTROL_TOKEN` | Override `control.token` for the control API |
| `SPADE_RENDEZVOUS_URL` | Override product rendezvous WebSocket URL |
| `INVOCATION_ID` | Set by systemd; with euid 0 selects system mode |

Accessors: `internal/config` (`EnvHome`, `EnvPort`, …).
