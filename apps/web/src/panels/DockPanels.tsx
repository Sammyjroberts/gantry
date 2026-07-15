import { Suspense, lazy, useCallback, useEffect, useRef } from "react";
import type { Panel, Scene3dConfig, VideoConfig, SqlConfig } from "../workspace/layout";
import { useCatalog } from "../query/useCatalog";
import { useLive } from "../live/LiveContext";
import { useWorkspaceData } from "../live/WorkspaceData";
import { useWorkspaceStore } from "../store/workspaceStore";
import { useExtraKeysStore } from "../store/extraKeys";
import { useVizConfig } from "../hooks/useVizConfig";
import type { Sampler } from "../pose";
import type { ReplayView } from "../useVideoReplay";
import { useTimeStore, resolveVisible } from "../store/timeStore";
import { useClock } from "../store/clock";

// Keep the heavy 3D / video / SQL chunks out of the main bundle — mounted only
// when a panel of that type is on the grid (same lazy discipline as the old App).
const Scene3D = lazy(() => import("../Scene3D"));
const VideoPanel = lazy(() => import("../VideoPanel"));
const SqlConsole = lazy(() => import("../SqlConsole"));

const fallback = <div className="scene3d-loading">loading module…</div>;

// ---- 3D ----

export function Scene3dPanel({ panel }: { panel: Panel }) {
  const config = panel.config as Scene3dConfig;
  const { baseUrl } = useLive();
  const { deviceIds, channelOptions } = useCatalog();
  const data = useWorkspaceData();
  const { loadVizConfig, saveVizConfig } = useVizConfig();
  const setExtra = useExtraKeysStore((s) => s.set);
  const clearExtra = useExtraKeysStore((s) => s.clear);

  // Fresh sampler each render, read imperatively by Scene3D's frame loop.
  const sampleRef = useRef<Sampler>(() => null);
  sampleRef.current = (key: string) => data.sampleAt(key);

  useEffect(() => () => clearExtra(panel.id), [panel.id, clearExtra]);

  // MUST be stable: Scene3D re-fires its binding effect on a new callback
  // identity, and this component re-renders when setExtra mutates the live
  // subscription — an unmemoized closure would loop (React #185).
  const onBoundChannelsChange = useCallback(
    (keys: string[]) => setExtra(panel.id, keys),
    [setExtra, panel.id],
  );

  const devices = config.deviceId ? [config.deviceId] : deviceIds;

  return (
    <Suspense fallback={fallback}>
      <Scene3D
        baseUrl={baseUrl}
        devices={devices.length > 0 ? devices : [""]}
        channels={channelOptions}
        sampleRef={sampleRef}
        replaying={data.replaying}
        onBoundChannelsChange={onBoundChannelsChange}
        loadVizConfig={loadVizConfig}
        saveVizConfig={saveVizConfig}
        onClose={() => {}}
      />
    </Suspense>
  );
}

// ---- video ----

export function VideoPanelPanel({ panel }: { panel: Panel }) {
  const config = panel.config as VideoConfig;
  const { baseUrl } = useLive();
  const nowMs = useClock();
  const windowSec = useTimeStore((s) => s.windowSec);
  const zoom = useTimeStore((s) => s.zoom);
  const replay = useTimeStore((s) => s.replay);

  let view: ReplayView | null = null;
  if (replay) {
    const vis = resolveVisible({ windowSec, zoom, replay } as Parameters<typeof resolveVisible>[0], nowMs);
    if (vis.cursorSec !== undefined) {
      view = {
        startSec: replay.startSec,
        endSec: replay.endSec,
        cursorSec: vis.cursorSec,
        playing: replay.clock.playing,
        speed: replay.clock.speed,
      };
    }
  }

  return (
    <Suspense fallback={fallback}>
      <VideoPanel baseUrl={baseUrl} replay={view} initialCamera={config.cameraId} onClose={() => {}} />
    </Suspense>
  );
}

// ---- SQL ----

export function SqlPanel({ panel }: { panel: Panel }) {
  const config = panel.config as SqlConfig;
  const { baseUrl } = useLive();
  const updateConfig = useWorkspaceStore((s) => s.updateConfig);
  return (
    <Suspense fallback={fallback}>
      <SqlConsole
        baseUrl={baseUrl}
        initialSql={config.sql}
        onSqlChange={(sql) => updateConfig(panel.id, { ...config, sql })}
        onClose={() => {}}
      />
    </Suspense>
  );
}
