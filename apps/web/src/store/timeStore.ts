/**
 * Time / zoom / playback store.
 *
 * The shared x-window state machine and the replay clock move behind a zustand
 * store so every panel in the workspace subscribes to one source of truth
 * without prop-drilling through the grid. The PURE transition logic still lives
 * in zoom.ts and playback.ts — this store only holds the state and dispatches
 * those transitions, so their unit coverage stays authoritative (the store
 * layer adds a thin parity test on top; see timeStore.test.ts).
 *
 * THE HOT-PATH RULE: this store holds only *view* state (window, zoom mode,
 * replay clock) — never per-frame telemetry. Charts/3D/readouts sample the
 * resolved window and cursor on the existing 150ms tick; they do not re-render
 * from this store per frame.
 */

import { create } from "zustand";
import { HISTORY_HORIZON_SEC, EXPERIMENT_FIT_PAD, REPLAY_CURSOR_FRAC } from "../config";
import {
  INITIAL_ZOOM,
  applyPreset,
  backToLive,
  panBy,
  resolveWindow,
  setRange,
  stepBy,
  zoomAt,
  zoomOutBy,
  type Bounds,
  type ZoomState,
} from "../zoom";
import {
  cursorAt,
  progress as replayProgress,
  seek as replaySeek,
  setSpeed as replaySetSpeed,
  startReplay,
  togglePlay as replayToggle,
  type PlaybackState,
} from "../playback";

/** Active replay session: the pure clock plus the experiment it sweeps. */
export interface ReplaySession {
  id: string;
  name: string;
  startSec: number;
  endSec: number;
  clock: PlaybackState;
}

export interface TimeState {
  windowSec: number;
  paused: boolean;
  zoom: ZoomState;
  replay: ReplaySession | null;

  // ---- window width / pause ----
  setWindowSec: (sec: number) => void;
  setPaused: (paused: boolean) => void;
  togglePaused: () => void;

  // ---- zoom (pure ops on the shared state) ----
  applyPreset: (sec: number) => void;
  zoomAt: (centerSec: number, factor: number) => void;
  setRange: (minSec: number, maxSec: number) => void;
  panBy: (deltaSec: number) => void;
  stepBack: () => void;
  stepForward: () => void;
  zoomOut: () => void;
  backToLive: () => void;
  /** Fit the window to [start,end] with padding; leaves any replay. */
  fitTo: (startSec: number, endSec: number) => void;

  // ---- replay clock ----
  enterReplay: (session: { id: string; name: string; startSec: number; endSec: number }) => void;
  exitReplay: () => void;
  replayTogglePlay: () => void;
  replaySeekFraction: (f: number) => void;
  replaySetSpeed: (s: number) => void;
}

/** The buffered/navigable extent at wall time `nowMs` (epoch seconds). */
export function boundsAt(nowMs: number): Bounds {
  const now = nowMs / 1000;
  return { oldest: now - HISTORY_HORIZON_SEC, now };
}

export const useTimeStore = create<TimeState>((set, get) => ({
  windowSec: 60,
  paused: false,
  zoom: INITIAL_ZOOM,
  replay: null,

  setWindowSec: (sec) => set({ windowSec: Math.max(0.1, sec) }),
  setPaused: (paused) => set({ paused }),
  togglePaused: () => set((s) => ({ paused: !s.paused })),

  applyPreset: (sec) => {
    const r = applyPreset(sec);
    set({ windowSec: r.windowSec, zoom: r.zoom });
  },
  zoomAt: (centerSec, factor) => {
    const { windowSec, zoom } = get();
    set({ zoom: zoomAt(zoom, windowSec, boundsAt(Date.now()), centerSec, factor) });
  },
  setRange: (minSec, maxSec) => {
    const { zoom } = get();
    set({ zoom: setRange(zoom, boundsAt(Date.now()), minSec, maxSec) });
  },
  panBy: (deltaSec) => {
    const { windowSec, zoom } = get();
    set({ zoom: panBy(zoom, windowSec, boundsAt(Date.now()), deltaSec) });
  },
  stepBack: () => {
    const { windowSec, zoom } = get();
    set({ zoom: stepBy(zoom, windowSec, boundsAt(Date.now()), -1) });
  },
  stepForward: () => {
    const { windowSec, zoom } = get();
    set({ zoom: stepBy(zoom, windowSec, boundsAt(Date.now()), 1) });
  },
  zoomOut: () => {
    const { windowSec, zoom } = get();
    set({ zoom: zoomOutBy(zoom, windowSec, boundsAt(Date.now()), 2) });
  },
  backToLive: () => set({ zoom: backToLive() }),
  fitTo: (startSec, endSec) => {
    const pad = Math.max(0, endSec - startSec) * EXPERIMENT_FIT_PAD;
    set((s) => ({
      replay: null,
      zoom: setRange(s.zoom, boundsAt(Date.now()), startSec - pad, endSec + pad),
    }));
  },

  enterReplay: (session) =>
    set({
      replay: {
        ...session,
        clock: startReplay(session.startSec, session.endSec, Date.now()),
      },
    }),
  exitReplay: () =>
    set((s) => {
      if (s.replay) {
        const r = s.replay;
        const pad = Math.max(0, r.endSec - r.startSec) * EXPERIMENT_FIT_PAD;
        return {
          replay: null,
          zoom: setRange(s.zoom, boundsAt(Date.now()), r.startSec - pad, r.endSec + pad),
        };
      }
      return { replay: null };
    }),
  replayTogglePlay: () =>
    set((s) => (s.replay ? { replay: { ...s.replay, clock: replayToggle(s.replay.clock, Date.now()) } } : {})),
  replaySeekFraction: (f) =>
    set((s) => {
      if (!s.replay) return {};
      const r = s.replay;
      const target = r.startSec + f * (r.endSec - r.startSec);
      return { replay: { ...r, clock: replaySeek(r.clock, target, Date.now()) } };
    }),
  replaySetSpeed: (speed) =>
    set((s) => (s.replay ? { replay: { ...s.replay, clock: replaySetSpeed(s.replay.clock, speed, Date.now()) } } : {})),
}));

// ---- derived reads (pure given state + wall clock) -------------------------

export interface VisibleWindow {
  range: [number, number];
  clamped: boolean;
  /** Replay playhead position (epoch seconds), or undefined when not replaying. */
  cursorSec?: number;
}

/**
 * Resolve the visible window for the current store state at wall time `nowMs`.
 * Replay overrides the zoom window with one sliding under the moving playhead;
 * otherwise it comes from the zoom state machine (clamped to the buffer).
 */
export function resolveVisible(state: TimeState, nowMs: number): VisibleWindow {
  if (state.replay) {
    const cursorSec = cursorAt(state.replay.clock, nowMs);
    return {
      range: [
        cursorSec - state.windowSec * REPLAY_CURSOR_FRAC,
        cursorSec + state.windowSec * (1 - REPLAY_CURSOR_FRAC),
      ],
      clamped: false,
      cursorSec,
    };
  }
  const resolved = resolveWindow(state.zoom, state.windowSec, boundsAt(nowMs));
  return { range: resolved.range, clamped: resolved.clamped };
}

/** Replay progress fraction [0,1] at `nowMs`, or undefined when not replaying. */
export function replayProgressAt(state: TimeState, nowMs: number): number | undefined {
  return state.replay ? replayProgress(state.replay.clock, nowMs) : undefined;
}
