import { test, expect } from "../harness/fixtures";
import { toggleDock } from "./_helpers";

// Spec (e) — SQL console: with a DuckDB binary provisioned into the temp data
// dir, run a SELECT and assert rows render; run a DROP and assert the error
// banner. When no DuckDB is available the spec is skipped (the graceful path is
// covered by the SqlConsole's engine-missing hint, asserted below).
test("SQL SELECT renders rows and DROP is rejected", async ({ console: page, state }) => {
  test.skip(!state.duckdb, "no DuckDB binary provisioned on this run");

  await toggleDock(page, "sql");
  const panel = page.locator(".sql-panel");
  await expect(panel).toBeVisible();

  // --- read-only SELECT: count(*) over the tlm view returns exactly one row,
  // even before any Parquet segment has flushed (empty glob → count 0). ---
  await panel.locator(".sql-textarea").fill("SELECT count(*) AS n FROM tlm");
  await panel.locator(".sql-run").click();

  await expect(panel.locator(".sql-table")).toBeVisible();
  await expect(panel.locator(".sql-table thead th")).toHaveText(["n"]);
  await expect(panel.locator(".sql-table tbody tr")).toHaveCount(1);
  await expect(panel.locator(".sql-rowcount")).toContainText("1 rows");
  await expect(panel.locator(".sql-error")).toHaveCount(0);

  // --- DROP is rejected by the read-only guard (400) → verbatim error banner. ---
  await panel.locator(".sql-textarea").fill("DROP VIEW tlm");
  await panel.locator(".sql-run").click();
  await expect(panel.locator(".sql-error")).toBeVisible();
  await expect(panel.locator(".sql-error")).toContainText(/rejected|DROP/i);
});
