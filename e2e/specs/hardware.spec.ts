import { test, expect, type Page } from "../harness/fixtures";
import { toggleDock, uniqueName } from "./_helpers";

// The telemetry feeder streams under this device id (see harness/feeder.mjs).
const FEEDER_DEVICE = "sim-rover";

/** Open the hardware panel (topbar ⬡ hardware toggle). */
async function openHardware(page: Page): Promise<void> {
  await page.locator(".ctl-btn").filter({ hasText: "hardware" }).click();
  await expect(page.locator(".hw-panel")).toBeVisible();
}

async function closeHardware(page: Page): Promise<void> {
  await page.locator(".hw-close").click();
  await expect(page.locator(".hw-panel")).toHaveCount(0);
}

// Spec (hardware) — the device identity slice end to end against the real Bench:
//   promote the feeder device → rename it → the name surfaces in the channel
//   sidebar → set a 3D pose binding → reload → the binding survived server-side
//   (proving viz_config_json round-trips, not localStorage).
test("promote → rename → sidebar name; 3D binding survives reload (server-side)", async ({
  console: page,
}) => {
  const displayName = uniqueName("Rover");

  // ---- promote + rename via the hardware panel ----
  await openHardware(page);

  // The feeder device shows up "seen but unconfigured" until promoted. On a
  // retry it may already be configured; handle both.
  const promote = page
    .locator(".hw-row--unconfigured")
    .filter({ hasText: FEEDER_DEVICE })
    .locator(".hw-btn--primary");
  if (await promote.count()) {
    await promote.first().click();
  }

  // The configured row for the device now exists; open its editor. (Once the
  // inline editor is open the row no longer shows the device id as text, so we
  // target the sole open .hw-edit form for the inputs.)
  const configuredRow = page
    .locator(".hw-row:not(.hw-row--unconfigured)")
    .filter({ hasText: FEEDER_DEVICE });
  await expect(configuredRow).toBeVisible();
  await configuredRow.getByRole("button", { name: "edit" }).click();

  const editForm = page.locator(".hw-panel .hw-edit");
  await expect(editForm).toBeVisible();
  await editForm.locator(".hw-input").first().fill(displayName);
  await editForm.getByRole("button", { name: "save" }).click();

  // The configured row reflects the new name.
  await expect(configuredRow.locator(".hw-name")).toHaveText(displayName);

  await closeHardware(page);

  // ---- the display name surfaces in the channel sidebar header ----
  await expect(
    page.locator(".device-name").filter({ hasText: displayName }),
  ).toBeVisible();

  // ---- set a 3D pose binding, let it save (debounced ~1s), then reload ----
  await toggleDock(page, "3D");
  const dock = page.locator(".scene3d-dock");
  await expect(dock.locator(".s3-controls")).toBeVisible();

  // The first channel <select> is the pitch attitude binding; option 0 is
  // "— none —", so index 1 is the first real channel.
  const pitchSelect = dock.locator(".s3-select").first();
  await pitchSelect.selectOption({ index: 1 });
  const boundValue = await pitchSelect.inputValue();
  expect(boundValue).not.toBe("");

  // Wait out the debounce (~1s) + the upsert round-trip before reloading.
  await page.waitForTimeout(2000);

  await page.reload({ waitUntil: "domcontentloaded" });
  await expect(page.locator(".statusbar")).toBeVisible();

  // Re-open the 3D dock; the per-device viz config is loaded from the server.
  await toggleDock(page, "3D");
  const dock2 = page.locator(".scene3d-dock");
  await expect(dock2.locator(".s3-controls")).toBeVisible();

  // The pitch binding persisted server-side (no localStorage): the select is
  // restored to the same channel key.
  const pitchSelect2 = dock2.locator(".s3-select").first();
  await expect
    .poll(async () => pitchSelect2.inputValue(), { timeout: 10_000 })
    .toBe(boundValue);

  // And the display name still shows in the sidebar after reload.
  await expect(
    page.locator(".device-name").filter({ hasText: displayName }),
  ).toBeVisible();
});
