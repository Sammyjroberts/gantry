// Package query is Gantry's shared bounded-window read engine: it drains a
// snapshot of the telemetry stream over an absolute [start, end] time range via
// JetStream replay and reduces it to per-series raw points or downsampled
// min/max/mean buckets. It is transport-agnostic — it takes a narrow Replayer
// interface (satisfied by *stream.Bus) plus a caller-supplied stream high-water
// mark and first-retained timestamp — so both the MCP tool surface
// (core/go/mcp) and the ConnectRPC QueryService (apps/bench) build on the same
// collector. Bench wires it to the embedded JetStream today; Cloud will wire
// the same engine to clustered NATS + the segment store later.
package query

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Sammyjroberts/gantry/core/go/stream"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// Drain tuning. Replay from an in-process JetStream is effectively instant, so
// these bound how long a collection waits for a replay to complete, not how much
// data it can return.
const (
	// drainFirstFrame is how long to wait for the FIRST replayed frame before
	// concluding the window is genuinely empty. Consumer creation + replay
	// startup can exceed the between-frame idle gap under ingest load (seen at
	// 500Hz: a short gap here returned empty results nondeterministically), so
	// the empty-window verdict gets its own generous deadline.
	drainFirstFrame = 3 * time.Second
	// drainIdle ends a drain once frames HAVE been flowing and none has arrived
	// for this long: the replay backlog is considered flushed. Needed because a
	// filtered subscription may never observe the stream's global last sequence
	// (that message can be on a subject we did not select), so a high-water mark
	// alone cannot terminate. Kept well above scheduler/GC hiccups — a 400ms gap
	// once truncated a 1200-frame replay mid-backlog under CPU load. Active
	// channels still terminate promptly via the high-water check; idle only
	// decides quiet-channel queries, where an extra second is acceptable.
	drainIdle = 1500 * time.Millisecond
	// drainCap is the absolute ceiling on a single collection, a safety valve
	// against a firehose device keeping the drain alive with live frames.
	drainCap = 10 * time.Second
	// defaultMaxSamples caps total points buffered across all series in one
	// collection, bounding memory on a very wide/dense window before downsampling.
	defaultMaxSamples = 400_000
	// endStopSlopNs: once a drained frame's timestamp exceeds EndNs by this
	// margin, the drain stops. Stream delivery is arrival-ordered, so anything
	// still owed for the range would have arrived by then; without this, a query
	// whose EndNs lies in the past drains every frame between EndNs and now just
	// to discard it (measured: ~2s per query against a live 300fps stream). The
	// slop absorbs cross-subject interleave and modest emitter clock skew; a
	// device lagging more than this behind its peers in one multi-device query
	// may lose its tail — an accepted trade at bench scale.
	endStopSlopNs = int64(2 * time.Second)
)

// Replayer opens a replay-then-live subscription over the telemetry backbone.
// Satisfied directly by *stream.Bus. Collect only ever uses the replay window
// and drains to a high-water mark, so the "live" tail is never consumed.
type Replayer interface {
	Subscribe(ctx context.Context, opts stream.SubscribeOptions) (<-chan stream.Delivered, error)
}

// SeriesKey identifies a series within a collection by (Device, Packet,
// Channel) — the full telemetry identity. This is more precise than addressing a
// channel by name alone: the same channel name may appear under different
// packets, and the query engine keeps them distinct.
type SeriesKey struct {
	Device  string
	Packet  string
	Channel string
}

// Sample is one decoded frame reduced to what readers need.
type Sample struct {
	TNs    int64
	Packet string
	// Num holds the numeric value when Numeric is true (f64/i64 as float, bool
	// as 0/1). For text/raw channels Numeric is false and Text carries a display
	// string.
	Num     float64
	Numeric bool
	Text    string
	Kind    gantryv1.ValueKind
}

// Options configures a Collect call.
type Options struct {
	// DeviceID filters to one device; "" spans all devices.
	DeviceID string
	// Channels filters to these channel names; empty spans all channels.
	Channels []string
	// StartNs and EndNs bound the collection by absolute Unix-nanosecond frame
	// timestamp. Frames with ts < StartNs are dropped; frames with ts > EndNs are
	// dropped when EndNs > 0. EndNs <= 0 means "no upper bound" (used by the
	// last-N-seconds callers, which rely on the high-water snapshot to stop).
	StartNs int64
	EndNs   int64
	// HighWater is the stream's last sequence, captured at call time as a
	// snapshot ceiling: frames beyond it (published after the call began) are
	// excluded, and reaching it terminates the drain. HasHighWater must be true
	// for the mark to take effect.
	HighWater    uint64
	HasHighWater bool
	// FirstTsNs is the stream's first retained frame timestamp (from stream
	// state). When > 0 and StartNs predates it, the collection is flagged
	// TruncatedByRetention: the requested range reaches before the retention
	// horizon and that older data is absent.
	FirstTsNs int64
	// MaxSamples caps total buffered samples; 0 uses the package default.
	MaxSamples int
}

// Collection is the result of draining a replay window.
type Collection struct {
	// Series maps each (device, packet, channel) to its in-range samples, in
	// arrival order (use SortedByTime for time order).
	Series map[SeriesKey][]Sample
	// Total is the number of in-range samples buffered across all series.
	Total int
	// Truncated is set if MaxSamples stopped collection early.
	Truncated bool
	// TruncatedByRetention is set if StartNs predates the stream's first retained
	// timestamp (data before the retention horizon is absent, not an error).
	TruncatedByRetention bool
}

