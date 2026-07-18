# SO-101 → Gantry bridge

Streams a pair of SO-101 arms (leader + follower, 6× Feetech STS3215 each) into
a Gantry Bench as two devices with one packet per joint. Plug the arms in and
run one command — it finds the ports, learns which arm is which, picks the best
backend, and streams.

## Quick start

```sh
pip install pyserial        # lerobot is optional (see Backends)
python so101_bridge.py
```

With nothing else specified the bridge:

1. scans USB-serial adapters and keeps the ones that look like Feetech bridges
   (CH340/CH343/CH9102, CP210x, FTDI — by USB VID:PID);
2. with exactly two, runs a **one-time pairing**: *"Unplug the LEADER arm's USB
   cable now, then press Enter."* The port that disappears is the leader; the
   result is saved to `.so101_ports.json` so it never asks again;
3. streams both arms to `http://localhost:4780`.

Override any of this:

```sh
python so101_bridge.py --follower COM5                  # single arm, explicit port
python so101_bridge.py --leader COM4 --follower COM5    # both explicit (skips detection)
python so101_bridge.py --repair                         # forget the saved pairing, pair again
python so101_bridge.py --endpoint http://bench-host:4780 --token gtk_...
python so101_bridge.py --setup                          # run so101_setup.py first, then bridge
```

If no adapters are found the bridge prints friendly guidance (and lists whatever
serial ports *are* attached) and exits — it never hangs waiting on hardware.

## Backends (auto-selected)

`so101_bridge.py` is the single entrypoint; it chooses a backend at startup:

| Backend | When | Positions |
| --- | --- | --- |
| **lerobot** | `import lerobot` succeeds | real **calibrated** degrees, gripper as 0–100 % (uses your saved calibration) |
| **raw** | otherwise (pyserial only) | raw-center degrees `((raw−2048)·360/4096)`, uncalibrated |

Force it with `--use-lerobot` or `--no-lerobot`. The lerobot path selects your
calibration by id via `--leader-id` / `--follower-id`. `so101_lerobot_bridge.py`
is now a thin backend module — running it directly just forces `--use-lerobot`
and hands off to the same CLI.

Both backends emit the position channel under the **same name, `pos`**, so the
bench setup and 3D bindings work identically whichever backend runs.

## What you get

- Devices `so101-leader` / `so101-follower`; packets `shoulder_pan`,
  `shoulder_lift`, `elbow_flex`, `wrist_flex`, `wrist_roll`, `gripper`.
- `pos` at 50 Hz per joint (sync-read, one bus transaction), plus `temp_c` /
  `voltage_v` / `load` at ~2 Hz.
- **`track_err`** on each follower joint whenever *both* arms stream: leader pos
  − follower pos, same unit, every fast cycle — teleop tracking quality with
  zero server-side work.
- **`read_errors`** counter on packet `gantry`: a running total of per-servo
  read failures, so a flaky servo shows up as a rising line, not silence.
- Registration is retried until the bench is up — start order doesn't matter.
- Sequence numbers advance on dropped batches so gaps are honest.

Console output is one tight status line per arm every ~2.5 s:

```
[ so101-follower] up  49.8Hz  joints=6/6  sent=1490 dropped=0  reconn=0 rderr=0
```

## Robustness

- **Serial disconnect → reconnect with backoff.** Yank an arm's USB mid-run and
  the bridge logs it, keeps the other arm streaming, and reconnects when the arm
  comes back (0.5 s → 5 s backoff). It never dies on unplug.
- **Per-servo read failures degrade gracefully.** A servo that doesn't answer is
  dropped from that cycle (the others still publish) and counted on
  `gantry/read_errors`.
- **Clean Ctrl-C.** Flushes and stops both arms.

## One-command bench setup

