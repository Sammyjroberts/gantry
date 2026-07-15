/**
 * useVideoReplay — drive a <video> element from the replay playback cursor.
 *
 * When the app is replaying an experiment, App feeds the cursor (epoch seconds)
 * plus play/pause/speed down here. Once per replay range we fetch every chunk
 * covering it (cached), then on each cursor tick {@link chunkAtCursor} tells us
 * which chunk + offset the cursor lands on:
 *   - chunk changed → point the element at that chunk and seek to the offset;
 *   - paused / scrubbed → seek to the exact offset;
 *   - playing → let the element run at `playbackRate = min(speed, 16)` and only
 *     re-seek when it drifts past a threshold, so 1×/4×/16× stay smooth.
 * A cursor in a gap (pruned/never-recorded) reports `hasVideo:false` so the
 * panel can show a "no video" overlay while the clock keeps running.
 *
 * Browser-only (HTMLVideoElement seek/playback); the cursor→chunk math it drives
 * is unit tested in videoSync.test.ts.
 */

import { useEffect, useRef, useState } from "react";
import { chunkUrl, listChunks } from "./videoApi";
import { chunkAtCursor, type VideoChunk } from "./videoSync";

/** Re-seek only when the element drifts this far from the wanted offset (s). */
const DRIFT_SEC = 0.35;
/** Video elements cap playbackRate; 16 is safe in Chromium. */
const MAX_RATE = 16;

export interface ReplayView {
  startSec: number;
  endSec: number;
  cursorSec: number;
  playing: boolean;
  speed: number;
}

export interface UseVideoReplayArgs {
  baseUrl: string;
  cameraId: string;
  videoRef: { current: HTMLVideoElement | null };
  replay: ReplayView | null;
}

export interface VideoReplayState {
  /** A chunk covers the cursor (else the panel shows the gap overlay). */
  hasVideo: boolean;
  /** The chunk index is still being fetched for this range. */
  loading: boolean;
  error: string | null;
}

export function useVideoReplay(args: UseVideoReplayArgs): VideoReplayState {
  const { baseUrl, cameraId, videoRef, replay } = args;

  const [chunks, setChunks] = useState<VideoChunk[]>([]);
  const [loading, setLoading] = useState(false);
  const [hasVideo, setHasVideo] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const loadedIdRef = useRef<string | null>(null);

  const startSec = replay?.startSec;
  const endSec = replay?.endSec;

  // Fetch every chunk over the replay range once (range is fixed per session).
  useEffect(() => {
    if (!replay || !cameraId || startSec === undefined || endSec === undefined) {
      setChunks([]);
      return;
    }
    let alive = true;
    const ac = new AbortController();
    setLoading(true);
    listChunks(
      baseUrl,
      { cameraId, fromNs: startSec * 1e9, toNs: endSec * 1e9 },
      ac.signal,
    )
      .then((cs) => {
        if (alive) setChunks(cs);
      })
      .catch((e: unknown) => {
        if (alive && !ac.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
      ac.abort();
    };
  }, [baseUrl, cameraId, replay, startSec, endSec]);

  // Sync the element to the cursor on every tick / control change.
  const cursorSec = replay?.cursorSec;
  const playing = replay?.playing ?? false;
  const speed = replay?.speed ?? 1;
  useEffect(() => {
    const v = videoRef.current;
    if (!v || !replay || cursorSec === undefined) return;
    const hit = chunkAtCursor(cursorSec * 1e9, chunks);
    if (!hit) {
      setHasVideo(false);
      if (!v.paused) v.pause();
      return;
    }
    setHasVideo(true);
    if (loadedIdRef.current !== hit.chunkId) {
      loadedIdRef.current = hit.chunkId;
      v.src = chunkUrl(baseUrl, hit.chunkId);
      v.load();
      // Seek once metadata is ready (currentTime before that is ignored).
      const onMeta = (): void => {
        v.currentTime = hit.offsetSec;
        v.removeEventListener("loadedmetadata", onMeta);
      };
      v.addEventListener("loadedmetadata", onMeta);
    }
    v.playbackRate = Math.min(speed, MAX_RATE);
    if (playing) {
      if (Math.abs(v.currentTime - hit.offsetSec) > DRIFT_SEC && v.readyState >= 1) {
        v.currentTime = hit.offsetSec;
      }
      void v.play().catch(() => undefined);
    } else {
      if (!v.paused) v.pause();
      if (v.readyState >= 1 && Math.abs(v.currentTime - hit.offsetSec) > 0.001) {
        v.currentTime = hit.offsetSec;
      }
    }
  }, [baseUrl, chunks, cursorSec, playing, speed, replay, videoRef]);

  return { hasVideo, loading, error };
}
