/**
 * Workspace layout envelope — the pure model behind the bench builder.
 *
 * A workspace is a panel grid persisted server-side (WorkspaceService) as an
 * opaque, versioned JSON document in `layout_json`:
 *
 *   {"v":1,"panels":[{id,type,grid:{x,y,w,h},title?,config:{...}}]}
 *
 * This module owns everything framework-free about that document so it can be
 * unit tested in isolation (see layout.test.ts): the envelope encode/decode
 * (tolerant of partial/old shapes), the migration seed that turns a fresh
 * bench's selected channels into a couple of chart panels, panel-binding
 * resolution against the live catalogue (including the explicit "unresolved"
 * state so a panel never silently cross-wires), and the value-at-cursor pick
 * that led/value panels use in both live and replay.
 *
 * A panel binds telemetry by (deviceId, packet, channel); the canonical ring /
 * series key is `channelKey(packet, channel)` — device is metadata (frames
 * carry device_id) but is retained so an unresolved binding can be rebound to a
 * different device without losing intent.
 */

import { channelKey } from "../channel";
import { valueAtOrBefore } from "../pose";

/** The panel types shipped in v1. Extend the union to add a new type. */
export type PanelType =
  | "timeseries"
  | "state"
  | "led"
  | "value"
  | "scene3d"
  | "video"
  | "sql";

export const PANEL_TYPES: readonly PanelType[] = [
  "timeseries",
  "state",
  "led",
  "value",
  "scene3d",
  "video",
  "sql",
];

/** A react-grid-layout cell: column x/y and width/height in grid units. */
export interface GridPos {
  x: number;
  y: number;
  w: number;
  h: number;
}

/**
 * A telemetry binding: (deviceId, packet, channel). The store/series key is
 * `channelKey(packet, channel)`; deviceId is retained so a binding can be
 * attributed and rebound. deviceId "" means "any device that exposes it".
 */
export interface PanelBinding {
  deviceId: string;
  packet: string;
  channel: string;
}

// ---- per-type config (discriminated by Panel.type) ------------------------

export interface TimeseriesConfig {
  channels: PanelBinding[];
  /** Live-follow width override, seconds. Falls back to the shared window. */
  windowSec?: number;
  /** Freeze the Y axis to the data range at lock time. */
  yLock?: boolean;
}
export interface StateConfig {
  channel: PanelBinding | null;
}
export interface LedConfig {
  channel: PanelBinding | null;
  onLabel?: string;
  offLabel?: string;
}
export interface ValueConfig {
  channel: PanelBinding | null;
}
export interface Scene3dConfig {
  deviceId: string;
}
export interface VideoConfig {
  cameraId: string;
}
export interface SqlConfig {
  sql: string;
  /** Auto-refresh cadence in ms; 0/undefined = manual run only. */
  refreshMs?: number;
}

export type PanelConfig =
  | TimeseriesConfig
  | StateConfig
  | LedConfig
  | ValueConfig
  | Scene3dConfig
  | VideoConfig
  | SqlConfig;

export interface Panel {
  id: string;
  type: PanelType;
  /** Custom title; when absent the panel derives one from its binding. */
  title?: string;
  grid: GridPos;
  config: PanelConfig;
}

/** Current layout envelope version. */
export const LAYOUT_VERSION = 1 as const;

export interface LayoutEnvelope {
  v: number;
  panels: Panel[];
}

/** Server-side cap on layout_json (1 MiB). Mirror it so we never send an over. */
export const LAYOUT_JSON_CAP = 1 << 20;

// ---- id + default factory -------------------------------------------------

let idSeq = 0;
/** A short, collision-resistant panel id (time + counter + random tail). */
export function newPanelId(): string {
  idSeq = (idSeq + 1) % 0xffff;
  const rand = Math.floor(Math.random() * 0xffff).toString(16);
  return `p${Date.now().toString(36)}${idSeq.toString(16)}${rand}`;
}

/** The grid footprint (w,h) a freshly-added panel of each type occupies. */
export const DEFAULT_GRID: Record<PanelType, { w: number; h: number }> = {
  timeseries: { w: 6, h: 6 },
  state: { w: 6, h: 3 },
  led: { w: 2, h: 3 },
  value: { w: 3, h: 3 },
  scene3d: { w: 5, h: 8 },
  video: { w: 5, h: 6 },
  sql: { w: 8, h: 7 },
};

/** A fresh, empty config for a panel type. */
export function defaultConfig(type: PanelType): PanelConfig {
  switch (type) {
    case "timeseries":
      return { channels: [] };
    case "state":
      return { channel: null };
    case "led":
      return { channel: null };
    case "value":
      return { channel: null };
    case "scene3d":
      return { deviceId: "" };
    case "video":
      return { cameraId: "" };
    case "sql":
      return { sql: "SELECT 1;" };
  }
}

