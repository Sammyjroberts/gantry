import { describe, it, expect } from "vitest";
import type { Experiment } from "@gantry/api-client";
import {
  defaultExperimentName,
  durationSec,
  experimentCsvHref,
  experimentRegions,
  formatDuration,
  isRunning,
  sortNewestFirst,
} from "./experiments";

const SEC = 1_000_000_000n;

function mkExp(over: Partial<Experiment>): Experiment {
  return {
    $typeName: "gantry.v1.Experiment",
    id: "",
    name: "",
    notes: "",
    deviceId: "",
    startNs: 0n,
    endNs: 0n,
    createdNs: 0n,
    ...over,
  } as Experiment;
}

describe("defaultExperimentName", () => {
  it("formats local time to minute precision", () => {
    // Built from local Date components, so it is timezone-stable.
    const d = new Date(2026, 6, 12, 17, 42, 8);
    expect(defaultExperimentName(d)).toBe("test-2026-07-12-17-42");
  });

  it("zero-pads month/day/hour/minute", () => {
    const d = new Date(2026, 0, 3, 4, 5);
    expect(defaultExperimentName(d)).toBe("test-2026-01-03-04-05");
  });
});

describe("isRunning", () => {
  it("is true only while end_ns is zero", () => {
    expect(isRunning(mkExp({ endNs: 0n }))).toBe(true);
    expect(isRunning(mkExp({ endNs: 5n }))).toBe(false);
  });
});

describe("formatDuration", () => {
  it("renders M:SS below an hour and H:MM:SS above", () => {
    expect(formatDuration(0)).toBe("0:00");
    expect(formatDuration(65)).toBe("1:05");
    expect(formatDuration(3661)).toBe("1:01:01");
  });
  it("floors and never goes negative", () => {
    expect(formatDuration(-10)).toBe("0:00");
    expect(formatDuration(9.9)).toBe("0:09");
  });
});

describe("durationSec", () => {
  const now = 2000n * SEC;
  it("measures a running run against now", () => {
    expect(durationSec(mkExp({ startNs: 1970n * SEC, endNs: 0n }), now)).toBe(30);
  });
  it("measures a finished run against its recorded end", () => {
    expect(durationSec(mkExp({ startNs: 1000n * SEC, endNs: 1042n * SEC }), now)).toBe(42);
  });
});

describe("experimentRegions", () => {
  it("extends a running run's band to now; finished runs end at end_ns", () => {
    const running = mkExp({ id: "r", name: "live", startNs: 1990n * SEC, endNs: 0n });
    const done = mkExp({ id: "d", name: "old", startNs: 1000n * SEC, endNs: 1100n * SEC });
    const regions = experimentRegions([running, done], 2000);
    // Sorted oldest-first for stable draw order.
    expect(regions.map((r) => r.id)).toEqual(["d", "r"]);
    const live = regions.find((r) => r.id === "r")!;
    expect(live.running).toBe(true);
    expect(live.startSec).toBe(1990);
    expect(live.endSec).toBe(2000); // extended to now
    const fin = regions.find((r) => r.id === "d")!;
    expect(fin.running).toBe(false);
    expect(fin.endSec).toBe(1100);
  });
});

describe("sortNewestFirst", () => {
  it("orders by start desc, then created desc", () => {
    const a = mkExp({ id: "a", startNs: 100n });
    const b = mkExp({ id: "b", startNs: 300n });
    const c = mkExp({ id: "c", startNs: 200n });
    expect(sortNewestFirst([a, b, c]).map((e) => e.id)).toEqual(["b", "c", "a"]);
  });
});

describe("experimentCsvHref", () => {
  it("builds a same-origin export URL, trimming a trailing slash", () => {
    expect(experimentCsvHref("http://host:4780/", "exp1")).toBe(
      "http://host:4780/export/experiments/exp1.csv",
    );
  });
  it("adds channels and format params when supplied", () => {
    const href = experimentCsvHref("http://host:4780", "e2", ["a", "b"], "wide");
    const url = new URL(href);
    expect(url.pathname).toBe("/export/experiments/e2.csv");
    expect(url.searchParams.get("channels")).toBe("a,b");
    expect(url.searchParams.get("format")).toBe("wide");
  });
  it("omits channels when the scope is empty (server exports all)", () => {
    const url = new URL(experimentCsvHref("http://host:4780", "e3", []));
    expect(url.searchParams.has("channels")).toBe(false);
  });
});
