/**
 * History layer — pure span/tier/render math for fetch-on-navigate.
 *
 * The live ring buffer (see @gantry/timeseries) only holds the most recent
 * window; anything older must be fetched from QueryService.QueryRange. This
 * module owns the *decisions* around that, with no RPC or React so they are unit
 * tested directly (see history.test.ts):
 *
 * - TIERING: a visible window at a given zoom maps to a resolution tier
 *   (bucket-span seconds on a 1-2-5 ladder). Zooming in shrinks the window,
 *   picks a finer tier, and re-fetches at higher `max_points`. A tier is cached
 *   independently, so panning within a zoom level reuses fetched data and
 *   neighbouring windows share a tier rather than churning.
 * - COVERAGE: given the visible window, what the ring already covers, and what a
 *   tier has already fetched, compute the minimal spans still to fetch (gap
 *   subtraction + coalescing). History is immutable, so nothing is invalidated.
 * - RENDER: a QueryRange series comes back raw XOR bucketed. Raw plots as a
 *   normal line; bucketed plots as a min/max envelope band + mean line. At the
 *   seam with live ring data we prefer the ring (no double-draw).
 *
 * All time is epoch SECONDS (float) to match uPlot's native x unit and the ring
 * window output; the ns<->s conversion happens at the RPC boundary in the hook.
 * (Epoch ns exceeds 2^53, so ns must not be held as a JS number for math.)
 */

import type { ChannelSeries } from "@gantry/api-client";

/** An inclusive time span in epoch seconds, `start <= end`. */
export type Span = [number, number];

/** Finest bucket span we will ever request (1ms), the floor of the tier ladder. */
export const MIN_TIER_SPAN_SEC = 1e-3;
/** Hard cap on points per channel per fetch (mirrors the server cap). */
export const MAX_POINTS_CAP = 5000;

/**
 * Choose a resolution tier for a window: the bucket span (seconds) on a 1-2-5
 * decade ladder that yields ~`targetPoints` buckets across `windowSec`. Returns
 * the smallest ladder value >= `windowSec / targetPoints`, floored at
 * {@link MIN_TIER_SPAN_SEC}. Quantizing to the ladder is what makes the tier
 * stable under small pans/zooms so the cache is reused.
 */
export function tierSpanSec(windowSec: number, targetPoints: number): number {
  const ideal = Math.max(MIN_TIER_SPAN_SEC, windowSec / Math.max(1, targetPoints));
  const pow = Math.floor(Math.log10(ideal));
  const base = Math.pow(10, pow);
  for (const m of [1, 2, 5]) {
    const step = base * m;
    if (step >= ideal - 1e-12) return roundTier(step);
  }
  return roundTier(base * 10);
}

/** Round a ladder value to kill float noise, so it is a stable Map key. */
function roundTier(v: number): number {
  return Number(v.toPrecision(6));
}

/** A stable string key for a resolution tier. */
export function tierKey(spanSec: number): string {
  return roundTier(spanSec).toExponential(4);
}

/**
 * The `max_points_per_channel` to request for a fetch of `spanSec` width at a
 * given tier: enough buckets to hold ~one per tier-span, clamped to the server
 * cap. Never zero.
 */
export function maxPointsForFetch(spanSec: number, tierSpan: number): number {
  const n = Math.ceil(spanSec / Math.max(MIN_TIER_SPAN_SEC, tierSpan));
  return Math.max(1, Math.min(MAX_POINTS_CAP, n));
}

/** Sort + merge overlapping/adjacent spans (adjacency within `tolSec`). */
export function coalesceSpans(spans: Span[], tolSec = 0): Span[] {
  const valid = spans.filter((s) => s[1] > s[0]);
  if (valid.length === 0) return [];
  const sorted = [...valid].sort((a, b) => a[0] - b[0]);
  const out: Span[] = [sorted[0]!.slice() as Span];
  for (let i = 1; i < sorted.length; i++) {
    const cur = sorted[i]!;
    const last = out[out.length - 1]!;
    if (cur[0] <= last[1] + tolSec) {
      last[1] = Math.max(last[1], cur[1]);
    } else {
      out.push(cur.slice() as Span);
    }
  }
  return out;
}

