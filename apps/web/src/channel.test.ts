import { describe, it, expect } from "vitest";
import { ValueKind, type ChannelInfo } from "@gantry/api-client";
import {
  channelKey,
  channelLabel,
  parseKey,
  groupByPacket,
  subscribeNames,
} from "./channel";

function info(partial: Partial<ChannelInfo>): ChannelInfo {
  return {
    $typeName: "gantry.v1.ChannelInfo",
    name: "",
    kind: ValueKind.F64,
    unit: "",
    description: "",
    packet: "",
    ...partial,
  } as ChannelInfo;
}

describe("channelKey / identity", () => {
  it("keys distinct packets with the same param name to distinct keys (the collision case)", () => {
    // imu.temp (f64) vs power.temp (i64): same param name, different packets.
    const imu = channelKey("imu", "temp");
    const power = channelKey("power", "temp");
    expect(imu).not.toBe(power);
  });

  it("does not collide when a dotted name straddles the packet boundary", () => {
    // A "." join would map both of these to "a.b.c"; the canonical key must not.
    expect(channelKey("a", "b.c")).not.toBe(channelKey("a.b", "c"));
  });

  it("round-trips through parseKey", () => {
    expect(parseKey(channelKey("imu", "temp"))).toEqual({ packet: "imu", name: "temp" });
    expect(parseKey(channelKey("", "estop"))).toEqual({ packet: "", name: "estop" });
    expect(parseKey(channelKey("a.b", "c"))).toEqual({ packet: "a.b", name: "c" });
  });

  it("is stable/equal for the same identity", () => {
    expect(channelKey("imu", "temp")).toBe(channelKey("imu", "temp"));
  });
});

describe("channelLabel", () => {
  it("renders packet.name for packeted channels", () => {
    expect(channelLabel("imu", "temp")).toBe("imu.temp");
  });
  it("renders the bare name for ad-hoc channels", () => {
    expect(channelLabel("", "drive.estop")).toBe("drive.estop");
  });
});

describe("groupByPacket", () => {
  it("groups channels by packet, ad-hoc bucket last", () => {
    const groups = groupByPacket([
      info({ packet: "power", name: "temp" }),
      info({ packet: "imu", name: "temp" }),
      info({ packet: "", name: "loose" }),
      info({ packet: "imu", name: "pitch_deg" }),
    ]);
    expect(groups.map((g) => g.packet)).toEqual(["imu", "power", ""]);
    // ad-hoc flagged
    expect(groups[groups.length - 1]!.adHoc).toBe(true);
    // imu keeps both its params in catalogue order
    expect(groups[0]!.channels.map((c) => c.name)).toEqual(["temp", "pitch_deg"]);
  });

  it("returns no ad-hoc bucket when every channel is packeted", () => {
    const groups = groupByPacket([info({ packet: "imu", name: "temp" })]);
    expect(groups.every((g) => !g.adHoc)).toBe(true);
  });
});

describe("subscribeNames", () => {
  it("collapses selected keys to the distinct channel NAMES sent on the wire", () => {
    // Two packets, same param name -> one wire name "temp"; server routes by name.
    const keys = [channelKey("imu", "temp"), channelKey("power", "temp"), channelKey("imu", "pitch_deg")];
    expect(subscribeNames(keys).sort()).toEqual(["pitch_deg", "temp"]);
  });

  it("is empty for an empty selection", () => {
    expect(subscribeNames([])).toEqual([]);
  });
});
