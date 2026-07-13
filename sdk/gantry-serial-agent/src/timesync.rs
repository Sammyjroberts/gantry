//! Device-tick → epoch-nanosecond mapping.
//!
//! A device stamps samples with a monotonic tick counter (`ticks`, at `tick_hz`). It has no
//! wall clock. The collector recovers epoch time from the periodic `TimeSync` records
//! (`docs/WIRE.md` §0x04): each carries the device tick at the moment it was *sent*, and the
//! collector notes its own epoch time at the moment it was *received*. The difference
//!
//! ```text
//! offset = host_receipt_ns − ticks_to_ns(sync_ticks)
//! ```
//!
//! estimates `epoch − device_clock`. But `host_receipt_ns` includes transport latency (USB
//! scheduling, buffering, the OS), so a single offset is biased *high* by however long that
//! particular record took to arrive. Latency is strictly additive and non-negative, so the
//! **smallest** offset in a recent window is the least-delayed — the best estimate of the true
//! offset. We therefore keep a rolling window of the last `N` syncs and use their **minimum**
//! (a minimum-latency filter). The window (rather than an all-time min) lets the estimate track
//! slow clock drift while still rejecting per-record jitter.
//!
//! For a spooled file there is no meaningful "receipt time": the whole recording is replayed at
//! once. Instead we *anchor* — pin the offset from a single known epoch time (the file's mtime,
//! or an explicit `--anchor`) at the first `TimeSync` — and hold it fixed. The device's own
//! ticks then drive every timestamp, preserving intervals exactly (see [`TimeMapper::anchor`]).

use std::collections::VecDeque;

/// Default rolling-window length for the minimum-latency filter (number of `TimeSync`s).
pub const DEFAULT_WINDOW: usize = 16;

/// Nanoseconds per second.
const NANOS_PER_SEC: i128 = 1_000_000_000;

/// Converts device ticks to epoch nanoseconds via `TimeSync` records.
#[derive(Debug)]
pub struct TimeMapper {
    tick_hz: u64,
    window: VecDeque<i128>,
    window_cap: usize,
    /// Cached minimum of `window` (the current offset estimate), or `None` before any sync.
    offset: Option<i128>,
    /// When `true`, the offset is pinned (file-replay anchor) and further syncs are ignored.
    anchored: bool,
}

impl TimeMapper {
    /// A mapper for a `tick_hz` clock using the default rolling window.
    pub fn new(tick_hz: u64) -> Self {
        Self::with_window(tick_hz, DEFAULT_WINDOW)
    }

    /// A mapper with an explicit rolling-window length (minimum 1).
    pub fn with_window(tick_hz: u64, window: usize) -> Self {
        Self {
            tick_hz,
            window: VecDeque::new(),
            window_cap: window.max(1),
            offset: None,
            anchored: false,
        }
    }

    /// Update `tick_hz` (a new session may change it). Clears any accumulated estimate.
    pub fn set_tick_hz(&mut self, tick_hz: u64) {
        self.tick_hz = tick_hz;
        self.reset();
    }

    /// Forget all offset history (called on session reset).
    pub fn reset(&mut self) {
        self.window.clear();
        self.offset = None;
        self.anchored = false;
    }

    /// Whether a usable offset estimate exists yet (i.e. at least one `TimeSync` seen / anchored).
    pub fn is_ready(&self) -> bool {
        self.offset.is_some()
    }

    /// Convert device ticks to epoch nanoseconds, using `tick_hz`. Overflow-safe (`i128` math).
    fn ticks_to_ns(&self, ticks: u64) -> i128 {
        if self.tick_hz == 0 {
            return 0;
        }
        (ticks as i128 * NANOS_PER_SEC) / self.tick_hz as i128
    }

