import { test, expect } from "../harness/fixtures";
import { selectChannel, addSelectionAsChart, newWorkspace } from "./_helpers";

// Spec (c) — Time navigation: preset switch highlights, drag-zoom on a chart
// enters inspect (the live pill flips to the "⟳ live" resume affordance), and
// back-to-live restores live follow. Needs a chart panel on the grid so there's
// a uPlot overlay to drive.
test("presets, drag-zoom into inspect, and back-to-live", async ({ console: page }) => {
  await newWorkspace(page);
  await selectChannel(page, "pitch_deg");
  await addSelectionAsChart(page);

  // Preset switch: default is the 1m window; switch to 10s and assert it activates.
  const tenSec = page.locator(".tr-preset").filter({ hasText: "10s" });
  await tenSec.click();
  await expect(tenSec).toHaveClass(/is-active/);
  // Starts live.
  await expect(page.locator(".tr-readout.is-live")).toBeVisible();

  // Drag-zoom on the chart's uPlot overlay (> MIN_DRAG_PX) → inspect mode. Scope
  // to the panel we just added (a single f64 row with a full-height plot area);
  // the seeded default workspace also carries a short boolean strip whose overlay
  // has no drag height.
  const over = page
    .locator(".panel[data-panel-type='timeseries']")
    .last()
    .locator(".panel-chart-row .u-over")
    .first();
  await expect(over).toBeVisible();
  await over.scrollIntoViewIfNeeded();
  const box = (await over.boundingBox())!;
  const y = box.y + box.height / 2;
  await page.mouse.move(box.x + box.width * 0.3, y);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width * 0.6, y, { steps: 12 });
  await page.mouse.up();

  // Inspect: the live pill is gone; the resume button appears.
  await expect(page.locator(".tr-live-btn")).toBeVisible();
  await expect(page.locator(".tr-readout.is-live")).toHaveCount(0);

  // Back-to-live restores live follow.
  await page.locator(".tr-live-btn").click();
  await expect(page.locator(".tr-readout.is-live")).toBeVisible();
  await expect(page.locator(".tr-live-btn")).toHaveCount(0);
});
