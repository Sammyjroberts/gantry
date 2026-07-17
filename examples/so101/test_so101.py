"""Pure-part tests for the SO-101 bridge. No hardware, no network.

Run:  python -m pytest examples/so101 -q
"""

import json
from urllib.error import URLError

import pytest

import so101_bridge as sb
import so101_lerobot_bridge as lb
import so101_ports as sp


# ---------------------------------------------------------------------------
# Feetech packet codec: checksum + encode against hand-built byte fixtures
# ---------------------------------------------------------------------------


def test_checksum_matches_hand_computed():
    # ~(sum) & 0xFF
    assert sb.feetech_checksum(bytes([1, 4, 2, 56, 2])) == 0xBE
    assert sb.feetech_checksum(b"") == 0xFF


def test_build_read_exact_bytes():
    # READ id=1, addr=56 (0x38), size=2 -> FF FF 01 04 02 38 02 BE
    assert sb.build_read(1, 56, 2) == bytes([0xFF, 0xFF, 0x01, 0x04, 0x02, 0x38, 0x02, 0xBE])


def test_build_sync_read_header_and_checksum():
    pkt = sb.build_sync_read(56, 2, [1, 2, 3, 4, 5, 6])
    assert pkt[:5] == bytes([0xFF, 0xFF, 0xFE, 0x0A, 0x82])  # broadcast, len=10, SYNC_READ
    assert pkt[5:7] == bytes([56, 2])  # addr, size
    assert pkt[7:13] == bytes([1, 2, 3, 4, 5, 6])  # servo ids
    body = pkt[2:-1]
    assert pkt[-1] == sb.feetech_checksum(body)


# ---------------------------------------------------------------------------
# Status-packet parse: round-trip, corruption, resync
# ---------------------------------------------------------------------------


def _feed(data: bytes):
    """A pyserial-Serial.read-alike over a fixed byte buffer (b'' when drained)."""
    box = {"i": 0}

    def read(n):
        i = box["i"]
        chunk = data[i : i + n]
        box["i"] = i + len(chunk)
        return chunk

    return read


def test_status_roundtrip():
    pkt = sb.build_status(1, bytes([0x00, 0x08]))  # raw 2048 = center
    assert pkt == bytes([0xFF, 0xFF, 0x01, 0x04, 0x00, 0x00, 0x08, 0xF2])
    got = sb.read_status(_feed(pkt))
    assert got == (1, bytes([0x00, 0x08]))
    raw = got[1][0] | (got[1][1] << 8)
    assert raw == 2048
    assert sb.raw_to_deg(raw) == 0.0


def test_status_corrupt_checksum_rejected():
    pkt = bytearray(sb.build_status(3, bytes([0x10, 0x00])))
    pkt[-1] ^= 0xFF  # smash the checksum
    assert sb.read_status(_feed(bytes(pkt))) is None


def test_status_resync_after_line_noise():
    junk = bytes([0x11, 0x22, 0x33, 0xFF, 0x00])  # noise incl. a lone 0xFF
    pkt = sb.build_status(2, bytes([0xAB, 0x03]))
    got = sb.read_status(_feed(junk + pkt))
    assert got == (2, bytes([0xAB, 0x03]))


def test_status_timeout_returns_none():
    assert sb.read_status(_feed(b"")) is None
    assert sb.read_status(_feed(b"\xff\xff\x01")) is None  # header only, truncated


def test_raw_to_deg_endpoints():
    assert sb.raw_to_deg(2048) == 0.0
    assert sb.raw_to_deg(0) == pytest.approx(-180.0)
    assert sb.raw_to_deg(3072) == pytest.approx(90.0)


# ---------------------------------------------------------------------------
# FeetechBus.sync_read parsing + degrade-to-remaining-servos
# ---------------------------------------------------------------------------


class _FakeSerial:
    def __init__(self, data: bytes):
        self._read = _feed(data)
        self.writes = []

    def reset_input_buffer(self):
        pass

    def write(self, pkt):
        self.writes.append(pkt)

    def read(self, n):
        return self._read(n)

    def close(self):
        pass


