import { describe, it, expect } from "vitest";
import type { ChannelSeries } from "@gantry/api-client";
import {
  HistoryCache,
  coalesceSpans,
  combineSeries,
  computeFetchSpans,
  maxPointsForFetch,
  seriesIsBucketed,
  subtractSpans,
  tierKey,
  tierSpanSec,
} from "./history";

// Minimal ChannelSeries builders. The history code only reads raw/buckets/tNs,
// so plain objects cast to the generated type are sufficient for these tests.
function rawSeries(pts: Array<[number, number]>): ChannelSeries {
  return {
    raw: pts.map(([t, value]) => ({ tNs: BigInt(Math.round(t * 1e9)), value, text: "" })),
    buckets: [],
  } as unknown as ChannelSeries;
}
function bucketSeries(pts: Array<[number, number, number, number]>): ChannelSeries {
  return {
    raw: [],
    buckets: pts.map(([t, min, max, mean]) => ({
      tNs: BigInt(Math.round(t * 1e9)),
      min,
      max,
      mean,
      count: 1,
    })),
  } as unknown as ChannelSeries;
}

describe("tierSpanSec", () => {
  it("targets ~targetPoints buckets across the window (1-2-5 ladder)", () => {
    // 500s / 500pts = 1s ideal -> ladder 1s.
    expect(tierSpanSec(500, 500)).toBe(1);
    // 5000s / 500 = 10s ideal -> ladder 10s.
    expect(tierSpanSec(5000, 500)).toBe(10);
    // 300s / 500 = 0.6 -> ladder 1s (next step up).
    expect(tierSpanSec(300, 500)).toBe(1);
    // 1400s / 500 = 2.8 -> ladder 5s.
    expect(tierSpanSec(1400, 500)).toBe(5);
  });
  it("shrinks the tier as you zoom in (finer resolution)", () => {
    const wide = tierSpanSec(3600, 500);
    const narrow = tierSpanSec(60, 500);
    expect(narrow).toBeLessThan(wide);
  });
  it("floors at the min tier span", () => {
    expect(tierSpanSec(0.01, 500)).toBe(0.001);
  });
  it("distinct tiers get distinct keys; equal tiers share a key", () => {
    expect(tierKey(tierSpanSec(500, 500))).toBe(tierKey(tierSpanSec(400, 500)));
    expect(tierKey(tierSpanSec(500, 500))).not.toBe(tierKey(tierSpanSec(5000, 500)));
  });
});

describe("maxPointsForFetch", () => {
  it("asks for ~one point per tier span", () => {
    expect(maxPointsForFetch(500, 1)).toBe(500);
  });
  it("caps at the server ceiling", () => {
    expect(maxPointsForFetch(1_000_000, 1)).toBe(5000);
  });
  it("never returns zero", () => {
    expect(maxPointsForFetch(0, 1)).toBe(1);
  });
});

describe("coalesceSpans", () => {
  it("merges overlapping and adjacent spans", () => {
    expect(coalesceSpans([[0, 10], [5, 15], [15, 20]])).toEqual([[0, 20]]);
  });
  it("keeps disjoint spans and sorts them", () => {
    expect(coalesceSpans([[30, 40], [0, 10]])).toEqual([[0, 10], [30, 40]]);
  });
  it("drops degenerate spans", () => {
    expect(coalesceSpans([[10, 10], [5, 4]])).toEqual([]);
  });
  it("honors adjacency tolerance", () => {
    expect(coalesceSpans([[0, 10], [12, 20]], 2)).toEqual([[0, 20]]);
  });
});

describe("subtractSpans", () => {
  it("returns the whole target when nothing is covered", () => {
    expect(subtractSpans([0, 100], [])).toEqual([[0, 100]]);
  });
  it("returns gaps around covered regions", () => {
    expect(subtractSpans([0, 100], [[20, 40], [60, 70]])).toEqual([
      [0, 20],
      [40, 60],
      [70, 100],
    ]);
  });
  it("returns empty when fully covered", () => {
    expect(subtractSpans([10, 20], [[0, 30]])).toEqual([]);
  });
  it("clips coverage that overhangs the target edges", () => {
    expect(subtractSpans([10, 20], [[0, 12], [18, 30]])).toEqual([[12, 18]]);
  });
});

