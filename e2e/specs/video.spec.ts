import { test, expect } from "../harness/fixtures";
import { addPanel, newWorkspace } from "./_helpers";

// Spec (g) — Video capture: with Chromium's fake camera + auto-granted media
// permission (see playwright.config.ts), add a video panel, record for ~5s,
// assert the upload "sent" counter climbs and the chunks appear via
// GET /video/chunks, then stop.
test("camera capture uploads chunks that appear in the catalog", async ({ console: page, state }) => {
  await newWorkspace(page);
  await addPanel(page, "video");
  const panel = page.locator(".panel[data-panel-type='video'] .video-panel");
  await expect(panel).toBeVisible();

  // Capture must be supported in a real browser (fake device counts).
  await expect(panel.locator(".video-note")).toHaveCount(0);

  // Start recording; the REC indicator lights up.
  await panel.locator(".video-btn--rec").click();
  await expect(panel.locator(".video-rec")).toBeVisible();

  // Chunks record on a ~2s stop/start cycle; after a few seconds ≥1 has uploaded.
  const sent = panel.locator(".video-stat", { hasText: "sent" }).locator("b");
  await expect
    .poll(async () => Number((await sent.textContent()) ?? "0"), {
      timeout: 25_000,
      message: "video sent counter should climb above 0",
    })
    .toBeGreaterThan(0);

  // Cross-check the server catalog: the camera's chunks are listed (open window).
  const res = await page.request.get(`${state.apiURL}/video/chunks?camera=bench-cam`);
  expect(res.ok()).toBeTruthy();
  const body = await res.json();
  expect(Array.isArray(body.chunks)).toBe(true);
  expect(body.chunks.length).toBeGreaterThan(0);

  // Stop recording.
  await panel.locator(".video-btn--stop").click();
  await expect(panel.locator(".video-btn--rec")).toBeVisible();
});
