/**
 * Video transport against the Edge server's /video endpoints (same origin as the
 * Connect RPC + /models routes — see config.resolveBaseUrl). Plain HTTP:
 *
 *   POST /video/chunks?camera=&start_ns=&duration_ms=   body=bytes → 201 {"id"}
 *   GET  /video/chunks?camera=&from_ns=&to_ns=          → {"chunks":[…]} ascending
 *   GET  /video/chunks/{id}                             → raw bytes (may 404 if pruned)
 *   GET  /video/cameras                                 → {"cameras":[…]}
 *
 * Kept thin and framework-free so the capture hook, live-follow hook, and the
 * replay <video> wiring share one source of truth for URLs and JSON shapes. The
 * snake_case wire shape is mapped to the camelCase {@link VideoChunk} the pure
 * videoSync module consumes here, at the boundary.
 */

import type { VideoChunk } from "./videoSync";

/** Camera as advertised by GET /video/cameras. */
export interface ServerCamera {
  cameraId: string;
  latestStartNs: number;
}

function trimBase(baseUrl: string): string {
  return baseUrl.replace(/\/$/, "");
}

/** Format an epoch-ns number as an integer query value (no exponent). */
function nsParam(ns: number): string {
  return Math.round(ns).toString();
}

/** Absolute URL for a single chunk's bytes (GET), tolerant of pruning (404). */
export function chunkUrl(baseUrl: string, id: string): string {
  return `${trimBase(baseUrl)}/video/chunks/${encodeURIComponent(id)}`;
}

interface WireChunk {
  id: string;
  camera_id: string;
  start_ns: number;
  duration_ms: number;
  mime: string;
  bytes: number;
  created_ns: number;
}

function toChunk(w: WireChunk): VideoChunk {
  return {
    id: w.id,
    cameraId: w.camera_id,
    startNs: Number(w.start_ns),
    durationMs: Number(w.duration_ms),
    mime: w.mime,
    bytes: Number(w.bytes),
    createdNs: Number(w.created_ns),
  };
}

export interface UploadChunkArgs {
  cameraId: string;
  startNs: number;
  durationMs: number;
  blob: Blob;
}

/**
 * POST one self-contained chunk. Returns the server-assigned id. Throws on any
 * non-2xx so the {@link import("./uploadQueue").UploadQueue} can retry/drop.
 */
export async function uploadChunk(
  baseUrl: string,
  args: UploadChunkArgs,
  signal?: AbortSignal,
): Promise<string> {
  const qs = new URLSearchParams({
    camera: args.cameraId,
    start_ns: nsParam(args.startNs),
    duration_ms: String(Math.round(args.durationMs)),
  });
  const res = await fetch(`${trimBase(baseUrl)}/video/chunks?${qs.toString()}`, {
    method: "POST",
    headers: { "content-type": args.blob.type || "video/webm" },
    body: args.blob,
    signal,
  });
  if (!res.ok) throw new Error(`upload chunk: HTTP ${res.status}`);
  const body = (await res.json()) as { id?: string };
  return body.id ?? "";
}

export interface ListChunksArgs {
  cameraId: string;
  fromNs: number;
  toNs: number;
}

/** List chunks for a camera within `[fromNs, toNs]`, ascending. */
export async function listChunks(
  baseUrl: string,
  args: ListChunksArgs,
  signal?: AbortSignal,
): Promise<VideoChunk[]> {
  const qs = new URLSearchParams({
    camera: args.cameraId,
    from_ns: nsParam(args.fromNs),
    to_ns: nsParam(args.toNs),
  });
  const res = await fetch(`${trimBase(baseUrl)}/video/chunks?${qs.toString()}`, { signal });
  if (!res.ok) throw new Error(`list chunks: HTTP ${res.status}`);
  const body = (await res.json()) as { chunks?: WireChunk[] };
  return (body.chunks ?? []).map(toChunk);
}

/** List cameras the server currently has chunks for. */
export async function listServerCameras(
  baseUrl: string,
  signal?: AbortSignal,
): Promise<ServerCamera[]> {
  const res = await fetch(`${trimBase(baseUrl)}/video/cameras`, { signal });
  if (!res.ok) throw new Error(`list cameras: HTTP ${res.status}`);
  const body = (await res.json()) as {
    cameras?: Array<{ camera_id: string; latest_start_ns: number }>;
  };
  return (body.cameras ?? []).map((c) => ({
    cameraId: c.camera_id,
    latestStartNs: Number(c.latest_start_ns),
  }));
}
