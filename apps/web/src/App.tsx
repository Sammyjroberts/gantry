import { useEffect, useMemo, useRef, useState } from "react";
import {
  createLiveClient,
  ValueKind,
  type DeviceChannels,
} from "@gantry/api-client";
import { TimeSeriesStore } from "@gantry/timeseries";
import { resolveBaseUrl, REPLAY_SECONDS, WINDOW_OPTIONS } from "./config";
import { useLiveStream } from "./useLiveStream";
import { useExperiments } from "./useExperiments";
import { ChannelPicker } from "./components/ChannelPicker";
import { StatusBar } from "./components/StatusBar";
import { Chart } from "./components/Chart";
import { ExperimentBar } from "./components/ExperimentBar";
import { formatValue, isBoolKind, isPlottable } from "./valueKind";
import { experimentRegions } from "./experiments";
import {
  INITIAL_ZOOM,
  backToLive,
  panBy,
  resolveWindow,
  setRange,
  zoomAt,
  type Bounds,
  type ZoomState,
} from "./zoom";
import {
  channelKey,
  channelLabel,
  infoKey,
  parseKey,
  subscribeNames,
  type ChannelId,
} from "./channel";

const CURSOR_SYNC_KEY = "gantry-cursor";

// Distinct, colour-blind-friendlyish line colours cycled per channel.
const PALETTE = [
  "#4fd1c5",
  "#e2b93b",
  "#7aa2f7",
  "#e5484d",
  "#a6e22e",
  "#c792ea",
  "#f78c6c",
  "#56d364",
];

interface ChannelMeta {
  unit: string;
  kind: ValueKind;
  device: string;
  packet: string;
  name: string;
}

/** Min/max of a value window, padded, for a per-chart Y-lock snapshot. */
function paddedYRange(y: Float64Array): [number, number] | undefined {
  if (y.length === 0) return undefined;
  let mn = Infinity;
  let mx = -Infinity;
  for (let i = 0; i < y.length; i++) {
    const v = y[i]!;
    if (v < mn) mn = v;
    if (v > mx) mx = v;
  }
  if (!Number.isFinite(mn) || !Number.isFinite(mx)) return undefined;
  if (mn === mx) {
    const p = Math.abs(mn) || 1;
    return [mn - p * 0.1, mx + p * 0.1];
  }
  const pad = (mx - mn) * 0.05;
  return [mn - pad, mx + pad];
}

