/**
 * Upload queue — bounded, retrying, drop-oldest buffer for capture chunks.
 *
 * Capture produces a self-contained ~2s WebM chunk a few times per minute; each
 * is uploaded fire-and-forget. Uploads can fail transiently (Bench restart, brief
 * network blip), so this queue retries a few times, but it must never grow
 * without bound while a camera keeps recording — so beyond `maxQueue` buffered
 * items the OLDEST is dropped (stale video is worth less than fresh video, and
 * live-follow/replay tolerate gaps).
 *
 * The transport is injected as an `UploadFn`, so the whole queue — enqueue,
 * sequential pump, retry, drop-oldest, and the running stats — is unit tested
 * with a mocked uploader and no real fetch (see uploadQueue.test.ts). The React
 * hook wires the real videoApi.uploadChunk in (see useVideoCapture.ts).
 */

/** A pending chunk upload. `attempts` counts tries already made (starts at 0). */
export interface PendingUpload {
  cameraId: string;
  startNs: number;
  durationMs: number;
  blob: Blob;
  attempts: number;
}

/** Running counters surfaced to the capture UI. */
export interface UploadStats {
  /** Chunks uploaded successfully. */
  sent: number;
  /** Chunks abandoned after exhausting retries. */
  failed: number;
  /** Chunks dropped un-tried because the buffer was full (drop-oldest). */
  dropped: number;
  /** Items currently buffered (queued + the one in flight). */
  queued: number;
}

export type UploadFn = (item: PendingUpload) => Promise<void>;

export interface UploadQueueOptions {
  upload: UploadFn;
  /** Max buffered items before drop-oldest kicks in (default 30). */
  maxQueue?: number;
  /** Total attempts per item before it is counted failed (default 3). */
  maxAttempts?: number;
  /** Notified after every stats change, for a live UI readout. */
  onStats?: (stats: UploadStats) => void;
}

export class UploadQueue {
  private readonly upload: UploadFn;
  private readonly maxQueue: number;
  private readonly maxAttempts: number;
  private readonly onStats?: (stats: UploadStats) => void;

  private readonly queue: PendingUpload[] = [];
  private pumping = false;
  private inFlight = false;
  private sent = 0;
  private failed = 0;
  private dropped = 0;
  // Resolvers waiting on whenIdle(), fired when the queue drains.
  private idleWaiters: Array<() => void> = [];

  constructor(opts: UploadQueueOptions) {
    this.upload = opts.upload;
    this.maxQueue = Math.max(1, opts.maxQueue ?? 30);
    this.maxAttempts = Math.max(1, opts.maxAttempts ?? 3);
    this.onStats = opts.onStats;
  }

  /**
   * Buffer a chunk for upload. If the buffer is already at capacity the oldest
   * still-queued item is dropped to make room (counted in `dropped`). Kicks the
   * pump if idle.
   */
  enqueue(item: Omit<PendingUpload, "attempts">): void {
    if (this.queue.length >= this.maxQueue) {
      this.queue.shift();
      this.dropped++;
    }
    this.queue.push({ ...item, attempts: 0 });
    this.emit();
    void this.pump();
  }

  /** A snapshot of the running counters (`queued` counts the in-flight item). */
  stats(): UploadStats {
    return {
      sent: this.sent,
      failed: this.failed,
      dropped: this.dropped,
      queued: this.queue.length + (this.inFlight ? 1 : 0),
    };
  }

  /** Resolves once the queue has fully drained (immediately if already idle). */
  whenIdle(): Promise<void> {
    if (this.queue.length === 0 && !this.pumping) return Promise.resolve();
    return new Promise((resolve) => this.idleWaiters.push(resolve));
  }

  private emit(): void {
    this.onStats?.(this.stats());
  }

  /**
   * Drain the queue one item at a time. The in-flight item is taken OFF the
   * queue while uploading (so a concurrent drop-oldest can never evict it); on a
   * retryable failure it is put back at the front, and abandoned as failed once
   * it has used `maxAttempts`. Runs at most one pump loop concurrently, so
   * uploads never race.
   */
  private async pump(): Promise<void> {
    if (this.pumping) return;
    this.pumping = true;
    try {
      while (this.queue.length > 0) {
        const item = this.queue.shift()!;
        this.inFlight = true;
        item.attempts++;
        try {
          await this.upload(item);
          this.sent++;
        } catch {
          if (item.attempts >= this.maxAttempts) {
            this.failed++;
          } else {
            this.queue.unshift(item); // retry: back to the front
          }
        } finally {
          this.inFlight = false;
        }
        this.emit();
      }
    } finally {
      this.pumping = false;
      this.emit();
      const waiters = this.idleWaiters;
      this.idleWaiters = [];
      for (const w of waiters) w();
    }
  }
}
