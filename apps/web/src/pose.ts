/**
 * Pose bindings — the pure model behind the 3D robot viewer.
 *
 * A device's robot is driven by mapping telemetry CHANNELS (keyed by the same
 * (packet, name) canonical key the charts use — see channel.ts) onto rigid-body
 * pose (pitch/roll/yaw + X/Y/Z offsets) and, for a URDF with joints, onto each
 * joint. Everything here is framework-free and unit-tested (pose.test.ts): the
 * deg/rad/sign/offset transforms, the model-source priority resolver, and the
 * replay cursor value lookup. The React/three imperative drive (Scene3D) reads
 * a `Sampler` — the only impure input — and calls these to compute values.
 *
 * Angles resolve to RADIANS (three.js native). Manual joint values are stored
 * in the joint's native unit (rad for revolute/continuous, m for prismatic).
 */

/** Reads the current numeric value for a channel key, or null if unavailable. */
export type Sampler = (channelKey: string) => number | null;

export type AngleUnit = "deg" | "rad";
export type Sign = 1 | -1;

/** A pitch/roll/yaw axis mapping: a channel, its unit, and a sign flip. */
export interface AngleBinding {
  channelKey: string | null;
  unit: AngleUnit;
  sign: Sign;
}

/** An X/Y/Z offset (metres): a bound channel value, else the manual constant. */
export interface OffsetBinding {
  channelKey: string | null;
  /** Static offset used when `channelKey` is null. */
  manual: number;
}

/** A URDF joint mapping: a channel (unit + sign) or a manual jog value. */
export interface JointBinding {
  mode: "channel" | "manual";
  channelKey: string | null;
  unit: AngleUnit;
  sign: Sign;
  /** Manual jog value in the joint's native unit (rad / m). */
  manual: number;
}

/** Editable dimensions (metres) for the generated primitive robot. */
export interface PrimitiveDims {
  chassisLen: number;
  chassisWidth: number;
  chassisHeight: number;
  wheelRadius: number;
  wheelWidth: number;
  trackWidth: number;
}

export interface PoseBindings {
  pitch: AngleBinding;
  roll: AngleBinding;
  yaw: AngleBinding;
  x: OffsetBinding;
  y: OffsetBinding;
  z: OffsetBinding;
  /** Per-joint bindings, keyed by URDF joint name. */
  joints: Record<string, JointBinding>;
  dims: PrimitiveDims;
}

export const DEG2RAD = Math.PI / 180;
export const RAD2DEG = 180 / Math.PI;

export function deg2rad(d: number): number {
  return d * DEG2RAD;
}
export function rad2deg(r: number): number {
  return r * RAD2DEG;
}

export function defaultAngle(): AngleBinding {
  return { channelKey: null, unit: "deg", sign: 1 };
}
export function defaultOffset(): OffsetBinding {
  return { channelKey: null, manual: 0 };
}
export function defaultJoint(): JointBinding {
  return { mode: "channel", channelKey: null, unit: "rad", sign: 1, manual: 0 };
}
export function defaultDims(): PrimitiveDims {
  return {
    chassisLen: 0.4,
    chassisWidth: 0.28,
    chassisHeight: 0.12,
    wheelRadius: 0.09,
    wheelWidth: 0.04,
    trackWidth: 0.34,
  };
}
export function defaultBindings(): PoseBindings {
  return {
    pitch: defaultAngle(),
    roll: defaultAngle(),
    yaw: defaultAngle(),
    x: defaultOffset(),
    y: defaultOffset(),
    z: defaultOffset(),
    joints: {},
    dims: defaultDims(),
  };
}

/** Resolve an angle binding to RADIANS given a sampler (0 when unbound/absent). */
export function resolveAngle(b: AngleBinding, sample: Sampler): number {
  if (!b.channelKey) return 0;
  const raw = sample(b.channelKey);
  if (raw === null || !Number.isFinite(raw)) return 0;
  const rad = b.unit === "deg" ? deg2rad(raw) : raw;
  return b.sign * rad;
}

/** Resolve an offset binding to metres (channel value, else the manual constant). */
export function resolveOffset(b: OffsetBinding, sample: Sampler): number {
  if (!b.channelKey) return b.manual;
  const raw = sample(b.channelKey);
  if (raw === null || !Number.isFinite(raw)) return b.manual;
  return raw;
}

/** Resolve a joint binding to its native unit value (rad / m). */
export function resolveJoint(b: JointBinding, sample: Sampler): number {
  if (b.mode === "manual") return b.manual;
  if (!b.channelKey) return 0;
  const raw = sample(b.channelKey);
  if (raw === null || !Number.isFinite(raw)) return 0;
  const v = b.unit === "deg" ? deg2rad(raw) : raw;
  return b.sign * v;
}

