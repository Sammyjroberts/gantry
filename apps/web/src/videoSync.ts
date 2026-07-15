/**
 * Video sync — pure cursor↔chunk selection math for the capture/replay panel.
 *
 * Two independent selection problems, both kept free of React/DOM/fetch so the
 * boundary maths (which chunk covers a cursor, offset within it, gaps, and the
 * live-follow "what plays next") are unit tested directly (see videoSync.test.ts):
 *
 *  - REPLAY: the playback clock (playback.ts) yields a cursor in epoch seconds.
 *    {@link chunkAtCursor} finds the chunk covering it and the offset (seconds)
 *    to seek the <video> element to. A cursor landing in a gap (a pruned or
 *    never-recorded interval) returns null → the panel shows a "no video"
 *    overlay while the clock keeps running.
 *  - LIVE-FOLLOW: watching a camera that is still recording. The panel polls a
 *    trailing window of chunks; {@link nextLiveChunk} picks the newest chunk not
 *    yet started so playback stays near the live edge (dropping any backlog),
 *    and {@link lagSeconds} estimates how far behind real time we are.
 *
 * Nanosecond note: epoch ns (~1.7e18) exceeds Number.MAX_SAFE_INTEGER, so held
 * as a JS `number` the low ~hundreds-of-ns are imprecise. That is far finer than
 * video seek granularity (chunks are ~2s; we need ms), so numbers are used
 * throughout for simplicity — see the module-level contract, not for ns-exact
 * telemetry math (that stays BigInt at the RPC boundary; see history.ts).
 */

/** One millisecond in nanoseconds. */
const MS_NS = 1e6;
/** One second in nanoseconds. */
const SEC_NS = 1e9;

/**
 * A stored video chunk descriptor (camelCased from GET /video/chunks; the
 * snake_case→camel mapping happens at the transport boundary in videoApi.ts).
 * A chunk covers the half-open interval `[startNs, startNs + durationMs*1e6)`.
 */
export interface VideoChunk {
  id: string;
  cameraId: string;
  startNs: number;
  durationMs: number;
  mime: string;
  bytes: number;
  createdNs: number;
}

/** Exclusive end of a chunk in epoch ns. */
export function chunkEndNs(c: VideoChunk): number {
  return c.startNs + c.durationMs * MS_NS;
}

/** The chunk + seek offset that covers a replay cursor. */
export interface ChunkCursor {
  chunkId: string;
  /** Seconds into the chunk to seek the <video> element to. */
  offsetSec: number;
}

/**
 * Rightmost index `i` in the ascending-by-startNs `chunks` with
 * `chunks[i].startNs <= ns`, or -1 if the cursor precedes every chunk. Binary
 * search — the replay range can hold hundreds of 2s chunks and this runs every
 * render tick.
 */
function lastStartingAtOrBefore(chunks: VideoChunk[], ns: number): number {
  let lo = 0;
  let hi = chunks.length - 1;
  let ans = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (chunks[mid]!.startNs <= ns) {
      ans = mid;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  return ans;
}

/**
 * Find the chunk covering `cursorNs` (epoch ns) and the offset within it, for
 * replay. Chunks must be ascending by `startNs` (the API returns them so). The
 * recorder emits back-to-back chunks, so the covering chunk is the last one that
 * starts at or before the cursor — provided the cursor is still inside it.
 * Returns null when the cursor is before the first chunk or falls in a gap
 * (pruned/missing), so the caller can blank the frame and keep the clock going.
 */
export function chunkAtCursor(cursorNs: number, chunks: VideoChunk[]): ChunkCursor | null {
  if (chunks.length === 0) return null;
  const i = lastStartingAtOrBefore(chunks, cursorNs);
  if (i < 0) return null; // cursor before the first chunk
  const c = chunks[i]!;
  if (cursorNs >= chunkEndNs(c)) return null; // inside a gap past this chunk's end
  return { chunkId: c.id, offsetSec: (cursorNs - c.startNs) / SEC_NS };
}

/**
 * The inclusive `[fromNs, toNs]` trailing window to poll for live-follow: the
 * last `widthSec` seconds up to `nowNs`. A wider window than the poll cadence
 * tolerates jitter and a few seconds of pipeline latency.
 */
export function trailingWindow(nowNs: number, widthSec: number): [number, number] {
  return [nowNs - widthSec * SEC_NS, nowNs];
}

/**
 * Live-follow selection: the newest chunk strictly newer than `lastStartedNs`
 * (the start_ns of the chunk currently/last playing), or null when nothing new
 * has arrived. Choosing the newest — not the next sequential — keeps playback
 * near the live edge by dropping any backlog, so lag stays bounded even if a
 * poll returns several fresh chunks at once. Pass `null` to start following from
 * the newest available chunk.
 */
export function nextLiveChunk(
  chunks: VideoChunk[],
  lastStartedNs: number | null,
): VideoChunk | null {
  let best: VideoChunk | null = null;
  for (const c of chunks) {
    if (lastStartedNs !== null && c.startNs <= lastStartedNs) continue;
    if (best === null || c.startNs > best.startNs) best = c;
  }
  return best;
}

/**
 * Estimated live-follow lag in seconds: how far the end of `chunk` sits behind
 * `nowNs`. Never negative (clock skew between recorder and viewer can push a
 * chunk's stamped end slightly into the future).
 */
export function lagSeconds(chunk: VideoChunk, nowNs: number): number {
  return Math.max(0, (nowNs - chunkEndNs(chunk)) / SEC_NS);
}
