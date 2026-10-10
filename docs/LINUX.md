# Linux agent

One command on the host: **`spadeide`**.

| Audience | Artifact | Install |
|----------|----------|---------|
| Ubuntu / Debian **desktop** | `.deb` | `sudo apt install ./spadeide-agent_*.deb` → app grid |
| **Server / LTS** (one person) | tarball | `./install.sh` |
| **Shared host** (several Unix accounts) | tarball | `sudo ./install.sh --system` |

Prefer a **user** install unless you need multi-user. A system install is a
privileged broker (`User=root`, state in `/var/lib/spade-agent`); each iPad
Unix-logs in once and is jailed to that account’s `$HOME`.

## Choose install

| Situation | Install | On the iPad |
|-----------|---------|-------------|
| Only you; desktop | `.deb` → SpadeIDE Agent from the app grid | Scan QR → open a folder under your home. No Unix password. |
| Only you; server | `./install.sh` | Same — pair once, only your `$HOME`. |
| Several people / bind tablet to a Unix account | `sudo ./install.sh --system` | Scan QR → **Unix Login** once → that home only. |

## Desktop

```bash
sudo apt install ./spadeide-agent_0.1.0_amd64.deb
```

Then open **SpadeIDE Agent** (icon in Activities / the app menu). It:

- Starts the agent if needed
- Shows a tray icon (AppIndicator) and a window with the pair QR
- Lists paired iPads, toggles remote access, copies the pair URL

`spadeide desktop` opens the same window from a terminal. Closing the window
hides it to the tray; **Quit** stops the UI (the agent can keep running via
systemd). **Launch at login** controls whether the tray comes back at sign-in.

The headless tarball does **not** include the GUI — that stays a CLI + systemd.

Default `.deb` is a user unit (your account). To switch to the privileged
broker, stop the user unit and enable the system unit — see
`/usr/share/doc/spadeide-agent/README.Debian`.

## Day-to-day

```bash
spadeide              # start if needed → QR + pair URL + tablet list
spadeide desktop      # tray + window (needs the .deb)
spadeide status       # paired / online (no new QR)
spadeide devices      # list tablets
spadeide unpair NAME  # remove one tablet (id prefix or name)
spadeide remote off   # default — never dial the internet introducer
spadeide remote on    # allow internet reachability when rendezvous is wired
spadeide stop
```

System install QR (do not start a second agent):

```bash
sudo spadeide pair --no-start
```

Upgrade from a new tarball (keeps pairings, same user/system layout):

```bash
./reinstall.sh          # or: sudo ./reinstall.sh  if it was --system
```

Several iPads can stay paired. Each bare `spadeide` issues a fresh pair secret
for a new tablet; already-paired ones stay. ● = online, ○ = paired offline.

### On the iPad

1. **Pair** — scan the QR (device identity; not a Unix password).
2. **Unix Login** (system install only) — username + password over the encrypted
   session; never stored. Unix `root` is refused. Wrong password disconnects.
3. **Open a project** — folder under that user’s home. Terminal is that user’s
   normal login shell (same groups as SSH — e.g. `docker` if they are in the group).

Later connects skip Unix login until the account is deleted, locked, or its
password changes. Re-pairing is only needed if you wipe agent identity
(`~/.spade-agent` or `/var/lib/spade-agent`).

## Copy one binary (Ubuntu Server)

```bash
# from your Mac after make linux / build-agent:
scp dist/agent/spadeide-agent-linux-amd64 user@server:~/

ssh user@server
chmod +x spadeide-agent-linux-amd64
sudo mv spadeide-agent-linux-amd64 /usr/local/bin/spadeide
spadeide
```

## Build

```bash
make linux
```

```
dist/linux/
  spadeide-agent_0.1.0_amd64.deb
  spadeide-agent_0.1.0_arm64.deb
  server/
    spadeide-agent-linux-amd64.tar.gz
    spadeide-agent-linux-arm64.tar.gz
dist/agent/
  spadeide-agent-linux-amd64      # bare binary
  spadeide-agent-linux-arm64
```

## Desktop .deb

```bash
sudo apt install ./spadeide-agent_0.1.0_amd64.deb
# app grid → SpadeIDE Agent   (or: spadeide desktop)
```

## Server tarball

```bash
tar -xzf spadeide-agent-linux-amd64.tar.gz
cd spadeide-agent-linux-amd64
./install.sh                         # first time — this account, jailed to $HOME
sudo loginctl enable-linger "$USER"   # keep user service after logout
spadeide
# or a privileged broker (risky): each iPad Unix-logs in once
# sudo ./install.sh --system
# sudo spadeide pair --no-start

# later upgrades (keeps pairings, same user/system layout):
./reinstall.sh                       # or: sudo ./reinstall.sh
```

Firewall: `sudo ufw allow 7422/tcp` and `sudo ufw allow 7422/udp` (QUIC). For LAN discovery (iPad finds the host
after IP changes): `sudo apt install avahi-daemon avahi-utils` — the agent
registers `_spadeide._tcp` through avahi when `avahi-publish-service` is on
`PATH`; without it, it falls back to an embedded mDNS responder (unreliable
next to avahi-daemon).

If the host also runs Docker, Avahi may advertise bridge IPs (`172.17.x.x`)
that the iPad cannot reach. Restrict interfaces in `/etc/avahi/avahi-daemon.conf`:

```
[server]
allow-interfaces=enp3s0
# or: deny-interfaces=docker0,br-*,veth*
```

Then `sudo systemctl restart avahi-daemon`.

systemd units call `spadeide serve --foreground`. Interactive `sudo spadeide` is
refused. A system install is the systemd unit (`User=root`, state in
`/var/lib/spade-agent`) — same on Ubuntu Desktop or a headless box. Auth uses
`unix_chkpwd` from `libpam-modules` (default on Ubuntu). Site walkthrough:
Docs → Linux agent (`agent-linux.html`).

## Windows

Later. See [plans/windows-agent.md](plans/windows-agent.md).