// Collect opens a replay subscription over [StartNs, EndNs] and drains the
// backlog into per-series samples. Replay starts from StartNs (converted to the
// stream's relative "seconds ago" window, with 1s of slop so boundary frames are
// not lost to rounding); frames are then filtered exactly by absolute timestamp.
// Termination is the earliest of: reaching the high-water sequence (when
// HasHighWater), an idle gap after frames have flowed, the absolute cap, the
// MaxSamples cap, the subscription closing, or ctx cancellation.
func Collect(ctx context.Context, rep Replayer, opts Options) (*Collection, error) {
	c := &Collection{Series: make(map[SeriesKey][]Sample)}
	if opts.FirstTsNs > 0 && opts.StartNs < opts.FirstTsNs {
		c.TruncatedByRetention = true
	}
	if opts.HasHighWater && opts.HighWater == 0 {
		return c, nil // empty stream: nothing to replay
	}
	maxSamples := opts.MaxSamples
	if maxSamples <= 0 {
		maxSamples = defaultMaxSamples
	}

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch, err := rep.Subscribe(subCtx, stream.SubscribeOptions{
		DeviceID:      opts.DeviceID,
		Channels:      opts.Channels,
		ReplaySeconds: replaySecondsFor(opts.StartNs),
	})
	if err != nil {
		return nil, fmt.Errorf("query subscribe: %w", err)
	}

	// One timer, two phases: until the first frame arrives it runs on the long
	// first-frame deadline; after that, each frame re-arms it with the shorter
	// between-frame idle gap.
	idle := time.NewTimer(drainFirstFrame)
	defer idle.Stop()
	capTimer := time.NewTimer(drainCap)
	defer capTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return c, ctx.Err()
		case <-capTimer.C:
			return c, nil
		case <-idle.C:
			return c, nil
		case d, ok := <-ch:
			if !ok {
				return c, nil
			}
			if opts.HasHighWater && d.StreamSeq > opts.HighWater {
				// First frame beyond the snapshot: everything up to and including
				// the high-water message has been drained. Stop before including
				// post-call frames. A single JetStream message expands to many
				// frames that all share one StreamSeq, so the high-water message is
				// only fully drained once we observe a STRICTLY greater sequence
				// (stopping at >= would truncate a multi-frame final message to its
				// first frame). When no later frame ever arrives — the queried
				// subjects hold the last write and ingest is quiet — the idle timer
				// terminates instead.
				return c, nil
			}
			c.add(d, opts.StartNs, opts.EndNs)
			if opts.EndNs > 0 && d.Frame != nil && int64(d.Frame.TimestampNs) > opts.EndNs+endStopSlopNs {
				return c, nil // arrival-ordered stream is past the range: done
			}
			if c.Total >= maxSamples {
				c.Truncated = true
				return c, nil
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(drainIdle)
		}
	}
}

// add appends d's frame to its series if it falls inside [startNs, endNs]. An
// endNs <= 0 means no upper bound. Frames outside the range are still treated as
// drain activity by the caller (they re-arm the idle timer) but are not buffered.
func (c *Collection) add(d stream.Delivered, startNs, endNs int64) {
	f := d.Frame
	if f == nil || f.Channel == "" {
		return
	}
	ts := int64(f.TimestampNs)
	if ts < startNs {
		return
	}
	if endNs > 0 && ts > endNs {
		return
	}
	key := SeriesKey{Device: d.DeviceID, Packet: f.Packet, Channel: f.Channel}
	s := Sample{TNs: ts, Packet: f.Packet, Kind: ValueKind(f.Value)}
	if n, ok := NumericValue(f.Value); ok {
		s.Num = n
		s.Numeric = true
	} else {
		s.Text = TextValue(f.Value)
	}
	c.Series[key] = append(c.Series[key], s)
	c.Total++
}

// SortedByTime returns the samples for a key in ascending timestamp order.
// Ordered consumers already deliver in stream order, but replayed cross-channel
// interleaving plus emitter-stamped timestamps mean per-series time order is not
// guaranteed; sort to be safe. The sort is in place on the stored slice.
func (c *Collection) SortedByTime(key SeriesKey) []Sample {
	s := c.Series[key]
	sort.SliceStable(s, func(i, j int) bool { return s[i].TNs < s[j].TNs })
	return s
}

// SortedKeys returns the collection's series keys in deterministic
// (device, packet, channel) order.
func (c *Collection) SortedKeys() []SeriesKey {
	keys := make([]SeriesKey, 0, len(c.Series))
	for k := range c.Series {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.Device != b.Device {
			return a.Device < b.Device
		}
		if a.Packet != b.Packet {
			return a.Packet < b.Packet
		}
		return a.Channel < b.Channel
	})
	return keys
}

// replaySecondsFor converts an absolute start into the stream's relative
// "seconds ago" replay window. A start at or after now yields 0 (live-only,
// nothing in range). +1s of slop ensures a boundary frame at exactly startNs is
// not missed to sub-second rounding; the per-frame timestamp filter in add is
// exact. A start older than the first retained message is clamped by JetStream
// to that first message, which is the out-of-retention behavior we want.
func replaySecondsFor(startNs int64) uint32 {
	nowNs := time.Now().UnixNano()
	if startNs >= nowNs {
		return 0
	}
	secs := (nowNs-startNs)/int64(time.Second) + 1
	return clampUint32(secs)
}

func clampUint32(v int64) uint32 {
	if v < 0 {
		return 0
	}
	const max = int64(^uint32(0))
	if v > max {
		return ^uint32(0)
	}
	return uint32(v)
}
