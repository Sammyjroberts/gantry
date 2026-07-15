import { test, expect } from "../harness/fixtures";
import { addPanel, newWorkspace } from "./_helpers";

// Spec (e) — SQL console: with a DuckDB binary provisioned into the temp data
// dir, add an SQL panel, run a SELECT and assert rows render; run a DROP and
// assert the error banner. When no DuckDB is available the spec is skipped (the
// graceful path is covered by the SqlConsole's engine-missing hint).
//
// The SQL panel (not the Data page) is used deliberately: the panel resolves its
// API base from the app-level LiveContext, which is memoized once at initial load
// with the harness `?api=` base. The Data page, reached via in-app navigation,
// re-resolves the base from window.location — which loses `?api=` after a
// react-router navigation (see the report notes) — so its SQL calls would target
// the static server. Both are valid entry points in the product; only the panel
// is exercisable under the current harness.
test("SQL SELECT renders rows and DROP is rejected", async ({ console: page, state }) => {
  test.skip(!state.duckdb, "no DuckDB binary provisioned on this run");

  await newWorkspace(page);
  await addPanel(page, "sql");
  const panel = page.locator(".panel[data-panel-type='sql'] .sql-panel");
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
