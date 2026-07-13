import { describe, it, expect } from "vitest";
import {
  formatClock,
  formatDurationShort,
  formatRangeLabel,
  toDatetimeLocal,
  fromDatetimeLocal,
} from "./timeFormat";

// Build a local-time epoch-seconds instant so assertions are timezone-agnostic
// (formatClock reads local fields; constructing via local fields cancels the tz).
function localSec(y: number, mo: number, d: number, h: number, mi: number, s: number): number {
  return new Date(y, mo - 1, d, h, mi, s).getTime() / 1000;
}

describe("formatClock", () => {
  it("renders zero-padded HH:MM:SS local", () => {
    expect(formatClock(localSec(2026, 7, 12, 14, 2, 5))).toBe("14:02:05");
  });
});

describe("formatDurationShort", () => {
  it("sub-second -> ms", () => {
    expect(formatDurationShort(0.5)).toBe("500ms");
  });
  it("whole seconds", () => {
    expect(formatDurationShort(10)).toBe("10s");
  });
  it("minutes drop a zero seconds unit", () => {
    expect(formatDurationShort(300)).toBe("5m");
    expect(formatDurationShort(90)).toBe("1m30s");
  });
  it("hours", () => {
    expect(formatDurationShort(3600)).toBe("1h");
    expect(formatDurationShort(3600 + 30 * 60)).toBe("1h30m");
    expect(formatDurationShort(6 * 3600)).toBe("6h");
  });
});

describe("formatRangeLabel", () => {
  it("renders 'start – end (width)'", () => {
    const a = localSec(2026, 7, 12, 14, 2, 10);
    const b = localSec(2026, 7, 12, 14, 7, 10);
    expect(formatRangeLabel([a, b])).toBe("14:02:10 – 14:07:10 (5m)");
  });
});

describe("datetime-local round-trip", () => {
  it("toDatetimeLocal renders local YYYY-MM-DDTHH:MM:SS", () => {
    expect(toDatetimeLocal(localSec(2026, 7, 12, 9, 5, 3))).toBe("2026-07-12T09:05:03");
  });
  it("round-trips through fromDatetimeLocal to the same second", () => {
    const s = localSec(2026, 1, 2, 23, 59, 45);
    expect(fromDatetimeLocal(toDatetimeLocal(s))).toBe(s);
  });
  it("returns null for an empty or bad value", () => {
    expect(fromDatetimeLocal("")).toBeNull();
    expect(fromDatetimeLocal("not-a-date")).toBeNull();
  });
});