`so101_setup.py` (uploads the 3D model, binds each joint, and builds a "SO-101
Teleop" workspace) is the companion tool — run it once against your bench, or
pass `--setup` to `so101_bridge.py` to run it first and then start bridging:

```sh
python so101_setup.py                       # local bench
python so101_setup.py --endpoint http://bench-host:4780 --token gtk_...
python so101_setup.py --dry-run             # show the plan, change nothing
```

It binds joints to the `pos` channel by default, which both backends emit.

Meshes are pinned + sha256-verified in `model/MANIFEST.json` (sourced from
TheRobotStudio/SO-ARM100, Apache-2.0 — see `model/NOTICE`) and cache under
`model/.cache` (gitignored) so re-runs are offline. Re-running is safe:
uploads overwrite in place and an existing "SO-101 Teleop" workspace is left
untouched.

### 3D model

`model/` carries the SO-101 URDF plus pinned, checksummed meshes
(`MANIFEST.json`) sourced from TheRobotStudio/SO-ARM100 (Apache-2.0; see
`model/NOTICE`). `so101_setup.py` downloads, verifies, and uploads them to the
bench's `/models/` store and wires the per-joint 3D visualization. To do it by
hand instead: upload the URDF + STLs via `/models/`, then bind each joint's
`pos` channel on the hardware detail page.

## Splitter (teleoperate + Gantry simultaneously)

`so101_teleop_gantry.py` above runs teleop *and* Gantry from one Python process.
`gantry-splitter` is the zero-touch alternative: a Rust daemon that sits between
`lerobot-teleoperate` and the arms as a **passive serial sniffer**, so
`lerobot-teleoperate` runs completely **unmodified** — rerun, cameras,
everything — while the splitter decodes the traffic it forwards and streams the
same per-joint telemetry to a Gantry bench. It lives in the SDK workspace at
`sdk/gantry-splitter` (macOS/Linux only — it needs PTYs).

**How it works.** For each port the splitter opens the *real* USB serial device,
creates a pseudo-terminal (PTY) pair, and pumps bytes between them with no
artificial buffering. You point `lerobot-teleoperate` at the PTY slave paths it
prints instead of the real devices; it can't tell the difference. The splitter
decodes the half-duplex Feetech traffic passing through — `Present_Position`
reads → `pos`, `Goal_Position` writes (the teleop `sync_write` actions) → `cmd`
— and publishes to the bench.

### Build (macOS)

```sh
# Rust toolchain, if you don't have it:
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
cargo build -p gantry-splitter --release   # -> target/release/gantry-splitter
```

### Use

Start the splitter with the **real** device paths, then point
`lerobot-teleoperate` at the PTY paths it prints:

```sh
gantry-splitter \
    --leader   /dev/tty.usbmodem5B7B0185921 \
    --follower /dev/tty.usbmodem5B610373301 \
    --calibration-dir ~/.cache/huggingface/lerobot/calibration/robots/so_follower \
    --leader-id  my_awesome_leader_arm  --follower-id my_awesome_follower_arm \
    [--endpoint http://localhost:4780] [--token gtk_...]
# prints, e.g.:
#   leader  -> /dev/ttys012   (point --teleop.port here; device=so101-leader)
#   follower-> /dev/ttys013   (point --robot.port here;  device=so101-follower)
```

```sh
lerobot-teleoperate \
    --teleop.type=so101_leader   --teleop.port=/dev/ttys012 --teleop.id=my_awesome_leader_arm \
    --robot.type=so101_follower  --robot.port=/dev/ttys013  --robot.id=my_awesome_follower_arm
```

A single arm works too — pass just `--leader` or just `--follower`.

### Channels

Devices `so101-leader` / `so101-follower`, one packet per joint (`shoulder_pan`
… `gripper`), same as the Python bridge:

- **`pos`** — measured `Present_Position` per joint (both arms).
- **`cmd`** — commanded `Goal_Position` per joint (the follower gets these from
  teleop's `sync_write`).
- **`track_err`** on each follower joint when both arms run: leader `pos` −
  follower `pos`, against the latest leader snapshot (same units as `pos`).

Registration retries until the bench is up, batches flush ~every 100 ms, and the
per-device sequence advances on dropped batches (honest gaps) — same contract as
the other kit tools.

### Calibration

Sniffed values are raw Feetech counts. With `--calibration-dir` + `--*-id` the
splitter loads lerobot's own per-motor calibration JSON (`<id>.json`, the
`MotorCalibration` schema) and applies the **same** normalization lerobot does,
so splitter output matches the lerobot-native paths: body joints in **degrees**,
the gripper in **0–100 %**. The lerobot default layout puts these under
`~/.cache/huggingface/lerobot/calibration/robots/so_follower/<id>.json` (and
`.../teleoperators/so_leader/<id>.json` for leaders) — pass the directory that
contains your `<id>.json`.

- `homing_offset` is applied by the servo in hardware
  (`Present_Position = Actual_Position − Homing_Offset`), so the splitter does
  **not** re-apply it — the raw value already carries it.
- Positions are sign-magnitude decoded (bit 15) before normalization, exactly as
  lerobot does.
- `--no-degrees` publishes body joints as −100…100 (matches lerobot
  `use_degrees=false`); the default is degrees.
- `--raw` (or omitting `--calibration-dir`) publishes uncalibrated raw-center
  degrees `((raw & 0xFFF) − 2048)·360/4096`, mirroring `so101_bridge.py`'s raw
  backend — self-consistent but not zeroed to your calibration.

### Caveats

- **macOS/Linux only.** PTYs are POSIX. On Windows the binary prints a message
  and exits 2 (the crate still compiles there); use `so101_bridge.py` /
  `so101_teleop_gantry.py` instead.
- The real ports are opened at **1 Mbaud** (the SO-101 bus speed); PTYs have no
  baud, so lerobot's `--*.port` baud setting is a harmless no-op.
- The splitter tolerates `lerobot-teleoperate` opening/closing the PTY
  repeatedly. A **real-device unplug** exits with a clear message — wrap it in
  `systemd`/a restart loop if you want auto-recovery (v1 leaves that to you).
- First hardware contact: confirm the printed PTY paths, that
  `lerobot-teleoperate` moves the follower normally (the splitter is passive and
  never injects bytes), and that `pos`/`cmd`/`track_err` appear on the bench.

## Tests

Pure-part coverage, no hardware and no network:

```sh
python -m pytest examples/so101 -q
```

Covers the Feetech packet codec (encode/checksum against byte fixtures, status
parse incl. corrupt-checksum + resync), raw→deg math, sync-read degrade,
publisher batching + sequence-on-drop (mocked `urlopen`), tracking-error
pairing, channel-spec shape, port persistence round-trip, and the auto-detect /
pairing / no-adapters paths (injected I/O).

The Rust splitter has its own suite in the SDK workspace (`cargo test -p
gantry-splitter`): decoder attribution/resync/checksum from byte fixtures,
calibration loading + lerobot-matching normalization, and — on macOS/Linux — a
full PTY loopback that asserts byte-identical passthrough, decoded frames on a
mock transport, and sub-millisecond pump latency. The unix-only tests skip
cleanly on Windows.

## Notes

- Raw-backend positions have no per-servo calibration offset — zero your arms
  mechanically, or run the lerobot backend for calibrated degrees.
- The raw Feetech impl is deliberately minimal (pyserial only). If a servo's
  firmware misbehaves on broadcast sync-read, the bridge silently falls back to
  per-servo reads (slower but correct).
- Register addresses and the lerobot device API (`bus.sync_read("Present_*",
  normalize=…)`, `SOFollower`/`SOLeader`, `connect(calibrate=True)`,
  `"<joint>.pos"` keys) are verified against huggingface/lerobot `main`.
```
