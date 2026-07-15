import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  useTimeStore,
  resolveVisible,
  replayProgressAt,
  boundsAt,
  type TimeState,
} from "./timeStore";
import { INITIAL_ZOOM, applyPreset, setRange, zoomAt, panBy } from "../zoom";
import { startReplay, cursorAt, togglePlay, seek } from "../playback";

// Freeze wall clock so bounds/replay math is deterministic.
const NOW_MS = 1_700_000_000_000;
const NOW_S = NOW_MS / 1000;

function reset() {
  useTimeStore.setState({ windowSec: 60, paused: false, zoom: INITIAL_ZOOM, replay: null });
}

beforeEach(() => {
  vi.spyOn(Date, "now").mockReturnValue(NOW_MS);
  reset();
});
afterEach(() => vi.restoreAllMocks());

const st = () => useTimeStore.getState();

describe("timeStore zoom parity with zoom.ts", () => {
  it("applyPreset snaps to live at the requested width", () => {
    st().applyPreset(30);
    const expected = applyPreset(30);
    expect(st().zoom).toEqual(expected.zoom);
    expect(st().windowSec).toBe(expected.windowSec);
    expect(st().zoom.mode).toBe("live");
  });

  it("setRange enters inspect matching the pure op", () => {
    st().setRange(NOW_S - 100, NOW_S - 40);
    const expected = setRange(INITIAL_ZOOM, boundsAt(NOW_MS), NOW_S - 100, NOW_S - 40);
    expect(st().zoom).toEqual(expected);
    expect(st().zoom.mode).toBe("inspect");
  });

  it("zoomAt narrows about the center matching the pure op", () => {
    st().setRange(NOW_S - 100, NOW_S - 40);
    const before = st().zoom;
    st().zoomAt(NOW_S - 70, 0.5);
    const expected = zoomAt(before, st().windowSec, boundsAt(NOW_MS), NOW_S - 70, 0.5);
    expect(st().zoom).toEqual(expected);
  });

  it("panBy preserves width matching the pure op", () => {
    st().setRange(NOW_S - 100, NOW_S - 40);
    const before = st().zoom;
    st().panBy(-10);
    const expected = panBy(before, st().windowSec, boundsAt(NOW_MS), -10);
    expect(st().zoom).toEqual(expected);
  });

  it("backToLive returns to the live window", () => {
    st().setRange(NOW_S - 100, NOW_S - 40);
    st().backToLive();
    expect(st().zoom.mode).toBe("live");
  });

  it("fitTo enters inspect and clears any replay", () => {
    st().enterReplay({ id: "e1", name: "run", startSec: NOW_S - 200, endSec: NOW_S - 100 });
    st().fitTo(NOW_S - 200, NOW_S - 100);
    expect(st().replay).toBeNull();
    expect(st().zoom.mode).toBe("inspect");
  });
});

describe("timeStore replay parity with playback.ts", () => {
  it("enterReplay installs a fresh clock", () => {
    st().enterReplay({ id: "e1", name: "run", startSec: NOW_S - 100, endSec: NOW_S - 10 });
    const expected = startReplay(NOW_S - 100, NOW_S - 10, NOW_MS);
    expect(st().replay!.clock).toEqual(expected);
    expect(st().replay!.name).toBe("run");
  });

  it("togglePlay + seek match the pure transitions", () => {
    st().enterReplay({ id: "e1", name: "run", startSec: NOW_S - 100, endSec: NOW_S - 10 });
    const clock0 = st().replay!.clock;
    st().replayTogglePlay();
    expect(st().replay!.clock).toEqual(togglePlay(clock0, NOW_MS));

    const clock1 = st().replay!.clock;
    st().replaySeekFraction(0.5);
    const target = clock1.startSec + 0.5 * (clock1.endSec - clock1.startSec);
    expect(st().replay!.clock).toEqual(seek(clock1, target, NOW_MS));
  });

  it("exitReplay clears the session and fits the window", () => {
    st().enterReplay({ id: "e1", name: "run", startSec: NOW_S - 100, endSec: NOW_S - 10 });
    st().exitReplay();
    expect(st().replay).toBeNull();
    expect(st().zoom.mode).toBe("inspect");
  });
});

describe("resolveVisible", () => {
  it("live mode yields the sliding window anchored to now", () => {
    const v = resolveVisible(st() as TimeState, NOW_MS);
    expect(v.range[1]).toBeCloseTo(NOW_S, 6);
    expect(v.range[0]).toBeCloseTo(NOW_S - 60, 6);
    expect(v.cursorSec).toBeUndefined();
  });

  it("replay mode centers the window on the playhead", () => {
    st().enterReplay({ id: "e1", name: "run", startSec: NOW_S - 100, endSec: NOW_S - 10 });
    const v = resolveVisible(st() as TimeState, NOW_MS);
    const cursor = cursorAt(st().replay!.clock, NOW_MS);
    expect(v.cursorSec).toBeCloseTo(cursor, 6);
    // playhead sits inside the window
    expect(cursor).toBeGreaterThanOrEqual(v.range[0]);
    expect(cursor).toBeLessThanOrEqual(v.range[1]);
  });

  it("replayProgressAt is undefined outside replay, a fraction inside", () => {
    expect(replayProgressAt(st() as TimeState, NOW_MS)).toBeUndefined();
    st().enterReplay({ id: "e1", name: "run", startSec: NOW_S - 100, endSec: NOW_S - 10 });
    const p = replayProgressAt(st() as TimeState, NOW_MS)!;
    expect(p).toBeGreaterThanOrEqual(0);
    expect(p).toBeLessThanOrEqual(1);
  });
});
