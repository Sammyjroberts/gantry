//! Translation-core test driven by a *real* device session synthesized in-process with
//! `gantry-tlm` (the device-side facade). We init the global pipeline, derive packets, send
//! samples, drain the framed bytes, then feed those bytes — in awkward chunk sizes — through the
//! agent's decoder + [`Translator`] with a mock transport and a fake host clock, and assert the
//! resulting `RegisterChannels` / `FrameBatch` content exactly.
//!
//! The whole scenario lives in one `#[test]` fn because `gantry-tlm`'s pipeline is process-global
//! (mirrors `gantry-tlm`'s own integration test). `gantry-tlm` is a dev-dependency with its
//! `enabled` feature on, so the derive/macros expand to the real pipeline.

mod common;

use std::io::Cursor;
use std::sync::atomic::{AtomicU64, Ordering};

use common::MockTransport;
use gantry_connect::{value::Kind as VKind, ValueKind};
use gantry_serial_agent::pipeline::{self, Pace, PipelineConfig};
use gantry_serial_agent::translate::{Config, TimeAnchor, Translator};
use gantry_tlm as tlm;
use gantry_tlm::tlm;
use gantry_wire::Decoder;

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

static CLOCK: AtomicU64 = AtomicU64::new(0);
fn dev_clock() -> u64 {
    CLOCK.load(Ordering::Relaxed)
}
fn set_clock(t: u64) {
    CLOCK.store(t, Ordering::Relaxed);
}

const TICK_HZ: u64 = 1_000_000; // microsecond ticks
/// Fixed host epoch time (ns) returned at the single TimeSync. Chosen so the offset is exactly
/// zero: offset = HOST_NS − ticks_to_ns(TimeSync.ticks=1_000_000) = 1e9 − 1e9 = 0.
/// Mapped epoch(ns) then equals ticks_to_ns(ticks) = ticks * 1000.
const HOST_NS: i128 = 1_000_000_000;

fn drain_to_vec(out: &mut Vec<u8>) {
    let mut buf = [0u8; 96];
    loop {
        let n = tlm::drain(&mut buf);
        if n == 0 {
            break;
        }
        out.extend_from_slice(&buf[..n]);
    }
}

fn as_f64(f: &gantry_connect::Frame) -> f64 {
    match f.value.as_ref().unwrap().kind.as_ref().unwrap() {
        VKind::F64(v) => *v,
        other => panic!("expected f64, got {other:?}"),
    }
}
fn as_i64(f: &gantry_connect::Frame) -> i64 {
    match f.value.as_ref().unwrap().kind.as_ref().unwrap() {
        VKind::I64(v) => *v,
        other => panic!("expected i64, got {other:?}"),
    }
}
fn as_bool(f: &gantry_connect::Frame) -> bool {
    match f.value.as_ref().unwrap().kind.as_ref().unwrap() {
        VKind::Flag(v) => *v,
        other => panic!("expected flag, got {other:?}"),
    }
}

