#!/usr/bin/env python3
"""Write a Debian .deb (ar of debian-binary + control.tar.gz + data.tar.gz).

macOS BSD `ar` emits __.SYMDEF and is not valid for dpkg; this writer matches
the GNU ar layout dpkg expects. Usage:

  pack-deb.py OUT.deb debian-binary control.tar.gz data.tar.gz
"""
from __future__ import annotations

import sys
from pathlib import Path


def ar_member(name: str, data: bytes) -> bytes:
    # Classic GNU ar header (no filename table — names must fit in 16 bytes).
    if len(name) > 16:
        raise SystemExit(f"ar member name too long: {name!r}")
    header = (
        f"{name:<16}"
        f"{0:<12}"  # mtime
        f"{0:<6}"  # uid
        f"{0:<6}"  # gid
        f"{0o100644:<8o}"
        f"{len(data):<10}"
        "`\n"
    ).encode("ascii")
    body = data
    if len(body) % 2 == 1:
        body += b"\n"
    return header + body


def main() -> None:
    if len(sys.argv) != 5:
        print(__doc__, file=sys.stderr)
        raise SystemExit(2)
    out = Path(sys.argv[1])
    parts = [Path(p) for p in sys.argv[2:]]
    blob = b"!<arch>\n"
    for path in parts:
        blob += ar_member(path.name, path.read_bytes())
    out.write_bytes(blob)
    print(out)


if __name__ == "__main__":
    main()
