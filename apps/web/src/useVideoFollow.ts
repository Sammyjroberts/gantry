/**
 * useVideoFollow — live-ish playback of a camera that is still recording.
 *
 * Polls a trailing window of chunks (~10s) every ~2s and plays them back-to-back
 * in the caller's <video> element. Each chunk is fetched to a Blob and shown via
 * an object URL, revoked as the next one loads (so memory stays flat over a long
 * watch). Selection is the pure {@link nextLiveChunk} — always jump to the newest
 * chunk not yet started, so any backlog is dropped and lag stays bounded; a few
 * seconds of latency is expected and reported via {@link lagSeconds}.
 *
 * Browser-only (object URLs + <video> playback are absent in jsdom); the pure
 * selection/lag math it drives is unit tested in videoSync.test.ts.
 */

import { useEffect, useRef, useState } from "react";
import { chunkUrl, listChunks } from "./videoApi";
import { lagSeconds, nextLiveChunk, trailingWindow, type VideoChunk } from "./videoSync";

const POLL_MS = 2000;
const WINDOW_SEC = 10;

export interface UseVideoFollowArgs {
  baseUrl: string;
  cameraId: string;
  videoRef: { current: HTMLVideoElement | null };
  enabled: boolean;
}

export interface VideoFollowState {
  /** Seconds behind real time for the chunk on screen, or null when idle. */
  lagSec: number | null;
  /** A chunk is currently loaded/playing. */
  active: boolean;
  error: string | null;
}

export function useVideoFollow(args: UseVideoFollowArgs): VideoFollowState {
  const { baseUrl, cameraId, videoRef, enabled } = args;

  const [lagSec, setLagSec] = useState<number | null>(null);
  const [active, setActive] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const chunksRef = useRef<VideoChunk[]>([]);
  const lastStartedRef = useRef<number | null>(null);
  const currentChunkRef = useRef<VideoChunk | null>(null);
  const currentUrlRef = useRef<string | null>(null);
  const loadingRef = useRef(false);

  useEffect(() => {
    if (!enabled || !cameraId) return;
    let stopped = false;
    const ac = new AbortController();

    const revoke = (url: string | null): void => {
      if (url) URL.revokeObjectURL(url);
    };

    const loadChunk = async (chunk: VideoChunk): Promise<void> => {
      if (loadingRef.current) return;
      loadingRef.current = true;
      try {
        const res = await fetch(chunkUrl(baseUrl, chunk.id), { signal: ac.signal });
        if (!res.ok) return; // pruned/gap → skip, try again next tick
        const blob = await res.blob();
        if (stopped) return;
        const url = URL.createObjectURL(blob);
        const prev = currentUrlRef.current;
        currentUrlRef.current = url;
        currentChunkRef.current = chunk;
        lastStartedRef.current = chunk.startNs;
        const v = videoRef.current;
        if (v) {
          v.src = url;
          void v.play().catch(() => undefined);
        }
        setActive(true);
        revoke(prev);
      } catch (e) {
        if (!ac.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      } finally {
        loadingRef.current = false;
      }
    };

    const advance = (): void => {
      const next = nextLiveChunk(chunksRef.current, lastStartedRef.current);
      if (next) void loadChunk(next);
    };

    // Chain to the next available chunk the instant the current one ends.
    const v = videoRef.current;
    const onEnded = (): void => advance();
    v?.addEventListener("ended", onEnded);

    const poll = async (): Promise<void> => {
      try {
        const [fromNs, toNs] = trailingWindow(Date.now() * 1e6, WINDOW_SEC);
        const chunks = await listChunks(baseUrl, { cameraId, fromNs, toNs }, ac.signal);
        if (stopped) return;
        chunksRef.current = chunks;
        setError(null);
        const cur = currentChunkRef.current;
        if (cur) setLagSec(lagSeconds(cur, Date.now() * 1e6));
        // If nothing is playing yet, or the element is idle/ended, kick playback.
        const el = videoRef.current;
        if (!el || el.paused || el.ended || currentChunkRef.current === null) advance();
      } catch (e) {
        if (!ac.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      }
    };

    void poll();
    const id = setInterval(() => void poll(), POLL_MS);

    return () => {
      stopped = true;
      ac.abort();
      clearInterval(id);
      v?.removeEventListener("ended", onEnded);
      revoke(currentUrlRef.current);
      currentUrlRef.current = null;
      currentChunkRef.current = null;
      lastStartedRef.current = null;
      setActive(false);
      setLagSec(null);
    };
  }, [baseUrl, cameraId, enabled, videoRef]);

  return { lagSec, active, error };
}