export function App() {
  const baseUrl = useMemo(resolveBaseUrl, []);

  const storeRef = useRef<TimeSeriesStore | null>(null);
  if (storeRef.current === null) storeRef.current = new TimeSeriesStore(120_000);
  const store = storeRef.current;

  const [devices, setDevices] = useState<DeviceChannels[]>([]);
  const [listError, setListError] = useState<string | null>(null);
  // Selection keyed by (packet, name) — see channel.ts. Name-only keys would
  // collide packet-siblings (imu.temp vs power.temp).
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [windowSec, setWindowSec] = useState<number>(60);
  const [paused, setPaused] = useState(false);
  // Shared x-window state machine (live ↔ inspect); one instance drives every
  // chart so zoom/pan stays time-synchronized across the stack (see zoom.ts).
  const [zoom, setZoom] = useState<ZoomState>(INITIAL_ZOOM);
  // Per-chart Y-lock: key -> frozen [min,max]. Absent = auto-scale.
  const [yLocks, setYLocks] = useState<Map<string, [number, number]>>(new Map());
  const [, setTick] = useState(0);

  const metaByChannel = useMemo(() => {
    const m = new Map<string, ChannelMeta>();
    for (const d of devices) {
      for (const c of d.channels) {
        m.set(infoKey(c), {
          unit: c.unit,
          kind: c.kind,
          device: d.deviceId,
          packet: c.packet,
          name: c.name,
        });
      }
    }
    return m;
  }, [devices]);

  // Label a series with its device only when the catalogue spans >1 device, so
  // multi-device subscriptions stay attributable (frames carry device_id).
  const multiDevice = devices.length > 1;

  // Load the channel catalogue on mount.
  useEffect(() => {
    const client = createLiveClient(baseUrl);
    const ac = new AbortController();
    client
      .listChannels({ deviceId: "" }, { signal: ac.signal })
      .then((res) => setDevices(res.devices))
      .catch((err: unknown) => {
        if (!ac.signal.aborted) {
          setListError(err instanceof Error ? err.message : String(err));
        }
      });
    return () => ac.abort();
  }, [baseUrl]);

  // Selected identities (keys) vs. the distinct channel NAMES sent on the wire.
  // The server routes by name; frames are re-keyed by (packet, name) client-side.
  const selectedList = useMemo(() => [...selected], [selected]);
  const subscribeChannels = useMemo(() => subscribeNames(selected), [selected]);

  const status = useLiveStream({
    baseUrl,
    store,
    deviceId: "",
    channels: subscribeChannels,
    replaySeconds: REPLAY_SECONDS,
  });

  const exp = useExperiments({ baseUrl, deviceId: "", pollMs: 5000 });

  // Chart render tick (halted while paused so the view freezes in place).
  useEffect(() => {
    if (paused) return;
    const id = setInterval(() => setTick((t) => t + 1), 150);
    return () => clearInterval(id);
  }, [paused]);

  const toggle = (id: ChannelId) => {
    const key = channelKey(id.packet, id.name);
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  // Selected keys that get a chart (numeric/bool kinds). Keys are (packet, name).
  const plotChannels = selectedList.filter((key) => {
    const meta = metaByChannel.get(key);
    return meta ? isPlottable(meta.kind) : true; // unknown kind -> assume numeric
  });

  // Buffer extent [oldest, now] in epoch seconds — the clamp for inspect mode.
  // Right edge tracks wall clock so the live window slides without new data.
  const nowSec = Date.now() / 1000;
  let oldestSec = Number.POSITIVE_INFINITY;
  for (const key of plotChannels) {
    const o = store.get(key)?.oldest();
    if (o) oldestSec = Math.min(oldestSec, Number(o.tNs) / 1e9);
  }
  if (!Number.isFinite(oldestSec)) oldestSec = nowSec - windowSec;
  const bounds: Bounds = { oldest: oldestSec, now: nowSec };

  const { range: xRange, clamped } = resolveWindow(zoom, windowSec, bounds);
  const fromNs = BigInt(Math.floor(xRange[0] * 1e9));
  const toNs = BigInt(Math.ceil(xRange[1] * 1e9));

  // Experiment overlay bands. Running runs extend to the live edge (nowSec).
  const regions = useMemo(
    () => experimentRegions(exp.experiments, nowSec),
    // nowSec advances each tick; recompute so the running band grows.
    [exp.experiments, nowSec],
  );

  const inspecting = zoom.mode === "inspect";

  // Zoom actions: pure ops on the shared state (see zoom.ts). Closures capture
  // the current bounds/windowSec — refreshed every render/tick.
  const onZoomAt = (centerSec: number, factor: number) =>
    setZoom((z) => zoomAt(z, windowSec, bounds, centerSec, factor));
  const onZoomRange = (min: number, max: number) =>
    setZoom((z) => setRange(z, bounds, min, max));
  const onPan = (deltaSec: number) =>
    setZoom((z) => panBy(z, windowSec, bounds, deltaSec));
  const onReset = () => setZoom(backToLive());
  const onZoomTo = (startSec: number, endSec: number) =>
    setZoom((z) => setRange(z, bounds, startSec, endSec));

  const toggleYLock = (key: string, y: Float64Array) =>
    setYLocks((prev) => {
      const next = new Map(prev);
      if (next.has(key)) next.delete(key);
      else {
        const r = paddedYRange(y);
        if (r) next.set(key, r);
      }
      return next;
    });

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <span className="brand-mark">▚</span> GANTRY <span className="brand-sub">console</span>
        </div>
        <div className="topbar-controls">
          <label className="ctl">
            window
            <select
              value={windowSec}
              onChange={(e) => setWindowSec(Number(e.target.value))}
            >
              {WINDOW_OPTIONS.map((s) => (
                <option key={s} value={s}>
                  {s < 60 ? `${s}s` : `${s / 60}m`}
                </option>
              ))}
            </select>
          </label>
          <button
            className={`ctl-btn ${paused ? "is-paused" : ""}`}
            onClick={() => setPaused((p) => !p)}
          >
            {paused ? "▶ resume" : "❚❚ pause"}
          </button>
          <span className="baseurl" title="API base URL">
            {baseUrl}
          </span>
        </div>
      </header>

      <ExperimentBar
        experiments={exp.experiments}
        running={exp.running}
        error={exp.error}
        baseUrl={baseUrl}
        exportChannels={subscribeChannels}
        onStart={(name) => void exp.start({ name })}
        onStop={(id) => void exp.stop(id)}
        onUpdate={(id, name, notes) => void exp.update(id, name, notes)}
        onDelete={(id) => void exp.remove(id)}
        onZoomTo={onZoomTo}
      />

      <div className="body">
        <ChannelPicker
          devices={devices}
          selected={selected}
          onToggle={toggle}
          error={listError}
        />

        <main className="charts">
          {inspecting && (
            <div className="inspect-pill">
              <span className="inspect-tag">⏸ inspecting</span>
              {clamped && (
                <span className="inspect-hint" title="Zoomed past the live buffer; deeper history is in CSV export.">
                  history limited to buffer
                </span>
              )}
              <button className="inspect-live" onClick={onReset}>
                ⟳ back to live
              </button>
            </div>
          )}

          {selectedList.length === 0 && (
            <div className="empty-state">
              Select channels from the sidebar to begin streaming.
            </div>
          )}
          {plotChannels.map((key, i) => {
            const meta = metaByChannel.get(key);
            const kind = meta?.kind ?? ValueKind.F64;
            const boolean = isBoolKind(kind);
            const { x, y } = store.window(key, fromNs, toNs);
            const latest = store.get(key)?.latest() ?? null;
            const color = PALETTE[i % PALETTE.length]!;
            // Display: "packet.name" (or bare name for ad-hoc), from the key if
            // the channel is not in the catalogue. Prefix device when the
            // catalogue spans multiple devices.
            const id = meta ?? parseKey(key);
            const title = channelLabel(id.packet, id.name);
            const device = meta?.device ?? "";
            const locked = yLocks.get(key);
            const yRange = boolean ? ([-0.1, 1.1] as [number, number]) : locked;
            return (
              <section className="chart-row" key={key}>
                <div className="chart-header">
                  <span className="chart-title" style={{ color }}>
                    {multiDevice && device && (
                      <span className="chart-device">{device}</span>
                    )}
                    {title}
                  </span>
                  <span className="chart-header-right">
                    {!boolean && (
                      <button
                        className={`ylock ${locked ? "is-locked" : ""}`}
                        title={locked ? "Y locked — click to auto-scale" : "lock Y range"}
                        onClick={() => toggleYLock(key, y)}
                      >
                        {locked ? "🔒 Y" : "🔓 Y"}
                      </button>
                    )}
                    <span className="chart-readout">
                      <span className="readout-val">
                        {formatValue(latest ? latest.value : null, kind)}
                      </span>
                      {meta?.unit && <span className="readout-unit">{meta.unit}</span>}
                    </span>
                  </span>
                </div>
                <Chart
                  data={[x, y]}
                  color={color}
                  height={boolean ? 64 : 160}
                  xRange={xRange}
                  yRange={yRange}
                  stepped={boolean}
                  syncKey={CURSOR_SYNC_KEY}
                  regions={regions}
                  onZoomAt={onZoomAt}
                  onZoomRange={onZoomRange}
                  onPan={onPan}
                  onReset={onReset}
                />
              </section>
            );
          })}
        </main>
      </div>

      <StatusBar
        conn={status.conn}
        fps={status.fps}
        droppedLate={status.droppedLate}
        reconnects={status.reconnects}
        channelCount={selectedList.length}
        lastError={status.lastError}
      />
    </div>
  );
}
