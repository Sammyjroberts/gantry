import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import { ValueKind } from "@gantry/api-client";

// Mock the client so the smoke test does no real network I/O: ListChannels
// resolves a small catalogue; Subscribe yields nothing then ends.
vi.mock("@gantry/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@gantry/api-client")>();
  return {
    ...actual,
    createLiveClient: () => ({
      listChannels: async () => ({
        devices: [
          {
            deviceId: "rover-01",
            channels: [
              // Collision case: two packets expose the same param name "temp"
              // with different kinds. (packet, name) keying keeps them distinct.
              { name: "temp", kind: actual.ValueKind.F64, unit: "degC", description: "", packet: "imu" },
              { name: "temp", kind: actual.ValueKind.I64, unit: "degC", description: "", packet: "power" },
              { name: "current_a", kind: actual.ValueKind.F64, unit: "A", description: "", packet: "drive" },
              // Ad-hoc channel (empty packet) -> "ad hoc" bucket.
              { name: "drive.estop", kind: actual.ValueKind.BOOL, unit: "", description: "", packet: "" },
            ],
          },
        ],
      }),
      // eslint-disable-next-line @typescript-eslint/no-empty-function
      subscribe: async function* () {
        /* no frames in the smoke test */
      },
    }),
  };
});

afterEach(() => cleanup());

describe("App", () => {
  it("renders the console shell and the grouped channel catalogue", async () => {
    const { App } = await import("./App");
    render(<App />);

    // Chrome renders synchronously.
    expect(screen.getByText("GANTRY")).toBeTruthy();
    expect(screen.getByText(/select channels/i)).toBeTruthy();

    // Packet groups arrive from the (mocked) ListChannels call.
    expect(await screen.findByText("imu")).toBeTruthy();
    expect(await screen.findByText("power")).toBeTruthy();
    expect(await screen.findByText("drive")).toBeTruthy();
    expect(await screen.findByText("ad hoc")).toBeTruthy();

    // The colliding param name "temp" appears once under each of its packets.
    expect(screen.getAllByText("temp")).toHaveLength(2);
    // Ad-hoc channel keeps its bare dotted name.
    expect(screen.getByText("drive.estop")).toBeTruthy();
  });

  it("exposes the expected ValueKind enum", () => {
    expect(ValueKind.BOOL).toBe(3);
  });
});
