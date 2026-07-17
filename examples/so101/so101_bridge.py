#!/usr/bin/env python3
"""SO-101 leader/follower -> Gantry bridge.  THE command for the kit.

Plug the arm(s) in and run with no arguments: the bridge finds the Feetech USB
adapters, figures out which is the leader and which is the follower (a one-time
"unplug the leader" pairing it then remembers), and streams per-joint telemetry
to a Gantry Bench over the plain HTTP JSON ingest endpoint (ConnectRPC accepts
JSON POSTs -- no SDK required).

Two backends, auto-selected:
  * lerobot  -- if `import lerobot` succeeds, we drive the arms through LeRobot's
                own device stack, so positions arrive as *calibrated* degrees
                (gripper as 0-100 %).  Preferred.
  * raw      -- otherwise a ~100-line pyserial-only Feetech master; positions are
                raw-center degrees ((raw-2048)*360/4096), no calibration.
Force either with --use-lerobot / --no-lerobot.

Telemetry model: one Gantry device per arm (so101-leader / so101-follower); one
PACKET per joint; channel "pos" at the fast rate, "temp_c" / "voltage_v" /
"load" at ~2 Hz.  When BOTH arms stream, the follower device also carries
"track_err" per joint (leader pos - follower pos) -- the teleop-quality signal,
free of any server-side work.  A "read_errors" counter rides on packet "gantry".

Usage:
    python so101_bridge.py                     # auto-detect + pair both arms
    python so101_bridge.py --follower COM5     # single arm, explicit port
    python so101_bridge.py --leader COM4 --follower COM5
    python so101_bridge.py --endpoint http://bench-host:4780 --token gtk_...
    python so101_bridge.py --setup             # run so101_setup.py first, then bridge

Dependency: pyserial  (pip install pyserial).  lerobot is optional.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import threading
import time
import urllib.request

from so101_ports import (
    NoAdaptersFound,
    PairingFailed,
    describe_ports,
    resolve_ports,
)

# ---------------------------------------------------------------------------
# Feetech STS3215 protocol (Dynamixel-protocol-1 style, half duplex TTL).
# Register addresses verified against lerobot feetech/tables.py (STS3215):
#   Present_Position (56,2)  Present_Load (60,2)  Present_Voltage (62,1)
#   Present_Temperature (63,1).  Model resolution 4096.
# ---------------------------------------------------------------------------

PRESENT_POSITION = 56  # 2 bytes, 0..4095 over 360 deg
PRESENT_LOAD = 60      # 2 bytes, sign-magnitude, sign bit 10, 0.1% units
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
JOINT_NAMES = list(JOINTS.values())
IDS = list(JOINTS)


# --- pure packet codec (hardware-free, unit-tested against byte fixtures) ---


def feetech_checksum(payload: bytes) -> int:
    return (~sum(payload)) & 0xFF


def build_instruction(servo_id: int, instr: int, params: bytes = b"") -> bytes:
    """Full instruction packet: FF FF ID LEN INSTR PARAMS... CHECKSUM."""
    params = bytes(params)
    body = bytes([servo_id, len(params) + 2, instr]) + params
    return b"\xff\xff" + body + bytes([feetech_checksum(body)])


def build_read(servo_id: int, addr: int, size: int) -> bytes:
    return build_instruction(servo_id, INSTR_READ, bytes([addr, size]))


def build_sync_read(addr: int, size: int, ids) -> bytes:
    return build_instruction(BROADCAST_ID, INSTR_SYNC_READ, bytes([addr, size]) + bytes(ids))


def build_status(servo_id: int, data: bytes = b"", error: int = 0) -> bytes:
    """Build a status (response) packet -- the servo's reply. Used by the bus
    for nothing, but it is the fixture generator the parser tests read back."""
    data = bytes(data)
    length = len(data) + 2  # error byte + checksum
    body = bytes([servo_id, length, error]) + data
    return b"\xff\xff" + body + bytes([feetech_checksum(body)])


def read_status(read_fn, hunt_seconds: float = 0.1):
    """Parse one status packet from a byte source.

    ``read_fn(n)`` returns up to n bytes (b"" on timeout/EOF), matching
    pyserial's Serial.read.  Hunts for the 0xFF 0xFF header so a burst of line
    noise before a real packet resynchronises instead of corrupting it.
    Returns (servo_id, data_bytes) with the error byte and checksum stripped,
    or None on timeout / short packet / checksum mismatch.
    """
    window = b""
    deadline = time.monotonic() + hunt_seconds
    while time.monotonic() < deadline:
        b1 = read_fn(1)
        if not b1:
            return None
        window = (window + b1)[-2:]
        if window == b"\xff\xff":
            break
    else:
        return None
    head = read_fn(2)
    if len(head) < 2:
        return None
    servo_id, length = head[0], head[1]
    if length < 2:
        return None
    rest = read_fn(length)  # error byte + data + checksum
    if len(rest) < length:
        return None
    body = bytes([servo_id, length]) + rest[:-1]
    if feetech_checksum(body) != rest[-1]:
        return None
    return servo_id, rest[1:-1]


def raw_to_deg(raw: int) -> float:
    """0..4095 -> degrees centered at the 2048 midpoint."""
    return (raw - 2048) * 360.0 / 4096.0


class FeetechBus:
    """Minimal half-duplex master for STS-series servos on one serial port.

    Serial errors (a yanked USB cable) propagate to the caller as OSError so the
    bridge's reconnect loop can catch them; per-servo *timeouts* return no data
    (an empty slot in sync_read) rather than raising.
    """

    def __init__(self, port: str, baud: int = 1_000_000, timeout_s: float = 0.02):
        import serial  # pyserial, imported lazily so pure tests need no hardware

        self.port = port
        self.ser = serial.Serial(port, baudrate=baud, timeout=timeout_s)

    def close(self) -> None:
        try:
            self.ser.close()
        except Exception:
            pass

    def _txn(self, packet: bytes) -> None:
        self.ser.reset_input_buffer()
        self.ser.write(packet)

    def read(self, servo_id: int, addr: int, size: int):
        self._txn(build_read(servo_id, addr, size))
        got = read_status(self.ser.read)
        if got is None or got[0] != servo_id or len(got[1]) != size:
            return None
        return got[1]

    def sync_read(self, addr: int, size: int, ids):
        """One broadcast transaction, one status packet per servo. Falls back to
        per-servo reads for any id that did not answer."""
        self._txn(build_sync_read(addr, size, ids))
        out = {}
        for _ in ids:
            got = read_status(self.ser.read)
            if got is not None and len(got[1]) == size:
                out[got[0]] = got[1]
        for sid in ids:
            if sid not in out:
                data = self.read(sid, addr, size)
                if data is not None:
                    out[sid] = data
        return out


# ---------------------------------------------------------------------------
# Backends: a small uniform surface the poll loop drives.  Each exposes
# open() / read_positions() / read_health() / missing_count() / pos_unit() /
# close(), plus .role and .port.
# ---------------------------------------------------------------------------


class RawBackend:
    """pyserial-only Feetech master. Positions are raw-center degrees."""

    def __init__(self, role: str, port: str, dev_id: str, baud: int = 1_000_000):
        self.role = role
        self.port = port
        self.device_id = dev_id
        self.baud = baud
        self.bus = None
        self._missing = 0

    def open(self) -> None:
        self.bus = FeetechBus(self.port, self.baud)

    def read_positions(self) -> dict:
        raw = self.bus.sync_read(PRESENT_POSITION, 2, IDS)
        self._missing = len(IDS) - len(raw)
        out = {}
        for sid, data in raw.items():
            val = data[0] | (data[1] << 8)
            out[JOINTS[sid]] = raw_to_deg(val & 0x0FFF)
        return out

    def read_health(self):
        results = []
        for sid in IDS:
            joint = JOINTS[sid]
            v = self.bus.read(sid, PRESENT_VOLTAGE, 1)
            if v is not None:
                results.append((joint, "voltage_v", v[0] / 10.0))
            t = self.bus.read(sid, PRESENT_TEMP, 1)
            if t is not None:
                results.append((joint, "temp_c", float(t[0])))
            ld = self.bus.read(sid, PRESENT_LOAD, 2)
            if ld is not None:
                raw = ld[0] | (ld[1] << 8)
                mag = (raw & 0x3FF) / 10.0
                results.append((joint, "load", -mag if raw & 0x400 else mag))
        return results

    def missing_count(self) -> int:
        return self._missing

    def pos_unit(self, joint: str) -> str:
        return "deg"

    def close(self) -> None:
        if self.bus is not None:
            self.bus.close()
            self.bus = None


def make_backend(kind: str, role: str, port: str, dev_id: str):
    if kind == "lerobot":
        from so101_lerobot_bridge import LerobotBackend

        return LerobotBackend(role, port, dev_id)
    return RawBackend(role, port, dev_id)


def choose_backend(force_lerobot, force_raw) -> str:
    if force_raw:
        return "raw"
    if force_lerobot:
        return "lerobot"
    try:
        from so101_lerobot_bridge import lerobot_available

        return "lerobot" if lerobot_available() else "raw"
    except Exception:
        return "raw"


# ---------------------------------------------------------------------------
# Tracking error: pure pairing of two arms' joint positions
# ---------------------------------------------------------------------------


def track_errors(leader: dict, follower: dict) -> dict:
    """leader pos - follower pos for every joint both arms reported (same unit)."""
    return {j: leader[j] - follower[j] for j in follower if j in leader}


class SharedLeader:
    """Thread-safe latest-leader-positions handoff for tracking error."""

    def __init__(self):
        self._lock = threading.Lock()
        self._pos = {}

    def update(self, positions: dict) -> None:
        with self._lock:
            self._pos = dict(positions)

    def snapshot(self) -> dict:
        with self._lock:
            return dict(self._pos)


# ---------------------------------------------------------------------------
# Gantry ingest (plain HTTP JSON against ConnectRPC)
# ---------------------------------------------------------------------------


def channel_specs(role: str, pos_unit_fn, dual: bool):
    """ChannelInfo dicts for one device's RegisterChannels call."""
    ch = []
    for joint in JOINT_NAMES:
        ch.append({"name": "pos", "kind": "VALUE_KIND_F64", "unit": pos_unit_fn(joint), "packet": joint})
        ch.append({"name": "temp_c", "kind": "VALUE_KIND_F64", "unit": "degC", "packet": joint})
        ch.append({"name": "voltage_v", "kind": "VALUE_KIND_F64", "unit": "V", "packet": joint})
        ch.append({"name": "load", "kind": "VALUE_KIND_F64", "unit": "%", "packet": joint})
        if role == "follower" and dual:
            ch.append({"name": "track_err", "kind": "VALUE_KIND_F64", "unit": pos_unit_fn(joint), "packet": joint})
    ch.append({"name": "read_errors", "kind": "VALUE_KIND_F64", "unit": "count", "packet": "gantry"})
    return ch


