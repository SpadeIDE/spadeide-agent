# SpadeIDE agent — architecture

Host daemon the SpadeIDE iPad app pairs with. This document is for auditors and
operators; implementation lives under `agent/internal/` (not an importable SDK —
see README).

## Processes and ports

| Surface | Transport | Bind | Auth |
|---|---|---|---|
| Pairing HTTP | TLS TCP | `SPADE_AGENT_PORT` (default 7422) | QR secret → pin device public key (`devicePub`) |
| Session WSS | TLS WebSocket `/v1` | same port | client cert whose SPKI matches pinned `devicePub` |
| Session QUIC | QUIC+TLS, ALPN `spadeide` | same port (UDP) | control-stream challenge/proof (+ optional client cert SPKI check) |
| Control API | plain HTTP | `127.0.0.1:SPADE_AGENT_CONTROL_PORT` | bearer `control.token` |
| Rendezvous | outbound WSS | product or `SPADE_RENDEZVOUS_URL` | agent identity + optional blinded RID |

LAN discovery (optional mDNS) advertises the agent when `SPADE_DISABLE_MDNS` is unset.

## Trust boundaries

1. **Pairing** — short-lived QR secret. The iPad generates its own key (Secure
   Enclave) and sends `devicePub`; the agent **pins that public key** on the
   device record (`FindDeviceByPubSPKI` later). `PairResponse` does not contain
   a client certificate — the agent never issues one. The QR secret is burned.
2. **Session** — only paired devices. WSS requires a TLS client certificate
   whose SPKI matches the pinned key. QUIC/relay use `auth.challenge` →
   single-use nonce → ECDSA proof over `AuthMessage` (cert SPKI path preferred
   when present).
3. **Filesystem** — home is browse-only; `workspace.open` promotes one write
   root. Symlink escapes rejected via resolved real paths.
4. **Control API** — loopback only. Token in `control.token` (0600) or
   `SPADE_CONTROL_TOKEN`. Rate-limited.
5. **Remote path** — rendezvous introduces; agent dials relay (and optional UDP
   punch). Nested TLS over the relay pipe; same session auth as QUIC.
6. **System mode** — privileged broker at `/var/lib/spade-agent`; each device
   binds a Unix user via `host.login` before FS/PTY.

## Packages (high level)

- `internal/server` — WSS/QUIC/relay session lifecycle, RPC dispatch
- `internal/auth` — TLS identity, pairing ladder, session nonces/proofs
- `internal/fs` — sandboxed file ops, watch, search/grep/replace
- `internal/pty`, `internal/git`, `internal/ai` — terminal, git porcelain, AI CLI detect
- `internal/rendezvous` + `internal/remote` — registration dialer and offer handling
- `internal/control` — tray/CLI HTTP API
- `internal/config` — state store, env accessors, paths

## Config directory

Resolved by `config.DefaultDir()`:

1. `SPADE_AGENT_HOME` if set
2. `/var/lib/spade-agent` in system mode (`SPADE_AGENT_MODE=system` or root+systemd)
3. otherwise `~/.spade-agent`
