import { createContext, useContext, useMemo, useRef, type ReactNode } from "react";
import { TimeSeriesStore } from "@gantry/timeseries";
import { useLiveStream, type LiveStreamStatus } from "../useLiveStream";
import { resolveBaseUrl, REPLAY_SECONDS } from "../config";
import { subscribeNames } from "../channel";
import { useWorkspaceStore } from "../store/workspaceStore";
import { useExtraKeysUnion } from "../store/extraKeys";
import { layoutChannelKeys } from "../workspace/layout";

/**
 * App-level live telemetry context.
 *
 * A single {@link TimeSeriesStore} and a single LiveService subscription live
 * here, above the router, so the connection (and its buffered history) persist
 * across page navigation and every panel reads one ring store. The subscription
 * is driven by the UNION of channels the active workspace's panels need plus the
 * sidebar selection and any 3D pose bindings — exactly the old App behaviour,
 * repackaged so the panel grid is the source of the channel set.
 */
export interface LiveContextValue {
  store: TimeSeriesStore;
  status: LiveStreamStatus;
  baseUrl: string;
  /** The (packet,name) keys currently subscribed (for status/history). */
  subscribedKeys: string[];
}

const Ctx = createContext<LiveContextValue | null>(null);

export function LiveProvider({ children }: { children: ReactNode }) {
  const baseUrl = useMemo(resolveBaseUrl, []);
  const storeRef = useRef<TimeSeriesStore | null>(null);
  if (storeRef.current === null) storeRef.current = new TimeSeriesStore(120_000);
  const store = storeRef.current;

  const panels = useWorkspaceStore((s) => s.panels);
  const selection = useWorkspaceStore((s) => s.selection);
  const extraKeys = useExtraKeysUnion();

  const subscribedKeys = useMemo(() => {
    const keys = new Set<string>([
      ...layoutChannelKeys(panels),
      ...selection,
      ...extraKeys,
    ]);
    return [...keys];
  }, [panels, selection, extraKeys]);

  const channels = useMemo(() => subscribeNames(subscribedKeys), [subscribedKeys]);

  const status = useLiveStream({
    baseUrl,
    store,
    deviceId: "",
    channels,
    replaySeconds: REPLAY_SECONDS,
  });

  const value = useMemo<LiveContextValue>(
    () => ({ store, status, baseUrl, subscribedKeys }),
    [store, status, baseUrl, subscribedKeys],
  );

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useLive(): LiveContextValue {
  const v = useContext(Ctx);
  if (!v) throw new Error("useLive must be used within a LiveProvider");
  return v;
}
