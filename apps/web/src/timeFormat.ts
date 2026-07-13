/**
 * Pure time-formatting helpers for the time-range toolbar and range readout.
 * All display is LOCAL timezone; the state machine and RPCs stay in epoch
 * seconds / nanoseconds (see zoom.ts, history.ts). Kept dependency-free so the
 * clock/duration formatting is unit tested directly (see timeFormat.test.ts).
 */

function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

/** `HH:MM:SS` local wall-clock for an epoch-seconds instant. */
export function formatClock(sec: number): string {
  const d = new Date(sec * 1000);
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
}

/**
 * Compact human width, e.g. `500ms`, `10s`, `90s`, `5m`, `1h`, `1h30m`. Used for
 * the range readout's parenthetical and the preset labels. Sub-second widths
 * render in ms; whole minutes/hours drop trailing zero units.
 */
export function formatDurationShort(sec: number): string {
  if (sec < 1) return `${Math.round(sec * 1000)}ms`;
  if (sec < 60) {
    // Keep one decimal only when it is not a whole number of seconds.
    return Number.isInteger(sec) ? `${sec}s` : `${sec.toFixed(1)}s`;
  }
  if (sec < 3600) {
    const m = Math.floor(sec / 60);
    const s = Math.round(sec % 60);
    return s === 0 ? `${m}m` : `${m}m${s}s`;
  }
  const h = Math.floor(sec / 3600);
  const m = Math.round((sec % 3600) / 60);
  return m === 0 ? `${h}h` : `${h}h${m}m`;
}

/**
 * The toolbar range readout: `14:02:10 – 14:07:10 (5m)`. `range` is epoch
 * seconds `[min, max]`.
 */
export function formatRangeLabel(range: [number, number]): string {
  const width = Math.max(0, range[1] - range[0]);
  return `${formatClock(range[0])} – ${formatClock(range[1])} (${formatDurationShort(width)})`;
}

/**
 * Format an epoch-seconds instant as the value an `<input type="datetime-local">`
 * expects: local `YYYY-MM-DDTHH:MM:SS` (no timezone suffix — the control is
 * implicitly local). Seconds precision is included so sub-minute ranges survive
 * an edit round-trip.
 */
export function toDatetimeLocal(sec: number): string {
  const d = new Date(sec * 1000);
  const date = `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
  const time = `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
  return `${date}T${time}`;
}

/**
 * Parse a `datetime-local` string (local tz) back to epoch seconds, or `null`
 * if unparseable. The Date constructor treats a bare `YYYY-MM-DDTHH:MM(:SS)`
 * string as LOCAL time, which is exactly what the control emits.
 */
export function fromDatetimeLocal(value: string): number | null {
  if (!value) return null;
  const ms = new Date(value).getTime();
  if (!Number.isFinite(ms)) return null;
  return ms / 1000;
}
