import { readFileSync } from "node:fs";

import { test, expect } from "../harness/fixtures";
import { selectChannel, runExperiment, openExperimentRow, uniqueName } from "./_helpers";

// Spec (b) — Experiments: start a named run, dwell 3s, stop; a panel row appears;
// CSV export downloads a non-empty file with the correct header; the run's
// zoom-to-range ("view entire") drives every chart into inspect (the region is
// addressable), proving the overlay/region wiring.
test("experiment lifecycle, CSV export, and region zoom", async ({ console: page }) => {
  await selectChannel(page, "pitch_deg");

  const name = uniqueName("csv-run");
  await runExperiment(page, name, 3000);

  const row = await openExperimentRow(page, name);

  // --- CSV export via the download API ---
  const [download] = await Promise.all([
    page.waitForEvent("download"),
    row.locator("a.exp-act", { hasText: "csv" }).click(),
  ]);
  const path = await download.path();
  expect(path).toBeTruthy();
  const csv = readFileSync(path!, "utf8");
  const lines = csv.split(/\r?\n/).filter((l) => l.length > 0);
  // Correct long-format header (see apps/edge/internal/server/export.go longHeader).
  expect(lines[0]).toBe("ts_ns,ts_iso,device_id,packet,channel,kind,value");
  // Non-empty: at least one data row captured during the 3s window.
  expect(lines.length).toBeGreaterThan(1);
  expect(csv).toContain("pitch_deg");

  // --- region zoom-to-range ("⤢ view entire") drives charts into inspect ---
  await expect(page.locator(".tr-readout.is-live")).toBeVisible(); // currently live
  await row.locator(".exp-zoom").click();
  // Leaving live means the "⟳ live" resume button appears (inspect mode).
  await expect(page.locator(".tr-live-btn")).toBeVisible();
});
