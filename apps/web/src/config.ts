/**
 * Resolve the API base URL.
 *
 * Priority: `?api=` query param → `VITE_API_BASE` env → `window.location.origin`.
 * In production the Bench binary serves the bundle, so same-origin (`origin`) is
 * correct. In dev the origin is the Vite server and requests to `/gantry.v1` are
 * proxied to the Bench (see vite.config.ts), so origin also works there.
 */
export function resolveBaseUrl(): string {
  if (typeof window === "undefined") return "http://localhost:4780";
  const q = new URLSearchParams(window.location.search).get("api");
  if (q) return q;
  const env = import.meta.env.VITE_API_BASE as string | undefined;
  if (env && env.length > 0) return env;
  return window.location.origin;
}

/** Seconds of JetStream history to replay on every (re)subscribe. */
export const REPLAY_SECONDS = 30;

/** Selectable visible-window sizes, in seconds. */
export const WINDOW_OPTIONS = [10, 30, 60, 120, 300] as const;

/**
 * Relative time presets for the toolbar (label + width in seconds). Choosing one
 * snaps back to LIVE follow at that width (see zoom.applyPreset). Widths beyond
 * the ring buffer are served by the history layer (QueryRange).
 */
export const TIME_PRESETS: ReadonlyArray<{ label: string; sec: number }> = [
  { label: "10s", sec: 10 },
  { label: "1m", sec: 60 },
  { label: "5m", sec: 300 },
  { label: "15m", sec: 900 },
  { label: "1h", sec: 3600 },
  { label: "6h", sec: 21_600 },
  { label: "24h", sec: 86_400 },
];

/**
 * Target buckets per channel for a history fetch. QueryRange downsamples to at
 * most this many points; it also sets the resolution tier (see history.ts).
 * Matches the server's default max_points_per_channel.
 */
export const HISTORY_TARGET_POINTS = 500;

/** Fraction of an experiment's span to pad each side when fitting the view. */
export const EXPERIMENT_FIT_PAD = 0.05;

/**
 * How far back inspect navigation may reach, seconds. The ring buffer only holds
 * the recent window, but the history layer serves the stream's retention window
 * (~24h) via QueryRange — so the inspect clamp uses this horizon, not the ring's
 * oldest sample. Ranges reaching past actual retention come back flagged
 * `truncated_by_retention` (a hint, not an error).
 */
export const HISTORY_HORIZON_SEC = 24 * 3600;

/**
 * Where the replay playhead sits within the sliding window (fraction from the
 * left). 0.85 keeps most of the swept past visible with a small lead, and the
 * vertical "now" line clearly on-screen near the right.
 */
export const REPLAY_CURSOR_FRAC = 0.85;
