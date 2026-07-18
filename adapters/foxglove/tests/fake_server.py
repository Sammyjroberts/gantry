"""A minimal FAKE ``foxglove.websocket.v1`` server for the tests.

Speaks just the subset the tap consumes: on connect it sends ``serverInfo`` then
``advertise``; it records ``subscribe`` / ``unsubscribe``; and it can push binary
MessageData frames (scalars as JSON, images as protobuf CompressedImage),
advertise/unadvertise channels dynamically, and drop the active connection to
simulate a server restart. No real Foxglove or foxglove-sdk involved.
"""

from __future__ import annotations

import asyncio
import json

from websockets.asyncio.server import serve

from gantry_foxglove import protocol as fp

_SCALARS_SCHEMA = json.dumps(
    {
        "type": "object",
        "title": "lerobot.Scalars",
        "properties": {
            "scalars": {
                "type": "array",
                "items": {
                    "type": "object",
                    "properties": {"label": {"type": "string"}, "value": {"type": "number"}},
                },
            }
        },
    }
)


def default_channels() -> list[dict]:
    """The lerobot-shaped channel set: two scalar topics + one image topic."""
    return [
        {"id": 1, "topic": "/observation/state", "encoding": "json",
         "schemaName": "lerobot.Scalars", "schema": _SCALARS_SCHEMA, "schemaEncoding": "jsonschema"},
        {"id": 2, "topic": "/action/state", "encoding": "json",
         "schemaName": "lerobot.Scalars", "schema": _SCALARS_SCHEMA, "schemaEncoding": "jsonschema"},
        {"id": 3, "topic": "/observation/images/front", "encoding": "protobuf",
         "schemaName": "foxglove.CompressedImage", "schema": "", "schemaEncoding": "protobuf"},
    ]


# --- tiny protobuf encoder for foxglove.CompressedImage (test fixtures) ----


def _varint(n: int) -> bytes:
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        out.append(b | (0x80 if n else 0))
        if not n:
            return bytes(out)


def _ld(field: int, data: bytes) -> bytes:
    """length-delimited (wire type 2) field."""
    return _varint((field << 3) | 2) + _varint(len(data)) + data


def encode_compressed_image(data: bytes, fmt: str = "jpeg", frame_id: str = "front",
                            log_time: int = 0) -> bytes:
    """Encode a foxglove.CompressedImage: timestamp(1), data(2), format(3), frame_id(4).

    A timestamp embedded message (field 1) is included so the decoder's skip path
    for non-target fields is exercised.
    """
    sec = log_time // 1_000_000_000
    nsec = log_time % 1_000_000_000
    ts = _varint((1 << 3) | 0) + _varint(sec) + _varint((2 << 3) | 0) + _varint(nsec)
    return _ld(1, ts) + _ld(2, data) + _ld(3, fmt.encode()) + _ld(4, frame_id.encode())


class FakeFoxgloveServer:
    def __init__(self, channels: list[dict] | None = None):
        self._channels = channels if channels is not None else default_channels()
        self._by_topic = {c["topic"]: c["id"] for c in self._channels}
        self._server = None
        self._conn = None  # latest client connection
        self._subs: dict[int, int] = {}  # channel id -> subscription id
        self.host = "127.0.0.1"
        self.port = 0
        self.subscribe_count = 0

    @property
    def url(self) -> str:
        return f"ws://{self.host}:{self.port}"

    async def start(self, port: int = 0) -> None:
        self._server = await serve(self._handler, self.host, port, subprotocols=[fp.SUBPROTOCOL])
        self.port = self._server.sockets[0].getsockname()[1]

    async def stop(self) -> None:
        if self._server is not None:
            self._server.close()
            await self._server.wait_closed()
            self._server = None

    async def _handler(self, ws) -> None:
        self._conn = ws
        self._subs.clear()
        await ws.send(json.dumps({"op": "serverInfo", "name": "fake", "capabilities": []}))
        await ws.send(json.dumps({"op": "advertise", "channels": self._channels}))
        try:
            async for message in ws:
                if isinstance(message, (bytes, bytearray)):
                    continue
                obj = json.loads(message)
                op = obj.get("op")
                if op == "subscribe":
                    for sub in obj.get("subscriptions", []):
                        self._subs[int(sub["channelId"])] = int(sub["id"])
                        self.subscribe_count += 1
                elif op == "unsubscribe":
                    ids = set(obj.get("subscriptionIds", []))
                    self._subs = {c: s for c, s in self._subs.items() if s not in ids}
        except Exception:
            pass
        finally:
            if self._conn is ws:
                self._conn = None

    async def wait_for_subscription(self, topic: str, timeout: float = 3.0) -> None:
        if not await wait_until(lambda: self.is_subscribed(topic), timeout=timeout):
            raise asyncio.TimeoutError(f"no subscription for {topic} within {timeout}s")

    def is_subscribed(self, topic: str) -> bool:
        return self._by_topic.get(topic) in self._subs

    async def _send_on_topic(self, topic: str, payload: bytes, log_time: int) -> None:
        cid = self._by_topic[topic]
        sub_id = self._subs.get(cid)
        if sub_id is None or self._conn is None:
            raise RuntimeError(f"topic {topic} not subscribed")
        await self._conn.send(fp.encode_message_data(sub_id, log_time, payload))

    async def send_scalars(self, topic: str, values: dict, log_time: int) -> None:
        payload = json.dumps(
            {"scalars": [{"label": k, "value": v} for k, v in values.items()]}
        ).encode()
        await self._send_on_topic(topic, payload, log_time)

    async def send_image(self, topic: str, jpeg: bytes, log_time: int, fmt: str = "jpeg") -> None:
        payload = encode_compressed_image(jpeg, fmt=fmt, log_time=log_time)
        await self._send_on_topic(topic, payload, log_time)

    async def advertise(self, channel: dict) -> None:
        self._channels.append(channel)
        self._by_topic[channel["topic"]] = channel["id"]
        if self._conn is not None:
            await self._conn.send(json.dumps({"op": "advertise", "channels": [channel]}))

    async def unadvertise(self, channel_ids: list[int]) -> None:
        self._channels = [c for c in self._channels if c["id"] not in channel_ids]
        for cid in channel_ids:
            self._subs.pop(cid, None)
        if self._conn is not None:
            await self._conn.send(json.dumps({"op": "unadvertise", "channelIds": channel_ids}))

    async def drop_clients(self) -> None:
        """Close the active connection to simulate a server restart."""
        if self._conn is not None:
            await self._conn.close()
            self._conn = None


async def wait_until(pred, timeout: float = 3.0, interval: float = 0.01) -> bool:
    """Poll ``pred`` until true or timeout. Returns the final truthiness."""
    loop = asyncio.get_running_loop()
    deadline = loop.time() + timeout
    while loop.time() < deadline:
        if pred():
            return True
        await asyncio.sleep(interval)
    return bool(pred())
