#!/usr/bin/env python3
"""
Generate a specialist state video with xAI and persist local metadata.

This script stores the source still, xAI request metadata, and downloaded video
under a deterministic per-specialist / per-state directory so the app can trim
and re-export GIFs repeatedly from the same source video.
"""

from __future__ import annotations

import argparse
import base64
import json
import mimetypes
import os
import shutil
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from pathlib import Path


DEFAULT_OUTPUT_ROOT = Path("tmp/specialist-state-renders")
DEFAULT_POLL_INTERVAL_SECONDS = 5
DEFAULT_TIMEOUT_SECONDS = 600
DEFAULT_MODEL = "grok-imagine-video"
DEFAULT_USER_AGENT = "TongueApp/1.0 (+https://tongueassets.com)"
STATE_PROMPT_TEMPLATES = {
    "idle-day": (
        "Create a seamless subtle daytime idle loop for this {subject_label}. "
        "The character should feel calm, alert, and gently alive. "
        "Keep the same exact character identity, outfit, props, framing, and background style. "
        "Motion should be minimal and readable: breathing, blinking, a gentle shift of weight between either foot, "
        "and a small bored stretch that feels casual rather than dramatic. "
        "Keep the movement non-gendered and natural, with the character testing weight on either foot before stretching."
    ),
    "listening": (
        "Create a seamless listening loop for this {subject_label}. "
        "The character should feel attentive, focused, and ready to hear the user. "
        "Keep the same exact character identity, outfit, props, framing, and background style. "
        "Use a subtle ear-forward attention pose, small head adjustments, and minimal motion that still reads as actively listening."
    ),
    "processing": (
        "Create a seamless processing loop for this {subject_label}. "
        "The character should feel like it is thinking, transcribing, or working through the request. "
        "Keep the same exact character identity, outfit, props, framing, and background style. "
        "Use compact repeated motion like a small nod, tiny hand movement, or quiet thinking pose that loops cleanly."
    ),
    "working": (
        "Create a seamless working loop for this {subject_label}. "
        "The character should look focused and actively doing their job. "
        "Keep the same exact character identity, outfit, props, framing, and background style. "
        "Use compact repeated motion like typing, scanning, writing, checking a tool, or sorting papers."
    ),
    "waving": (
        "Create a seamless friendly waving loop for this {subject_label}. "
        "The character should feel welcoming, cheerful, and clearly greeting the user. "
        "Keep the same exact character identity, outfit, props, framing, and background style. "
        "Use a compact repeated wave with one hand, slight body bounce, and a warm expression that loops cleanly."
    ),
    "idle-night": (
        "Create a seamless nighttime idle loop for this {subject_label}. "
        "The character should feel clearly at rest and peaceful, suitable for night mode, not unconscious or collapsed. "
        "Keep the same exact character identity, outfit, props, framing, and background style. "
        "Use gentle breathing, slight head bob, and soft sleepy stillness."
    ),
    "celebrate": (
        "Create a short celebratory loop for this {subject_label}. "
        "The character should look delighted, successful, and playful. "
        "Keep the same exact character identity, outfit, props, framing, and background style. "
        "Use a compact motion such as a tiny fist pump, cheerful bounce, or proud prop lift that loops cleanly."
    ),
    "talking": (
        "Create a seamless talking loop for this {subject_label}. "
        "The character should feel like it is actively speaking a reply in a friendly, natural way. "
        "Keep the same exact character identity, outfit, props, framing, and background style. "
        "Use a subtle speaking pose, small mouth movement, gentle head motion, and compact hand or body motion that loops cleanly."
    ),
    "error": (
        "Create a loop for this {subject_label} that shows confusion and being blocked. "
        "The character should feel puzzled, uncertain, and stuck, not panicked or broken. "
        "Keep the same exact character identity, outfit, props, framing, and background style. "
        "Use small confused gestures like a head tilt, hesitant glance, shrug, or uncertain hand movement."
    ),
}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Generate a specialist state video via xAI.")
    parser.add_argument("--specialist", required=True, help="Specialist id, used for deterministic output layout.")
    parser.add_argument("--state", required=True, choices=sorted(STATE_PROMPT_TEMPLATES), help="Specialist state to generate.")
    parser.add_argument("--input-image", required=True, type=Path, help="Still image used as the source identity reference.")
    parser.add_argument("--output-root", type=Path, default=DEFAULT_OUTPUT_ROOT)
    parser.add_argument("--model", default=DEFAULT_MODEL)
    parser.add_argument("--duration", type=int, default=5, help="Requested video duration in seconds.")
    parser.add_argument("--subject-label", default="Tongue specialist mascot", help="Phrase used inside the built-in state prompts.")
    parser.add_argument("--poll-interval-seconds", type=int, default=DEFAULT_POLL_INTERVAL_SECONDS)
    parser.add_argument("--timeout-seconds", type=int, default=DEFAULT_TIMEOUT_SECONDS)
    parser.add_argument("--base-url", default=os.environ.get("XAI_BASE_URL", "https://api.x.ai/v1"))
    parser.add_argument("--prompt-suffix", default="", help="Optional extra prompt text appended to the built-in state prompt.")
    parser.add_argument("--dry-run", action="store_true")
    return parser.parse_args()


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def ensure_api_key() -> str:
    api_key = os.environ.get("XAI_API_KEY", "").strip()
    if not api_key:
        print("XAI_API_KEY is not set.", file=sys.stderr)
        raise SystemExit(1)
    return api_key


