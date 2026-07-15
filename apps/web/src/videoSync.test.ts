import { describe, it, expect } from "vitest";
import {
  chunkAtCursor,
  chunkEndNs,
  lagSeconds,
  nextLiveChunk,
  trailingWindow,
  type VideoChunk,
} from "./videoSync";

const SEC = 1e9;
const MS = 1e6;

/** Build a chunk starting at `startSec` (epoch s) lasting `durMs` (default 2000). */
function chunk(id: string, startSec: number, durMs = 2000): VideoChunk {
  return {
    id,
    cameraId: "bench-cam",
    startNs: startSec * SEC,
    durationMs: durMs,
    mime: "video/webm",
    bytes: 1024,
    createdNs: startSec * SEC,
  };
}

// A back-to-back run of 2s chunks at t=100,102,104 (no gaps).
const runBack = [chunk("a", 100), chunk("b", 102), chunk("c", 104)];

describe("chunkAtCursor", () => {
  it("returns null for an empty chunk list", () => {
    expect(chunkAtCursor(100 * SEC, [])).toBeNull();
  });

  it("returns null when the cursor precedes the first chunk", () => {
    expect(chunkAtCursor(99 * SEC, runBack)).toBeNull();
  });

  it("finds the covering chunk and the seek offset (seconds)", () => {
    const hit = chunkAtCursor(101 * SEC, runBack);
    expect(hit).toEqual({ chunkId: "a", offsetSec: 1 });
  });

  it("start boundary is inclusive (offset 0)", () => {
    expect(chunkAtCursor(102 * SEC, runBack)).toEqual({ chunkId: "b", offsetSec: 0 });
  });

  it("end boundary rolls into the next back-to-back chunk (offset 0)", () => {
    // t=104 is the exclusive end of chunk b and the inclusive start of chunk c.
    expect(chunkAtCursor(104 * SEC, runBack)).toEqual({ chunkId: "c", offsetSec: 0 });
  });

  it("returns null in a gap between non-adjacent chunks (pruned/missing)", () => {
    // Chunks at 100-102 and 110-112; cursor at 105 is in the hole.
    const gapped = [chunk("a", 100), chunk("d", 110)];
    expect(chunkAtCursor(105 * SEC, gapped)).toBeNull();
  });

  it("returns null past the end of the last chunk", () => {
    // last chunk c ends at 106.
    expect(chunkAtCursor(107 * SEC, runBack)).toBeNull();
  });

  it("covers a mid-offset just before a chunk's end", () => {
    const hit = chunkAtCursor(103.9 * SEC, runBack); // inside chunk b (102-104)
    expect(hit?.chunkId).toBe("b");
    expect(hit?.offsetSec).toBeCloseTo(1.9, 6);
  });
});

describe("chunkEndNs", () => {
  it("is start + duration", () => {
    expect(chunkEndNs(chunk("a", 100, 2000))).toBe(102 * SEC);
  });
});

describe("nextLiveChunk", () => {
  it("returns the newest chunk when starting fresh (null cursor)", () => {
    expect(nextLiveChunk(runBack, null)?.id).toBe("c");
  });

  it("returns the newest chunk strictly newer than the last started", () => {
    // lastStarted = chunk a's start (100s); newest newer is c.
    expect(nextLiveChunk(runBack, 100 * SEC)?.id).toBe("c");
  });

  it("drops backlog: jumps to newest even when several are unseen", () => {
    const many = [chunk("a", 100), chunk("b", 102), chunk("c", 104), chunk("e", 106)];
    expect(nextLiveChunk(many, 100 * SEC)?.id).toBe("e");
  });

  it("returns null when nothing is newer than the last started", () => {
    expect(nextLiveChunk(runBack, 104 * SEC)).toBeNull();
  });

  it("returns null for an empty list", () => {
    expect(nextLiveChunk([], null)).toBeNull();
  });
});

describe("lagSeconds", () => {
  it("measures how far a chunk's end sits behind now", () => {
    const c = chunk("a", 100, 2000); // ends at 102s
    expect(lagSeconds(c, 105 * SEC)).toBeCloseTo(3, 6);
  });

  it("never goes negative (chunk end in the future)", () => {
    const c = chunk("a", 100, 2000);
    expect(lagSeconds(c, 101 * SEC)).toBe(0);
  });
});

describe("trailingWindow", () => {
  it("spans the last widthSec up to now", () => {
    const [from, to] = trailingWindow(1000 * SEC, 10);
    expect(to).toBe(1000 * SEC);
    expect(from).toBe(990 * SEC);
  });

  it("width is exactly widthSec in ns", () => {
    const [from, to] = trailingWindow(500 * SEC, 10);
    expect(to - from).toBe(10 * SEC);
  });

  it("one millisecond is 1e6 ns (sanity on the unit constants)", () => {
    expect(MS).toBe(1e6);
  });
});
