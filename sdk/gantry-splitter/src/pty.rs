//! PTY creation (POSIX only).
//!
//! We open a pseudo-terminal *master*, grant + unlock it, and hand the caller the master as a
//! [`File`] plus the *slave* device path (e.g. `/dev/ttys042` on macOS, `/dev/pts/7` on Linux).
//! lerobot opens that slave path as if it were the real serial port; we pump bytes between the
//! master and the true device. PTYs have no baud rate — lerobot's `--*.port` baud setting on the
//! slave is a no-op, which is exactly what we want (the real port is opened at 1 Mbaud).

use std::fs::File;
use std::io;

use rustix::pty::{grantpt, openpt, ptsname, unlockpt, OpenptFlags};
use rustix::termios::{tcgetattr, tcsetattr, OptionalActions};

/// A created PTY: the master end (read/write) and the slave path to point lerobot at.
pub struct Pty {
    /// The master end. Duplex; clone with [`File::try_clone`] for the two pump threads.
    pub master: File,
    /// Filesystem path of the slave (`--teleop.port` / `--robot.port` for lerobot).
    pub slave_path: String,
}

/// Open a new PTY pair, returning the master file and the slave path.
pub fn create() -> io::Result<Pty> {
    // RDWR so we can both read host writes and write device replies; NOCTTY so this never becomes
    // our controlling terminal.
    let master = openpt(OpenptFlags::RDWR | OpenptFlags::NOCTTY)?;
    grantpt(&master)?;
    unlockpt(&master)?;
    let slave = ptsname(&master, Vec::new())?;
    let slave_path = slave.to_string_lossy().into_owned();

    // A fresh PTY starts in canonical ("cooked") line discipline, which mangles binary bytes
    // (NL↔CRNL translation, echo, signal chars). The Feetech protocol is raw binary, so force the
    // pty into raw mode. On Linux tcsetattr on the master applies to the pair; lerobot's pyserial
    // also sets raw on open, so this is belt-and-suspenders — best-effort, never fatal.
    if let Ok(mut tio) = tcgetattr(&master) {
        tio.make_raw();
        let _ = tcsetattr(&master, OptionalActions::Now, &tio);
    }

    Ok(Pty {
        master: File::from(master),
        slave_path,
    })
}
