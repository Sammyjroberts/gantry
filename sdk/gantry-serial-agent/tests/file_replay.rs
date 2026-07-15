//! File-replay test: a spool recording *is* a paused stream. We build a wire byte stream, write
//! it to a temp file, then run the exact `--from-file` pipeline (as a lib call) with a fixed
//! epoch anchor and assert the same translation results as the live path.

mod common;

use std::io::Write;

use common::MockTransport;
use gantry_edge::value::Kind as VKind;
use gantry_serial_agent::pipeline::{self, Pace, PipelineConfig};
use gantry_serial_agent::source::open_file;
use gantry_serial_agent::translate::{Config, TimeAnchor, Translator};
use gantry_wire::{
    encode_device_info, encode_packet_def, encode_samples_header, encode_time_sync, FieldDef, Kind,
};

const HZ: u64 = 1_000_000;

/// Build a small but complete device stream into a Vec.
fn build_stream() -> Vec<u8> {
    let mut out = Vec::new();
    let mut scratch = [0u8; 512];

    let n = encode_device_info(&mut scratch, "rocket", 0xBEEF, HZ).unwrap();
    out.extend_from_slice(&scratch[..n]);

    let fields = [
        FieldDef {
            name: "alt_m",
            kind: Kind::F32,
            unit: "m",
        },
        FieldDef {
            name: "stage",
            kind: Kind::I32,
            unit: "",
        },
    ];
    let n = encode_packet_def(&mut scratch, 1, "flight", &fields).unwrap();
    out.extend_from_slice(&scratch[..n]);

    // Two samples BEFORE the first TimeSync (must be buffered, then stamped).
    for (t, alt, stage) in [(100u64, 10.0f32, 1i32), (200, 20.0, 2)] {
        let mut w = encode_samples_header(&mut scratch, 1, t);
        w.put_f32(alt);
        w.put_zigzag(stage as i64);
        let n = w.finish().unwrap();
        out.extend_from_slice(&scratch[..n]);
    }

    // TimeSync at tick 1_000_000.
    let n = encode_time_sync(&mut scratch, 1_000_000, 1).unwrap();
    out.extend_from_slice(&scratch[..n]);

    // One sample AFTER the sync (stamped directly).
    let mut w = encode_samples_header(&mut scratch, 1, 1_000_100);
    w.put_f32(30.0);
    w.put_zigzag(3);
    let n = w.finish().unwrap();
    out.extend_from_slice(&scratch[..n]);

    out
}

fn as_f32ish(f: &gantry_edge::Frame) -> f64 {
    match f.value.as_ref().unwrap().kind.as_ref().unwrap() {
        VKind::F64(v) => *v,
        other => panic!("want f64, got {other:?}"),
    }
}

#[test]
fn from_file_pipeline_translates_with_fixed_anchor() {
    let bytes = build_stream();

    // Write the recording to a temp file.
    let mut tmp = tempfile::NamedTempFile::new().unwrap();
    tmp.write_all(&bytes).unwrap();
    tmp.flush().unwrap();

    // Fixed anchor: pin the first TimeSync (tick 1_000_000 = 1s device time) to epoch 10s.
    // Offset = 10e9 - 1e9 = 9e9. So epoch(ticks) = ticks_to_ns(ticks) + 9e9.
    const ANCHOR_NS: i128 = 10_000_000_000;

    let mock = MockTransport::new();
    let cfg = Config {
        batch_max_frames: 10_000,
        anchor: TimeAnchor::Fixed(ANCHOR_NS),
        ..Config::default()
    };
    // The host clock is irrelevant for a Fixed anchor (never consulted for offset), but supply one.
    let mut translator = Translator::new(mock.clone(), Box::new(|| 0), cfg);
    let mut decoder = gantry_wire::Decoder::new();

    let file = open_file(tmp.path()).unwrap();
    let pcfg = PipelineConfig {
        read_chunk: 8,
        pace: Pace::Max,
        ..PipelineConfig::default()
    };
    pipeline::run(file, &mut decoder, &mut translator, pcfg).unwrap();

    // Registration: one channel per field, packet "flight".
    let regs = mock.registrations();
    let flight = regs
        .iter()
        .find_map(|(_d, c)| {
            if c.first().map(|c| c.packet.as_str()) == Some("flight") {
                Some(c.clone())
            } else {
                None
            }
        })
        .expect("flight registration");
    assert_eq!(flight.len(), 2);
    assert_eq!(flight[0].name, "alt_m");
    assert_eq!(flight[0].unit, "m");
    assert_eq!(flight[1].name, "stage");

    // Frames: alt_m at 10,20,30 with ticks 100,200,1_000_100 → epoch = ticks*1000 + 9e9.
    let alt: Vec<_> = mock
        .all_frames()
        .into_iter()
        .filter(|f| f.channel == "alt_m")
        .collect();
    assert_eq!(alt.len(), 3);
    assert_eq!(as_f32ish(&alt[0]), 10.0);
    assert_eq!(alt[0].timestamp_ns, 100 * 1_000 + 9_000_000_000);
    assert_eq!(as_f32ish(&alt[1]), 20.0);
    assert_eq!(alt[1].timestamp_ns, 200 * 1_000 + 9_000_000_000);
    assert_eq!(as_f32ish(&alt[2]), 30.0);
    assert_eq!(alt[2].timestamp_ns, 1_000_100 * 1_000 + 9_000_000_000);

    // No drops, all three device_id-tagged to "rocket".
    for b in mock.batches() {
        assert_eq!(b.device_id, "rocket");
    }
    assert_eq!(translator.counters().presync_dropped, 0);
    assert_eq!(translator.counters().orphan_frames, 0);
}
