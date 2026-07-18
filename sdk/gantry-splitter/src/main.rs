//! `gantry-splitter` — a passive serial-sniffing proxy for the SO-101.
//!
//! ```text
//! gantry-splitter --leader /dev/tty.usbmodemA --follower /dev/tty.usbmodemB \
//!     [--endpoint http://localhost:4780] [--token gtk_...] \
//!     [--calibration-dir ~/.cache/huggingface/lerobot/calibration/robots/so_follower] \
//!     [--leader-id NAME --follower-id NAME] [--raw] [--no-degrees]
//! ```
//!
//! On macOS/Linux it opens the real serial device(s), creates PTY pair(s), prints the slave paths
//! to point lerobot at, and forwards bytes while publishing decoded telemetry. On Windows it prints
//! a clear message and exits 2 (PTYs are POSIX-only) — but the whole crate still *compiles* there.

use std::process::ExitCode;

use clap::Parser;

#[derive(Parser, Debug)]
#[command(
    name = "gantry-splitter",
    about = "Passive serial sniffer: forward SO-101 traffic over a PTY (lerobot runs unmodified) and publish per-joint telemetry to a Gantry bench."
)]
struct Cli {
    /// Real serial port of the LEADER arm (e.g. /dev/tty.usbmodemA). At least one of
    /// --leader/--follower is required.
    #[arg(long)]
    leader: Option<String>,

    /// Real serial port of the FOLLOWER arm (e.g. /dev/tty.usbmodemB).
    #[arg(long)]
    follower: Option<String>,

    /// Gantry ingest endpoint.
    #[arg(long, default_value = "http://localhost:4780")]
    endpoint: String,

    /// Bearer token for a non-loopback bench. Falls back to GANTRY_TOKEN so it need not appear in
    /// the process args / shell history.
    #[arg(long, env = "GANTRY_TOKEN")]
    token: Option<String>,

    /// Baud rate for the REAL serial ports (the SO-101 bus runs at 1 Mbaud). PTYs have no baud.
    #[arg(long, default_value_t = 1_000_000)]
    baud: u32,

    /// Directory holding lerobot calibration JSONs (`<id>.json`). The lerobot default layout is
    /// `~/.cache/huggingface/lerobot/calibration/robots/so_follower` (followers) and
    /// `.../calibration/teleoperators/so_leader` (leaders). Omit (or pass --raw) for uncalibrated
    /// raw-center degrees.
    #[arg(long)]
    calibration_dir: Option<String>,

    /// lerobot calibration id for the leader (the `<id>.json` file name).
    #[arg(long, default_value = "so101_leader")]
    leader_id: String,

    /// lerobot calibration id for the follower.
    #[arg(long, default_value = "so101_follower")]
    follower_id: String,

    /// Skip calibration entirely: publish uncalibrated raw-center degrees.
    #[arg(long)]
    raw: bool,

    /// Publish body joints as -100..100 instead of degrees (matches lerobot `use_degrees=false`).
    /// The lerobot default is degrees, so this is off by default.
    #[arg(long)]
    no_degrees: bool,
}

fn main() -> ExitCode {
    let cli = Cli::parse();

    #[cfg(unix)]
    {
        match run_unix(cli) {
            Ok(()) => ExitCode::SUCCESS,
            Err(e) => {
                eprintln!("gantry-splitter: {e}");
                ExitCode::FAILURE
            }
        }
    }

    #[cfg(not(unix))]
    {
        let _ = &cli;
        eprintln!(
            "gantry-splitter: PTYs require macOS/Linux; use examples/so101/so101_bridge.py \
             (or com0com, future) on Windows."
        );
        ExitCode::from(2)
    }
}

#[cfg(unix)]
fn run_unix(cli: Cli) -> Result<(), String> {
    use gantry_splitter::calibration::Normalizer;
    use gantry_splitter::daemon::{run, PortSpec, SplitterConfig};
    use gantry_splitter::sink::Role;

    let use_degrees = !cli.no_degrees;
    let dual = cli.leader.is_some() && cli.follower.is_some();

    // Build a normalizer for one role, honoring --raw / --calibration-dir, warning on fallback.
    let make_norm = |id: &str, role: &str| -> Normalizer {
        if cli.raw {
            return Normalizer::Raw;
        }
        match cli.calibration_dir.as_ref() {
            None => {
                eprintln!(
                    "gantry-splitter: no --calibration-dir; {role} output is uncalibrated raw-center degrees (pass --calibration-dir for lerobot-matching values, or --raw to silence this)."
                );
                Normalizer::Raw
            }
            Some(dir) => match Normalizer::load(std::path::Path::new(dir), id, use_degrees) {
                Ok(n) => n,
                Err(e) => {
                    eprintln!("gantry-splitter: {role} calibration load failed ({e}); falling back to raw.");
                    Normalizer::Raw
                }
            },
        }
    };

    let mut ports = Vec::new();
    if let Some(path) = cli.leader.clone() {
        ports.push(PortSpec {
            role: Role::Leader,
            device_path: path,
            device_id: "so101-leader".to_string(),
            normalizer: make_norm(&cli.leader_id, "leader"),
            emit_track: false,
        });
    }
    if let Some(path) = cli.follower.clone() {
        ports.push(PortSpec {
            role: Role::Follower,
            device_path: path,
            device_id: "so101-follower".to_string(),
            normalizer: make_norm(&cli.follower_id, "follower"),
            emit_track: dual, // track_err needs a leader snapshot to subtract.
        });
    }
    if ports.is_empty() {
        return Err("at least one of --leader or --follower is required".to_string());
    }

    run(SplitterConfig {
        endpoint: cli.endpoint,
        token: cli.token,
        baud: cli.baud,
        ports,
    })
}
