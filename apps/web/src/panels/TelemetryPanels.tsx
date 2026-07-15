import { useState } from "react";
import { ValueKind } from "@gantry/api-client";
import { Chart } from "../components/Chart";
import { channelKey, channelLabel, parseKey } from "../channel";
import { formatValue, isBoolKind } from "../valueKind";
import { useCatalog } from "../query/useCatalog";
import { useWorkspaceData } from "../live/WorkspaceData";
import { useWorkspaceStore } from "../store/workspaceStore";
import { useTimeStore } from "../store/timeStore";
import { bindingLabel, UnresolvedBinding, BindingPicker } from "./common";
import type {
  Panel,
  PanelBinding,
  TimeseriesConfig,
  StateConfig,
  LedConfig,
  ValueConfig,
} from "../workspace/layout";

const CURSOR_SYNC_KEY = "gantry-cursor";
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

/** Shared chart gesture handlers wired to the time store (disabled in replay). */
function useChartHandlers() {
  const replaying = useTimeStore((s) => !!s.replay);
  const zoomAt = useTimeStore((s) => s.zoomAt);
  const setRange = useTimeStore((s) => s.setRange);
  const panBy = useTimeStore((s) => s.panBy);
  const backToLive = useTimeStore((s) => s.backToLive);
  const exitReplay = useTimeStore((s) => s.exitReplay);
  return replaying
    ? { onReset: exitReplay }
    : {
        onZoomAt: zoomAt,
        onZoomRange: setRange,
        onPan: panBy,
        onReset: backToLive,
      };
}

// ---- timeseries -----------------------------------------------------------

export function TimeseriesPanel({ panel }: { panel: Panel }) {
  const config = panel.config as TimeseriesConfig;
  const { catalogIndex, metaByChannel, channelOptions, multiDevice } = useCatalog();
  const data = useWorkspaceData();
  const updateConfig = useWorkspaceStore((s) => s.updateConfig);
  const handlers = useChartHandlers();
  const [yLocks, setYLocks] = useState<Map<string, [number, number]>>(new Map());

  const rebind = (index: number, binding: PanelBinding | null) => {
    const channels = config.channels.slice();
    if (binding) channels[index] = binding;
    else channels.splice(index, 1);
    updateConfig(panel.id, { ...config, channels });
  };

  if (config.channels.length === 0) {
    return (
      <div className="panel-empty">
        no channels — open the gear to add, or select channels in the sidebar and
        "add as chart"
      </div>
    );
  }

  return (
    <div className="panel-charts">
      {config.channels.map((b, i) => {
        const key = channelKey(b.packet, b.channel);
        const resolved = catalogIndex.has(key);
        if (!resolved) {
          return (
            <div className="panel-chart-row" key={`${key}-${i}`}>
              <UnresolvedBinding
                binding={b}
                options={channelOptions}
                onRebind={(nb) => rebind(i, nb)}
              />
            </div>
          );
        }
        const meta = metaByChannel.get(key);
        const kind = meta?.kind ?? ValueKind.F64;
        const boolean = isBoolKind(kind);
        const combined = data.combinedFor(key);
        const id = meta ?? parseKey(key);
        const title = channelLabel(id.packet, id.name);
        const color = PALETTE[i % PALETTE.length]!;
        const locked = yLocks.get(key);
        const yRange = boolean ? ([-0.1, 1.1] as [number, number]) : locked;
        const latest = data.sampleAt(key);
        return (
          <section className="panel-chart-row" key={`${key}-${i}`}>
            <div className="chart-header">
              <span className="chart-title" style={{ color }}>
                {multiDevice && meta?.device && (
                  <span className="chart-device">{meta.device}</span>
                )}
                {title}
                {combined.hasEnvelope && <span className="chart-agg">envelope</span>}
              </span>
              <span className="chart-header-right">
                {!boolean && (
                  <button
                    className={`ylock ${locked ? "is-locked" : ""}`}
                    title={locked ? "Y locked — click to auto-scale" : "lock Y range"}
                    onClick={() =>
                      setYLocks((prev) => {
                        const next = new Map(prev);
                        if (next.has(key)) next.delete(key);
                        else {
                          const r = paddedYRange(combined.line);
                          if (r) next.set(key, r);
                        }
                        return next;
                      })
                    }
                  >
                    {locked ? "🔒 Y" : "🔓 Y"}
                  </button>
                )}
                <span className="chart-readout">
                  <span className="readout-val">{formatValue(latest, kind)}</span>
                  {meta?.unit && <span className="readout-unit">{meta.unit}</span>}
                </span>
              </span>
            </div>
            <div className="chart-canvas-wrap">
              {data.loading && <div className="chart-shimmer" aria-hidden />}
              <Chart
                data={[combined.x, combined.line, combined.low, combined.high]}
                color={color}
                height={boolean ? 84 : 150}
                xRange={data.xRange}
                yRange={yRange}
                stepped={boolean}
                syncKey={CURSOR_SYNC_KEY}
                regions={data.regions}
                cursorSec={data.cursorSec}
                {...handlers}
              />
            </div>
          </section>
        );
      })}
    </div>
  );
}

