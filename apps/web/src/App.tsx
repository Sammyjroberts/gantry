import { useEffect, useMemo, useRef, useState } from "react";
import {
  createLiveClient,
  ValueKind,
  type DeviceChannels,
} from "@gantry/api-client";
import { TimeSeriesStore } from "@gantry/timeseries";
import {
  resolveBaseUrl,
  REPLAY_SECONDS,
  HISTORY_TARGET_POINTS,
  HISTORY_HORIZON_SEC,
  EXPERIMENT_FIT_PAD,
  REPLAY_CURSOR_FRAC,
} from "./config";
import { useLiveStream } from "./useLiveStream";
import { useExperiments } from "./useExperiments";
import { useHistory } from "./useHistory";
import { ChannelPicker } from "./components/ChannelPicker";
import { StatusBar } from "./components/StatusBar";
import { Chart } from "./components/Chart";
import { ExperimentBar } from "./components/ExperimentBar";
import { TimeRangeBar } from "./components/TimeRangeBar";
import { ReplayBar } from "./components/ReplayBar";
import { formatValue, isBoolKind, isPlottable } from "./valueKind";
import { experimentRegions } from "./experiments";
import { combineSeries, type Span } from "./history";
import {
  cursorAt,
  progress as replayProgress,
  seek as replaySeek,
  setSpeed as replaySetSpeed,
  startReplay,
  togglePlay as replayToggle,
  type PlaybackState,
} from "./playback";
import {
  INITIAL_ZOOM,
  applyPreset,
  backToLive,
  panBy,
  resolveWindow,
  setRange,
  stepBy,
  zoomAt,
  zoomOutBy,
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
function paddedYRange(y: ArrayLike<number | null>): [number, number] | undefined {
  let mn = Infinity;
  let mx = -Infinity;
  let seen = false;
  for (let i = 0; i < y.length; i++) {
    const v = y[i];
    if (v === null || v === undefined || !Number.isFinite(v)) continue;
    seen = true;
    if (v < mn) mn = v;
    if (v > mx) mx = v;
  }
  if (!seen) return undefined;
  if (mn === mx) {
    const p = Math.abs(mn) || 1;
    return [mn - p * 0.1, mx + p * 0.1];
  }
  const pad = (mx - mn) * 0.05;
  return [mn - pad, mx + pad];
}

/** Active replay session: the pure clock plus the experiment it is sweeping. */
interface ReplaySession {
  id: string;
  name: string;
  startSec: number;
  endSec: number;
  clock: PlaybackState;
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
  // Active experiment replay (a moving playhead sweep), or null when not replaying.
  const [replay, setReplay] = useState<ReplaySession | null>(null);
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

  // Chart render tick (150ms). Halted while paused so the live view freezes in
  // place — but replay drives its own sweep, so keep ticking whenever a replay
  // is playing regardless of the live pause.
  const replayPlaying = replay?.clock.playing ?? false;
  useEffect(() => {
    if (paused && !replayPlaying) return;
    const id = setInterval(() => setTick((t) => t + 1), 150);
    return () => clearInterval(id);
  }, [paused, replayPlaying]);

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

  // Inspect clamp extent [oldest, now] in epoch seconds. The right edge tracks
  // wall clock so the live window slides without new data. The left edge is the
  // HISTORY horizon (retention), not the ring's oldest sample: navigation can
  // reach older-than-buffered data because the history layer serves it.
  const nowMs = Date.now();
  const nowSec = nowMs / 1000;
  const bounds: Bounds = { oldest: nowSec - HISTORY_HORIZON_SEC, now: nowSec };

  // Per-channel oldest buffered sample (the ring/history seam).
  const plotKey = plotChannels.join(",");
  const ringOldestByKey = useMemo(() => {
    const m = new Map<string, number | null>();
    for (const key of plotChannels) {
      const o = store.get(key)?.oldest();
      m.set(key, o ? Number(o.tNs) / 1e9 : null);
    }
    return m;
    // Recompute each tick as the buffer fills (nowSec advances every render).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [plotKey, nowSec]);

  // The replay playhead (epoch seconds), sampled from the pure clock this tick.
  const replayCursorSec = replay ? cursorAt(replay.clock, nowMs) : undefined;

  // Resolve the visible window. Replay overrides it with a window sliding under
  // the moving playhead; otherwise it comes from the zoom state machine.
  let xRange: [number, number];
  let clamped = false;
  if (replay && replayCursorSec !== undefined) {
    xRange = [
      replayCursorSec - windowSec * REPLAY_CURSOR_FRAC,
      replayCursorSec + windowSec * (1 - REPLAY_CURSOR_FRAC),
    ];
  } else {
    const resolved = resolveWindow(zoom, windowSec, bounds);
    xRange = resolved.range;
    clamped = resolved.clamped;
  }
  const fromNs = BigInt(Math.floor(xRange[0] * 1e9));
  const toNs = BigInt(Math.ceil(xRange[1] * 1e9));

  // History layer. During replay we prefetch the whole experiment; otherwise we
  // fetch the visible window. Tier resolution follows the visible width.
  const visibleWidthSec = xRange[1] - xRange[0];
  const historyWindow: Span | null =
    plotChannels.length === 0
      ? null
      : replay
        ? [replay.startSec, replay.endSec]
        : [xRange[0], xRange[1]];
  const history = useHistory({
    baseUrl,
    deviceId: "",
    channelNames: subscribeChannels,
    channelKeys: plotChannels,
    window: historyWindow,
    windowSec: replay ? windowSec : visibleWidthSec,
    nowSec,
    ringOldestByKey,
    targetPoints: HISTORY_TARGET_POINTS,
    enabled: plotChannels.length > 0,
  });

  // Experiment overlay bands. Running runs extend to the live edge (nowSec).
  const regions = useMemo(
    () => experimentRegions(exp.experiments, nowSec),
    // nowSec advances each tick; recompute so the running band grows.
    [exp.experiments, nowSec],
  );

  // Zoom actions: pure ops on the shared state (see zoom.ts). Closures capture
  // the current bounds/windowSec — refreshed every render/tick.
  const onZoomAt = (centerSec: number, factor: number) =>
    setZoom((z) => zoomAt(z, windowSec, bounds, centerSec, factor));
  const onZoomRange = (min: number, max: number) =>
    setZoom((z) => setRange(z, bounds, min, max));
  const onPan = (deltaSec: number) =>
    setZoom((z) => panBy(z, windowSec, bounds, deltaSec));
  const onReset = () => setZoom(backToLive());

  // Toolbar actions.
  const onPreset = (sec: number) => {
    const r = applyPreset(sec);
    setWindowSec(r.windowSec);
    setZoom(r.zoom);
  };
  const onStepBack = () => setZoom((z) => stepBy(z, windowSec, bounds, -1));
  const onStepForward = () => setZoom((z) => stepBy(z, windowSec, bounds, 1));
  const onZoomOut = () => setZoom((z) => zoomOutBy(z, windowSec, bounds, 2));
  const onAbsolute = (fromSec: number, toSec: number) =>
    setZoom((z) => setRange(z, bounds, fromSec, toSec));

  // "View entire" (⤢): fit the window to [start, end] with ~5% padding. Uses the
  // history horizon clamp, so runs older than the ring buffer still resolve.
  const onZoomTo = (startSec: number, endSec: number) => {
    const pad = Math.max(0, endSec - startSec) * EXPERIMENT_FIT_PAD;
    setReplay(null);
    setZoom((z) => setRange(z, bounds, startSec - pad, endSec + pad));
  };

  // Replay controls (pure clock transitions; see playback.ts).
  const enterReplay = (id: string, startSec: number, endSec: number) => {
    const name = exp.experiments.find((e) => e.id === id)?.name ?? id;
    setReplay({ id, name, startSec, endSec, clock: startReplay(startSec, endSec, Date.now()) });
  };
  const exitReplay = () => {
    setReplay((r) => {
      if (r) {
        const pad = Math.max(0, r.endSec - r.startSec) * EXPERIMENT_FIT_PAD;
        setZoom((z) => setRange(z, bounds, r.startSec - pad, r.endSec + pad));
      }
      return null;
    });
  };
  const onReplayToggle = () =>
    setReplay((r) => (r ? { ...r, clock: replayToggle(r.clock, Date.now()) } : r));
  const onReplaySeekFraction = (f: number) =>
    setReplay((r) =>
      r ? { ...r, clock: replaySeek(r.clock, r.startSec + f * (r.endSec - r.startSec), Date.now()) } : r,
    );
  const onReplaySetSpeed = (s: number) =>
    setReplay((r) => (r ? { ...r, clock: replaySetSpeed(r.clock, s, Date.now()) } : r));

  const toggleYLock = (key: string, y: ArrayLike<number | null>) =>
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

      <TimeRangeBar
        mode={zoom.mode}
        windowSec={windowSec}
        range={xRange}
        clamped={clamped}
        truncated={history.truncated}
        loading={history.loading}
        onPreset={onPreset}
        onStepBack={onStepBack}
        onStepForward={onStepForward}
        onZoomOut={onZoomOut}
        onAbsolute={onAbsolute}
        onBackToLive={onReset}
      />

      {replay && replayCursorSec !== undefined && (
        <ReplayBar
          name={replay.name}
          startSec={replay.startSec}
          endSec={replay.endSec}
          cursorSec={replayCursorSec}
          playing={replay.clock.playing}
          speed={replay.clock.speed}
          progress={replayProgress(replay.clock, nowMs)}
          loading={history.loading}
          onTogglePlay={onReplayToggle}
          onSeekFraction={onReplaySeekFraction}
          onSetSpeed={onReplaySetSpeed}
          onExit={exitReplay}
        />
      )}

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
        onReplay={enterReplay}
      />

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
            // Ring window (recent) + fetched history (older), merged at the seam
            // (prefer ring where both exist — no double-draw). See history.ts.
            const ring = store.window(key, fromNs, toNs);
            const seamSec = ringOldestByKey.get(key);
            const seam = seamSec == null ? Number.POSITIVE_INFINITY : seamSec;
            const histRender = history.render(key, [xRange[0], xRange[1]]);
            const combined = combineSeries(ring, histRender, seam);
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
                    {combined.hasEnvelope && (
                      <span className="chart-agg" title="Downsampled: min/max envelope + mean line.">
                        envelope
                      </span>
                    )}
                  </span>
                  <span className="chart-header-right">
                    {history.truncated && (
                      <span className="chart-retention" title="Range reaches past stream retention; earlier data is unavailable.">
                        beyond retention
                      </span>
                    )}
                    {!boolean && (
                      <button
                        className={`ylock ${locked ? "is-locked" : ""}`}
                        title={locked ? "Y locked — click to auto-scale" : "lock Y range"}
                        onClick={() => toggleYLock(key, combined.line)}
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
                <div className="chart-canvas-wrap">
                  {history.loading && <div className="chart-shimmer" aria-hidden />}
                  <Chart
                    data={[combined.x, combined.line, combined.low, combined.high]}
                    color={color}
                    height={boolean ? 64 : 160}
                    xRange={xRange}
                    yRange={yRange}
                    stepped={boolean}
                    syncKey={CURSOR_SYNC_KEY}
                    regions={regions}
                    cursorSec={replayCursorSec}
                    onZoomAt={replay ? undefined : onZoomAt}
                    onZoomRange={replay ? undefined : onZoomRange}
                    onPan={replay ? undefined : onPan}
                    onReset={replay ? exitReplay : onReset}
                  />
                </div>
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
