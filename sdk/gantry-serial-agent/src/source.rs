//! Byte sources: a serial port, a spool file, and a tee wrapper.
//!
//! A spooled recording *is* a paused stream (`docs/WIRE.md`): the same decoder pipeline runs
//! whether bytes arrive from a live COM port or a file, so both are just [`Read`]ers here.

use std::fs::File;
use std::io::{self, Read, Write};
use std::path::Path;
use std::time::Duration;

/// Read timeout for the serial port. Short enough that the flush timer still fires when a device
/// goes briefly quiet; on timeout the port yields `io::ErrorKind::TimedOut`, which the pipeline
/// treats as "no bytes right now".
pub const SERIAL_READ_TIMEOUT: Duration = Duration::from_millis(50);

/// Open a serial port (a COM port on Windows) for reading at `baud`.
pub fn open_serial(port: &str, baud: u32) -> serialport::Result<Box<dyn serialport::SerialPort>> {
    serialport::new(port, baud)
        .timeout(SERIAL_READ_TIMEOUT)
        .open()
}

/// Open a spool file for replay.
pub fn open_file(path: &Path) -> io::Result<File> {
    File::open(path)
}

/// A [`Read`] that mirrors every byte it yields into a writer — the raw spool recording. The
/// bytes written are exactly the bytes read (byte-identical to the live stream), so the tee file
/// can later be replayed with `--from-file`.
pub struct TeeReader<R: Read, W: Write> {
    inner: R,
    tee: W,
}

impl<R: Read, W: Write> TeeReader<R, W> {
    /// Wrap `inner`, copying all read bytes into `tee`.
    pub fn new(inner: R, tee: W) -> Self {
        Self { inner, tee }
    }
}

impl<R: Read, W: Write> Read for TeeReader<R, W> {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        let n = self.inner.read(buf)?;
        if n > 0 {
            self.tee.write_all(&buf[..n])?;
        }
        Ok(n)
    }
}
