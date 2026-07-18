"""Protocol codec tests from raw byte fixtures (no I/O)."""

import json
import struct

import pytest

from fake_server import encode_compressed_image
from gantry_foxglove import protocol as fp


def test_subprotocol_constant():
    assert fp.SUBPROTOCOL == "foxglove.websocket.v1"


def test_decode_message_data_raw_bytes():
    # 0x01 | sub_id=7 (u32 LE) | log_time=0x0102030405060708 (u64 LE) | payload
    payload = b'{"scalars":[]}'
    raw = b"\x01" + struct.pack("<I", 7) + struct.pack("<Q", 0x0102030405060708) + payload
    msg = fp.decode_binary(raw)
    assert isinstance(msg, fp.MessageData)
    assert msg.subscription_id == 7
    assert msg.log_time == 0x0102030405060708
    assert msg.payload == payload


def test_message_data_roundtrip():
    raw = fp.encode_message_data(42, 123456789, b"hello")
    msg = fp.decode_binary(raw)
    assert (msg.subscription_id, msg.log_time, msg.payload) == (42, 123456789, b"hello")


def test_decode_time_frame():
    raw = b"\x02" + struct.pack("<Q", 999)
    msg = fp.decode_binary(raw)
    assert isinstance(msg, fp.TimeMessage)
    assert msg.timestamp == 999


def test_decode_empty_and_unknown_opcode():
    assert fp.decode_binary(b"") is None
    assert fp.decode_binary(b"\x7f") is None  # unknown opcode -> None


def test_decode_truncated_message_data_raises():
    with pytest.raises(ValueError):
        fp.decode_binary(b"\x01\x00\x00")  # header cut short


def test_encode_subscribe_shape():
    out = json.loads(fp.encode_subscribe([(1, 10), (2, 20)]))
    assert out == {
        "op": "subscribe",
        "subscriptions": [{"id": 1, "channelId": 10}, {"id": 2, "channelId": 20}],
    }


def test_encode_unsubscribe_shape():
    assert json.loads(fp.encode_unsubscribe([3, 4])) == {
        "op": "unsubscribe",
        "subscriptionIds": [3, 4],
    }


def test_parse_advertise_and_unadvertise():
    adv = {
        "op": "advertise",
        "channels": [
            {"id": 1, "topic": "/observation/state", "encoding": "json",
             "schemaName": "lerobot.Scalars", "schema": "{}"},
        ],
    }
    chans = fp.parse_advertise(adv)
    assert len(chans) == 1
    c = chans[0]
    assert (c.id, c.topic, c.encoding, c.schema_name) == (1, "/observation/state", "json", "lerobot.Scalars")
    assert fp.parse_unadvertise({"op": "unadvertise", "channelIds": [1, 2]}) == [1, 2]


def test_decode_json_requires_op():
    with pytest.raises(ValueError):
        fp.decode_json("{}")


def test_extract_compressed_image_pulls_jpeg_and_format():
    jpeg = b"\xff\xd8\xff\xe0FAKEJPEGDATA\xff\xd9"
    payload = encode_compressed_image(jpeg, fmt="jpeg", frame_id="front", log_time=42_000_000_000)
    data, fmt = fp.extract_compressed_image(payload)
    assert data == jpeg
    assert fmt == "jpeg"


def test_extract_compressed_image_skips_unknown_fields():
    # Only field 2 (data) present, plus a stray varint field the scanner must skip.
    from fake_server import _ld, _varint

    payload = _varint((9 << 3) | 0) + _varint(12345) + _ld(2, b"IMG") + _ld(3, b"jpeg")
    data, fmt = fp.extract_compressed_image(payload)
    assert data == b"IMG" and fmt == "jpeg"