/** Build a new panel of `type` at grid cell (x,y) with default footprint. */
export function makePanel(type: PanelType, x = 0, y = 0, config?: PanelConfig): Panel {
  const g = DEFAULT_GRID[type];
  return {
    id: newPanelId(),
    type,
    grid: { x, y, w: g.w, h: g.h },
    config: config ?? defaultConfig(type),
  };
}

// ---- serialize / parse (tolerant) -----------------------------------------

/** Serialize a panel list to the versioned layout_json string. */
export function serializeLayout(panels: Panel[]): string {
  const env: LayoutEnvelope = { v: LAYOUT_VERSION, panels };
  return JSON.stringify(env);
}

function asBinding(raw: unknown): PanelBinding | null {
  if (!raw || typeof raw !== "object") return null;
  const r = raw as Record<string, unknown>;
  const channel = typeof r.channel === "string" ? r.channel : "";
  if (!channel) return null;
  return {
    deviceId: typeof r.deviceId === "string" ? r.deviceId : "",
    packet: typeof r.packet === "string" ? r.packet : "",
    channel,
  };
}

function asBindings(raw: unknown): PanelBinding[] {
  if (!Array.isArray(raw)) return [];
  const out: PanelBinding[] = [];
  for (const b of raw) {
    const parsed = asBinding(b);
    if (parsed) out.push(parsed);
  }
  return out;
}

function asGrid(raw: unknown, type: PanelType): GridPos {
  const d = DEFAULT_GRID[type];
  if (!raw || typeof raw !== "object") return { x: 0, y: 0, w: d.w, h: d.h };
  const r = raw as Record<string, unknown>;
  const num = (v: unknown, fallback: number) =>
    typeof v === "number" && Number.isFinite(v) ? v : fallback;
  return {
    x: Math.max(0, num(r.x, 0)),
    y: Math.max(0, num(r.y, 0)),
    w: Math.max(1, num(r.w, d.w)),
    h: Math.max(1, num(r.h, d.h)),
  };
}

function normalizeConfig(type: PanelType, raw: unknown): PanelConfig {
  const r = (raw && typeof raw === "object" ? raw : {}) as Record<string, unknown>;
  switch (type) {
    case "timeseries":
      return {
        channels: asBindings(r.channels),
        windowSec:
          typeof r.windowSec === "number" && r.windowSec > 0 ? r.windowSec : undefined,
        yLock: r.yLock === true,
      };
    case "state":
      return { channel: asBinding(r.channel) };
    case "led":
      return {
        channel: asBinding(r.channel),
        onLabel: typeof r.onLabel === "string" ? r.onLabel : undefined,
        offLabel: typeof r.offLabel === "string" ? r.offLabel : undefined,
      };
    case "value":
      return { channel: asBinding(r.channel) };
    case "scene3d":
      return { deviceId: typeof r.deviceId === "string" ? r.deviceId : "" };
    case "video":
      return { cameraId: typeof r.cameraId === "string" ? r.cameraId : "" };
    case "sql":
      return {
        sql: typeof r.sql === "string" ? r.sql : "SELECT 1;",
        refreshMs:
          typeof r.refreshMs === "number" && r.refreshMs > 0 ? r.refreshMs : undefined,
      };
  }
}

function isPanelType(t: unknown): t is PanelType {
  return typeof t === "string" && (PANEL_TYPES as readonly string[]).includes(t);
}

/**
 * Parse a layout_json document into a clean panel list, tolerating partial /
 * legacy / garbage shapes (returns `[]` rather than throwing). Unknown panel
 * types and panels without an id are dropped; every surviving panel has a valid
 * grid and a normalized config for its type.
 */
export function parseLayout(json: string | null | undefined): Panel[] {
  if (!json) return [];
  let parsed: unknown;
  try {
    parsed = JSON.parse(json);
  } catch {
    return [];
  }
  const env = parsed as { panels?: unknown } | null;
  const rawPanels: unknown[] = env && Array.isArray(env.panels) ? env.panels : [];
  const out: Panel[] = [];
  for (const p of rawPanels) {
    if (!p || typeof p !== "object") continue;
    const rp = p as Record<string, unknown>;
    if (!isPanelType(rp.type)) continue;
    const id = typeof rp.id === "string" && rp.id ? rp.id : newPanelId();
    out.push({
      id,
      type: rp.type,
      title: typeof rp.title === "string" && rp.title ? rp.title : undefined,
      grid: asGrid(rp.grid, rp.type),
      config: normalizeConfig(rp.type, rp.config),
    });
  }
  return out;
}

