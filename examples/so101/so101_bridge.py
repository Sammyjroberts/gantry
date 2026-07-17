#!/usr/bin/env python3
"""SO-101 leader/follower -> Gantry bridge.

Polls two SO-101 arms (6x Feetech STS3215 serial-bus servos each) over their
USB adapters and publishes per-joint telemetry to a Gantry Bench via the plain
HTTP JSON ingest endpoint (ConnectRPC accepts JSON POSTs by design - no SDK
required).

Model: one Gantry device per arm; one PACKET per joint (a servo's state is a
struct); channels per packet: pos_deg at the fast rate, temp_c / voltage_v /
load at the slow rate.

Usage:
    python so101_bridge.py --leader COM4 --follower COM5
    python so101_bridge.py --follower COM5                # single arm is fine
    python so101_bridge.py --leader COM4 --endpoint http://bench-host:4780 \
        --token gtk_...                                   # remote bench

Dependency: pyserial  (pip install pyserial)
"""

import argparse
import json
import sys
import threading
import time
import urllib.request

# ---------------------------------------------------------------------------
# Feetech STS3215 protocol (Dynamixel-protocol-1 style, half duplex TTL)
# ---------------------------------------------------------------------------

PRESENT_POSITION = 56  # 2 bytes, 0..4095 over 360 deg
PRESENT_LOAD = 60      # 2 bytes, signed-ish magnitude in 0.1% units
PRESENT_VOLTAGE = 62   # 1 byte, 0.1 V units
PRESENT_TEMP = 63      # 1 byte, deg C

INSTR_READ = 0x02
INSTR_SYNC_READ = 0x82
BROADCAST_ID = 0xFE

JOINTS = {  # servo id -> SO-101 joint name (LeRobot convention)
    1: "shoulder_pan",
    2: "shoulder_lift",
    3: "elbow_flex",
    4: "wrist_flex",
    5: "wrist_roll",
    6: "gripper",
}


def _checksum(payload: bytes) -> int:
    return (~sum(payload)) & 0xFF


class FeetechBus:
    """Minimal half-duplex master for STS-series servos on one serial port."""

    def __init__(self, port: str, baud: int = 1_000_000, timeout_s: float = 0.05):
        import serial  # pyserial

        self.ser = serial.Serial(port, baudrate=baud, timeout=timeout_s)

    def close(self) -> None:
        self.ser.close()

    def _send(self, servo_id: int, instr: int, params: bytes) -> None:
        body = bytes([servo_id, len(params) + 2, instr]) + params
        pkt = b"\xff\xff" + body + bytes([_checksum(body)])
        self.ser.reset_input_buffer()
        self.ser.write(pkt)

    def _read_status(self):
        """Read one status packet; returns (servo_id, data bytes) or None."""
        # Hunt for the 0xFF 0xFF header (resync-safe on line noise).
        window = b""
        deadline = time.monotonic() + 0.1
        while time.monotonic() < deadline:
            b1 = self.ser.read(1)
            if not b1:
                return None
            window = (window + b1)[-2:]
            if window == b"\xff\xff":
                break
        else:
            return None
        head = self.ser.read(2)
        if len(head) < 2:
            return None
        servo_id, length = head[0], head[1]
        rest = self.ser.read(length)  # error byte + data + checksum
        if len(rest) < length or length < 2:
            return None
        body = bytes([servo_id, length]) + rest[:-1]
        if _checksum(body) != rest[-1]:
            return None
        return servo_id, rest[1:-1]  # skip error byte, drop checksum

    def read(self, servo_id: int, addr: int, size: int):
        self._send(servo_id, INSTR_READ, bytes([addr, size]))
        got = self._read_status()
        if got is None or got[0] != servo_id or len(got[1]) != size:
            return None
        return got[1]

    def sync_read(self, addr: int, size: int, ids):
        """One bus transaction, one status packet per servo. Falls back to
        per-servo reads for any id that did not answer."""
        self._send(BROADCAST_ID, INSTR_SYNC_READ, bytes([addr, size]) + bytes(ids))
        out = {}
        for _ in ids:
            got = self._read_status()
            if got is not None and len(got[1]) == size:
                out[got[0]] = got[1]
        for sid in ids:
            if sid not in out:
                data = self.read(sid, addr, size)
                if data is not None:
                    out[sid] = data
        return out


def raw_to_deg(raw: int) -> float:
    """0..4095 -> degrees centered at the 2048 midpoint."""
    return (raw - 2048) * 360.0 / 4096.0


# ---------------------------------------------------------------------------
# Gantry ingest (plain HTTP JSON against ConnectRPC)
# ---------------------------------------------------------------------------


