package query

import (
	"context"
	"math"
)

// SegmentReader is the historical (durable) read seam the planner merges with
// the JetStream tail. It is defined here — over query's own SeriesKey/Sample
// types — rather than imported from core/go/segments so that query keeps NO
// dependency on the segment store (segments depends on query, not the reverse;
// a segments.Reader satisfies this interface via an adapter). A nil SegmentReader
// selects the pure-replay fallback used until a catalog is wired.
type SegmentReader interface {
	// ReadRange streams stored samples in [startNs, endNs] for the device/channel
	// filter to visit, keyed by series. deviceID == "" spans all devices; empty
	// channels spans all channels.
	ReadRange(ctx context.Context, deviceID string, channels []string, startNs, endNs int64, visit func(SeriesKey, Sample) error) error
	// Horizon reports the min start and max end timestamps across all durable
	// segments, and whether any segment exists.
	Horizon(ctx context.Context) (minStartNs, maxEndNs int64, ok bool, err error)
}

// dedupKey identifies a single sample for segment/tail overlap dedupe.
type dedupKey struct {
	key SeriesKey
	ts  int64
}

// CollectWithSegments answers a bounded range from the durable segment store for
// the historical span plus a JetStream replay for the tail that has not yet been
// flushed, merging the two.
//
// Semantics (the planner seam):
//   - Segments serve [StartNs, EndNs]; the reader only opens the segments whose
//     time range overlaps, so a wide window is O(segments touched), not O(rows).
//   - The tail replay covers (newest segment end … EndNs] — the frames still only
//     in JetStream. With no segments it degrades to the full-window replay, i.e.
//     exactly what Collect does today.
//   - Overlap at the boundary is deduped by (series, ts) PREFERRING segments: a
//     ts already emitted from a segment is dropped from the tail, so a frame that
//     is both flushed and still in the retained tail is counted once.
//   - TruncatedByRetention reflects the SEGMENT horizon (the oldest durable
//     start), which reaches far further back than the JetStream tail: it is set
//     when StartNs predates the oldest data available (segment min-start, or the
//     stream's first retained ts when there are no segments).
func CollectWithSegments(ctx context.Context, rep Replayer, seg SegmentReader, opts Options) (*Collection, error) {
	if seg == nil {
		return Collect(ctx, rep, opts) // pure-replay fallback
	}
	c := &Collection{Series: make(map[SeriesKey][]Sample)}

	minStart, maxEnd, hasSeg, err := seg.Horizon(ctx)
	if err != nil {
		return nil, err
	}

	// Retention horizon: the oldest timestamp any tier can answer for. Segments
	// extend it far below the stream's first retained ts.
	horizon := opts.FirstTsNs
	if hasSeg && (horizon == 0 || minStart < horizon) {
		horizon = minStart
	}
	if horizon > 0 && opts.StartNs < horizon {
		c.TruncatedByRetention = true
	}

	maxSamples := opts.MaxSamples
	if maxSamples <= 0 {
		maxSamples = defaultMaxSamples
	}

	// --- historical span from segments ---
	seen := make(map[dedupKey]struct{})
	if hasSeg {
		segEnd := opts.EndNs
		if segEnd <= 0 {
			segEnd = math.MaxInt64
		}
		err := seg.ReadRange(ctx, opts.DeviceID, opts.Channels, opts.StartNs, segEnd, func(k SeriesKey, s Sample) error {
			c.Series[k] = append(c.Series[k], s)
			c.Total++
			seen[dedupKey{k, s.TNs}] = struct{}{}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if c.Total >= maxSamples {
			c.Truncated = true
			return c, nil
		}
	}

	// --- tail from the stream replay ---
	tailOpts := opts
	if hasSeg && maxEnd >= opts.StartNs {
		// Start the tail at the newest segment end; the small overlap is resolved
		// by the (series, ts) dedupe below, and it avoids missing frames buffered
		// right at the flush boundary.
		tailOpts.StartNs = maxEnd
	}
	tailOpts.FirstTsNs = 0 // retention already decided from the segment horizon
	tail, err := Collect(ctx, rep, tailOpts)
	if err != nil {
		return nil, err
	}
	for k, samples := range tail.Series {
		for _, s := range samples {
			if _, dup := seen[dedupKey{k, s.TNs}]; dup {
				continue // prefer the segment copy
			}
			c.Series[k] = append(c.Series[k], s)
			c.Total++
			if c.Total >= maxSamples {
				c.Truncated = true
				return c, nil
			}
		}
	}
	if tail.Truncated {
		c.Truncated = true
	}
	return c, nil
}
