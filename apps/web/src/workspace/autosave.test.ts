import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createDebounced } from "./autosave";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("createDebounced (workspace autosave)", () => {
  it("collapses a burst of calls into one trailing invocation", () => {
    const fn = vi.fn();
    const d = createDebounced(fn, 2000);
    d.call("a");
    d.call("b");
    d.call("c");
    expect(fn).not.toHaveBeenCalled();
    expect(d.pending()).toBe(true);
    vi.advanceTimersByTime(2000);
    expect(fn).toHaveBeenCalledTimes(1);
    expect(fn).toHaveBeenCalledWith("c"); // last args win
    expect(d.pending()).toBe(false);
  });

  it("reschedules the timer on each call (does not fire early)", () => {
    const fn = vi.fn();
    const d = createDebounced(fn, 2000);
    d.call(1);
    vi.advanceTimersByTime(1500);
    d.call(2); // resets the 2s window
    vi.advanceTimersByTime(1500);
    expect(fn).not.toHaveBeenCalled();
    vi.advanceTimersByTime(500);
    expect(fn).toHaveBeenCalledTimes(1);
    expect(fn).toHaveBeenCalledWith(2);
  });

  it("flush fires the pending call immediately", () => {
    const fn = vi.fn();
    const d = createDebounced(fn, 2000);
    d.call("x");
    d.flush();
    expect(fn).toHaveBeenCalledTimes(1);
    expect(fn).toHaveBeenCalledWith("x");
    // nothing left to fire
    vi.advanceTimersByTime(2000);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("flush with nothing pending is a no-op", () => {
    const fn = vi.fn();
    const d = createDebounced(fn, 2000);
    d.flush();
    expect(fn).not.toHaveBeenCalled();
  });

  it("cancel drops the pending call", () => {
    const fn = vi.fn();
    const d = createDebounced(fn, 2000);
    d.call("y");
    d.cancel();
    vi.advanceTimersByTime(5000);
    expect(fn).not.toHaveBeenCalled();
    expect(d.pending()).toBe(false);
  });
});
