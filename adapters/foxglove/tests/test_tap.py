"""Integration tests: FAKE foxglove server -> tap -> mocked publisher.

Each test drives a real WebSocket session on localhost with ``asyncio.run`` (no
pytest-asyncio dependency) and asserts the tap decodes, maps via the lerobot
profile, and batches to a recording publisher with the right device / packet /
channel / value / timestamp.
"""

import asyncio

from fake_server import FakeFoxgloveServer, wait_until
from gantry_foxglove.mapping import lerobot_profile
from gantry_foxglove.publisher import PublisherPool
from gantry_foxglove.tap import FoxgloveTap


class RecordingPublisher:
    """Structural stand-in for GantryPublisher: captures instead of POSTing."""

    def __init__(self, endpoint, device_id, token=None):
        self.device_id = device_id
        self.frames = []
        self.published = []  # (packet, channel, value, t_ns)
        self.channels = []  # (packet, channel, unit)

    def ensure_channel(self, packet, channel, unit):
        self.channels.append((packet, channel, unit))

    def add(self, packet, channel, value, t_ns):
        self.frames.append((packet, channel, value, t_ns))

    def flush(self):
        self.published.extend(self.frames)
        self.frames = []


def _published(pool, device):
    pub = pool.publishers.get(device)
    return pub.published if pub else []


def _find(pool, device, packet, channel):
    for p, c, v, t in _published(pool, device):
        if p == packet and c == channel:
            return (v, t)
    return None


def _run(coro):
    return asyncio.run(coro)


def test_scalars_map_to_publisher():
    async def scenario():
        server = FakeFoxgloveServer()
        await server.start()
        pool = PublisherPool("http://x", None, factory=RecordingPublisher)
        tap = FoxgloveTap(server.url, lerobot_profile(), pool, video=None, flush_interval=0.02)
        task = asyncio.create_task(tap.run())
        try:
            await server.wait_for_subscription("/observation/state")
            await server.wait_for_subscription("/action/state")
            await server.send_scalars("/observation/state",
                                      {"shoulder_pan.pos": 10.0, "gripper.pos": 5.0}, 111)
            await server.send_scalars("/action/state", {"shoulder_pan.pos": 12.0}, 222)
            ok = await wait_until(
                lambda: _find(pool, "so101-follower", "shoulder_pan", "cmd") is not None
                and _find(pool, "so101-leader", "shoulder_pan", "pos") is not None
            )
            assert ok
        finally:
            tap.stop()
            await task
            await server.stop()

        assert _find(pool, "so101-follower", "shoulder_pan", "pos") == (10.0, 111)
        assert _find(pool, "so101-follower", "gripper", "pos") == (5.0, 111)
        assert _find(pool, "so101-leader", "shoulder_pan", "pos") == (12.0, 222)
        assert _find(pool, "so101-follower", "shoulder_pan", "cmd") == (12.0, 222)
        # Derived tracking error on the follower: cmd - pos = 2.
        assert _find(pool, "so101-follower", "shoulder_pan", "track_err") == (2.0, 222)
        # Units registered.
        follower = pool.publishers["so101-follower"]
        assert ("shoulder_pan", "pos", "deg") in follower.channels
        assert ("shoulder_pan", "track_err", "deg") in follower.channels

    _run(scenario())


def test_unadvertise_then_readvertise():
    async def scenario():
        server = FakeFoxgloveServer()
        await server.start()
        pool = PublisherPool("http://x", None, factory=RecordingPublisher)
        tap = FoxgloveTap(server.url, lerobot_profile(), pool, video=None, flush_interval=0.02)
        task = asyncio.create_task(tap.run())
        try:
            await server.wait_for_subscription("/action/state")
            # Drop the action channel (id 2). The tap must forget its subscription.
            await server.unadvertise([2])
            assert await wait_until(lambda: 2 not in tap._chan_to_sub)
            # Re-advertise the same topic under a NEW channel id; tap resubscribes.
            await server.advertise({"id": 42, "topic": "/action/state", "encoding": "json",
                                    "schemaName": "lerobot.Scalars", "schema": "{}"})
            await server.wait_for_subscription("/action/state")
            await server.send_scalars("/action/state", {"elbow_flex.pos": 7.0}, 900)
            assert await wait_until(
                lambda: _find(pool, "so101-leader", "elbow_flex", "pos") is not None)
        finally:
            tap.stop()
            await task
            await server.stop()

        assert _find(pool, "so101-leader", "elbow_flex", "pos") == (7.0, 900)

    _run(scenario())


def test_reconnect_after_server_drop():
    async def scenario():
        server = FakeFoxgloveServer()
        await server.start()
        pool = PublisherPool("http://x", None, factory=RecordingPublisher)
        tap = FoxgloveTap(server.url, lerobot_profile(), pool, video=None, flush_interval=0.02)
        task = asyncio.create_task(tap.run())
        try:
            await server.wait_for_subscription("/observation/state")
            connects_before = tap.connects
            # Simulate a server restart: drop the active connection.
            await server.drop_clients()
            # Tap reconnects (backoff) and resubscribes to the still-listening server.
            assert await wait_until(lambda: tap.connects > connects_before, timeout=5.0)
            await server.wait_for_subscription("/observation/state", timeout=5.0)
            await server.send_scalars("/observation/state", {"gripper.pos": 3.0}, 1234)
            assert await wait_until(
                lambda: _find(pool, "so101-follower", "gripper", "pos") is not None, timeout=5.0)
        finally:
            tap.stop()
            await task
            await server.stop()

        assert _find(pool, "so101-follower", "gripper", "pos") == (3.0, 1234)

    _run(scenario())
