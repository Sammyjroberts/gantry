import { test, expect, type Page } from "../harness/fixtures";
import type { HarnessState } from "../harness/util";
import { uniqueName } from "./_helpers";

// The telemetry feeder streams under this device id (see harness/feeder.mjs).
const FEEDER_DEVICE = "sim-rover";

/**
 * Open a console sub-page directly, preserving the harness `?api=` base.
 *
 * The promote/config flow lives on the /hardware and /hardware/:deviceId pages.
 * These pages re-resolve the API base from window.location on mount, but in-app
 * react-router navigation drops the `?api=` query (see the report notes), so we
 * load each page with the query intact rather than click through nav links —
 * exercising the real HardwareService round-trips deterministically.
 */
async function openPage(page: Page, state: HarnessState, path: string): Promise<void> {
  const url = `${state.webURL}${path}?api=${encodeURIComponent(state.apiURL)}`;
  await page.goto(url, { waitUntil: "domcontentloaded" });
  await expect(page.locator(".statusbar")).toBeVisible();
}

// Spec (hardware) — the device-identity slice end to end against the real Bench:
//   promote the feeder device → rename it → the name surfaces on the hardware
//   list after a fresh load (proving identity round-trips through HardwareService,
//   not localStorage). The promote/config flow moved to /hardware/:deviceId.
test("promote → rename → name persists on the hardware list (server-side)", async ({
  console: page,
  state,
}) => {
  const displayName = uniqueName("Rover");

  // ---- the feeder device is visible on the hardware list (seen/unconfigured) ----
  await openPage(page, state, "/hardware");
  await expect(page.getByRole("heading", { name: "Hardware" })).toBeVisible();
  await expect(page.locator(".hw-card").filter({ hasText: FEEDER_DEVICE }).first()).toBeVisible();

  // ---- detail page: set the display name and save (promotes if unconfigured) ----
  await openPage(page, state, `/hardware/${encodeURIComponent(FEEDER_DEVICE)}`);
  await expect(page.locator(".hardware-detail-page")).toBeVisible();
  await expect(page.locator(".hw-detail-id")).toHaveText(FEEDER_DEVICE);

  const nameField = page.locator(".hw-field").filter({ hasText: "Display name" }).locator("input");
  await nameField.fill(displayName);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes("HardwareService/UpsertHardware") && r.ok()),
    page.getByTestId("hw-save").click(),
  ]);

  // ---- fresh load of the list: the display name persisted server-side ----
  await openPage(page, state, "/hardware");
  await expect(page.locator(".hw-card-name").filter({ hasText: displayName })).toBeVisible();

  // And the detail form re-seeds from the server row on a fresh load, too.
  await openPage(page, state, `/hardware/${encodeURIComponent(FEEDER_DEVICE)}`);
  await expect(
    page.locator(".hw-field").filter({ hasText: "Display name" }).locator("input"),
  ).toHaveValue(displayName);
});

// Spec (hardware, 3D binding) — set a pose binding on the detail page's embedded
// Scene3D and prove it round-trips through viz_config_json across a reload.
// (Previously fixme'd on a React #185 feedback loop; the app now memoizes
// onBoundChannelsChange, so this must pass.)
test("3D pose binding survives reload (server-side)", async ({ console: page, state }) => {
  await openPage(page, state, `/hardware/${encodeURIComponent(FEEDER_DEVICE)}`);
  const controls = page.locator(".hw-scene-host .s3-controls");
  await expect(controls).toBeVisible();

  const pitchSelect = controls.locator(".s3-select").first();
  await pitchSelect.selectOption({ label: "imu.pitch_deg (deg)" });
  const boundValue = await pitchSelect.inputValue();
  expect(boundValue).not.toBe("");
  await page.waitForResponse(
    (r) => r.url().includes("HardwareService/UpsertHardware") && r.ok(),
    { timeout: 15_000 },
  );

  await openPage(page, state, `/hardware/${encodeURIComponent(FEEDER_DEVICE)}`);
  const pitchSelect2 = page.locator(".hw-scene-host .s3-controls .s3-select").first();
  await expect.poll(async () => pitchSelect2.inputValue(), { timeout: 10_000 }).toBe(boundValue);
});
