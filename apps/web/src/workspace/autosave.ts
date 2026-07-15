/**
 * A trailing debounce used for workspace autosave.
 *
 * Grid edits (drag/resize/config) fire rapidly; we coalesce them into a single
 * Upsert ~2s after the last edit. Kept as a tiny framework-free primitive so
 * the collapse/flush/cancel semantics are unit tested with fake timers
 * (autosave.test.ts) rather than asserted through React.
 */
export interface Debounced<A extends unknown[]> {
  /** Schedule (or reschedule) a trailing call with the given args. */
  call: (...args: A) => void;
  /** Fire the pending call now, if any. */
  flush: () => void;
  /** Drop the pending call without firing. */
  cancel: () => void;
  /** True while a call is scheduled. */
  pending: () => boolean;
}

export function createDebounced<A extends unknown[]>(
  fn: (...args: A) => void,
  delayMs: number,
): Debounced<A> {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let lastArgs: A | null = null;

  const clear = () => {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  };

  return {
    call: (...args: A) => {
      lastArgs = args;
      clear();
      timer = setTimeout(() => {
        timer = null;
        const a = lastArgs;
        lastArgs = null;
        if (a) fn(...a);
      }, delayMs);
    },
    flush: () => {
      if (timer !== null) {
        clear();
        const a = lastArgs;
        lastArgs = null;
        if (a) fn(...a);
      }
    },
    cancel: () => {
      clear();
      lastArgs = null;
    },
    pending: () => timer !== null,
  };
}

/** Default autosave debounce, milliseconds. */
export const AUTOSAVE_DELAY_MS = 2000;
