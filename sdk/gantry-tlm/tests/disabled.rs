//! When the `enabled` feature is OFF (the default), every public item is an inert stub:
//! `send`/`drain` are no-ops, the derive emits nothing, and `tlm!` does not evaluate its
//! argument. This test only compiles in the disabled configuration and is what `cargo test
//! --workspace` (default features) runs for this crate.

#![cfg(not(feature = "enabled"))]
// In the disabled configuration the derive/sends compile out, so the demo struct's fields and
// the side-effecting fn are deliberately never read — that is what these tests assert.
#![allow(dead_code)]

use std::sync::atomic::{AtomicU32, Ordering};

use gantry_tlm as tlm;
use gantry_tlm::tlm; // brings the `tlm!` macro into scope (macro namespace)

// The derive is still importable and applies with no error — it just expands to nothing.
#[derive(tlm::Telemetry)]
struct Imu {
    pitch: f32,
    gyro_y: f32,
}

fn clock() -> u64 {
    0
}

#[test]
fn disabled_api_is_inert() {
    // init! is a no-op expression.
    tlm::init!(
        bytes = 4096,
        clock = clock,
        tick_hz = 1_000_000,
        device_id = "x",
        session = 1
    );

    tlm::send(&Imu {
        pitch: 1.0,
        gyro_y: 2.0,
    });

    // drain always returns 0 and writes nothing.
    let mut buf = [0xAAu8; 16];
    assert_eq!(tlm::drain(&mut buf), 0);
    assert!(buf.iter().all(|&b| b == 0xAA));
}

#[test]
fn tlm_macro_does_not_evaluate_its_argument() {
    static CALLS: AtomicU32 = AtomicU32::new(0);
    fn side_effect() -> i32 {
        CALLS.fetch_add(1, Ordering::Relaxed);
        7
    }

    tlm!("hack.value", side_effect());

    assert_eq!(
        CALLS.load(Ordering::Relaxed),
        0,
        "disabled tlm! must not evaluate its argument"
    );
}