def _bus_over(data: bytes):
    bus = sb.FeetechBus.__new__(sb.FeetechBus)  # bypass serial.Serial()
    bus.ser = _FakeSerial(data)
    bus.port = "COMX"
    return bus


def test_sync_read_parses_all_servos_any_order():
    # Two status packets, servo 2 first: keyed by returned id, order-independent.
    stream = sb.build_status(2, bytes([0x00, 0x0C])) + sb.build_status(1, bytes([0x00, 0x08]))
    out = _bus_over(stream).sync_read(56, 2, [1, 2])
    assert set(out) == {1, 2}
    assert (out[1][0] | out[1][1] << 8) == 2048
    assert (out[2][0] | out[2][1] << 8) == 3072


def test_sync_read_degrades_when_a_servo_is_silent():
    # Only servo 1 answers; servo 2 never does (and the per-servo fallback also
    # reads nothing) -> result has just servo 1.
    out = _bus_over(sb.build_status(1, bytes([0x00, 0x08]))).sync_read(56, 2, [1, 2])
    assert set(out) == {1}


class _FakeBus:
    def __init__(self, table):
        self.table = table

    def sync_read(self, addr, size, ids):
        return {sid: bytes([self.table[sid] & 0xFF, (self.table[sid] >> 8) & 0xFF]) for sid in ids if sid in self.table}

    def read(self, sid, addr, size):
        return None

    def close(self):
        pass


def test_raw_backend_positions_and_missing_count():
    be = sb.RawBackend("follower", "COMX", "so101-follower")
    be.bus = _FakeBus({1: 2048, 2: 3072})  # 4 of 6 servos silent
    pos = be.read_positions()
    assert pos["shoulder_pan"] == 0.0
    assert pos["shoulder_lift"] == pytest.approx(90.0)
    assert be.missing_count() == 4


# ---------------------------------------------------------------------------
# Tracking error pairing
# ---------------------------------------------------------------------------


def test_track_errors_only_shared_joints():
    leader = {"shoulder_pan": 10.0, "elbow_flex": 5.0, "wrist_roll": 1.0}
    follower = {"shoulder_pan": 7.0, "elbow_flex": 5.5, "gripper": 50.0}
    errs = sb.track_errors(leader, follower)
    assert errs == {"shoulder_pan": pytest.approx(3.0), "elbow_flex": pytest.approx(-0.5)}
    assert "wrist_roll" not in errs  # leader-only
    assert "gripper" not in errs  # follower-only


def test_shared_leader_snapshot_is_isolated():
    s = sb.SharedLeader()
    s.update({"gripper": 12.0})
    snap = s.snapshot()
    snap["gripper"] = 999.0  # mutating the copy must not corrupt shared state
    assert s.snapshot() == {"gripper": 12.0}


# ---------------------------------------------------------------------------
# Channel registration spec
# ---------------------------------------------------------------------------


def test_channel_specs_follower_dual_has_track_err():
    ch = sb.channel_specs("follower", lambda j: "%" if j == "gripper" else "deg", dual=True)
    names = {(c["packet"], c["name"]) for c in ch}
    assert ("shoulder_pan", "track_err") in names
    assert ("gantry", "read_errors") in names
    # gripper pos unit follows the backend's pos_unit fn
    gpos = next(c for c in ch if c["packet"] == "gripper" and c["name"] == "pos")
    assert gpos["unit"] == "%"


def test_channel_specs_leader_and_single_have_no_track_err():
    for role, dual in (("leader", True), ("follower", False)):
        ch = sb.channel_specs(role, lambda j: "deg", dual=dual)
        assert not any(c["name"] == "track_err" for c in ch)
        assert any(c["packet"] == "gantry" and c["name"] == "read_errors" for c in ch)


# ---------------------------------------------------------------------------
# Publisher: batching, sequence-advances-on-drop, JSON shape (mock urlopen)
# ---------------------------------------------------------------------------


