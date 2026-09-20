# Linux agent

One command on the host: **`spadeide`**.

| Audience | Artifact | Install |
|----------|----------|---------|
| Ubuntu / Debian **desktop** | `.deb` | `sudo apt install ./spadeide_*.deb` |
| **Server / LTS** | tarball or bare binary | `./install.sh` or copy to `/usr/local/bin/spadeide` |

## Day-to-day

```bash
spadeide              # start if needed → QR + pair URL + tablet list
spadeide status       # paired / online (no new QR)
spadeide devices      # list tablets
spadeide unpair NAME  # remove one tablet (id prefix or name)
spadeide remote off   # default — never dial the internet introducer
spadeide remote on    # allow internet reachability when rendezvous is wired
spadeide stop
```

Several iPads can stay paired. Each bare `spadeide` issues a fresh pair secret
for a new tablet; already-paired ones stay. ● = online, ○ = paired offline.

On the iPad: SpadeIDE → paste or scan the URL → Open Project.

## Copy one binary (Ubuntu Server)

```bash
# from your Mac after make linux / build-agent:
scp dist/agent/spadeide-linux-amd64 user@server:~/

ssh user@server
chmod +x spadeide-linux-amd64
sudo mv spadeide-linux-amd64 /usr/local/bin/spadeide
spadeide
```

## Build

```bash
make linux
```

```
dist/linux/
  spadeide_0.1.0_amd64.deb
  spadeide_0.1.0_arm64.deb
  server/
    spadeide-linux-amd64.tar.gz
    spadeide-linux-arm64.tar.gz
dist/agent/
  spadeide-linux-amd64      # bare binary
  spadeide-linux-arm64
```

## Desktop .deb

```bash
sudo apt install ./spadeide_0.1.0_amd64.deb
spadeide
```

## Server tarball

```bash
tar -xzf spadeide-linux-amd64.tar.gz
cd spadeide-linux-amd64
./install.sh
sudo loginctl enable-linger "$USER"   # keep user service after logout
spadeide
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

systemd units call `spadeide serve --foreground`. Do not run as root.

## Windows

Later. See [plans/windows-agent.md](plans/windows-agent.md).
