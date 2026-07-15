//! Frame/batch construction.
//!
//! `no_std` note: like [`crate::model`], this depends only on the generated prost types.
//! The flusher (std-only) calls into here; a future `no_std` build reuses it verbatim.

use gantry_connect_proto::v1::{Frame, FrameBatch, Value};

/// Build a single [`Frame`].
#[inline]
pub fn frame(channel: impl Into<String>, timestamp_ns: u64, value: Value) -> Frame {
    Frame {
        channel: channel.into(),
        timestamp_ns,
        value: Some(value),
        // Std client emits ad-hoc (packet-less) channels today; packets are the
        // derive-struct MCU story. device_id is authoritative on the batch.
        packet: String::new(),
        device_id: String::new(),
    }
}

/// Assemble a [`FrameBatch`] for a device with a given sequence number.
#[inline]
pub fn batch(device_id: impl Into<String>, sequence: u64, frames: Vec<Frame>) -> FrameBatch {
    FrameBatch {
        device_id: device_id.into(),
        sequence,
        frames,
        // Stamped by the ingest server at acceptance; emitters leave it zero.
        received_ns: 0,
    }
}
