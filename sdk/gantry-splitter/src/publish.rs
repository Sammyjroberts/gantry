//! Publishing to a Gantry bench.
//!
//! One [`Publisher`] per device (`so101-leader` / `so101-follower`). It reuses the `gantry-edge`
//! wire types and `Transport` trait but *not* `gantry_edge::Client`: the client's `send_*` API
//! cannot set `Frame.packet`, and here `packet = joint` is first-class. So we assemble
//! [`FrameBatch`]es field-by-field, exactly like `gantry-serial-agent`'s translator.
//!
//! Behavior (matching the established `so101_bridge.py` / serial-agent pattern):
//! * a background thread flushes every ~100 ms;
//! * channels are (re)registered until the bench acks — start order doesn't matter;
//! * the per-device `sequence` advances on every batch, **including drops**, so the server sees
//!   honest gaps;
//! * the outgoing buffer is bounded and drop-oldest (telemetry favors fresh data).

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Condvar, Mutex};
use std::thread::JoinHandle;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use gantry_edge::model::value_f64;
use gantry_edge::{ChannelInfo, Frame, FrameBatch, RetryConfig, Transport};

/// Flush cadence.
const FLUSH_INTERVAL: Duration = Duration::from_millis(100);
/// Bound on the outgoing frame buffer before drop-oldest kicks in.
const BUFFER_CAPACITY: usize = 100_000;

struct State {
    frames: Vec<Frame>,
    sequence: u64,
    registered: bool,
    dropped_buffer: u64,
    sent: u64,
    dropped_transport: u64,
    shutdown: bool,
}

struct Inner {
    device_id: String,
    channels: Vec<ChannelInfo>,
    retry: RetryConfig,
    transport: Arc<dyn Transport>,
    state: Mutex<State>,
    wake: Condvar,
}

/// A per-device telemetry publisher with its own background flusher.
pub struct Publisher {
    inner: Arc<Inner>,
    handle: Mutex<Option<JoinHandle<()>>>,
    stop: Arc<AtomicBool>,
}

impl Publisher {
    /// Start a publisher for `device_id`, registering `channels` (retried until the bench is up).
    pub fn start<T: Transport + 'static>(
        transport: T,
        device_id: impl Into<String>,
        channels: Vec<ChannelInfo>,
    ) -> Arc<Self> {
        Self::start_arc(Arc::new(transport), device_id, channels)
    }

    /// As [`start`](Self::start) but taking an already-shared transport (multiple devices can share
    /// one HTTP transport).
    pub fn start_arc(
        transport: Arc<dyn Transport>,
        device_id: impl Into<String>,
        channels: Vec<ChannelInfo>,
    ) -> Arc<Self> {
        let inner = Arc::new(Inner {
            device_id: device_id.into(),
            channels,
            retry: RetryConfig::default(),
            transport,
            state: Mutex::new(State {
                frames: Vec::new(),
                sequence: 1,
                registered: false,
                dropped_buffer: 0,
                sent: 0,
                dropped_transport: 0,
                shutdown: false,
            }),
            wake: Condvar::new(),
        });
        let stop = Arc::new(AtomicBool::new(false));
        let flusher_inner = Arc::clone(&inner);
        let flusher_stop = Arc::clone(&stop);
        let handle = std::thread::Builder::new()
            .name(format!("gantry-splitter-pub-{}", inner.device_id))
            .spawn(move || run_flusher(flusher_inner, flusher_stop))
            .expect("failed to spawn publisher flusher");
        Arc::new(Self {
            inner,
            handle: Mutex::new(Some(handle)),
            stop,
        })
    }

    /// The device id this publisher owns.
    pub fn device_id(&self) -> &str {
        &self.inner.device_id
    }

    /// Enqueue one sample (`packet`, `channel`, value at `timestamp_ns`).
    pub fn add(&self, packet: &str, channel: &str, value: f64, timestamp_ns: u64) {
        let mut st = match self.inner.state.lock() {
            Ok(g) => g,
            Err(_) => return,
        };
        if st.shutdown {
            return;
        }
        if st.frames.len() >= BUFFER_CAPACITY {
            st.frames.remove(0);
            st.dropped_buffer += 1;
        }
        st.frames.push(Frame {
            channel: channel.to_string(),
            timestamp_ns,
            value: Some(value_f64(value)),
            packet: packet.to_string(),
            device_id: String::new(),
        });
    }

    /// Snapshot of `(sent, dropped_transport, dropped_buffer)` frame counts.
    pub fn counters(&self) -> (u64, u64, u64) {
        let st = self.inner.state.lock().expect("poisoned");
        (st.sent, st.dropped_transport, st.dropped_buffer)
    }

    /// Flush pending frames, stop the flusher, and join it. Idempotent.
    pub fn shutdown(&self) {
        if self.stop.swap(true, Ordering::SeqCst) {
            return;
        }
        {
            let mut st = self.inner.state.lock().expect("poisoned");
            st.shutdown = true;
        }
        // Final synchronous flush so shutdown doesn't drop the tail.
        flush_once(&self.inner);
        self.inner.wake.notify_all();
        if let Some(h) = self.handle.lock().ok().and_then(|mut h| h.take()) {
            let _ = h.join();
        }
    }
}

impl Drop for Publisher {
    fn drop(&mut self) {
        self.shutdown();
    }
}

fn run_flusher(inner: Arc<Inner>, stop: Arc<AtomicBool>) {
    // Register up front, then keep flushing on a timer. Registration is retried inside flush_once
    // until it lands.
    try_register(&inner);
    let mut guard = inner.state.lock().expect("poisoned");
    while !stop.load(Ordering::SeqCst) {
        let (g, _timeout) = inner
            .wake
            .wait_timeout(guard, FLUSH_INTERVAL)
            .expect("poisoned");
        guard = g;
        if guard.shutdown || stop.load(Ordering::SeqCst) {
            break;
        }
        drop(guard);
        flush_once(&inner);
        guard = inner.state.lock().expect("poisoned");
    }
}

/// Drain the buffer and publish one batch (registering first if not yet acked).
fn flush_once(inner: &Arc<Inner>) {
    if !inner.state.lock().map(|s| s.registered).unwrap_or(true) {
        try_register(inner);
    }
    let (frames, sequence) = {
        let mut st = match inner.state.lock() {
            Ok(g) => g,
            Err(_) => return,
        };
        if st.frames.is_empty() {
            return;
        }
        let frames = std::mem::take(&mut st.frames);
        let sequence = st.sequence;
        st.sequence += 1; // advances even if the publish below drops — honest gaps.
        (frames, sequence)
    };

    let n = frames.len() as u64;
    let batch = FrameBatch {
        device_id: inner.device_id.clone(),
        sequence,
        frames,
        received_ns: 0,
    };
    match publish_with_retry(&*inner.transport, &batch, &inner.retry) {
        Ok(_) => {
            if let Ok(mut st) = inner.state.lock() {
                st.sent += n;
                if !st.registered {
                    // A successful publish proves the bench is up; make sure metadata is there.
                    drop(st);
                    try_register(inner);
                }
            }
        }
        Err(()) => {
            if let Ok(mut st) = inner.state.lock() {
                st.dropped_transport += n;
            }
        }
    }
}

fn try_register(inner: &Arc<Inner>) {
    if inner
        .transport
        .register(&inner.device_id, &inner.channels)
        .is_ok()
    {
        if let Ok(mut st) = inner.state.lock() {
            st.registered = true;
        }
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

/// Epoch nanoseconds now.
pub fn now_ns() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0)
}
