import { useEffect, useRef, useState } from "react";
import { createLiveClient } from "@gantry/api-client";
import type { TimeSeriesStore } from "@gantry/timeseries";
import { frameNumeric } from "./valueKind";
import { channelKey } from "./channel";

export type ConnState = "idle" | "connecting" | "live" | "reconnecting" | "error";

export interface LiveStreamStatus {
  conn: ConnState;
  fps: number;
  droppedLate: number;
  reconnects: number;
  lastError: string | null;
}

export interface UseLiveStreamArgs {
  baseUrl: string;
  store: TimeSeriesStore;
  deviceId: string;
  channels: string[];
  replaySeconds: number;
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const t = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(t);
        resolve();
      },
      { once: true },
    );
  });
}

/**
 * Subscribe to LiveService.Subscribe (server-streaming) for the selected
 * channels and append frames into the store.
 *
 * Freshness/reconnect model (the JetStream-replay hot-read path):
 * - Every (re)subscribe requests `replaySeconds` of history, so a fresh mount,
 *   a selection change, or a dropped-and-reconnected stream all *heal* the
 *   window from the stream rather than showing a gap.
 * - On stream error or clean close we reconnect with exponential backoff
 *   (250ms → 5s cap). A successful data frame resets the backoff.
 * - The connection is independent of chart pause: it keeps buffering so resume
 *   is seamless.
 *
 * Keepalive contract (live.proto): the server sends a SubscribeResponse with
 * zero frames immediately on a successful subscribe (stream-open signal) and
 * may repeat empty responses as heartbeats. Any response — empty included — is
 * proof the stream is open, so it marks the connection LIVE and resets backoff.
 * Empty responses carry no data and never touch the store.
 *
 * Frames are keyed by (packet, name) — see channel.ts. The request is routed by
 * channel NAME, so a subscription to "temp" returns frames from every packet
 * that exposes "temp"; re-keying on (packet, name) keeps imu.temp and
 * power.temp in separate buffers.
 */
export function useLiveStream(args: UseLiveStreamArgs): LiveStreamStatus {
  const { baseUrl, store, deviceId, channels, replaySeconds } = args;

  const [conn, setConn] = useState<ConnState>("idle");
  const [fps, setFps] = useState(0);
  const [droppedLate, setDroppedLate] = useState(0);
  const [reconnects, setReconnects] = useState(0);
  const [lastError, setLastError] = useState<string | null>(null);

  const frameCounter = useRef(0);
  // Keep the latest channel list in a ref so the connection effect can read it
  // without listing the (unstable) array in its dependencies.
  const channelsRef = useRef(channels);
  channelsRef.current = channels;
  const channelsKey = channels.join(",");

  // Sample fps + dropped-late once per second.
  useEffect(() => {
    const id = setInterval(() => {
      setFps(frameCounter.current);
      frameCounter.current = 0;
      setDroppedLate(store.totalDroppedLate());
    }, 1000);
    return () => clearInterval(id);
  }, [store]);

  // Connection + reconnect-with-backoff loop.
  useEffect(() => {
    if (channelsKey.length === 0) {
      setConn("idle");
      return;
    }
    const ac = new AbortController();
    let stopped = false;
    let backoff = 250;
    const client = createLiveClient(baseUrl);

    async function run(): Promise<void> {
      let first = true;
      while (!stopped) {
        setConn(first ? "connecting" : "reconnecting");
        if (!first) setReconnects((n) => n + 1);
        first = false;
        try {
          const stream = client.subscribe(
            { deviceId, channels: channelsRef.current, replaySeconds },
            { signal: ac.signal },
          );
          for await (const resp of stream) {
            if (stopped) break;
            // Any response (incl. the empty stream-open / keepalive) means the
            // stream is healthy: mark live and reset backoff.
            setConn("live");
            backoff = 250;
            frameCounter.current += resp.frames.length;
            for (const f of resp.frames) {
              const v = frameNumeric(f);
              if (v !== null) {
                store.append(channelKey(f.packet, f.channel), f.timestampNs, v);
              }
            }
          }
          // Clean server close: fall through, back off, re-subscribe (re-replay).
        } catch (err) {
          if (ac.signal.aborted || stopped) return;
          setLastError(err instanceof Error ? err.message : String(err));
          setConn("error");
        }
        if (stopped || ac.signal.aborted) return;
        await sleep(backoff, ac.signal);
        backoff = Math.min(backoff * 2, 5000);
      }
    }

    void run();
    return () => {
      stopped = true;
      ac.abort();
    };
  }, [baseUrl, store, deviceId, channelsKey, replaySeconds]);

  return { conn, fps, droppedLate, reconnects, lastError };
}
