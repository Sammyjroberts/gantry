import { describe, it, expect } from "vitest";
import {
  INITIAL_ZOOM,
  MIN_WINDOW_SEC,
  backToLive,
  clampRange,
  liveWindow,
  panBy,
  resolveWindow,
  setRange,
  zoomAt,
  type Bounds,
  type ZoomState,
} from "./zoom";

// 1000s of buffered history: oldest=1000, live edge=2000.
const bounds: Bounds = { oldest: 1000, now: 2000 };

describe("liveWindow", () => {
  it("anchors [now - width, now]", () => {
    expect(liveWindow(60, 2000)).toEqual([1940, 2000]);
  });
});

describe("resolveWindow (live)", () => {
  it("returns the sliding window and is never marked clamped", () => {
    const r = resolveWindow(INITIAL_ZOOM, 60, bounds);
    expect(r.range).toEqual([1940, 2000]);
    expect(r.clamped).toBe(false);
  });
});

describe("clampRange", () => {
  it("passes an in-bounds range through untouched", () => {
    expect(clampRange(1500, 1600, bounds)).toEqual({ range: [1500, 1600], clamped: false });
  });

  it("keeps width when sliding off the right edge (can't see the future)", () => {
    const r = clampRange(1980, 2040, bounds); // width 60, max past now
    expect(r.range).toEqual([1940, 2000]);
    expect(r.clamped).toBe(true);
  });

  it("keeps width when sliding off the left (buffer) edge", () => {
    const r = clampRange(900, 960, bounds); // width 60, min past oldest
    expect(r.range).toEqual([1000, 1060]);
    expect(r.clamped).toBe(true);
  });

  it("shows everything and flags clamp when the request is wider than the buffer", () => {
    const r = clampRange(500, 3000, bounds);
    expect(r.range).toEqual([1000, 2000]);
    expect(r.clamped).toBe(true);
  });
});

describe("zoomAt", () => {
  it("zooms in about the cursor, entering inspect mode", () => {
    // live [1940,2000], cursor at midpoint 1970, factor 0.5 -> width 30.
    const z = zoomAt(INITIAL_ZOOM, 60, bounds, 1970, 0.5);
    expect(z.mode).toBe("inspect");
    expect(z.min).toBeCloseTo(1955, 6);
    expect(z.max).toBeCloseTo(1985, 6);
  });

  it("holds the cursor point fixed for an off-center zoom", () => {
    // live [1940,2000], cursor at 1994 (ratio 0.9), zoom in 0.5 -> width 30.
    const z = zoomAt(INITIAL_ZOOM, 60, bounds, 1994, 0.5);
    // cursor stays at the same value: min = 1994 - 0.9*30, max = 1994 + 0.1*30
    expect(z.min).toBeCloseTo(1967, 6);
    expect(z.max).toBeCloseTo(1997, 6);
  });

  it("clamps a zoom-out past the buffer to the whole extent", () => {
    const z = zoomAt(INITIAL_ZOOM, 60, bounds, 1970, 100);
    const r = resolveWindow(z, 60, bounds);
    expect(r.range).toEqual([1000, 2000]);
    expect(r.clamped).toBe(true);
  });

  it("floors the window at MIN_WINDOW_SEC", () => {
    const z = zoomAt(INITIAL_ZOOM, 60, bounds, 1970, 0.00001);
    expect(z.max - z.min).toBeCloseTo(MIN_WINDOW_SEC, 6);
  });
});

describe("panBy", () => {
  it("shifts the window later, clamping at the live edge with preserved width", () => {
    const start = setRange(INITIAL_ZOOM, bounds, 1900, 1960); // width 60
    const z = panBy(start, 60, bounds, 100); // push past now
    // Slides back inside the right edge, keeping the 60s width.
    expect(resolveWindow(z, 60, bounds).range).toEqual([1940, 2000]);
  });

  it("shifts the window earlier, clamping at the buffer horizon", () => {
    const start = setRange(INITIAL_ZOOM, bounds, 1100, 1160);
    const z = panBy(start, 60, bounds, -200);
    expect(resolveWindow(z, 60, bounds).range).toEqual([1000, 1060]);
  });
});

describe("setRange", () => {
  it("enters inspect at an explicit range", () => {
    const z = setRange(INITIAL_ZOOM, bounds, 1200, 1400);
    expect(z).toEqual({ mode: "inspect", min: 1200, max: 1400 });
  });

  it("ignores a degenerate/backwards range", () => {
    expect(setRange(INITIAL_ZOOM, bounds, 1400, 1200)).toBe(INITIAL_ZOOM);
    expect(setRange(INITIAL_ZOOM, bounds, 1400, 1400)).toBe(INITIAL_ZOOM);
  });
});

describe("live -> inspect -> back-to-live", () => {
  it("transitions and restores the sliding window", () => {
    let z: ZoomState = INITIAL_ZOOM;
    expect(z.mode).toBe("live");

    z = zoomAt(z, 60, bounds, 1970, 0.5); // gesture enters inspect
    expect(z.mode).toBe("inspect");
    expect(resolveWindow(z, 60, bounds).range).toEqual([1955, 1985]);

    z = panBy(z, 60, bounds, -5); // still inspecting
    expect(z.mode).toBe("inspect");

    z = backToLive();
    expect(z.mode).toBe("live");
    // Back on the live edge; window tracks a moved-forward clock.
    expect(resolveWindow(z, 60, { oldest: 1000, now: 2100 }).range).toEqual([2040, 2100]);
  });
});
