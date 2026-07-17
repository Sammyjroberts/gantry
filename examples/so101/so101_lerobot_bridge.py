#!/usr/bin/env python3
"""SO-101 -> Gantry bridge built on LeRobot's own device stack.

Prefer this over so101_bridge.py when you already use LeRobot: you inherit
their serial handling AND your saved calibration, so positions arrive as real
calibrated degrees (gripper as 0-100%) instead of raw-center approximations.

    pip install lerobot pyserial
    python so101_lerobot_bridge.py --leader-port COM4 --follower-port COM5 \
        --leader-id my_leader --follower-id my_follower

The --*-id values are the LeRobot ids you calibrated with (they select the
calibration file). Telemetry lands identically to so101_bridge.py: devices
so101-leader / so101-follower, one packet per joint, pos (deg / % for
gripper) at the poll rate plus temp_c / voltage_v / load at ~2 Hz.

LeRobot's internal APIs move between releases; the imports below match the
2026-era layout (lerobot.robots.so_follower / lerobot.teleoperators.so_leader)
with fallbacks. If an import fails, run so101_bridge.py instead — it needs
only pyserial.
"""

import argparse
import time

from so101_bridge import GantryPublisher, JOINTS  # reuse the HTTP publisher

JOINT_NAMES = list(JOINTS.values())


def _imports():
    """Resolve LeRobot classes across recent layouts."""
    try:
        from lerobot.robots.so_follower import SOFollower
        from lerobot.robots.so_follower.config_so_follower import SOFollowerConfig as FollowerCfg
    except ImportError:  # older layout
        from lerobot.robots.so101_follower import SO101Follower as SOFollower  # type: ignore
        from lerobot.robots.so101_follower import SO101FollowerConfig as FollowerCfg  # type: ignore
    try:
        from lerobot.teleoperators.so_leader import SOLeader
        from lerobot.teleoperators.so_leader.config_so_leader import SOLeaderTeleopConfig as LeaderCfg
    except ImportError:
        from lerobot.teleoperators.so101_leader import SO101Leader as SOLeader  # type: ignore
        from lerobot.teleoperators.so101_leader import SO101LeaderConfig as LeaderCfg  # type: ignore
    return SOFollower, FollowerCfg, SOLeader, LeaderCfg


def _make_cfg(cfg_cls, port: str, dev_id: str):
    """Config dataclasses differ slightly across versions; try the rich form first."""
    for kwargs in (
        {"port": port, "id": dev_id, "use_degrees": True},
        {"port": port, "id": dev_id},
        {"port": port},
    ):
        try:
            return cfg_cls(**kwargs)
        except TypeError:
            continue
    raise SystemExit(f"could not construct {cfg_cls.__name__} — check your lerobot version")


def _pos_unit(joint: str) -> str:
    return "%" if joint == "gripper" else "deg"


def _publish_positions(pub: GantryPublisher, values: dict, t_ns: int) -> None:
    """values: {'shoulder_pan.pos': deg, ...} from get_observation/get_action."""
    for key, val in values.items():
        if not key.endswith(".pos"):
            continue  # skip camera frames etc.
        joint = key.removesuffix(".pos")
        if joint in JOINT_NAMES:
            pub.add(joint, "pos", float(val), t_ns)


def _publish_health(pub: GantryPublisher, bus, t_ns: int) -> None:
    """Best-effort extra registers straight off LeRobot's bus."""
    for reg, chan, scale in (
        ("Present_Temperature", "temp_c", 1.0),
        ("Present_Voltage", "voltage_v", 0.1),
        ("Present_Load", "load", 0.1),
    ):
        try:
            for joint, raw in bus.sync_read(reg, normalize=False).items():
                if joint in JOINT_NAMES:
                    pub.add(joint, chan, float(raw) * scale, t_ns)
        except Exception:
            return  # register set differs on this firmware — skip health quietly


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--leader-port")
    ap.add_argument("--follower-port")
    ap.add_argument("--leader-id", default="so101_leader")
    ap.add_argument("--follower-id", default="so101_follower")
    ap.add_argument("--endpoint", default="http://localhost:4780")
    ap.add_argument("--token", default=None)
    ap.add_argument("--hz", type=float, default=50.0)
    args = ap.parse_args()
    if not args.leader_port and not args.follower_port:
        ap.error("give at least one of --leader-port / --follower-port")

    SOFollower, FollowerCfg, SOLeader, LeaderCfg = _imports()

    arms = []  # (publisher, read_positions_fn, bus)
    if args.follower_port:
        follower = SOFollower(_make_cfg(FollowerCfg, args.follower_port, args.follower_id))
        follower.connect(calibrate=True)
        arms.append((GantryPublisher(args.endpoint, "so101-follower", args.token),
                     follower.get_observation, follower.bus))
    if args.leader_port:
        leader = SOLeader(_make_cfg(LeaderCfg, args.leader_port, args.leader_id))
        leader.connect(calibrate=True)
        arms.append((GantryPublisher(args.endpoint, "so101-leader", args.token),
                     leader.get_action, leader.bus))

    for pub, _, _ in arms:
        pub.register()

    period = 1.0 / args.hz
    health_every = max(1, int(args.hz / 2))
    cycle = 0
    last_report = time.monotonic()
    print(f"bridging {[p.device_id for p, _, _ in arms]} -> {args.endpoint} (ctrl-c to stop)")
    try:
        while True:
            start = time.monotonic()
            t_ns = time.time_ns()
            for pub, read_positions, bus in arms:
                _publish_positions(pub, read_positions(), t_ns)
                if cycle % health_every == 0:
                    _publish_health(pub, bus, t_ns)
            if cycle % 5 == 0:
                for pub, _, _ in arms:
                    pub.flush()
            cycle += 1
            if time.monotonic() - last_report > 5:
                for pub, _, _ in arms:
                    print(f"[{pub.device_id}] sent={pub.sent} dropped={pub.dropped}")
                last_report = time.monotonic()
            sleep = period - (time.monotonic() - start)
            if sleep > 0:
                time.sleep(sleep)
    except KeyboardInterrupt:
        for pub, _, _ in arms:
            pub.flush()


if __name__ == "__main__":
    main()
