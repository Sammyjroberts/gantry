//! The [`Client`]: builder, enqueue API, and the background flusher thread.
//!
//! This is the std-only part of the SDK (threads + clocks). See the crate docs for the
//! `no_std` roadmap: the flusher here would be replaced by a caller-driven `poll`/`drain`,
//! while [`crate::model`]/[`crate::batch`]/[`crate::buffer`] are reused unchanged.

use std::sync::{Arc, Condvar, Mutex};
use std::thread::JoinHandle;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use gantry_connect_proto::v1::{ChannelInfo, Value};

use crate::batch;
use crate::buffer::Buffer;
use crate::error::{BuildError, FlushError};
use crate::model::{self, ChannelSpec};
use crate::transport::{Transport, TransportError};

/// Retry/backoff policy for transient transport failures.
///
/// The flusher retries a failed [`Transport::publish`] up to `max_retries` times, sleeping
/// with capped exponential backoff (`initial_backoff`, doubling, clamped to `max_backoff`).
/// After the cap it **drops the batch** — telemetry, not transactions — and moves on so the
/// flusher stays live and the [`Buffer`]'s drop-oldest policy governs memory.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RetryConfig {
    /// Number of retries after the first failed attempt.
    pub max_retries: u32,
    /// Backoff before the first retry.
    pub initial_backoff: Duration,
    /// Upper bound on the backoff between retries.
    pub max_backoff: Duration,
}

impl Default for RetryConfig {
    fn default() -> Self {
        Self {
            max_retries: 5,
            initial_backoff: Duration::from_millis(50),
            max_backoff: Duration::from_secs(5),
        }
    }
}

/// A snapshot of client counters.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ClientStats {
    /// Frames accepted into the buffer over the client's lifetime.
    pub frames_enqueued: u64,
    /// Frames successfully published (acked by a transport).
    pub frames_sent: u64,
    /// Frames evicted by the buffer's drop-oldest policy.
    pub frames_dropped: u64,
    /// Frames currently sitting in the buffer.
    pub frames_buffered: u64,
    /// Batches successfully published.
    pub batches_sent: u64,
    /// Batches given up on after exhausting retries.
    pub batches_dropped: u64,
    /// Highest per-device sequence acked by the transport.
    pub last_acked_sequence: u64,
}

// Mutable state guarded by `Inner::state`.
struct State {
    buf: Buffer,
    // When the currently-buffered run of frames started (drives the max-age flush).
    first_at: Option<Instant>,
    shutdown: bool,
    // flush() bumps `flush_req`; the flusher raises `flush_ack` to match once the buffer
    // has drained to empty. flush() waits for flush_ack >= its captured target.
    flush_req: u64,
    flush_ack: u64,
    // stats
    frames_enqueued: u64,
    frames_sent: u64,
    batches_sent: u64,
    batches_dropped: u64,
    last_acked_sequence: u64,
}

struct Inner {
    state: Mutex<State>,
    data_cv: Condvar,     // wakes the flusher (new data / flush / shutdown)
    progress_cv: Condvar, // wakes flush() waiters when flush_ack advances
    device_id: String,
    batch_max_frames: usize,
    batch_max_age: Duration,
    retry: RetryConfig,
    transport: Arc<dyn Transport>,
}

/// A telemetry client: register channels, push samples, and let the background flusher
/// batch and ship them. Cheap to construct one per device/process.
pub struct Client {
    inner: Arc<Inner>,
    handle: Mutex<Option<JoinHandle<()>>>,
}

impl Client {
    /// Start building a client.
    pub fn builder() -> ClientBuilder {
        ClientBuilder::new()
    }

    /// Register (or update) channel metadata for this device. Synchronous: forwards straight
    /// to the transport on the calling thread.
    pub fn register(&self, specs: &[ChannelSpec]) -> Result<(), TransportError> {
        let infos: Vec<ChannelInfo> = specs.iter().map(ChannelSpec::to_channel_info).collect();
        self.inner.transport.register(&self.inner.device_id, &infos)
    }

    /// Enqueue an `f64` sample, timestamped now.
    pub fn send_f64(&self, channel: &str, value: f64) {
        self.enqueue(channel, model::value_f64(value), now_ns());
    }
    /// Enqueue an `f64` sample with an explicit hardware timestamp (ns since Unix epoch).
    pub fn send_f64_at(&self, channel: &str, value: f64, timestamp_ns: u64) {
        self.enqueue(channel, model::value_f64(value), timestamp_ns);
    }

    /// Enqueue an `i64` sample, timestamped now.
    pub fn send_i64(&self, channel: &str, value: i64) {
        self.enqueue(channel, model::value_i64(value), now_ns());
    }
    /// Enqueue an `i64` sample with an explicit hardware timestamp.
    pub fn send_i64_at(&self, channel: &str, value: i64, timestamp_ns: u64) {
        self.enqueue(channel, model::value_i64(value), timestamp_ns);
    }

