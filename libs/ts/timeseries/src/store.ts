import { RingSeries, type WindowResult } from "./ringSeries";

/**
 * A collection of per-channel {@link RingSeries}, keyed by canonical channel
 * name. This is the store the live dashboard appends streamed frames into and
 * queries windows out of.
 */
export class TimeSeriesStore {
  private readonly series = new Map<string, RingSeries>();
  /** Default per-channel ring capacity (samples). */
  readonly defaultCapacity: number;

  constructor(defaultCapacity = 60_000) {
    if (!Number.isInteger(defaultCapacity) || defaultCapacity <= 0) {
      throw new RangeError("defaultCapacity must be a positive integer");
    }
    this.defaultCapacity = defaultCapacity;
  }

  /** Get the ring for `channel`, creating it (at the given capacity) if absent. */
  ensure(channel: string, capacity = this.defaultCapacity): RingSeries {
    let s = this.series.get(channel);
    if (!s) {
      s = new RingSeries(capacity);
      this.series.set(channel, s);
    }
    return s;
  }

  get(channel: string): RingSeries | undefined {
    return this.series.get(channel);
  }

  has(channel: string): boolean {
    return this.series.has(channel);
  }

  /** Append a sample to `channel` (creating its ring on demand). */
  append(channel: string, tNs: bigint, value: number): boolean {
    return this.ensure(channel).append(tNs, value);
  }

  /** Window query for a single channel; empty result if the channel is unknown. */
  window(channel: string, t1?: bigint, t2?: bigint): WindowResult {
    const s = this.series.get(channel);
    if (!s) return { x: new Float64Array(0), y: new Float64Array(0) };
    return s.window(t1, t2);
  }

  channels(): string[] {
    return [...this.series.keys()];
  }

  /** Sum of late-dropped samples across all channels. */
  totalDroppedLate(): number {
    let n = 0;
    for (const s of this.series.values()) n += s.droppedLate;
    return n;
  }

  /** Total accepted samples across all channels. */
  totalAccepted(): number {
    let n = 0;
    for (const s of this.series.values()) n += s.accepted;
    return n;
  }

  /** Remove a single channel's ring, or clear everything if no name given. */
  remove(channel?: string): void {
    if (channel === undefined) this.series.clear();
    else this.series.delete(channel);
  }
}
