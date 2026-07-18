"""Codec for the open ``foxglove.websocket.v1`` protocol (client side).

Spec: https://github.com/foxglove/ws-protocol (docs/spec.md). This module is a
pure, hardware-free codec — no I/O — so it is exercised directly from raw byte
fixtures in the tests. Only the subset a read-only *subscriber* needs is
implemented: parse the server's JSON control messages (serverInfo / advertise /
unadvertise / status), build the client's subscribe / unsubscribe JSON, and
decode the binary server frames (MessageData opcode 0x01, Time opcode 0x02).

Binary framing is little-endian, matching the spec:

    MessageData := 0x01 | subscription_id (u32 LE) | log_time (u64 LE, ns) | payload
    Time        := 0x02 | timestamp (u64 LE, ns)

Message payloads are decoded per the channel's ``encoding``: lerobot scalar
channels use ``json``; its image channels use ``protobuf`` (a
``foxglove.CompressedImage`` message, from which :func:`extract_compressed_image`
pulls the JPEG bytes without a protobuf dependency).
"""

from __future__ import annotations

import json
import struct
from dataclasses import dataclass

# WebSocket subprotocol offered on connect. A server that does not select it is
# not speaking this protocol.
SUBPROTOCOL = "foxglove.websocket.v1"

# Binary server->client opcodes (first byte of a binary frame).
OP_MESSAGE_DATA = 0x01
OP_TIME = 0x02

# JSON server->client op names we act on (others are logged/ignored).
SERVER_INFO = "serverInfo"
ADVERTISE = "advertise"
UNADVERTISE = "unadvertise"
STATUS = "status"


@dataclass(frozen=True)
class Channel:
    """A channel as announced in a server ``advertise`` message."""

    id: int
    topic: str
    encoding: str
    schema_name: str = ""
    schema: str = ""
    schema_encoding: str = ""

    @classmethod
    def from_json(cls, obj: dict) -> "Channel":
        return cls(
            id=int(obj["id"]),
            topic=str(obj["topic"]),
            encoding=str(obj.get("encoding", "")),
            schema_name=str(obj.get("schemaName", "")),
            schema=str(obj.get("schema", "")),
            schema_encoding=str(obj.get("schemaEncoding", "")),
        )


@dataclass(frozen=True)
class MessageData:
    """A decoded binary MessageData frame (opcode 0x01)."""

    subscription_id: int
    log_time: int  # nanoseconds, from the producer's clock
    payload: bytes


@dataclass(frozen=True)
class TimeMessage:
    """A decoded binary Time frame (opcode 0x02)."""

    timestamp: int  # nanoseconds


# --- client -> server JSON messages ---------------------------------------


def encode_subscribe(subscriptions) -> str:
    """Build a ``subscribe`` message.

    ``subscriptions`` is an iterable of ``(subscription_id, channel_id)`` pairs.
    """
    return json.dumps(
        {
            "op": "subscribe",
            "subscriptions": [
                {"id": int(sub_id), "channelId": int(chan_id)} for sub_id, chan_id in subscriptions
            ],
        }
    )


def encode_unsubscribe(subscription_ids) -> str:
    """Build an ``unsubscribe`` message for the given subscription ids."""
    return json.dumps({"op": "unsubscribe", "subscriptionIds": [int(s) for s in subscription_ids]})


# --- server -> client JSON messages ---------------------------------------


def decode_json(text: str) -> dict:
    """Parse a server JSON control message into a dict (must have an ``op``)."""
    obj = json.loads(text)
    if "op" not in obj:
        raise ValueError("foxglove JSON message missing 'op'")
    return obj


def parse_advertise(obj: dict):
    """Return the list of :class:`Channel` in an ``advertise`` message."""
    return [Channel.from_json(c) for c in obj.get("channels", [])]


def parse_unadvertise(obj: dict):
    """Return the list of channel ids in an ``unadvertise`` message."""
    return [int(c) for c in obj.get("channelIds", [])]


# --- server -> client binary messages -------------------------------------


def decode_binary(data: bytes):
    """Decode a binary server frame into a :class:`MessageData` or
    :class:`TimeMessage`. Returns ``None`` for an unknown/empty opcode.

    Raises ``ValueError`` on a truncated frame of a known opcode so a corrupt
    stream is surfaced rather than silently mis-decoded.
    """
    if not data:
        return None
    op = data[0]
    if op == OP_MESSAGE_DATA:
        if len(data) < 13:
            raise ValueError(f"MessageData frame too short: {len(data)} bytes")
        sub_id, log_time = struct.unpack_from("<IQ", data, 1)
        return MessageData(subscription_id=sub_id, log_time=log_time, payload=data[13:])
    if op == OP_TIME:
        if len(data) < 9:
            raise ValueError(f"Time frame too short: {len(data)} bytes")
        (ts,) = struct.unpack_from("<Q", data, 1)
        return TimeMessage(timestamp=ts)
    return None


def encode_message_data(subscription_id: int, log_time: int, payload: bytes) -> bytes:
    """Encode a MessageData frame. Used by the test fake-server (and symmetry)."""
    return b"\x01" + struct.pack("<IQ", int(subscription_id), int(log_time)) + bytes(payload)


# --- protobuf: foxglove.CompressedImage extraction ------------------------
#
# foxglove-sdk logs images on a protobuf channel. We only need two fields of the
# CompressedImage message, so rather than depend on protobuf we scan the wire
# format directly. Field numbers are from the foxglove schema
# (schemas/proto/foxglove/CompressedImage.proto):
#     timestamp = 1 (message), data = 2 (bytes), format = 3 (string),
#     frame_id  = 4 (string)


def _read_varint(buf: bytes, i: int):
    """Read a base-128 varint at offset ``i``; return (value, next_offset)."""
    shift = 0
    result = 0
    while True:
        if i >= len(buf):
            raise ValueError("truncated varint")
        b = buf[i]
        i += 1
        result |= (b & 0x7F) << shift
        if not (b & 0x80):
            return result, i
        shift += 7


def extract_compressed_image(payload: bytes):
    """Extract ``(data, format)`` from a ``foxglove.CompressedImage`` protobuf
    message. ``data`` is the compressed image bytes (e.g. JPEG), ``format`` the
    format string (e.g. ``"jpeg"``). Unknown fields are skipped. Missing fields
    default to ``b""`` / ``""``.
    """
    data = b""
    fmt = ""
    i = 0
    n = len(payload)
    while i < n:
        tag, i = _read_varint(payload, i)
        field = tag >> 3
        wire = tag & 0x07
        if wire == 0:  # varint
            _, i = _read_varint(payload, i)
        elif wire == 1:  # 64-bit
            i += 8
        elif wire == 2:  # length-delimited
            length, i = _read_varint(payload, i)
            chunk = payload[i : i + length]
            i += length
            if field == 2:
                data = bytes(chunk)
            elif field == 3:
                fmt = chunk.decode("utf-8", "replace")
        elif wire == 5:  # 32-bit
            i += 4
        else:
            raise ValueError(f"unsupported protobuf wire type {wire}")
    return data, fmt
