import { test, expect } from "../harness/fixtures";
import { selectChannel, addSelectionAsChart, framesPerSec, newWorkspace } from "./_helpers";

// Spec (a) — Charts live: select channels, add them as a timeseries panel, assert
// the canvas charts render and the frames/sec status becomes nonzero (live stream
// flowing from the real Bench).
test("charts render live and frames/sec climbs", async ({ console: page }) => {
  await newWorkspace(page);
  await selectChannel(page, "pitch_deg");
  await selectChannel(page, "roll_deg");
  await addSelectionAsChart(page);

  // The timeseries panel holds one chart row per bound channel, each a uPlot canvas.
  const panel = page.locator(".panel[data-panel-type='timeseries']").last();
  await expect(panel).toBeVisible();
  await expect(panel.locator(".panel-chart-row")).toHaveCount(2);
  await expect(panel.locator(".panel-chart-row canvas").first()).toBeVisible();

  // Connection goes LIVE and the frames/sec counter becomes nonzero.
  await expect(page.locator(".conn-pill.conn-live")).toBeVisible();
  await expect
    .poll(() => framesPerSec(page), { timeout: 20_000, message: "frames/sec should climb above 0" })
    .toBeGreaterThan(0);

  // The live readout for the primary chart shows a numeric value (data drawn).
  await expect
    .poll(async () => (await panel.locator(".chart-readout .readout-val").first().textContent()) ?? "", {
      timeout: 15_000,
      message: "chart readout should show a number",
    })
    .toMatch(/-?\d/);
});
