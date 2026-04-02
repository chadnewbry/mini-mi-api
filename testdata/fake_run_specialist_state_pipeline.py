#!/usr/bin/env python3

from __future__ import annotations

import pathlib
import sys


MINIMAL_GIF = b"GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"
MINIMAL_PNG = bytes([
    0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
    0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
    0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
    0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
    0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
    0x54, 0x78, 0x9C, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
    0x00, 0x03, 0x01, 0x01, 0x00, 0xC9, 0xFE, 0x92,
    0xEF, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E,
    0x44, 0xAE, 0x42, 0x60, 0x82,
])


def read_arg(name: str) -> str:
    args = sys.argv[1:]
    for index, arg in enumerate(args):
        if arg == name and index + 1 < len(args):
            return args[index + 1]
    raise SystemExit(f"missing {name}")


def main() -> int:
    output_root = pathlib.Path(read_arg("--output-root"))
    state = read_arg("--state")
    source_path = output_root / "main-agent" / state / "source.png"
    gif_path = output_root / "main-agent" / state / "transparent-frames" / "trimmed-transparent.gif"
    source_path.parent.mkdir(parents=True, exist_ok=True)
    gif_path.parent.mkdir(parents=True, exist_ok=True)
    source_path.write_bytes(MINIMAL_PNG)
    gif_path.write_bytes(MINIMAL_GIF)
    print(gif_path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