    /// Enqueue a boolean sample, timestamped now.
    pub fn send_bool(&self, channel: &str, value: bool) {
        self.enqueue(channel, model::value_bool(value), now_ns());
    }
    /// Enqueue a boolean sample with an explicit hardware timestamp.
    pub fn send_bool_at(&self, channel: &str, value: bool, timestamp_ns: u64) {
        self.enqueue(channel, model::value_bool(value), timestamp_ns);
    }

    /// Enqueue a text sample, timestamped now.
    pub fn send_text(&self, channel: &str, value: impl Into<String>) {
        self.enqueue(channel, model::value_text(value), now_ns());
    }
    /// Enqueue a text sample with an explicit hardware timestamp.
    pub fn send_text_at(&self, channel: &str, value: impl Into<String>, timestamp_ns: u64) {
        self.enqueue(channel, model::value_text(value), timestamp_ns);
    }

    /// Enqueue a raw-bytes sample, timestamped now.
    pub fn send_raw(&self, channel: &str, value: impl Into<Vec<u8>>) {
        self.enqueue(channel, model::value_raw(value), now_ns());
    }
    /// Enqueue a raw-bytes sample with an explicit hardware timestamp.
    pub fn send_raw_at(&self, channel: &str, value: impl Into<Vec<u8>>, timestamp_ns: u64) {
        self.enqueue(channel, model::value_raw(value), timestamp_ns);
    }

    /// Enqueue a pre-built [`Value`] with an explicit timestamp (escape hatch).
    pub fn send_value_at(&self, channel: &str, value: Value, timestamp_ns: u64) {
        self.enqueue(channel, value, timestamp_ns);
    }

    fn enqueue(&self, channel: &str, value: Value, timestamp_ns: u64) {
        let mut st = match self.inner.state.lock() {
            Ok(g) => g,
            Err(_) => return, // flusher panicked; nothing we can do, drop the sample
        };
        if st.shutdown {
            return;
        }
        let frame = batch::frame(channel, timestamp_ns, value);
        st.buf.push(frame);
        st.frames_enqueued += 1;
        if st.first_at.is_none() {
            st.first_at = Some(Instant::now());
        }
        if st.buf.len() >= self.inner.batch_max_frames {
            self.inner.data_cv.notify_one();
        }
    }

    /// Block until every frame enqueued before this call has been published (or dropped).
    pub fn flush(&self) -> Result<(), FlushError> {
        let target = {
            let mut st = self.inner.state.lock().map_err(|_| FlushError)?;
            if st.shutdown {
                return Ok(());
            }
            st.flush_req += 1;
            let t = st.flush_req;
            self.inner.data_cv.notify_all();
            t
        };
        let mut st = self.inner.state.lock().map_err(|_| FlushError)?;
        while st.flush_ack < target && !st.shutdown {
            st = self.inner.progress_cv.wait(st).map_err(|_| FlushError)?;
        }
        Ok(())
    }

    /// Snapshot the client's counters.
    pub fn stats(&self) -> ClientStats {
        let st = self.inner.state.lock().expect("state mutex poisoned");
        ClientStats {
            frames_enqueued: st.frames_enqueued,
            frames_sent: st.frames_sent,
            frames_dropped: st.buf.dropped(),
            frames_buffered: st.buf.len() as u64,
            batches_sent: st.batches_sent,
            batches_dropped: st.batches_dropped,
            last_acked_sequence: st.last_acked_sequence,
        }
    }

    /// Flush pending frames, stop the flusher, and join its thread. Idempotent.
    pub fn shutdown(&self) {
        let handle = {
            let mut st = match self.inner.state.lock() {
                Ok(g) => g,
                Err(p) => p.into_inner(),
            };
            if st.shutdown {
                None
            } else {
                st.shutdown = true;
                self.inner.data_cv.notify_all();
                self.handle.lock().ok().and_then(|mut h| h.take())
            }
        };
        if let Some(h) = handle {
            let _ = h.join();
        }
    }
}

impl Drop for Client {
    fn drop(&mut self) {
        self.shutdown();
    }
}

/// Builder for [`Client`].
pub struct ClientBuilder {
    device_id: Option<String>,
    transport: Option<Arc<dyn Transport>>,
    batch_max_frames: usize,
    batch_max_age: Duration,
    buffer_capacity: usize,
    retry: RetryConfig,
}

impl ClientBuilder {
    fn new() -> Self {
        Self {
            device_id: None,
            transport: None,
            batch_max_frames: 500,
            batch_max_age: Duration::from_millis(100),
            buffer_capacity: 10_000,
            retry: RetryConfig::default(),
        }
    }

    /// Stable identifier of the emitting device/process (required).
    pub fn device_id(mut self, id: impl Into<String>) -> Self {
        self.device_id = Some(id.into());
        self
    }

