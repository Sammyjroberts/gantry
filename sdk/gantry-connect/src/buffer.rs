//! A bounded, drop-oldest frame buffer.
//!
//! This is the memory-bounding heart of the SDK. It holds only `alloc`-level state
//! (`VecDeque` + counters) — no locks, no clocks — so it is trivially unit-testable and
//! `no_std`-ready. The [`crate::client`] wraps one of these in a `Mutex`/`Condvar` and owns
//! the timing/threading.
//!
//! ## Drop-oldest policy
//!
//! When the buffer is full and a new frame is pushed, the **oldest** frame is evicted. This
//! is the deliberate policy for telemetry (per `docs/ARCHITECTURE.md`: telemetry, not
//! transactions): when the transport is down we would rather keep the freshest data and
//! shed the stale backlog than block producers or grow without bound. Every eviction is
//! counted so the drop is observable, not silent.

use std::collections::VecDeque;

use gantry_connect_proto::v1::Frame;

/// A bounded FIFO of frames with a drop-oldest overflow policy.
#[derive(Debug)]
pub struct Buffer {
    frames: VecDeque<Frame>,
    capacity: usize,
    dropped: u64,
}

impl Buffer {
    /// Create a buffer holding at most `capacity` frames. `capacity` must be > 0.
    pub fn new(capacity: usize) -> Self {
        debug_assert!(capacity > 0, "buffer capacity must be > 0");
        Self {
            frames: VecDeque::with_capacity(capacity.min(1024)),
            capacity,
            dropped: 0,
        }
    }

    /// Push a frame. If the buffer is full, the oldest frame is dropped first.
    /// Returns `true` if a drop occurred.
    pub fn push(&mut self, frame: Frame) -> bool {
        let dropped = if self.frames.len() >= self.capacity {
            self.frames.pop_front();
            self.dropped += 1;
            true
        } else {
            false
        };
        self.frames.push_back(frame);
        dropped
    }

    /// Drain up to `max` frames from the front (oldest first).
    pub fn drain(&mut self, max: usize) -> Vec<Frame> {
        let n = self.frames.len().min(max);
        self.frames.drain(..n).collect()
    }

    /// Number of frames currently buffered.
    pub fn len(&self) -> usize {
        self.frames.len()
    }

    /// Whether the buffer is empty.
    pub fn is_empty(&self) -> bool {
        self.frames.is_empty()
    }

    /// Total frames dropped over the lifetime of this buffer.
    pub fn dropped(&self) -> u64 {
        self.dropped
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use gantry_connect_proto::v1::{value::Kind, Value};

    fn f(seq: i64) -> Frame {
        Frame {
            channel: "c".into(),
            timestamp_ns: seq as u64,
            value: Some(Value {
                kind: Some(Kind::I64(seq)),
            }),
        }
    }

    fn seq_of(frame: &Frame) -> i64 {
        match frame.value.as_ref().unwrap().kind.as_ref().unwrap() {
            Kind::I64(v) => *v,
            _ => unreachable!(),
        }
    }

    #[test]
    fn push_within_capacity_never_drops() {
        let mut b = Buffer::new(3);
        assert!(!b.push(f(0)));
        assert!(!b.push(f(1)));
        assert!(!b.push(f(2)));
        assert_eq!(b.len(), 3);
        assert_eq!(b.dropped(), 0);
    }

    #[test]
    fn overflow_drops_oldest_and_counts() {
        let mut b = Buffer::new(3);
        for i in 0..3 {
            assert!(!b.push(f(i)));
        }
        // Now full; pushing 3,4 must evict 0,1.
        assert!(b.push(f(3)));
        assert!(b.push(f(4)));

        assert_eq!(b.len(), 3, "length stays bounded at capacity");
        assert_eq!(b.dropped(), 2, "two evictions counted");

        // Remaining frames are the newest three: 2,3,4.
        let drained = b.drain(usize::MAX);
        let seqs: Vec<i64> = drained.iter().map(seq_of).collect();
        assert_eq!(seqs, vec![2, 3, 4]);
    }

    #[test]
    fn drain_respects_max_and_order() {
        let mut b = Buffer::new(10);
        for i in 0..5 {
            b.push(f(i));
        }
        let first = b.drain(2);
        assert_eq!(first.iter().map(seq_of).collect::<Vec<_>>(), vec![0, 1]);
        assert_eq!(b.len(), 3);
        let rest = b.drain(100);
        assert_eq!(rest.iter().map(seq_of).collect::<Vec<_>>(), vec![2, 3, 4]);
        assert!(b.is_empty());
    }
}
