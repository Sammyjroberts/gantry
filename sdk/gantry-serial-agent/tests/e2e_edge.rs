//! End-to-end test against a real Edge binary. `#[ignore]` by default (it builds Go and starts a
//! server); run explicitly:
//!
//! ```text
//! cargo test -p gantry-serial-agent --test e2e_edge -- --ignored --nocapture
//! ```
//!
//! It builds `apps/edge`, starts it on a random port with a temp data dir, replays a synthesized
//! spool file through the agent's real `--from-file` pipeline against the live ingest endpoint,
//! then hits `LiveService/ListChannels` over plain HTTP/1.1 JSON to assert the channels + packets
//! arrived.

use std::io::Write;
use std::net::TcpListener;
use std::path::PathBuf;
use std::process::{Child, Command};
use std::time::{Duration, Instant};

use gantry_serial_agent::pipeline::{self, Pace, PipelineConfig};
use gantry_serial_agent::translate::{Config, TimeAnchor, Translator};
use gantry_transport_http::HttpTransport;
use gantry_wire::{
    encode_device_info, encode_packet_def, encode_samples_header, encode_time_sync, Decoder,
    FieldDef, Kind,
};

const HZ: u64 = 1_000_000;

/// Kills the Edge child on drop so a panicking assertion never leaks the process.
struct EdgeGuard(Child);
impl Drop for EdgeGuard {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

fn repo_root() -> PathBuf {
    // .../sdk/gantry-serial-agent → repo root.
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .parent()
        .unwrap()
        .to_path_buf()
}

fn free_port() -> u16 {
    TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}

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
    let n = encode_time_sync(&mut scratch, 1_000_000, 1).unwrap();
    out.extend_from_slice(&scratch[..n]);
    for (t, alt, stage) in [(1_000_100u64, 10.0f32, 1i32), (1_000_200, 20.0, 2)] {
        let mut w = encode_samples_header(&mut scratch, 1, t);
        w.put_f32(alt);
        w.put_zigzag(stage as i64);
        let n = w.finish().unwrap();
        out.extend_from_slice(&scratch[..n]);
    }
    out
}

#[test]
#[ignore = "builds Go + starts a server; run explicitly with --ignored"]
fn e2e_replay_to_real_edge() {
    let root = repo_root();
    let tmp = tempfile::tempdir().unwrap();
    let edge_bin = tmp.path().join("edge.exe");

    // 1. Build the Edge binary.
    let status = Command::new("go")
        .args(["build", "-o"])
        .arg(&edge_bin)
        .arg("./apps/edge/cmd/edge")
        .current_dir(&root)
        .status()
        .expect("run `go build` (is Go on PATH?)");
    assert!(status.success(), "go build edge failed");

    // 2. Start Edge on a random port with a temp data dir.
    let port = free_port();
    let data_dir = tmp.path().join("data");
    let child = Command::new(&edge_bin)
        .arg("--port")
        .arg(port.to_string())
        .arg("--data-dir")
        .arg(&data_dir)
        .spawn()
        .expect("spawn edge");
    let _guard = EdgeGuard(child);
    let base = format!("http://127.0.0.1:{port}");

    // 3. Wait for readiness (a GET on "/" returns *something* once serving).
    let deadline = Instant::now() + Duration::from_secs(20);
    loop {
        if ureq::get(&base)
            .timeout(Duration::from_millis(500))
            .call()
            .is_ok()
        {
            break;
        }
        assert!(Instant::now() < deadline, "edge did not become ready");
        std::thread::sleep(Duration::from_millis(150));
    }

    // 4. Write a synthesized spool file and replay it through the real agent pipeline.
    let bytes = build_stream();
    let mut spool = tempfile::NamedTempFile::new().unwrap();
    spool.write_all(&bytes).unwrap();
    spool.flush().unwrap();

    let transport = HttpTransport::new(base.clone());
    let cfg = Config {
        anchor: TimeAnchor::Fixed(10_000_000_000),
        ..Config::default()
    };
    let mut translator = Translator::new(transport, Box::new(|| 0), cfg);
    let mut decoder = Decoder::new();
    let file = std::fs::File::open(spool.path()).unwrap();
    pipeline::run(
        file,
        &mut decoder,
        &mut translator,
        PipelineConfig {
            read_chunk: 8,
            pace: Pace::Max,
            ..PipelineConfig::default()
        },
    )
    .unwrap();
    let c = translator.counters();
    assert!(c.frames_sent >= 2, "frames were not published: {c:?}");
    assert_eq!(c.batches_dropped, 0, "batches were dropped: {c:?}");

    // 5. ListChannels over plain HTTP/1.1 JSON (Connect protocol) and assert.
    let resp = ureq::post(&format!("{base}/gantry.v1.LiveService/ListChannels"))
        .set("Content-Type", "application/json")
        .set("Connect-Protocol-Version", "1")
        .timeout(Duration::from_secs(5))
        .send_string(r#"{"deviceId":"rocket"}"#)
        .expect("ListChannels call")
        .into_string()
        .unwrap();

    let json: serde_json::Value = serde_json::from_str(&resp).expect("parse ListChannels JSON");
    let devices = json["devices"].as_array().expect("devices array");
    let rocket = devices
        .iter()
        .find(|d| d["deviceId"] == "rocket")
        .expect("device rocket present");
    let channels = rocket["channels"].as_array().expect("channels array");

    let find = |name: &str| {
        channels
            .iter()
            .find(|c| c["name"] == name)
            .unwrap_or_else(|| panic!("channel {name} missing; got {resp}"))
    };
    let alt = find("alt_m");
    assert_eq!(alt["packet"], "flight", "alt_m packet round-trip");
    assert_eq!(alt["kind"], "VALUE_KIND_F64", "alt_m kind");
    assert_eq!(alt["unit"], "m", "alt_m unit");
    let stage = find("stage");
    assert_eq!(stage["packet"], "flight", "stage packet round-trip");
    assert_eq!(stage["kind"], "VALUE_KIND_I64", "stage kind (i32→I64)");

    println!("e2e OK: channels {channels:?}");
}
