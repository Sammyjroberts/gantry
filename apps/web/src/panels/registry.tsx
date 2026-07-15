import type { ComponentType } from "react";
import {
  Activity,
  Box,
  Database,
  Gauge,
  LineChart,
  Lightbulb,
  Video,
  type LucideIcon,
} from "lucide-react";
import type {
  Panel,
  PanelConfig,
  PanelType,
  TimeseriesConfig,
  StateConfig,
  LedConfig,
  ValueConfig,
  Scene3dConfig,
  VideoConfig,
  SqlConfig,
} from "../workspace/layout";
import { useCatalog } from "../query/useCatalog";
import { channelKey } from "../channel";
import { TimeseriesPanel, StatePanel, LedPanel, ValuePanel } from "./TelemetryPanels";
import { Scene3dPanel, VideoPanelPanel, SqlPanel } from "./DockPanels";
import { BindingPicker, bindingFromOption } from "./common";

/**
 * The panel type registry — the single place a panel type is declared.
 *
 * To ADD A NEW PANEL TYPE:
 *   1. add the literal to `PanelType` and a config interface in workspace/layout.ts,
 *      plus a `defaultConfig` case and a `DEFAULT_GRID` footprint;
 *   2. write a Body component ({ panel }) that reads its config + data;
 *   3. (optional) write a ConfigEditor ({ panel, onChange });
 *   4. register the three here. Persistence, resolution, grid, chrome, the
 *      add-menu and the subscription union all pick it up automatically.
 */
export interface PanelDef {
  type: PanelType;
  label: string;
  Icon: LucideIcon;
  Body: ComponentType<{ panel: Panel }>;
  ConfigEditor?: ComponentType<{ panel: Panel; onChange: (config: PanelConfig) => void }>;
}

// ---- config editors -------------------------------------------------------

function TimeseriesEditor({ panel, onChange }: { panel: Panel; onChange: (c: PanelConfig) => void }) {
  const config = panel.config as TimeseriesConfig;
  const { channelOptions } = useCatalog();
  const setChannel = (i: number, key: string) => {
    const opt = channelOptions.find((o) => o.key === key);
    if (!opt) return;
    const channels = config.channels.slice();
    channels[i] = bindingFromOption(opt);
    onChange({ ...config, channels });
  };
  const addChannel = (key: string) => {
    const opt = channelOptions.find((o) => o.key === key);
    if (!opt) return;
    onChange({ ...config, channels: [...config.channels, bindingFromOption(opt)] });
  };
  return (
    <div className="panel-cfg">
      <div className="panel-cfg-label">channels</div>
      {config.channels.map((b, i) => (
        <div className="panel-cfg-row" key={i}>
          <select
            className="panel-cfg-select"
            value={channelKey(b.packet, b.channel)}
            onChange={(e) => setChannel(i, e.target.value)}
          >
            {!channelOptions.some((o) => o.key === channelKey(b.packet, b.channel)) && (
              <option value={channelKey(b.packet, b.channel)}>
                {b.packet ? `${b.packet}.${b.channel}` : b.channel} (unresolved)
              </option>
            )}
            {channelOptions.map((o) => (
              <option key={o.key} value={o.key}>
                {o.label}
              </option>
            ))}
          </select>
          <button
            className="panel-cfg-x"
            onClick={() =>
              onChange({ ...config, channels: config.channels.filter((_, j) => j !== i) })
            }
          >
            ✕
          </button>
        </div>
      ))}
      <select className="panel-cfg-select" value="" onChange={(e) => e.target.value && addChannel(e.target.value)}>
        <option value="">+ add channel…</option>
        {channelOptions.map((o) => (
          <option key={o.key} value={o.key}>
            {o.label}
          </option>
        ))}
      </select>
      <label className="panel-cfg-check">
        <input
          type="checkbox"
          checked={!!config.yLock}
          onChange={(e) => onChange({ ...config, yLock: e.target.checked })}
        />
        default Y-lock
      </label>
    </div>
  );
}


function SingleChannelEditor({ panel, onChange }: { panel: Panel; onChange: (c: PanelConfig) => void }) {
  const config = panel.config as StateConfig | ValueConfig;
  const { channelOptions } = useCatalog();
  return (
    <div className="panel-cfg">
      <div className="panel-cfg-label">channel</div>
      <BindingPicker
        value={config.channel}
        options={channelOptions}
        onChange={(binding) => onChange({ ...config, channel: binding })}
      />
    </div>
  );
}

