import { describe, it, expect } from "vitest";
import {
  cursorAt,
  isFinished,
  pause,
  play,
  progress,
  seek,
  setSpeed,
  startReplay,
  togglePlay,
} from "./playback";

describe("startReplay", () => {
  it("plays from the start at 1x", () => {
    const s = startReplay(100, 200, 1000);
    expect(s.playing).toBe(true);
    expect(s.speed).toBe(1);
    expect(cursorAt(s, 1000)).toBe(100);
  });
  it("tolerates a zero/negative span", () => {
    const s = startReplay(100, 50, 1000);
    expect(s.endSec).toBe(100);
    expect(cursorAt(s, 5000)).toBe(100);
  });
});

describe("cursorAt (playing)", () => {
  it("advances in real time at 1x", () => {
    const s = startReplay(0, 1000, 0);
    expect(cursorAt(s, 10_000)).toBe(10); // 10s wall -> 10s cursor
  });
  it("advances 4x faster at speed 4", () => {
    const s = setSpeed(startReplay(0, 1000, 0), 4, 0);
    expect(cursorAt(s, 10_000)).toBe(40);
  });
  it("clamps at the end", () => {
    const s = startReplay(0, 5, 0);
    expect(cursorAt(s, 60_000)).toBe(5);
    expect(isFinished(s, 60_000)).toBe(true);
  });
});

describe("pause / play", () => {
  it("freezes the cursor on pause", () => {
    const s = startReplay(0, 1000, 0);
    const p = pause(s, 10_000);
    expect(p.playing).toBe(false);
    expect(cursorAt(p, 999_999)).toBe(10); // frozen regardless of wall time
  });
  it("resumes from the frozen cursor without jumping", () => {
    const s = startReplay(0, 1000, 0);
    const p = pause(s, 10_000); // cursor 10
    const r = play(p, 50_000); // resume much later
    expect(cursorAt(r, 52_000)).toBe(12); // 10 + 2s since resume
  });
  it("restarts from the beginning if resumed at the end", () => {
    const s = startReplay(0, 5, 0);
    const done = pause(s, 60_000); // cursor clamped at 5 (end)
    const r = play(done, 60_000);
    expect(cursorAt(r, 60_000)).toBe(0);
  });
  it("togglePlay flips the state", () => {
    const s = startReplay(0, 100, 0);
    expect(togglePlay(s, 1000).playing).toBe(false);
    expect(togglePlay(pause(s, 1000), 2000).playing).toBe(true);
  });
});

describe("seek", () => {
  it("jumps the cursor, clamped, preserving playing", () => {
    const s = startReplay(0, 1000, 0);
    const j = seek(s, 500, 10_000);
    expect(j.playing).toBe(true);
    expect(cursorAt(j, 10_000)).toBe(500);
    expect(cursorAt(j, 12_000)).toBe(502); // keeps advancing from the seek
  });
  it("clamps out-of-range seeks", () => {
    const s = startReplay(0, 100, 0);
    expect(cursorAt(seek(s, -50, 0), 0)).toBe(0);
    expect(cursorAt(seek(s, 999, 0), 0)).toBe(100);
  });
  it("seek while paused stays put", () => {
    const s = pause(startReplay(0, 1000, 0), 0);
    const j = seek(s, 300, 5000);
    expect(cursorAt(j, 99_999)).toBe(300);
  });
});

describe("setSpeed", () => {
  it("changes rate without jumping the cursor", () => {
    const s = startReplay(0, 1000, 0);
    // at 5s wall, cursor is 5; switch to 16x.
    const fast = setSpeed(s, 16, 5000);
    expect(cursorAt(fast, 5000)).toBe(5); // no jump at the switch instant
    expect(cursorAt(fast, 6000)).toBe(21); // 5 + 1s*16
  });
});

describe("progress", () => {
  it("reports swept fraction", () => {
    const s = startReplay(100, 200, 0);
    expect(progress(s, 0)).toBe(0);
    expect(progress(s, 50_000)).toBe(0.5); // 50s at 1x -> cursor 150 -> 50%
    expect(progress(s, 999_999)).toBe(1);
  });
});
