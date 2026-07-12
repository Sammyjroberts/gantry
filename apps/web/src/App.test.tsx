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
              { name: "drive.motor_left.current_a", kind: actual.ValueKind.F64, unit: "A", description: "" },
              { name: "drive.estop", kind: actual.ValueKind.BOOL, unit: "", description: "" },
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
  it("renders the console shell and the channel catalogue", async () => {
    const { App } = await import("./App");
    render(<App />);

    // Chrome renders synchronously.
    expect(screen.getByText("GANTRY")).toBeTruthy();
    expect(screen.getByText(/select channels/i)).toBeTruthy();

    // Channels arrive from the (mocked) ListChannels call.
    expect(await screen.findByText("drive.motor_left.current_a")).toBeTruthy();
    expect(await screen.findByText("drive.estop")).toBeTruthy();
  });

  it("exposes the expected ValueKind enum", () => {
    expect(ValueKind.BOOL).toBe(3);
  });
});