// ---- state strip ----------------------------------------------------------

export function StatePanel({ panel }: { panel: Panel }) {
  const config = panel.config as StateConfig;
  const { catalogIndex, metaByChannel, channelOptions } = useCatalog();
  const data = useWorkspaceData();
  const updateConfig = useWorkspaceStore((s) => s.updateConfig);
  const handlers = useChartHandlers();
  const b = config.channel;
  const key = b ? channelKey(b.packet, b.channel) : "";
  if (!b || !catalogIndex.has(key)) {
    return (
      <UnresolvedBinding
        binding={b}
        options={channelOptions}
        onRebind={(nb) => updateConfig(panel.id, { channel: nb })}
      />
    );
  }
  const meta = metaByChannel.get(key);
  const combined = data.combinedFor(key);
  const latest = data.sampleAt(key);
  const kind = meta?.kind ?? ValueKind.BOOL;
  return (
    <div className="panel-state">
      <div className="panel-state-head">
        <span>{bindingLabel(b)}</span>
        <span className="readout-val">{formatValue(latest, kind)}</span>
      </div>
      <Chart
        data={[combined.x, combined.line, combined.low, combined.high]}
        color="#7aa2f7"
        height={84}
        xRange={data.xRange}
        yRange={isBoolKind(kind) ? [-0.1, 1.1] : undefined}
        stepped
        syncKey={CURSOR_SYNC_KEY}
        regions={data.regions}
        cursorSec={data.cursorSec}
        {...handlers}
      />
    </div>
  );
}

// ---- LED indicator --------------------------------------------------------

export function LedPanel({ panel }: { panel: Panel }) {
  const config = panel.config as LedConfig;
  const { catalogIndex, channelOptions } = useCatalog();
  const data = useWorkspaceData();
  const updateConfig = useWorkspaceStore((s) => s.updateConfig);
  const b = config.channel;
  const key = b ? channelKey(b.packet, b.channel) : "";
  if (!b || !catalogIndex.has(key)) {
    return (
      <UnresolvedBinding
        binding={b}
        options={channelOptions}
        onRebind={(nb) => updateConfig(panel.id, { ...config, channel: nb })}
      />
    );
  }
  const v = data.sampleAt(key);
  const on = v !== null && v >= 0.5;
  const label = on ? config.onLabel ?? "ON" : config.offLabel ?? "OFF";
  return (
    <div className="panel-led">
      <div className={`led-lamp ${on ? "is-on" : "is-off"} ${v === null ? "is-stale" : ""}`} />
      <div className="led-label">{label}</div>
      <div className="led-channel">{bindingLabel(b)}</div>
    </div>
  );
}

// ---- instantaneous value --------------------------------------------------

export function ValuePanel({ panel }: { panel: Panel }) {
  const config = panel.config as ValueConfig;
  const { catalogIndex, metaByChannel, channelOptions } = useCatalog();
  const data = useWorkspaceData();
  const updateConfig = useWorkspaceStore((s) => s.updateConfig);
  const b = config.channel;
  const key = b ? channelKey(b.packet, b.channel) : "";
  if (!b || !catalogIndex.has(key)) {
    return (
      <UnresolvedBinding
        binding={b}
        options={channelOptions}
        onRebind={(nb) => updateConfig(panel.id, { channel: nb })}
      />
    );
  }
  const meta = metaByChannel.get(key);
  const kind = meta?.kind ?? ValueKind.F64;
  const v = data.sampleAt(key);
  return (
    <div className="panel-value">
      <div className="value-number" data-testid="value-readout">
        {formatValue(v, kind)}
        {meta?.unit && <span className="value-unit">{meta.unit}</span>}
      </div>
      <div className="value-label">{bindingLabel(b)}</div>
      {data.replaying && <div className="value-atcursor">@ cursor</div>}
    </div>
  );
}

// re-export the single-channel picker for config editors
export { BindingPicker };
