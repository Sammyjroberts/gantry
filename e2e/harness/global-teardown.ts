import { existsSync, readFileSync, rmSync } from "node:fs";

import { STATE_FILE, killTree, type HarnessState } from "./util";

export default async function globalTeardown(): Promise<void> {
  if (!existsSync(STATE_FILE)) return;
  let state: HarnessState;
  try {
    state = JSON.parse(readFileSync(STATE_FILE, "utf8"));
  } catch {
    return;
  }
  for (const pid of state.pids ?? []) killTree(pid);
  // Best-effort temp cleanup (edge may still hold a file handle briefly on Win).
  if (state.tempDir) {
    try {
      rmSync(state.tempDir, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
    } catch {
      /* leave it for the OS temp reaper */
    }
  }
  try {
    rmSync(STATE_FILE, { force: true });
  } catch {
    /* ignore */
  }
}
