//! `gantry-serial-agent` — read a `docs/WIRE.md` byte stream (a COM port or a spool file) and
//! forward it to a Gantry Edge/Backend ingest endpoint.
//!
//! ```text
//! gantry-serial-agent --port COM7 [--baud 115200] [--endpoint http://localhost:4780] [--tee-file flight.gtl]
//! gantry-serial-agent --from-file flight.gtl [--endpoint ...] [--rate live|max] [--anchor <rfc3339>]
//! ```

use std::fs::File;
use std::path::{Path, PathBuf};
use std::process::ExitCode;
use std::time::UNIX_EPOCH;

use clap::{Parser, ValueEnum};
use gantry_wire::Decoder;

use gantry_serial_agent::pipeline::{self, Pace, PipelineConfig};
use gantry_serial_agent::source::{open_file, open_serial, TeeReader};
use gantry_serial_agent::system_clock;
use gantry_serial_agent::translate::{Config, TimeAnchor, Translator};
use gantry_transport_http::HttpTransport;

#[derive(Copy, Clone, PartialEq, Eq, Debug, ValueEnum)]
enum RateArg {
    /// Pace replay by the device's own TimeSync deltas (approx. real time).
    Live,
    /// Shove bytes as fast as possible.
    Max,
}

#[derive(Parser, Debug)]
#[command(
    name = "gantry-serial-agent",
    about = "Forward a Gantry device wire stream (serial or spool file) to an Edge/Backend endpoint."
)]
struct Cli {
    /// Serial port to read (e.g. COM7 on Windows, /dev/ttyACM0 on Linux). Mutually exclusive
    /// with --from-file.
    #[arg(long, conflicts_with = "from_file")]
    port: Option<String>,

    /// Serial baud rate.
    #[arg(long, default_value_t = 115_200)]
    baud: u32,

    /// Replay a spool file instead of reading a port.
    #[arg(long)]
    from_file: Option<PathBuf>,

    /// Ingest endpoint base URL.
    #[arg(long, default_value = "http://localhost:4780")]
    endpoint: String,

    /// While reading the port, append the raw bytes to this spool file (byte-identical).
    #[arg(long)]
    tee_file: Option<PathBuf>,

    /// File-replay pacing.
    #[arg(long, value_enum, default_value_t = RateArg::Max)]
    rate: RateArg,

    /// Epoch anchor for file replay (RFC 3339 UTC, e.g. 2026-07-12T18:00:00Z). Defaults to the
    /// spool file's modification time.
    #[arg(long)]
    anchor: Option<String>,
}

fn main() -> ExitCode {
    let cli = Cli::parse();
    match run(cli) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("gantry-serial-agent: {e}");
            ExitCode::FAILURE
        }
    }
}

fn run(cli: Cli) -> Result<(), String> {
    let transport = HttpTransport::new(cli.endpoint.clone());

    if let Some(path) = cli.from_file.as_ref() {
        run_file(&cli, path, transport)
    } else if let Some(port) = cli.port.as_ref() {
        run_serial(&cli, port, transport)
    } else {
        Err("one of --port or --from-file is required".to_string())
    }
}

fn run_file(cli: &Cli, path: &Path, transport: HttpTransport) -> Result<(), String> {
    let anchor_ns = match cli.anchor.as_ref() {
        Some(s) => parse_rfc3339_ns(s).map_err(|e| format!("--anchor: {e}"))?,
        None => {
            file_mtime_ns(path).map_err(|e| format!("reading mtime of {}: {e}", path.display()))?
        }
    };
    let cfg = Config {
        anchor: TimeAnchor::Fixed(anchor_ns),
        ..Config::default()
    };
    let mut translator = Translator::new(transport, system_clock(), cfg);
    let mut decoder = Decoder::new();
    let file = open_file(path).map_err(|e| format!("opening {}: {e}", path.display()))?;
    let pcfg = PipelineConfig {
        read_chunk: 8192,
        pace: match cli.rate {
            RateArg::Live => Pace::Live,
            RateArg::Max => Pace::Max,
        },
        ..PipelineConfig::default()
    };
    eprintln!(
        "gantry-serial-agent: replaying {} → {} (anchor {} ns, rate {:?})",
        path.display(),
        cli.endpoint,
        anchor_ns,
        cli.rate
    );
    pipeline::run(file, &mut decoder, &mut translator, pcfg).map_err(|e| format!("replay: {e}"))?;
    report(&translator, &decoder);
    Ok(())
}

fn run_serial(cli: &Cli, port: &str, transport: HttpTransport) -> Result<(), String> {
    let cfg = Config {
        anchor: TimeAnchor::Live,
        ..Config::default()
    };
    let mut translator = Translator::new(transport, system_clock(), cfg);
    let mut decoder = Decoder::new();
    let serial =
        open_serial(port, cli.baud).map_err(|e| format!("opening serial port {port}: {e}"))?;
    let pcfg = PipelineConfig {
        read_chunk: 512,
        ..PipelineConfig::default()
    };
    eprintln!(
        "gantry-serial-agent: reading {} @ {} baud → {}",
        port, cli.baud, cli.endpoint
    );

    // --tee-file: mirror the raw bytes into a spool recording while forwarding.
    if let Some(tee_path) = cli.tee_file.as_ref() {
        let tee = File::create(tee_path)
            .map_err(|e| format!("creating tee file {}: {e}", tee_path.display()))?;
        eprintln!(
            "gantry-serial-agent: teeing raw bytes to {}",
            tee_path.display()
        );
        let reader = TeeReader::new(serial, tee);
        pipeline::run(reader, &mut decoder, &mut translator, pcfg)
            .map_err(|e| format!("serial: {e}"))?;
    } else {
        pipeline::run(serial, &mut decoder, &mut translator, pcfg)
            .map_err(|e| format!("serial: {e}"))?;
    }
    report(&translator, &decoder);
    Ok(())
}

