//! Translation core: `docs/WIRE.md` [`Record`]s → `gantry.v1` ingest calls.
//!
//! This is the heart of the agent and is deliberately free of serial and HTTP: it takes decoded
//! [`Record`]s on one side and a [`Transport`] on the other, so it is unit-testable with a mock
//! transport and a fake clock. The byte-source and endpoint wiring lives in
//! [`crate::source`] / [`crate::pipeline`].
//!
//! ## What it does
//!
//! * **`DeviceInfo`** establishes device identity (`device_id`, `session`, `tick_hz`). A *session
//!   change* means a device reboot: the packet table is reset and channels re-register.
//! * **`PacketDef`** becomes a `RegisterChannels` call — one [`ChannelInfo`] per field
//!   (`packet` = packet name, `name` = field name, mapped `kind`, `unit`). Re-defs are
//!   idempotent: we re-register only when the channel set actually changes.
//! * **`Samples`** becomes one [`Frame`] per field (`channel` = field name, `packet` = packet
//!   name, mapped `value`), timestamped by the [`TimeMapper`] and batched into a [`FrameBatch`]
//!   with a monotonic agent-side `sequence`, flushed by frame count or by the pipeline's timer.
//! * **`TimeSync`** feeds the [`TimeMapper`]. Before the first sync no epoch offset exists, so
//!   samples are held in a bounded pre-sync buffer and released (correctly stamped) once it lands.
//! * **`Stats`** (and the agent's own decoder/queue counters) are forwarded as ordinary channels
//!   under the `gantry` packet, so link health shows up in the console like any other telemetry.
//! * **`Unknown`** records are counted (the decoder already skips them).
//!
//! ## Queue / retry
//!
//! We drive the [`Transport`] directly with our own bounded, drop-oldest [`Buffer`] (reused from
//! `gantry-connect`) rather than going through `gantry_connect::Client`. The `Client`'s public
//! `send_*` API cannot set `Frame.packet`, and packets are first-class here — so a batch must be
//! assembled field-by-field. Publish uses capped exponential backoff ([`RetryConfig`]); a batch
//! that exhausts retries (or hits a permanent error) is dropped and counted — telemetry, not
//! transactions.

use std::collections::{BTreeMap, VecDeque};

use gantry_connect::buffer::Buffer;
use gantry_connect::model::{value_bool, value_f64, value_i64, value_raw, value_text};
use gantry_connect::{
    ChannelInfo, Frame, FrameBatch, RetryConfig, Transport, TransportError, ValueKind,
};
use gantry_wire::{Kind, Record, Value as WireValue};

use crate::timesync::TimeMapper;

/// The `packet` name under which the agent publishes link-health / self-telemetry channels.
pub const STATS_PACKET: &str = "gantry";

/// A host wall-clock source (epoch nanoseconds). Injected so tests can drive it deterministically.
pub type HostClock = Box<dyn FnMut() -> i128 + Send>;

/// How the [`TimeMapper`] establishes its epoch offset.
#[derive(Debug, Clone, Copy)]
pub enum TimeAnchor {
    /// Live/serial: derive the offset from the host receipt time of every `TimeSync`
    /// (minimum-latency filter over a rolling window).
    Live,
    /// File replay: pin the offset at the *first* `TimeSync` to this epoch-ns anchor (file mtime
    /// or `--anchor`) and hold it fixed, so device ticks alone drive timestamps.
    Fixed(i128),
}

/// Tuning for the translator.
#[derive(Debug, Clone)]
pub struct Config {
    /// Flush a batch once this many frames are queued (the pipeline also flushes on a timer).
    pub batch_max_frames: usize,
    /// Max samples held before the first `TimeSync` (drop-oldest beyond this).
    pub presync_capacity: usize,
    /// Bound on the outgoing frame buffer (drop-oldest beyond this).
    pub buffer_capacity: usize,
    /// Retry/backoff policy for transient publish failures.
    pub retry: RetryConfig,
    /// How to anchor device ticks to epoch time.
    pub anchor: TimeAnchor,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            batch_max_frames: 500,
            presync_capacity: 10_000,
            buffer_capacity: 100_000,
            retry: RetryConfig::default(),
            anchor: TimeAnchor::Live,
        }
    }
}

/// Decoder-side counters, snapshotted by the pipeline so they can be forwarded as channels.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct DecoderCounters {
    pub crc_failures: u64,
    pub malformed: u64,
    pub dropped_samples: u64,
    pub unknown_records: u64,
}

