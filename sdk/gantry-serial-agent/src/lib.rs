//! # gantry-serial-agent — the host-side collector
//!
//! Turns a `docs/WIRE.md` device byte stream into `gantry.v1` ingest calls. This is the bridge
//! for "plug the laptop into the robot": firmware speaks the wire format over USB-CDC (a COM port
//! on Windows), and this agent forwards it to a Gantry Edge (or Backend) endpoint over
//! Connect/HTTP. The same pipeline replays a spool file — a recording *is* a paused stream.
//!
//! ```text
//! COM7 / flight.gtl ──▶ gantry_wire::Decoder ──▶ Translator ──▶ Transport ──▶ Edge @ :4780
//!     (source)              (records)            (frames/batches)   (HTTP)
//! ```
//!
//! ## Modules
//!
//! * [`translate`] — the transport-agnostic core: [`Translator`] maps records to
//!   `RegisterChannels`/`PublishBatch`. Unit-testable with a mock [`gantry_connect::Transport`].
//! * [`timesync`] — [`TimeMapper`], device ticks → epoch nanoseconds.
//! * [`source`] — serial / file byte sources and the raw-spool tee.
//! * [`pipeline`] — the single-threaded pump wiring a source through the decoder to the translator.
//!
//! [`Translator`]: translate::Translator
//! [`TimeMapper`]: timesync::TimeMapper

pub mod pipeline;
pub mod source;
pub mod timesync;
pub mod translate;

pub use pipeline::{run, Pace, PipelineConfig};
pub use timesync::TimeMapper;
pub use translate::{
    AgentCounters, Config, DecoderCounters, HostClock, TimeAnchor, Translator, STATS_PACKET,
};

use std::time::{SystemTime, UNIX_EPOCH};

/// A real host wall-clock returning epoch nanoseconds, for [`Translator::new`].
pub fn system_clock() -> HostClock {
    Box::new(|| {
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_nanos() as i128)
            .unwrap_or(0)
    })
}
