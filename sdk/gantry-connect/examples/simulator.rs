//! A small robot simulator: the demo feed for the Gantry web UI.
//!
//! Emits ~6 channels at 50 Hz as `device_id = "sim-robot"` to a Gantry ingest endpoint
//! (default `http://localhost:4780`, override with `GANTRY_ENDPOINT`). Registers channels on
//! startup, prints a status line every ~2s, and runs until Ctrl-C.
//!
//! Run it with:
//!     cargo run -p gantry-connect --example simulator
//!     GANTRY_ENDPOINT=http://localhost:4780 cargo run -p gantry-connect --example simulator

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::thread::sleep;
use std::time::{Duration, Instant};

use gantry_connect::{ChannelSpec, Client};
use gantry_transport_http::HttpTransport;

const HZ: u64 = 50;
const PERIOD: Duration = Duration::from_millis(1000 / HZ);

fn main() {
    let endpoint =
        std::env::var("GANTRY_ENDPOINT").unwrap_or_else(|_| "http://localhost:4780".into());
    println!(
        "gantry simulator -> {endpoint} (device_id=sim-robot, {HZ}Hz/channel). Ctrl-C to stop."
    );

    let transport = HttpTransport::new(endpoint);
    let client = Client::builder()
        .device_id("sim-robot")
        .transport(transport)
        .batch_max_frames(500)
        .batch_max_age(Duration::from_millis(100))
        .buffer_capacity(50_000)
        .build()
        .expect("build client");

    let channels = [
        ChannelSpec::f64(
            "drive.motor_left.current_a",
            "A",
            "left drive motor current",
        ),
        ChannelSpec::f64(
            "drive.motor_right.current_a",
            "A",
            "right drive motor current",
        ),
        ChannelSpec::f64("imu.gyro_z", "rad/s", "yaw rate (square-ish)"),
        ChannelSpec::f64("battery.voltage", "V", "pack voltage, slow decay + noise"),
        ChannelSpec::f64("thermal.board_c", "degC", "board temperature drift"),
        ChannelSpec::bool("status.armed", "", "armed state, toggles"),
    ];

    match client.register(&channels) {
        Ok(()) => println!("registered {} channels", channels.len()),
        Err(e) => eprintln!("warning: channel registration failed ({e}); continuing (buffered)"),
    }

    // Ctrl-C handling.
    let running = Arc::new(AtomicBool::new(true));
    {
        let running = Arc::clone(&running);
        ctrlc::set_handler(move || running.store(false, Ordering::SeqCst))
            .expect("install Ctrl-C handler");
    }

    let start = Instant::now();
    let mut rng = Lcg::new(0x1234_5678);
    let mut tick: u64 = 0;

    while running.load(Ordering::SeqCst) {
        let t = start.elapsed().as_secs_f64();

        // Two out-of-phase sinusoids for the drive motors.
        client.send_f64("drive.motor_left.current_a", 6.0 * (t * 2.0).sin() + 6.5);
        client.send_f64(
            "drive.motor_right.current_a",
            6.0 * (t * 2.0 + std::f64::consts::PI).sin() + 6.5,
        );

        // Square-ish yaw rate: sign of a slow sine, softened by a little noise.
        let gyro = if (t * 0.7).sin() >= 0.0 { 1.5 } else { -1.5 } + rng.noise(0.05);
        client.send_f64("imu.gyro_z", gyro);

        // Battery: slow linear decay from ~25.2V plus noise.
        client.send_f64("battery.voltage", 25.2 - 0.01 * t + rng.noise(0.02));

        // Board temperature: asymptotic drift upward.
        client.send_f64(
            "thermal.board_c",
            24.0 + 12.0 * (1.0 - (-t / 90.0).exp()) + rng.noise(0.1),
        );

        // Armed toggles every ~5s.
        client.send_bool("status.armed", (t as u64 / 5) % 2 == 0);

        tick += 1;
        if tick % (HZ * 2) == 0 {
            let s = client.stats();
            println!(
                "t={:5.1}s  frames_sent={:>7}  buffered={:>4}  dropped={:>4}  last_acked_seq={}",
                t, s.frames_sent, s.frames_buffered, s.frames_dropped, s.last_acked_sequence
            );
        }

        sleep(PERIOD);
    }

    println!("\nstopping, flushing...");
    client.shutdown();
    let s = client.stats();
    println!(
        "done. frames_sent={} batches_sent={} dropped={} last_acked_seq={}",
        s.frames_sent, s.batches_sent, s.frames_dropped, s.last_acked_sequence
    );
}

/// Tiny linear-congruential generator so the demo has no `rand` dependency.
struct Lcg {
    state: u64,
}

impl Lcg {
    fn new(seed: u64) -> Self {
        Self { state: seed | 1 }
    }
    /// Uniform-ish noise in [-amp, amp].
    fn noise(&mut self, amp: f64) -> f64 {
        self.state = self
            .state
            .wrapping_mul(6364136223846793005)
            .wrapping_add(1442695040888963407);
        let unit = (self.state >> 11) as f64 / (1u64 << 53) as f64; // [0,1)
        (unit * 2.0 - 1.0) * amp
    }
}
