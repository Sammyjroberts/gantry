/**
 * Result of a window query, laid out as two aligned typed arrays ready to hand
 * straight to uPlot as `[x, y]`. `x` is epoch **seconds** (float; uPlot's native
 * x unit), `y` is the raw channel value.
 */
export interface WindowResult {
  /** X axis: epoch seconds (nanoseconds / 1e9). */
  x: Float64Array;
  /** Y axis: channel values, aligned 1:1 with `x`. */
  y: Float64Array;
}

/** A single stored sample. */
export interface Sample {
  tNs: bigint;
  value: number;
}

/**
 * Fixed-capacity per-channel ring buffer of `(t_ns, value)` samples for
 * streaming charts.
 *
 * - Timestamps are stored as `bigint` nanoseconds (a `BigInt64Array`) so full
 *   epoch-nanosecond precision is preserved; window extraction converts to
 *   `Float64` seconds for uPlot.
 * - Append is O(1) amortised and wraps in place once `capacity` is reached
 *   (oldest sample overwritten).
 * - **Out-of-order policy (v1):** samples whose timestamp is strictly older than
 *   the last accepted timestamp are *dropped* and counted in `droppedLate`. This
 *   keeps the buffer time-sorted, which makes window queries a binary search.
 *   (Insert-sorted is a possible future policy; dropping late frames with a
 *   counter is the documented v1 behaviour.)
 */
export class RingSeries {
  readonly capacity: number;
  private readonly tNs: BigInt64Array;
  private readonly val: Float64Array;
  /** Physical index of logical sample 0 (the oldest). */
  private startIdx = 0;
  /** Number of live samples currently held (<= capacity). */
  private len = 0;
  /** Timestamp of the most recently accepted sample, for the drop-late check. */
  private lastNs: bigint | null = null;

  /** Count of samples rejected as out-of-order (late). */
  droppedLate = 0;
  /** Count of samples accepted over the lifetime of this ring. */
  accepted = 0;

  constructor(capacity: number) {
    if (!Number.isInteger(capacity) || capacity <= 0) {
      throw new RangeError(`capacity must be a positive integer, got ${capacity}`);
    }
    this.capacity = capacity;
    this.tNs = new BigInt64Array(capacity);
    this.val = new Float64Array(capacity);
  }

  /** Number of samples currently held. */
  get size(): number {
    return this.len;
  }

  /** True once the ring has filled and is overwriting oldest samples. */
  get full(): boolean {
    return this.len === this.capacity;
  }

  /**
   * Append one sample. Returns `true` if accepted, `false` if dropped as late.
   */
  append(tNs: bigint, value: number): boolean {
    if (this.lastNs !== null && tNs < this.lastNs) {
      this.droppedLate++;
      return false;
    }
    const writeAt = (this.startIdx + this.len) % this.capacity;
    if (this.len < this.capacity) {
      this.len++;
    } else {
      // Full: writeAt === startIdx (oldest slot); advance start after overwrite.
      this.startIdx = (this.startIdx + 1) % this.capacity;
    }
    this.tNs[writeAt] = tNs;
    this.val[writeAt] = value;
    this.lastNs = tNs;
    this.accepted++;
    return true;
  }

  /**
   * Append a batch of samples in order. Returns the number accepted (batch
   * length minus any dropped as late).
   */
  appendBatch(samples: Iterable<Sample>): number {
    let n = 0;
    for (const s of samples) {
      if (this.append(s.tNs, s.value)) n++;
    }
    return n;
  }

  /** Timestamp (ns) of the logical sample at index `i` (0 = oldest). */
  private tAt(i: number): bigint {
    return this.tNs[(this.startIdx + i) % this.capacity]!;
  }

  /** First logical index whose timestamp is >= target. */
  private lowerBound(target: bigint): number {
    let lo = 0;
    let hi = this.len;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      if (this.tAt(mid) < target) lo = mid + 1;
      else hi = mid;
    }
    return lo;
  }

  /** First logical index whose timestamp is > target. */
  private upperBound(target: bigint): number {
    let lo = 0;
    let hi = this.len;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      if (this.tAt(mid) <= target) lo = mid + 1;
      else hi = mid;
    }
    return lo;
  }

  /**
   * Extract all samples with `t1 <= t <= t2` (inclusive) as aligned typed
   * arrays in epoch seconds. If `t1`/`t2` are omitted the full buffer is
   * returned. Runs in O(log n + m) for m returned points.
   */
  window(t1?: bigint, t2?: bigint): WindowResult {
    const i0 = t1 === undefined ? 0 : this.lowerBound(t1);
    const i1 = t2 === undefined ? this.len : this.upperBound(t2);
    const n = Math.max(0, i1 - i0);
    const x = new Float64Array(n);
    const y = new Float64Array(n);
    for (let k = 0; k < n; k++) {
      const phys = (this.startIdx + i0 + k) % this.capacity;
      x[k] = Number(this.tNs[phys]!) / 1e9;
      y[k] = this.val[phys]!;
    }
    return { x, y };
  }

  /** The most recently accepted sample, or null if empty. */
  latest(): Sample | null {
    if (this.len === 0) return null;
    const phys = (this.startIdx + this.len - 1) % this.capacity;
    return { tNs: this.tNs[phys]!, value: this.val[phys]! };
  }

  /** The oldest retained sample, or null if empty. */
  oldest(): Sample | null {
    if (this.len === 0) return null;
    return { tNs: this.tNs[this.startIdx]!, value: this.val[this.startIdx]! };
  }

  /** Drop all samples (keeps allocated capacity). */
  clear(): void {
    this.startIdx = 0;
    this.len = 0;
    this.lastNs = null;
  }
}
