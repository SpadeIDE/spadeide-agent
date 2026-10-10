# SpadeIDE agent — wire protocol

All session RPC and events use one JSON **envelope** (WebSocket text frames, or
length-prefixed frames on QUIC/relay streams).

## Envelope

```json
{
  "id": "client-request-id",
  "method": "fs.list",
  "params": { },
  "ok": true,
  "result": { },
  "error": { "code": "forbidden", "message": "…" },
  "event": "pty.data"
}
```

- Requests carry `id` + `method` + optional `params`.
- Responses echo `id` with `ok` and either `result` or `error`.
- Server-pushed events set `event` + `params` (no `id`).

Error codes: `unauthorized`, `forbidden`, `not_found`, `invalid_params`,
`io_error`, `internal`, `not_paired`, `bad_secret`.

## Auth handshake (QUIC / relay control stream)

1. Client → `{ "method": "auth.challenge", "id": "…" }`
2. Server → `{ "ok": true, "result": { "nonce": "…" } }`
3. Client → `{ "method": "auth", "params": { "deviceId", "ts", "nonce", "sig" } }`  
   `sig` is base64 ASN.1 ECDSA-P256-SHA256 over the UTF-8 bytes of
   `AuthMessage` (must match `auth.AuthMessage` / iPad `DeviceIdentity`):

   ```
   spade-auth-v2\n
   <agent-fingerprint>\n
   <ts-decimal>\n
   <nonce>
   ```

   Newlines are literal `\n` (U+000A), not pipes. Version string is `spade-auth-v2`.
4. Server → `{ "ok": true }` or an error; then RPC frames may flow.

WSS prefers the TLS client certificate (SPKI must match the pinned `devicePub`).
When a cert is present and recognized, the proof may be omitted; otherwise the
challenge/proof path above applies on QUIC/relay.

## RPC methods

Canonical list: `protocol.AllMethods` in `agent/internal/protocol/envelope.go`.
The server handler table is tested against that slice — new methods must appear
in both places.

| Group | Methods |
|---|---|
| Session | `hello`, `ping`, `host.login` |
| Workspace | `workspace.roots`, `workspace.open` |
| FS | `fs.list`, `fs.read`, `fs.write`, `fs.mkdir`, `fs.delete`, `fs.rename`, `fs.copy`, `fs.move`, `fs.stat`, `fs.watch`, `fs.unwatch`, `fs.search`, `fs.grep`, `fs.replaceInFiles` |
| Git | `git.status`, `git.stage`, `git.unstage`, `git.discard`, `git.commit`, `git.push`, `git.pull`, `git.diff`, `git.branches`, `git.checkout`, `git.applyHunk` |
| PTY | `pty.create`, `pty.input`, `pty.resize`, `pty.close` |
| AI | `ai.list` |

## Events

- `pty.data` / `pty.exit` — terminal output and process exit
- `fs.changed` — watch notifications

## Streams (QUIC / relay mux)

Three logical streams after auth: **control** (auth, events), **rpc** (methods),
**pty** (pty events). WSS multiplexes everything on one socket.
