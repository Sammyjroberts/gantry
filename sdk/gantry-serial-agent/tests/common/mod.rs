//! Shared test helpers: a recording mock [`Transport`] and small assertions.

#![allow(dead_code)]

use std::sync::{Arc, Mutex};

use gantry_edge::{ChannelInfo, Frame, FrameBatch, Transport, TransportError};

/// Everything the mock has observed.
#[derive(Default)]
pub struct Recorded {
    /// One entry per `register` call: (device_id, channels).
    pub registrations: Vec<(String, Vec<ChannelInfo>)>,
    /// One entry per successful `publish` call.
    pub batches: Vec<FrameBatch>,
    /// If set, every `publish` fails with this error (to exercise retry/drop).
    pub fail_publish: Option<TransportError>,
    /// Count of publish attempts (including failed ones).
    pub publish_attempts: u64,
}

/// A cloneable, thread-safe [`Transport`] that records calls for later assertions.
#[derive(Clone, Default)]
pub struct MockTransport {
    pub inner: Arc<Mutex<Recorded>>,
}

impl MockTransport {
    pub fn new() -> Self {
        Self::default()
    }

    /// Snapshot: all frames across all recorded batches, in order.
    pub fn all_frames(&self) -> Vec<Frame> {
        self.inner
            .lock()
            .unwrap()
            .batches
            .iter()
            .flat_map(|b| b.frames.clone())
            .collect()
    }

    /// All registration calls.
    pub fn registrations(&self) -> Vec<(String, Vec<ChannelInfo>)> {
        self.inner.lock().unwrap().registrations.clone()
    }

    pub fn batches(&self) -> Vec<FrameBatch> {
        self.inner.lock().unwrap().batches.clone()
    }

    /// Frames whose `packet` equals `packet`.
    pub fn frames_in_packet(&self, packet: &str) -> Vec<Frame> {
        self.all_frames()
            .into_iter()
            .filter(|f| f.packet == packet)
            .collect()
    }
}

impl Transport for MockTransport {
    fn publish(&self, batch: FrameBatch) -> Result<u64, TransportError> {
        let mut g = self.inner.lock().unwrap();
        g.publish_attempts += 1;
        if let Some(err) = g.fail_publish.clone() {
            return Err(err);
        }
        let seq = batch.sequence;
        g.batches.push(batch);
        Ok(seq)
    }

    fn register(&self, device_id: &str, channels: &[ChannelInfo]) -> Result<(), TransportError> {
        self.inner
            .lock()
            .unwrap()
            .registrations
            .push((device_id.to_string(), channels.to_vec()));
        Ok(())
    }
}
