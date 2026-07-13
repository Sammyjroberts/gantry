//! End-to-end device session simulation. Only compiled with the `enabled` feature.
//!
//! Run with: `cargo test -p gantry-tlm --features enabled`.
//!
//! One `#[test]` fn owns the whole scenario so the process-global pipeline is never touched
//! concurrently (a single test => the test binary runs it alone).

#![cfg(feature = "enabled")]

use std::sync::atomic::{AtomicU64, Ordering};

use gantry_tlm as tlm;
use gantry_tlm::tlm; // brings the `tlm!` macro into scope (macro namespace)
use gantry_wire::{Decoder, Record, Value};

#[derive(tlm::Telemetry)]
struct Imu {
    #[tlm(unit = "deg")]
    pitch: f32,
    #[tlm(unit = "dps")]
    gyro_y: f32,
}

#[derive(tlm::Telemetry)]
#[tlm(packet = "power")]
struct Battery {
    #[tlm(unit = "V")]
    volts: f32,
    #[tlm(name = "temp_c", unit = "degC")]
    temperature: i32,
    charging: bool,
}

/// A controllable test clock (monotonic ticks).
static CLOCK: AtomicU64 = AtomicU64::new(0);
fn clock() -> u64 {
    CLOCK.load(Ordering::Relaxed)
}
fn set_clock(t: u64) {
    CLOCK.store(t, Ordering::Relaxed);
}

const TICK_HZ: u64 = 1_000_000; // microsecond ticks

/// Drain in small chunks and feed the decoder in small chunks, collecting all records.
fn drain_and_decode(dec: &mut Decoder, sink: &mut Vec<Record>) {
    let mut buf = [0u8; 96]; // deliberately small: multiple drain passes
    loop {
        let n = tlm::drain(&mut buf);
        if n == 0 {
            break;
        }
        // Feed the decoder 7 bytes at a time to exercise chunk reassembly.
        for chunk in buf[..n].chunks(7) {
            dec.push(chunk, |r| sink.push(r));
        }
    }
}

