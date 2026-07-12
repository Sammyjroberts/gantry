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
}

export function App() {
  const baseUrl = useMemo(resolveBaseUrl, []);

  const storeRef = useRef<TimeSeriesStore | null>(null);
  if (storeRef.current === null) storeRef.current = new TimeSeriesStore(120_000);
  const store = storeRef.current;

  const [devices, setDevices] = useState<DeviceChannels[]>([]);
  const [listError, setListError] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [windowSec, setWindowSec] = useState<number>(60);
  const [paused, setPaused] = useState(false);
  const [, setTick] = useState(0);

  const metaByChannel = useMemo(() => {
    const m = new Map<string, ChannelMeta>();
    for (const d of devices) {
      for (const c of d.channels) {
        m.set(c.name, { unit: c.unit, kind: c.kind, device: d.deviceId });
      }
    }
    return m;
  }, [devices]);

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

  const selectedList = useMemo(() => [...selected], [selected]);

  const status = useLiveStream({
    baseUrl,
    store,
    deviceId: "",
    channels: selectedList,
    replaySeconds: REPLAY_SECONDS,
  });

  // Chart render tick (halted while paused so the view freezes in place).
  useEffect(() => {
    if (paused) return;
    const id = setInterval(() => setTick((t) => t + 1), 150);
    return () => clearInterval(id);
  }, [paused]);

  const toggle = (name: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  };

  // Right edge tracks wall clock so the window slides even without new data.
  const nowNs = BigInt(Date.now()) * 1_000_000n;
  const windowNs = BigInt(windowSec) * 1_000_000_000n;
  const fromNs = nowNs - windowNs;
  const xRange: [number, number] = [Number(fromNs) / 1e9, Number(nowNs) / 1e9];

  const plotChannels = selectedList.filter((c) => {
    const meta = metaByChannel.get(c);
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
          {plotChannels.map((name, i) => {
            const meta = metaByChannel.get(name);
            const kind = meta?.kind ?? ValueKind.F64;
            const boolean = isBoolKind(kind);
            const { x, y } = store.window(name, fromNs, nowNs);
            const latest = store.get(name)?.latest() ?? null;
            const color = PALETTE[i % PALETTE.length]!;
            return (
              <section className="chart-row" key={name}>
                <div className="chart-header">
                  <span className="chart-title" style={{ color }}>
                    {name}
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
