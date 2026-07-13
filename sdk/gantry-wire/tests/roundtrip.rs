//! Exhaustive round-trip + resync tests for the wire codec. Runs on the host with the default
//! `std` feature (decoder available); compiled out when the decoder isn't (`--no-default-features`).

#![cfg(feature = "alloc")]

use gantry_wire::{
    encode_device_info, encode_packet_def, encode_samples_header, encode_stats, encode_time_sync,
    Decoder, FieldDef, Kind, Record, RecordWriter, Value,
};

/// Encode a Samples record for a packet with the given values into a fresh Vec.
fn samples(packet_id: u32, ticks: u64, build: impl FnOnce(&mut RecordWriter)) -> Vec<u8> {
    let mut buf = [0u8; 512];
    let mut w = encode_samples_header(&mut buf, packet_id, ticks);
    build(&mut w);
    let n = w.finish().unwrap();
    buf[..n].to_vec()
}

fn decode_all(stream: &[u8]) -> (Vec<Record>, Decoder) {
    let mut d = Decoder::new();
    let recs = d.push_to_vec(stream);
    (recs, d)
}

#[test]
fn device_info_roundtrip_with_empty_id_and_max_tickhz() {
    let mut buf = [0u8; 128];
    // Empty device_id, and a varint edge (u64::MAX) for tick_hz.
    let n = encode_device_info(&mut buf, "", 0xDEAD_BEEF, u64::MAX).unwrap();
    let (recs, _) = decode_all(&buf[..n]);
    assert_eq!(
        recs,
        vec![Record::DeviceInfo {
            device_id: String::new(),
            session: 0xDEAD_BEEF,
            tick_hz: u64::MAX,
        }]
    );
}

#[test]
fn packet_def_roundtrip_all_kinds_and_empty_unit() {
    let mut buf = [0u8; 256];
    let fields = [
        FieldDef {
            name: "a",
            kind: Kind::F32,
            unit: "deg",
        },
        FieldDef {
            name: "b",
            kind: Kind::F64,
            unit: "",
        }, // empty unit
        FieldDef {
            name: "c",
            kind: Kind::I32,
            unit: "cm",
        },
        FieldDef {
            name: "d",
            kind: Kind::I64,
            unit: "ticks",
        },
        FieldDef {
            name: "e",
            kind: Kind::Bool,
            unit: "",
        },
        FieldDef {
            name: "f",
            kind: Kind::Str,
            unit: "",
        },
        FieldDef {
            name: "g",
            kind: Kind::Bytes,
            unit: "",
        },
    ];
    let n = encode_packet_def(&mut buf, 7, "imu", &fields).unwrap();
    let (recs, _) = decode_all(&buf[..n]);
    match &recs[0] {
        Record::PacketDef {
            packet_id,
            name,
            fields,
        } => {
            assert_eq!(*packet_id, 7);
            assert_eq!(name, "imu");
            assert_eq!(fields.len(), 7);
            assert_eq!(fields[1].unit, "");
            assert_eq!(fields[0].kind, Kind::F32);
            assert_eq!(fields[6].kind, Kind::Bytes);
        }
        other => panic!("expected PacketDef, got {other:?}"),
    }
}

#[test]
fn samples_roundtrip_every_kind_and_int_extremes() {
    // Register a packet with every kind, then a Samples with extreme values.
    let mut def = [0u8; 256];
    let fields = [
        FieldDef {
            name: "f32",
            kind: Kind::F32,
            unit: "",
        },
        FieldDef {
            name: "f64",
            kind: Kind::F64,
            unit: "",
        },
        FieldDef {
            name: "i32",
            kind: Kind::I32,
            unit: "",
        },
        FieldDef {
            name: "i64",
            kind: Kind::I64,
            unit: "",
        },
        FieldDef {
            name: "bool",
            kind: Kind::Bool,
            unit: "",
        },
        FieldDef {
            name: "str",
            kind: Kind::Str,
            unit: "",
        },
        FieldDef {
            name: "bytes",
            kind: Kind::Bytes,
            unit: "",
        },
    ];
    let dn = encode_packet_def(&mut def, 1, "everything", &fields).unwrap();

    let s = samples(1, 0, |w| {
        w.put_f32(1.5);
        w.put_f64(-2.25);
        w.put_zigzag(i32::MIN as i64);
        w.put_zigzag(i64::MAX);
        w.put_bool(true);
        w.put_str("hi");
        w.put_bytes(&[0xDE, 0xAD, 0x00, 0xBE]); // includes a zero byte -> exercises COBS
    });

    let mut stream = def[..dn].to_vec();
    stream.extend_from_slice(&s);
    let (recs, dec) = decode_all(&stream);
    assert_eq!(dec.dropped_samples(), 0);
    assert!(matches!(recs[0], Record::PacketDef { .. }));
    match &recs[1] {
        Record::Samples {
            packet_id,
            ticks,
            values,
        } => {
            assert_eq!(*packet_id, 1);
            assert_eq!(*ticks, 0); // varint 0 edge
            assert_eq!(values[0], Value::F32(1.5));
            assert_eq!(values[1], Value::F64(-2.25));
            assert_eq!(values[2], Value::I32(i32::MIN));
            assert_eq!(values[3], Value::I64(i64::MAX));
            assert_eq!(values[4], Value::Bool(true));
            assert_eq!(values[5], Value::Str("hi".into()));
            assert_eq!(values[6], Value::Bytes(vec![0xDE, 0xAD, 0x00, 0xBE]));
        }
        other => panic!("expected Samples, got {other:?}"),
    }
}

