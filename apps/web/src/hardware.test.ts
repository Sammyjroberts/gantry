import { describe, it, expect, beforeEach } from "vitest";
import type { Hardware } from "@gantry/api-client";
import { defaultBindings, type PoseBindings } from "./pose";
import {
  encodeVizConfig,
  decodeVizConfig,
  encodePanelDefaults,
  decodePanelDefaults,
  deviceDisplayName,
  readLegacyBindings,
  clearLegacyBindings,
  LEGACY_POSE_PREFIX,
  VIZ_CONFIG_VERSION,
  PANEL_DEFAULTS_VERSION,
} from "./hardware";

function mkHardware(over: Partial<Hardware>): Hardware {
  return {
    $typeName: "gantry.v1.Hardware",
    deviceId: "",
    displayName: "",
    description: "",
    notes: "",
    vizConfigJson: "",
    panelDefaultsJson: "",
    createdNs: 0n,
    updatedNs: 0n,
    ...over,
  } as Hardware;
}

describe("viz config envelope", () => {
  it("round-trips pose bindings through the versioned envelope", () => {
    const b = defaultBindings();
    b.yaw = { channelKey: "imu|yaw", unit: "rad", sign: -1 };
    b.joints = { arm: { mode: "manual", channelKey: null, unit: "rad", sign: 1, manual: 0.5 } };

    const json = encodeVizConfig(b);
    // The envelope carries the version tag.
    expect(JSON.parse(json).v).toBe(VIZ_CONFIG_VERSION);

    const back = decodeVizConfig(json) as PoseBindings;
    expect(back.yaw).toEqual({ channelKey: "imu|yaw", unit: "rad", sign: -1 });
    expect(back.joints.arm).toEqual({
      mode: "manual",
      channelKey: null,
      unit: "rad",
      sign: 1,
      manual: 0.5,
    });
  });

  it("returns null for an empty/absent document (distinct from defaults)", () => {
    expect(decodeVizConfig("")).toBeNull();
    expect(decodeVizConfig(undefined)).toBeNull();
    expect(decodeVizConfig(null)).toBeNull();
  });

  it("tolerates a legacy bare (unversioned) bindings blob", () => {
    const bare = JSON.stringify({ pitch: { channelKey: "p", unit: "deg", sign: 1 } });
    const back = decodeVizConfig(bare) as PoseBindings;
    expect(back.pitch.channelKey).toBe("p");
    expect(back.roll.channelKey).toBeNull(); // merged onto defaults
  });

  it("falls back to defaults for a garbage document", () => {
    // Non-JSON → null; a valid-JSON-but-nonsense object → merged defaults.
    expect(decodeVizConfig("not json")).toBeNull();
    const back = decodeVizConfig("42") as PoseBindings;
    expect(back.pitch.channelKey).toBeNull();
  });
});

describe("panel defaults envelope", () => {
  it("round-trips a channel-key selection", () => {
    const chans = ["drive|drive.speed", "imu|imu.pitch"];
    const json = encodePanelDefaults(chans);
    expect(JSON.parse(json).v).toBe(PANEL_DEFAULTS_VERSION);
    expect(decodePanelDefaults(json)).toEqual(chans);
  });

  it("returns [] for empty/absent/garbage documents", () => {
    expect(decodePanelDefaults("")).toEqual([]);
    expect(decodePanelDefaults(undefined)).toEqual([]);
    expect(decodePanelDefaults("not json")).toEqual([]);
  });

  it("tolerates a bare array", () => {
    expect(decodePanelDefaults(JSON.stringify(["a", "b"]))).toEqual(["a", "b"]);
  });
});

describe("deviceDisplayName fallback", () => {
  it("returns the display name when set", () => {
    const byId = new Map([["rover-1", mkHardware({ deviceId: "rover-1", displayName: "Rover One" })]]);
    expect(deviceDisplayName("rover-1", byId)).toBe("Rover One");
  });

  it("falls back to the device id when unset or blank", () => {
    const byId = new Map([["rover-1", mkHardware({ deviceId: "rover-1", displayName: "   " })]]);
    expect(deviceDisplayName("rover-1", byId)).toBe("rover-1");
  });

  it("falls back to the device id when there is no row", () => {
    expect(deviceDisplayName("unknown", new Map())).toBe("unknown");
  });
});

describe("legacy localStorage migration", () => {
  beforeEach(() => localStorage.clear());

  it("reads a legacy bindings blob without removing it, then clears on demand", () => {
    const b = defaultBindings();
    b.pitch = { channelKey: "imu|pitch", unit: "deg", sign: -1 };
    localStorage.setItem(`${LEGACY_POSE_PREFIX}mr-wobbles`, JSON.stringify(b));

    const read = readLegacyBindings("mr-wobbles") as PoseBindings;
    expect(read.pitch).toEqual({ channelKey: "imu|pitch", unit: "deg", sign: -1 });
    // Still present until explicitly cleared (push-then-remove ordering).
    expect(localStorage.getItem(`${LEGACY_POSE_PREFIX}mr-wobbles`)).not.toBeNull();

    clearLegacyBindings("mr-wobbles");
    expect(localStorage.getItem(`${LEGACY_POSE_PREFIX}mr-wobbles`)).toBeNull();
  });

  it("returns null when no legacy entry is present", () => {
    expect(readLegacyBindings("absent-device")).toBeNull();
  });
});