fn report(translator: &Translator<HttpTransport>, decoder: &Decoder) {
    let c = translator.counters();
    eprintln!(
        "gantry-serial-agent: done. device={:?} frames_translated={} frames_sent={} \
         batches_sent={} batches_dropped={} presync_dropped={} orphan_frames={} \
         register_failures={} | decoder: crc_failures={} malformed={} dropped_samples={} \
         unknown_records={}",
        translator.device_id(),
        c.frames_translated,
        c.frames_sent,
        c.batches_sent,
        c.batches_dropped,
        c.presync_dropped,
        c.orphan_frames,
        c.register_failures,
        decoder.crc_failures(),
        decoder.malformed(),
        decoder.dropped_samples(),
        decoder.unknown_records(),
    );
}

/// A spool file's modification time as epoch nanoseconds (the default replay anchor).
fn file_mtime_ns(path: &Path) -> std::io::Result<i128> {
    let mtime = std::fs::metadata(path)?.modified()?;
    Ok(mtime
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos() as i128)
        .unwrap_or(0))
}

/// Minimal RFC 3339 (UTC) parser: `YYYY-MM-DDTHH:MM:SS[.fraction]Z`. Returns epoch nanoseconds.
/// Deliberately dependency-free — the agent has no need for a full datetime crate.
fn parse_rfc3339_ns(s: &str) -> Result<i128, String> {
    let s = s.trim();
    let bytes = s.as_bytes();
    if bytes.len() < 20 {
        return Err(format!("too short: {s:?}"));
    }
    // Split date and time on 'T'.
    let (date, rest) = s.split_once(['T', 't']).ok_or("missing 'T' separator")?;
    let d: Vec<&str> = date.split('-').collect();
    if d.len() != 3 {
        return Err("date must be YYYY-MM-DD".into());
    }
    let year: i64 = d[0].parse().map_err(|_| "bad year")?;
    let month: i64 = d[1].parse().map_err(|_| "bad month")?;
    let day: i64 = d[2].parse().map_err(|_| "bad day")?;

    // Strip trailing zone; only 'Z' (UTC) is supported.
    let rest = rest
        .strip_suffix(['Z', 'z'])
        .ok_or("only UTC ('Z') timestamps are supported")?;
    let (hms, frac) = match rest.split_once('.') {
        Some((h, f)) => (h, f),
        None => (rest, ""),
    };
    let t: Vec<&str> = hms.split(':').collect();
    if t.len() != 3 {
        return Err("time must be HH:MM:SS".into());
    }
    let hour: i64 = t[0].parse().map_err(|_| "bad hour")?;
    let min: i64 = t[1].parse().map_err(|_| "bad minute")?;
    let sec: i64 = t[2].parse().map_err(|_| "bad second")?;

    // Fractional seconds → nanoseconds (pad/truncate to 9 digits).
    let mut frac_ns: i128 = 0;
    if !frac.is_empty() {
        let mut digits: String = frac.chars().take_while(|c| c.is_ascii_digit()).collect();
        while digits.len() < 9 {
            digits.push('0');
        }
        digits.truncate(9);
        frac_ns = digits.parse().map_err(|_| "bad fraction")?;
    }

    let days = days_from_civil(year, month, day);
    let secs = days * 86_400 + hour * 3_600 + min * 60 + sec;
    Ok(secs as i128 * 1_000_000_000 + frac_ns)
}

/// Days since 1970-01-01 for a proleptic-Gregorian date (Howard Hinnant's algorithm).
fn days_from_civil(y: i64, m: i64, d: i64) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = (if y >= 0 { y } else { y - 399 }) / 400;
    let yoe = y - era * 400; // [0, 399]
    let doy = (153 * (if m > 2 { m - 3 } else { m + 9 }) + 2) / 5 + d - 1; // [0, 365]
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy; // [0, 146096]
    era * 146_097 + doe - 719_468
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rfc3339_epoch() {
        assert_eq!(parse_rfc3339_ns("1970-01-01T00:00:00Z").unwrap(), 0);
    }

    #[test]
    fn rfc3339_known_instant() {
        // 2021-11-14T22:13:20Z == 1_636_928_000 s.
        assert_eq!(
            parse_rfc3339_ns("2021-11-14T22:13:20Z").unwrap(),
            1_636_928_000i128 * 1_000_000_000
        );
    }

    #[test]
    fn rfc3339_fraction() {
        assert_eq!(
            parse_rfc3339_ns("1970-01-01T00:00:00.5Z").unwrap(),
            500_000_000
        );
    }

    #[test]
    fn rfc3339_rejects_non_utc() {
        assert!(parse_rfc3339_ns("2021-11-14T22:13:20+01:00").is_err());
    }
}
