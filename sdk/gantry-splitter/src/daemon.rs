//! The daemon (POSIX only): open each real serial port, create a PTY, and pump between them while
//! publishing decoded telemetry.
//!
//! Reconnect / robustness contract (per the product spec):
//! * lerobot opens and closes the PTY slave repeatedly — the host→device pump *tolerates* EOF/EIO
//!   and keeps the master alive, so a re-open just resumes.
//! * PTYs have no baud; a baud change lerobot makes on the slave is a no-op. The real port is
//!   opened at [`SplitterConfig::baud`] (default 1 Mbaud).
//! * A real-device unplug surfaces as a fatal read error on the device→host pump; the daemon prints
//!   a clear message and exits non-zero (systemd/loop restart is the operator's job in v1).

use std::sync::mpsc;
use std::sync::Arc;
use std::time::Duration;

use gantry_edge::Transport;
use gantry_edge_http::HttpTransport;

use crate::calibration::Normalizer;
use crate::decoder::Direction;
use crate::pty;
use crate::publish::Publisher;
use crate::pump::{pump_direction, EofPolicy, PortProcessor};
use crate::sink::{channel_specs, PortSink, Role, SharedLeader};

/// Read timeout on the real serial port: short enough to stay responsive, long enough not to spin.
const SERIAL_TIMEOUT: Duration = Duration::from_millis(10);

/// One port to bridge.
pub struct PortSpec {
    pub role: Role,
    /// Real serial device path (e.g. `/dev/tty.usbmodemA`).
    pub device_path: String,
    /// Gantry device id (e.g. `so101-leader`).
    pub device_id: String,
    /// How to normalize raw counts (calibrated or raw).
    pub normalizer: Normalizer,
    /// Publish `track_err` (follower only, when both arms run).
    pub emit_track: bool,
}

/// Full daemon configuration.
pub struct SplitterConfig {
    pub endpoint: String,
    pub token: Option<String>,
    pub baud: u32,
    pub ports: Vec<PortSpec>,
}

/// Run the daemon. Returns `Err` on a fatal condition (device unplug, port open failure).
pub fn run(config: SplitterConfig) -> Result<(), String> {
    if config.ports.is_empty() {
        return Err("at least one of --leader or --follower is required".to_string());
    }

    // One HTTP transport, shared by both devices' publishers.
    let mut builder = HttpTransport::builder(config.endpoint.clone());
    if let Some(token) = config.token.as_ref().filter(|t| !t.trim().is_empty()) {
        builder = builder.bearer_token(token.trim());
    }
    let transport: Arc<dyn Transport> = Arc::new(builder.build());

    let shared_leader = SharedLeader::new();

    // Keep publishers alive for the daemon's lifetime (Drop flushes on exit).
    let mut publishers: Vec<Arc<Publisher>> = Vec::new();
    // Fatal (device-reader) threads report here; the first to exit ends the daemon.
    let (fatal_tx, fatal_rx) = mpsc::channel::<String>();
    // Detached tolerate-side threads; kept so they aren't joined early.
    let mut tolerate_handles = Vec::new();

    for spec in config.ports {
        // Open the real device.
        let serial = serialport::new(spec.device_path.as_str(), config.baud)
            .timeout(SERIAL_TIMEOUT)
            .open()
            .map_err(|e| format!("opening serial port {}: {e}", spec.device_path))?;
        let serial_writer = serial
            .try_clone()
            .map_err(|e| format!("cloning serial port {}: {e}", spec.device_path))?;

        // Create the PTY lerobot will use.
        let ptypair =
            pty::create().map_err(|e| format!("creating PTY for {}: {e}", spec.device_id))?;
        let master_reader = ptypair.master;
        let master_writer = master_reader
            .try_clone()
            .map_err(|e| format!("cloning PTY master for {}: {e}", spec.device_id))?;

        let role_label = match spec.role {
            Role::Leader => "leader  ",
            Role::Follower => "follower",
        };
        let hint = match spec.role {
            Role::Leader => "point --teleop.port here",
            Role::Follower => "point --robot.port here",
        };
        println!(
            "{role_label}-> {}   ({hint}; device={})",
            ptypair.slave_path, spec.device_id
        );

        // Publisher + sink + processor.
        let channels = channel_specs(&spec.normalizer, spec.emit_track);
        let publisher =
            Publisher::start_arc(Arc::clone(&transport), spec.device_id.clone(), channels);
        publishers.push(Arc::clone(&publisher));

        let sink = PortSink::new(
            spec.role,
            publisher,
            spec.normalizer,
            Arc::clone(&shared_leader),
            spec.emit_track,
        );
        let processor = PortProcessor::new(sink);

        // device -> host (status replies). Fatal EOF: unplug ends the daemon.
        let proc_dh = Arc::clone(&processor);
        let fatal_tx_dh = fatal_tx.clone();
        let device_id = spec.device_id.clone();
        std::thread::Builder::new()
            .name(format!("split-dev2host-{}", spec.device_id))
            .spawn(move || {
                let res = pump_direction(
                    serial,
                    master_writer,
                    Direction::DeviceToHost,
                    proc_dh,
                    EofPolicy::Fatal,
                );
                let msg = match res {
                    Ok(()) => format!("{device_id}: serial device closed (EOF)"),
                    Err(e) => format!("{device_id}: serial device error: {e}"),
                };
                let _ = fatal_tx_dh.send(msg);
            })
            .map_err(|e| format!("spawning device pump: {e}"))?;

        // host -> device (instructions). Tolerate EOF: lerobot reopens the PTY freely.
        let proc_hd = Arc::clone(&processor);
        let h = std::thread::Builder::new()
            .name(format!("split-host2dev-{}", spec.device_id))
            .spawn(move || {
                let _ = pump_direction(
                    master_reader,
                    serial_writer,
                    Direction::HostToDevice,
                    proc_hd,
                    EofPolicy::Tolerate,
                );
            })
            .map_err(|e| format!("spawning host pump: {e}"))?;
        tolerate_handles.push(h);
    }
    drop(fatal_tx); // so recv() unblocks if every fatal thread somehow exits

    println!(
        "gantry-splitter: forwarding. lerobot can now open the PTY path(s) above. (ctrl-c to stop)"
    );

    // Block until the first port's device side dies.
    match fatal_rx.recv() {
        Ok(msg) => Err(msg),
        Err(_) => Err("all serial devices closed".to_string()),
    }
}
