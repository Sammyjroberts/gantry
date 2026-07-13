import { SPEEDS } from "../playback";
import { formatClock, formatDurationShort } from "../timeFormat";

export interface ReplayBarProps {
  name: string;
  startSec: number;
  endSec: number;
  /** Current playhead (epoch seconds). */
  cursorSec: number;
  playing: boolean;
  speed: number;
  /** Swept fraction [0,1] for the scrub slider. */
  progress: number;
  /** The experiment history is still being fetched. */
  loading: boolean;
  onTogglePlay: () => void;
  /** Seek to a fraction [0,1] of the experiment. */
  onSeekFraction: (fraction: number) => void;
  onSetSpeed: (speed: number) => void;
  onExit: () => void;
}

/**
 * Replay transport bar. Sweeps a per-experiment playhead start→end with
 * play/pause, 1×/4×/16× speed, and a scrub slider that seeks anywhere. The
 * playhead position is owned by the pure playback clock (see playback.ts); this
 * bar is a thin control surface over it. A vertical "now" line mirrors the
 * cursor on every chart (see Chart.cursorSec).
 */
export function ReplayBar({
  name,
  startSec,
  endSec,
  cursorSec,
  playing,
  speed,
  progress,
  loading,
  onTogglePlay,
  onSeekFraction,
  onSetSpeed,
  onExit,
}: ReplayBarProps) {
  const elapsed = Math.max(0, cursorSec - startSec);
  const total = Math.max(0, endSec - startSec);

  return (
    <div className="replay-bar">
      <span className="replay-tag">▶ REPLAY</span>
      <span className="replay-name" title={name}>
        {name}
      </span>

      <button className="replay-play" onClick={onTogglePlay} title={playing ? "pause" : "play"}>
        {playing ? "❚❚" : "▶"}
      </button>

      <input
        className="replay-scrub"
        type="range"
        min={0}
        max={1000}
        value={Math.round(progress * 1000)}
        onChange={(e) => onSeekFraction(Number(e.target.value) / 1000)}
        title="scrub"
      />

      <span className="replay-time">
        <span className="replay-clock">{formatClock(cursorSec)}</span>
        <span className="replay-elapsed">
          +{formatDurationShort(elapsed)} / {formatDurationShort(total)}
        </span>
      </span>

      <span className="replay-speeds">
        {SPEEDS.map((s) => (
          <button
            key={s}
            className={`replay-speed ${speed === s ? "is-active" : ""}`}
            onClick={() => onSetSpeed(s)}
          >
            {s}×
          </button>
        ))}
      </span>

      {loading && <span className="replay-loading">buffering…</span>}

      <button className="replay-exit" onClick={onExit} title="exit replay">
        ✕ exit
      </button>
    </div>
  );
}
