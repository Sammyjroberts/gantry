/**
 * Playback clock — pure state machine for experiment replay.
 *
 * Replay sweeps a cursor from an experiment's start to its end at 1x/4x/16x,
 * with pause and free scrubbing. The clock is kept here, free of React/timers,
 * so the position math (speed, pause, seek, end-clamp) is unit tested directly
 * (see playback.test.ts). The component samples {@link cursorAt} on each render
 * tick to place the vertical "now" line and the sliding window.
 *
 * All times are epoch SECONDS; wall time is millis (from `Date.now()`), so a
 * position is `anchorCursor + (nowMs - anchorWallMs)/1000 * speed`, clamped to
 * `[start, end]`. When paused, the position is frozen in `cursorSec`.
 */

export interface PlaybackState {
  /** Experiment bounds, epoch seconds. */
  startSec: number;
  endSec: number;
  /** Playback rate multiplier (1, 4, 16). */
  speed: number;
  playing: boolean;
  /** Cursor position captured at `anchorWallMs` (epoch seconds). */
  cursorSec: number;
  /** Wall clock (ms) at which `cursorSec` was captured. */
  anchorWallMs: number;
}

/** Selectable playback speeds. */
export const SPEEDS = [1, 4, 16] as const;

function clamp(v: number, lo: number, hi: number): number {
  return v < lo ? lo : v > hi ? hi : v;
}

/**
 * Begin replay of `[startSec, endSec]`, playing from the start. A zero/negative
 * span is tolerated (cursor pinned at start).
 */
export function startReplay(startSec: number, endSec: number, nowMs: number): PlaybackState {
  const end = Math.max(startSec, endSec);
  return {
    startSec,
    endSec: end,
    speed: 1,
    playing: true,
    cursorSec: startSec,
    anchorWallMs: nowMs,
  };
}

/**
 * The cursor position (epoch seconds) at wall time `nowMs`. While playing it
 * advances from the anchor at `speed`, clamped to `[start, end]`; while paused
 * it is the frozen `cursorSec`.
 */
export function cursorAt(state: PlaybackState, nowMs: number): number {
  if (!state.playing) return clamp(state.cursorSec, state.startSec, state.endSec);
  const elapsedSec = ((nowMs - state.anchorWallMs) / 1000) * state.speed;
  return clamp(state.cursorSec + elapsedSec, state.startSec, state.endSec);
}

/** True once the cursor has reached the experiment end. */
export function isFinished(state: PlaybackState, nowMs: number): boolean {
  return cursorAt(state, nowMs) >= state.endSec;
}

/** Freeze playback at the current cursor. Idempotent. */
export function pause(state: PlaybackState, nowMs: number): PlaybackState {
  if (!state.playing) return state;
  return { ...state, playing: false, cursorSec: cursorAt(state, nowMs), anchorWallMs: nowMs };
}

/**
 * Resume playback, re-anchoring at the current cursor. If the cursor is already
 * at (or past) the end, restart from the beginning so ▶ always plays something.
 */
export function play(state: PlaybackState, nowMs: number): PlaybackState {
  const at = cursorAt(state, nowMs);
  const cursor = at >= state.endSec ? state.startSec : at;
  return { ...state, playing: true, cursorSec: cursor, anchorWallMs: nowMs };
}

/** Toggle play/pause. */
export function togglePlay(state: PlaybackState, nowMs: number): PlaybackState {
  return state.playing ? pause(state, nowMs) : play(state, nowMs);
}

/** Seek the cursor to `sec` (clamped), preserving play/pause; re-anchors. */
export function seek(state: PlaybackState, sec: number, nowMs: number): PlaybackState {
  return {
    ...state,
    cursorSec: clamp(sec, state.startSec, state.endSec),
    anchorWallMs: nowMs,
  };
}

/**
 * Change speed without jumping the cursor: capture the current position as the
 * new anchor, then apply the new rate.
 */
export function setSpeed(state: PlaybackState, speed: number, nowMs: number): PlaybackState {
  return { ...state, speed, cursorSec: cursorAt(state, nowMs), anchorWallMs: nowMs };
}

/** Fraction [0,1] of the experiment the cursor has swept (for the scrub slider). */
export function progress(state: PlaybackState, nowMs: number): number {
  const span = state.endSec - state.startSec;
  if (span <= 0) return 1;
  return clamp((cursorAt(state, nowMs) - state.startSec) / span, 0, 1);
}
