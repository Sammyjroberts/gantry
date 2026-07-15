import { describe, it, expect, vi } from "vitest";
import { UploadQueue, type PendingUpload } from "./uploadQueue";

/** A tiny item factory (the blob is opaque to the queue). */
function item(startSec: number): Omit<PendingUpload, "attempts"> {
  return {
    cameraId: "bench-cam",
    startNs: startSec * 1e9,
    durationMs: 2000,
    blob: new Blob([String(startSec)], { type: "video/webm" }),
  };
}

describe("UploadQueue happy path", () => {
  it("uploads enqueued items in order and counts them sent", async () => {
    const seen: number[] = [];
    const q = new UploadQueue({
      upload: async (it) => {
        seen.push(it.startNs / 1e9);
      },
    });
    q.enqueue(item(1));
    q.enqueue(item(2));
    q.enqueue(item(3));
    await q.whenIdle();
    expect(seen).toEqual([1, 2, 3]);
    expect(q.stats()).toMatchObject({ sent: 3, failed: 0, dropped: 0, queued: 0 });
  });

  it("emits stats via onStats as it drains", async () => {
    const sentCounts: number[] = [];
    const q = new UploadQueue({
      upload: async () => undefined,
      onStats: (s) => sentCounts.push(s.sent),
    });
    q.enqueue(item(1));
    await q.whenIdle();
    expect(sentCounts.at(-1)).toBe(1);
  });
});

describe("UploadQueue retry", () => {
  it("retries a transient failure and then succeeds (single sent)", async () => {
    const upload = vi
      .fn<(it: PendingUpload) => Promise<void>>()
      .mockRejectedValueOnce(new Error("blip"))
      .mockResolvedValueOnce(undefined);
    const q = new UploadQueue({ upload, maxAttempts: 3 });
    q.enqueue(item(1));
    await q.whenIdle();
    expect(upload).toHaveBeenCalledTimes(2);
    expect(q.stats()).toMatchObject({ sent: 1, failed: 0 });
  });

  it("gives up after maxAttempts and counts the item failed", async () => {
    const upload = vi
      .fn<(it: PendingUpload) => Promise<void>>()
      .mockRejectedValue(new Error("down"));
    const q = new UploadQueue({ upload, maxAttempts: 3 });
    q.enqueue(item(1));
    await q.whenIdle();
    expect(upload).toHaveBeenCalledTimes(3);
    expect(q.stats()).toMatchObject({ sent: 0, failed: 1, queued: 0 });
  });

  it("keeps processing later items after one fails out", async () => {
    const upload = vi.fn<(it: PendingUpload) => Promise<void>>(async (it) => {
      if (it.startNs / 1e9 === 1) throw new Error("bad");
    });
    const q = new UploadQueue({ upload, maxAttempts: 2 });
    q.enqueue(item(1));
    q.enqueue(item(2));
    await q.whenIdle();
    expect(q.stats()).toMatchObject({ sent: 1, failed: 1, queued: 0 });
  });
});

describe("UploadQueue drop-oldest", () => {
  it("drops the oldest un-tried item when the buffer is full", async () => {
    // Block uploads so the queue fills past the cap while the pump waits.
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const seen: number[] = [];
    const upload = async (it: PendingUpload): Promise<void> => {
      await gate;
      seen.push(it.startNs / 1e9);
    };
    const q = new UploadQueue({ upload, maxQueue: 3 });

    // Enqueue 6 with a cap of 3. Item 1 is pulled in-flight (off the queue,
    // awaiting the gate); items 2-6 contend for the 3 buffer slots, so the two
    // oldest still-queued (2 and 3) are dropped, leaving [4,5,6].
    for (let i = 1; i <= 6; i++) q.enqueue(item(i));
    expect(q.stats().dropped).toBe(2);
    expect(q.stats().queued).toBeLessThanOrEqual(3 + 1); // +1 for the in-flight item

    release();
    await q.whenIdle();
    // The dropped oldest never reached the uploader; the in-flight one and the
    // surviving newest did.
    expect(seen).not.toContain(2);
    expect(seen).not.toContain(3);
    expect(seen).toContain(1);
    expect(seen).toContain(6);
    expect(q.stats()).toMatchObject({ sent: 4, dropped: 2, failed: 0, queued: 0 });
  });
});

describe("UploadQueue whenIdle", () => {
  it("resolves immediately when nothing is queued", async () => {
    const q = new UploadQueue({ upload: async () => undefined });
    await expect(q.whenIdle()).resolves.toBeUndefined();
  });
});