/** Every channel key referenced by these bindings (for subscription + drive). */
export function boundChannelKeys(b: PoseBindings): string[] {
  const keys = new Set<string>();
  for (const a of [b.pitch, b.roll, b.yaw]) if (a.channelKey) keys.add(a.channelKey);
  for (const o of [b.x, b.y, b.z]) if (o.channelKey) keys.add(o.channelKey);
  for (const j of Object.values(b.joints)) {
    if (j.mode === "channel" && j.channelKey) keys.add(j.channelKey);
  }
  return [...keys];
}

// ---- model-source priority resolution -------------------------------------

export type ModelKind = "urdf" | "glb" | "stl" | "primitive";

export interface ModelSource {
  kind: ModelKind;
  /** File name on the server (absent for the generated primitive fallback). */
  file?: string;
}

/** Extensions probed per device, in priority order. */
const MODEL_EXTS: ReadonlyArray<{ ext: string; kind: ModelKind }> = [
  { ext: "urdf", kind: "urdf" },
  { ext: "glb", kind: "glb" },
  { ext: "stl", kind: "stl" },
];

/**
 * Resolve the best model source for `device` from the server's file list, in
 * priority order (urdf → glb → stl), falling back to the generated primitive.
 * Matching is case-insensitive on `<device>.<ext>`.
 */
export function resolveModelSource(device: string, files: string[]): ModelSource {
  const lower = new Map(files.map((f) => [f.toLowerCase(), f] as const));
  for (const { ext, kind } of MODEL_EXTS) {
    const want = `${device}.${ext}`.toLowerCase();
    const hit = lower.get(want);
    if (hit) return { kind, file: hit };
  }
  return { kind: "primitive" };
}

// ---- replay cursor value lookup -------------------------------------------

/**
 * The value at or before `cursorSec` in an aligned `(x, y)` series, or null if
 * the cursor precedes the first sample / no finite value exists. `x` is sorted
 * ascending epoch seconds; `y` may contain nulls (gaps) which are skipped
 * backwards. This is the pure primitive behind the replay drive: during replay
 * the robot re-enacts the run by reading each bound channel's value at the
 * playback cursor (see playback.ts / Scene3D), mirroring the chart cursor.
 */
export function valueAtOrBefore(
  x: ArrayLike<number>,
  y: ArrayLike<number | null>,
  cursorSec: number,
): number | null {
  const n = x.length;
  if (n === 0) return null;
  // Binary search: largest index i with x[i] <= cursorSec.
  let lo = 0;
  let hi = n - 1;
  let idx = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >>> 1;
    if (x[mid]! <= cursorSec) {
      idx = mid;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  if (idx < 0) return null;
  for (let i = idx; i >= 0; i--) {
    const v = y[i];
    if (v !== null && v !== undefined && Number.isFinite(v)) return v;
  }
  return null;
}

// ---- merge -----------------------------------------------------------------

/**
 * Merge a persisted (possibly partial/old) bindings blob onto fresh defaults.
 * The bindings themselves live server-side now (HardwareService viz_config_json,
 * see hardware.ts) — this merge is the tolerant decoder for whatever shape the
 * server (or a one-time localStorage migration) hands back. There is NO
 * localStorage read/write for bindings in this module: durable viz state roams
 * with the device, never the browser.
 */
export function mergeBindings(raw: unknown): PoseBindings {
  const base = defaultBindings();
  if (!raw || typeof raw !== "object") return base;
  const r = raw as Partial<PoseBindings>;
  const angle = (b: Partial<AngleBinding> | undefined, d: AngleBinding): AngleBinding => ({
    channelKey: b?.channelKey ?? d.channelKey,
    unit: b?.unit === "rad" || b?.unit === "deg" ? b.unit : d.unit,
    sign: b?.sign === -1 ? -1 : 1,
  });
  const offset = (b: Partial<OffsetBinding> | undefined, d: OffsetBinding): OffsetBinding => ({
    channelKey: b?.channelKey ?? d.channelKey,
    manual: typeof b?.manual === "number" ? b.manual : d.manual,
  });
  const joints: Record<string, JointBinding> = {};
  if (r.joints && typeof r.joints === "object") {
    for (const [name, jb] of Object.entries(r.joints)) {
      const j = jb as Partial<JointBinding>;
      joints[name] = {
        mode: j?.mode === "manual" ? "manual" : "channel",
        channelKey: j?.channelKey ?? null,
        unit: j?.unit === "deg" ? "deg" : "rad",
        sign: j?.sign === -1 ? -1 : 1,
        manual: typeof j?.manual === "number" ? j.manual : 0,
      };
    }
  }
  return {
    pitch: angle(r.pitch, base.pitch),
    roll: angle(r.roll, base.roll),
    yaw: angle(r.yaw, base.yaw),
    x: offset(r.x, base.x),
    y: offset(r.y, base.y),
    z: offset(r.z, base.z),
    joints,
    dims: { ...base.dims, ...(r.dims ?? {}) },
  };
}