def state_directory(output_root: Path, specialist_id: str, state: str) -> Path:
    return output_root / specialist_id / state


def manifest_path(output_root: Path, specialist_id: str, state: str) -> Path:
    return state_directory(output_root, specialist_id, state) / "manifest.json"


def build_prompt(state: str, prompt_suffix: str, subject_label: str) -> str:
    prompt = STATE_PROMPT_TEMPLATES[state].format(subject_label=subject_label.strip() or "Tongue specialist mascot")
    suffix = prompt_suffix.strip()
    return f"{prompt} {suffix}".strip() if suffix else prompt


def data_uri_for_image(image_path: Path) -> str:
    mime_type, _ = mimetypes.guess_type(image_path.name)
    resolved_mime = mime_type or "image/png"
    encoded = base64.b64encode(image_path.read_bytes()).decode("utf-8")
    return f"data:{resolved_mime};base64,{encoded}"


def make_request(url: str, api_key: str, payload: dict) -> dict:
    body = json.dumps(payload).encode("utf-8")
    request = urllib.request.Request(
        url,
        data=body,
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
            "User-Agent": DEFAULT_USER_AGENT,
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            return json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as error:
        detail = error.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"xAI request failed ({error.code}): {detail}") from error


def get_request(url: str, api_key: str) -> dict:
    request = urllib.request.Request(
        url,
        headers={
            "Authorization": f"Bearer {api_key}",
            "User-Agent": DEFAULT_USER_AGENT,
        },
        method="GET",
    )
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            return json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as error:
        detail = error.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"xAI polling failed ({error.code}): {detail}") from error


def resolve_video_url(payload: dict) -> str | None:
    if isinstance(payload.get("url"), str) and payload["url"]:
        return payload["url"]

    video = payload.get("video")
    if isinstance(video, dict) and isinstance(video.get("url"), str) and video["url"]:
        return video["url"]

    data = payload.get("data")
    if isinstance(data, list):
        for entry in data:
            if isinstance(entry, dict) and isinstance(entry.get("url"), str) and entry["url"]:
                return entry["url"]

    result = payload.get("result")
    if isinstance(result, dict) and isinstance(result.get("url"), str) and result["url"]:
        return result["url"]

    return None


def response_state(payload: dict) -> str:
    for key in ("status", "state"):
        value = payload.get(key)
        if isinstance(value, str) and value:
            return value.lower()
    return "unknown"


