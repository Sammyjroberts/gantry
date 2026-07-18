//! # gantry-splitter — a passive serial-sniffing proxy for the SO-101
//!
//! Sit transparently between `lerobot-teleoperate` and the SO-101 Feetech bus. For each port
//! (leader and/or follower) the daemon opens the **real** serial device, creates a **PTY** pair,
//! and pumps bytes between the two with minimum latency. lerobot runs *unmodified* — pointed at the
//! PTY slave paths — while the splitter decodes the traffic it forwards and publishes per-joint
//! telemetry (`pos`, `cmd`, `track_err`) to a Gantry bench.
//!
//! ## Layout
//! * [`decoder`] — the stateful, passive Feetech decoder (half-duplex read attribution). Pure.
//! * [`calibration`] — load lerobot's per-motor calibration JSON and normalize raw counts to match
//!   lerobot's own output (degrees / 0–100 %). Pure.
//! * [`publish`] — one [`publish::Publisher`] per device: batch, register, retry, sequence.
//! * [`sink`] — wire readings → normalize → publish, and compute `track_err`.
//! * [`pump`] — the generic read→forward→tee-decode loop (drives serial in prod, a PTY in tests).
//! * `pty` / `daemon` — POSIX-only PTY creation and the serial↔PTY wiring (`cfg(unix)`).
//!
//! Everything except `pty`/`daemon` is platform-independent and compiles + tests on the Windows dev
//! box; the daemon is `cfg(unix)` and the binary prints a clear message and exits 2 elsewhere.

pub mod calibration;
pub mod decoder;
pub mod publish;
pub mod pump;
pub mod sink;

#[cfg(unix)]
pub mod pty;

#[cfg(unix)]
pub mod daemon;
