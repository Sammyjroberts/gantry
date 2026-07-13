import { useEffect, useMemo, useRef, useState } from "react";
import {
  createLiveClient,
  ValueKind,
  type DeviceChannels,
} from "@gantry/api-client";
import { TimeSeriesStore } from "@gantry/timeseries";
import { resolveBaseUrl, REPLAY_SECONDS, WINDOW_OPTIONS } from "./config";
import { useLiveStream } from "./useLiveStream";
import { ChannelPicker } from "./components/ChannelPicker";
import { StatusBar } from "./components/StatusBar";
import { Chart } from "./components/Chart";
import { formatValue, isBoolKind, isPlottable } from "./valueKind";
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

  // Right edge tracks wall clock so the window slides even without new data.
  const nowNs = BigInt(Date.now()) * 1_000_000n;
  const windowNs = BigInt(windowSec) * 1_000_000_000n;
  const fromNs = nowNs - windowNs;
  const xRange: [number, number] = [Number(fromNs) / 1e9, Number(nowNs) / 1e9];

  // Selected keys that get a chart (numeric/bool kinds). Keys are (packet, name).
  const plotChannels = selectedList.filter((key) => {
    const meta = metaByChannel.get(key);
    return meta ? isPlottable(meta.kind) : true; // unknown kind -> assume numeric
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

      <div className="body">
        <ChannelPicker
          devices={devices}
          selected={selected}
          onToggle={toggle}
          error={listError}
        />

        <main className="charts">
          {selectedList.length === 0 && (
            <div className="empty-state">
              Select channels from the sidebar to begin streaming.
            </div>
          )}
          {plotChannels.map((key, i) => {
            const meta = metaByChannel.get(key);
            const kind = meta?.kind ?? ValueKind.F64;
            const boolean = isBoolKind(kind);
            const { x, y } = store.window(key, fromNs, nowNs);
            const latest = store.get(key)?.latest() ?? null;
            const color = PALETTE[i % PALETTE.length]!;
            // Display: "packet.name" (or bare name for ad-hoc), from the key if
            // the channel is not in the catalogue. Prefix device when the
            // catalogue spans multiple devices.
            const id = meta ?? parseKey(key);
            const title = channelLabel(id.packet, id.name);
            const device = meta?.device ?? "";
            return (
              <section className="chart-row" key={key}>
                <div className="chart-header">
                  <span className="chart-title" style={{ color }}>
                    {multiDevice && device && (
                      <span className="chart-device">{device}</span>
                    )}
                    {title}
                  </span>
                  <span className="chart-readout">
                    <span className="readout-val">
                      {formatValue(latest ? latest.value : null, kind)}
                    </span>
                    {meta?.unit && <span className="readout-unit">{meta.unit}</span>}
                  </span>
                </div>
                <Chart
                  data={[x, y]}
                  color={color}
                  height={boolean ? 64 : 160}
                  xRange={xRange}
                  yRange={boolean ? [-0.1, 1.1] : undefined}
                  stepped={boolean}
                  syncKey={CURSOR_SYNC_KEY}
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
