package experiments

import (
	"context"
	"fmt"
	"time"

	"github.com/Sammyjroberts/gantry/libs/go/stream"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Replay drain tuning (mirrors libs/go/mcp/collect.go). Replay from an in-process
// JetStream is effectively instant, so these bound how long a bounded replay
// waits, not how much it can return.
const (
	// replayIdle ends a drain once no frame has arrived for this long: the replay
	// backlog is considered flushed. A filtered subscription may never observe the
	// stream's global last sequence (that message can be on a subject we did not
	// select), so a high-water mark alone cannot terminate the drain.
	replayIdle = 400 * time.Millisecond
	// replayCap is the absolute ceiling on one bounded replay, a safety valve
	// against a firehose device keeping the drain alive with live frames beyond
	// the requested window.
	replayCap = 10 * time.Second
)

// ReplayBus is the read-only slice of stream.Bus the export path needs: a
// subscribe entry point plus the NATS connection used to read the stream's
// high-water sequence. *stream.Bus satisfies it. Depending on this interface
// (rather than the concrete Bus) keeps the replay path testable and makes the
// read-only nature explicit.
type ReplayBus interface {
	Subscribe(ctx context.Context, opts stream.SubscribeOptions) (<-chan stream.Delivered, error)
	Conn() *nats.Conn
}

// Replayer performs bounded historical reads of the telemetry stream for an
// experiment's [start, end] window. It is a thin wrapper over stream.Bus.Subscribe:
// the stream package is not modified; the "cut off at the window" logic lives here.
type Replayer struct {
	bus ReplayBus
	js  jetstream.JetStream // lazily derived from bus.Conn()
}

// NewReplayer wraps a bus for bounded replay.
func NewReplayer(bus ReplayBus) *Replayer { return &Replayer{bus: bus} }

// jetStream lazily builds a JetStream context from the bus's NATS connection so
// we can read the TLM stream's last sequence. Mirrors mcp.BusStreamStater —
// stream.Bus intentionally does not expose its own jetstream handle.
func (r *Replayer) jetStream() (jetstream.JetStream, error) {
	if r.js != nil {
		return r.js, nil
	}
	js, err := jetstream.New(r.bus.Conn())
	if err != nil {
		return nil, fmt.Errorf("experiments: jetstream from bus conn: %w", err)
	}
	r.js = js
	return r.js, nil
}

// highWater returns the TLM stream's current last sequence — the snapshot ceiling
// so a replay never bleeds into frames published after the export began.
func (r *Replayer) highWater(ctx context.Context) (uint64, error) {
	js, err := r.jetStream()
	if err != nil {
		return 0, err
	}
	st, err := js.Stream(ctx, stream.StreamName)
	if err != nil {
		return 0, fmt.Errorf("experiments: lookup stream: %w", err)
	}
	info, err := st.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("experiments: stream info: %w", err)
	}
	return info.State.LastSeq, nil
}

// Replay streams every frame in [startNs, endNs] for the given device/channel
// filter to visit, in stream (replay) order. deviceID == "" spans all devices;
// channels empty spans all channels. Frames outside the window are skipped;
// frames published after the call began (beyond the high-water snapshot) are
// excluded. visit is called synchronously; returning an error aborts the replay
// (e.g. the HTTP client disconnected). A window with no data yields zero calls —
// the caller writes a header-only CSV, the documented v1 out-of-retention case.
func (r *Replayer) Replay(ctx context.Context, startNs, endNs int64, deviceID string, channels []string, visit func(stream.Delivered) error) error {
	highWater, err := r.highWater(ctx)
	if err != nil {
		return err
	}
	if highWater == 0 {
		return nil // empty stream: nothing to replay
	}

	// Replay from startNs. stream.Subscribe takes a relative "seconds ago" window;
	// convert the absolute start into that. A start in the future clamps to 0
	// (live-only → nothing in range). JetStream clamps a start older than the
	// first retained message to that first message, which is exactly the
	// out-of-retention behavior we want.
	nowNs := time.Now().UnixNano()
	var replaySeconds uint32
	if startNs < nowNs {
		// +1s of slop so boundary frames at exactly startNs are not missed to
		// sub-second rounding; the per-frame timestamp filter below is exact.
		secs := (nowNs-startNs)/int64(time.Second) + 1
		replaySeconds = clampUint32(secs)
	}

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch, err := r.bus.Subscribe(subCtx, stream.SubscribeOptions{
		DeviceID:      deviceID,
		Channels:      channels,
		ReplaySeconds: replaySeconds,
	})
	if err != nil {
		return fmt.Errorf("experiments: replay subscribe: %w", err)
	}

	idle := time.NewTimer(replayIdle)
	defer idle.Stop()
	capTimer := time.NewTimer(replayCap)
	defer capTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-capTimer.C:
			return nil
		case <-idle.C:
			return nil
		case d, ok := <-ch:
			if !ok {
				return nil
			}
			if d.StreamSeq > highWater {
				return nil // reached the live tail; stop before post-call frames
			}
			atHighWater := d.StreamSeq >= highWater
			if d.Frame != nil {
				ts := int64(d.Frame.TimestampNs)
				if ts >= startNs && ts <= endNs {
					if err := visit(d); err != nil {
						return err
					}
				}
			}
			if atHighWater {
				return nil // drained everything up to the snapshot
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(replayIdle)
		}
	}
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
