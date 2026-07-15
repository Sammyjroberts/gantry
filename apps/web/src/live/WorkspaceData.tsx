import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { Experiment } from "@gantry/api-client";
import { useLive } from "./LiveContext";
import { useClock } from "../store/clock";
import { useTimeStore, resolveVisible } from "../store/timeStore";
import { useWorkspaceStore } from "../store/workspaceStore";
import { useHistory } from "../useHistory";
import { subscribeNames } from "../channel";
import { layoutChannelKeys } from "../workspace/layout";
import { combineSeries, type CombinedPlot, type Span } from "../history";
import { experimentRegions, type ExperimentRegion } from "../experiments";
import { valueAtOrBefore } from "../pose";
import { HISTORY_TARGET_POINTS, HISTORY_HORIZON_SEC } from "../config";

/**
 * Per-tick telemetry projection for the workspace panels.
 *
 * This is the repackaged core of the old App render body: it resolves the
 * visible window (live zoom or replay sweep) from the time store on each 150ms
 * clock tick, runs the ONE history layer over the union of bound channels, and
 * exposes the two reads panels need — `combinedFor(key)` (ring+history merged
 * for a chart) and `sampleAt(key)` (live latest / value-at-cursor for readouts).
 * Panels never touch the store or history directly; they call these.
 */
export interface WorkspaceDataValue {
  xRange: [number, number];
  cursorSec: number | undefined;
  clamped: boolean;
  windowSec: number;
  replaying: boolean;
  nowSec: number;
  loading: boolean;
  truncated: boolean;
  regions: ExperimentRegion[];
  /** Merged ring+history series for a channel over the visible window. */
  combinedFor: (key: string) => CombinedPlot;
  /** Live latest value, or value-at-cursor during replay. */
  sampleAt: (key: string) => number | null;
  /** Aligned series around the cursor for a channel (readout replay reads). */
  seriesAtCursor: (key: string) => { x: number[]; y: (number | null)[] } | null;
}

const Ctx = createContext<WorkspaceDataValue | null>(null);

export function WorkspaceDataProvider({
  experiments,
  children,
}: {
  experiments: Experiment[];
  children: ReactNode;
}) {
  const { store } = useLive();
  const nowMs = useClock();
  const nowSec = nowMs / 1000;

  const windowSec = useTimeStore((s) => s.windowSec);
  const zoom = useTimeStore((s) => s.zoom);
  const replay = useTimeStore((s) => s.replay);
  const panels = useWorkspaceStore((s) => s.panels);

  const timeState = { windowSec, zoom, replay } as Parameters<typeof resolveVisible>[0];
  const visible = resolveVisible(timeState, nowMs);
  const xRange = visible.range;
  const cursorSec = visible.cursorSec;

  const boundKeys = useMemo(() => layoutChannelKeys(panels), [panels]);
  const boundKey = boundKeys.join(",");
  const channelNames = useMemo(() => subscribeNames(boundKeys), [boundKey]); // eslint-disable-line react-hooks/exhaustive-deps

  // Per-channel ring/history seam (oldest buffered sample).
  const ringOldestByKey = useMemo(() => {
    const m = new Map<string, number | null>();
    for (const key of boundKeys) {
      const o = store.get(key)?.oldest();
      m.set(key, o ? Number(o.tNs) / 1e9 : null);
    }
    return m;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [boundKey, nowSec, store]);

  const visibleWidthSec = xRange[1] - xRange[0];
  const historyWindow: Span | null =
    boundKeys.length === 0
      ? null
      : replay
        ? [replay.startSec, replay.endSec]
        : [xRange[0], xRange[1]];

  const history = useHistory({
    baseUrl: useLive().baseUrl,
    deviceId: "",
    channelNames,
    channelKeys: boundKeys,
    window: historyWindow,
    windowSec: replay ? windowSec : visibleWidthSec,
    nowSec,
    ringOldestByKey,
    targetPoints: HISTORY_TARGET_POINTS,
    enabled: boundKeys.length > 0,
  });

  const regions = useMemo(
    () => experimentRegions(experiments, nowSec),
    [experiments, nowSec],
  );

  const value = useMemo<WorkspaceDataValue>(() => {
    const fromNs = BigInt(Math.floor(xRange[0] * 1e9));
    const toNs = BigInt(Math.ceil(xRange[1] * 1e9));

    const combinedFor = (key: string): CombinedPlot => {
      const ring = store.window(key, fromNs, toNs);
      const seamSec = ringOldestByKey.get(key);
      const seam = seamSec == null ? Number.POSITIVE_INFINITY : seamSec;
      const histRender = history.render(key, [xRange[0], xRange[1]]);
      return combineSeries(ring, histRender, seam);
    };

    const seriesAtCursor = (key: string): { x: number[]; y: (number | null)[] } | null => {
      if (cursorSec === undefined) return null;
      const cursorNs = BigInt(Math.floor(cursorSec * 1e9));
      const w = store.window(key, undefined, cursorNs);
      if (w.x.length > 0) {
        return { x: Array.from(w.x), y: Array.from(w.y) as (number | null)[] };
      }
      const hr = history.render(key, [cursorSec - HISTORY_HORIZON_SEC, cursorSec]);
      if (!hr) return null;
      return hr.kind === "raw"
        ? { x: Array.from(hr.x), y: Array.from(hr.y) as (number | null)[] }
        : { x: Array.from(hr.x), y: Array.from(hr.mean) as (number | null)[] };
    };

    const sampleAt = (key: string): number | null => {
      if (replay && cursorSec !== undefined) {
        const cursorNs = BigInt(Math.floor(cursorSec * 1e9));
        const w = store.window(key, undefined, cursorNs);
        if (w.x.length > 0) return w.y[w.x.length - 1] ?? null;
        const s = seriesAtCursor(key);
        return s ? valueAtOrBefore(s.x, s.y, cursorSec) : null;
      }
      return store.get(key)?.latest()?.value ?? null;
    };

    return {
      xRange: [xRange[0], xRange[1]],
      cursorSec,
      clamped: visible.clamped,
      windowSec,
      replaying: !!replay,
      nowSec,
      loading: history.loading,
      truncated: history.truncated,
      regions,
      combinedFor,
      sampleAt,
      seriesAtCursor,
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    xRange[0],
    xRange[1],
    cursorSec,
    visible.clamped,
    windowSec,
    replay,
    nowSec,
    history.loading,
    history.truncated,
    history.version,
    regions,
    ringOldestByKey,
    store,
  ]);

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useWorkspaceData(): WorkspaceDataValue {
  const v = useContext(Ctx);
  if (!v) throw new Error("useWorkspaceData must be used within a WorkspaceDataProvider");
  return v;
}
