//! Full-loopback integration test (POSIX only).
//!
//! Stands up the splitter's pump over a PTY pair that plays the part of the real serial device (we
//! have no hardware in CI), plus the real host-facing PTY the "lerobot master" opens. A fake
//! lerobot writes `SYNC_READ(Present_Position)` requests; a fake servo answers with status packets.
//! We assert:
//!   * bytes pass through **byte-identical** in both directions;
//!   * the decoder publishes the expected `pos` frames to a mock transport;
//!   * the pump adds **< 1 ms per transaction** in this environment.
//!
//! Everything here is `cfg(unix)` — on Windows the file compiles to nothing and the test is skipped.

#![cfg(unix)]

use std::fs::OpenOptions;
use std::io::{Read, Write};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use gantry_edge::{value, Frame, FrameBatch, Transport, TransportError};
use gantry_splitter::calibration::Normalizer;
use gantry_splitter::decoder::{checksum, Direction};
use gantry_splitter::pty;
use gantry_splitter::publish::Publisher;
use gantry_splitter::pump::{pump_direction, EofPolicy, PortProcessor};
use gantry_splitter::sink::{PortSink, Role, SharedLeader};

const INSTR_SYNC_READ: u8 = 0x82;
const PRESENT_POSITION: u8 = 56;

// --- a recording mock transport -------------------------------------------------------------

#[derive(Clone, Default)]
struct Mock {
    batches: Arc<Mutex<Vec<FrameBatch>>>,
}
impl Transport for Mock {
    fn publish(&self, batch: FrameBatch) -> Result<u64, TransportError> {
        let seq = batch.sequence;
        self.batches.lock().unwrap().push(batch);
        Ok(seq)
    }
}
impl Mock {
    fn frames(&self) -> Vec<Frame> {
        self.batches
            .lock()
            .unwrap()
            .iter()
            .flat_map(|b| b.frames.clone())
            .collect()
    }
}

// --- Feetech packet builders ----------------------------------------------------------------

fn sync_read_request(addr: u8, size: u8, ids: &[u8]) -> Vec<u8> {
    let mut params = vec![addr, size];
    params.extend_from_slice(ids);
    let len = (params.len() + 2) as u8;
    let mut body = vec![0xFE, len, INSTR_SYNC_READ];
    body.extend_from_slice(&params);
    let ck = checksum(&body);
    let mut pkt = vec![0xFF, 0xFF];
    pkt.extend_from_slice(&body);
    pkt.push(ck);
    pkt
}

fn status(id: u8, data: &[u8]) -> Vec<u8> {
    let len = (data.len() + 2) as u8;
    let mut body = vec![id, len, 0u8]; // err = 0
    body.extend_from_slice(data);
    let ck = checksum(&body);
    let mut pkt = vec![0xFF, 0xFF];
    pkt.extend_from_slice(&body);
    pkt.push(ck);
    pkt
}

