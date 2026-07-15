import { describe, it, expect } from "vitest";
import {
  resolveAngle,
  resolveOffset,
  resolveJoint,
  resolveModelSource,
  valueAtOrBefore,
  boundChannelKeys,
  defaultBindings,
  mergeBindings,
  deg2rad,
  type Sampler,
} from "./pose";

/** A sampler backed by a fixed map. */
function sampler(values: Record<string, number | null>): Sampler {
  return (key) => (key in values ? values[key]! : null);
}

describe("resolveAngle (deg/rad/sign → radians)", () => {
  it("returns 0 when unbound", () => {
    expect(resolveAngle({ channelKey: null, unit: "deg", sign: 1 }, sampler({}))).toBe(0);
  });

  it("converts degrees to radians", () => {
    const s = sampler({ p: 90 });
    expect(resolveAngle({ channelKey: "p", unit: "deg", sign: 1 }, s)).toBeCloseTo(Math.PI / 2, 12);
  });

  it("passes radians through untouched", () => {
    const s = sampler({ p: 1.25 });
    expect(resolveAngle({ channelKey: "p", unit: "rad", sign: 1 }, s)).toBeCloseTo(1.25, 12);
  });

  it("applies the sign flip", () => {
    const s = sampler({ p: 45 });
    expect(resolveAngle({ channelKey: "p", unit: "deg", sign: -1 }, s)).toBeCloseTo(-deg2rad(45), 12);
  });

  it("returns 0 for a missing / non-finite sample", () => {
    expect(resolveAngle({ channelKey: "p", unit: "deg", sign: 1 }, sampler({}))).toBe(0);
    expect(resolveAngle({ channelKey: "p", unit: "deg", sign: 1 }, sampler({ p: NaN }))).toBe(0);
  });
});

describe("resolveOffset (channel vs manual)", () => {
  it("uses the manual constant when unbound", () => {
    expect(resolveOffset({ channelKey: null, manual: 0.3 }, sampler({}))).toBe(0.3);
  });
  it("uses the channel value when bound", () => {
    expect(resolveOffset({ channelKey: "z", manual: 0.3 }, sampler({ z: 1.2 }))).toBe(1.2);
  });
  it("falls back to manual when the bound sample is absent", () => {
    expect(resolveOffset({ channelKey: "z", manual: 0.3 }, sampler({}))).toBe(0.3);
  });
});

describe("resolveJoint (channel / manual jog)", () => {
  it("returns the manual jog value in manual mode", () => {
    expect(
      resolveJoint({ mode: "manual", channelKey: "x", unit: "rad", sign: 1, manual: 0.7 }, sampler({ x: 9 })),
    ).toBe(0.7);
  });
  it("reads + converts + signs the channel in channel mode", () => {
    const s = sampler({ w: 180 });
    expect(
      resolveJoint({ mode: "channel", channelKey: "w", unit: "deg", sign: -1, manual: 0 }, s),
    ).toBeCloseTo(-Math.PI, 12);
  });
  it("returns 0 for an unbound channel joint", () => {
    expect(
      resolveJoint({ mode: "channel", channelKey: null, unit: "rad", sign: 1, manual: 5 }, sampler({})),
    ).toBe(0);
  });
});

describe("boundChannelKeys", () => {
  it("collects every referenced channel key, de-duplicated", () => {
    const b = defaultBindings();
    b.pitch = { channelKey: "imupitch", unit: "deg", sign: 1 };
    b.z = { channelKey: "imuz", manual: 0 };
    b.joints = {
      left: { mode: "channel", channelKey: "encleft", unit: "rad", sign: 1, manual: 0 },
      right: { mode: "manual", channelKey: "encright", unit: "rad", sign: 1, manual: 0 },
    };
    const keys = boundChannelKeys(b).sort();
    // The manual-mode joint's channel is NOT needed (jog overrides it).
    expect(keys).toEqual(["encleft", "imupitch", "imuz"].sort());
  });
});

describe("resolveModelSource (priority urdf → glb → stl → primitive)", () => {
  it("prefers urdf over glb/stl", () => {
    const src = resolveModelSource("mr-wobbles", [
      "mr-wobbles.stl",
      "mr-wobbles.glb",
      "mr-wobbles.urdf",
    ]);
    expect(src).toEqual({ kind: "urdf", file: "mr-wobbles.urdf" });
  });
  it("falls to glb when no urdf, then stl", () => {
    expect(resolveModelSource("bot", ["bot.stl", "bot.glb"])).toEqual({ kind: "glb", file: "bot.glb" });
    expect(resolveModelSource("bot", ["bot.stl"])).toEqual({ kind: "stl", file: "bot.stl" });
  });
  it("is case-insensitive on the file name", () => {
    expect(resolveModelSource("Bot", ["bot.URDF"])).toEqual({ kind: "urdf", file: "bot.URDF" });
  });
  it("falls back to the generated primitive when nothing matches", () => {
    expect(resolveModelSource("bot", ["other.urdf"])).toEqual({ kind: "primitive" });
    expect(resolveModelSource("bot", [])).toEqual({ kind: "primitive" });
  });
});

describe("valueAtOrBefore (replay cursor lookup)", () => {
  const x = [1, 2, 3, 4, 5];
  const y = [10, 20, 30, 40, 50];

  it("returns the value at an exact sample time", () => {
    expect(valueAtOrBefore(x, y, 3)).toBe(30);
  });
  it("returns the last value strictly before the cursor", () => {
    expect(valueAtOrBefore(x, y, 3.9)).toBe(30);
  });
  it("returns null when the cursor precedes the first sample", () => {
    expect(valueAtOrBefore(x, y, 0.5)).toBeNull();
  });
  it("clamps to the final sample past the end", () => {
    expect(valueAtOrBefore(x, y, 99)).toBe(50);
  });
  it("returns null for an empty series", () => {
    expect(valueAtOrBefore([], [], 3)).toBeNull();
  });
  it("skips trailing null/NaN gaps backwards", () => {
    expect(valueAtOrBefore([1, 2, 3], [10, null, NaN], 3)).toBe(10);
  });
});

describe("bindings merge", () => {
  it("merges a partial/legacy blob onto fresh defaults", () => {
    const merged = mergeBindings({ pitch: { channelKey: "a", sign: -1 }, joints: { j: { mode: "manual", manual: 1 } } });
    expect(merged.pitch).toEqual({ channelKey: "a", unit: "deg", sign: -1 });
    expect(merged.roll.channelKey).toBeNull();
    expect(merged.joints.j).toEqual({ mode: "manual", channelKey: null, unit: "rad", sign: 1, manual: 1 });
    expect(merged.dims.wheelRadius).toBeGreaterThan(0);
  });

  it("returns defaults for a garbage blob", () => {
    expect(mergeBindings(null).pitch.channelKey).toBeNull();
    expect(mergeBindings("nonsense").roll.unit).toBe("deg");
  });
});