class _Resp:
    status = 200

    def __enter__(self):
        return self

    def __exit__(self, *a):
        return False


def test_publisher_batch_shape_and_sequence(monkeypatch):
    posted = []

    def fake_urlopen(req, timeout=None):
        posted.append((req.full_url, json.loads(req.data.decode())))
        return _Resp()

    monkeypatch.setattr(sb.urllib.request, "urlopen", fake_urlopen)
    pub = sb.GantryPublisher("http://bench:4780/", "so101-follower", None)
    pub.registered = True  # isolate the batch POST from the register-on-first-ack
    pub.add("shoulder_pan", "pos", 12.5, 111)
    pub.add("gantry", "read_errors", 0.0, 111)
    assert pub.sequence == 1
    pub.flush()
    assert pub.sent == 2 and pub.dropped == 0
    assert pub.sequence == 2  # advanced
    assert pub.frames == []  # cleared

    url, body = posted[-1]
    assert url.endswith("/gantry.v1.IngestService/PublishBatch")
    assert body["batch"]["deviceId"] == "so101-follower"
    assert body["batch"]["sequence"] == 1
    frame = body["batch"]["frames"][0]
    assert frame == {"channel": "pos", "packet": "shoulder_pan", "timestampNs": "111", "value": {"f64": 12.5}}


def test_publisher_sequence_advances_on_drop(monkeypatch):
    def down(req, timeout=None):
        raise URLError("bench unreachable")

    monkeypatch.setattr(sb.urllib.request, "urlopen", down)
    pub = sb.GantryPublisher("http://bench:4780", "so101-leader", None)
    pub.registered = True
    pub.add("elbow_flex", "pos", 1.0, 1)
    pub.flush()
    assert pub.dropped == 1 and pub.sent == 0
    assert pub.sequence == 2  # gap is honest: sequence still advances

    pub.add("elbow_flex", "pos", 2.0, 2)
    pub.flush()
    assert pub.sequence == 3  # not reused


def test_publisher_auth_header_when_token(monkeypatch):
    seen = {}

    def fake_urlopen(req, timeout=None):
        seen["auth"] = req.get_header("Authorization")
        return _Resp()

    monkeypatch.setattr(sb.urllib.request, "urlopen", fake_urlopen)
    pub = sb.GantryPublisher("http://bench:4780", "d", "gtk_secret")
    pub.register([{"name": "pos", "kind": "VALUE_KIND_F64", "unit": "deg", "packet": "x"}])
    assert seen["auth"] == "Bearer gtk_secret"


# ---------------------------------------------------------------------------
# Port persistence round-trip + candidate/adapter recognition
# ---------------------------------------------------------------------------


def test_port_map_roundtrip(tmp_path):
    path = str(tmp_path / ".so101_ports.json")
    sp.save_port_map({"leader": "COM4", "follower": "COM5", "junk": "x"}, path)
    assert sp.load_port_map(path) == {"leader": "COM4", "follower": "COM5"}


def test_port_map_missing_or_corrupt(tmp_path):
    assert sp.load_port_map(str(tmp_path / "nope.json")) == {}
    bad = tmp_path / "bad.json"
    bad.write_text("{not json", encoding="utf-8")
    assert sp.load_port_map(str(bad)) == {}


def test_is_known_adapter():
    assert sp.is_known_adapter(0x1A86, 0x7523)  # CH340
    assert sp.is_known_adapter(0x10C4, 0xEA60)  # CP210x
    assert not sp.is_known_adapter(0x1234, 0x5678)
    assert not sp.is_known_adapter(None, None)


# ---------------------------------------------------------------------------
# Auto-detect + interactive pairing (injected I/O, no hardware)
# ---------------------------------------------------------------------------


def test_interactive_pair_unplug_diff():
    calls = iter([["COM4", "COM5"], ["COM5"]])  # before, after unplug
    mapping = sp.interactive_pair(
        list_ports_fn=lambda: next(calls),
        input_fn=lambda *_: "",
        print_fn=lambda *_: None,
    )
    assert mapping == {"leader": "COM4", "follower": "COM5"}


