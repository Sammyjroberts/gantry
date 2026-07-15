import { expect, type Page } from "@playwright/test";

/** Tick the sidebar checkbox for a channel by its param name (e.g. "pitch_deg"). */
export async function selectChannel(page: Page, name: string): Promise<void> {
  const label = page.locator(".channel-list label").filter({ hasText: name }).first();
  await expect(label).toBeVisible();
  await label.locator('input[type="checkbox"]').check();
  // A chart row for the selection should mount.
  await expect(page.locator(".chart-row").first()).toBeVisible();
}

/** Read the live frames/sec counter from the status bar. */
export async function framesPerSec(page: Page): Promise<number> {
  const v = await page.locator(".stat").filter({ hasText: "frames/s" }).locator(".stat-v").textContent();
  return Number((v ?? "0").trim());
}

/** Toggle a top-bar dock button (matched by its label text). */
export async function toggleDock(page: Page, label: "3D" | "video" | "sql"): Promise<void> {
  await page.locator(".ctl-btn").filter({ hasText: label }).click();
}

/** Start an experiment, dwell, and stop it. Returns the experiment name. */
export async function runExperiment(page: Page, name: string, dwellMs: number): Promise<string> {
  await page.locator(".exp-name-input").fill(name);
  await page.locator(".exp-start-btn").click();
  await expect(page.locator(".exp-active-name")).toHaveText(name);
  // Intentional dwell so the run captures live telemetry (not a flake sleep —
  // the experiment window must span real data for export/replay to be meaningful).
  await page.waitForTimeout(dwellMs);
  await page.locator(".exp-stop").click();
  await expect(page.locator(".exp-active")).toHaveCount(0);
  return name;
}

/** Open the experiments history panel and return the row for `name`. */
export async function openExperimentRow(page: Page, name: string) {
  const toggle = page.locator(".exp-toggle");
  if ((await toggle.getAttribute("aria-expanded")) !== "true") {
    await toggle.click();
  }
  const row = page.locator(".exp-row").filter({ hasText: name }).first();
  await expect(row).toBeVisible();
  return row;
}

/** A unique-ish experiment name so specs don't collide on the shared server. */
export function uniqueName(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${Math.floor(Math.random() * 1e4)}`;
}
