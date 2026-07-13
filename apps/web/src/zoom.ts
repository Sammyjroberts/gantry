/**
 * Pure x-axis window state machine for the charts. This module holds NO uPlot
 * or React references — it is just window math (epoch seconds) so it can be unit
 * tested in isolation from the canvas wiring (see zoom.test.ts). The Chart
 * component translates pointer/wheel gestures into calls here; App owns a single
 * {@link ZoomState} shared across every chart so they stay time-synchronized.
 *
 * TWO MODES:
 * - `live`   — the window tracks the wall-clock right edge: `[now - windowSec, now]`.
 *              It slides forward every render tick; there is no stored range.
 * - `inspect`— the window is a FIXED `[min, max]` detached from the live edge.
 *              Entered by any zoom/pan/drag gesture; left via {@link backToLive}.
 *
 * The visible window is always clamped to the ring buffer's extent
 * `[bounds.oldest, bounds.now]`: you cannot pan into the future, and zooming out
 * past the buffered history clamps to what exists and raises `clamped` (the UI
 * shows a "history limited to buffer" hint — deeper history lives in CSV/export).
 */

export type ZoomMode = "live" | "inspect";

export interface ZoomState {
  mode: ZoomMode;
  /** Inspect window lower edge, epoch seconds. Ignored while `mode === "live"`. */
  min: number;
  /** Inspect window upper edge, epoch seconds. Ignored while `mode === "live"`. */
  max: number;
}

/** The buffered data extent, epoch seconds. Recomputed each render. */
export interface Bounds {
  /** Oldest sample retained in the ring buffer. */
  oldest: number;
  /** Live right edge (wall clock now). */
  now: number;
}

/** A resolved visible window plus whether it was clamped to the buffer extent. */
export interface ResolvedWindow {
  range: [number, number];
  /** True when the request was clamped to `[oldest, now]` (buffer-limited). */
  clamped: boolean;
}

/**
 * Smallest window we allow zooming into, seconds. At 50 Hz this is ~5 samples,
 * which keeps the plot meaningful and avoids a degenerate zero-width scale.
 */
export const MIN_WINDOW_SEC = 0.1;

/** The initial live state. */
export const INITIAL_ZOOM: ZoomState = { mode: "live", min: 0, max: 0 };

/** The live sliding window for a given width, anchored to `now`. */
export function liveWindow(windowSec: number, now: number): [number, number] {
  return [now - windowSec, now];
}

/**
 * Clamp an arbitrary `[min, max]` request to the buffer extent, PRESERVING width
 * where possible (a pan that runs off an edge slides back in rather than
 * shrinking). If the requested width exceeds the whole available span we show
 * everything and flag `clamped`.
 */
export function clampRange(min: number, max: number, bounds: Bounds): ResolvedWindow {
  const lo = bounds.oldest;
  const hi = bounds.now;
  const avail = Math.max(0, hi - lo);
  let width = Math.max(MIN_WINDOW_SEC, max - min);

  // Requested window is wider than everything we hold: show all, flag it.
  if (width >= avail) {
    return { range: [lo, hi], clamped: true };
  }

  let clamped = false;
  // Slide back inside the right edge first (can't see the future)...
  if (max > hi) {
    max = hi;
    min = hi - width;
    clamped = true;
  }
  // ...then the left edge (buffer horizon). Width is guaranteed <= avail here,
  // so fixing the left edge never re-violates the right.
  if (min < lo) {
    min = lo;
    max = lo + width;
    clamped = true;
  }
  return { range: [min, max], clamped };
}

/**
 * Resolve the visible window for the current state. In `live` mode this is the
 * wall-clock sliding window (never marked clamped — the live view is expected to
 * outrun the buffer at long windows). In `inspect` mode the fixed range is
 * clamped to the buffer extent.
 */
export function resolveWindow(
  state: ZoomState,
  windowSec: number,
  bounds: Bounds,
): ResolvedWindow {
  if (state.mode === "live") {
    return { range: liveWindow(windowSec, bounds.now), clamped: false };
  }
  return clampRange(state.min, state.max, bounds);
}

/** The concrete `[min, max]` a state currently occupies, for gesture math. */
function currentRange(state: ZoomState, windowSec: number, bounds: Bounds): [number, number] {
  return state.mode === "live" ? liveWindow(windowSec, bounds.now) : [state.min, state.max];
}

/** Build an inspect state from a range, clamped to the buffer. */
export function inspectFromRange(
  min: number,
  max: number,
  bounds: Bounds,
): ZoomState {
  const { range } = clampRange(min, max, bounds);
  return { mode: "inspect", min: range[0], max: range[1] };
}

/**
 * Zoom about a cursor position. `factor > 1` widens (zoom out), `factor < 1`
 * narrows (zoom in). The point under the cursor (`centerSec`) stays put. Always
 * transitions to (or stays in) inspect mode.
 */
export function zoomAt(
  state: ZoomState,
  windowSec: number,
  bounds: Bounds,
  centerSec: number,
  factor: number,
): ZoomState {
  const [min, max] = currentRange(state, windowSec, bounds);
  const width = max - min;
  if (width <= 0) return inspectFromRange(min, max, bounds);
  const ratio = (centerSec - min) / width; // cursor position within the window
  const newWidth = Math.max(MIN_WINDOW_SEC, width * factor);
  const newMin = centerSec - ratio * newWidth;
  const newMax = centerSec + (1 - ratio) * newWidth;
  return inspectFromRange(newMin, newMax, bounds);
}

/**
 * Pan the window by `deltaSec` (positive = later in time). Transitions to
 * inspect mode; the width is preserved and clamped to the buffer edges.
 */
export function panBy(
  state: ZoomState,
  windowSec: number,
  bounds: Bounds,
  deltaSec: number,
): ZoomState {
  const [min, max] = currentRange(state, windowSec, bounds);
  return inspectFromRange(min + deltaSec, max + deltaSec, bounds);
}

/**
 * Enter inspect mode at an explicit range (drag-box zoom, or "zoom to
 * experiment region" from the list). Degenerate/backwards ranges are ignored.
 */
export function setRange(
  state: ZoomState,
  bounds: Bounds,
  min: number,
  max: number,
): ZoomState {
  if (!(max > min)) return state;
  return inspectFromRange(min, max, bounds);
}

/** Return to the live sliding window (double-click / "back to live" pill). */
export function backToLive(): ZoomState {
  return { mode: "live", min: 0, max: 0 };
}
