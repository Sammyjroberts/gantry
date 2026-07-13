//! Focused unit tests for the translation core, driving [`Translator::handle`] with hand-built
//! [`Record`]s (no serial, no HTTP, no global pipeline) so each behavior is exercised in isolation:
//! stats forwarding, orphan/pre-sync handling, session-reset re-registration, and idempotent
//! re-definition.

mod common;

use common::MockTransport;
use gantry_connect::{value::Kind as VKind, ValueKind};
use gantry_serial_agent::translate::{Config, DecoderCounters, TimeAnchor, Translator};
use gantry_wire::{Field, Kind, Record, Value as WireValue};

const HZ: u64 = 1_000_000;

fn field(name: &str, kind: Kind, unit: &str) -> Field {
    Field {
        name: name.to_string(),
        kind,
        unit: unit.to_string(),
    }
}

fn mk<T: gantry_connect::Transport>(sink: T, host_ns: i128, anchor: TimeAnchor) -> Translator<T> {
    let cfg = Config {
        batch_max_frames: 10_000,
        anchor,
        ..Config::default()
    };
    Translator::new(sink, Box::new(move || host_ns), cfg)
}

fn i64_of(f: &gantry_connect::Frame) -> i64 {
    match f.value.as_ref().unwrap().kind.as_ref().unwrap() {
        VKind::I64(v) => *v,
        other => panic!("want i64, got {other:?}"),
    }
}

#[test]
fn stats_record_and_agent_counters_forwarded_as_channels() {
    let mock = MockTransport::new();
    let mut t = mk(mock.clone(), 1_000_000_000, TimeAnchor::Live);

    t.handle(Record::DeviceInfo {
        device_id: "rover".into(),
        session: 1,
        tick_hz: HZ,
    });
    // TimeSync so the mapper is ready (offset = 1e9 - ticks_to_ns(1_000_000)=0).
    t.handle(Record::TimeSync {
        ticks: 1_000_000,
        seq: 1,
    });
    // Simulate decoder having seen some bad traffic.
    t.set_decoder_counters(DecoderCounters {
        crc_failures: 3,
        malformed: 1,
        dropped_samples: 2,
        unknown_records: 4,
    });
    // A device Stats record.
    t.handle(Record::Stats {
        frames_dropped: 7,
        records_sent: 100,
    });
    t.flush();

    let stats = mock.frames_in_packet("gantry");
    let get = |name: &str| -> i64 {
        let f = stats
            .iter()
            .find(|f| f.channel == name)
            .unwrap_or_else(|| panic!("missing stats channel {name}"));
        i64_of(f)
    };
    assert_eq!(get("gantry.device.frames_dropped"), 7);
    assert_eq!(get("gantry.device.records_sent"), 100);
    assert_eq!(get("gantry.agent.crc_failures"), 3);
    assert_eq!(get("gantry.agent.malformed_frames"), 1);
    assert_eq!(get("gantry.agent.orphan_samples"), 2);
    assert_eq!(get("gantry.agent.unknown_records"), 4);

    // Stats channels were registered once, under packet "gantry".
    let regs = mock.registrations();
    assert!(regs
        .iter()
        .any(|(_d, c)| c.iter().any(|ci| ci.packet == "gantry"
            && ci.name == "gantry.agent.crc_failures"
            && ci.kind == ValueKind::I64 as i32)));
}

#[test]
fn orphan_samples_are_counted_not_forwarded() {
    let mock = MockTransport::new();
    let mut t = mk(mock.clone(), 0, TimeAnchor::Live);
    t.handle(Record::DeviceInfo {
        device_id: "d".into(),
        session: 1,
        tick_hz: HZ,
    });
    // No PacketDef for id 9 ⇒ orphan.
    t.handle(Record::Samples {
        packet_id: 9,
        ticks: 10,
        values: vec![WireValue::F32(1.0)],
    });
    t.flush();
    assert_eq!(t.counters().orphan_frames, 1);
    assert!(mock.all_frames().is_empty());
}