class GantryPublisher:
    def __init__(self, endpoint: str, device_id: str, token: str | None):
        self.endpoint = endpoint.rstrip("/")
        self.device_id = device_id
        self.token = token
        self.sequence = 1
        self.frames = []
        self.channels = []
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

    def register(self, channels=None) -> None:
        """Idempotent; retried from the poll loop until it lands (a bench that
        boots after the bridge must still get units/kinds)."""
        if channels is not None:
            self.channels = channels
        self.registered = self._post(
            "IngestService/RegisterChannels",
            {"deviceId": self.device_id, "channels": self.channels},
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
# Per-arm poll loop with reconnect/backoff + a shared status line
# ---------------------------------------------------------------------------


class Backoff:
    def __init__(self, start=0.5, cap=5.0):
        self.start = start
        self.cap = cap
        self._cur = start

    def next(self) -> float:
        v = self._cur
        self._cur = min(self._cur * 2, self.cap)
        return v

    def reset(self) -> None:
        self._cur = self.start


class Stats:
    def __init__(self):
        self.reconnects = 0
        self.read_errors = 0
        self.last_joints = 0
        self.connected = False
        self._cycles = 0
        self._window_start = time.monotonic()

    def tick(self, joints: int) -> None:
        self._cycles += 1
        self.last_joints = joints

    def rate(self) -> float:
        now = time.monotonic()
        dt = now - self._window_start
        hz = self._cycles / dt if dt > 0 else 0.0
        self._cycles = 0
        self._window_start = now
        return hz


def run_arm(backend, pub, channels, hz, stop, stats, shared_leader, emit_track):
    period = 1.0 / hz
    health_every = max(1, int(hz / 2))
    backoff = Backoff()
    cycle = 0
    while not stop.is_set():
        try:
            backend.open()
        except OSError as e:
            stats.connected = False
            print(f"[{pub.device_id}] waiting for {backend.port}: {e}", file=sys.stderr)
            if stop.wait(backoff.next()):
                break
            continue
        except Exception as e:  # config/import errors are not transient
            print(f"[{pub.device_id}] cannot start backend: {e}", file=sys.stderr)
            return
        backoff.reset()
        stats.connected = True
        pub.registered = False
        pub.register(channels)
        print(f"[{pub.device_id}] connected on {backend.port}")
        try:
            while not stop.is_set():
                start = time.monotonic()
                t_ns = time.time_ns()

                positions = backend.read_positions()
                for joint, val in positions.items():
                    pub.add(joint, "pos", val, t_ns)

                if backend.role == "leader" and shared_leader is not None:
                    shared_leader.update(positions)
                if emit_track and shared_leader is not None:
                    for joint, err in track_errors(shared_leader.snapshot(), positions).items():
                        pub.add(joint, "track_err", err, t_ns)

                miss = backend.missing_count()
                if miss:
                    stats.read_errors += miss
                pub.add("gantry", "read_errors", float(stats.read_errors), t_ns)

                if cycle % health_every == 0:
                    for joint, chan, val in backend.read_health():
                        pub.add(joint, chan, val, t_ns)

                if cycle % 5 == 0:
                    pub.flush()
                stats.tick(len(positions))
                cycle += 1

                sleep = period - (time.monotonic() - start)
                if sleep > 0 and stop.wait(sleep):
                    break
        except OSError as e:  # yanked cable / lost link mid-run -> reconnect
            stats.reconnects += 1
            stats.connected = False
            print(f"[{pub.device_id}] link lost ({e}); reconnecting...", file=sys.stderr)
        finally:
            try:
                pub.flush()
            except Exception:
                pass
            backend.close()
        if not stop.is_set() and stop.wait(backoff.next()):
            break


def report_loop(reporters, stop):
    while not stop.wait(2.5):
        for pub, stats in reporters:
            state = "up" if stats.connected else "--"
            print(
                f"[{pub.device_id:>14}] {state} {stats.rate():5.1f}Hz  "
                f"joints={stats.last_joints}/6  sent={pub.sent} dropped={pub.dropped}  "
                f"reconn={stats.reconnects} rderr={stats.read_errors}"
            )


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def _run_setup() -> None:
    here = os.path.dirname(os.path.abspath(__file__))
    setup = os.path.join(here, "so101_setup.py")
    if os.path.exists(setup):
        print("running so101_setup.py ...")
        subprocess.run([sys.executable, setup], check=False)
    else:
        print("--setup: so101_setup.py not found yet; skipping", file=sys.stderr)


def build_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(
        description="SO-101 -> Gantry bridge (auto-detect ports, auto-select backend).",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    ap.add_argument("--leader", help="serial port of the leader arm (e.g. COM4); skips auto-detect")
    ap.add_argument("--follower", help="serial port of the follower arm (e.g. COM5); skips auto-detect")
    ap.add_argument("--endpoint", default="http://localhost:4780")
    ap.add_argument("--token", default=None, help="gtk_ access token for a remote bench")
    ap.add_argument("--hz", type=float, default=50.0, help="position poll rate per arm")
    ap.add_argument("--leader-id", default="so101_leader", help="lerobot calibration id for the leader")
    ap.add_argument("--follower-id", default="so101_follower", help="lerobot calibration id for the follower")
    be = ap.add_mutually_exclusive_group()
    be.add_argument("--use-lerobot", action="store_true", help="force the LeRobot (calibrated) backend")
    be.add_argument("--no-lerobot", action="store_true", help="force the raw pyserial backend")
    ap.add_argument("--setup", action="store_true", help="run so101_setup.py first, then bridge")
    ap.add_argument("--repair", action="store_true", help="ignore the saved port pairing and pair again")
    return ap


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)

    if args.setup:
        _run_setup()

    if args.repair:
        from so101_ports import DEFAULT_CACHE

        try:
            os.remove(DEFAULT_CACHE)
        except OSError:
            pass

    try:
        ports = resolve_ports(args.leader, args.follower)
    except NoAdaptersFound:
        print("No SO-101 USB adapters found.", file=sys.stderr)
        print(
            "Plug in the arm(s) and retry, or name the ports explicitly:\n"
            "    python so101_bridge.py --leader COM4 --follower COM5",
            file=sys.stderr,
        )
        attached = describe_ports()
        if attached:
            print("Serial ports currently attached:", file=sys.stderr)
            for line in attached:
                print("  " + line, file=sys.stderr)
        else:
            print("(no serial ports are attached at all)", file=sys.stderr)
        return 2
    except PairingFailed as e:
        print(f"Could not identify the arms: {e}", file=sys.stderr)
        return 2

    roles = [(r, ports.get(r)) for r in ("leader", "follower") if ports.get(r)]
    if not roles:
        print("No ports resolved for either arm.", file=sys.stderr)
        return 2
    dual = len(roles) == 2

    backend_kind = choose_backend(args.use_lerobot, args.no_lerobot)
    print(
        f"backend={backend_kind}  arms={[r for r, _ in roles]}  -> {args.endpoint}"
        f"{'  (tracking error on follower)' if dual else ''}"
    )

    shared_leader = SharedLeader() if dual else None
    dev_ids = {"leader": "so101-leader", "follower": "so101-follower"}
    cal_ids = {"leader": args.leader_id, "follower": args.follower_id}

    stop = threading.Event()
    threads = []
    reporters = []
    for role, port in roles:
        backend = make_backend(backend_kind, role, port, cal_ids[role])
        pub = GantryPublisher(args.endpoint, dev_ids[role], args.token)
        channels = channel_specs(role, backend.pos_unit, dual)
        stats = Stats()
        emit_track = dual and role == "follower"
        t = threading.Thread(
            target=run_arm,
            args=(backend, pub, channels, args.hz, stop, stats, shared_leader, emit_track),
            daemon=True,
        )
        threads.append(t)
        reporters.append((pub, stats))

    reporter = threading.Thread(target=report_loop, args=(reporters, stop), daemon=True)
    for t in threads:
        t.start()
    reporter.start()
    print("(ctrl-c to stop)")
    try:
        while any(t.is_alive() for t in threads):
            time.sleep(0.3)
    except KeyboardInterrupt:
        print("\nstopping...")
    finally:
        stop.set()
        for t in threads:
            t.join(timeout=2)
    return 0


if __name__ == "__main__":
    sys.exit(main())