/**
 * The parts of `target` not covered by any span in `covered`. `covered` need
 * not be sorted/disjoint (it is coalesced internally). Returns disjoint gaps in
 * ascending order; empty if fully covered.
 */
export function subtractSpans(target: Span, covered: Span[]): Span[] {
  if (!(target[1] > target[0])) return [];
  const cov = coalesceSpans(covered);
  const gaps: Span[] = [];
  let cursor = target[0];
  for (const c of cov) {
    if (c[1] <= cursor) continue; // entirely before the cursor
    if (c[0] >= target[1]) break; // entirely after the target
    if (c[0] > cursor) gaps.push([cursor, Math.min(c[0], target[1])]);
    cursor = Math.max(cursor, c[1]);
    if (cursor >= target[1]) break;
  }
  if (cursor < target[1]) gaps.push([cursor, target[1]]);
  return gaps;
}

/**
 * The spans still to fetch for a visible window: `window` minus what the ring
 * covers minus what the tier has already cached, dropping slivers narrower than
 * `minSpanSec` (not worth a round-trip). The ring span may be `null` (empty
 * buffer). Result is coalesced.
 */
export function computeFetchSpans(
  window: Span,
  ring: Span | null,
  cached: Span[],
  minSpanSec: number,
): Span[] {
  const covered = ring ? [ring, ...cached] : cached;
  const gaps = subtractSpans(window, covered);
  return coalesceSpans(gaps.filter((g) => g[1] - g[0] >= minSpanSec));
}

/** A normalized cached point: raw points carry min=max=mean=value. */
export interface HistPoint {
  t: number;
  min: number;
  max: number;
  mean: number;
}

/** True if a QueryRange series came back bucketed (envelope), not raw. */
export function seriesIsBucketed(series: ChannelSeries): boolean {
  return series.buckets.length > 0 && series.raw.length === 0;
}

/** Normalize a QueryRange series into sorted {@link HistPoint}s (seconds). */
export function seriesToPoints(series: ChannelSeries): HistPoint[] {
  const pts: HistPoint[] = [];
  if (series.buckets.length > 0) {
    for (const b of series.buckets) {
      const t = Number(b.tNs) / 1e9;
      pts.push({ t, min: b.min, max: b.max, mean: b.mean });
    }
  } else {
    for (const r of series.raw) {
      const t = Number(r.tNs) / 1e9;
      pts.push({ t, min: r.value, max: r.value, mean: r.value });
    }
  }
  pts.sort((a, b) => a.t - b.t);
  return pts;
}

/** A per-channel render payload selected out of the cache for a window. */
export type HistoryRender =
  | { kind: "raw"; x: number[]; y: number[] }
  | { kind: "envelope"; x: number[]; low: number[]; high: number[]; mean: number[] };

interface TierChannel {
  spans: Span[];
  bucketed: boolean;
  points: HistPoint[]; // sorted by t, unique t
}

/**
 * Immutable-history cache, keyed by (resolution tier, channel). Coalesces
 * fetched spans and merges points; never invalidates. Rendering selects the
 * points inside a window and decides raw-vs-envelope from what was fetched.
 */
export class HistoryCache {
  // tierKey -> channelKey -> data
  private readonly tiers = new Map<string, Map<string, TierChannel>>();

  private slot(tier: string, channel: string): TierChannel {
    let byChan = this.tiers.get(tier);
    if (!byChan) {
      byChan = new Map();
      this.tiers.set(tier, byChan);
    }
    let tc = byChan.get(channel);
    if (!tc) {
      tc = { spans: [], bucketed: false, points: [] };
      byChan.set(channel, tc);
    }
    return tc;
  }

  /**
   * Record a fetched span and its (possibly empty) series for a channel at a
   * tier. A span with no returned points still marks coverage, so an empty
   * region is not re-fetched forever.
   */
  insert(
    tierSpan: number,
    channel: string,
    fetchedSpan: Span,
    series: ChannelSeries | null,
  ): void {
    const tc = this.slot(tierKey(tierSpan), channel);
    tc.spans = coalesceSpans([...tc.spans, fetchedSpan]);
    if (series) {
      if (seriesIsBucketed(series)) tc.bucketed = true;
      const merged = mergePoints(tc.points, seriesToPoints(series));
      tc.points = merged;
    }
  }

