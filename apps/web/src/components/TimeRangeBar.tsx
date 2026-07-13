import { useEffect, useState } from "react";
import { TIME_PRESETS } from "../config";
import {
  formatDurationShort,
  formatRangeLabel,
  fromDatetimeLocal,
  toDatetimeLocal,
} from "../timeFormat";

export interface TimeRangeBarProps {
  /** Live follow vs. fixed inspect range. */
  mode: "live" | "inspect";
  /** Live-follow width (drives the preset highlight). */
  windowSec: number;
  /** Resolved visible range `[minSec, maxSec]` (epoch seconds). */
  range: [number, number];
  /** The range was clamped to the buffer/live edge. */
  clamped: boolean;
  /** Some visible channel reached past stream retention. */
  truncated: boolean;
  /** A history fetch is in flight. */
  loading: boolean;
  onPreset: (sec: number) => void;
  onStepBack: () => void;
  onStepForward: () => void;
  onZoomOut: () => void;
  onAbsolute: (fromSec: number, toSec: number) => void;
  onBackToLive: () => void;
}

/**
 * Grafana-style time navigation toolbar sitting above the charts. Drives the
 * shared zoom state machine (see zoom.ts): relative presets snap to LIVE follow,
 * absolute from/to enters INSPECT at a fixed range, step buttons shift by the
 * window's own width, and the readout doubles as a click-to-edit absolute input.
 */
export function TimeRangeBar({
  mode,
  windowSec,
  range,
  clamped,
  truncated,
  loading,
  onPreset,
  onStepBack,
  onStepForward,
  onZoomOut,
  onAbsolute,
  onBackToLive,
}: TimeRangeBarProps) {
  const [editing, setEditing] = useState(false);
  const [fromStr, setFromStr] = useState("");
  const [toStr, setToStr] = useState("");

  // Seed the inputs from the current range whenever the editor opens.
  useEffect(() => {
    if (editing) {
      setFromStr(toDatetimeLocal(range[0]));
      setToStr(toDatetimeLocal(range[1]));
    }
    // Only re-seed on open, not on every range tick while editing.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editing]);

  const live = mode === "live";
  const width = range[1] - range[0];

  const applyAbsolute = () => {
    const from = fromDatetimeLocal(fromStr);
    const to = fromDatetimeLocal(toStr);
    if (from !== null && to !== null && to > from) {
      onAbsolute(from, to);
      setEditing(false);
    }
  };

  return (
    <div className="trbar">
      <div className="trbar-presets">
        {TIME_PRESETS.map((p) => (
          <button
            key={p.sec}
            className={`tr-preset ${live && windowSec === p.sec ? "is-active" : ""}`}
            onClick={() => onPreset(p.sec)}
            title={`live · ${formatDurationShort(p.sec)} window`}
          >
            {p.label}
          </button>
        ))}
      </div>

      <div className="trbar-nav">
        <button className="tr-btn" onClick={onStepBack} title="step back one window">
          ◀
        </button>
        {editing ? (
          <div className="tr-abs">
            <input
              type="datetime-local"
              step={1}
              className="tr-abs-input"
              value={fromStr}
              onChange={(e) => setFromStr(e.target.value)}
            />
            <span className="tr-abs-sep">→</span>
            <input
              type="datetime-local"
              step={1}
              className="tr-abs-input"
              value={toStr}
              onChange={(e) => setToStr(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") applyAbsolute();
                if (e.key === "Escape") setEditing(false);
              }}
            />
            <button className="tr-btn tr-btn--go" onClick={applyAbsolute} title="apply range">
              apply
            </button>
            <button className="tr-btn" onClick={() => setEditing(false)} title="cancel">
              ✕
            </button>
          </div>
        ) : (
          <button
            className={`tr-readout ${live ? "is-live" : ""}`}
            onClick={() => setEditing(true)}
            title="click to set an absolute range"
          >
            {live && <span className="tr-live-dot" />}
            <span className="tr-readout-text">{formatRangeLabel(range)}</span>
            {loading && <span className="tr-spin" title="loading history" />}
          </button>
        )}
        <button className="tr-btn" onClick={onStepForward} title="step forward one window">
          ▶
        </button>
        <button className="tr-btn" onClick={onZoomOut} title="zoom out ×2">
          ⊖
        </button>
      </div>

      <div className="trbar-flags">
        {truncated && (
          <span className="tr-flag" title="Range reaches past stream retention; earlier data is unavailable.">
            beyond retention
          </span>
        )}
        {clamped && (
          <span className="tr-flag tr-flag--muted" title="Clamped to available buffer/live edge.">
            clamped
          </span>
        )}
        {!live && (
          <button className="tr-live-btn" onClick={onBackToLive} title="resume live follow">
            ⟳ live
          </button>
        )}
        <span className="tr-width" title="visible width">
          {formatDurationShort(width)}
        </span>
      </div>
    </div>
  );
}
