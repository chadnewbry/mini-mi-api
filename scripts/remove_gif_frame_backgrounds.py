#!/usr/bin/env python3
"""
Remove the background and drop shadow from extracted animation frames with xAI,
then rebuild a GIF from the cleaned PNG sequence.
"""

from __future__ import annotations

import argparse
import base64
import concurrent.futures
import json
import mimetypes
import os
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path


DEFAULT_USER_AGENT = "TongueApp/1.0 (+https://tongueassets.com)"
DEFAULT_BASE_URL = "https://api.x.ai/v1"
DEFAULT_MODEL = "grok-imagine-image"
DEFAULT_ENGINE = "poofbg"
DEFAULT_REMBG_BIN = os.environ.get("REMBG_BIN", "rembg")
DEFAULT_REMBG_MODEL = "isnet-general-use"
DEFAULT_PROMPT = (
    "Remove the entire background and any floor shadow or drop shadow from this character frame. "
    "Keep the exact same character, pose, expression, outfit, props, lighting on the character, "
    "and framing. Output only the isolated character on a fully transparent background. "
    "Do not add any new background color, vignette, floor, glow, or cast shadow."
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Remove backgrounds from existing animation frames.")
    parser.add_argument("--input-dir", type=Path)
    parser.add_argument("--output-dir", type=Path)
    parser.add_argument("--input-file", type=Path)
    parser.add_argument("--output-file", type=Path)
    parser.add_argument("--frame-prefix", default="frame")
    parser.add_argument("--gif-filename", default="transparent.gif")
    parser.add_argument("--engine", default=DEFAULT_ENGINE, choices=["rembg", "xai", "poofbg"])
    parser.add_argument("--rembg-bin", default=DEFAULT_REMBG_BIN)
    parser.add_argument("--rembg-model", default=DEFAULT_REMBG_MODEL)
    parser.add_argument("--post-process-mask", action="store_true")
    parser.add_argument("--alpha-matting", action="store_true")
    parser.add_argument("--alpha-matting-foreground-threshold", type=int, default=240)
    parser.add_argument("--alpha-matting-background-threshold", type=int, default=10)
    parser.add_argument("--alpha-matting-erode-size", type=int, default=10)
    parser.add_argument("--model", default=DEFAULT_MODEL)
    parser.add_argument("--prompt", default=DEFAULT_PROMPT)
    parser.add_argument("--base-url", default=os.environ.get("XAI_BASE_URL", DEFAULT_BASE_URL))
    parser.add_argument("--frame-duration-ms", type=int, default=90)
    parser.add_argument("--sample-fps", type=float, default=8.0)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    using_single_file = args.input_file is not None or args.output_file is not None
    if using_single_file:
        if args.input_file is None or args.output_file is None:
            parser.error("--input-file and --output-file must be provided together.")
    elif args.input_dir is None or args.output_dir is None:
        parser.error("--input-dir and --output-dir are required unless using --input-file/--output-file.")
    return args


def ensure_api_key() -> str:
    api_key = os.environ.get("XAI_API_KEY", "").strip()
    if not api_key:
        raise RuntimeError("XAI_API_KEY is not set.")
    return api_key


def data_uri_for_image(image_path: Path) -> str:
    mime_type, _ = mimetypes.guess_type(image_path.name)
    resolved_mime = mime_type or "image/png"
    encoded = base64.b64encode(image_path.read_bytes()).decode("utf-8")
    return f"data:{resolved_mime};base64,{encoded}"


def request_json(url: str, api_key: str, payload: dict) -> dict:
    request = urllib.request.Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
            "User-Agent": DEFAULT_USER_AGENT,
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=180) as response:
            return json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as error:
        detail = error.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"xAI image edit failed ({error.code}): {detail}") from error


def resolve_image_url(payload: dict) -> str | None:
    if isinstance(payload.get("url"), str) and payload["url"]:
        return payload["url"]

    data = payload.get("data")
    if isinstance(data, list):
        for entry in data:
            if isinstance(entry, dict) and isinstance(entry.get("url"), str) and entry["url"]:
                return entry["url"]

    return None


def decoded_image_bytes(payload: dict) -> bytes | None:
    data = payload.get("data")
    if isinstance(data, list):
        for entry in data:
            if isinstance(entry, dict) and isinstance(entry.get("b64_json"), str) and entry["b64_json"]:
                return base64.b64decode(entry["b64_json"])
    return None


def download_file(url: str, destination: Path) -> None:
    request = urllib.request.Request(url, headers={"User-Agent": DEFAULT_USER_AGENT}, method="GET")
    with urllib.request.urlopen(request, timeout=300) as response, destination.open("wb") as output_file:
        shutil.copyfileobj(response, output_file)