    /// Record a live `TimeSync`: the device tick it carried and the host epoch-ns at receipt.
    /// No-op once anchored (file replay). Recomputes the rolling minimum offset.
    pub fn observe(&mut self, sync_ticks: u64, host_receipt_ns: i128) {
        if self.anchored {
            return;
        }
        let sample = host_receipt_ns - self.ticks_to_ns(sync_ticks);
        if self.window.len() == self.window_cap {
            self.window.pop_front();
        }
        self.window.push_back(sample);
        self.offset = self.window.iter().copied().min();
    }

    /// Pin the offset from a single known epoch time at `sync_ticks` and hold it fixed. Used for
    /// file replay, where `epoch_ns` is the anchor (file mtime or `--anchor`). Subsequent
    /// [`observe`](Self::observe) calls are ignored, so device ticks alone drive all timestamps
    /// and inter-sample intervals are preserved exactly.
    pub fn anchor(&mut self, sync_ticks: u64, epoch_ns: i128) {
        self.offset = Some(epoch_ns - self.ticks_to_ns(sync_ticks));
        self.anchored = true;
    }

    /// Map device ticks to epoch nanoseconds, or `None` if no offset is known yet.
    pub fn map(&self, ticks: u64) -> Option<u64> {
        let offset = self.offset?;
        let ns = self.ticks_to_ns(ticks) + offset;
        Some(ns.max(0) as u64)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const HZ: u64 = 1_000_000; // microsecond ticks

    #[test]
    fn tick_math_is_exact() {
        let mut m = TimeMapper::new(HZ);
        // Anchor: at tick 1_000_000 (= 1s of device time) the epoch is 5s.
        m.anchor(1_000_000, 5 * NANOS_PER_SEC);
        // Offset = 5s − 1s = 4s. Tick 2_000_000 (2s) → 6s.
        assert_eq!(m.map(2_000_000), Some(6 * NANOS_PER_SEC as u64));
        // Tick 0 → 4s.
        assert_eq!(m.map(0), Some(4 * NANOS_PER_SEC as u64));
    }

    #[test]
    fn minimum_latency_filter_picks_least_delayed() {
        let mut m = TimeMapper::new(HZ);
        // True offset is 1_000_000_000 ns (1s). Each sync is device tick = k*HZ (k seconds),
        // received at true_epoch + jitter. The minimum-jitter sample must win.
        let true_offset: i128 = NANOS_PER_SEC;
        // sync at 1s device, 300ms late
        m.observe(HZ, (NANOS_PER_SEC + true_offset) + 300_000_000);
        // sync at 2s device, 5ms late  <-- least delayed
        m.observe(2 * HZ, (2 * NANOS_PER_SEC + true_offset) + 5_000_000);
        // sync at 3s device, 120ms late
        m.observe(3 * HZ, (3 * NANOS_PER_SEC + true_offset) + 120_000_000);
        // Estimated offset ≈ true_offset + 5ms. Map tick 0 → ~offset.
        let mapped = m.map(0).unwrap();
        assert_eq!(mapped, (true_offset + 5_000_000) as u64);
    }

    #[test]
    fn not_ready_before_any_sync() {
        let m = TimeMapper::new(HZ);
        assert!(!m.is_ready());
        assert_eq!(m.map(123), None);
    }

    #[test]
    fn window_slides_to_track_drift() {
        let mut m = TimeMapper::with_window(HZ, 2);
        // Two early samples with a low offset, then the clock "drifts" up and the window slides
        // so the stale low sample no longer pins the estimate.
        m.observe(HZ, NANOS_PER_SEC + 100); // offset 100
        m.observe(2 * HZ, 2 * NANOS_PER_SEC + 100); // offset 100
        assert_eq!(m.map(0), Some(100));
        m.observe(3 * HZ, 3 * NANOS_PER_SEC + 500); // offset 500, evicts first
        m.observe(4 * HZ, 4 * NANOS_PER_SEC + 500); // offset 500, evicts second
        assert_eq!(m.map(0), Some(500));
    }
}
