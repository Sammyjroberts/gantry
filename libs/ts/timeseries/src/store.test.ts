import { describe, it, expect } from "vitest";
import { TimeSeriesStore } from "./store";

const ns = (n: number): bigint => BigInt(n) * 1_000_000_000n;

describe("TimeSeriesStore", () => {
  it("creates per-channel rings on demand and appends", () => {
    const store = new TimeSeriesStore(100);
    store.append("a.x", ns(1), 1);
    store.append("a.y", ns(1), 2);
    store.append("a.x", ns(2), 3);
    expect(store.channels().sort()).toEqual(["a.x", "a.y"]);
    expect(store.get("a.x")!.size).toBe(2);
    expect(Array.from(store.window("a.x").y)).toEqual([1, 3]);
  });

  it("returns an empty window for unknown channels", () => {
    const store = new TimeSeriesStore();
    const w = store.window("missing");
    expect(w.x.length).toBe(0);
    expect(w.y.length).toBe(0);
  });

  it("aggregates dropped-late and accepted counters across channels", () => {
    const store = new TimeSeriesStore();
    store.append("a", ns(5), 1);
    store.append("a", ns(4), 1); // late
    store.append("b", ns(9), 1);
    store.append("b", ns(1), 1); // late
    expect(store.totalDroppedLate()).toBe(2);
    expect(store.totalAccepted()).toBe(2);
  });

  it("honours a custom capacity per channel", () => {
    const store = new TimeSeriesStore(10);
    store.ensure("small", 2);
    for (let i = 0; i < 5; i++) store.append("small", ns(i), i);
    expect(store.get("small")!.size).toBe(2);
    expect(Array.from(store.window("small").y)).toEqual([3, 4]);
  });

  it("remove() drops one or all channels", () => {
    const store = new TimeSeriesStore();
    store.append("a", ns(1), 1);
    store.append("b", ns(1), 1);
    store.remove("a");
    expect(store.channels()).toEqual(["b"]);
    store.remove();
    expect(store.channels()).toEqual([]);
  });
});