  /** Spans already fetched for a channel at a tier (coalesced). */
  covered(tierSpan: number, channel: string): Span[] {
    return this.tiers.get(tierKey(tierSpan))?.get(channel)?.spans ?? [];
  }

  /**
   * Select the render payload for a channel at a tier, clipped to `window`.
   * Returns `null` when nothing has been fetched in the window. Bucketed tiers
   * render as an envelope; raw tiers as a line.
   */
  select(tierSpan: number, channel: string, window: Span): HistoryRender | null {
    const tc = this.tiers.get(tierKey(tierSpan))?.get(channel);
    if (!tc || tc.points.length === 0) return null;
    const lo = window[0];
    const hi = window[1];
    // One point of overscan each side so the line reaches the window edges.
    const inRange = tc.points.filter((p) => p.t >= lo && p.t <= hi);
    if (inRange.length === 0) return null;
    if (tc.bucketed) {
      return {
        kind: "envelope",
        x: inRange.map((p) => p.t),
        low: inRange.map((p) => p.min),
        high: inRange.map((p) => p.max),
        mean: inRange.map((p) => p.mean),
      };
    }
    return {
      kind: "raw",
      x: inRange.map((p) => p.t),
      y: inRange.map((p) => p.mean),
    };
  }
}

/** Merge two sorted point lists by time, de-duplicating on `t` (b wins). */
function mergePoints(a: HistPoint[], b: HistPoint[]): HistPoint[] {
  if (a.length === 0) return b;
  if (b.length === 0) return a;
  const out: HistPoint[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    const pa = a[i]!;
    const pb = b[j]!;
    if (pa.t < pb.t) {
      out.push(pa);
      i++;
    } else if (pb.t < pa.t) {
      out.push(pb);
      j++;
    } else {
      out.push(pb); // same timestamp: newer fetch wins
      i++;
      j++;
    }
  }
  while (i < a.length) out.push(a[i++]!);
  while (j < b.length) out.push(b[j++]!);
  return out;
}

/** Ring window data (epoch-seconds x, aligned y), as produced by the store. */
export interface RingData {
  x: ArrayLike<number>;
  y: ArrayLike<number>;
}

/**
 * The combined plot payload handed to a Chart: a single union x timeline with a
 * representative `line` (history mean/value in the old region, ring value in the
 * recent region) and optional envelope `low`/`high` (non-null only in the
 * bucketed old region). `seamSec` is the ring's oldest sample — at or after it
 * the ring is authoritative, so history is dropped there (no double-draw).
 */
export interface CombinedPlot {
  x: number[];
  line: (number | null)[];
  low: (number | null)[];
  high: (number | null)[];
  hasEnvelope: boolean;
}

export function combineSeries(
  ring: RingData,
  hist: HistoryRender | null,
  seamSec: number,
): CombinedPlot {
  const x: number[] = [];
  const line: (number | null)[] = [];
  const low: (number | null)[] = [];
  const high: (number | null)[] = [];

  // Old region: history strictly before the ring seam.
  let hasEnvelope = false;
  if (hist) {
    if (hist.kind === "envelope") {
      for (let i = 0; i < hist.x.length; i++) {
        const t = hist.x[i]!;
        if (t >= seamSec) break;
        x.push(t);
        line.push(hist.mean[i]!);
        low.push(hist.low[i]!);
        high.push(hist.high[i]!);
        hasEnvelope = true;
      }
    } else {
      for (let i = 0; i < hist.x.length; i++) {
        const t = hist.x[i]!;
        if (t >= seamSec) break;
        x.push(t);
        line.push(hist.y[i]!);
        low.push(null);
        high.push(null);
      }
    }
  }

  // Recent region: the ring, authoritative from the seam onward.
  for (let i = 0; i < ring.x.length; i++) {
    const t = ring.x[i]!;
    if (t < seamSec) continue;
    x.push(t);
    line.push(ring.y[i]!);
    low.push(null);
    high.push(null);
  }

  return { x, line, low, high, hasEnvelope };
}