def write_response_image(payload: dict, destination: Path) -> None:
    raw_bytes = decoded_image_bytes(payload)
    if raw_bytes is not None:
        destination.write_bytes(raw_bytes)
        return

    image_url = resolve_image_url(payload)
    if not image_url:
        raise RuntimeError(f"xAI did not return an edited image payload: {json.dumps(payload, indent=2)}")
    download_file(image_url, destination)


def ensure_poofbg_api_key() -> str:
    api_key = os.environ.get("POOFBG_API_KEY", "").strip()
    if not api_key:
        raise RuntimeError("POOFBG_API_KEY is not set.")
    return api_key


def remove_background_with_poofbg(api_key: str, source: Path, destination: Path) -> None:
    boundary = f"----PoofBgBoundary{int(time.time() * 1000)}"
    image_data = source.read_bytes()
    mime_type, _ = mimetypes.guess_type(source.name)
    resolved_mime = mime_type or "image/png"

    body = (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="image_file"; filename="{source.name}"\r\n'
        f"Content-Type: {resolved_mime}\r\n\r\n"
    ).encode("utf-8") + image_data + f"\r\n--{boundary}--\r\n".encode("utf-8")

    request = urllib.request.Request(
        "https://api.poof.bg/v1/remove",
        data=body,
        headers={
            "x-api-key": api_key,
            "Content-Type": f"multipart/form-data; boundary={boundary}",
            "Accept": "image/png",
            "User-Agent": DEFAULT_USER_AGENT,
        },
        method="POST",
    )
    max_attempts = 5
    for attempt in range(1, max_attempts + 1):
        try:
            with urllib.request.urlopen(request, timeout=120) as response:
                destination.write_bytes(response.read())
            return
        except urllib.error.HTTPError as error:
            detail = error.read().decode("utf-8", errors="replace")
            if error.code == 429 and attempt < max_attempts:
                wait = 2 ** attempt
                print(f"  rate limited, retrying in {wait}s (attempt {attempt}/{max_attempts})", flush=True)
                time.sleep(wait)
                request = urllib.request.Request(
                    "https://api.poof.bg/v1/remove",
                    data=body,
                    headers={
                        "x-api-key": api_key,
                        "Content-Type": f"multipart/form-data; boundary={boundary}",
                        "Accept": "image/png",
                        "User-Agent": DEFAULT_USER_AGENT,
                    },
                    method="POST",
                )
                continue
            raise RuntimeError(f"poof.bg background removal failed ({error.code}): {detail}") from error


def remove_background_with_rembg(
    rembg_bin: str,
    model: str,
    source: Path,
    destination: Path,
    post_process_mask: bool,
    alpha_matting: bool,
    alpha_matting_foreground_threshold: int,
    alpha_matting_background_threshold: int,
    alpha_matting_erode_size: int,
) -> None:
    command = [rembg_bin, "i", "-m", model]
    if post_process_mask:
        command.append("-ppm")
    if alpha_matting:
        command.extend(
            [
                "-a",
                "-af",
                str(alpha_matting_foreground_threshold),
                "-ab",
                str(alpha_matting_background_threshold),
                "-ae",
                str(alpha_matting_erode_size),
            ]
        )
    command.extend([str(source), str(destination)])
    process = subprocess.run(
        command,
        check=False,
        capture_output=True,
        text=True,
    )
    if process.returncode != 0:
        raise RuntimeError(process.stderr.strip() or process.stdout.strip() or "rembg failed")


def frame_paths(input_dir: Path, frame_prefix: str) -> list[Path]:
    return sorted(input_dir.glob(f"{frame_prefix}_*.png"))


def resolve_pillow_python(rembg_bin: str) -> str:
    configured = os.environ.get("PILLOW_PYTHON", "").strip()
    if configured and Path(configured).exists():
        return configured

    virtual_env = os.environ.get("VIRTUAL_ENV", "").strip()
    if virtual_env:
        candidate = Path(virtual_env) / "bin" / "python"
        if candidate.exists():
            return str(candidate)

    rembg_python = Path(rembg_bin).expanduser().resolve().parent / "python"
    if rembg_python.exists():
        return str(rembg_python)

    return sys.executable


