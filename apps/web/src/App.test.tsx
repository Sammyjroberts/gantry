import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import { ValueKind } from "@gantry/api-client";

// Mock every client factory so the smoke test does no real network I/O and no
// uPlot canvas work (the loaded workspace has no panels). ListChannels resolves
// a small catalogue; the workspace list has one empty workspace.
vi.mock("@gantry/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@gantry/api-client")>();
  const noStream = async function* () {
    /* no frames */
  };
  return {
    ...actual,
    createLiveClient: () => ({
      listChannels: async () => ({
        devices: [
          {
            deviceId: "rover-01",
            channels: [
              { name: "temp", kind: actual.ValueKind.F64, unit: "degC", description: "", packet: "imu" },
              { name: "temp", kind: actual.ValueKind.I64, unit: "degC", description: "", packet: "power" },
              { name: "current_a", kind: actual.ValueKind.F64, unit: "A", description: "", packet: "drive" },
              { name: "drive.estop", kind: actual.ValueKind.BOOL, unit: "", description: "", packet: "" },
            ],
          },
        ],
      }),
      subscribe: noStream,
    }),
    createWorkspaceClient: () => ({
      listWorkspaces: async () => ({
        workspaces: [
          { id: "ws1", name: "bench", layoutJson: "", createdNs: 1n, updatedNs: 2n },
        ],
      }),
      getWorkspace: async () => ({
        workspace: { id: "ws1", name: "bench", layoutJson: '{"v":1,"panels":[]}', createdNs: 1n, updatedNs: 2n },
      }),
      upsertWorkspace: async ({ workspace }: { workspace: unknown }) => ({ workspace }),
      deleteWorkspace: async () => ({}),
    }),
    createExperimentClient: () => ({
      listExperiments: async () => ({ experiments: [] }),
    }),
    createQueryClient: () => ({
      queryRange: async () => ({ series: [], truncatedByRetention: false }),
    }),
    createHardwareClient: () => ({
      listHardware: async () => ({ hardware: [], unconfiguredDeviceIds: [] }),
      getHardware: async () => ({ hardware: undefined }),
    }),
  };
});

afterEach(() => cleanup());

describe("App", () => {
  it("renders the nav rail and the grouped channel catalogue", async () => {
    const { App } = await import("./App");
    render(<App />);

    // Nav rail brand + links.
    expect(screen.getByText("GANTRY")).toBeTruthy();
    expect(screen.getByTestId("nav-workspace")).toBeTruthy();
    expect(screen.getByTestId("nav-hardware")).toBeTruthy();
    expect(screen.getByTestId("nav-experiments")).toBeTruthy();
    expect(screen.getByTestId("nav-data")).toBeTruthy();

    // Packet groups arrive from the (mocked) ListChannels call in the sidebar.
    expect(await screen.findByText("imu")).toBeTruthy();
    expect(await screen.findByText("power")).toBeTruthy();
    expect(await screen.findByText("drive")).toBeTruthy();

    // The colliding param name "temp" appears once under each of its packets.
    expect(screen.getAllByText("temp")).toHaveLength(2);
    expect(screen.getByText("drive.estop")).toBeTruthy();
  });

  it("exposes the expected ValueKind enum", () => {
    expect(ValueKind.BOOL).toBe(3);
  });
});
