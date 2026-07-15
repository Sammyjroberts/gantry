import { test, expect } from "../harness/fixtures";
import { selectChannel, framesPerSec } from "./_helpers";

// Spec (a) — Charts live: select channels, assert canvas charts render and the
// frames/sec status becomes nonzero (live stream flowing from the real Bench).
test("charts render live and frames/sec climbs", async ({ console: page }) => {
  await selectChannel(page, "pitch_deg");
  await selectChannel(page, "roll_deg");

  // Two chart rows, each with a uPlot canvas.
  await expect(page.locator(".chart-row")).toHaveCount(2);
  await expect(page.locator(".chart-row canvas").first()).toBeVisible();

  // Connection goes LIVE and the frames/sec counter becomes nonzero.
  await expect(page.locator(".conn-pill.conn-live")).toBeVisible();
  await expect
    .poll(() => framesPerSec(page), { timeout: 20_000, message: "frames/sec should climb above 0" })
    .toBeGreaterThan(0);

  // The live readout for the primary chart shows a numeric value (data drawn).
  await expect
    .poll(async () => (await page.locator(".chart-readout .readout-val").first().textContent()) ?? "", {
      timeout: 15_000,
      message: "chart readout should show a number",
    })
    .toMatch(/\d/);
});