    /// The transport to ship batches over (required).
    pub fn transport<T: Transport + 'static>(mut self, transport: T) -> Self {
        self.transport = Some(Arc::new(transport));
        self
    }

    /// Flush once a batch reaches this many frames. Default 500.
    pub fn batch_max_frames(mut self, n: usize) -> Self {
        self.batch_max_frames = n;
        self
    }

    /// Flush once the oldest buffered frame reaches this age. Default 100ms.
    pub fn batch_max_age(mut self, age: Duration) -> Self {
        self.batch_max_age = age;
        self
    }

    /// Maximum frames held in memory before drop-oldest kicks in. Default 10_000.
    pub fn buffer_capacity(mut self, n: usize) -> Self {
        self.buffer_capacity = n;
        self
    }

    /// Retry/backoff policy for transient transport errors.
    pub fn retry(mut self, retry: RetryConfig) -> Self {
        self.retry = retry;
        self
    }

    /// Build the client and start its background flusher thread.
    pub fn build(self) -> Result<Client, BuildError> {
        let device_id = self
            .device_id
            .filter(|s| !s.is_empty())
            .ok_or(BuildError::MissingDeviceId)?;
        let transport = self.transport.ok_or(BuildError::MissingTransport)?;
        if self.batch_max_frames == 0 {
            return Err(BuildError::InvalidConfig("batch_max_frames must be > 0"));
        }
        if self.buffer_capacity == 0 {
            return Err(BuildError::InvalidConfig("buffer_capacity must be > 0"));
        }

        let inner = Arc::new(Inner {
            state: Mutex::new(State {
                buf: Buffer::new(self.buffer_capacity),
                first_at: None,
                shutdown: false,
                flush_req: 0,
                flush_ack: 0,
                frames_enqueued: 0,
                frames_sent: 0,
                batches_sent: 0,
                batches_dropped: 0,
                last_acked_sequence: 0,
            }),
            data_cv: Condvar::new(),
            progress_cv: Condvar::new(),
            device_id,
            batch_max_frames: self.batch_max_frames,
            batch_max_age: self.batch_max_age,
            retry: self.retry,
            transport,
        });

        let flusher_inner = Arc::clone(&inner);
        let handle = std::thread::Builder::new()
            .name("gantry-connect-flusher".into())
            .spawn(move || run_flusher(flusher_inner))
            .expect("failed to spawn flusher thread");

        Ok(Client {
            inner,
            handle: Mutex::new(Some(handle)),
        })
    }
}

/// The background flusher loop. Exits when `shutdown` is set and the buffer is empty.
fn run_flusher(inner: Arc<Inner>) {
    let mut sequence: u64 = 1;

    loop {
        // Phase 1: under the lock, obtain a batch of frames to send, or exit.
        let frames = {
            let mut st = match inner.state.lock() {
                Ok(g) => g,
                Err(_) => return, // poisoned; bail
            };
            loop {
                if st.buf.is_empty() {
                    // Nothing to send: satisfy any outstanding flush request.
                    if st.flush_req > st.flush_ack {
                        st.flush_ack = st.flush_req;
                        inner.progress_cv.notify_all();
                    }
                    if st.shutdown {
                        return;
                    }
                    st = match inner.data_cv.wait(st) {
                        Ok(g) => g,
                        Err(_) => return,
                    };
                    continue;
                }

                let now = Instant::now();
                let age = st.first_at.map(|t| now.saturating_duration_since(t));
                let age_ready = age.is_some_and(|a| a >= inner.batch_max_age);
                let ready = st.buf.len() >= inner.batch_max_frames
                    || age_ready
                    || st.flush_req > st.flush_ack
                    || st.shutdown;

                if ready {
                    let frames = st.buf.drain(inner.batch_max_frames);
                    st.first_at = if st.buf.is_empty() {
                        None
                    } else {
                        // Remaining frames start a fresh age window.
                        Some(Instant::now())
                    };
                    break frames;
                }

                // Not ready: sleep until the age deadline (or until notified sooner).
                let wait = inner.batch_max_age.saturating_sub(age.unwrap_or_default());
                st = match inner.data_cv.wait_timeout(st, wait) {
                    Ok((g, _)) => g,
                    Err(_) => return,
                };
            }
        };

        // Phase 2: publish outside the lock so producers never block on the network.
        let n = frames.len() as u64;
        let batch = batch::batch(inner.device_id.clone(), sequence, frames);
        match publish_with_retry(&*inner.transport, &batch, &inner.retry) {
            Ok(acked) => {
                if let Ok(mut st) = inner.state.lock() {
                    st.frames_sent += n;
                    st.batches_sent += 1;
                    if acked > st.last_acked_sequence {
                        st.last_acked_sequence = acked;
                    }
                }
            }
            Err(()) => {
                if let Ok(mut st) = inner.state.lock() {
                    st.batches_dropped += 1;
                }
            }
        }
        // Sequence is per-batch monotonic regardless of ack, so the server can detect gaps.
        sequence += 1;
    }
}

/// Publish with capped exponential backoff. `Err(())` means "gave up, drop the batch".
fn publish_with_retry(
    transport: &dyn Transport,
    batch: &gantry_connect_proto::v1::FrameBatch,
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

fn now_ns() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0)
}
