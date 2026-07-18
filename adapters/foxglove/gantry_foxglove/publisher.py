"""Gantry ingest over plain-HTTP JSON (ConnectRPC accepts JSON POSTs).

The :class:`GantryPublisher` class is adapted from
``examples/so101/so101_bridge.py`` (the SO-101 kit's publisher): same
registration-retry and sequence-on-drop behaviour, proven against a live Bench.
It is copied rather than imported so this adapter stays standalone (adapters and
examples are separate territory). The additions here are lazy, incremental
channel registration (topics/joints are discovered from the stream, not known up
front) and a per-device :class:`PublisherPool`.
"""

from __future__ import annotations

import json
import urllib.request
from urllib.parse import urlencode


def post_video_chunk(
    endpoint: str,
    token: str | None,
    camera: str,
    start_ns: int,
    duration_ms: int,
    mime: str,
    data: bytes,
    timeout: float = 5.0,
) -> bool:
    """POST one self-contained video chunk to Gantry's video lane.

    Mirrors the Bench contract in ``apps/bench/internal/server/video.go``:
    ``POST /video/chunks?camera=&start_ns=&duration_ms=`` with the raw chunk
    bytes as the body and ``Content-Type`` set to the mime. Returns True on 2xx.
    """
    qs = urlencode({"camera": camera, "start_ns": int(start_ns), "duration_ms": int(duration_ms)})
    req = urllib.request.Request(
        f"{endpoint.rstrip('/')}/video/chunks?{qs}",
        data=data,
        headers={"content-type": mime},
        method="POST",
    )
    if token:
        req.add_header("authorization", f"Bearer {token}")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return 200 <= resp.status < 300
    except Exception:
        return False


class GantryPublisher:
    """Per-device ingest client. Batches frames, registers channels lazily.

    Adapted from ``examples/so101/so101_bridge.py``:GantryPublisher.
    """

    def __init__(self, endpoint: str, device_id: str, token: str | None = None):
        self.endpoint = endpoint.rstrip("/")
        self.device_id = device_id
        self.token = token
        self.sequence = 1
        self.frames: list[dict] = []
        # Channel registry, discovered from the stream. _seen keys are
        # (packet, channel); _specs is the RegisterChannels payload.
        self._specs: list[dict] = []
        self._seen: set[tuple[str, str]] = set()
        self._dirty = False
        self.registered = False
        self.sent = 0
        self.dropped = 0

    def _post(self, rpc: str, body: dict) -> bool:
        req = urllib.request.Request(
            f"{self.endpoint}/gantry.v1.{rpc}",
            data=json.dumps(body).encode(),
            headers={"content-type": "application/json"},
            method="POST",
        )
        if self.token:
            req.add_header("authorization", f"Bearer {self.token}")
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                return 200 <= resp.status < 300
        except Exception:
            return False

    def ensure_channel(self, packet: str, channel: str, unit: str | None) -> None:
        """Register (packet, channel) with its unit the first time it is seen."""
        key = (packet, channel)
        if key in self._seen:
            return
        self._seen.add(key)
        self._specs.append(
            {"name": channel, "kind": "VALUE_KIND_F64", "unit": unit or "", "packet": packet}
        )
        self._dirty = True

    def register(self) -> None:
        """Idempotent RegisterChannels; on success clears the dirty flag so a
        later new channel re-registers, and a Bench that boots after us still
        gets units."""
        if self._post("IngestService/RegisterChannels", {"deviceId": self.device_id, "channels": self._specs}):
            self.registered = True
            self._dirty = False

    def add(self, packet: str, channel: str, value: float, t_ns: int) -> None:
        self.frames.append(
            {
                "channel": channel,
                "packet": packet,
                "timestampNs": str(int(t_ns)),
                "value": {"f64": float(value)},
            }
        )

    def flush(self) -> None:
        """Register any new channels, then publish the batched frames. Sequence
        advances even on a dropped batch so the server sees honest gaps."""
        if self._dirty or not self.registered:
            self.register()
        if not self.frames:
            return
        batch = {"deviceId": self.device_id, "sequence": self.sequence, "frames": self.frames}
        if self._post("IngestService/PublishBatch", {"batch": batch}):
            self.sent += len(self.frames)
        else:
            self.dropped += len(self.frames)
        self.sequence += 1
        self.frames = []


class PublisherPool:
    """Lazily creates one :class:`GantryPublisher` per device id."""

    def __init__(self, endpoint: str, token: str | None = None, factory=GantryPublisher):
        self.endpoint = endpoint
        self.token = token
        self._factory = factory
        self.publishers: dict[str, GantryPublisher] = {}

    def get(self, device_id: str) -> GantryPublisher:
        pub = self.publishers.get(device_id)
        if pub is None:
            pub = self.publishers[device_id] = self._factory(self.endpoint, device_id, self.token)
        return pub

    def flush_all(self) -> None:
        for pub in self.publishers.values():
            pub.flush()

    def pending(self) -> int:
        return sum(len(p.frames) for p in self.publishers.values())