#[test]
fn time_sync_and_stats_roundtrip() {
    let mut a = [0u8; 64];
    let mut b = [0u8; 64];
    let an = encode_time_sync(&mut a, 123_456, 9).unwrap();
    let bn = encode_stats(&mut b, 42, 1000).unwrap();
    let mut stream = a[..an].to_vec();
    stream.extend_from_slice(&b[..bn]);
    let (recs, _) = decode_all(&stream);
    assert_eq!(
        recs,
        vec![
            Record::TimeSync {
                ticks: 123_456,
                seq: 9
            },
            Record::Stats {
                frames_dropped: 42,
                records_sent: 1000
            },
        ]
    );
}

#[test]
fn orphan_samples_without_def_is_dropped_and_counted() {
    // Samples for packet 5 with no PacketDef seen -> dropped, and self-heals once def arrives.
    let orphan = samples(5, 10, |w| w.put_f32(1.0)); // decoder can't know layout; header-only parse
    let mut dec = Decoder::new();
    let recs = dec.push_to_vec(&orphan);
    assert!(recs.is_empty());
    assert_eq!(dec.dropped_samples(), 1);
}

#[test]
fn unknown_record_type_is_skipped_but_surfaced() {
    // Application-private type 0x80: collectors skip unknown types by design.
    let mut buf = [0u8; 64];
    let mut w = RecordWriter::new(&mut buf, 0x80);
    w.put_varint(999);
    w.put_str("whatever");
    let n = w.finish().unwrap();

    let (recs, dec) = decode_all(&buf[..n]);
    assert_eq!(recs, vec![Record::Unknown { type_id: 0x80 }]);
    assert_eq!(dec.unknown_records(), 1);
}

#[test]
fn corrupt_crc_is_detected_and_counted() {
    let mut buf = [0u8; 64];
    let n = encode_time_sync(&mut buf, 7, 1).unwrap();
    let mut frame = buf[..n].to_vec();
    // frame[1] is the (COBS-passthrough) type byte; flipping a high bit changes the CRC'd body
    // without introducing a zero, so it stays one frame that fails the checksum.
    frame[1] ^= 0x80;
    assert_ne!(frame[1], 0);

    let (recs, dec) = decode_all(&frame);
    assert!(recs.is_empty());
    assert_eq!(dec.crc_failures(), 1);
}

#[test]
fn resync_after_garbage_and_torn_frames() {
    let mut good = [0u8; 64];
    let gn = encode_stats(&mut good, 1, 2).unwrap();

    let mut stream = Vec::new();
    stream.extend_from_slice(&[0xFF, 0x10, 0x20]); // garbage run (no delimiter yet)
    stream.push(0x00); // delimiter closes the garbage frame
    stream.extend_from_slice(&[0x00, 0x00]); // stray delimiters -> empty frames, ignored
    stream.extend_from_slice(&good[..gn]); // a clean frame

    let (recs, dec) = decode_all(&stream);
    assert_eq!(
        recs,
        vec![Record::Stats {
            frames_dropped: 1,
            records_sent: 2
        }]
    );
    assert!(
        dec.malformed() >= 1,
        "the garbage frame should be counted malformed"
    );
}

#[test]
fn incremental_single_byte_feeding_matches_bulk() {
    let mut d1 = [0u8; 64];
    let mut d2 = [0u8; 256];
    let n1 = encode_device_info(&mut d1, "dev", 1, 1_000_000).unwrap();
    let fields = [FieldDef {
        name: "x",
        kind: Kind::F32,
        unit: "m",
    }];
    let n2 = encode_packet_def(&mut d2, 1, "p", &fields).unwrap();
    let s = samples(1, 5, |w| w.put_f32(3.0));

    let mut stream = d1[..n1].to_vec();
    stream.extend_from_slice(&d2[..n2]);
    stream.extend_from_slice(&s);

    // Feed one byte at a time.
    let mut dec = Decoder::new();
    let mut recs = Vec::new();
    for &byte in &stream {
        dec.push(&[byte], |r| recs.push(r));
    }
    assert_eq!(recs.len(), 3);
    assert!(matches!(recs[0], Record::DeviceInfo { .. }));
    assert!(matches!(recs[1], Record::PacketDef { .. }));
    assert!(matches!(&recs[2], Record::Samples { values, .. } if values == &vec![Value::F32(3.0)]));
}

#[test]
fn session_change_resets_packet_registry() {
    // Register packet 1 in session A; a Samples decodes. New session B (no def) -> orphan drop.
    let mut info_a = [0u8; 32];
    let ian = encode_device_info(&mut info_a, "d", 1, 1).unwrap();
    let mut def = [0u8; 64];
    let fields = [FieldDef {
        name: "x",
        kind: Kind::I32,
        unit: "",
    }];
    let dn = encode_packet_def(&mut def, 1, "p", &fields).unwrap();
    let s1 = samples(1, 0, |w| w.put_zigzag(5));

    let mut info_b = [0u8; 32];
    let ibn = encode_device_info(&mut info_b, "d", 2, 1).unwrap(); // different session
    let s2 = samples(1, 1, |w| w.put_zigzag(6));

    let mut stream = info_a[..ian].to_vec();
    stream.extend_from_slice(&def[..dn]);
    stream.extend_from_slice(&s1);
    stream.extend_from_slice(&info_b[..ibn]);
    stream.extend_from_slice(&s2);

    let (recs, dec) = decode_all(&stream);
    // DeviceInfo(A), PacketDef, Samples(=5), DeviceInfo(B). The second Samples is now orphaned.
    assert_eq!(recs.len(), 4);
    assert!(matches!(&recs[2], Record::Samples { values, .. } if values == &vec![Value::I32(5)]));
    assert_eq!(dec.dropped_samples(), 1);
}
