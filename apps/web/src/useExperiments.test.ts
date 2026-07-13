import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, waitFor, act, cleanup } from "@testing-library/react";
import type { Experiment } from "@gantry/api-client";
import { useExperiments } from "./useExperiments";

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

// Mutable fixture the mocked client reads from / records into.
let listData: Experiment[] = [];
const client = {
  listExperiments: vi.fn(async () => ({ experiments: listData })),
  startExperiment: vi.fn(async (req: { name: string; deviceId: string }) => ({
    experiment: mkExp({ id: "new", name: req.name, deviceId: req.deviceId, startNs: 500n * SEC }),
  })),
  stopExperiment: vi.fn(async (req: { id: string }) => ({
    experiment: mkExp({ id: req.id, name: "run", startNs: 100n * SEC, endNs: 200n * SEC }),
  })),
  updateExperiment: vi.fn(async (req: { id: string; name: string; notes: string }) => ({
    experiment: mkExp({ id: req.id, name: req.name, notes: req.notes, startNs: 100n * SEC }),
  })),
  deleteExperiment: vi.fn(async () => ({})),
};

vi.mock("@gantry/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@gantry/api-client")>();
  return { ...actual, createExperimentClient: () => client };
});

beforeEach(() => {
  listData = [];
  vi.clearAllMocks();
});
afterEach(() => cleanup());

function render() {
  return renderHook(() =>
    useExperiments({ baseUrl: "http://x", deviceId: "", pollMs: 0 }),
  );
}

describe("useExperiments", () => {
  it("loads and sorts the list newest-first on mount", async () => {
    listData = [
      mkExp({ id: "a", startNs: 100n }),
      mkExp({ id: "b", startNs: 300n }),
      mkExp({ id: "c", startNs: 200n }),
    ];
    const { result } = render();
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.experiments.map((e) => e.id)).toEqual(["b", "c", "a"]);
  });

  it("start() calls the RPC and merges the returned run into the running set", async () => {
    const { result } = render();
    await waitFor(() => expect(result.current.loading).toBe(false));

    await act(async () => {
      await result.current.start({ name: "burn-in" });
    });

    expect(client.startExperiment).toHaveBeenCalledWith(
      expect.objectContaining({ name: "burn-in", notes: "", deviceId: "", startNs: 0n }),
    );
    expect(result.current.experiments.some((e) => e.id === "new")).toBe(true);
    expect(result.current.running.map((e) => e.id)).toContain("new");
  });

  it("stop() upserts the finished run so it leaves the running set", async () => {
    listData = [mkExp({ id: "x", name: "run", startNs: 100n * SEC, endNs: 0n })];
    const { result } = render();
    await waitFor(() => expect(result.current.running.map((e) => e.id)).toEqual(["x"]));

    await act(async () => {
      await result.current.stop("x");
    });

    expect(client.stopExperiment).toHaveBeenCalledWith({ id: "x", endNs: 0n });
    expect(result.current.running.map((e) => e.id)).not.toContain("x");
    expect(result.current.experiments.find((e) => e.id === "x")!.endNs).toBe(200n * SEC);
  });

  it("update() edits name/notes via the RPC and upserts the result", async () => {
    listData = [mkExp({ id: "x", name: "old", notes: "", startNs: 100n * SEC })];
    const { result } = render();
    await waitFor(() => expect(result.current.experiments).toHaveLength(1));

    await act(async () => {
      await result.current.update("x", "new-name", "checked bearings");
    });

    expect(client.updateExperiment).toHaveBeenCalledWith({
      id: "x",
      name: "new-name",
      notes: "checked bearings",
    });
    const row = result.current.experiments.find((e) => e.id === "x")!;
    expect(row.name).toBe("new-name");
    expect(row.notes).toBe("checked bearings");
  });

  it("remove() deletes locally after the RPC resolves", async () => {
    listData = [mkExp({ id: "x", startNs: 100n }), mkExp({ id: "y", startNs: 200n })];
    const { result } = render();
    await waitFor(() => expect(result.current.experiments).toHaveLength(2));

    await act(async () => {
      await result.current.remove("x");
    });

    expect(client.deleteExperiment).toHaveBeenCalledWith({ id: "x" });
    expect(result.current.experiments.map((e) => e.id)).toEqual(["y"]);
  });

  it("surfaces RPC errors as a string", async () => {
    const { result } = render();
    await waitFor(() => expect(result.current.loading).toBe(false));
    client.startExperiment.mockRejectedValueOnce(new Error("boom"));

    await act(async () => {
      await result.current.start({ name: "z" });
    });
    expect(result.current.error).toBe("boom");
  });
});
