//! Error types for building and driving a [`crate::Client`].

use std::fmt;

/// Returned by [`crate::ClientBuilder::build`] when required config is missing/invalid.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum BuildError {
    /// `device_id` was never set (or was empty).
    MissingDeviceId,
    /// No transport was provided.
    MissingTransport,
    /// A numeric bound was set to zero (`batch_max_frames` / `buffer_capacity`).
    InvalidConfig(&'static str),
}

impl fmt::Display for BuildError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            BuildError::MissingDeviceId => write!(f, "device_id is required"),
            BuildError::MissingTransport => write!(f, "a transport is required"),
            BuildError::InvalidConfig(m) => write!(f, "invalid config: {m}"),
        }
    }
}

impl std::error::Error for BuildError {}

/// Returned by [`crate::Client::flush`] if the flusher thread has died (mutex poisoned).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FlushError;

impl fmt::Display for FlushError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "flush failed: flusher thread is not running")
    }
}

impl std::error::Error for FlushError {}
