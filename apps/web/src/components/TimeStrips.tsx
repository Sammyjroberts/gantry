import { Pause, Play } from "lucide-react";
import { TimeRangeBar } from "./TimeRangeBar";
import { ReplayBar } from "./ReplayBar";
import { ExperimentBar } from "./ExperimentBar";
import { useTimeStore, replayProgressAt } from "../store/timeStore";
import { useWorkspaceData } from "../live/WorkspaceData";
import { useClock } from "../store/clock";
import { useLive } from "../live/LiveContext";
import { subscribeNames } from "../channel";
import type { UseExperimentsResult } from "../useExperiments";

/**
 * The global strips above the routed content: time-range navigation, the replay
 * scrubber (when replaying), and the experiment bar. All wired to the time store
 * and the workspace data projection (range / loading / regions / cursor).
 */
export function TimeStrips({ exp }: { exp: UseExperimentsResult }) {
  const data = useWorkspaceData();
  const { baseUrl, subscribedKeys } = useLive();
  const nowMs = useClock();

  const windowSec = useTimeStore((s) => s.windowSec);
  const zoomMode = useTimeStore((s) => s.zoom.mode);
  const replay = useTimeStore((s) => s.replay);
  const paused = useTimeStore((s) => s.paused);
  const t = useTimeStore();

  const exportChannels = subscribeNames(subscribedKeys);

  return (
    <>
      <div className="time-strip-row">
        <button
          className={`ctl-btn ${paused ? "is-paused" : ""}`}
          onClick={() => t.togglePaused()}
          title={paused ? "resume live" : "pause live"}
          data-testid="pause-toggle"
        >
          {paused ? <Play size={13} /> : <Pause size={13} />}
        </button>
        <div className="time-strip-grow">
          <TimeRangeBar
            mode={zoomMode}
            windowSec={windowSec}
            range={data.xRange}
            clamped={data.clamped}
            truncated={data.truncated}
            loading={data.loading}
            onPreset={(sec) => t.applyPreset(sec)}
            onStepBack={() => t.stepBack()}
            onStepForward={() => t.stepForward()}
            onZoomOut={() => t.zoomOut()}
            onAbsolute={(from, to) => t.setRange(from, to)}
            onBackToLive={() => t.backToLive()}
          />
        </div>
      </div>

      {replay && data.cursorSec !== undefined && (
        <ReplayBar
          name={replay.name}
          startSec={replay.startSec}
          endSec={replay.endSec}
          cursorSec={data.cursorSec}
          playing={replay.clock.playing}
          speed={replay.clock.speed}
          progress={replayProgressAt(t, nowMs) ?? 0}
          loading={data.loading}
          onTogglePlay={() => t.replayTogglePlay()}
          onSeekFraction={(f) => t.replaySeekFraction(f)}
          onSetSpeed={(s) => t.replaySetSpeed(s)}
          onExit={() => t.exitReplay()}
        />
      )}

      <ExperimentBar
        experiments={exp.experiments}
        running={exp.running}
        error={exp.error}
        baseUrl={baseUrl}
        exportChannels={exportChannels}
        onStart={(name) => void exp.start({ name })}
        onStop={(id) => void exp.stop(id)}
        onUpdate={(id, name, notes) => void exp.update(id, name, notes)}
        onDelete={(id) => void exp.remove(id)}
        onZoomTo={(start, end) => t.fitTo(start, end)}
        onReplay={(id, start, end) => {
          const name = exp.experiments.find((e) => e.id === id)?.name ?? id;
          t.enterReplay({ id, name, startSec: start, endSec: end });
        }}
      />
    </>
  );
}