#[test]
fn full_session_translates_to_ingest_calls() {
    // ---- Synthesize a device session (bytes) -------------------------------------------------
    set_clock(0);
    tlm::init!(
        bytes = 8192,
        clock = dev_clock,
        tick_hz = TICK_HZ,
        device_id = "mr-wobbles",
        session = 0xABCD,
    );

    // Samples are sent BEFORE any TimeSync, so the agent must buffer them (pre-sync) and stamp
    // them once the sync lands.
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
    // Cross the 1s boundary and drain: forces exactly one TimeSync(ticks=1_000_000, seq=1) to be
    // appended after the samples.
    set_clock(TICK_HZ);
    let mut bytes = Vec::new();
    drain_to_vec(&mut bytes);

    // ---- Session change: re-init (new session) and re-register ------------------------------
    set_clock(2 * TICK_HZ);
    tlm::init!(
        bytes = 4096,
        clock = dev_clock,
        tick_hz = TICK_HZ,
        device_id = "mr-wobbles",
        session = 0x2222,
    );
    tlm::send(&Imu {
        pitch: 42.0,
        gyro_y: -1.0,
    });
    // Cross another 1s boundary so session 2 also emits a TimeSync and its buffered sample flushes.
    set_clock(3 * TICK_HZ);
    drain_to_vec(&mut bytes);

    // ---- Feed the agent pipeline in awkward chunks ------------------------------------------
    let mock = MockTransport::new();
    let cfg = Config {
        batch_max_frames: 10_000, // one batch per flush; assert exact frames
        anchor: TimeAnchor::Live,
        ..Config::default()
    };
    let mut translator = Translator::new(mock.clone(), Box::new(|| HOST_NS), cfg);
    let mut decoder = Decoder::new();
    let pcfg = PipelineConfig {
        read_chunk: 5, // deliberately tiny: exercise decoder chunk reassembly
        pace: Pace::Max,
        ..PipelineConfig::default()
    };
    pipeline::run(Cursor::new(&bytes), &mut decoder, &mut translator, pcfg).unwrap();

    // ---- Assert registrations ----------------------------------------------------------------
    let regs = mock.registrations();
    // Find the FIRST registration for each packet (session-2 adds a second imu registration).
    let find_reg = |packet: &str| -> Vec<gantry_connect::ChannelInfo> {
        regs.iter()
            .find_map(|(_dev, chans)| {
                if chans.first().map(|c| c.packet.as_str()) == Some(packet) {
                    Some(chans.clone())
                } else {
                    None
                }
            })
            .unwrap_or_else(|| panic!("no registration for packet {packet}"))
    };

    let imu = find_reg("imu");
    assert_eq!(imu.len(), 2);
    assert_eq!(imu[0].name, "pitch");
    assert_eq!(imu[0].packet, "imu");
    assert_eq!(imu[0].kind, ValueKind::F64 as i32);
    assert_eq!(imu[0].unit, "deg");
    assert_eq!(imu[1].name, "gyro_y");
    assert_eq!(imu[1].unit, "dps");

    let power = find_reg("power");
    assert_eq!(power.len(), 3);
    assert_eq!(power[0].name, "volts");
    assert_eq!(power[0].kind, ValueKind::F64 as i32);
    assert_eq!(power[0].unit, "V");
    assert_eq!(power[1].name, "temp_c");
    assert_eq!(power[1].kind, ValueKind::I64 as i32); // i32 field widens to I64
    assert_eq!(power[1].unit, "degC");
    assert_eq!(power[2].name, "charging");
    assert_eq!(power[2].kind, ValueKind::Bool as i32);

    let adhoc = find_reg("adhoc");
    assert_eq!(adhoc.len(), 1);
    assert_eq!(adhoc[0].name, "hack.value");
    assert_eq!(adhoc[0].kind, ValueKind::I64 as i32); // i32 → I64

    // Session change must re-register imu (two imu registrations total).
    let imu_regs = regs
        .iter()
        .filter(|(_d, c)| c.first().map(|c| c.packet.as_str()) == Some("imu"))
        .count();
    assert_eq!(imu_regs, 2, "session change should re-register imu");

    // ---- Assert frames (channel / packet / value / timestamp) --------------------------------
    let imu_frames: Vec<_> = mock
        .all_frames()
        .into_iter()
        .filter(|f| f.packet == "imu" && f.channel == "pitch")
        .collect();
    // 5 from session 1 + 1 from session 2.
    assert_eq!(imu_frames.len(), 6);
    for (i, f) in imu_frames.iter().take(5).enumerate() {
        assert_eq!(f.channel, "pitch");
        assert_eq!(f.packet, "imu");
        assert_eq!(as_f64(f), i as f64);
        // Timestamp: offset 0 ⇒ epoch_ns == ticks_to_ns(100+i) == (100+i)*1000.
        assert_eq!(f.timestamp_ns, (100 + i as u64) * 1_000);
    }

    let power_frames = mock.frames_in_packet("power");
    // 3 fields × 5 samples = 15.
    assert_eq!(power_frames.len(), 15);
    let volts: Vec<_> = power_frames
        .iter()
        .filter(|f| f.channel == "volts")
        .collect();
    for (i, f) in volts.iter().enumerate() {
        assert!((as_f64(f) - (12.0 - i as f64 * 0.1)).abs() < 1e-6);
        assert_eq!(f.timestamp_ns, (200 + i as u64) * 1_000);
    }
    let temp: Vec<_> = power_frames
        .iter()
        .filter(|f| f.channel == "temp_c")
        .collect();
    for (i, f) in temp.iter().enumerate() {
        assert_eq!(as_i64(f), 20 + i as i64);
    }
    let charging: Vec<_> = power_frames
        .iter()
        .filter(|f| f.channel == "charging")
        .collect();
    for (i, f) in charging.iter().enumerate() {
        assert_eq!(as_bool(f), i % 2 == 0);
    }

    let adhoc_frames = mock.frames_in_packet("adhoc");
    assert_eq!(adhoc_frames.len(), 5);
    for (i, f) in adhoc_frames.iter().enumerate() {
        assert_eq!(f.channel, "hack.value");
        assert_eq!(as_i64(f), i as i64);
        assert_eq!(f.timestamp_ns, (300 + i as u64) * 1_000);
    }

    // ---- Batch sequencing + device id --------------------------------------------------------
    let batches = mock.batches();
    assert!(!batches.is_empty());
    for b in &batches {
        assert_eq!(b.device_id, "mr-wobbles");
    }
    // Sequences strictly increasing.
    let seqs: Vec<u64> = batches.iter().map(|b| b.sequence).collect();
    for w in seqs.windows(2) {
        assert!(w[1] > w[0], "batch sequences must increase: {seqs:?}");
    }

    // ---- Agent counters ----------------------------------------------------------------------
    let c = translator.counters();
    assert!(c.frames_sent > 0);
    assert_eq!(c.orphan_frames, 0);
    assert_eq!(c.presync_dropped, 0);
    // A clean session ⇒ no decoder CRC failures.
    assert_eq!(decoder.crc_failures(), 0);
}