#[test]
fn loopback_passthrough_decode_and_latency() {
    const IDS: [u8; 6] = [1, 2, 3, 4, 5, 6];
    const N: usize = 200;

    // The two PTY pairs. `dev` stands in for the real serial device; `host` is what lerobot opens.
    let dev = pty::create().expect("dev pty");
    let host = pty::create().expect("host pty");

    // Fake endpoints (plain files on the slave paths). The pty was set raw by `pty::create`, so
    // these carry binary byte-for-byte.
    let mut servo = OpenOptions::new()
        .read(true)
        .write(true)
        .open(&dev.slave_path)
        .expect("open servo slave");
    let mut lerobot = OpenOptions::new()
        .read(true)
        .write(true)
        .open(&host.slave_path)
        .expect("open lerobot slave");

    // Pump wiring: publish decoded readings to a mock transport.
    let mock = Mock::default();
    let publisher = Publisher::start(mock.clone(), "so101-follower", vec![]);
    let sink = PortSink::new(
        Role::Follower,
        Arc::clone(&publisher),
        Normalizer::Raw,
        SharedLeader::new(),
        false,
    );
    let processor = PortProcessor::new(sink);

    let dev_master = dev.master;
    let host_master = host.master;
    let dev_r = dev_master.try_clone().unwrap();
    let dev_w = dev_master.try_clone().unwrap();
    let host_r = host_master.try_clone().unwrap();
    let host_w = host_master.try_clone().unwrap();

    // device -> host (status replies): read the fake device, forward to the host pty, decode.
    let proc_dh = Arc::clone(&processor);
    std::thread::spawn(move || {
        let _ = pump_direction(
            dev_r,
            host_w,
            Direction::DeviceToHost,
            proc_dh,
            EofPolicy::Tolerate,
        );
    });
    // host -> device (instructions): read the host pty, forward to the fake device, decode.
    let proc_hd = Arc::clone(&processor);
    std::thread::spawn(move || {
        let _ = pump_direction(
            host_r,
            dev_w,
            Direction::HostToDevice,
            proc_hd,
            EofPolicy::Tolerate,
        );
    });

    // The fake servo: read each full request, then answer with six status packets.
    let request = sync_read_request(PRESENT_POSITION, 2, &IDS);
    let req_len = request.len();
    let responses: Vec<Vec<u8>> = IDS
        .iter()
        .map(|&id| {
            let raw: u16 = 2048 + id as u16 * 10; // distinct, decodable value per joint
            status(id, &raw.to_le_bytes())
        })
        .collect();
    let response_blob: Vec<u8> = responses.concat();

    let servo_seen = Arc::new(Mutex::new(Vec::<u8>::new()));
    let servo_seen_t = Arc::clone(&servo_seen);
    let response_blob_t = response_blob.clone();
    let servo_thread = std::thread::spawn(move || {
        for _ in 0..N {
            let mut buf = vec![0u8; req_len];
            if servo.read_exact(&mut buf).is_err() {
                return;
            }
            servo_seen_t.lock().unwrap().extend_from_slice(&buf);
            if servo.write_all(&response_blob_t).is_err() {
                return;
            }
            servo.flush().ok();
        }
    });

    // The fake lerobot: fire N transactions, timing each round trip.
    let mut lat = Vec::with_capacity(N);
    let mut lerobot_seen = Vec::new();
    for _ in 0..N {
        let t0 = Instant::now();
        lerobot.write_all(&request).unwrap();
        lerobot.flush().unwrap();
        let mut resp = vec![0u8; response_blob.len()];
        lerobot.read_exact(&mut resp).unwrap();
        lat.push(t0.elapsed());
        lerobot_seen.extend_from_slice(&resp);
    }
    servo_thread.join().unwrap();

    // Byte-identical passthrough, both directions.
    let expected_requests: Vec<u8> = request.iter().cloned().cycle().take(req_len * N).collect();
    assert_eq!(
        *servo_seen.lock().unwrap(),
        expected_requests,
        "servo must receive the request bytes unmodified"
    );
    let expected_responses: Vec<u8> = response_blob
        .iter()
        .cloned()
        .cycle()
        .take(response_blob.len() * N)
        .collect();
    assert_eq!(
        lerobot_seen, expected_responses,
        "lerobot must receive the response bytes unmodified"
    );

    // Let the last decode land, then flush the publisher synchronously.
    std::thread::sleep(Duration::from_millis(50));
    publisher.shutdown();

    // Decoded pos frames were published (six joints, raw→deg via the Raw normalizer).
    let frames = mock.frames();
    let pos: Vec<_> = frames.iter().filter(|f| f.channel == "pos").collect();
    assert!(
        pos.len() >= 6,
        "expected at least 6 pos frames, got {}",
        pos.len()
    );
    for joint in [
        "shoulder_pan",
        "shoulder_lift",
        "elbow_flex",
        "wrist_flex",
        "wrist_roll",
        "gripper",
    ] {
        assert!(
            pos.iter().any(|f| f.packet == joint),
            "missing pos frames for joint {joint}"
        );
    }
    // Spot-check a value: servo id 1 sent raw 2058 → (2058-2048)*360/4096 ≈ 0.879°.
    let sp = pos
        .iter()
        .find(|f| f.packet == "shoulder_pan")
        .expect("shoulder_pan pos");
    if let Some(value::Kind::F64(v)) = sp.value.as_ref().and_then(|v| v.kind.clone()) {
        let expected = (2058.0 - 2048.0) * 360.0 / 4096.0;
        assert!(
            (v - expected).abs() < 1e-6,
            "shoulder_pan pos {v} != {expected}"
        );
    } else {
        panic!("expected f64 pos value");
    }

    // Latency: the pump must not add artificial buffering. Assert mean round-trip < 1 ms.
    let total: Duration = lat.iter().sum();
    let mean = total / (lat.len() as u32);
    let mut sorted = lat.clone();
    sorted.sort();
    let median = sorted[sorted.len() / 2];
    let max = *sorted.last().unwrap();
    println!(
        "loopback latency over {N} transactions: mean={:?} median={:?} max={:?}",
        mean, median, max
    );
    assert!(
        mean < Duration::from_millis(1),
        "pump added too much latency: mean per-transaction round trip = {mean:?} (>= 1ms)"
    );
}
