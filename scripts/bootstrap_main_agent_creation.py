#!/usr/bin/env python3
"""
Bootstrap a deterministic local workspace for Tongue's main agent avatar flow.
"""

from __future__ import annotations

import json
import os
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


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def ensure_directory(path: Path) -> None:
    path.mkdir(parents=True, exist_ok=True)


def build_base_portrait_prompt() -> str:
    return (
        "Create a square, app-ready full-body portrait of a human person for Tongue's main agent using the provided source photos as identity references. "
        "Preserve the person's recognizable facial structure, hair, skin tone, eyewear if present, and overall vibe. "
        "Render a stylized human character that still feels unmistakably like the same person. "
        "Visual direction: chunky voxel art mixed with soft low-poly 3D character design, simple blocky forms, stepped voxel edges, polished icon-ready readability. "
        "Use stylized Tongue character proportions: oversized head, medium torso, short little legs, tiny simple feet, lean compact silhouette, toy-like full-body shape. "
        "Favor a chibi-like full-body character over realistic human proportions, but do not make the character look overweight or heavyset unless the source photos clearly call for that. "
        "Use a neutral default standing pose suitable for later animation. "
        "Keep both arms relaxed down by the sides in a gentle animation-ready A-pose. "
        "Show one centered character only in a true full-body head-to-toe shot with the entire person fully visible and nothing cropped. "
        "Slight isometric angle, soft studio lighting, plain removable background, no text, no extra people, no animal traits, no mascot costume, no busy scene."
    )


def build_voxel_refine_prompt() -> str:
    return (
        "Refine this portrait into Tongue's visual language. Keep the same human identity, but push it toward crisp voxel / low-poly readability. "
        "Favor clear planes, stable anatomy, simple edges, and small-size legibility. "
        "Preserve the stylized Tongue body plan: oversized head, medium torso, short little legs, tiny feet, lean compact full-body silhouette, true head-to-toe framing. "
        "Do not make the character look chubby, overweight, or bulky unless that is clearly required by the source photos. "
        "Preserve a neutral standing pose with both arms hanging down at the sides in a gentle animation-ready A-pose, suitable as a base pose for animation. "
        "Keep the background plain and removable. "
        "Do not add animals, props, text, or scene clutter."
    )


def default_manifest(existing: dict[str, object] | None = None) -> dict[str, object]:
    existing = existing or {}
    return {
        "createdAt": existing.get("createdAt", now_iso()),
        "updatedAt": now_iso(),
        "sourcePhotoPaths": existing.get("sourcePhotoPaths", []),
        "candidateImagePaths": existing.get("candidateImagePaths", []),
        "currentCandidateIndex": existing.get("currentCandidateIndex"),
        "totalCandidates": existing.get("totalCandidates"),
        "currentStepLabel": existing.get("currentStepLabel"),
        "generationLogPath": str(DEFAULT_WORKSPACE_ROOT / "generation.log"),
        "selectedSourcePhotoPath": existing.get("selectedSourcePhotoPath"),
        "selectedCandidatePath": existing.get("selectedCandidatePath"),
        "publishedPreviewPath": existing.get("publishedPreviewPath"),
        "stateAssetPaths": existing.get("stateAssetPaths", {}),
        "stateSourceImagePaths": existing.get("stateSourceImagePaths", {}),
        "status": "workspace-bootstrapped",
        "notes": existing.get("notes"),
    }


def main() -> int:
    workspace_root = DEFAULT_WORKSPACE_ROOT
    source_photos = workspace_root / "source-photos"
    candidate_renders = workspace_root / "candidate-renders"
    selected = workspace_root / "selected"
    state_renders = workspace_root / "state-renders"

    for path in [source_photos, candidate_renders, selected, state_renders]:
        ensure_directory(path)

    manifest_path = workspace_root / "manifest.json"
    existing_manifest: dict[str, object] | None = None
    if manifest_path.exists():
        try:
            existing_manifest = json.loads(manifest_path.read_text())
        except json.JSONDecodeError:
            existing_manifest = None

    manifest = default_manifest(existing_manifest)
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")
    (workspace_root / "generation.log").write_text("")

    (workspace_root / "base-portrait-prompt.txt").write_text(build_base_portrait_prompt() + "\n")
    (workspace_root / "voxel-refine-prompt.txt").write_text(build_voxel_refine_prompt() + "\n")

    print(f"Workspace: {workspace_root}")
    print(f"Manifest: {manifest_path}")
    print(f"Source photos: {source_photos}")
    print(f"Candidate renders: {candidate_renders}")
    print("Wrote prompt files: base-portrait-prompt.txt, voxel-refine-prompt.txt")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
