import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, waitFor, act, cleanup } from "@testing-library/react";
import { ConnectError, Code, type Hardware } from "@gantry/api-client";
import { useHardware } from "./useHardware";
import { decodeVizConfig, encodeVizConfig, LEGACY_POSE_PREFIX } from "./hardware";
import { defaultBindings } from "./pose";

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

// Mutable server-side fixtures the mocked client reads from / records into.
let rows: Hardware[] = [];
let unconfigured: string[] = [];
let getImpl: (deviceId: string) => Promise<{ hardware: Hardware }> = async (deviceId) => {
  const hw = rows.find((r) => r.deviceId === deviceId);
  if (!hw) throw new ConnectError("not found", Code.NotFound);
  return { hardware: hw };
};

const client = {
  listHardware: vi.fn(async () => ({ hardware: rows, unconfiguredDeviceIds: unconfigured })),
  getHardware: vi.fn(async (req: { deviceId: string }) => getImpl(req.deviceId)),
  upsertHardware: vi.fn(async (req: { hardware: Partial<Hardware> }) => {
    const merged = mkHardware({ ...req.hardware, createdNs: 1n, updatedNs: 2n });
    rows = [...rows.filter((r) => r.deviceId !== merged.deviceId), merged];
    return { hardware: merged };
  }),
  deleteHardware: vi.fn(async (req: { deviceId: string }) => {
    rows = rows.filter((r) => r.deviceId !== req.deviceId);
    return {};
  }),
};

vi.mock("@gantry/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@gantry/api-client")>();
  return { ...actual, createHardwareClient: () => client };
});

beforeEach(() => {
  rows = [];
  unconfigured = [];
  getImpl = async (deviceId) => {
    const hw = rows.find((r) => r.deviceId === deviceId);
    if (!hw) throw new ConnectError("not found", Code.NotFound);
    return { hardware: hw };
  };
  localStorage.clear();
  vi.clearAllMocks();
});
afterEach(() => cleanup());

function render() {
  return renderHook(() => useHardware({ baseUrl: "http://x", pollMs: 0 }));
}

describe("useHardware", () => {
  it("loads the configured list + unconfigured set on mount", async () => {
    rows = [mkHardware({ deviceId: "rover-1", displayName: "Rover One" })];
    unconfigured = ["arm-7"];
    const { result } = render();
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.hardware.map((h) => h.deviceId)).toEqual(["rover-1"]);
    expect(result.current.unconfigured).toEqual(["arm-7"]);
    expect(result.current.displayName("rover-1")).toBe("Rover One");
    expect(result.current.displayName("arm-7")).toBe("arm-7"); // fallback
  });

  it("upsert merges the patch onto the known row (never clobbers other fields)", async () => {
    rows = [
      mkHardware({ deviceId: "rover-1", displayName: "Rover One", notes: "keep me", vizConfigJson: "{}" }),
    ];
    const { result } = render();
    await waitFor(() => expect(result.current.hardware).toHaveLength(1));

    await act(async () => {
      await result.current.upsert({ deviceId: "rover-1", displayName: "Renamed" });
    });

    // The wire message preserved notes + vizConfigJson from the known row.
    const sent = client.upsertHardware.mock.calls[0]![0].hardware;
    expect(sent).toMatchObject({
      deviceId: "rover-1",
      displayName: "Renamed",
      notes: "keep me",
      vizConfigJson: "{}",
    });
    expect(result.current.displayName("rover-1")).toBe("Renamed");
  });

  it("saveVizConfig writes a versioned envelope merged onto the row", async () => {
    rows = [mkHardware({ deviceId: "rover-1", displayName: "Keep Name" })];
    const { result } = render();
    await waitFor(() => expect(result.current.hardware).toHaveLength(1));

    const b = defaultBindings();
    b.pitch = { channelKey: "imu|pitch", unit: "deg", sign: 1 };
    await act(async () => {
      await result.current.saveVizConfig("rover-1", b);
    });

    const sent = client.upsertHardware.mock.calls.at(-1)![0].hardware;
    expect(sent.displayName).toBe("Keep Name"); // preserved
    const decoded = decodeVizConfig(sent.vizConfigJson);
    expect(decoded?.pitch.channelKey).toBe("imu|pitch");
  });

  it("loadVizConfig returns the server bindings when present", async () => {
    const b = defaultBindings();
    b.yaw = { channelKey: "imu|yaw", unit: "rad", sign: -1 };
    rows = [mkHardware({ deviceId: "rover-1", vizConfigJson: encodeVizConfig(b) })];
    const { result } = render();
    await waitFor(() => expect(result.current.loading).toBe(false));

    let loaded = defaultBindings();
    await act(async () => {
      loaded = await result.current.loadVizConfig("rover-1");
    });
    expect(loaded.yaw).toEqual({ channelKey: "imu|yaw", unit: "rad", sign: -1 });
  });

  it("migration: pushes legacy localStorage bindings up and removes them when server is empty", async () => {
    // Server has no row for this device (getHardware → NotFound); localStorage does.
    const legacy = defaultBindings();
    legacy.roll = { channelKey: "imu|roll", unit: "deg", sign: -1 };
    localStorage.setItem(`${LEGACY_POSE_PREFIX}rover-1`, JSON.stringify(legacy));

    const { result } = render();
    await waitFor(() => expect(result.current.loading).toBe(false));

    let loaded = defaultBindings();
    await act(async () => {
      loaded = await result.current.loadVizConfig("rover-1");
    });

    // Returned the legacy bindings...
    expect(loaded.roll).toEqual({ channelKey: "imu|roll", unit: "deg", sign: -1 });
    // ...pushed them to the server (an upsert with the encoded envelope)...
    const sent = client.upsertHardware.mock.calls.at(-1)![0].hardware;
    expect(decodeVizConfig(sent.vizConfigJson)?.roll.channelKey).toBe("imu|roll");
    // ...and removed the localStorage entry.
    expect(localStorage.getItem(`${LEGACY_POSE_PREFIX}rover-1`)).toBeNull();
  });

  it("migration: no localStorage → defaults, no upsert", async () => {
    const { result } = render();
    await waitFor(() => expect(result.current.loading).toBe(false));

    let loaded = defaultBindings();
    await act(async () => {
      loaded = await result.current.loadVizConfig("brand-new");
    });
    expect(loaded).toEqual(defaultBindings());
    expect(client.upsertHardware).not.toHaveBeenCalled();
  });

  it("panelDefaults round-trips via savePanelDefaults", async () => {
    rows = [mkHardware({ deviceId: "rover-1" })];
    const { result } = render();
    await waitFor(() => expect(result.current.hardware).toHaveLength(1));

    await act(async () => {
      await result.current.savePanelDefaults("rover-1", ["drive|speed", "imu|pitch"]);
    });
    // The saved defaults are now readable from the merged row.
    expect(result.current.panelDefaults("rover-1")).toEqual(["drive|speed", "imu|pitch"]);
  });
});