def poll_for_video(base_url: str, request_id: str, api_key: str, poll_interval_seconds: int, timeout_seconds: int) -> dict:
    deadline = time.monotonic() + timeout_seconds
    result_url = f"{base_url.rstrip('/')}/videos/{urllib.parse.quote(request_id)}"

    while time.monotonic() < deadline:
        payload = get_request(result_url, api_key)
        state = response_state(payload)
        if state in {"completed", "succeeded", "success", "done"} and resolve_video_url(payload):
            return payload
        if state in {"failed", "error", "cancelled", "canceled"}:
            raise RuntimeError(f"xAI video generation failed: {json.dumps(payload, indent=2)}")
        time.sleep(max(1, poll_interval_seconds))

    raise RuntimeError(f"Timed out waiting for video generation {request_id}.")


def download_file(url: str, destination: Path) -> None:
    request = urllib.request.Request(url, headers={"User-Agent": DEFAULT_USER_AGENT}, method="GET")
    with urllib.request.urlopen(request, timeout=300) as response, destination.open("wb") as output_file:
        shutil.copyfileobj(response, output_file)


def existing_manifest(output_root: Path, specialist_id: str, state: str) -> dict | None:
    path = manifest_path(output_root, specialist_id, state)
    if not path.exists():
        return None
    return json.loads(path.read_text())


def save_manifest(output_root: Path, specialist_id: str, state: str, manifest: dict) -> Path:
    path = manifest_path(output_root, specialist_id, state)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(manifest, indent=2))
    return path


def main() -> int:
    args = parse_args()
    state_dir = state_directory(args.output_root, args.specialist, args.state)
    state_dir.mkdir(parents=True, exist_ok=True)

    source_image_path = state_dir / "source.png"
    shutil.copyfile(args.input_image, source_image_path)

    manifest = existing_manifest(args.output_root, args.specialist, args.state) or {}
    manifest.update(
        {
            "specialistID": args.specialist,
            "state": args.state,
            "updatedAt": now_iso(),
            "sourceImagePath": str(source_image_path),
            "model": args.model,
            "durationSeconds": args.duration,
            "prompt": build_prompt(args.state, args.prompt_suffix, args.subject_label),
        }
    )

    if args.dry_run:
        output_manifest_path = save_manifest(args.output_root, args.specialist, args.state, manifest)
        print(f"Manifest: {output_manifest_path}")
        print(f"State directory: {state_dir}")
        return 0

    api_key = ensure_api_key()
    payload = {
        "model": args.model,
        "prompt": manifest["prompt"],
        "duration": args.duration,
        "image": {
            "url": data_uri_for_image(source_image_path),
        },
    }

    creation = make_request(f"{args.base_url.rstrip('/')}/videos/generations", api_key, payload)
    request_id = creation.get("request_id")
    if not isinstance(request_id, str) or not request_id:
        raise RuntimeError(f"xAI did not return a request_id: {json.dumps(creation, indent=2)}")

    completed_payload = poll_for_video(
        base_url=args.base_url,
        request_id=request_id,
        api_key=api_key,
        poll_interval_seconds=args.poll_interval_seconds,
        timeout_seconds=args.timeout_seconds,
    )
    video_url = resolve_video_url(completed_payload)
    if not video_url:
        raise RuntimeError(f"xAI did not return a downloadable video URL: {json.dumps(completed_payload, indent=2)}")

    video_path = state_dir / "source-video.mp4"
    download_file(video_url, video_path)

    manifest.update(
        {
            "requestID": request_id,
            "videoURL": video_url,
            "videoPath": str(video_path),
            "rawStatusPayload": completed_payload,
        }
    )
    output_manifest_path = save_manifest(args.output_root, args.specialist, args.state, manifest)

    print(f"Request ID: {request_id}")
    print(f"Video: {video_path}")
    print(f"Manifest: {output_manifest_path}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
