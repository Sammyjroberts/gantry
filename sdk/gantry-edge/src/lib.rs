//! # Gantry Edge — SDK core
//!
//! An OTEL-like telemetry SDK for robot/rocket firmware and bench tools. You embed it,
//! push samples on named channels, and it batches frames and ships them to a Gantry Bench
//! or Cloud ingest endpoint.
//!
//! ## Design (per `docs/ARCHITECTURE.md`)
//!
//! **Data model ≠ transport.** The core does batching, buffering, sequencing, and retry;
//! it knows nothing about HTTP/NATS/serial. Transports are pluggable behind the
//! [`Transport`] trait. The default Connect/HTTP transport lives in the separate
//! `gantry-edge-http` crate.
//!
//! **No async runtime in the core.** Firmware-friendliness: the [`Transport`] trait is
//! *synchronous*, and the background flusher is a plain `std::thread`. There is no tokio
//! anywhere in this crate or its dependency tree.
//!
//! ## `no_std` roadmap
//!
//! The frame/value/batch construction lives in [`model`] and [`batch`], which depend only
//! on `alloc`-level facilities (via the generated prost types) — no `std`-only APIs. The
//! std-only pieces are quarantined:
//!   - [`buffer`] uses `std::sync` (`Mutex`/`Condvar`),
//!   - [`client`] uses `std::thread` + `std::time`.
//!
//! A future `no_std` split keeps [`model`]/[`batch`] as-is and swaps the [`client`] flusher
//! for a caller-driven `poll`/`drain` API. Nothing in [`model`]/[`batch`] needs to change.
//!
//! ## Buffer policy (telemetry, not transactions)
//!
//! The in-memory buffer is **bounded** ([`ClientBuilder::buffer_capacity`]). When the
//! transport is down and the buffer fills, the oldest frames are **dropped** to make room
//! for new ones (drop-oldest). Telemetry favors fresh data over a complete-but-stale
//! backlog; drops are counted in [`ClientStats::frames_dropped`]. If you need
//! store-and-forward durability over constrained links, that is the job of
//! `gantry-edge-agent` (a collector daemon), not the in-process SDK.
//!
//! ## Example
//! ```no_run
//! use gantry_edge::{Client, ChannelSpec};
//! use std::time::Duration;
//! # struct T;
//! # impl gantry_edge::Transport for T {
//! #   fn publish(&self, _b: gantry_edge::FrameBatch) -> Result<u64, gantry_edge::TransportError> { Ok(0) }
//! # }
//! # let transport = T;
//! let client = Client::builder()
//!     .device_id("sim-robot")
//!     .transport(transport)
//!     .batch_max_frames(500)
//!     .batch_max_age(Duration::from_millis(100))
//!     .build()
//!     .unwrap();
//!
//! client.register(&[ChannelSpec::f64("drive.motor_left.current_a", "A", "left motor current")]).ok();
//! client.send_f64("drive.motor_left.current_a", 1.5);
//! client.flush().unwrap();
//! ```

pub mod batch;
pub mod buffer;
pub mod client;
pub mod error;
pub mod model;
pub mod transport;

// Re-export the wire types so users need only depend on `gantry-edge`.
pub use gantry_edge_proto::prost;
pub use gantry_edge_proto::v1::{value, ChannelInfo, Frame, FrameBatch, Value, ValueKind};

pub use client::{Client, ClientBuilder, ClientStats, RetryConfig};
pub use error::BuildError;
pub use model::ChannelSpec;
pub use transport::{Transport, TransportError};
