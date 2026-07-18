"""Video-tee tests. Skipped cleanly where PyAV (or numpy) is unavailable."""

import io

import pytest

from gantry_foxglove.video import VideoTee, av_available

pytestmark = pytest.mark.skipif(not av_available(), reason="PyAV (av) not installed")

np = pytest.importorskip("numpy")
import av  # noqa: E402  (guarded by the skipif above)


def make_jpeg(width=32, height=24, fill=128) -> bytes:
    """Encode a solid-colour frame to a standalone JPEG via PyAV."""
    arr = np.full((height, width, 3), fill, dtype=np.uint8)
    frame = av.VideoFrame.from_ndarray(arr, format="rgb24")
    buf = io.BytesIO()
    container = av.open(buf, mode="w", format="mjpeg")
    stream = container.add_stream("mjpeg", rate=1)
    stream.width = width
    stream.height = height
    stream.pix_fmt = "yuvj420p"
    for pkt in stream.encode(frame):
        container.mux(pkt)
    for pkt in stream.encode(None):
        container.mux(pkt)
    container.close()
    return buf.getvalue()


def test_video_tee_produces_playable_mp4_chunk():
    posted = []

    def poster(camera, start_ns, duration_ms, mime, data):
        posted.append((camera, start_ns, duration_ms, mime, data))
        return True

    tee = VideoTee("http://x", None, seg_seconds=2.0, fps=15, poster=poster)
    tee.start()
    jpeg = make_jpeg()
    sec = 1_000_000_000
    # Frames within the first 2s window, then one past it to force a segment
    # boundary, then close to flush the tail segment.
    for i, t in enumerate([0, sec // 2, sec, 2 * sec + sec // 10, 3 * sec]):
        tee.feed("front", jpeg, t)
    tee.close()

    assert posted, "expected at least one video chunk"
    camera, start_ns, duration_ms, mime, data = posted[0]
    assert camera == "front"
    assert mime == "video/mp4"
    assert start_ns == 0
    # A real MP4 has an 'ftyp' box: bytes 4..8 of the file.
    assert data[4:8] == b"ftyp", f"not an MP4 (bytes: {data[:16]!r})"
    assert len(data) > 0


def test_video_tee_camera_id_from_sanitize():
    posted = []
    tee = VideoTee("http://x", None, seg_seconds=0.001, fps=15,
                   poster=lambda *a: posted.append(a) or True)
    tee.start()
    jpeg = make_jpeg()
    tee.feed("wrist_top", jpeg, 0)
    tee.feed("wrist_top", jpeg, 5_000_000)  # > seg boundary -> finalize first
    tee.close()
    assert posted and posted[0][0] == "wrist_top"