describe("computeFetchSpans", () => {
  it("subtracts ring + cached coverage and drops slivers", () => {
    // window [0,100]; ring covers recent [80,100]; cached older [0,20];
    // gap [20,80] remains.
    const spans = computeFetchSpans([0, 100], [80, 100], [[0, 20]], 1);
    expect(spans).toEqual([[20, 80]]);
  });
  it("drops gaps narrower than minSpanSec", () => {
    const spans = computeFetchSpans([0, 100], [80, 100], [[0, 79.5]], 1);
    expect(spans).toEqual([]); // only a 0.5s sliver at [79.5,80]
  });
  it("fetches the whole window when the ring is empty and cache cold", () => {
    expect(computeFetchSpans([0, 100], null, [], 1)).toEqual([[0, 100]]);
  });
});

describe("seriesIsBucketed / render decision", () => {
  it("bucketed series -> envelope", () => {
    expect(seriesIsBucketed(bucketSeries([[1, 0, 2, 1]]))).toBe(true);
  });
  it("raw series -> line", () => {
    expect(seriesIsBucketed(rawSeries([[1, 5]]))).toBe(false);
  });
});

describe("HistoryCache", () => {
  it("caches raw points and selects a raw render clipped to the window", () => {
    const c = new HistoryCache();
    const tier = tierSpanSec(60, 500);
    c.insert(tier, "chan", [0, 10], rawSeries([[1, 10], [5, 20], [9, 30]]));
    expect(c.covered(tier, "chan")).toEqual([[0, 10]]);
    const r = c.select(tier, "chan", [2, 8]);
    expect(r).toEqual({ kind: "raw", x: [5], y: [20] });
  });

  it("caches buckets and selects an envelope render", () => {
    const c = new HistoryCache();
    const tier = tierSpanSec(3600, 500);
    c.insert(tier, "chan", [0, 100], bucketSeries([[10, -1, 3, 1], [50, 0, 4, 2]]));
    const r = c.select(tier, "chan", [0, 100]);
    expect(r).toEqual({
      kind: "envelope",
      x: [10, 50],
      low: [-1, 0],
      high: [3, 4],
      mean: [1, 2],
    });
  });

  it("merges adjacent fetched spans and their points", () => {
    const c = new HistoryCache();
    const tier = tierSpanSec(60, 500);
    c.insert(tier, "chan", [0, 5], rawSeries([[1, 10]]));
    c.insert(tier, "chan", [5, 10], rawSeries([[7, 20]]));
    expect(c.covered(tier, "chan")).toEqual([[0, 10]]);
    const r = c.select(tier, "chan", [0, 10]);
    expect(r).toEqual({ kind: "raw", x: [1, 7], y: [10, 20] });
  });

  it("returns null when nothing is cached in the window", () => {
    const c = new HistoryCache();
    const tier = tierSpanSec(60, 500);
    c.insert(tier, "chan", [0, 10], rawSeries([[1, 10]]));
    expect(c.select(tier, "chan", [100, 200])).toBeNull();
  });

  it("keeps tiers independent", () => {
    const c = new HistoryCache();
    const fine = tierSpanSec(60, 500);
    const coarse = tierSpanSec(3600, 500);
    c.insert(fine, "chan", [0, 10], rawSeries([[1, 10]]));
    expect(c.select(coarse, "chan", [0, 10])).toBeNull();
  });
});

describe("combineSeries (seam merge)", () => {
  const ring = { x: [8, 9, 10], y: [80, 90, 100] };

  it("passes ring straight through when there is no history", () => {
    const c = combineSeries(ring, null, 8);
    expect(c.x).toEqual([8, 9, 10]);
    expect(c.line).toEqual([80, 90, 100]);
    expect(c.hasEnvelope).toBe(false);
    expect(c.low).toEqual([null, null, null]);
  });

  it("prefers ring at/after the seam and history before it (no double-draw)", () => {
    const hist = { kind: "raw" as const, x: [1, 5, 8, 9], y: [1, 5, 999, 999] };
    const c = combineSeries(ring, hist, 8);
    // history points at t>=8 dropped; ring owns 8,9,10.
    expect(c.x).toEqual([1, 5, 8, 9, 10]);
    expect(c.line).toEqual([1, 5, 80, 90, 100]);
    expect(c.hasEnvelope).toBe(false);
  });

  it("emits an envelope band only in the pre-seam history region", () => {
    const hist = {
      kind: "envelope" as const,
      x: [1, 5],
      low: [0, 1],
      high: [2, 6],
      mean: [1, 3],
    };
    const c = combineSeries(ring, hist, 8);
    expect(c.x).toEqual([1, 5, 8, 9, 10]);
    expect(c.line).toEqual([1, 3, 80, 90, 100]);
    expect(c.low).toEqual([0, 1, null, null, null]);
    expect(c.high).toEqual([2, 6, null, null, null]);
    expect(c.hasEnvelope).toBe(true);
  });
});
