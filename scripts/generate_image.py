#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = [
#     "google-genai>=1.0.0",
#     "pillow>=10.0.0",
# ]
# ///
"""
Generate images using Gemini 3 Pro Image.
"""

import argparse
import os
import sys
from pathlib import Path

SUPPORTED_ASPECT_RATIOS = [
    "1:1",
    "2:3",
    "3:2",
    "3:4",
    "4:3",
    "4:5",
    "5:4",
    "9:16",
    "16:9",
    "21:9",
]


def get_api_key(provided_key: str | None) -> str | None:
    if provided_key:
        return provided_key
    return os.environ.get("GEMINI_API_KEY")


def auto_detect_resolution(max_input_dim: int) -> str:
    if max_input_dim >= 3000:
        return "4K"
    if max_input_dim >= 1500:
        return "2K"
    return "1K"


def choose_output_resolution(
    requested_resolution: str | None,
    max_input_dim: int,
    has_input_images: bool,
) -> tuple[str, bool]:
    if requested_resolution is not None:
        return requested_resolution, False
    if has_input_images and max_input_dim > 0:
        return auto_detect_resolution(max_input_dim), True
    return "1K", False


def main() -> None:
    parser = argparse.ArgumentParser(description="Generate images using Gemini 3 Pro Image")
    parser.add_argument("--prompt", "-p", required=True)
    parser.add_argument("--filename", "-f", required=True)
    parser.add_argument("--input-image", "-i", action="append", dest="input_images", metavar="IMAGE")
    parser.add_argument("--resolution", "-r", choices=["1K", "2K", "4K"], default=None)
    parser.add_argument("--aspect-ratio", "-a", choices=SUPPORTED_ASPECT_RATIOS, default=None)
    parser.add_argument("--api-key", "-k")
    args = parser.parse_args()

    api_key = get_api_key(args.api_key)
    if not api_key:
        print("Error: provide --api-key or set GEMINI_API_KEY", file=sys.stderr)
        sys.exit(1)

    from google import genai
    from google.genai import types
    from PIL import Image as PILImage

    client = genai.Client(api_key=api_key)
    output_path = Path(args.filename)
    output_path.parent.mkdir(parents=True, exist_ok=True)

    input_images = []
    max_input_dim = 0
    if args.input_images:
        if len(args.input_images) > 14:
            print(f"Error: too many input images ({len(args.input_images)}), max 14", file=sys.stderr)
            sys.exit(1)
        for img_path in args.input_images:
            try:
                with PILImage.open(img_path) as img:
                    copied = img.copy()
                    width, height = copied.size
                input_images.append(copied)
                max_input_dim = max(max_input_dim, width, height)
            except Exception as error:
                print(f"Error loading input image '{img_path}': {error}", file=sys.stderr)
                sys.exit(1)

    output_resolution, _ = choose_output_resolution(
        requested_resolution=args.resolution,
        max_input_dim=max_input_dim,
        has_input_images=bool(input_images),
    )

    if input_images:
        contents = [*input_images, args.prompt]
    else:
        contents = args.prompt

    image_cfg_kwargs = {"image_size": output_resolution}
    if args.aspect_ratio:
        image_cfg_kwargs["aspect_ratio"] = args.aspect_ratio

    try:
        response = client.models.generate_content(
            model="gemini-3-pro-image-preview",
            contents=contents,
            config=types.GenerateContentConfig(
                response_modalities=["TEXT", "IMAGE"],
                image_config=types.ImageConfig(**image_cfg_kwargs),
            ),
        )
    except Exception as error:
        print(f"Error generating image: {error}", file=sys.stderr)
        sys.exit(1)

    image_saved = False
    for part in response.parts:
        if part.inline_data is None:
            continue

        from io import BytesIO

        image_data = part.inline_data.data
        if isinstance(image_data, str):
            import base64

            image_data = base64.b64decode(image_data)

        image = PILImage.open(BytesIO(image_data))
        if image.mode == "RGBA":
            rgb_image = PILImage.new("RGB", image.size, (255, 255, 255))
            rgb_image.paste(image, mask=image.split()[3])
            rgb_image.save(str(output_path), "PNG")
        elif image.mode == "RGB":
            image.save(str(output_path), "PNG")
        else:
            image.convert("RGB").save(str(output_path), "PNG")
        image_saved = True
        break

    if not image_saved:
        print("Error: no image was generated in the response", file=sys.stderr)
        sys.exit(1)

    full_path = output_path.resolve()
    print(f"MEDIA:{full_path}")


if __name__ == "__main__":
    main()