/// Agent-side counters (queue/translation health).
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct AgentCounters {
    /// Frames produced from `Samples` records.
    pub frames_translated: u64,
    /// Frames successfully published (acked by the transport).
    pub frames_sent: u64,
    /// Frames dropped because a batch exhausted retries / hit a permanent error.
    pub frames_dropped_transport: u64,
    /// Samples dropped from the pre-sync buffer before the first `TimeSync`.
    pub presync_dropped: u64,
    /// `Samples` whose `packet_id` had no known layout (should be ~0; decoder drops these first).
    pub orphan_frames: u64,
    /// `RegisterChannels` calls that failed (will be retried on the next periodic `PacketDef`).
    pub register_failures: u64,
    /// Batches successfully published.
    pub batches_sent: u64,
    /// Batches given up on after exhausting retries.
    pub batches_dropped: u64,
    /// Highest per-device batch sequence acked by the transport.
    pub last_acked_sequence: u64,
}

/// A learned packet layout (needed to name each `Samples` field and its packet).
#[derive(Debug, Clone)]
struct Packet {
    name: String,
    fields: Vec<gantry_wire::Field>,
}

/// A frame built before the first `TimeSync`, awaiting an epoch offset.
struct Presync {
    frame: Frame,
    ticks: u64,
}

/// Translates decoded wire records into ingest calls over a [`Transport`].
pub struct Translator<T: Transport> {
    sink: T,
    clock: HostClock,
    time: TimeMapper,
    anchor: TimeAnchor,

    device_id: Option<String>,
    session: Option<u32>,
    tick_hz: u64,

    /// packet_id → learned layout.
    packets: BTreeMap<u64, Packet>,
    /// packet_id → last-registered channel set (for idempotent re-registration).
    registered: BTreeMap<u64, Vec<ChannelInfo>>,

    /// Samples awaiting the first `TimeSync`.
    presync: VecDeque<Presync>,
    presync_capacity: usize,

    /// Outgoing frames, bounded + drop-oldest.
    pending: Buffer,
    batch_max_frames: usize,
    sequence: u64,
    retry: RetryConfig,

    counters: AgentCounters,
    dec_counters: DecoderCounters,
    stats_registered: bool,
}

impl<T: Transport> Translator<T> {
    /// Build a translator over `sink`, using `clock` for host-side time and `cfg` for tuning.
    pub fn new(sink: T, clock: HostClock, cfg: Config) -> Self {
        Self {
            sink,
            clock,
            time: TimeMapper::new(1),
            anchor: cfg.anchor,
            device_id: None,
            session: None,
            tick_hz: 0,
            packets: BTreeMap::new(),
            registered: BTreeMap::new(),
            presync: VecDeque::new(),
            presync_capacity: cfg.presync_capacity.max(1),
            pending: Buffer::new(cfg.buffer_capacity.max(1)),
            batch_max_frames: cfg.batch_max_frames.max(1),
            sequence: 1,
            retry: cfg.retry,
            counters: AgentCounters::default(),
            dec_counters: DecoderCounters::default(),
            stats_registered: false,
        }
    }

    /// Snapshot the agent-side counters.
    pub fn counters(&self) -> AgentCounters {
        AgentCounters {
            frames_dropped_transport: self.counters.frames_dropped_transport
                + self.pending.dropped(),
            ..self.counters
        }
    }

    /// Feed the latest decoder counters (called by the pipeline after each `push`).
    pub fn set_decoder_counters(&mut self, c: DecoderCounters) {
        self.dec_counters = c;
    }

    /// The device identity seen so far, if any.
    pub fn device_id(&self) -> Option<&str> {
        self.device_id.as_deref()
    }

    /// Handle one decoded record.
    pub fn handle(&mut self, record: Record) {
        match record {
            Record::DeviceInfo {
                device_id,
                session,
                tick_hz,
            } => self.on_device_info(device_id, session, tick_hz),
            Record::PacketDef {
                packet_id,
                name,
                fields,
            } => self.on_packet_def(packet_id, name, fields),
            Record::Samples {
                packet_id,
                ticks,
                values,
            } => self.on_samples(packet_id, ticks, values),
            Record::TimeSync { ticks, seq } => self.on_time_sync(ticks, seq),
            Record::Stats {
                frames_dropped,
                records_sent,
            } => self.on_stats(frames_dropped, records_sent),
            Record::Unknown { .. } => { /* counted by the decoder; nothing to forward */ }
        }
    }

