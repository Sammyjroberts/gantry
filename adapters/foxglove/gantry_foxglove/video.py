"""Optional video tee: Foxglove image frames -> Gantry video lane.

If PyAV (``av``) is importable, JPEG camera frames are decoded and re-encoded
into rolling ~2s H.264 MP4 chunks (browser-playable, ``+faststart``) and POSTed
to ``POST /video/chunks`` with a camera id derived from the topic. If PyAV is
absent, :func:`av_available` is False and the tap logs once that the tee is
disabled (``pip install av``) and continues scalars-only.

Encoding runs on a dedicated worker thread fed by a queue, so the heavy
decode/encode never blocks the async WebSocket receive loop. Frame pacing and
chunk boundaries come from each frame's ``log_time`` (ns).
"""

from __future__ import annotations

import io
import logging
import queue
import threading
from fractions import Fraction

log = logging.getLogger("gantry_foxglove.video")

try:  # PyAV is optional.
    import av  # type: ignore

    _AV = True
except Exception:  # pragma: no cover - exercised only where av is absent
    av = None  # type: ignore
    _AV = False


def av_available() -> bool:
    return _AV


# Sentinel to stop the worker thread.
_STOP = object()


class _CameraEncoder:
    """Accumulates decoded frames into one in-memory MP4, sized by wall-span."""

    def __init__(self, fps: int):
        self.fps = fps
        self._buf = io.BytesIO()
        # No +faststart: it needs a seekable second pass the in-memory BytesIO
        # can't provide on all PyAV builds. Chunks are fetched whole, so a
        # trailing moov is fine (the whole file is available before playback).
        self._container = av.open(self._buf, mode="w", format="mp4")
        self._stream = None
        self._n = 0
        self.start_ns: int | None = None
        self.last_ns: int | None = None

    def _ensure_stream(self, width: int, height: int) -> None:
        if self._stream is None:
            st = self._container.add_stream("h264", rate=self.fps)
            st.width = width
            st.height = height
            st.pix_fmt = "yuv420p"
            self._stream = st

    def add(self, frame, log_time: int) -> None:
        self._ensure_stream(frame.width, frame.height)
        if self.start_ns is None:
            self.start_ns = log_time
        self.last_ns = log_time
        frame = frame.reformat(format="yuv420p", width=self._stream.width, height=self._stream.height)
        # Assign a monotonic CFR timestamp at the output rate. A freshly decoded
        # JPEG frame carries the decoder's own pts/time_base; feeding those (or a
        # bare pts without a matching time_base) makes libx264 reject frames as
        # non-monotonic and the mux trailer fails.
        frame.pts = self._n
        frame.time_base = Fraction(1, self.fps)
        self._n += 1
        for pkt in self._stream.encode(frame):
            self._container.mux(pkt)

    def finish(self) -> bytes:
        """Flush the encoder, close the container, return the complete MP4."""
        if self._stream is not None:
            for pkt in self._stream.encode():
                self._container.mux(pkt)
        self._container.close()
        return self._buf.getvalue()

    def duration_ms(self) -> int:
        if self.start_ns is None or self.last_ns is None:
            return 0
        span = (self.last_ns - self.start_ns) // 1_000_000
        # A single-frame chunk still lasts ~1/fps; never report 0.
        return max(span, 1000 // self.fps)


class VideoTee:
    """Threaded JPEG-frame -> MP4-chunk tee. No-op if PyAV is unavailable.

    ``poster`` is the function used to upload a finished chunk; it defaults to the
    real HTTP POST and is injectable for tests. Signature:
    ``poster(camera, start_ns, duration_ms, mime, data) -> bool``.
    """

    def __init__(self, endpoint: str, token: str | None = None, *, seg_seconds: float = 2.0,
                 fps: int = 30, poster=None):
        self.endpoint = endpoint
        self.token = token
        self.seg_ns = int(seg_seconds * 1_000_000_000)
        self.fps = fps
        self.enabled = _AV
        self.posted = 0
        self.dropped = 0
        if poster is not None:
            self._poster = poster
        else:
            from .publisher import post_video_chunk

            def _poster(camera, start_ns, duration_ms, mime, data):
                return post_video_chunk(endpoint, token, camera, start_ns, duration_ms, mime, data)

            self._poster = _poster
        self._q: "queue.Queue" = queue.Queue(maxsize=256)
        self._encoders: dict[str, _CameraEncoder] = {}
        self._thread: threading.Thread | None = None

    def start(self) -> None:
        if not self.enabled or self._thread is not None:
            return
        self._thread = threading.Thread(target=self._run, name="foxglove-video", daemon=True)
        self._thread.start()

    def feed(self, camera: str, jpeg: bytes, log_time: int) -> None:
        """Hand a JPEG frame to the encoder thread (dropped if the queue is full,
        so a stalled encoder can never stall the WebSocket loop)."""
        if not self.enabled:
            return
        try:
            self._q.put_nowait((camera, jpeg, log_time))
        except queue.Full:
            self.dropped += 1

    def close(self) -> None:
        """Stop the worker and flush any partial chunks."""
        if not self.enabled or self._thread is None:
            return
        self._q.put(_STOP)
        self._thread.join(timeout=10.0)
        self._thread = None

    # --- worker thread ---

    def _run(self) -> None:
        while True:
            item = self._q.get()
            if item is _STOP:
                self._flush_all()
                return
            camera, jpeg, log_time = item
            try:
                self._on_frame(camera, jpeg, log_time)
            except Exception as e:  # a bad frame must not kill the tee
                log.warning("video: dropping frame on camera %s: %s", camera, e)

    def _decode_jpeg(self, jpeg: bytes):
        """Decode a single JPEG blob to an av.VideoFrame."""
        with av.open(io.BytesIO(jpeg)) as c:
            for frame in c.decode(video=0):
                return frame
        raise ValueError("no frame decoded from JPEG")

    def _on_frame(self, camera: str, jpeg: bytes, log_time: int) -> None:
        enc = self._encoders.get(camera)
        if enc is not None and enc.start_ns is not None and (log_time - enc.start_ns) >= self.seg_ns:
            self._finalize(camera)
            enc = None
        if enc is None:
            enc = self._encoders[camera] = _CameraEncoder(self.fps)
        frame = self._decode_jpeg(jpeg)
        enc.add(frame, log_time)

    def _finalize(self, camera: str) -> None:
        enc = self._encoders.pop(camera, None)
        if enc is None or enc.start_ns is None:
            return
        try:
            data = enc.finish()
        except Exception as e:
            log.warning("video: encode finalize failed for %s: %s", camera, e)
            self.dropped += 1
            return
        if not data:
            return
        if self._poster(camera, enc.start_ns, enc.duration_ms(), "video/mp4", data):
            self.posted += 1
        else:
            self.dropped += 1

    def _flush_all(self) -> None:
        for camera in list(self._encoders):
            self._finalize(camera)