def test_interactive_pair_ambiguous_raises():
    calls = iter([["COM4", "COM5"], ["COM4", "COM5"]])  # nothing disappeared
    with pytest.raises(sp.PairingFailed):
        sp.interactive_pair(list_ports_fn=lambda: next(calls), input_fn=lambda *_: "", print_fn=lambda *_: None)


def test_resolve_ports_override_short_circuits():
    called = []
    out = sp.resolve_ports(leader="COM9", list_ports_fn=lambda: called.append(1) or [])
    assert out == {"leader": "COM9", "follower": None}
    assert not called  # detection skipped entirely


def test_resolve_ports_no_candidates_raises():
    with pytest.raises(sp.NoAdaptersFound):
        sp.resolve_ports(list_ports_fn=lambda: [])


def test_resolve_ports_uses_cache_when_present(tmp_path):
    path = str(tmp_path / "ports.json")
    sp.save_port_map({"leader": "COM4", "follower": "COM5"}, path)
    out = sp.resolve_ports(
        cache_path=path,
        list_ports_fn=lambda: ["COM4", "COM5"],
        input_fn=lambda *_: pytest.fail("should not prompt when cache is valid"),
        print_fn=lambda *_: None,
    )
    assert out == {"leader": "COM4", "follower": "COM5"}


def test_resolve_ports_pairs_and_persists_two_candidates(tmp_path):
    path = str(tmp_path / "ports.json")
    seq = iter([["COM4", "COM5"], ["COM4", "COM5"], ["COM5"]])  # resolve list, pair before, pair after
    out = sp.resolve_ports(
        cache_path=path,
        list_ports_fn=lambda: next(seq),
        input_fn=lambda *_: "",
        print_fn=lambda *_: None,
    )
    assert out == {"leader": "COM4", "follower": "COM5"}
    assert sp.load_port_map(path) == {"leader": "COM4", "follower": "COM5"}  # remembered


def test_resolve_ports_ambiguous_count_raises():
    with pytest.raises(sp.PairingFailed):
        sp.resolve_ports(list_ports_fn=lambda: ["COM4", "COM5", "COM6"])


# ---------------------------------------------------------------------------
# Backend selection + lerobot availability
# ---------------------------------------------------------------------------


def test_choose_backend_forcing():
    assert sb.choose_backend(force_lerobot=False, force_raw=True) == "raw"
    assert sb.choose_backend(force_lerobot=True, force_raw=False) == "lerobot"


def test_choose_backend_auto_follows_availability(monkeypatch):
    monkeypatch.setattr(lb, "lerobot_available", lambda: False)
    assert sb.choose_backend(False, False) == "raw"
    monkeypatch.setattr(lb, "lerobot_available", lambda: True)
    assert sb.choose_backend(False, False) == "lerobot"


def test_lerobot_backend_position_key_parsing():
    be = lb.LerobotBackend.__new__(lb.LerobotBackend)
    be.role = "follower"
    be._read = lambda: {"shoulder_pan.pos": 3.0, "gripper.pos": 55.0, "wrist_cam": object()}
    pos = be.read_positions()
    assert pos == {"shoulder_pan": 3.0, "gripper": 55.0}
    assert be.missing_count() == 4  # 6 joints - 2 reported


# ---------------------------------------------------------------------------
# No-hardware smoke: CLI exits with friendly guidance when nothing is attached
# ---------------------------------------------------------------------------


def test_cli_reports_no_adapters(monkeypatch, capsys):
    def raise_none(*a, **k):
        raise sp.NoAdaptersFound()

    monkeypatch.setattr(sb, "resolve_ports", raise_none)
    monkeypatch.setattr(sb, "describe_ports", lambda: [])
    rc = sb.main([])
    assert rc == 2
    err = capsys.readouterr().err
    assert "No SO-101 USB adapters found" in err
    assert "--leader" in err  # actionable guidance
