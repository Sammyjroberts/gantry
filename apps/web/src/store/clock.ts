/**
 * A shared 150ms render clock.
 *
 * The old App forced a whole-tree re-render every 150ms to advance the live
 * window and refresh readouts. In the panel world each panel that shows a
 * value-at-time (value/led/state readouts, and each chart's per-tick reslice)
 * subscribes to this clock instead, so the tree re-renders per panel at the
 * same proven cadence — never per telemetry frame (the hot-path rule).
 *
 * Charts still update their canvas imperatively via rAF inside Chart; the clock
 * only drives the React-side reslice of the visible window, which is cheap.
 */

import { useEffect } from "react";
import { create } from "zustand";

interface ClockState {
  nowMs: number;
  tick: () => void;
}

export const useClockStore = create<ClockState>((set) => ({
  nowMs: Date.now(),
  tick: () => set({ nowMs: Date.now() }),
}));

/** Subscribe to the current wall-clock tick (epoch ms). */
export function useClock(): number {
  return useClockStore((s) => s.nowMs);
}

/**
 * Drive the clock at 150ms. Mounted once at the app shell. `active` gates it:
 * when the live view is paused and no replay is playing the clock freezes so
 * the view holds still (parity with the old paused-tick behaviour).
 */
export function useClockDriver(active: boolean): void {
  useEffect(() => {
    if (!active) return;
    const tick = useClockStore.getState().tick;
    tick();
    const id = setInterval(tick, 150);
    return () => clearInterval(id);
  }, [active]);
}
