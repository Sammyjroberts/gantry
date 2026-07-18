import { test, expect, type Page } from "../harness/fixtures";
import type { HarnessState } from "../harness/util";
import { uniqueName } from "./_helpers";

/**
 * Open a console sub-page directly, preserving the harness `?api=` base (the
 * pages re-resolve the API base from window.location on mount). Mirrors the
 * hardware spec's helper.
 */
async function openPage(page: Page, state: HarnessState, path: string): Promise<void> {
  const url = `${state.webURL}${path}?api=${encodeURIComponent(state.apiURL)}`;
  await page.goto(url, { waitUntil: "domcontentloaded" });
  await expect(page.locator(".statusbar")).toBeVisible();
}

// Spec (telemetry sources) — the bench-managed Foxglove client slice through the
// real Bench: add a source (pointing at an unreachable URL, which is fine — the
// supervisor connects/backoffs), see its live status dot, toggle the ENABLED
// checkbox, and confirm the row + toggle persist across a reload (server-side,
// not localStorage).
test("add a source → live status → toggle enabled persists across reload", async ({
  console: page,
  state,
}) => {
  const name = uniqueName("fox-src");
  // A port nothing listens on: the in-process client will connect→backoff.
  const url = "ws://127.0.0.1:59999";

  await openPage(page, state, "/hardware");
  await expect(page.getByTestId("sources-card")).toBeVisible();

  // ---- add the source (lerobot profile, enabled on create) ----
  await page.getByTestId("source-add").click();
  await page.getByTestId("source-name").fill(name);
  await page.getByTestId("source-url").fill(url);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes("SourceService/UpsertSource") && r.ok()),
    page.getByTestId("source-create").click(),
  ]);

  // The row appears; locate it by name within the table.
  const row = page.locator(".sources-table tr").filter({ hasText: name });
  await expect(row).toBeVisible();

  // ---- live status: an unreachable URL drives connecting → backoff ----
  const dot = row.locator(".source-dot");
  await expect
    .poll(async () => dot.getAttribute("data-state"), { timeout: 10_000 })
    .toMatch(/connecting|backoff/);

  // ---- toggle ENABLED off; the supervisor stops → status becomes disabled ----
  // The list polls on a 2s interval, so use click() (not uncheck(), whose
  // final-state assertion can fight a concurrent refetch) and prove the toggle
  // took effect through the resulting server-driven status change.
  const checkbox = row.locator('input[type="checkbox"]');
  await expect(checkbox).toBeChecked();
  await checkbox.click();
  await expect
    .poll(async () => dot.getAttribute("data-state"), { timeout: 10_000 })
    .toBe("disabled");

  // ---- reload: the source and its (now-disabled) toggle persisted server-side ----
  await openPage(page, state, "/hardware");
  const rowAfter = page.locator(".sources-table tr").filter({ hasText: name });
  await expect(rowAfter).toBeVisible();
  await expect(rowAfter.locator('input[type="checkbox"]')).not.toBeChecked();

  // Clean up so the shared bench doesn't accumulate rows across specs/retries.
  await rowAfter.getByRole("button", { name: /delete source/i }).click();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes("SourceService/DeleteSource") && r.ok()),
    rowAfter.getByRole("button", { name: /confirm/i }).click(),
  ]);
});