    fn on_device_info(&mut self, device_id: String, session: u32, tick_hz: u64) {
        let session_changed = self.session != Some(session);
        if session_changed {
            // Reboot: flush what we have under the old identity, then reset all learned state so
            // channels re-register and the time offset is re-estimated for the new session.
            self.flush();
            self.packets.clear();
            self.registered.clear();
            self.presync.clear();
            self.time.set_tick_hz(tick_hz);
            self.tick_hz = tick_hz;
            self.session = Some(session);
        } else if tick_hz != self.tick_hz {
            self.time.set_tick_hz(tick_hz);
            self.tick_hz = tick_hz;
        }
        self.device_id = Some(device_id);
    }

    fn on_packet_def(&mut self, packet_id: u64, name: String, fields: Vec<gantry_wire::Field>) {
        let infos: Vec<ChannelInfo> = fields
            .iter()
            .map(|f| ChannelInfo {
                name: f.name.clone(),
                kind: kind_to_valuekind(f.kind) as i32,
                unit: f.unit.clone(),
                description: String::new(),
                packet: name.clone(),
            })
            .collect();

        // Idempotent: only hit the transport when the channel set is new or changed.
        let changed = self.registered.get(&packet_id) != Some(&infos);
        if changed {
            if let Some(dev) = self.device_id.clone() {
                match self.sink.register(&dev, &infos) {
                    Ok(()) => {
                        self.registered.insert(packet_id, infos);
                    }
                    Err(_) => {
                        // Leave uncached so the next periodic PacketDef retries registration.
                        self.counters.register_failures += 1;
                    }
                }
            }
        }

        self.packets.insert(packet_id, Packet { name, fields });
    }

    fn on_samples(&mut self, packet_id: u64, ticks: u64, values: Vec<WireValue>) {
        let Some(packet) = self.packets.get(&packet_id).cloned() else {
            self.counters.orphan_frames += 1;
            return;
        };
        let mapped = self.time.map(ticks);
        for (field, wire_val) in packet.fields.iter().zip(values.into_iter()) {
            let frame = Frame {
                channel: field.name.clone(),
                timestamp_ns: mapped.unwrap_or(0),
                value: Some(convert_value(wire_val)),
                packet: packet.name.clone(),
                device_id: String::new(),
            };
            match mapped {
                Some(_) => self.enqueue(frame),
                None => self.push_presync(frame, ticks),
            }
        }
    }

    fn on_time_sync(&mut self, ticks: u64, _seq: u64) {
        let was_ready = self.time.is_ready();
        match self.anchor {
            TimeAnchor::Live => {
                let now = (self.clock)();
                self.time.observe(ticks, now);
            }
            TimeAnchor::Fixed(epoch_ns) => {
                if !self.time.is_ready() {
                    self.time.anchor(ticks, epoch_ns);
                }
            }
        }
        if !was_ready && self.time.is_ready() {
            self.drain_presync();
        }
    }

    fn on_stats(&mut self, device_frames_dropped: u64, device_records_sent: u64) {
        self.register_stats_channels();
        let now = (self.clock)().max(0) as u64;
        let c = self.counters();
        let d = self.dec_counters;
        let samples: [(&str, i64); 8] = [
            ("gantry.device.frames_dropped", device_frames_dropped as i64),
            ("gantry.device.records_sent", device_records_sent as i64),
            ("gantry.agent.crc_failures", d.crc_failures as i64),
            ("gantry.agent.malformed_frames", d.malformed as i64),
            ("gantry.agent.orphan_samples", d.dropped_samples as i64),
            ("gantry.agent.unknown_records", d.unknown_records as i64),
            ("gantry.agent.presync_dropped", c.presync_dropped as i64),
            (
                "gantry.agent.frames_dropped",
                c.frames_dropped_transport as i64,
            ),
        ];
        for (name, v) in samples {
            self.enqueue(Frame {
                channel: name.to_string(),
                timestamp_ns: now,
                value: Some(value_i64(v)),
                packet: STATS_PACKET.to_string(),
                device_id: String::new(),
            });
        }
    }

