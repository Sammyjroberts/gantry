//! Prost-generated types for the `gantry.v1` wire contract.
//!
//! This crate carries the *data model only* — no service/client codegen. The
//! ARCHITECTURE.md lesson (data model ≠ transport) means these message types are
//! shared unchanged across every transport (Connect/HTTP, NATS, raw framing).
//!
//! `prost` is re-exported so downstream crates can `use gantry_connect_proto::prost::Message`
//! without adding their own pinned prost dependency.

pub use prost;

/// Generated messages for the `gantry.v1` package.
pub mod gantry {
    pub mod v1 {
        include!(concat!(env!("OUT_DIR"), "/gantry.v1.rs"));
    }
}

/// Convenience alias: `gantry_connect_proto::v1::FrameBatch`.
pub use gantry::v1;

#[cfg(test)]
mod tests {
    use crate::v1::{value::Kind, Frame, FrameBatch, Value};
    use prost::Message;

    #[test]
    fn framebatch_roundtrip() {
        let batch = FrameBatch {
            device_id: "sim-robot".into(),
            sequence: 42,
            frames: vec![
                Frame {
                    channel: "drive.motor_left.current_a".into(),
                    timestamp_ns: 1_700_000_000_000_000_000,
                    value: Some(Value {
                        kind: Some(Kind::F64(1.5)),
                    }),
                    packet: "drive".into(),
                    device_id: String::new(),
                },
                Frame {
                    channel: "status.armed".into(),
                    timestamp_ns: 1_700_000_000_000_000_001,
                    value: Some(Value {
                        kind: Some(Kind::Flag(true)),
                    }),
                    packet: String::new(),
                    device_id: String::new(),
                },
            ],
        };

        let bytes = batch.encode_to_vec();
        let decoded = FrameBatch::decode(bytes.as_slice()).expect("decode");

        assert_eq!(batch, decoded);
        assert_eq!(decoded.sequence, 42);
        assert_eq!(decoded.frames.len(), 2);
    }
}