#[test]
fn full_session_then_overflow() {
    // ---- Happy path -------------------------------------------------------------------------
    set_clock(0);
    tlm::init!(
        bytes = 4096,
        clock = clock,
        tick_hz = TICK_HZ,
        device_id = "mr-wobbles",
        session = 0xABCD,
    );

    let mut dec = Decoder::new();
    let mut records = Vec::new();

    // Interleave two derived packets and an ad-hoc metric across advancing timestamps.
    for i in 0..5u32 {
        set_clock(100 + i as u64);
        tlm::send(&Imu {
            pitch: i as f32,
            gyro_y: i as f32 * 2.0,
        });

        set_clock(200 + i as u64);
        tlm::send(&Battery {
            volts: 12.0 - i as f32 * 0.1,
            temperature: 20 + i as i32,
            charging: i % 2 == 0,
        });

        set_clock(300 + i as u64);
        tlm!("hack.value", i as i32);
    }

    drain_and_decode(&mut dec, &mut records);

    // DeviceInfo must be first.
    match &records[0] {
        Record::DeviceInfo {
            device_id,
            session,
            tick_hz,
        } => {
            assert_eq!(device_id, "mr-wobbles");
            assert_eq!(*session, 0xABCD);
            assert_eq!(*tick_hz, TICK_HZ);
        }
        other => panic!("expected DeviceInfo first, got {other:?}"),
    }

    // Every packet_id must have its PacketDef appear before its first Samples.
    let mut defined = std::collections::HashSet::new();
    let mut imu_samples = Vec::new();
    let mut power_samples = Vec::new();
    let mut adhoc_samples = Vec::new();
    let mut def_names = std::collections::HashMap::new();
    for rec in &records {
        match rec {
            Record::PacketDef {
                packet_id, name, ..
            } => {
                defined.insert(*packet_id);
                def_names.insert(*packet_id, name.clone());
            }
            Record::Samples {
                packet_id,
                ticks,
                values,
            } => {
                assert!(
                    defined.contains(packet_id),
                    "Samples for packet {packet_id} arrived before its PacketDef"
                );
                match def_names.get(packet_id).map(String::as_str) {
                    Some("imu") => imu_samples.push((*ticks, values.clone())),
                    Some("power") => power_samples.push((*ticks, values.clone())),
                    Some("adhoc") => adhoc_samples.push((*ticks, values.clone())),
                    other => panic!("unexpected packet name {other:?}"),
                }
            }
            _ => {}
        }
    }

    assert_eq!(imu_samples.len(), 5);
    assert_eq!(power_samples.len(), 5);
    assert_eq!(adhoc_samples.len(), 5);

    // Exact field values + timestamps for the derived packets.
    for (i, (ticks, values)) in imu_samples.iter().enumerate() {
        assert_eq!(*ticks, 100 + i as u64);
        assert_eq!(values[0], Value::F32(i as f32));
        assert_eq!(values[1], Value::F32(i as f32 * 2.0));
    }
    for (i, (ticks, values)) in power_samples.iter().enumerate() {
        assert_eq!(*ticks, 200 + i as u64);
        assert_eq!(values[0], Value::F32(12.0 - i as f32 * 0.1));
        // i32 field widens to i64 on the wire (kind I64).
        assert_eq!(values[1], Value::I64(20 + i as i64));
        assert_eq!(values[2], Value::Bool(i % 2 == 0));
    }
    for (i, (ticks, values)) in adhoc_samples.iter().enumerate() {
        assert_eq!(*ticks, 300 + i as u64);
        assert_eq!(values[0], Value::I32(i as i32));
    }

    // Confirm the adhoc PacketDef carries the field name from the macro literal.
    let adhoc_id = def_names
        .iter()
        .find(|(_, n)| n.as_str() == "adhoc")
        .map(|(id, _)| *id)
        .unwrap();
    let adhoc_field = records.iter().find_map(|r| match r {
        Record::PacketDef {
            packet_id, fields, ..
        } if *packet_id == adhoc_id => Some(fields[0].name.clone()),
        _ => None,
    });
    assert_eq!(adhoc_field.as_deref(), Some("hack.value"));

    // No drops on the happy path.
    assert_eq!(tlm::stats().frames_dropped, 0, "no drops with a roomy ring");

    // ---- Deliberate overflow: drop-oldest + Stats counter -----------------------------------
    set_clock(10_000);
    // A tiny ring (second init! call site => its own static storage; generation bump
    // invalidates the earlier packet-id cells so packets re-register cleanly).
    tlm::init!(
        bytes = 256,
        clock = clock,
        tick_hz = TICK_HZ,
        device_id = "tiny",
        session = 0x1,
    );

    // Drain once up front so the decoder learns the DeviceInfo + PacketDef (which will later be
    // evicted from the small ring). The decoder keeps that layout across pushes.
    let mut dec2 = Decoder::new();
    let mut recs2 = Vec::new();
    tlm::send(&Imu {
        pitch: 0.0,
        gyro_y: 0.0,
    });
    drain_and_decode(&mut dec2, &mut recs2);
    assert!(recs2
        .iter()
        .any(|r| matches!(r, Record::PacketDef { name, .. } if name == "imu")));

    // Now flood without draining so the ring must evict oldest whole records.
    for i in 1..200u32 {
        set_clock(10_000 + i as u64);
        tlm::send(&Imu {
            pitch: i as f32,
            gyro_y: 0.0,
        });
    }

    let dropped_before = tlm::stats().frames_dropped;
    assert!(
        dropped_before > 0,
        "flooding a 256-byte ring must drop records"
    );

    // Drain the survivors; they must be the NEWEST samples (drop-oldest). Advance the clock past
    // the Stats cadence (5s) — but stay under the DeviceInfo cadence (10s) so no session churn —
    // so this drain also emits a Stats record carrying the drop counter.
    set_clock(10_000 + 6 * TICK_HZ);
    recs2.clear();
    drain_and_decode(&mut dec2, &mut recs2);

    let survivor_pitches: Vec<f32> = recs2
        .iter()
        .filter_map(|r| match r {
            Record::Samples { values, .. } => match values.first() {
                Some(Value::F32(v)) => Some(*v),
                _ => None,
            },
            _ => None,
        })
        .collect();
    assert!(!survivor_pitches.is_empty(), "some samples must survive");
    // Drop-oldest => survivors are a suffix of 1..200; the last sample (199) must be present,
    // and the earliest survivor must be well past the start.
    assert!(
        survivor_pitches.contains(&199.0),
        "newest sample must survive drop-oldest, got {survivor_pitches:?}"
    );
    assert!(
        survivor_pitches.iter().all(|&p| p > 100.0),
        "old samples must have been evicted, got {survivor_pitches:?}"
    );

    let stats_dropped = recs2
        .iter()
        .find_map(|r| match r {
            Record::Stats { frames_dropped, .. } => Some(*frames_dropped),
            _ => None,
        })
        .expect("a Stats record should be emitted once the cadence elapses");
    assert!(
        stats_dropped >= dropped_before && stats_dropped > 0,
        "Stats must report the (monotonic) drop counter: {stats_dropped} vs {dropped_before}"
    );
}
