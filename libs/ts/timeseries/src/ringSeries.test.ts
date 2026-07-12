import { describe, it, expect } from "vitest";
import { RingSeries } from "./ringSeries";

const ns = (n: number): bigint => BigInt(n) * 1_000_000_000n; // seconds -> ns

describe("RingSeries", () => {
  it("rejects a non-positive capacity", () => {
    expect(() => new RingSeries(0)).toThrow(RangeError);
    expect(() => new RingSeries(-5)).toThrow(RangeError);
    expect(() => new RingSeries(1.5)).toThrow(RangeError);
  });

  it("appends in O(1) and tracks size/full", () => {
    const r = new RingSeries(3);
    expect(r.size).toBe(0);
    expect(r.full).toBe(false);
    r.append(ns(1), 10);
    r.append(ns(2), 20);
    expect(r.size).toBe(2);
    expect(r.full).toBe(false);
    r.append(ns(3), 30);
    expect(r.full).toBe(true);
    expect(r.latest()).toEqual({ tNs: ns(3), value: 30 });
    expect(r.oldest()).toEqual({ tNs: ns(1), value: 10 });
  });

  it("wraps in place, overwriting the oldest samples", () => {
    const r = new RingSeries(3);
    for (let i = 1; i <= 5; i++) r.append(ns(i), i * 10);
    // Only the last 3 remain: t=3,4,5
    expect(r.size).toBe(3);
    const { x, y } = r.window();
    expect(Array.from(x)).toEqual([3, 4, 5]);
    expect(Array.from(y)).toEqual([30, 40, 50]);
    expect(r.oldest()).toEqual({ tNs: ns(3), value: 30 });
    expect(r.latest()).toEqual({ tNs: ns(5), value: 50 });
  });

  it("keeps wrapping correct across many revolutions", () => {
    const r = new RingSeries(4);
    for (let i = 1; i <= 100; i++) r.append(ns(i), i);
    const { x, y } = r.window();
    expect(Array.from(x)).toEqual([97, 98, 99, 100]);
    expect(Array.from(y)).toEqual([97, 98, 99, 100]);
    expect(r.accepted).toBe(100);
  });

  it("extracts inclusive windows correctly", () => {
    const r = new RingSeries(10);
    for (let i = 0; i < 10; i++) r.append(ns(i), i);
    const w = r.window(ns(3), ns(6));
    expect(Array.from(w.x)).toEqual([3, 4, 5, 6]); // inclusive both ends
    expect(Array.from(w.y)).toEqual([3, 4, 5, 6]);
  });

  it("windows that fall outside retained range clamp to what exists", () => {
    const r = new RingSeries(5);
    for (let i = 10; i < 20; i++) r.append(ns(i), i); // retains 15..19
    expect(Array.from(r.window(ns(0), ns(12)).x)).toEqual([]); // all evicted
    expect(Array.from(r.window(ns(18), ns(999)).x)).toEqual([18, 19]);
    expect(Array.from(r.window(ns(17)).x)).toEqual([17, 18, 19]); // open upper bound
    expect(Array.from(r.window(undefined, ns(16)).x)).toEqual([15, 16]); // open lower
  });

  it("windows correctly even when the range straddles the physical wrap point", () => {
    const r = new RingSeries(5);
    // Fill then overwrite so startIdx != 0 and data wraps the physical array.
    for (let i = 0; i < 8; i++) r.append(ns(i), i * 2); // retains t=3..7
    const w = r.window(ns(4), ns(6));
    expect(Array.from(w.x)).toEqual([4, 5, 6]);
    expect(Array.from(w.y)).toEqual([8, 10, 12]);
  });

  it("drops out-of-order (late) samples and counts them", () => {
    const r = new RingSeries(10);
    expect(r.append(ns(5), 50)).toBe(true);
    expect(r.append(ns(6), 60)).toBe(true);
    expect(r.append(ns(4), 40)).toBe(false); // late -> dropped
    expect(r.append(ns(6), 61)).toBe(true); // equal ts is allowed (not strictly older)
    expect(r.droppedLate).toBe(1);
    expect(r.accepted).toBe(3);
    const { x, y } = r.window();
    expect(Array.from(x)).toEqual([5, 6, 6]);
    expect(Array.from(y)).toEqual([50, 60, 61]);
  });

  it("preserves full bigint nanosecond timestamps beyond Float64 integer range", () => {
    const r = new RingSeries(2);
    const base = 1_700_000_000_000_000_000n; // ~2023 in epoch ns, > 2^53
    r.append(base, 1);
    r.append(base + 7n, 2);
    expect(r.latest()!.tNs).toBe(base + 7n);
    expect(r.oldest()!.tNs).toBe(base);
  });

  it("clear() empties without losing capacity", () => {
    const r = new RingSeries(3);
    r.append(ns(1), 1);
    r.clear();
    expect(r.size).toBe(0);
    expect(r.latest()).toBeNull();
    // after clear, an older timestamp is accepted again (lastNs reset)
    expect(r.append(ns(1), 1)).toBe(true);
  });
});
