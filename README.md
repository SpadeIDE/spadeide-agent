# SpadeIDE agent

Host agent for [SpadeIDE](https://github.com/SpadeIDE) — the Go daemon that the iPad app pairs with. One command on the host starts it and prints a pairing QR / URL.

**License:** Apache License 2.0 (see [LICENSE](LICENSE)).

## What this is

- Local TLS listener (WSS + QUIC) for paired tablets
- Filesystem sandbox, PTY, git helpers, optional remote access via a rendezvous introducer
- Linux packages: `.deb` (Ubuntu/Debian) and headless tarballs

The iPad app and macOS menu-bar Agent.app live in a separate private repository. This repo is the open host backend.

## Requirements

- Go 1.26+
- For Linux packages: bash, tar, gzip; `dpkg-deb` optional (a pure-Python packer is included for macOS build hosts)

## Build

```bash
make agent          # this machine → dist/agent/spade-agent
make linux          # .deb + server tarballs for amd64 and arm64
make linux-deb      # Ubuntu/Debian packages only
make linux-server   # headless tarballs only
make release        # build + stage github-release/ (drag onto GitHub Release)
make test
```

### GitHub Release

Same flow as KubeSpade desktop: build once, drop the folder onto the release.

```bash
./scripts/build-release.sh 0.2.0
# → github-release/
#      spadeide_0.2.0_amd64.deb
#      spadeide_0.2.0_arm64.deb
#      spadeide-0.2.0-linux-amd64.tar.gz
#      spadeide-0.2.0-linux-arm64.tar.gz
#      SHA256SUMS.txt
```

Create a GitHub release tagged `v0.2.0` and drag everything from `github-release/` onto the assets.

Cross-compile the CLI only:

```bash
GOOS=linux ARCH=amd64 ./scripts/build-agent.sh
```

On Linux the shipped binary name is `spadeide`.

## Install (Linux)

From a built package:

```bash
# Ubuntu / Debian
sudo apt install ./dist/linux/spadeide_0.1.0_amd64.deb
spadeide

# Server tarball
tar xzf dist/linux/server/spadeide-linux-amd64.tar.gz
cd spadeide-linux-amd64
./install.sh                 # user install
sudo ./install.sh --system   # dedicated user, boot start
```

See [docs/LINUX.md](docs/LINUX.md) for ports, systemd, discovery, and remote access.

## Module

Go module path: `github.com/spadeide/spade-agent`
