#!/usr/bin/env python3
"""Teleoperate SO-101 (leader -> follower) AND stream telemetry to Gantry.

Serial ports are exclusive: `lerobot-teleoperate` and a separate telemetry
reader cannot both own the arms. This script is the one process that does both
jobs — LeRobot's own classes drive the teleop loop (calibrated, torque on the
follower, identical to lerobot-teleoperate) while every cycle's positions land
in Gantry: leader pos, follower pos, and per-joint track_err on the follower.

    python so101_teleop_gantry.py \
        --follower-port /dev/tty.usbmodem5B610373301 --follower-id my_awesome_follower_arm \
        --leader-port  /dev/tty.usbmodem5B7B0185921 --leader-id  my_awesome_leader_arm \
        [--fps 60] [--endpoint http://localhost:4780] [--token gtk_...]

Requires: lerobot (pip install lerobot). Ctrl-C disconnects cleanly (LeRobot
disables follower torque on disconnect by default).
"""

from __future__ import annotations

import argparse
import sys
import time

from so101_bridge import JOINT_NAMES, GantryPublisher
from so101_lerobot_bridge import _imports, _make_cfg

HEALTH = (  # register name, channel, scale (raw units per feetech tables)
    ("Present_Temperature", "temp_c", 1.0),
    ("Present_Voltage", "voltage_v", 0.1),
    ("Present_Load", "load", 0.1),
)


def positions(values: dict) -> dict:
    """{'shoulder_pan.pos': v, ...} -> {'shoulder_pan': v} for known joints."""
    out = {}
    for key, val in values.items():
        if key.endswith(".pos"):
            joint = key[: -len(".pos")]
            if joint in JOINT_NAMES:
                out[joint] = float(val)
    return out


def publish_health(pub: GantryPublisher, bus, t_ns: int) -> None:
    for reg, chan, scale in HEALTH:
        try:
            readings = bus.sync_read(reg, normalize=False)
        except Exception:
            return
        for joint, raw in readings.items():
            if joint in JOINT_NAMES:
                pub.add(joint, chan, float(raw) * scale, t_ns)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--follower-port", required=True)
    ap.add_argument("--follower-id", default="so101_follower")
    ap.add_argument("--leader-port", required=True)
    ap.add_argument("--leader-id", default="so101_leader")
    ap.add_argument("--fps", type=float, default=60.0)
    ap.add_argument("--endpoint", default="http://localhost:4780")
    ap.add_argument("--token", default=None)
    args = ap.parse_args()

    SOFollower, FollowerCfg, SOLeader, LeaderCfg = _imports()
    follower = SOFollower(_make_cfg(FollowerCfg, args.follower_port, args.follower_id))
    leader = SOLeader(_make_cfg(LeaderCfg, args.leader_port, args.leader_id))
    follower.connect(calibrate=True)
    leader.connect(calibrate=True)
    print(f"teleop up: {args.leader_id} -> {args.follower_id} at {args.fps:.0f} fps")

    pub_l = GantryPublisher(args.endpoint, "so101-leader", args.token)
    pub_f = GantryPublisher(args.endpoint, "so101-follower", args.token)
    pub_l.register()
    pub_f.register()

    period = 1.0 / args.fps
    health_every = max(1, int(args.fps / 2))
    cycle = 0
    last_report = time.monotonic()
    try:
        while True:
            start = time.monotonic()
            t_ns = time.time_ns()

            action = leader.get_action()          # leader positions
            follower.send_action(action)          # the actual teleop
            obs = follower.get_observation()      # follower positions

            lead = positions(action)
            foll = positions(obs)
            for joint, val in lead.items():
                pub_l.add(joint, "pos", val, t_ns)
            for joint, val in foll.items():
                pub_f.add(joint, "pos", val, t_ns)
            for joint in lead.keys() & foll.keys():
                pub_f.add(joint, "track_err", lead[joint] - foll[joint], t_ns)

            if cycle % health_every == 0:
                publish_health(pub_l, leader.bus, t_ns)
                publish_health(pub_f, follower.bus, t_ns)
            if cycle % 6 == 0:  # ~10 Hz HTTP posts at 60 fps
                pub_l.flush()
                pub_f.flush()
            cycle += 1

            if time.monotonic() - last_report > 5:
                hz = 1.0 / max(period, time.monotonic() - start)
                print(
                    f"loop ~{hz:.0f}Hz | leader sent={pub_l.sent} dropped={pub_l.dropped}"
                    f" | follower sent={pub_f.sent} dropped={pub_f.dropped}"
                )
                last_report = time.monotonic()

            sleep = period - (time.monotonic() - start)
            if sleep > 0:
                time.sleep(sleep)
    except KeyboardInterrupt:
        print("\nstopping...")
    finally:
        pub_l.flush()
        pub_f.flush()
        try:
            leader.disconnect()
        except Exception:
            pass
        try:
            follower.disconnect()  # disables torque by default
        except Exception:
            pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
