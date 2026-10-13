# C4 — PTY throughput

**Status: done.** Measured on paired iPad (LAN), 2026-10 (pre/post same device
and agent build pair). Code: 32 KiB read + ~5 ms coalesce, flush on deadline /
full buffer / EOF.

## Behaviour

`readLoop` reads up to 32 KiB, holds pending bytes briefly (~5 ms) to batch more
output into one `pty.data` envelope, and flushes on deadline, full buffer, or
EOF. Interactive keystrokes still flush within one coalesce window.

## Measurement (iPad, LAN)

```bash
time yes | head -c 500000
```

| | Baseline (pre-C4) | After |
|---|---|---|
| Shell `time` | ~2.3 s | ~2.3 s (unchanged — host-side) |
| UI drain (stopwatch to quiet terminal) | ~20 s | &lt;3 s |
| Reconnect on 2–50 MiB flood | often | none on 5 MB |

Also typed after the flood — echo still immediate (coalesce window only).

Optional larger stress (ran after 500 KB stayed stable):

```bash
time yes | head -c 2000000
```

Completed without session drop.

## Pass / fail (recorded)

- UI drain clearly faster than ~20 s for 500 KB — **pass** (&lt;3 s).
- No reconnect on sizes that used to drop the session — **pass** (5 MB).
- Typing lag not worse than before — **pass**.

## Out of scope

- Changing envelope format / binary frames
- Shared ring buffer with the iPad decoder
