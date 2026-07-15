import { describe, it, expect } from "vitest";
import { channelKey } from "../channel";
import {
  LAYOUT_VERSION,
  buildCatalogIndex,
  layoutChannelKeys,
  makePanel,
  parseLayout,
  panelBindings,
  readingAt,
  resolveBinding,
  resolveBindings,
  roundTrip,
  seedDefaultLayout,
  serializeLayout,
  type Panel,
  type PanelBinding,
  type TimeseriesConfig,
} from "./layout";

const bind = (packet: string, channel: string, deviceId = "dev-1"): PanelBinding => ({
  deviceId,
  packet,
  channel,
});

describe("layout envelope round-trip", () => {
  it("preserves a mixed panel set through serialize → parse", () => {
    const panels: Panel[] = [
      makePanel("timeseries", 0, 0, {
        channels: [bind("imu", "pitch"), bind("power", "temp")],
        windowSec: 30,
        yLock: true,
      }),
      { ...makePanel("value", 6, 0, { channel: bind("power", "volts") }), title: "Bus V" },
      makePanel("led", 9, 0, { channel: bind("imu", "fault"), onLabel: "FAULT" }),
      makePanel("scene3d", 0, 6, { deviceId: "dev-1" }),
      makePanel("video", 5, 6, { cameraId: "bench-cam" }),
      makePanel("sql", 0, 14, { sql: "SELECT 1;", refreshMs: 5000 }),
      makePanel("state", 8, 6, { channel: bind("imu", "mode") }),
    ];
    const back = roundTrip(panels);
    expect(back).toEqual(panels);
  });

  it("stamps the current version and echoes it in the JSON", () => {
    const json = serializeLayout([makePanel("timeseries")]);
    expect(JSON.parse(json).v).toBe(LAYOUT_VERSION);
  });

  it("tolerates garbage / partial documents without throwing", () => {
    expect(parseLayout(null)).toEqual([]);
    expect(parseLayout("")).toEqual([]);
    expect(parseLayout("not json{")).toEqual([]);
    expect(parseLayout('{"v":1}')).toEqual([]);
    expect(parseLayout('{"v":1,"panels":[{"type":"nope"}]}')).toEqual([]);
  });

  it("repairs a panel missing grid/config and drops typeless panels", () => {
    const json = JSON.stringify({
      v: 1,
      panels: [
        { id: "a", type: "value" }, // no grid, no config
        { id: "b" }, // no type -> dropped
        { id: "c", type: "timeseries", grid: { x: 2, y: 3 }, config: { channels: "bad" } },
      ],
    });
    const panels = parseLayout(json);
    expect(panels.map((p) => p.id)).toEqual(["a", "c"]);
    expect(panels[0]!.grid).toEqual({ x: 0, y: 0, w: 3, h: 3 });
    expect((panels[1]!.config as TimeseriesConfig).channels).toEqual([]);
    expect(panels[1]!.grid.x).toBe(2);
  });
});

describe("migration seed", () => {
  it("seeds one empty chart for an empty selection", () => {
    const panels = seedDefaultLayout([]);
    expect(panels).toHaveLength(1);
    expect(panels[0]!.type).toBe("timeseries");
    expect((panels[0]!.config as TimeseriesConfig).channels).toEqual([]);
  });

  it("splits a wide selection across two stacked charts", () => {
    const chans = Array.from({ length: 8 }, (_, i) => ({
      deviceId: "dev-1",
      packet: "imu",
      channel: `c${i}`,
    }));
    const panels = seedDefaultLayout(chans);
    expect(panels).toHaveLength(2);
    const a = (panels[0]!.config as TimeseriesConfig).channels;
    const b = (panels[1]!.config as TimeseriesConfig).channels;
    expect(a.length + b.length).toBe(8);
    expect(panels[1]!.grid.x).toBe(6); // side-by-side seed
  });

  it("keeps a small selection in a single chart", () => {
    const panels = seedDefaultLayout([{ deviceId: "d", packet: "imu", channel: "pitch" }]);
    expect(panels).toHaveLength(1);
    expect((panels[0]!.config as TimeseriesConfig).channels).toHaveLength(1);
  });
});

describe("binding resolution", () => {
  const catalog = buildCatalogIndex([
    { deviceId: "dev-1", packet: "imu", channel: "pitch" },
    { deviceId: "dev-1", packet: "power", channel: "temp" },
  ]);

  it("resolves a present binding to its canonical key", () => {
    const r = resolveBinding(bind("imu", "pitch"), catalog);
    expect(r.resolved).toBe(true);
    expect(r.key).toBe(channelKey("imu", "pitch"));
  });

  it("flags an absent binding as unresolved (never cross-wires)", () => {
    const r = resolveBinding(bind("imu", "gone"), catalog);
    expect(r.resolved).toBe(false);
    expect(r.key).toBe(channelKey("imu", "gone"));
  });

  it("distinguishes packet-siblings with the same channel name", () => {
    const cat = buildCatalogIndex([{ deviceId: "d", packet: "power", channel: "temp" }]);
    expect(resolveBinding(bind("power", "temp"), cat).resolved).toBe(true);
    expect(resolveBinding(bind("imu", "temp"), cat).resolved).toBe(false);
  });

  it("resolves a list", () => {
    const rs = resolveBindings([bind("imu", "pitch"), bind("imu", "gone")], catalog);
    expect(rs.map((r) => r.resolved)).toEqual([true, false]);
  });
});

describe("panel channel keys / subscription union", () => {
  it("collects the distinct union across panels", () => {
    const panels: Panel[] = [
      makePanel("timeseries", 0, 0, { channels: [bind("imu", "pitch"), bind("imu", "roll")] }),
      makePanel("value", 6, 0, { channel: bind("imu", "pitch") }), // dup
      makePanel("led", 9, 0, { channel: bind("power", "fault") }),
      makePanel("scene3d", 0, 6, { deviceId: "dev-1" }), // no channels
    ];
    const keys = layoutChannelKeys(panels).sort();
    expect(keys).toEqual(
      [channelKey("imu", "pitch"), channelKey("imu", "roll"), channelKey("power", "fault")].sort(),
    );
  });

  it("panelBindings is empty for non-telemetry panels", () => {
    expect(panelBindings(makePanel("sql"))).toEqual([]);
    expect(panelBindings(makePanel("video"))).toEqual([]);
    expect(panelBindings(makePanel("led"))).toEqual([]); // unbound led
  });
});

describe("value-at-cursor (led / value panels)", () => {
  it("returns the latest ring value in live mode", () => {
    expect(readingAt({ live: true, latest: 42, cursorSec: 0, series: null })).toBe(42);
  });

  it("returns null when live and nothing has arrived", () => {
    expect(readingAt({ live: true, latest: null, cursorSec: 0, series: null })).toBeNull();
  });

  it("reads the value at/or-before the cursor in replay", () => {
    const series = { x: [10, 20, 30], y: [1, 2, 3] as (number | null)[] };
    expect(readingAt({ live: false, latest: 999, cursorSec: 25, series })).toBe(2);
    expect(readingAt({ live: false, latest: 999, cursorSec: 30, series })).toBe(3);
  });

  it("returns null in replay before the first sample", () => {
    const series = { x: [10, 20], y: [1, 2] as (number | null)[] };
    expect(readingAt({ live: false, latest: 5, cursorSec: 5, series })).toBeNull();
  });

  it("skips null gaps backward in replay", () => {
    const series = { x: [10, 20, 30], y: [1, null, null] as (number | null)[] };
    expect(readingAt({ live: false, latest: 0, cursorSec: 30, series })).toBe(1);
  });
});
