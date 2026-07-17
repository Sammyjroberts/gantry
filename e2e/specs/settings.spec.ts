import { test, expect, type Page } from "../harness/fixtures";
import type { HarnessState } from "../harness/util";
import { uniqueName } from "./_helpers";

/**
 * Open the Settings page directly, preserving the harness `?api=` base (in-app
 * router navigation drops the query — see hardware.spec). The e2e Bench runs on
 * localhost and therefore fully TRUSTS the browser, so token management works
 * without a token here (loopback is the bootstrap path). The -require-auth
 * "connect to bench" prompt flow is exercised by the Go server e2e
 * (TestAuthLifecycle) and a component test (ConnectPrompt.test.tsx), since
 * driving a non-loopback bench through the Playwright harness would mean binding
 * a non-loopback socket — heavy plumbing for no extra product coverage.
 */
async function openSettings(page: Page, state: HarnessState): Promise<void> {
  const url = `${state.webURL}/settings?api=${encodeURIComponent(state.apiURL)}`;
  await page.goto(url, { waitUntil: "domcontentloaded" });
  await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
}

// Spec (settings) — the access-token lifecycle end to end against the real
// Bench: create a scoped token (its secret is shown exactly ONCE), confirm it
// lands in the table, then revoke it and confirm it's gone. Proves the
// TokenService round-trips through the console UI.
test("create token (secret shown once) → appears in table → revoke", async ({
  console: page,
  state,
}) => {
  const name = uniqueName("ci-token");

  await openSettings(page, state);

  // ---- create: name + scopes (default read; add operate) ----
  await page.getByRole("button", { name: "New token" }).click();
  await page.getByTestId("token-name").fill(name);
  await page.getByTestId("scope-operate").locator('input[type="checkbox"]').check();

  await Promise.all([
    page.waitForResponse((r) => r.url().includes("TokenService/CreateToken") && r.ok()),
    page.getByRole("button", { name: "Create token" }).click(),
  ]);

  // ---- the secret is revealed exactly once, in a copy block ----
  const secretBlock = page.getByTestId("token-secret");
  await expect(secretBlock).toBeVisible();
  await expect(secretBlock).toContainText("gtk_");
  await expect(secretBlock).toContainText("won't see this again");

  // Dismiss the reveal → the secret is gone from the DOM (never re-fetchable).
  await secretBlock.getByTitle("dismiss").click();
  await expect(page.getByTestId("token-secret")).toHaveCount(0);

  // ---- the new token is listed (name + a scope chip) ----
  const row = page.locator(".tok-table tbody tr").filter({ hasText: name });
  await expect(row).toBeVisible();
  await expect(row.locator(".tok-chip", { hasText: "operate" })).toBeVisible();

  // ---- revoke: click the trash, confirm, and the row disappears ----
  await row.getByTitle("revoke").click();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes("TokenService/DeleteToken") && r.ok()),
    row.getByRole("button", { name: "Confirm revoke" }).click(),
  ]);
  await expect(page.locator(".tok-table tbody tr").filter({ hasText: name })).toHaveCount(0);
});
