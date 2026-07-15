import { createServer } from "node:net";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));

/** Repo root is two levels up from e2e/harness/. */
export const REPO_ROOT = resolve(here, "..", "..");
export const E2E_ROOT = resolve(here, "..");

/** Where the harness hands the chosen URLs + child PIDs to the test workers. */
export const STATE_FILE = resolve(E2E_ROOT, ".playwright-state.json");

export const IS_WIN = process.platform === "win32";
export const EXE = IS_WIN ? ".exe" : "";

export interface HarnessState {
  /** Base URL of the static web server (the built console). */
  webURL: string;
  /** Edge API base URL (passed to the app via ?api=). */
  apiURL: string;
  /** PIDs to kill on teardown. */
  pids: number[];
  /** Temp dir to remove on teardown. */
  tempDir: string;
  /** Whether the DuckDB SQL engine is available on this run. */
  duckdb: boolean;
}

/** Grab an OS-assigned free TCP port (bind :0, read it back, release). */
export function freePort(): Promise<number> {
  return new Promise((res, rej) => {
    const srv = createServer();
    srv.once("error", rej);
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      const port = typeof addr === "object" && addr ? addr.port : 0;
      srv.close(() => res(port));
    });
  });
}

/** Poll `fn` until it resolves truthy or the deadline passes. */
export async function waitFor(
  what: string,
  fn: () => Promise<boolean> | boolean,
  { timeoutMs = 30_000, intervalMs = 200 } = {},
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastErr: unknown;
  for (;;) {
    try {
      if (await fn()) return;
    } catch (e) {
      lastErr = e;
    }
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for ${what}${lastErr ? ` (last error: ${lastErr})` : ""}`);
    }
    await sleep(intervalMs);
  }
}

export function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

/** Kill a process tree cross-platform (Windows needs taskkill /T). */
export function killTree(pid: number): void {
  if (!pid) return;
  try {
    if (IS_WIN) {
      spawnSync("taskkill", ["/PID", String(pid), "/T", "/F"], { stdio: "ignore" });
    } else {
      process.kill(pid, "SIGTERM");
    }
  } catch {
    /* already gone */
  }
}
