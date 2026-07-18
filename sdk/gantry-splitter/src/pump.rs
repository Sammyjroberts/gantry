//! The byte pump: forward one direction of a port unconditionally, then tee a copy to the decoder.
//!
//! Two of these run per port (device→host and host→device). **Forwarding is unconditional and
//! comes first** — the write to the far side happens immediately on every read, before any lock or
//! decode, so decode work never adds latency to the passthrough. Decode runs on a copy afterward
//! and its result feeds the [`PortSink`]; a decode/CRC hiccup can never stall the pump.
//!
//! The functions are generic over [`Read`]/[`Write`] so the same code drives a real serial device
//! in production and a PTY pair standing in for one in tests.

use std::io::{self, Read, Write};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use crate::decoder::{Direction, PortDecoder};
use crate::sink::PortSink;

/// What to do when a reader hits clean EOF (`read` returns `Ok(0)`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EofPolicy {
    /// The real device went away (serial unplug): stop the pump — the daemon exits with a message.
    Fatal,
    /// lerobot closed the PTY slave (it does this every open/close cycle): tolerate and keep the
    /// far side alive, briefly backing off so we don't spin on repeated EIO/EOF.
    Tolerate,
}

/// Shared per-port decode state: the two directions feed one decoder (half-duplex context) and one
/// sink, each behind its own lock so a slow publish never blocks the *other* direction's decode.
pub struct PortProcessor {
    decoder: Mutex<PortDecoder>,
    sink: Arc<PortSink>,
}

impl PortProcessor {
    /// Wrap a decoder and sink.
    pub fn new(sink: Arc<PortSink>) -> Arc<Self> {
        Arc::new(Self {
            decoder: Mutex::new(PortDecoder::new()),
            sink,
        })
    }

    /// Feed already-forwarded bytes to the decoder and publish whatever they complete.
    fn ingest(&self, dir: Direction, bytes: &[u8]) {
        let readings = match self.decoder.lock() {
            Ok(mut d) => d.feed(dir, bytes),
            Err(_) => return,
        };
        if !readings.is_empty() {
            self.sink.on_readings(&readings);
        }
    }
}

/// Pump one direction until EOF/fatal error: read → forward → tee-decode, forever.
///
/// `is_timeout` classifies a read error as "no bytes right now" (retry) vs a real failure. Serial
/// ports surface [`io::ErrorKind::TimedOut`]; PTYs generally block instead.
pub fn pump_direction<R: Read, W: Write>(
    mut reader: R,
    mut writer: W,
    dir: Direction,
    processor: Arc<PortProcessor>,
    eof: EofPolicy,
) -> io::Result<()> {
    let mut buf = [0u8; 4096];
    loop {
        match reader.read(&mut buf) {
            Ok(0) => match eof {
                EofPolicy::Fatal => return Ok(()),
                EofPolicy::Tolerate => {
                    std::thread::sleep(Duration::from_millis(5));
                    continue;
                }
            },
            Ok(n) => {
                // Forward FIRST — unconditional, minimum latency.
                writer.write_all(&buf[..n])?;
                writer.flush()?;
                // Then decode a copy; never affects the passthrough.
                processor.ingest(dir, &buf[..n]);
            }
            Err(e) if is_retryable_read(&e) => continue,
            Err(e) => match eof {
                // A closed PTY slave can surface as EIO rather than EOF; tolerate it too.
                EofPolicy::Tolerate => {
                    std::thread::sleep(Duration::from_millis(5));
                    continue;
                }
                EofPolicy::Fatal => return Err(e),
            },
        }
    }
}

fn is_retryable_read(e: &io::Error) -> bool {
    matches!(
        e.kind(),
        io::ErrorKind::TimedOut | io::ErrorKind::WouldBlock | io::ErrorKind::Interrupted
    )
}
