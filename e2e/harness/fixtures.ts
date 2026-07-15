import { test as base, expect, type Page } from "@playwright/test";
import { existsSync, readFileSync } from "node:fs";

import { STATE_FILE, type HarnessState } from "./util";

function readState(): HarnessState {
  if (!existsSync(STATE_FILE)) {
    throw new Error(`harness state missing (${STATE_FILE}); did global-setup run?`);
  }
  return JSON.parse(readFileSync(STATE_FILE, "utf8"));
}

export interface GantryFixtures {
  state: HarnessState;
  /** The web base URL with the API pointed at the ephemeral Bench via ?api=. */
  consoleURL: string;
  /** A page already navigated to the console and waiting for React to mount. */
  console: Page;
}

export const test = base.extend<GantryFixtures>({
  state: async ({}, use) => {
    await use(readState());
  },
  consoleURL: async ({ state }, use) => {
    await use(`${state.webURL}/?api=${encodeURIComponent(state.apiURL)}`);
  },
  console: async ({ page, consoleURL }, use) => {
    await page.goto(consoleURL, { waitUntil: "domcontentloaded" });
    // The app shell renders the brand + status bar immediately on mount.
    await expect(page.locator(".statusbar")).toBeVisible();
    await use(page);
  },
});

export { expect };
