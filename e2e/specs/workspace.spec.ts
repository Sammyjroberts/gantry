import { test, expect } from "../harness/fixtures";
import { navTo, addPanel } from "./_helpers";

// Bench builder — the new console shell: router nav, the panel grid, binding a
// panel to a live channel, and server-side layout persistence across reload.

test("nav rail routes to every page", async ({ console: page }) => {
  // Workspace (default route) shows the workspace toolbar.
  await expect(page.locator(".ws-toolbar")).toBeVisible();

  await navTo(page, "hardware");
  await expect(page.getByRole("heading", { name: "Hardware" })).toBeVisible();

  await navTo(page, "experiments");
  await expect(page.getByRole("heading", { name: "Experiments" })).toBeVisible();

  await navTo(page, "data");
  await expect(page.locator(".data-page")).toBeVisible();

  await navTo(page, "workspace");
  await expect(page.locator(".ws-toolbar")).toBeVisible();
});

test("add a value panel, bind it to a live channel, and see a number", async ({ console: page }) => {
  await addPanel(page, "value");
  const panel = page.locator(".panel[data-panel-type='value']").last();
  await expect(panel).toBeVisible();

  // A fresh value panel is unbound — bind it to a live channel via the rebind
  // picker, then the instantaneous readout should show a number.
  await panel.locator(".panel-unresolved-rebind select").selectOption({ label: "imu.pitch_deg" });
  await expect
    .poll(async () => (await panel.getByTestId("value-readout").textContent()) ?? "", {
      timeout: 20_000,
      message: "value readout should show a live number",
    })
    .toMatch(/-?\d/);
});

test("layout persists across a reload (server-side)", async ({ console: page }) => {
  // Add a distinctive LED panel, let autosave flush, reload, and assert it
  // survived — proving the layout round-tripped through WorkspaceService.
  const before = await page.locator(".panel[data-panel-type='led']").count();
  await addPanel(page, "led");
  await expect(page.locator(".panel[data-panel-type='led']")).toHaveCount(before + 1);

  // Autosave debounce is ~2s; wait past it, then reload the root route.
  await page.waitForTimeout(3000);
  await page.reload({ waitUntil: "domcontentloaded" });

  await expect(page.locator(".panel[data-panel-type='led']").first()).toBeVisible({ timeout: 20_000 });
  await expect(await page.locator(".panel[data-panel-type='led']").count()).toBeGreaterThanOrEqual(before + 1);
});
