import { test, expect } from "../harness/fixtures";
import { selectChannel, addSelectionAsChart, runExperiment, openExperimentRow, uniqueName, newWorkspace } from "./_helpers";

// Spec (d) — Replay: replay a recorded experiment; the transport bar + playhead
// pill appear and pause/resume toggles the clock. A chart panel is on the grid so
// the replay cursor has something to sweep.
test("replay transport with pause/resume", async ({ console: page }) => {
  await newWorkspace(page);
  await selectChannel(page, "pitch_deg");
  await addSelectionAsChart(page);

  const name = uniqueName("replay-run");
  await runExperiment(page, name, 2500);

  const row = await openExperimentRow(page, name);
  await row.locator(".exp-replay").click();

  // Transport bar + moving playhead pill are present.
  const bar = page.locator(".replay-bar");
  await expect(bar).toBeVisible();
  await expect(bar.locator(".replay-tag")).toHaveText(/REPLAY/);
  await expect(bar.locator(".replay-clock")).toBeVisible();

  // Starts playing → the button shows the pause glyph. Toggle to pause, then resume.
  const play = bar.locator(".replay-play");
  await expect(play).toHaveText("❚❚"); // playing
  await play.click();
  await expect(play).toHaveText("▶"); // paused
  await play.click();
  await expect(play).toHaveText("❚❚"); // resumed

  // Exit returns to the normal view.
  await bar.locator(".replay-exit").click();
  await expect(page.locator(".replay-bar")).toHaveCount(0);
});