class GantryPublisher:
    def __init__(self, endpoint: str, device_id: str, token: str | None):
        self.endpoint = endpoint.rstrip("/")
        self.device_id = device_id
        self.token = token
        self.sequence = 1
        self.frames = []
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
            with urllib.request.urlopen(req, timeout=2) as resp:
                return 200 <= resp.status < 300
        except Exception:
            return False

    def register(self) -> None:
        """Idempotent; retried from the poll loop until it lands (a bench that
        boots after the bridge must still get units/kinds)."""
        channels = []
        for joint in JOINTS.values():
            channels.append({"name": "pos_deg", "kind": "VALUE_KIND_F64", "unit": "deg", "packet": joint})
            channels.append({"name": "temp_c", "kind": "VALUE_KIND_F64", "unit": "degC", "packet": joint})
            channels.append({"name": "voltage_v", "kind": "VALUE_KIND_F64", "unit": "V", "packet": joint})
            channels.append({"name": "load", "kind": "VALUE_KIND_F64", "unit": "%", "packet": joint})
        self.registered = self._post(
            "IngestService/RegisterChannels",
            {"deviceId": self.device_id, "channels": channels},
        )

    def add(self, packet: str, channel: str, value: float, t_ns: int) -> None:
        self.frames.append(
            {
                "channel": channel,
                "packet": packet,
                "timestampNs": str(t_ns),
                "value": {"f64": value},
            }
        )

    def flush(self) -> None:
        if not self.frames:
            return
        batch = {"deviceId": self.device_id, "sequence": self.sequence, "frames": self.frames}
        if self._post("IngestService/PublishBatch", {"batch": batch}):
            self.sent += len(self.frames)
            if not self.registered:
                self.register()
        else:
            self.dropped += len(self.frames)
        # Sequence advances even on drop so the server sees honest gaps.
        self.sequence += 1
        self.frames = []


# ---------------------------------------------------------------------------
# Poll loop: fast lane (positions) + slow lane (health) per arm
# ---------------------------------------------------------------------------


def run_arm(name: str, port: str, endpoint: str, token: str | None, hz: float, stop: threading.Event) -> None:
    pub = GantryPublisher(endpoint, name, token)
    pub.register()
    try:
        bus = FeetechBus(port)
    except Exception as e:
        print(f"[{name}] cannot open {port}: {e}", file=sys.stderr)
        return
    ids = list(JOINTS)
    period = 1.0 / hz
    cycle = 0
    last_report = time.monotonic()
    while not stop.is_set():
        start = time.monotonic()
        t_ns = time.time_ns()

        positions = bus.sync_read(PRESENT_POSITION, 2, ids)
        for sid, data in positions.items():
            raw = data[0] | (data[1] << 8)
            pub.add(JOINTS[sid], "pos_deg", raw_to_deg(raw & 0x0FFF), t_ns)

        if cycle % max(1, int(hz / 2)) == 0:  # health at ~2 Hz
            for sid in ids:
                joint = JOINTS[sid]
                v = bus.read(sid, PRESENT_VOLTAGE, 1)
                if v is not None:
                    pub.add(joint, "voltage_v", v[0] / 10.0, t_ns)
                t = bus.read(sid, PRESENT_TEMP, 1)
                if t is not None:
                    pub.add(joint, "temp_c", float(t[0]), t_ns)
                ld = bus.read(sid, PRESENT_LOAD, 2)
                if ld is not None:
                    raw = ld[0] | (ld[1] << 8)
                    mag = (raw & 0x3FF) / 10.0
                    pub.add(joint, "load", -mag if raw & 0x400 else mag, t_ns)

        if cycle % 5 == 0:  # ~10 Hz HTTP posts at the default 50 Hz poll
            pub.flush()
        cycle += 1

        if time.monotonic() - last_report > 5:
            print(f"[{name}] sent={pub.sent} dropped={pub.dropped} joints={len(positions)}/6")
            last_report = time.monotonic()

        sleep = period - (time.monotonic() - start)
        if sleep > 0:
            time.sleep(sleep)
    pub.flush()
    bus.close()


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--leader", help="serial port of the leader arm (e.g. COM4)")
    ap.add_argument("--follower", help="serial port of the follower arm (e.g. COM5)")
    ap.add_argument("--endpoint", default="http://localhost:4780")
    ap.add_argument("--token", default=None, help="gtk_ access token for a remote bench")
    ap.add_argument("--hz", type=float, default=50.0, help="position poll rate per arm")
    args = ap.parse_args()
    arms = [(n, p) for n, p in (("so101-leader", args.leader), ("so101-follower", args.follower)) if p]
    if not arms:
        ap.error("give at least one of --leader / --follower")

    stop = threading.Event()
    threads = [
        threading.Thread(target=run_arm, args=(n, p, args.endpoint, args.token, args.hz, stop), daemon=True)
        for n, p in arms
    ]
    for t in threads:
        t.start()
    print(f"bridging {[n for n, _ in arms]} -> {args.endpoint}  (ctrl-c to stop)")
    try:
        while any(t.is_alive() for t in threads):
            time.sleep(0.5)
    except KeyboardInterrupt:
        stop.set()
        for t in threads:
            t.join(timeout=2)


if __name__ == "__main__":
    main()
