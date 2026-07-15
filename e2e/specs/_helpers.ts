import { expect, type Page } from "@playwright/test";

/** Tick the sidebar checkbox for a channel by its param name (e.g. "pitch_deg"). */
export async function selectChannel(page: Page, name: string): Promise<void> {
  const label = page.locator(".channel-list label").filter({ hasText: name }).first();
  await expect(label).toBeVisible();
  await label.locator('input[type="checkbox"]').check();
}

/** Add the current sidebar selection as a timeseries chart panel. */
export async function addSelectionAsChart(page: Page): Promise<void> {
  await page.getByTestId("add-as-chart").click();
  await expect(page.locator(".panel[data-panel-type='timeseries']").first()).toBeVisible();
}

/** Navigate to a page via the left nav rail. */
export async function navTo(
  page: Page,
  label: "workspace" | "hardware" | "experiments" | "data",
): Promise<void> {
  await page.getByTestId(`nav-${label}`).click();
}

/** Add a panel of `type` via the workspace toolbar add-panel menu. */
export async function addPanel(page: Page, type: string): Promise<void> {
  await page.getByTestId("add-panel-btn").click();
  await page.getByTestId(`add-panel-${type}`).click();
}

/**
 * Start from a fresh, empty workspace via the toolbar "new workspace" button.
 *
 * The Bench is shared across the whole suite (workers:1) and layouts persist
 * server-side, so the default workspace accumulates panels across specs and
 * retries. Panel-adding specs create their own empty workspace first so the grid
 * holds exactly the panels they add (kept near the top / on-screen), making
 * `.last()`/type-scoped panel locators deterministic.
 */
export async function newWorkspace(page: Page): Promise<void> {
  // Wait for the manager to finish bootstrapping (a workspace is selected) so the
  // create doesn't race the initial load-or-seed.
  await expect(page.getByTestId("workspace-switcher")).not.toHaveValue("");
  await page.getByTestId("workspace-new").click();
  await expect(page.locator(".panel")).toHaveCount(0);
}

/** Read the live frames/sec counter from the status bar. */
export async function framesPerSec(page: Page): Promise<number> {
  const v = await page.locator(".stat").filter({ hasText: "frames/s" }).locator(".stat-v").textContent();
  return Number((v ?? "0").trim());
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
