#!/usr/bin/env python3
"""LeRobot backend for the SO-101 -> Gantry bridge.

This is a thin module now: the CLI lives in so101_bridge.py, which auto-selects
this backend whenever `import lerobot` succeeds.  Driving the arms through
LeRobot's own device stack means you inherit their serial handling AND your
saved calibration, so positions arrive as real calibrated degrees (gripper as
0-100 %) instead of the raw path's raw-center approximation.

    pip install lerobot pyserial
    python so101_bridge.py --use-lerobot --leader-id my_leader --follower-id my_follower

Running this file directly still works -- it just forces the lerobot backend and
hands off to so101_bridge.main().

LeRobot's internal layout moves between releases.  Verified against 2026-era
`main`: the follower is lerobot.robots.so_follower.SOFollower with config
SOFollowerRobotConfig; the leader is lerobot.teleoperators.so_leader.SOLeader
with config SOLeaderTeleopConfig; both expose `.bus` (a FeetechMotorsBus),
connect(calibrate=True), and return positions keyed "<joint>.pos".  Imports fall
back to the SO101* aliases and older module names; if none resolve, run
`python so101_bridge.py --no-lerobot` for the pyserial-only path.
"""

from __future__ import annotations

import importlib.util
import sys

from so101_bridge import JOINT_NAMES  # reuse the joint set


def lerobot_available() -> bool:
    """True if the lerobot package can be imported (checked without importing
    its heavy submodules)."""
    try:
        return importlib.util.find_spec("lerobot") is not None
    except (ImportError, ValueError):
        return False


def _imports():
    """Resolve LeRobot classes across recent layouts.

    Ground truth (huggingface/lerobot main): directories are so_follower /
    so_leader; config classes are SOFollowerRobotConfig / SOLeaderTeleopConfig
    (the SO101* names are aliases).  We try the canonical names first, then the
    aliases, then the older so101_* module paths.
    """
    # follower
    try:
        from lerobot.robots.so_follower import SOFollower
        try:
            from lerobot.robots.so_follower.config_so_follower import SOFollowerRobotConfig as FollowerCfg
        except ImportError:
            from lerobot.robots.so_follower import SO101FollowerConfig as FollowerCfg  # type: ignore
    except ImportError:  # older layout
        from lerobot.robots.so101_follower import SO101Follower as SOFollower  # type: ignore
        from lerobot.robots.so101_follower import SO101FollowerConfig as FollowerCfg  # type: ignore
    # leader
    try:
        from lerobot.teleoperators.so_leader import SOLeader
        try:
            from lerobot.teleoperators.so_leader.config_so_leader import SOLeaderTeleopConfig as LeaderCfg
        except ImportError:
            from lerobot.teleoperators.so_leader import SO101LeaderConfig as LeaderCfg  # type: ignore
    except ImportError:
        from lerobot.teleoperators.so101_leader import SO101Leader as SOLeader  # type: ignore
        from lerobot.teleoperators.so101_leader import SO101LeaderConfig as LeaderCfg  # type: ignore
    return SOFollower, FollowerCfg, SOLeader, LeaderCfg


def _make_cfg(cfg_cls, port: str, dev_id: str):
    """Config dataclasses differ slightly across versions; try the rich form
    first (use_degrees defaults to True upstream, but we pass it explicitly)."""
    for kwargs in (
        {"port": port, "id": dev_id, "use_degrees": True},
        {"port": port, "id": dev_id},
        {"port": port},
    ):
        try:
            return cfg_cls(**kwargs)
        except TypeError:
            continue
    raise SystemExit(f"could not construct {cfg_cls.__name__} -- check your lerobot version")


# Health registers: register-name strings verified against feetech/tables.py.
# sync_read normalize is a no-op for these (only Present/Goal_Position
# normalize), so values come back raw and we scale them ourselves.
_HEALTH = (
    ("Present_Temperature", "temp_c", 1.0),
    ("Present_Voltage", "voltage_v", 0.1),
    ("Present_Load", "load", 0.1),
)


class LerobotBackend:
    """Adapts an SOFollower / SOLeader to the bridge's backend surface."""

    def __init__(self, role: str, port: str, dev_id: str):
        if role not in ("leader", "follower"):
            raise ValueError(role)
        self.role = role
        self.port = port
        self.device_id = dev_id
        self.dev = None
        self.bus = None
        self._read = None
        self._missing = 0

    def open(self) -> None:
        SOFollower, FollowerCfg, SOLeader, LeaderCfg = _imports()
        if self.role == "follower":
            self.dev = SOFollower(_make_cfg(FollowerCfg, self.port, self.device_id))
        else:
            self.dev = SOLeader(_make_cfg(LeaderCfg, self.port, self.device_id))
        self.dev.connect(calibrate=True)
        self.bus = self.dev.bus
        self._read = self.dev.get_observation if self.role == "follower" else self.dev.get_action

    def read_positions(self) -> dict:
        vals = self._read()  # {'shoulder_pan.pos': deg, ..., 'cam...': frame}
        out = {}
        for key, val in vals.items():
            if not key.endswith(".pos"):
                continue  # skip camera frames etc.
            joint = key[: -len(".pos")]
            if joint in JOINT_NAMES:
                out[joint] = float(val)
        self._missing = len(JOINT_NAMES) - len(out)
        return out

    def read_health(self):
        results = []
        for reg, chan, scale in _HEALTH:
            try:
                readings = self.bus.sync_read(reg, normalize=False)
            except Exception:
                break  # register set differs on this firmware -- skip health quietly
            for joint, raw in readings.items():
                if joint in JOINT_NAMES:
                    results.append((joint, chan, float(raw) * scale))
        return results

    def missing_count(self) -> int:
        return self._missing

    def pos_unit(self, joint: str) -> str:
        return "%" if joint == "gripper" else "deg"

    def close(self) -> None:
        if self.dev is not None:
            try:
                self.dev.disconnect()
            except Exception:
                pass
            self.dev = None


def main(argv=None) -> int:
    from so101_bridge import main as bridge_main

    argv = list(sys.argv[1:] if argv is None else argv)
    if "--no-lerobot" not in argv and "--use-lerobot" not in argv:
        argv.append("--use-lerobot")
    return bridge_main(argv)


if __name__ == "__main__":
    sys.exit(main())
