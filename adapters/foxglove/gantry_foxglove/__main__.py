"""CLI: ``python -m gantry_foxglove --endpoint ... --url ws://127.0.0.1:8765``.

Wires a mapping (built-in ``--profile`` or a ``--map map.json`` file), a
per-device publisher pool, and the optional video tee into a :class:`FoxgloveTap`
and runs it until Ctrl-C.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import logging
import signal
import sys

from .mapping import Mapping, load_profile
from .publisher import PublisherPool
from .tap import FoxgloveTap
from .video import VideoTee, av_available


def build_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(
        prog="gantry_foxglove",
        description="Generic Foxglove-WebSocket -> Gantry tap (works with lerobot and ROS 2 "
        "foxglove_bridge).",
    )
    ap.add_argument("--endpoint", default="http://localhost:4780",
                    help="Gantry Bench ingest endpoint (default: %(default)s)")
    ap.add_argument("--token", default=None, help="gtk_ access token for a remote bench")
    ap.add_argument("--url", default="ws://127.0.0.1:8765",
                    help="Foxglove WebSocket server URL (default: %(default)s)")
    mx = ap.add_mutually_exclusive_group()
    mx.add_argument("--profile", default="lerobot",
                    help="built-in mapping profile (default: lerobot)")
    mx.add_argument("--map", dest="map_file",
                    help="path to a JSON mapping file (overrides --profile)")
    ap.add_argument("--no-video", action="store_true",
                    help="disable the image->video tee even if PyAV is installed")
    ap.add_argument("--video-fps", type=int, default=30, help="MP4 output frame rate (default: 30)")
    ap.add_argument("--video-seconds", type=float, default=2.0,
                    help="approx chunk length in seconds (default: 2.0)")
    ap.add_argument("-v", "--verbose", action="store_true", help="debug logging")
    return ap


def load_mapping(args) -> Mapping:
    if args.map_file:
        with open(args.map_file, "r", encoding="utf-8") as fh:
            return Mapping.from_dict(json.load(fh))
    return load_profile(args.profile)


async def _amain(args) -> int:
    mapping = load_mapping(args)
    pool = PublisherPool(args.endpoint, args.token)

    video = None
    if not args.no_video and mapping.wants_images():
        if av_available():
            video = VideoTee(args.endpoint, args.token, seg_seconds=args.video_seconds, fps=args.video_fps)
        else:
            logging.getLogger("gantry_foxglove").warning(
                "video tee disabled: PyAV not installed (pip install av) — streaming scalars only"
            )

    tap = FoxgloveTap(args.url, mapping, pool, video)

    loop = asyncio.get_running_loop()
    try:  # POSIX: clean stop on SIGINT/SIGTERM. Windows falls back to KeyboardInterrupt.
        loop.add_signal_handler(signal.SIGINT, tap.stop)
        loop.add_signal_handler(signal.SIGTERM, tap.stop)
    except NotImplementedError:
        pass

    print(f"gantry-foxglove: {args.url}  ->  {args.endpoint}   profile={mapping.name}"
          f"   video={'on' if video else 'off'}")
    print("(ctrl-c to stop)")
    try:
        await tap.run()
    except KeyboardInterrupt:
        tap.stop()
    print(f"stopped. connects={tap.connects} messages={tap.messages} mapped_frames={tap.mapped_frames}"
          + (f" video_chunks={video.posted}" if video else ""))
    return 0


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    try:
        return asyncio.run(_amain(args))
    except KeyboardInterrupt:
        return 0


if __name__ == "__main__":
    sys.exit(main())