def clean_image(args: argparse.Namespace, source: Path, destination: Path, api_key: str) -> None:
    if args.engine == "poofbg":
        poofbg_key = ensure_poofbg_api_key()
        remove_background_with_poofbg(poofbg_key, source, destination)
        return

    if args.engine == "rembg":
        remove_background_with_rembg(
            rembg_bin=args.rembg_bin,
            model=args.rembg_model,
            source=source,
            destination=destination,
            post_process_mask=args.post_process_mask,
            alpha_matting=args.alpha_matting,
            alpha_matting_foreground_threshold=args.alpha_matting_foreground_threshold,
            alpha_matting_background_threshold=args.alpha_matting_background_threshold,
            alpha_matting_erode_size=args.alpha_matting_erode_size,
        )
        return

    payload = {
        "model": args.model,
        "prompt": args.prompt,
        "response_format": "b64_json",
        "image_format": "url",
        "image": {"url": data_uri_for_image(source), "type": "image_url"},
    }
    response = request_json(f"{args.base_url.rstrip('/')}/images/edits", api_key, payload)
    write_response_image(response, destination)
    time.sleep(1)


def rebuild_gif(
    output_dir: Path,
    gif_filename: str,
    frame_duration_ms: int,
    frame_prefix: str,
    rembg_bin: str,
) -> None:
    frame_count = len(frame_paths(output_dir, frame_prefix))
    if frame_count == 0:
        raise RuntimeError("No cleaned frames found to rebuild GIF.")
    pillow_python = resolve_pillow_python(rembg_bin)
    process = subprocess.run(
        [
            pillow_python,
            "-c",
            (
                "from PIL import Image\n"
                f"from pathlib import Path\n"
                f"frames=sorted(Path({output_dir.as_posix()!r}).glob({(frame_prefix + '_*.png')!r}))\n"
                "rgba=[Image.open(p).convert('RGBA') for p in frames]\n"
                "images=[]\n"
                "for image in rgba:\n"
                "    pal=image.convert('P', palette=Image.Palette.ADAPTIVE)\n"
                "    alpha=image.getchannel('A')\n"
                "    mask=Image.eval(alpha, lambda a: 255 if a <= 0 else 0)\n"
                "    pal.paste(255, mask)\n"
                "    pal.info['transparency']=255\n"
                "    images.append(pal)\n"
                f"images[0].save(Path({(output_dir / gif_filename).as_posix()!r}), save_all=True, append_images=images[1:], duration={frame_duration_ms}, loop=0, disposal=2, transparency=255)\n"
            ),
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    if process.returncode != 0:
        stderr = process.stderr.strip()
        if "No module named 'PIL'" in stderr or 'No module named "PIL"' in stderr:
            raise RuntimeError(
                "Failed to rebuild GIF because Pillow is unavailable for "
                f"{pillow_python}. Set PILLOW_PYTHON or run this script from a venv with Pillow installed."
            )
        raise RuntimeError(stderr or "Failed to rebuild GIF.")


def main() -> int:
    args = parse_args()
    api_key = ensure_api_key() if args.engine == "xai" else ""

    if args.input_file is not None:
        args.output_file.parent.mkdir(parents=True, exist_ok=True)
        print(f"Cleaning {args.input_file.name}")
        if args.dry_run:
            return 0
        clean_image(args, args.input_file, args.output_file, api_key)
        print(f"Image: {args.output_file}")
        return 0

    frames = frame_paths(args.input_dir, args.frame_prefix)
    if not frames:
        raise RuntimeError(f"No frames found in {args.input_dir}")

    args.output_dir.mkdir(parents=True, exist_ok=True)

    if not args.dry_run and args.engine == "poofbg":
        poofbg_concurrency = 8
        print(f"Processing {len(frames)} frames ({poofbg_concurrency} concurrent workers)")

        def process_frame(frame_path: Path) -> None:
            destination = args.output_dir / frame_path.name
            clean_image(args, frame_path, destination, api_key)
            print(f"  done {frame_path.name}", flush=True)

        with concurrent.futures.ThreadPoolExecutor(max_workers=poofbg_concurrency) as pool:
            futures = {pool.submit(process_frame, fp): fp for fp in frames}
            for future in concurrent.futures.as_completed(futures):
                future.result()
    else:
        for frame_path in frames:
            destination = args.output_dir / frame_path.name
            print(f"Cleaning {frame_path.name}")
            if args.dry_run:
                continue
            clean_image(args, frame_path, destination, api_key)

    if not args.dry_run:
        rebuild_gif(
            output_dir=args.output_dir,
            gif_filename=args.gif_filename,
            frame_duration_ms=args.frame_duration_ms,
            frame_prefix=args.frame_prefix,
            rembg_bin=args.rembg_bin,
        )
        print(f"GIF: {args.output_dir / args.gif_filename}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
