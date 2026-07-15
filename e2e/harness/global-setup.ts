import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { mkdtempSync, mkdirSync, copyFileSync, existsSync, writeFileSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import {
  REPO_ROOT,
  E2E_ROOT,
  STATE_FILE,
  EXE,
  freePort,
  waitFor,
  type HarnessState,
} from "./util";

/** Build the Edge binary to a TEMP path (never bin/, which the live edge owns). */
function buildEdge(tempDir: string): string {
  const bin = join(tempDir, `edge${EXE}`);
  const override = process.env.E2E_EDGE_BIN;
  if (override && existsSync(override)) {
    console.log(`[global-setup] using prebuilt edge: ${override}`);
    return override;
  }
  console.log("[global-setup] go build edge -> temp ...");
  const r = spawnSync("go", ["build", "-o", bin, "./apps/edge/cmd/edge"], {
    cwd: REPO_ROOT,
    stdio: "inherit",
  });
  if (r.status !== 0) throw new Error("go build edge failed");
  return bin;
}

/**
 * Provision a DuckDB binary into <dataDir>/duckdb/ so the Edge SQL engine turns
 * on (DirProvider resolution, see libs/go/duckdb/provider.go). Order:
 *   1. GANTRY_DUCKDB env (CI downloads + caches it here) — inherited by the child.
 *   2. A local copy under data/edge/duckdb/ (bench box) — copied, never touched.
 * Returns true when SQL will be available.
 */
function provisionDuckDB(dataDir: string): boolean {
  const env = process.env.GANTRY_DUCKDB;
  if (env && existsSync(env)) {
    console.log(`[global-setup] duckdb via GANTRY_DUCKDB=${env}`);
    return true; // child inherits process.env; EnvProvider picks it up
  }
  const local = join(REPO_ROOT, "data", "edge", "duckdb", `duckdb${EXE}`);
  if (existsSync(local)) {
    const dst = join(dataDir, "duckdb");
    mkdirSync(dst, { recursive: true });
    const dstBin = join(dst, `duckdb${EXE}`);
    copyFileSync(local, dstBin);
    console.log(`[global-setup] duckdb copied from ${local}`);
    return true;
  }
  console.warn("[global-setup] no duckdb binary found; SQL spec will assert the graceful hint");
  return false;
}

async function waitHTTP(url: string, what: string, timeoutMs = 30_000): Promise<void> {
  await waitFor(
    what,
    async () => {
      const res = await fetch(url).catch(() => null);
      return !!res; // any HTTP response means the listener is up
    },
    { timeoutMs },
  );
}

async function channelsReady(apiURL: string): Promise<void> {
  await waitFor(
    "telemetry channels to appear",
    async () => {
      const res = await fetch(`${apiURL}/gantry.v1.LiveService/ListChannels`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "Connect-Protocol-Version": "1" },
        body: JSON.stringify({ deviceId: "" }),
      });
      if (!res.ok) return false;
      const json = await res.json();
      const devs = json.devices ?? [];
      const chans = devs.flatMap((d: { channels?: unknown[] }) => d.channels ?? []);
      return chans.length >= 4;
    },
    { timeoutMs: 30_000 },
  );
}

export default async function globalSetup(): Promise<void> {
  // Precondition: the web build must exist (pnpm -r build). Fail loud otherwise.
  const dist = join(REPO_ROOT, "apps", "web", "dist");
  if (!existsSync(join(dist, "index.html"))) {
    throw new Error(
      `web build missing at ${dist} — run \`pnpm -r build\` before the e2e suite (it is a precondition).`,
    );
  }

  const tempDir = mkdtempSync(join(tmpdir(), "gantry-e2e-"));
  const dataDir = join(tempDir, "data");
  mkdirSync(dataDir, { recursive: true });

  const edgeBin = buildEdge(tempDir);
  const duckdb = provisionDuckDB(dataDir);

  const edgePort = await freePort();
  const webPort = await freePort();
  const apiURL = `http://127.0.0.1:${edgePort}`;
  const webURL = `http://127.0.0.1:${webPort}`;

  const children: ChildProcess[] = [];
  const spawnChild = (cmd: string, args: string[], label: string): ChildProcess => {
    const c = spawn(cmd, args, { cwd: REPO_ROOT, stdio: "inherit", env: process.env });
    c.on("error", (e) => console.error(`[${label}] spawn error:`, e));
    children.push(c);
    return c;
  };

  // 1. Edge on a random port, temp data dir.
  const edge = spawnChild(
    edgeBin,
    ["--port", String(edgePort), "--data-dir", dataDir],
    "edge",
  );
  await waitHTTP(apiURL, "edge to serve");

  // 2. Static server for the built console.
  const web = spawnChild(
    process.execPath,
    [join(E2E_ROOT, "harness", "static-server.mjs"), dist, String(webPort)],
    "web",
  );
  await waitHTTP(webURL, "static web server");

  // 3. Telemetry feeder (~30 Hz).
  const feeder = spawnChild(
    process.execPath,
    [join(E2E_ROOT, "harness", "feeder.mjs"), apiURL, "30"],
    "feeder",
  );
  await channelsReady(apiURL);

  const state: HarnessState = {
    webURL,
    apiURL,
    pids: [edge.pid, web.pid, feeder.pid].filter((p): p is number => typeof p === "number"),
    tempDir,
    duckdb,
  };
  writeFileSync(STATE_FILE, JSON.stringify(state, null, 2));
  // Sanity: temp data dir is not the live one.
  if (statSync(dataDir).isDirectory()) {
    console.log(`[global-setup] ready — api=${apiURL} web=${webURL} duckdb=${duckdb}`);
  }
}
