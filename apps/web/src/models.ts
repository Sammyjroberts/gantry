/**
 * Model-file transport against the Bench server's model directory (same origin
 * as the Connect RPC endpoints — see config.resolveBaseUrl):
 *
 *   GET  /models/            → { "files": ["mr-wobbles.urdf", ...] }
 *   GET  /models/<name>      → the file body (urdf/stl/glb/dae)
 *   PUT  /models/<name>      → save a file (the URDF editor's Save)
 *
 * Kept thin and framework-free so the three.js loaders (Scene3D) and the editor
 * share one source of truth for URLs.
 */

/** Absolute URL for a model file (or the listing when `name` is omitted). */
export function modelUrl(baseUrl: string, name?: string): string {
  const base = baseUrl.replace(/\/$/, "");
  return name ? `${base}/models/${encodeURIComponent(name)}` : `${base}/models/`;
}

/** List available model file names. Returns `[]` on a missing/empty directory. */
export async function listModels(baseUrl: string, signal?: AbortSignal): Promise<string[]> {
  const res = await fetch(modelUrl(baseUrl), { signal });
  if (!res.ok) throw new Error(`list models: HTTP ${res.status}`);
  const body = (await res.json()) as { files?: unknown };
  if (!body || !Array.isArray(body.files)) return [];
  return body.files.filter((f): f is string => typeof f === "string");
}

/** Fetch a model file's text (URDF). Throws on non-2xx (incl. 404). */
export async function loadModelText(
  baseUrl: string,
  name: string,
  signal?: AbortSignal,
): Promise<string> {
  const res = await fetch(modelUrl(baseUrl, name), { signal });
  if (!res.ok) throw new Error(`load ${name}: HTTP ${res.status}`);
  return res.text();
}

/** Save a model file via PUT. Throws on non-2xx. */
export async function saveModelText(
  baseUrl: string,
  name: string,
  text: string,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(modelUrl(baseUrl, name), {
    method: "PUT",
    headers: { "content-type": "application/xml" },
    body: text,
    signal,
  });
  if (!res.ok) throw new Error(`save ${name}: HTTP ${res.status}`);
}