function LedEditor({ panel, onChange }: { panel: Panel; onChange: (c: PanelConfig) => void }) {
  const config = panel.config as LedConfig;
  const { channelOptions } = useCatalog();
  return (
    <div className="panel-cfg">
      <div className="panel-cfg-label">channel</div>
      <BindingPicker
        value={config.channel}
        options={channelOptions}
        onChange={(binding) => onChange({ ...config, channel: binding })}
      />
      <div className="panel-cfg-row">
        <input
          className="panel-cfg-input"
          placeholder="ON label"
          value={config.onLabel ?? ""}
          onChange={(e) => onChange({ ...config, onLabel: e.target.value || undefined })}
        />
        <input
          className="panel-cfg-input"
          placeholder="OFF label"
          value={config.offLabel ?? ""}
          onChange={(e) => onChange({ ...config, offLabel: e.target.value || undefined })}
        />
      </div>
    </div>
  );
}

function Scene3dEditor({ panel, onChange }: { panel: Panel; onChange: (c: PanelConfig) => void }) {
  const config = panel.config as Scene3dConfig;
  const { deviceIds } = useCatalog();
  return (
    <div className="panel-cfg">
      <div className="panel-cfg-label">device</div>
      <select
        className="panel-cfg-select"
        value={config.deviceId}
        onChange={(e) => onChange({ deviceId: e.target.value })}
      >
        <option value="">(all / first)</option>
        {deviceIds.map((d) => (
          <option key={d} value={d}>
            {d}
          </option>
        ))}
      </select>
    </div>
  );
}

function VideoEditor({ panel, onChange }: { panel: Panel; onChange: (c: PanelConfig) => void }) {
  const config = panel.config as VideoConfig;
  return (
    <div className="panel-cfg">
      <div className="panel-cfg-label">camera id</div>
      <input
        className="panel-cfg-input"
        value={config.cameraId}
        placeholder="bench-cam"
        onChange={(e) => onChange({ cameraId: e.target.value })}
      />
    </div>
  );
}

function SqlEditor({ panel, onChange }: { panel: Panel; onChange: (c: PanelConfig) => void }) {
  const config = panel.config as SqlConfig;
  return (
    <div className="panel-cfg">
      <div className="panel-cfg-label">auto-refresh (ms, 0 = manual)</div>
      <input
        className="panel-cfg-input"
        type="number"
        min={0}
        value={config.refreshMs ?? 0}
        onChange={(e) => {
          const n = Number(e.target.value);
          onChange({ ...config, refreshMs: n > 0 ? n : undefined });
        }}
      />
      <div className="panel-cfg-hint">edit the query directly in the panel</div>
    </div>
  );
}

// ---- the registry ---------------------------------------------------------

export const PANEL_REGISTRY: Record<PanelType, PanelDef> = {
  timeseries: { type: "timeseries", label: "Chart", Icon: LineChart, Body: TimeseriesPanel, ConfigEditor: TimeseriesEditor },
  state: { type: "state", label: "State strip", Icon: Activity, Body: StatePanel, ConfigEditor: SingleChannelEditor },
  led: { type: "led", label: "LED", Icon: Lightbulb, Body: LedPanel, ConfigEditor: LedEditor },
  value: { type: "value", label: "Value", Icon: Gauge, Body: ValuePanel, ConfigEditor: SingleChannelEditor },
  scene3d: { type: "scene3d", label: "3D", Icon: Box, Body: Scene3dPanel, ConfigEditor: Scene3dEditor },
  video: { type: "video", label: "Video", Icon: Video, Body: VideoPanelPanel, ConfigEditor: VideoEditor },
  sql: { type: "sql", label: "SQL", Icon: Database, Body: SqlPanel, ConfigEditor: SqlEditor },
};

/** Ordered list for the add-panel menu. */
export const PANEL_MENU: PanelDef[] = [
  PANEL_REGISTRY.timeseries,
  PANEL_REGISTRY.value,
  PANEL_REGISTRY.led,
  PANEL_REGISTRY.state,
  PANEL_REGISTRY.scene3d,
  PANEL_REGISTRY.video,
  PANEL_REGISTRY.sql,
];
