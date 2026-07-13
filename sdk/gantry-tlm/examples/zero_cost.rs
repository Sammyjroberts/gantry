//! Zero-cost-when-disabled demonstrator.
//!
//! Build it both ways and compare binary size (methodology in the crate report / README):
//!
//! ```text
//! cargo build --release --example zero_cost                     # disabled (default)
//! cargo build --release --example zero_cost --features enabled  # machinery linked in
//! ```
//!
//! With `enabled` OFF, `#[derive(Telemetry)]` expands to nothing, `send`/`tlm!`/`drain`/`init!`
//! are inert inline stubs, and the loop below optimizes away to essentially nothing.

// When built without `enabled`, the derive and sends compile out entirely, so these items are
// intentionally unread — that is exactly the zero-cost property this example demonstrates.
#![allow(dead_code)]

use gantry_tlm as tlm;
use gantry_tlm::tlm; // brings the `tlm!` macro into scope (macro namespace)

#[derive(tlm::Telemetry)]
struct Imu {
    #[tlm(unit = "deg")]
    pitch: f32,
    #[tlm(unit = "dps")]
    gyro_y: f32,
}

// A trivial monotonic clock; real firmware passes a hardware timer read.
fn clock() -> u64 {
    0
}

fn main() {
    tlm::init!(
        bytes = 2048,
        clock = clock,
        tick_hz = 1_000_000,
        device_id = "example",
        session = 1,
    );

    let mut buf = [0u8; 256];
    let mut total = 0usize;
    for i in 0..1000u32 {
        tlm::send(&Imu {
            pitch: i as f32 * 0.1,
            gyro_y: i as f32,
        });
        tlm!("loop.i", i as i32);
        total += tlm::drain(&mut buf);
    }
    // Keep `total` observable so the loop can't be dropped wholesale when enabled.
    std::hint::black_box(total);
}
