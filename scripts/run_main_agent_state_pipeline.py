#!/usr/bin/env python3
"""
Run the still-to-GIF state pipeline for Tongue's main agent and update the local manifest.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
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
SPECIALIST_STATE_PIPELINE_SCRIPT = Path(
    os.environ.get(
        "TONGUE_SPECIALIST_STATE_PIPELINE_SCRIPT",
        DEFAULT_REPO_ROOT / "scripts" / "run_specialist_state_pipeline.py",
    )
).resolve()
DEFAULT_STATES = ["idle-day", "listening", "processing", "working", "waving", "idle-night", "celebrate", "talking", "error"]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run the still-to-GIF pipeline for Tongue's main agent.")
    parser.add_argument("--state", action="append", default=[], help="Limit to one or more specific states.")
    parser.add_argument("--prompt-suffix", default="", help="Optional extra prompt text appended to each animation generation prompt.")
    return parser.parse_args()


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def load_manifest(path: Path) -> dict[str, object]:
    if not path.exists():
        raise SystemExit(f"Manifest not found: {path}.")
    return json.loads(path.read_text())


def write_manifest(path: Path, manifest: dict[str, object]) -> None:
    path.write_text(json.dumps(manifest, indent=2) + "\n")


def load_manifest_if_present(path: Path) -> dict[str, object]:
    if not path.exists():
        return {}
    return json.loads(path.read_text())


def append_log(log_path: Path, message: str) -> None:
    with log_path.open("a") as handle:
        handle.write(message.rstrip() + "\n")


def selected_input_image(manifest: dict[str, object]) -> Path:
    for key in ("selectedCandidatePath", "selectedSourcePhotoPath"):
        value = manifest.get(key)
        if isinstance(value, str) and value.strip():
            path = Path(value)
            if path.exists():
                return path
    raise SystemExit("No selected candidate or selected source photo is available for state generation.")


def run_command(command: list[str], cwd: Path, log_path: Path) -> None:
    append_log(log_path, "$ " + " ".join(command))
    completed = subprocess.run(command, cwd=cwd, text=True, capture_output=True, check=False)
    if completed.stdout:
        print(completed.stdout, end="")
        append_log(log_path, completed.stdout)
    if completed.stderr:
        print(completed.stderr, file=sys.stderr, end="")
        append_log(log_path, completed.stderr)
    if completed.returncode != 0:
        raise SystemExit(completed.returncode)


def merge_manifest_update(path: Path, update: dict[str, object]) -> dict[str, object]:
    latest = load_manifest_if_present(path)
    state_asset_paths = dict(latest.get("stateAssetPaths", {}))
    incoming_state_asset_paths = update.get("stateAssetPaths")
    if isinstance(incoming_state_asset_paths, dict):
        state_asset_paths.update(incoming_state_asset_paths)
    state_source_image_paths = dict(latest.get("stateSourceImagePaths", {}))
    incoming_state_source_image_paths = update.get("stateSourceImagePaths")
    if isinstance(incoming_state_source_image_paths, dict):
        state_source_image_paths.update(incoming_state_source_image_paths)
    latest.update(update)
    latest["stateAssetPaths"] = state_asset_paths
    latest["stateSourceImagePaths"] = state_source_image_paths
    write_manifest(path, latest)
    return latest


def main() -> int:
    args = parse_args()
    workspace_root = DEFAULT_WORKSPACE_ROOT
    manifest_path = workspace_root / "manifest.json"
    log_path = workspace_root / "generation.log"
    state_output_root = workspace_root / "state-renders"
    states = args.state or DEFAULT_STATES

    manifest = load_manifest(manifest_path)
    input_image = selected_input_image(manifest)
    log_path.parent.mkdir(parents=True, exist_ok=True)

    manifest["currentCandidateIndex"] = None
    manifest["totalCandidates"] = len(states)
    manifest["currentStepLabel"] = "Preparing animated state generation"
    manifest["generationLogPath"] = str(log_path)
    manifest["updatedAt"] = now_iso()
    manifest["status"] = "generating-states"
    manifest.setdefault("stateAssetPaths", {})
    manifest.setdefault("stateSourceImagePaths", {})
    manifest = merge_manifest_update(manifest_path, manifest)
    append_log(log_path, "Starting main agent state generation.")
    append_log(log_path, f"Input image: {input_image}")

    for index, state in enumerate(states, start=1):
        append_log(log_path, f"[state-pipeline] starting state={state}")
        manifest["currentCandidateIndex"] = index
        manifest["currentStepLabel"] = f"Generating state {index} of {len(states)}: {state}"
        manifest["updatedAt"] = now_iso()
        manifest = merge_manifest_update(manifest_path, manifest)

        command = [
            sys.executable,
            str(SPECIALIST_STATE_PIPELINE_SCRIPT),
            "--specialist",
            "main-agent",
            "--input-image",
            str(input_image),
            "--output-root",
            str(state_output_root),
            "--state",
            state,
        ]
        if args.prompt_suffix.strip():
            command.extend(["--prompt-suffix", args.prompt_suffix.strip()])

        run_command(
            command,
            cwd=DEFAULT_REPO_ROOT,
            log_path=log_path,
        )

        generated_source = state_output_root / "main-agent" / state / "source.png"
        generated_gif = state_output_root / "main-agent" / state / "transparent-frames" / "trimmed-transparent.gif"
        generated_static = state_output_root / "main-agent" / state / "final-static.png"
        if generated_source.exists():
            manifest["stateSourceImagePaths"] = {state: str(generated_source)}
        if generated_gif.exists():
            manifest["stateAssetPaths"] = {state: str(generated_gif)}
            append_log(log_path, f"[state-pipeline] state={state} using animated asset {generated_gif}")
        elif generated_static.exists():
            manifest["stateAssetPaths"] = {state: str(generated_static)}
            append_log(log_path, f"[state-pipeline] state={state} using static fallback asset {generated_static}")
        else:
            append_log(log_path, f"[state-pipeline] state={state} finished without generated asset")
        manifest["updatedAt"] = now_iso()
        manifest = merge_manifest_update(manifest_path, manifest)
        append_log(log_path, f"Finished state: {state}")

    manifest["currentCandidateIndex"] = len(states)
    manifest["totalCandidates"] = len(states)
    manifest["currentStepLabel"] = "Finished animated state generation."
    manifest["updatedAt"] = now_iso()
    manifest["status"] = "states-generated"
    manifest = merge_manifest_update(manifest_path, manifest)
    append_log(log_path, "Finished main agent state generation.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
