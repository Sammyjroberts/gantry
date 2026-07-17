import { useEffect, useMemo, useRef, useState } from "react";
import { createQueryClient, type ChannelSeries } from "@gantry/api-client";
import { apiClientOptions } from "./auth/authGate";
import {
  HistoryCache,
  coalesceSpans,
  computeFetchSpans,
  maxPointsForFetch,
  tierSpanSec,
  type HistoryRender,
  type Span,
} from "./history";
import { channelKey } from "./channel";

export interface UseHistoryArgs {
  baseUrl: string;
  deviceId: string;
  /** Distinct channel NAMES to request (QueryRange routes by name, like Live). */
  channelNames: string[];
  /** Selected (packet, name) keys — the identities we track coverage/render for. */
  channelKeys: string[];
  /** Visible window `[fromSec, toSec]` (epoch seconds), or null to disable. */
  window: Span | null;
  /** Window width used to pick the resolution tier. */
  windowSec: number;
  /** Live edge (epoch seconds) — the right edge of ring coverage. */
  nowSec: number;
  /** Oldest buffered sample per channel key (epoch seconds); null = empty ring. */
  ringOldestByKey: Map<string, number | null>;
  /** Target buckets per channel across the window (server default 500). */
  targetPoints: number;
  /** Master switch (e.g. only when charts exist / inspecting / replaying). */
  enabled: boolean;
}

export interface UseHistoryResult {
  /** The immutable-history cache; call {@link HistoryCache.select} to read. */
  cache: HistoryCache;
  /** Current resolution tier (bucket-span seconds) for the visible window. */
  tierSpan: number;
  /** A fetch is in flight (drives the per-chart shimmer). */
  loading: boolean;
  /** The visible range reached past stream retention on the last fetch. */
  truncated: boolean;
  /** Bumps whenever new history lands, so consumers re-render. */
  version: number;
  /** Convenience: cache.select at the current tier for a channel key. */
  render: (channelKey: string, window: Span) => HistoryRender | null;
}

/**
 * Fetch-on-navigate history for the charts.
 *
 * Given the visible window + selected channels, it figures out which spans the
 * ring buffer doesn't cover (older than what's buffered) and which the current
 * resolution tier hasn't already fetched, then pulls them via
 * QueryService.QueryRange and caches them immutably (see history.ts). Navigation
 * is debounced (~250ms) so scrubbing doesn't spam queries; the gap math is
 * cheap and issues no RPC when the window is already covered, so it is safe to
 * run on every tick.
 *
 * Zooming in shrinks the window, selects a finer tier, and re-fetches at higher
 * `max_points`; the coarse tier stays cached for when you zoom back out.
 */
export function useHistory(args: UseHistoryArgs): UseHistoryResult {
  const {
    baseUrl,
    deviceId,
    channelNames,
    channelKeys,
    window,
    windowSec,
    nowSec,
    ringOldestByKey,
    targetPoints,
    enabled,
  } = args;

  const cacheRef = useRef<HistoryCache | null>(null);
  if (cacheRef.current === null) cacheRef.current = new HistoryCache();
  const cache = cacheRef.current;

  const clientRef = useRef(createQueryClient(baseUrl, apiClientOptions()));
  const baseUrlRef = useRef(baseUrl);
  if (baseUrlRef.current !== baseUrl) {
    baseUrlRef.current = baseUrl;
    clientRef.current = createQueryClient(baseUrl, apiClientOptions());
  }

  const [version, setVersion] = useState(0);
  const [loading, setLoading] = useState(false);
  const [truncated, setTruncated] = useState(false);

  const tierSpan = useMemo(
    () => tierSpanSec(windowSec, targetPoints),
    [windowSec, targetPoints],
  );

  // Latest fast-moving inputs read at fetch time without re-arming the effect.
  const liveRef = useRef({ nowSec, ringOldestByKey, channelNames, channelKeys, window });
  liveRef.current = { nowSec, ringOldestByKey, channelNames, channelKeys, window };

  const inFlightRef = useRef(0);

  // Debounce the visible window: only the trailing value after 250ms of quiet
  // drives a fetch pass. Rounded to whole seconds so live-mode's per-tick slide
  // doesn't re-arm the timer forever.
  const fromKey = window ? Math.floor(window[0]) : null;
  const toKey = window ? Math.ceil(window[1]) : null;
  const channelsKey = channelKeys.join(",");
  const [pass, setPass] = useState(0);

  useEffect(() => {
    if (!enabled || fromKey === null) return;
    const id = setTimeout(() => setPass((p) => p + 1), 250);
    return () => clearTimeout(id);
  }, [enabled, fromKey, toKey, channelsKey, tierSpan, deviceId]);

  useEffect(() => {
    if (!enabled) return;
    const live = liveRef.current;
    const win = live.window;
    if (!win || live.channelNames.length === 0) return;

    // Union of the still-missing spans across all selected channels.
    const minSpan = tierSpan; // don't chase sub-bucket slivers
    const gaps: Span[] = [];
    for (const key of live.channelKeys) {
      const oldest = live.ringOldestByKey.get(key) ?? null;
      const ring: Span | null = oldest === null ? null : [oldest, live.nowSec];
      const need = computeFetchSpans(win, ring, cache.covered(tierSpan, key), minSpan);
      gaps.push(...need);
    }
    const spans = coalesceSpans(gaps);
    if (spans.length === 0) return;

    const ac = new AbortController();
    const client = clientRef.current;
    const names = live.channelNames;
    const keys = live.channelKeys;

    void (async () => {
      inFlightRef.current += spans.length;
      setLoading(true);
      let sawTruncation = false;
      try {
        for (const span of spans) {
          const startNs = BigInt(Math.floor(span[0] * 1e9));
          const endNs = BigInt(Math.ceil(span[1] * 1e9));
          const maxPoints = maxPointsForFetch(span[1] - span[0], tierSpan);
          let resp;
          try {
            resp = await client.queryRange(
              { deviceId, channels: names, startNs, endNs, maxPointsPerChannel: maxPoints },
              { signal: ac.signal },
            );
          } catch {
            if (ac.signal.aborted) return;
            continue; // transient: leave the span uncached so it retries later
          }
          if (ac.signal.aborted) return;
          if (resp.truncatedByRetention) sawTruncation = true;

          const byKey = new Map<string, ChannelSeries>();
          for (const s of resp.series) byKey.set(channelKey(s.packet, s.channel), s);
          // Record coverage for every requested key (even empty responses) so a
          // genuinely data-free region is not re-fetched forever.
          for (const key of keys) {
            cache.insert(tierSpan, key, span, byKey.get(key) ?? null);
          }
          setVersion((v) => v + 1);
        }
        if (sawTruncation) setTruncated(true);
      } finally {
        inFlightRef.current = Math.max(0, inFlightRef.current - spans.length);
        if (inFlightRef.current === 0) setLoading(false);
      }
    })();

    return () => ac.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pass, enabled, tierSpan, deviceId, cache]);

  const render = useMemo(() => {
    // version is a dependency so the closure is fresh after new data lands.
    void version;
    return (key: string, win: Span) => cache.select(tierSpan, key, win);
  }, [cache, tierSpan, version]);

  return { cache, tierSpan, loading, truncated, version, render };
}
