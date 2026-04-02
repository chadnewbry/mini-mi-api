#!/usr/bin/env python3
"""
Run the full still-to-GIF pipeline for one specialist across one or more states.
"""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import sys
import time
import traceback
from pathlib import Path


DEFAULT_REPO_ROOT = Path(
    os.environ.get("TONGUE_REPO_ROOT", Path(__file__).resolve().parent.parent)
).resolve()
DEFAULT_STATES = ["idle-day", "working", "waving", "idle-night", "celebrate", "error"]
DEFAULT_PIPELINE_MODE = os.environ.get("MINIME_STATE_PIPELINE_MODE", "safe").strip().lower() or "safe"


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
    parser.add_argument("--pipeline-mode", choices=["full", "safe"], default=DEFAULT_PIPELINE_MODE)
    parser.add_argument("--prompt-suffix", default="", help="Optional extra prompt text appended to each animation generation prompt.")
    parser.add_argument("--dry-run", action="store_true")
    return parser.parse_args()


def run_command(command: list[str], cwd: Path, dry_run: bool) -> subprocess.CompletedProcess[str] | None:
    print("$ " + " ".join(command), flush=True)
    if dry_run:
        return None
    started_at = time.monotonic()
    completed = subprocess.run(
        command,
        cwd=cwd,
        text=True,
        capture_output=True,
        check=False,
        env=os.environ.copy(),
    )
    if completed.stdout:
        print(completed.stdout, end="", flush=True)
    if completed.stderr:
        print(completed.stderr, file=sys.stderr, end="", flush=True)
    duration_seconds = time.monotonic() - started_at
    log_stage(
        "command finished "
        f"exit={completed.returncode} duration={duration_seconds:.2f}s "
        f"command={' '.join(command)}"
    )
    if completed.returncode != 0:
        raise RuntimeError(
            f"command failed with exit code {completed.returncode}: {' '.join(command)}"
        )
    return completed


def log_stage(message: str) -> None:
    print(f"[state-pipeline] {message}", flush=True)


def create_static_fallback(state_dir: Path) -> Path:
    fallback_path = state_dir / "final-static.png"
    source_path = state_dir / "source.png"
    if source_path.exists():
        shutil.copy2(source_path, fallback_path)
        return fallback_path

    first_frame = next(iter(sorted(state_dir.glob("frame_*.png"))), None)
    if first_frame is not None:
        shutil.copy2(first_frame, fallback_path)
        return fallback_path

    raise RuntimeError(f"no source.png or extracted frames available for fallback in {state_dir}")


def main() -> int:
    args = parse_args()
    states = args.state or DEFAULT_STATES

    for state in states:
        state_dir = args.output_root / args.specialist / state
        log_stage(f"starting state={state} mode={args.pipeline_mode}")
        log_stage(f"state_dir={state_dir}")
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

        log_stage(f"generating source video for state={state}")
        run_command(
            command,
            cwd=args.repo_root,
            dry_run=args.dry_run,
        )

        if not args.dry_run and not (state_dir / "source-video.mp4").exists():
            raise RuntimeError(f"missing source video for state {state}: {state_dir / 'source-video.mp4'}")

        for existing_frame in sorted(state_dir.glob("frame_*.png")):
            existing_frame.unlink(missing_ok=True)

        extraction_command = [
            "ffmpeg",
            "-y",
            "-i",
            str(state_dir / "source-video.mp4"),
            "-vf",
            f"fps={args.sample_fps},scale={args.output_size}:{args.output_size}:force_original_aspect_ratio=decrease,pad={args.output_size}:{args.output_size}:(ow-iw)/2:(oh-ih)/2:color=0x00000000",
            "-start_number",
            "1",
            str(state_dir / "frame_%02d.png"),
        ]
        log_stage(f"extracting frames for state={state}")
        run_command(
            extraction_command,
            cwd=args.repo_root,
            dry_run=args.dry_run,
        )
        extracted_frames = sorted(state_dir.glob("frame_*.png"))
        log_stage(f"extracted {len(extracted_frames)} frames for state={state}")

        background_command = [
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
        ]
        log_stage(f"removing frame backgrounds for state={state}")
        try:
            run_command(
                background_command,
                cwd=args.repo_root,
                dry_run=args.dry_run,
            )
            log_stage(f"background removal succeeded for state={state}")
        except Exception as error:
            log_stage(f"background removal failed for state={state}: {error}")
            traceback.print_exc()
            if args.pipeline_mode != "safe" or args.dry_run:
                raise

            fallback_path = create_static_fallback(state_dir)
            log_stage(
                f"using static fallback for state={state}: {fallback_path}"
            )

        final_gif = state_dir / "transparent-frames" / "trimmed-transparent.gif"
        final_static = state_dir / "final-static.png"
        log_stage(
            f"artifact summary state={state} "
            f"source_video_exists={(state_dir / 'source-video.mp4').exists()} "
            f"source_png_exists={(state_dir / 'source.png').exists()} "
            f"gif_exists={final_gif.exists()} static_exists={final_static.exists()}"
        )
        if final_gif.exists():
            log_stage(f"final animated asset ready for state={state}: {final_gif}")
        elif final_static.exists():
            log_stage(f"final static asset ready for state={state}: {final_static}")
        else:
            raise RuntimeError(f"state={state} finished without a final asset")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
