/**
 * Smoke test for the lazy Scene3D chunk.
 *
 * COVERED: the module imports and mounts under jsdom with a mocked model
 * server (GET /models/ → empty listing → the generated primitive fallback), the
 * primitive robot is constructed with real three.js geometry, and the control
 * surface (bindings + URDF editor) renders and is wired.
 *
 * NOT COVERED (documented, intentional): real WebGL rendering and the
 * per-frame imperative drive. @react-three/fiber's <Canvas> and useFrame are
 * mocked out — jsdom has no WebGL context — so the scene graph, lighting, orbit
 * controls and the useFrame pose update are not exercised here. The pure pieces
 * behind the drive (binding math + replay cursor lookup) are covered directly in
 * pose.test.ts; live rendering needs a real browser (see the report).
 */
import { describe, it, expect, afterEach, vi, beforeEach } from "vitest";
import { render, screen, cleanup, waitFor } from "@testing-library/react";
import type { Sampler } from "./pose";

vi.mock("@react-three/fiber", () => ({
  Canvas: ({ children }: { children: React.ReactNode }) => (
    <div data-testid="r3f-canvas">{children}</div>
  ),
  useFrame: () => {},
}));

vi.mock("@react-three/drei", () => ({
  OrbitControls: () => null,
  Grid: () => null,
  ContactShadows: () => null,
  GizmoHelper: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  GizmoViewport: () => null,
}));

afterEach(() => cleanup());
beforeEach(() => {
  localStorage.clear();
  // Model server: empty listing → primitive fallback, no file fetches.
  global.fetch = vi.fn(async () => ({
    ok: true,
    status: 200,
    json: async () => ({ files: [] }),
    text: async () => "",
  })) as unknown as typeof fetch;
});

describe("Scene3D (lazy chunk smoke test)", () => {
  it("mounts with a mocked model server and shows the editor + bindings", async () => {
    const { default: Scene3D } = await import("./Scene3D");
    const sampleRef = { current: (() => null) as Sampler };

    render(
      <Scene3D
        baseUrl="http://localhost:4780"
        devices={["mr-wobbles"]}
        channels={[{ key: "imupitch", label: "imu.pitch", device: "mr-wobbles", unit: "deg" }]}
        sampleRef={sampleRef}
        replaying={false}
        onBoundChannelsChange={() => {}}
        onClose={() => {}}
      />,
    );

    // The (mocked) canvas host is present.
    expect(screen.getByTestId("r3f-canvas")).toBeTruthy();
    // Control surface rendered.
    expect(screen.getByText("Attitude")).toBeTruthy();
    expect(screen.getByPlaceholderText(/URDF XML/i)).toBeTruthy();

    // The empty listing resolves to the primitive fallback (async effect), which
    // reveals the primitive dimension editor.
    await waitFor(() => expect(screen.getByText("Primitive dims")).toBeTruthy());
  });

  it("reports bound channel keys upward for subscription", async () => {
    const { default: Scene3D } = await import("./Scene3D");
    const sampleRef = { current: (() => null) as Sampler };
    const onBound = vi.fn();

    render(
      <Scene3D
        baseUrl="http://localhost:4780"
        devices={["mr-wobbles"]}
        channels={[]}
        sampleRef={sampleRef}
        replaying={false}
        onBoundChannelsChange={onBound}
        onClose={() => {}}
      />,
    );
    // Called on mount with the (empty) default bindings' channel set.
    await waitFor(() => expect(onBound).toHaveBeenCalled());
    expect(onBound).toHaveBeenCalledWith([]);
  });
});
