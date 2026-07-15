# Gantry browser e2e (Playwright)

The **top tier of the test pyramid**: the real Edge binary + a live telemetry
feeder, driven through a headless Chromium against the production web build. This
is the only tier that exercises the browser, the ConnectRPC/HTTP wire, WebGL, and
`getUserMedia`/`MediaRecorder` together — the integration surface no unit or
in-process test can reach. (It has already earned its keep: it caught a real
video-capture bug — see *Notes* below.)

## The test pyramid

| Tier | What it covers | Where it lives | Runner |
|---|---|---|---|
| **Unit** | Pure modules, per package — zoom/playback/history math, CSV/SQL shaping, registry kind-inference, `gantry-wire` codec, `tlm!` derive. | `apps/web/src/*.test.ts(x)`, `libs/go/**/​*_test.go`, `sdk/**/tests`, `sdk/**/src` unit tests. | `vitest`, `go test`, `cargo test` |
| **Integration** | Multiple parts wired in-process: the Edge `App`/handler e2e tests (ingest → JetStream → query/export/SQL/video over `httptest`), tlm-synthesized sessions round-tripped through the wire decoder, the serial-agent → real-Edge test. | `apps/edge/internal/server/*_e2e_test.go`, `libs/go/segments`, `sdk/gantry-serial-agent/tests/e2e_edge.rs` (`#[ignore]`). | `go test`, `cargo test` |
| **e2e (this suite)** | The browser + the real `edge` binary + a live feeder: charts, experiments/CSV, time-nav, replay, SQL console, 3D (WebGL), video capture. | `e2e/specs/*.spec.ts` | `playwright test` |

Rule of thumb: push a behavior **down** the pyramid whenever you can (a pure
module test is faster and more precise than a browser test). Reserve this suite
for things that are only true when the whole stack runs: real streaming, real GL,
real media capture, real cross-origin HTTP.

## Specs

| Spec | Asserts |
|---|---|
| `charts.spec.ts` | Select channels → uPlot canvases render; connection goes LIVE; `frames/sec` climbs above 0; live readout shows a number. |
| `experiments.spec.ts` | Start named run → dwell 3 s → stop → panel row appears; CSV export downloads a non-empty file with the exact long-format header; the run's zoom-to-range drives every chart into inspect (region addressable). |
| `timenav.spec.ts` | Preset switch highlights; drag-zoom on a chart enters inspect (live pill → "⟳ live" resume); back-to-live restores live follow. |
| `replay.spec.ts` | Replay a run → transport bar + playhead clock appear; play/pause toggles; exit returns to the live view. |
| `sql.spec.ts` | With a provisioned DuckDB binary: `SELECT count(*) FROM tlm` renders a row; `DROP VIEW tlm` is rejected with the error banner. Skips (documented) if no DuckDB. |
| `panel3d.spec.ts` | Toggle 3D → lazy three.js chunk loads and a WebGL `<canvas>` mounts (SwiftShader). Falls back to asserting the graceful viewport shell + annotating the run if WebGL is genuinely absent. |
| `video.spec.ts` | Fake-camera capture → the upload `sent` counter climbs and chunks appear via `GET /video/chunks`; stop. |

## Running it

Prerequisites: **Go** on `PATH` (the harness builds Edge), and the **web build**
must exist — `pnpm -r build` from the repo root (global-setup fails loudly if
`apps/web/dist/index.html` is missing).

```bash
# from repo root
pnpm -r build

# from e2e/
npm install
npm run install:browsers    # playwright install --with-deps chromium
npm test                    # runs the full suite headless
npm run test:headed         # watch it drive the UI
npm run report              # open the last HTML report (CI uploads this on failure)
```

## Harness (`harness/`)

`global-setup.ts` runs once and owns all process lifecycle, then hands the chosen
URLs + PIDs to the workers via `.playwright-state.json` (git-ignored):

1. **Build Edge to a TEMP path** — `go build -o <tmp>/edge[.exe] ./apps/edge/cmd/edge`
   (never `bin/`, which a live Edge owns). Override with `E2E_EDGE_BIN`.
2. **Provision DuckDB** into `<tmp>/data/duckdb/` so the SQL engine turns on
   (`DirProvider`, see `libs/go/duckdb/provider.go`). Order: `GANTRY_DUCKDB` env
   (CI downloads + caches it) → a local copy under `data/edge/duckdb/` (copied,
   never mutated). Absent → the SQL spec asserts the graceful hint and skips.
3. **Start Edge** on an OS-assigned free port with the temp data dir.
4. **Serve the built console** (`harness/static-server.mjs`, Node builtins only)
   on another free port. The app reads its API base from `?api=` (see
   `apps/web/src/config.ts`), so we point it at the ephemeral Edge with **no
   rebuild or re-embed** — cross-origin is fine (Edge reflects localhost origins).
5. **Start the feeder** (`harness/feeder.mjs`): a Rust-free, dependency-free Node
   script that POSTs Connect-protocol JSON `PublishBatch` at ~30 Hz for 4 channels
   (3 packet-tagged — `imu.pitch_deg`, `imu.roll_deg`, `power.voltage` — plus an
   ad-hoc `heartbeat` bool). Wire shapes hand-rolled from `proto/gantry/v1`.

`global-teardown.ts` kills the tree and removes the temp dir.

### Flake mitigations

- **One worker, not parallel**: every spec shares one Edge + feeder, and some
  mutate server state (experiments, video chunks). Serial keeps them deterministic.
- **No arbitrary sleeps** for readiness — `expect.poll`/`toBeVisible` with
  timeouts everywhere. The only `waitForTimeout`s are *intentional dwells* so an
  experiment window spans real telemetry (export/replay need data), not races.
- **Unique experiment names** per spec so the shared server's list never collides.
- **`retries: 1`** absorbs a rare cold-start hiccup without masking real failures;
  `trace: on-first-retry` + `screenshot: only-on-failure` capture the evidence
  (CI uploads `playwright-report/` + `test-results/` as artifacts).

### Browser flags (`playwright.config.ts`)

- `--use-fake-device-for-media-stream --use-fake-ui-for-media-stream` +
  `permissions: ['camera']` → deterministic fake camera for `video.spec.ts`.
- `--use-gl=angle --use-angle=swiftshader --enable-unsafe-swiftshader
  --ignore-gpu-blocklist` → a real software WebGL context headless, so the 3D
  panel mounts an actual canvas (verified: `ANGLE … SwiftShader`).

## Notes

- **Bug caught by this tier:** `MediaRecorder` tags blobs `video/webm;codecs=vp9`,
  but the Edge video allowlist did an exact match on `video/webm` and returned
  **415**, so capture was broken end-to-end in production — invisible to the
  adapter-mocked unit tests. Fixed at the root (`libs/go/video/service.go` now
  normalizes to the base media type) with a regression test
  (`libs/go/video/mime_test.go`).

## Suggested `justfile` recipes

The `justfile` is owned elsewhere; propose adding:

```just
# Full local test sweep across every tier.
test-all: test-go test-rust test-web e2e

# Browser e2e (builds web first; harness builds Edge + starts a feeder).
e2e:
    pnpm -r build
    cd e2e && npm install && npm run install:browsers && npm test
```
