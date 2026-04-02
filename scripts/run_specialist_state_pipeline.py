#!/usr/bin/env python3
"""
Run the full still-to-GIF pipeline for one specialist across one or more states.
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path


DEFAULT_REPO_ROOT = Path(
    os.environ.get("TONGUE_REPO_ROOT", Path(__file__).resolve().parent.parent)
).resolve()
DEFAULT_STATES = ["idle-day", "working", "waving", "idle-night", "celebrate", "error"]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run the specialist state pipeline from a chosen still image.")
    parser.add_argument("--specialist", required=True)
    parser.add_argument("--input-image", type=Path, required=True, help="Chosen avatar still used as the identity reference.")
    parser.add_argument("--output-root", type=Path, required=True, help="Root directory for per-state outputs.")
    parser.add_argument("--repo-root", type=Path, default=DEFAULT_REPO_ROOT)
    parser.add_argument("--state", action="append", default=[], help="Limit to one or more states.")
    parser.add_argument("--duration", type=int, default=5)
    parser.add_argument("--frame-count", type=int, default=24)
    parser.add_argument("--start-frame", type=int, default=1)
    parser.add_argument("--end-frame", type=int, default=24)
    parser.add_argument("--sample-fps", type=float, default=8.0)
    parser.add_argument("--frame-duration-ms", type=int, default=90)
    parser.add_argument("--output-size", type=int, default=512)
    parser.add_argument("--rembg-model", default="isnet-general-use")
    parser.add_argument("--prompt-suffix", default="", help="Optional extra prompt text appended to each animation generation prompt.")
    parser.add_argument("--dry-run", action="store_true")
    return parser.parse_args()


def run_command(command: list[str], cwd: Path, dry_run: bool) -> None:
    print("$ " + " ".join(command))
    if dry_run:
        return
    completed = subprocess.run(
        command,
        cwd=cwd,
        text=True,
        capture_output=True,
        check=False,
        env=os.environ.copy(),
    )
    if completed.stdout:
        print(completed.stdout, end="")
    if completed.stderr:
        print(completed.stderr, file=sys.stderr, end="")
    if completed.returncode != 0:
        raise SystemExit(completed.returncode)


def main() -> int:
    args = parse_args()
    states = args.state or DEFAULT_STATES

    for state in states:
        state_dir = args.output_root / args.specialist / state
        command = [
            sys.executable,
            "scripts/generate_specialist_state_video.py",
            "--specialist",
            args.specialist,
            "--state",
            state,
            "--input-image",
            str(args.input_image),
            "--output-root",
            str(args.output_root),
            "--duration",
            str(args.duration),
        ]
        if args.prompt_suffix.strip():
            command.extend(["--prompt-suffix", args.prompt_suffix.strip()])

        run_command(
            command,
            cwd=args.repo_root,
            dry_run=args.dry_run,
        )
        run_command(
            [
                "swift",
                "scripts/extract_video_frames.swift",
                "--input-video",
                str(state_dir / "source-video.mp4"),
                "--output-dir",
                str(state_dir),
                "--frame-prefix",
                "frame",
                "--frame-count",
                str(args.frame_count),
                "--start-frame",
                str(args.start_frame),
                "--end-frame",
                str(args.end_frame),
                "--sample-fps",
                str(args.sample_fps),
                "--output-size",
                str(args.output_size),
                "--frame-duration-ms",
                str(args.frame_duration_ms),
                "--gif-filename",
                "trimmed.gif",
            ],
            cwd=args.repo_root,
            dry_run=args.dry_run,
        )
        run_command(
            [
                sys.executable,
                "scripts/remove_gif_frame_backgrounds.py",
                "--engine",
                "rembg",
                "--rembg-model",
                args.rembg_model,
                "--post-process-mask",
                "--input-dir",
                str(state_dir),
                "--output-dir",
                str(state_dir / "transparent-frames"),
                "--gif-filename",
                "trimmed-transparent.gif",
                "--frame-duration-ms",
                str(args.frame_duration_ms),
            ],
            cwd=args.repo_root,
            dry_run=args.dry_run,
        )

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