/** Round-trip helper: `parseLayout(serializeLayout(panels))`. */
export function roundTrip(panels: Panel[]): Panel[] {
  return parseLayout(serializeLayout(panels));
}

// ---- migration seed -------------------------------------------------------

/** A catalogue channel the seed/binding logic reads (device + packet + name). */
export interface SeedChannel {
  deviceId: string;
  packet: string;
  channel: string;
}

/**
 * Seed a fresh bench's default workspace from a set of channel bindings (the
 * spirit of the old fixed layout: a couple of chart panels bound to the
 * operator's selected channels). Splits the bindings across up to two stacked
 * timeseries panels so a wide selection isn't crammed into one chart. An empty
 * selection yields a single empty chart panel to drop channels into.
 */
export function seedDefaultLayout(channels: SeedChannel[]): Panel[] {
  const bindings: PanelBinding[] = channels.map((c) => ({
    deviceId: c.deviceId,
    packet: c.packet,
    channel: c.channel,
  }));
  if (bindings.length === 0) {
    return [makePanel("timeseries", 0, 0)];
  }
  // Up to 6 channels per chart; at most two seeded charts (rest go to chart 2).
  const mid = Math.ceil(Math.min(bindings.length, 12) / 2);
  const first = bindings.slice(0, mid);
  const second = bindings.slice(mid, 12);
  const panels: Panel[] = [];
  panels.push(makePanel("timeseries", 0, 0, { channels: first }));
  if (second.length > 0) {
    panels.push(makePanel("timeseries", 6, 0, { channels: second }));
  }
  return panels;
}

// ---- binding resolution ---------------------------------------------------

export interface ResolvedBinding {
  binding: PanelBinding;
  /** Canonical ring/series key `channelKey(packet, channel)`. */
  key: string;
  /** True when the binding matches a channel in the live catalogue. */
  resolved: boolean;
}

/**
 * A catalogue lookup: the set of `channelKey(packet, channel)` values currently
 * advertised by the bench. Built once per catalogue in the query layer.
 */
export type CatalogIndex = ReadonlySet<string>;

/** Build a {@link CatalogIndex} from catalogue channels. */
export function buildCatalogIndex(channels: SeedChannel[]): Set<string> {
  const s = new Set<string>();
  for (const c of channels) s.add(channelKey(c.packet, c.channel));
  return s;
}

/**
 * Resolve one binding against the catalogue. `resolved` is false when the
 * (packet, channel) pair is absent — the panel then renders an explicit
 * "unresolved binding" state with a rebind affordance rather than silently
 * charting nothing (or, worse, the wrong series).
 */
export function resolveBinding(
  binding: PanelBinding,
  catalog: CatalogIndex,
): ResolvedBinding {
  const key = channelKey(binding.packet, binding.channel);
  return { binding, key, resolved: catalog.has(key) };
}

/** Resolve a list of bindings (timeseries panels bind many channels). */
export function resolveBindings(
  bindings: PanelBinding[],
  catalog: CatalogIndex,
): ResolvedBinding[] {
  return bindings.map((b) => resolveBinding(b, catalog));
}

/** Every distinct binding a panel needs live data for (drives the subscription). */
export function panelBindings(panel: Panel): PanelBinding[] {
  switch (panel.type) {
    case "timeseries":
      return (panel.config as TimeseriesConfig).channels;
    case "state":
    case "value": {
      const c = (panel.config as StateConfig | ValueConfig).channel;
      return c ? [c] : [];
    }
    case "led": {
      const c = (panel.config as LedConfig).channel;
      return c ? [c] : [];
    }
    default:
      return [];
  }
}

/**
 * The union of channel keys every panel in a layout needs, for the live
 * subscription (useLiveStream is driven by this union — the hot-path rule: one
 * subscription for the whole workspace, not one per panel).
 */
export function layoutChannelKeys(panels: Panel[]): string[] {
  const keys = new Set<string>();
  for (const p of panels) {
    for (const b of panelBindings(p)) keys.add(channelKey(b.packet, b.channel));
  }
  return [...keys];
}

// ---- value-at-cursor (led / value panels) ---------------------------------

/**
 * The reading a led/value panel shows: in live mode the latest ring value; in
 * replay the value at (or before) the playback cursor, drawn from the same
 * ring+history seam the charts merge. Pure: the caller supplies `latest` (ring
 * head) and `series` (aligned x/y around the cursor). Mirrors App's sampleRef.
 */
export function readingAt(args: {
  live: boolean;
  latest: number | null;
  cursorSec: number;
  series: { x: ArrayLike<number>; y: ArrayLike<number | null> } | null;
}): number | null {
  if (args.live) return args.latest;
  if (!args.series) return null;
  return valueAtOrBefore(args.series.x, args.series.y, args.cursorSec);
}