#[test]
fn presync_buffers_then_flushes_and_drops_oldest() {
    let mock = MockTransport::new();
    let cfg = Config {
        batch_max_frames: 10_000,
        presync_capacity: 3,
        anchor: TimeAnchor::Live,
        ..Config::default()
    };
    let mut t = Translator::new(mock.clone(), Box::new(|| 5_000_000_000), cfg);

    t.handle(Record::DeviceInfo {
        device_id: "d".into(),
        session: 1,
        tick_hz: HZ,
    });
    t.handle(Record::PacketDef {
        packet_id: 1,
        name: "p".into(),
        fields: vec![field("x", Kind::I32, "")],
    });
    // 5 samples before any TimeSync; capacity 3 ⇒ 2 dropped (oldest).
    for i in 0..5i64 {
        t.handle(Record::Samples {
            packet_id: 1,
            ticks: (i as u64 + 1) * 10,
            values: vec![WireValue::I32(i as i32)],
        });
    }
    assert!(
        mock.all_frames().is_empty(),
        "nothing ships before first sync"
    );
    assert_eq!(t.counters().presync_dropped, 2);

    // First TimeSync at tick 1_000_000: offset = 5e9 - 1e9 = 4e9.
    t.handle(Record::TimeSync {
        ticks: 1_000_000,
        seq: 1,
    });
    t.flush();

    let frames = mock.frames_in_packet("p");
    // The 3 survivors are the newest: i = 2,3,4.
    assert_eq!(frames.len(), 3);
    let vals: Vec<i64> = frames.iter().map(i64_of).collect();
    assert_eq!(vals, vec![2, 3, 4]);
    // Timestamp for i=4 (ticks=50): ticks_to_ns(50)=50_000, + offset 4e9 = 4_000_050_000.
    assert_eq!(frames[2].timestamp_ns, 4_000_050_000);
}

#[test]
fn session_change_resets_and_reregisters() {
    let mock = MockTransport::new();
    let mut t = mk(mock.clone(), 0, TimeAnchor::Live);

    let imu = vec![field("pitch", Kind::F32, "deg")];

    t.handle(Record::DeviceInfo {
        device_id: "d".into(),
        session: 1,
        tick_hz: HZ,
    });
    t.handle(Record::PacketDef {
        packet_id: 1,
        name: "imu".into(),
        fields: imu.clone(),
    });
    // Same def again in the same session ⇒ idempotent (no second register).
    t.handle(Record::PacketDef {
        packet_id: 1,
        name: "imu".into(),
        fields: imu.clone(),
    });
    assert_eq!(mock.registrations().len(), 1, "re-def must be idempotent");

    // New session ⇒ reset; the same def must register again.
    t.handle(Record::DeviceInfo {
        device_id: "d".into(),
        session: 2,
        tick_hz: HZ,
    });
    t.handle(Record::PacketDef {
        packet_id: 1,
        name: "imu".into(),
        fields: imu,
    });
    assert_eq!(
        mock.registrations().len(),
        2,
        "session change must re-register"
    );
}

#[test]
fn changed_definition_reregisters_same_session() {
    let mock = MockTransport::new();
    let mut t = mk(mock.clone(), 0, TimeAnchor::Live);
    t.handle(Record::DeviceInfo {
        device_id: "d".into(),
        session: 1,
        tick_hz: HZ,
    });
    t.handle(Record::PacketDef {
        packet_id: 1,
        name: "imu".into(),
        fields: vec![field("pitch", Kind::F32, "deg")],
    });
    // Different unit ⇒ a real change ⇒ re-register.
    t.handle(Record::PacketDef {
        packet_id: 1,
        name: "imu".into(),
        fields: vec![field("pitch", Kind::F32, "rad")],
    });
    assert_eq!(mock.registrations().len(), 2);
}
