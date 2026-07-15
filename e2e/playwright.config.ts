import { defineConfig, devices } from "@playwright/test";

/**
 * Gantry browser e2e — the top of the test pyramid: the real Edge binary + a
 * live telemetry feeder driven through Chromium. Unit (per-package) and
 * integration (in-process App/tlm) tiers live in the language toolchains; see
 * e2e/README.md.
 *
 * One worker, not parallel: every spec shares ONE Edge instance + feeder, and
 * some specs mutate server state (experiments, video chunks). Serial keeps them
 * deterministic. retries=1 absorbs the rare cold-start hiccup without masking
 * real failures.
 */
export default defineConfig({
  testDir: "./specs",
  fullyParallel: false,
  workers: 1,
  retries: 1,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI
    ? [["list"], ["html", { open: "never", outputFolder: "playwright-report" }]]
    : [["list"]],
  outputDir: "test-results",
  globalSetup: "./harness/global-setup.ts",
  globalTeardown: "./harness/global-teardown.ts",
  use: {
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    video: "off",
    permissions: ["camera"],
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
  },
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        launchOptions: {
          args: [
            // Deterministic fake camera for the video-capture spec.
            "--use-fake-device-for-media-stream",
            "--use-fake-ui-for-media-stream",
            // Software WebGL (SwiftShader) so the 3D panel mounts a real GL
            // context under headless CI.
            "--use-gl=angle",
            "--use-angle=swiftshader",
            "--enable-unsafe-swiftshader",
            "--ignore-gpu-blocklist",
          ],
        },
      },
    },
  ],
});
