#!/usr/bin/env python3
"""
Generate multiple still-image candidates for Tongue's main agent avatar flow.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path


DEFAULT_REPO_ROOT = Path(
    os.environ.get("TONGUE_REPO_ROOT", Path(__file__).resolve().parent.parent)
).resolve()
DEFAULT_WORKSPACE_ROOT = Path(
    os.environ.get(
        "TONGUE_MAIN_AGENT_WORKSPACE_ROOT",
        DEFAULT_REPO_ROOT / "tmp" / "main-agent-creation" / "current",
    )
).resolve()
GENERATOR_SCRIPT = Path(
    os.environ.get(
        "TONGUE_MAIN_AGENT_IMAGE_GENERATOR_SCRIPT",
        DEFAULT_REPO_ROOT / "scripts" / "generate_image.py",
    )
).resolve()
RETRY_MARKERS = ["429", "rate limit", "resource_exhausted", "unavailable", "503"]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Generate still-image candidates for Tongue's main agent.")
    parser.add_argument("--prompt-suffix", default="", help="Optional extra prompt text appended to each candidate prompt.")
    parser.add_argument("--candidate-index", type=int, help="Generate only the specified 1-based candidate index.")
    parser.add_argument("--candidate-count", type=int, default=4, help="Total number of candidates expected.")
    return parser.parse_args()


def candidate_prompt(base_prompt: str, candidate_index: int, candidate_count: int, prompt_suffix: str) -> str:
    prompt = (
        f"{base_prompt} Produce candidate {candidate_index} of {candidate_count}. "
        "Vary pose and expression slightly while keeping the identity stable and the background simple. "
        "Keep the facial expression within a soft friendly neutral range with only minor variation around the slight closed-mouth smile. "
        "Do not crop the character. The result must remain a true full-body head-to-toe shot with visible feet. "
        "Keep the silhouette lean and compact rather than chubby or bulky. "
        "Keep the pose neutral and animation-ready, with arms down at the sides in a gentle A-pose."
    )
    if prompt_suffix.strip():
        prompt = f"{prompt} {prompt_suffix.strip()}"
    return prompt


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def should_retry(output: str) -> bool:
    lowered = output.lower()
    return any(marker in lowered for marker in RETRY_MARKERS)


def run_with_backoff(
    command: list[str],
    max_attempts: int = 6,
    initial_backoff_seconds: int = 45,
    backoff_multiplier: float = 2.0,
    max_backoff_seconds: int = 1800,
) -> subprocess.CompletedProcess[str]:
    attempt = 0
    delay = max(1, initial_backoff_seconds)
    while True:
        attempt += 1
        completed = subprocess.run(command, text=True, capture_output=True, check=False)
        combined = f"{completed.stdout}\n{completed.stderr}"
        if completed.returncode == 0:
            return completed
        if attempt >= max_attempts or not should_retry(combined):
            return completed
        print(f"Retrying after {delay}s due to transient image-generation failure.", file=sys.stderr)
        time.sleep(delay)
        delay = min(max_backoff_seconds, max(1, int(delay * backoff_multiplier)))


def load_manifest(path: Path) -> dict[str, object]:
    if not path.exists():
        raise SystemExit(f"Manifest not found: {path}. Run bootstrap_main_agent_creation.py first.")
    return json.loads(path.read_text())


def write_manifest(path: Path, manifest: dict[str, object]) -> None:
    path.write_text(json.dumps(manifest, indent=2) + "\n")


def append_log(log_path: Path, message: str) -> None:
    with log_path.open("a") as handle:
        handle.write(message.rstrip() + "\n")


def main() -> int:
    args = parse_args()
    workspace_root = DEFAULT_WORKSPACE_ROOT
    manifest_path = workspace_root / "manifest.json"
    prompt_path = workspace_root / "base-portrait-prompt.txt"
    candidate_dir = workspace_root / "candidate-renders"
    log_path = workspace_root / "generation.log"

    manifest = load_manifest(manifest_path)
    source_photo_paths = [
        Path(path)
        for path in manifest.get("sourcePhotoPaths", [])
        if isinstance(path, str) and Path(path).exists()
    ]

    if not source_photo_paths:
        raise SystemExit("No source photo paths were found in the manifest. Import photos in Main Agent Debug first.")
    if not prompt_path.exists():
        raise SystemExit(f"Prompt file not found: {prompt_path}. Run bootstrap_main_agent_creation.py first.")

    prompt = prompt_path.read_text().strip()
    candidate_dir.mkdir(parents=True, exist_ok=True)
    log_path.parent.mkdir(parents=True, exist_ok=True)
    count = max(1, args.candidate_count)
    generated_paths: list[str] = [
        path for path in manifest.get("candidateImagePaths", [])
        if isinstance(path, str) and Path(path).exists()
    ]
    manifest["candidateImagePaths"] = generated_paths
    manifest["selectedCandidatePath"] = generated_paths[0] if generated_paths else None
    manifest["currentCandidateIndex"] = args.candidate_index or 0
    manifest["totalCandidates"] = count
    manifest["currentStepLabel"] = "Preparing candidate generation"
    manifest["generationLogPath"] = str(log_path)
    manifest["updatedAt"] = now_iso()
    manifest["status"] = "generating-candidates"
    write_manifest(manifest_path, manifest)
    if args.candidate_index is None:
        log_path.write_text("")
        append_log(log_path, "Starting main agent candidate generation.")
    else:
        append_log(log_path, f"Starting candidate {args.candidate_index} of {count}.")
    append_log(log_path, f"Using {len(source_photo_paths)} source photos.")

    candidate_indexes = [args.candidate_index] if args.candidate_index else list(range(1, count + 1))

    for index in candidate_indexes:
        output_path = candidate_dir / f"main-agent-candidate-{index:02d}.png"
        manifest["currentCandidateIndex"] = index
        manifest["currentStepLabel"] = f"Generating candidate {index} of {count}"
        manifest["updatedAt"] = now_iso()
        write_manifest(manifest_path, manifest)
        append_log(log_path, f"Generating candidate {index} of {count}: {output_path.name}")

        command = [
            "uv",
            "run",
            str(GENERATOR_SCRIPT),
            "--prompt",
            candidate_prompt(prompt, index, count, args.prompt_suffix),
            "--filename",
            str(output_path),
            "--resolution",
            "1K",
            "--aspect-ratio",
            "1:1",
        ]
        for source_photo_path in source_photo_paths:
            command.extend(["--input-image", str(source_photo_path)])

        print("$ " + " ".join(command))
        append_log(log_path, "$ " + " ".join(command))
        completed = run_with_backoff(command)
        if completed.stdout:
            print(completed.stdout, end="")
            append_log(log_path, completed.stdout)
        if completed.stderr:
            print(completed.stderr, file=sys.stderr, end="")
            append_log(log_path, completed.stderr)
        if completed.returncode != 0:
            manifest["currentStepLabel"] = f"Candidate generation failed on candidate {index}"
            manifest["updatedAt"] = now_iso()
            manifest["status"] = "generation-failed"
            write_manifest(manifest_path, manifest)
            return completed.returncode

        output_path_string = str(output_path)
        generated_paths = [
            existing_path for existing_path in generated_paths
            if Path(existing_path).name != output_path.name
        ]
        generated_paths.append(output_path_string)
        generated_paths.sort()
        manifest["candidateImagePaths"] = generated_paths
        manifest["selectedCandidatePath"] = generated_paths[0] if generated_paths else None
        manifest["updatedAt"] = now_iso()
        manifest["status"] = "generating-candidates" if len(generated_paths) < count else "candidates-generated"
        if len(generated_paths) < count:
            manifest["currentStepLabel"] = f"Generated {len(generated_paths)} of {count} choices."
        else:
            manifest["currentStepLabel"] = f"Generated {len(generated_paths)} candidates."
        write_manifest(manifest_path, manifest)
        append_log(log_path, f"Finished candidate {index} of {count}.")

    manifest["candidateImagePaths"] = generated_paths
    manifest["selectedCandidatePath"] = generated_paths[0] if generated_paths else None
    manifest["currentCandidateIndex"] = candidate_indexes[-1]
    manifest["totalCandidates"] = count
    manifest["currentStepLabel"] = (
        f"Generated {len(generated_paths)} candidates."
        if len(generated_paths) >= count
        else f"Generated {len(generated_paths)} of {count} choices."
    )
    manifest["updatedAt"] = now_iso()
    manifest["status"] = "candidates-generated" if len(generated_paths) >= count else "generating-candidates"
    write_manifest(manifest_path, manifest)
    append_log(log_path, f"Generated {len(generated_paths)} candidates.")
    print(f"Generated {len(generated_paths)} candidates.")
    print(f"Manifest: {manifest_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
