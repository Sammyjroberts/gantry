//! The pump: a [`Read`] byte source → the wire [`Decoder`] → the [`Translator`].
//!
//! One thread, no async runtime. The loop reads a chunk, feeds it to the incremental decoder
//! (which delivers whole records to the translator), refreshes the decoder counters, and flushes
//! the translator on a timer (~100 ms) so batches ship promptly even when the device is quiet.
//! A serial read that times out simply yields no bytes and the timer still fires. On EOF (a spool
//! file's end) the loop makes a final flush and returns.

use std::io::{self, Read};
use std::time::{Duration, Instant};

use gantry_wire::{Decoder, Record};

use crate::translate::{DecoderCounters, Translator};
use gantry_connect::Transport;

/// Replay pacing for file sources.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Pace {
    /// Shove bytes as fast as they can be read (the default, and the only mode for live serial).
    Max,
    /// Pace replay by the device's own `TimeSync` deltas, approximating real time. Coarse: it
    /// sleeps at `TimeSync` granularity (~1 s cadence), not per-sample.
    Live,
}

/// Pipeline tuning.
#[derive(Debug, Clone, Copy)]
pub struct PipelineConfig {
    /// Read buffer size. Small for serial (keeps the flush timer responsive); large for files.
    pub read_chunk: usize,
    /// How often to flush queued frames regardless of batch fill.
    pub flush_interval: Duration,
    /// Replay pacing (ignored for live serial, which is always [`Pace::Max`]-like).
    pub pace: Pace,
}

impl Default for PipelineConfig {
    fn default() -> Self {
        Self {
            read_chunk: 4096,
            flush_interval: Duration::from_millis(100),
            pace: Pace::Max,
        }
    }
}

/// Snapshot the decoder's counters for forwarding as channels.
pub fn decoder_counters(dec: &Decoder) -> DecoderCounters {
    DecoderCounters {
        crc_failures: dec.crc_failures(),
        malformed: dec.malformed(),
        dropped_samples: dec.dropped_samples(),
        unknown_records: dec.unknown_records(),
    }
}

/// Pace file replay by `TimeSync` deltas (only when [`Pace::Live`]).
struct Pacer {
    enabled: bool,
    tick_hz: u64,
    last_ticks: Option<u64>,
}

impl Pacer {
    fn new(enabled: bool) -> Self {
        Self {
            enabled,
            tick_hz: 0,
            last_ticks: None,
        }
    }

    fn observe(&mut self, record: &Record) {
        match record {
            Record::DeviceInfo { tick_hz, .. } => self.tick_hz = *tick_hz,
            Record::TimeSync { ticks, .. } if self.enabled => {
                if let Some(prev) = self.last_ticks {
                    if *ticks > prev && self.tick_hz > 0 {
                        let dt_ns =
                            (*ticks - prev) as u128 * 1_000_000_000u128 / self.tick_hz as u128;
                        std::thread::sleep(
                            Duration::from_nanos(dt_ns.min(u64::MAX as u128) as u64),
                        );
                    }
                }
                self.last_ticks = Some(*ticks);
            }
            _ => {}
        }
    }
}

/// Run the pump until the source reaches EOF (files) or errors (serial). `TimedOut`/`WouldBlock`
/// reads are treated as idle ticks, not errors, so the flush timer keeps running.
pub fn run<R: Read, T: Transport>(
    mut source: R,
    decoder: &mut Decoder,
    translator: &mut Translator<T>,
    cfg: PipelineConfig,
) -> io::Result<()> {
    let mut buf = vec![0u8; cfg.read_chunk.max(1)];
    let mut last_flush = Instant::now();
    let mut pacer = Pacer::new(cfg.pace == Pace::Live);

    loop {
        match source.read(&mut buf) {
            Ok(0) => break, // EOF
            Ok(n) => {
                decoder.push(&buf[..n], |rec| {
                    pacer.observe(&rec);
                    translator.handle(rec);
                });
                translator.set_decoder_counters(decoder_counters(decoder));
            }
            Err(e)
                if e.kind() == io::ErrorKind::TimedOut || e.kind() == io::ErrorKind::WouldBlock =>
            {
                // Idle: no bytes this interval. Fall through to the flush check.
            }
            Err(e) => return Err(e),
        }

        if last_flush.elapsed() >= cfg.flush_interval {
            translator.flush();
            last_flush = Instant::now();
        }
    }

    translator.set_decoder_counters(decoder_counters(decoder));
    translator.flush();
    Ok(())
}