    fn register_stats_channels(&mut self) {
        if self.stats_registered {
            return;
        }
        let Some(dev) = self.device_id.clone() else {
            return;
        };
        let names = [
            "gantry.device.frames_dropped",
            "gantry.device.records_sent",
            "gantry.agent.crc_failures",
            "gantry.agent.malformed_frames",
            "gantry.agent.orphan_samples",
            "gantry.agent.unknown_records",
            "gantry.agent.presync_dropped",
            "gantry.agent.frames_dropped",
        ];
        let infos: Vec<ChannelInfo> = names
            .iter()
            .map(|n| ChannelInfo {
                name: (*n).to_string(),
                kind: ValueKind::I64 as i32,
                unit: "count".to_string(),
                description: "gantry serial-agent link/health counter".to_string(),
                packet: STATS_PACKET.to_string(),
            })
            .collect();
        if self.sink.register(&dev, &infos).is_ok() {
            self.stats_registered = true;
        } else {
            self.counters.register_failures += 1;
        }
    }

    fn push_presync(&mut self, frame: Frame, ticks: u64) {
        if self.presync.len() >= self.presync_capacity {
            self.presync.pop_front();
            self.counters.presync_dropped += 1;
        }
        self.presync.push_back(Presync { frame, ticks });
    }

    fn drain_presync(&mut self) {
        let buffered: Vec<Presync> = self.presync.drain(..).collect();
        for mut p in buffered {
            if let Some(ts) = self.time.map(p.ticks) {
                p.frame.timestamp_ns = ts;
            }
            self.enqueue(p.frame);
        }
    }

    fn enqueue(&mut self, frame: Frame) {
        self.counters.frames_translated += 1;
        self.pending.push(frame);
        if self.pending.len() >= self.batch_max_frames {
            self.flush();
        }
    }

    /// Publish all currently-queued frames (called on the batch threshold, the pipeline's timer,
    /// session change, and at end-of-stream).
    pub fn flush(&mut self) {
        if self.pending.is_empty() {
            return;
        }
        let Some(dev) = self.device_id.clone() else {
            return;
        };
        let frames = self.pending.drain(usize::MAX);
        for chunk in frames.chunks(self.batch_max_frames) {
            let batch = FrameBatch {
                device_id: dev.clone(),
                sequence: self.sequence,
                frames: chunk.to_vec(),
            };
            self.sequence += 1;
            match publish_with_retry(&self.sink, &batch, &self.retry) {
                Ok(acked) => {
                    self.counters.batches_sent += 1;
                    self.counters.frames_sent += chunk.len() as u64;
                    if acked > self.counters.last_acked_sequence {
                        self.counters.last_acked_sequence = acked;
                    }
                }
                Err(()) => {
                    self.counters.batches_dropped += 1;
                    self.counters.frames_dropped_transport += chunk.len() as u64;
                }
            }
        }
    }

    /// Consume the translator, returning the underlying transport (for tests / teardown).
    pub fn into_sink(self) -> T {
        self.sink
    }
}

/// Map a wire [`Kind`] to a proto [`ValueKind`].
pub fn kind_to_valuekind(kind: Kind) -> ValueKind {
    match kind {
        Kind::F32 | Kind::F64 => ValueKind::F64,
        Kind::I32 | Kind::I64 => ValueKind::I64,
        Kind::Bool => ValueKind::Bool,
        Kind::Str => ValueKind::Text,
        Kind::Bytes => ValueKind::Raw,
    }
}

/// Convert a decoded wire [`WireValue`] into a proto `Value`.
fn convert_value(v: WireValue) -> gantry_connect::Value {
    match v {
        WireValue::F32(x) => value_f64(x as f64),
        WireValue::F64(x) => value_f64(x),
        WireValue::I32(x) => value_i64(x as i64),
        WireValue::I64(x) => value_i64(x),
        WireValue::Bool(x) => value_bool(x),
        WireValue::Str(s) => value_text(s),
        WireValue::Bytes(b) => value_raw(b),
    }
}

/// Publish with capped exponential backoff. `Err(())` means "gave up, drop the batch".
fn publish_with_retry(
    transport: &dyn Transport,
    batch: &FrameBatch,
    cfg: &RetryConfig,
) -> Result<u64, ()> {
    let mut attempt = 0u32;
    let mut delay = cfg.initial_backoff;
    loop {
        match transport.publish(batch.clone()) {
            Ok(acked) => return Ok(acked),
            Err(err) => {
                if !err.is_retryable() || attempt >= cfg.max_retries {
                    return Err(());
                }
                attempt += 1;
                std::thread::sleep(delay);
                delay = (delay * 2).min(cfg.max_backoff);
            }
        }
    }
}

/// A permanent (non-retryable) transport error helper for callers/tests.
pub fn permanent_error(msg: &str) -> TransportError {
    TransportError::Other(msg.to_string())
}
