# SpadeIDE Agent 0.1.0

Host agent for SpadeIDE (Linux + macOS). Pair with the iPad app on the same network or via rendezvous when remote access is on.

## Highlights

- **User vs system install (Linux)** — default install runs as your account and stays under `$HOME`. `sudo ./install.sh --system` is a privileged broker: each iPad Unix-logs in once, then files and the terminal stay in that user’s home (never as root).
- **`reinstall.sh`** — upgrade from a new tarball without losing pairings; detects user vs system layout.
- **Remote access** — off by default; `spadeide remote on` registers with the rendezvous. Blinded hourly handles; Direct · QUIC when punch works, otherwise Relay.
- **QUIC on the LAN** — multiplexed streams on UDP `:7422` (open UDP alongside TCP); WebSocket fallback remains.
- **Pairing** — stable agent identity (`agentId`), per-device unpair, pair codes that last until used or 10 minutes.

## Install

**Ubuntu / Debian desktop**

```bash
sudo apt install ./spadeide-agent_0.1.0_amd64.deb   # or arm64
```

**Server / LTS**

```bash
tar xzf spadeide-agent-0.1.0-linux-amd64.tar.gz
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

See `SHA256SUMS.txt` in this release. Matching rendezvous build recommended for remote access.
