package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/libs/go/stream"
)

// Drain tuning. Replay from an in-process JetStream is effectively instant, so
// these bound how long a tool call waits for a replay to complete, not how much
// data it can return.
const (
	// drainIdle ends a drain once no frame has arrived for this long: the replay
	// backlog is considered flushed. Needed because a filtered subscription may
	// never observe the stream's global last sequence (that message can be on a
	// subject we did not select), so a high-water mark alone cannot terminate.
	drainIdle = 400 * time.Millisecond
	// drainCap is the absolute ceiling on a single collection, a safety valve
	// against a firehose device keeping the drain alive with live frames.
	drainCap = 6 * time.Second
	// maxCollectPoints caps total points buffered across all channels in one
	// call, bounding memory on a very wide/dense window before downsampling.
	maxCollectPoints = 400_000
)

// collectKey identifies a series within a collection by (device, channel).
// Packet is carried as sample metadata rather than key identity: MCP callers
// address channels by name, and the rare same-name-across-packets collision is
// acceptable for the v1 read surface (documented in docs/MCP.md).
type collectKey struct {
	device  string
	channel string
}

// sample is one decoded frame reduced to what the tools need.
type sample struct {
	tNs    int64
	packet string
	// num holds the numeric value when numeric is true (f64/i64 as float, bool
	// as 0/1). For text/raw channels numeric is false and text carries a
	// display string.
	num     float64
	numeric bool
	text    string
	kind    gantryv1.ValueKind
}

// collection is the result of draining a replay window.
type collection struct {
	series map[collectKey][]sample
	total  int
	// truncated is set if maxCollectPoints stopped collection early.
	truncated bool
}

// collectWindow opens a replay subscription over the last `seconds` and drains
// the backlog into per-series samples. Termination is the earliest of: reaching
// the stream high-water sequence (when hasHighWater), an idle gap, the absolute
// cap, or ctx cancellation. Frames published after the call began (stream
// sequence beyond the high-water mark) are excluded so results are a stable
// snapshot of "up to now".
func collectWindow(ctx context.Context, rep Replayer, highWater uint64, hasHighWater bool, deviceID string, channels []string, seconds uint32) (*collection, error) {
	c := &collection{series: make(map[collectKey][]sample)}
	if hasHighWater && highWater == 0 {
		return c, nil // empty stream: nothing to replay
	}

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch, err := rep.Subscribe(subCtx, stream.SubscribeOptions{
		DeviceID:      deviceID,
		Channels:      channels,
		ReplaySeconds: seconds,
	})
	if err != nil {
		return nil, fmt.Errorf("replay subscribe: %w", err)
	}

	idle := time.NewTimer(drainIdle)
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
			if hasHighWater && d.StreamSeq > highWater {
				return c, nil // reached live tail; stop before including post-call frames
			}
			c.add(d)
			atHighWater := hasHighWater && d.StreamSeq >= highWater
			if c.total >= maxCollectPoints {
				c.truncated = true
				return c, nil
			}
			if atHighWater {
				return c, nil // drained everything up to the snapshot
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

func (c *collection) add(d stream.Delivered) {
	f := d.Frame
	if f == nil || f.Channel == "" {
		return
	}
	key := collectKey{device: d.DeviceID, channel: f.Channel}
	s := sample{tNs: int64(f.TimestampNs), packet: f.Packet, kind: valueKind(f.Value)}
	if n, ok := numericValue(f.Value); ok {
		s.num = n
		s.numeric = true
	} else {
		s.text = textValue(f.Value)
	}
	c.series[key] = append(c.series[key], s)
	c.total++
}

// sortedByTime returns the samples for a key in ascending timestamp order.
// Ordered consumers already deliver in stream order, but replayed cross-channel
// interleaving plus emitter-stamped timestamps mean per-series time order is not
// guaranteed; sort to be safe.
func (c *collection) sortedByTime(key collectKey) []sample {
	s := c.series[key]
	sort.SliceStable(s, func(i, j int) bool { return s[i].tNs < s[j].tNs })
	return s
}

// numericValue extracts a float64 from a telemetry Value for numeric kinds
// (f64, i64, bool→0/1). ok is false for text/raw/unset.
func numericValue(v *gantryv1.Value) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch k := v.Kind.(type) {
	case *gantryv1.Value_F64:
		return k.F64, true
	case *gantryv1.Value_I64:
		return float64(k.I64), true
	case *gantryv1.Value_Flag:
		if k.Flag {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

// textValue renders any Value as a display string (used for non-numeric
// channels and for get_last's raw value echo).
func textValue(v *gantryv1.Value) string {
	if v == nil {
		return ""
	}
	switch k := v.Kind.(type) {
	case *gantryv1.Value_F64:
		return strconv.FormatFloat(k.F64, 'g', -1, 64)
	case *gantryv1.Value_I64:
		return strconv.FormatInt(k.I64, 10)
	case *gantryv1.Value_Flag:
		return strconv.FormatBool(k.Flag)
	case *gantryv1.Value_Text:
		return k.Text
	case *gantryv1.Value_Raw:
		return base64.StdEncoding.EncodeToString(k.Raw)
	default:
		return ""
	}
}

// valueKind maps a Value's oneof arm to its ValueKind (mirrors registry.InferKind
// without pulling the registry into the value path).
func valueKind(v *gantryv1.Value) gantryv1.ValueKind {
	if v == nil {
		return gantryv1.ValueKind_VALUE_KIND_UNSPECIFIED
	}
	switch v.Kind.(type) {
	case *gantryv1.Value_F64:
		return gantryv1.ValueKind_VALUE_KIND_F64
	case *gantryv1.Value_I64:
		return gantryv1.ValueKind_VALUE_KIND_I64
	case *gantryv1.Value_Flag:
		return gantryv1.ValueKind_VALUE_KIND_BOOL
	case *gantryv1.Value_Text:
		return gantryv1.ValueKind_VALUE_KIND_TEXT
	case *gantryv1.Value_Raw:
		return gantryv1.ValueKind_VALUE_KIND_RAW
	default:
		return gantryv1.ValueKind_VALUE_KIND_UNSPECIFIED
	}
}

// kindString gives the compact JSON tag for a ValueKind ("f64", "i64", "bool",
// "text", "raw", "unspecified").
func kindString(k gantryv1.ValueKind) string {
	switch k {
	case gantryv1.ValueKind_VALUE_KIND_F64:
		return "f64"
	case gantryv1.ValueKind_VALUE_KIND_I64:
		return "i64"
	case gantryv1.ValueKind_VALUE_KIND_BOOL:
		return "bool"
	case gantryv1.ValueKind_VALUE_KIND_TEXT:
		return "text"
	case gantryv1.ValueKind_VALUE_KIND_RAW:
		return "raw"
	default:
		return "unspecified"
	}
}
