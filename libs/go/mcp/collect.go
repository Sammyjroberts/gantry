package mcp

import (
	"context"
	"sort"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/libs/go/query"
)

// The MCP tools address channels by name and key their results by
// (device, channel). The shared query engine keys by the fuller
// (device, packet, channel) identity, so this file re-projects the engine's
// series down to MCP's coarser key, preserving the v1 read surface's documented
// same-name-across-packets merge behavior. The bucket math, value decoding, and
// the bounded replay drain all live in libs/go/query now.

// sample, bucket, and rawPoint alias the shared engine types so the tool code
// (tools.go) reads unchanged against them.
type sample = query.Sample
type bucket = query.Bucket
type rawPoint = query.RawPoint

func downsample(s []sample, maxPoints int) []bucket { return query.Downsample(s, maxPoints) }
func rawPoints(s []sample) []rawPoint               { return query.RawPoints(s) }
func kindString(k gantryv1.ValueKind) string        { return query.KindString(k) }
func isNumericKind(k gantryv1.ValueKind) bool       { return query.IsNumericKind(k) }

// collectKey identifies a series within a collection by (device, channel).
// Packet is carried as sample metadata rather than key identity: MCP callers
// address channels by name, and the rare same-name-across-packets collision is
// acceptable for the v1 read surface (documented in docs/MCP.md).
type collectKey struct {
	device  string
	channel string
}

// collection is the (device, channel)-keyed view the tools consume.
type collection struct {
	series    map[collectKey][]sample
	total     int
	truncated bool
}

// sortedByTime returns the samples for a key in ascending timestamp order.
func (c *collection) sortedByTime(key collectKey) []sample {
	s := c.series[key]
	sort.SliceStable(s, func(i, j int) bool { return s[i].TNs < s[j].TNs })
	return s
}

// collectWindow drains the last `seconds` via the shared query engine and
// re-keys the (device, packet, channel) series down to MCP's (device, channel)
// identity. The window is expressed as an absolute [now-seconds, unbounded]
// range: the high-water snapshot (not an end timestamp) bounds the tail, exactly
// as the previous replay-window collector did.
func collectWindow(ctx context.Context, rep Replayer, highWater uint64, hasHighWater bool, deviceID string, channels []string, seconds uint32) (*collection, error) {
	startNs := time.Now().Add(-time.Duration(seconds) * time.Second).UnixNano()
	q, err := query.Collect(ctx, rep, query.Options{
		DeviceID:     deviceID,
		Channels:     channels,
		StartNs:      startNs,
		EndNs:        0, // unbounded upper: the high-water snapshot stops the drain
		HighWater:    highWater,
		HasHighWater: hasHighWater,
	})
	if err != nil {
		return nil, err
	}
	c := &collection{series: make(map[collectKey][]sample), total: q.Total, truncated: q.Truncated}
	for k, samples := range q.Series {
		ck := collectKey{device: k.Device, channel: k.Channel}
		c.series[ck] = append(c.series[ck], samples...)
	}
	return c, nil
}
