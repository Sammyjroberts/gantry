//! The pluggable transport boundary.

use std::fmt;

use gantry_edge_proto::v1::{ChannelInfo, FrameBatch};

/// A telemetry transport.
///
/// This is the entire seam between the SDK core and the wire. It is deliberately
/// **synchronous** (no async runtime) so the core can run on constrained/firmware targets.
///
/// A transport receives a fully-built [`FrameBatch`] and durably hands it off, returning the
/// highest per-device `sequence` the server has acked (see `PublishBatchResponse.acked_sequence`
/// in the proto). Emitters with local buffers can trim through that point.
///
/// [`Transport::register`] has a default no-op impl: channel registration is optional metadata,
/// and not every transport (e.g. a raw serial framing) has a place to put it. The HTTP transport
/// overrides it.
pub trait Transport: Send + Sync {
    /// Publish one batch. Returns the highest acked per-device sequence.
    fn publish(&self, batch: FrameBatch) -> Result<u64, TransportError>;

    /// Register or update channel metadata for the device. Default: no-op.
    fn register(&self, device_id: &str, channels: &[ChannelInfo]) -> Result<(), TransportError> {
        let _ = (device_id, channels);
        Ok(())
    }
}

// Blanket impl so `Box<dyn Transport>` / `Arc<dyn Transport>` also satisfy `Transport`.
impl<T: Transport + ?Sized> Transport for std::sync::Arc<T> {
    fn publish(&self, batch: FrameBatch) -> Result<u64, TransportError> {
        (**self).publish(batch)
    }
    fn register(&self, device_id: &str, channels: &[ChannelInfo]) -> Result<(), TransportError> {
        (**self).register(device_id, channels)
    }
}

/// Error taxonomy for transports.
///
/// The [`TransportError::is_retryable`] classification drives the flusher's retry/backoff:
/// transient faults are retried with capped exponential backoff, permanent ones cause the
/// batch to be dropped (telemetry, not transactions).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum TransportError {
    /// Could not reach the endpoint (DNS, connection refused, reset, network down).
    Connection(String),
    /// Request timed out.
    Timeout,
    /// The server returned a Connect-protocol error (non-200 with a `code` field).
    Protocol { code: String, message: String },
    /// Unexpected HTTP status with no parseable Connect error body.
    Status(u16),
    /// Failed to encode the request message.
    Encode(String),
    /// Failed to decode the response message.
    Decode(String),
    /// Anything else.
    Other(String),
}

impl TransportError {
    /// Whether the flusher should retry after backoff, or give up and drop the batch.
    pub fn is_retryable(&self) -> bool {
        match self {
            TransportError::Connection(_) | TransportError::Timeout => true,
            TransportError::Status(code) => *code >= 500,
            // Connect codes that indicate a transient server-side condition.
            TransportError::Protocol { code, .. } => matches!(
                code.as_str(),
                "unavailable" | "deadline_exceeded" | "aborted" | "internal" | "resource_exhausted"
            ),
            // Encode/Decode/Other and 4xx-ish Protocol codes: retrying won't help.
            _ => false,
        }
    }
}

impl fmt::Display for TransportError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            TransportError::Connection(m) => write!(f, "connection error: {m}"),
            TransportError::Timeout => write!(f, "request timed out"),
            TransportError::Protocol { code, message } => {
                write!(f, "connect error [{code}]: {message}")
            }
            TransportError::Status(c) => write!(f, "unexpected http status {c}"),
            TransportError::Encode(m) => write!(f, "encode error: {m}"),
            TransportError::Decode(m) => write!(f, "decode error: {m}"),
            TransportError::Other(m) => write!(f, "transport error: {m}"),
        }
    }
}

impl std::error::Error for TransportError {}
