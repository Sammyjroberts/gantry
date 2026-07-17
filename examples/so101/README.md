# SO-101 → Gantry bridge

Streams a pair of SO-101 arms (leader + follower, 6× Feetech STS3215 each)
into a Gantry Bench as two devices with one packet per joint.

## Run

```sh
pip install pyserial
python so101_bridge.py --leader COM4 --follower COM5
```

Find the COM ports in Device Manager (two USB serial adapters, one per arm) —
or run with a single arm. Against a remote bench add
`--endpoint http://bench-host:4780 --token gtk_...`.

## What you get

- Devices `so101-leader` / `so101-follower`; packets `shoulder_pan`,
  `shoulder_lift`, `elbow_flex`, `wrist_flex`, `wrist_roll`, `gripper`.
- `pos_deg` at 50 Hz per joint (sync-read, one bus transaction), plus
  `temp_c` / `voltage_v` / `load` at ~2 Hz.
- Registration is retried until the bench is up — start order doesn't matter.
- Sequence numbers advance on dropped batches so gaps are honest.

## Bench setup that makes it sing

1. Hardware page: promote both devices, name them.
2. Workspace: charts for matching leader/follower joints on top of each other —
   teleop tracking is instantly visible; add temp/load value panels.
3. Record an experiment around a teleop session; replay it; export CSV.
4. 3D (optional): the SO-ARM100/101 URDF + STLs (TheRobotStudio repo) can be
   uploaded via `/models/` — flatten mesh filenames so `<mesh>` refs resolve to
   sibling `/models/<file>` names (the live URDF editor makes the path edits
   painless), then bind each joint's `pos_deg` on the hardware detail page.

## Notes

- Positions are raw-center degrees ((raw−2048)·360/4096) without per-servo
  calibration offsets; zero your arms mechanically or adjust in a later pass.
- The Feetech protocol impl here is deliberately minimal (~100 lines, pyserial
  only). If your servos' firmware misbehaves with broadcast sync-read, the
  bridge silently falls back to per-servo reads (slower but correct).
