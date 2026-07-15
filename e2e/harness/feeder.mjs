// Node telemetry feeder — the CI-friendly stand-in for the Rust SDK.
//
//   node feeder.mjs <apiBaseURL> [hz]
//
// POSTs Connect-protocol JSON to the Bench IngestService at ~30 Hz. No Rust, no
// proto tooling: the JSON wire shapes are hand-rolled from proto/gantry/v1
// (ingest.proto + telemetry.proto). Connect JSON accepts a plain HTTP POST with
// the Connect-Protocol-Version header (curl-able by design; see ARCHITECTURE.md).
//
// Channels (3 packet-tagged + 1 ad-hoc, matching the spec's "incl. a
// packet-tagged one"):
//   imu.pitch_deg   f64   sine
//   imu.roll_deg    f64   cosine
//   power.voltage   f64   slow drift
//   heartbeat       bool  toggles (ad-hoc: empty packet)
//
// 64-bit fields (fixed64 timestamp_ns, uint64 sequence) are encoded as JSON
// strings — the proto3 JSON canonical form protojson emits and accepts.

const base = (process.argv[2] || "").replace(/\/+$/, "");
const hz = Number(process.argv[3] || 30);
if (!base) {
  console.error("feeder: missing apiBaseURL arg");
  process.exit(1);
}

const DEVICE = "sim-rover";
const HEADERS = {
  "Content-Type": "application/json",
  "Connect-Protocol-Version": "1",
};

async function call(method, body) {
  const res = await fetch(`${base}/gantry.v1.${method}`, {
    method: "POST",
    headers: HEADERS,
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(`${method} -> ${res.status}: ${text}`);
  }
  return res.json().catch(() => ({}));
}

// Register channel metadata up front so the picker shows units/kinds (the ingest
// engine also auto-registers from frames, but this gives nicer labels).
async function registerChannels() {
  await call("IngestService/RegisterChannels", {
    deviceId: DEVICE,
    channels: [
      { name: "pitch_deg", packet: "imu", kind: "VALUE_KIND_F64", unit: "deg" },
      { name: "roll_deg", packet: "imu", kind: "VALUE_KIND_F64", unit: "deg" },
      { name: "voltage", packet: "power", kind: "VALUE_KIND_F64", unit: "V" },
      { name: "heartbeat", packet: "", kind: "VALUE_KIND_BOOL", unit: "" },
    ],
  });
}

function frames(nowNs, t) {
  const ts = String(nowNs);
  return [
    { packet: "imu", channel: "pitch_deg", timestampNs: ts, value: { f64: 10 * Math.sin(t) } },
    { packet: "imu", channel: "roll_deg", timestampNs: ts, value: { f64: 10 * Math.cos(t) } },
    { packet: "power", channel: "voltage", timestampNs: ts, value: { f64: 12 + 0.5 * Math.sin(t / 7) } },
    { packet: "", channel: "heartbeat", timestampNs: ts, value: { flag: Math.floor(t * 2) % 2 === 0 } },
  ];
}

let seq = 1;
let t = 0;
const dt = 1 / hz;
const periodMs = Math.max(1, Math.round(1000 / hz));

async function tick() {
  const nowNs = BigInt(Date.now()) * 1_000_000n;
  try {
    await call("IngestService/PublishBatch", {
      batch: { deviceId: DEVICE, sequence: String(seq), frames: frames(nowNs, t) },
    });
    seq++;
    t += dt;
  } catch (e) {
    // Transient (server starting/stopping) — keep going; the harness owns lifecycle.
    if (seq % 30 === 1) console.error("feeder:", String(e).slice(0, 200));
  }
}

async function main() {
  // Wait for the server, then register, then stream.
  for (let i = 0; i < 100; i++) {
    try {
      await registerChannels();
      break;
    } catch {
      await new Promise((r) => setTimeout(r, 200));
    }
  }
  console.log(`feeder: streaming ${hz}Hz to ${base}`);
  setInterval(tick, periodMs);
}

main();
