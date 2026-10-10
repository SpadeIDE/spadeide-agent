# C4 — PTY throughput

**Status: done.** Measured on paired iPad (LAN): 500 KB UI drain ~20 s → &lt;3 s;
5 MB completes without reconnect (pre-C4 often dropped the session).

## Behaviour

`readLoop` reads up to 32 KiB, holds pending bytes briefly (~5 ms) to batch more
output into one `pty.data` envelope, and flushes on deadline, full buffer, or
EOF. Interactive keystrokes still flush within one coalesce window.

## After-patch measurement (iPad, LAN)

Same as baseline — compare against your recorded numbers:

```bash
time yes | head -c 500000
```

| | Baseline (pre-C4) | After |
|---|---|---|
| Shell `time` | ~2.3 s | ? |
| UI drain (stopwatch) | ~20 s | ? |
| Reconnect on 2–50 MiB flood | often | ? |

Also type a few characters after the flood — echo should still feel immediate.

Larger optional stress (only if 500 KB stays stable):

```bash
time yes | head -c 2000000
```

## Pass / fail

- UI drain clearly faster than ~20 s for 500 KB, and/or no reconnect on sizes
  that used to drop the session.
- Typing lag not worse than before.

## Out of scope

- Changing envelope format / binary frames
- Shared ring buffer with the iPad decoder
