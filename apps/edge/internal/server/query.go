package server

import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
	"github.com/Sammyjroberts/gantry/libs/go/mcp"
	"github.com/Sammyjroberts/gantry/libs/go/query"
	"github.com/Sammyjroberts/gantry/libs/go/stream"
)

const (
	// queryDefaultMaxPoints is the per-channel bucket cap when a request omits
	// max_points_per_channel (proto default 0).
	queryDefaultMaxPoints = 500
	// queryMaxPoints is the hard server-side ceiling on max_points_per_channel.
	queryMaxPoints = 5000
)

// queryService implements gantry.v1.QueryService over the shared bounded-window
// read engine (libs/go/query). It replays the requested [start_ns, end_ns] range
// from JetStream and returns each (device, packet, channel) series either raw or
// downsampled into min/max/mean buckets. It holds no tail state.
type queryService struct {
	gantryv1connect.UnimplementedQueryServiceHandler
	bus    *stream.Bus
	stater mcp.StreamStater
	// segments serves the durable historical span; nil falls back to pure
	// stream replay (the pre-segment-store behavior).
	segments query.SegmentReader
}

func (s *queryService) QueryRange(ctx context.Context, req *connect.Request[gantryv1.QueryRangeRequest]) (*connect.Response[gantryv1.QueryRangeResponse], error) {
	m := req.Msg
	startNs := int64(m.StartNs)
	endNs := int64(m.EndNs)
	if endNs <= startNs {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("end_ns (%d) must be greater than start_ns (%d)", endNs, startNs))
	}

	maxPoints := int(m.MaxPointsPerChannel)
	if maxPoints <= 0 {
		maxPoints = queryDefaultMaxPoints
	}
	if maxPoints > queryMaxPoints {
		maxPoints = queryMaxPoints
	}

	// Snapshot stream state: last sequence bounds the drain (excludes frames
	// published after the call began); first retained timestamp drives the
	// out-of-retention flag.
	st, err := s.stater.StreamState(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("stream state: %w", err))
	}

	coll, err := query.CollectWithSegments(ctx, s.bus, s.segments, query.Options{
		DeviceID:     m.DeviceId,
		Channels:     m.Channels,
		StartNs:      startNs,
		EndNs:        endNs,
		HighWater:    st.LastSeq,
		HasHighWater: true,
		FirstTsNs:    st.FirstTsNs,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &gantryv1.QueryRangeResponse{TruncatedByRetention: coll.TruncatedByRetention}
	for _, key := range coll.SortedKeys() {
		samples := coll.SortedByTime(key)
		resp.Series = append(resp.Series, buildSeries(key, samples, maxPoints))
	}
	return connect.NewResponse(resp), nil
}

// buildSeries renders one collected series to a proto ChannelSeries: numeric
// series over the cap are downsampled to buckets, everything else is raw points
// (numeric points carry value, text/raw points carry text).
func buildSeries(key query.SeriesKey, samples []query.Sample, maxPoints int) *gantryv1.ChannelSeries {
	cs := &gantryv1.ChannelSeries{
		DeviceId: key.Device,
		Packet:   key.Packet,
		Channel:  key.Channel,
		Kind:     seriesKind(samples),
	}

	numeric := true
	for _, s := range samples {
		if !s.Numeric {
			numeric = false
			break
		}
	}

	if numeric && len(samples) > maxPoints {
		for _, b := range query.Downsample(samples, maxPoints) {
			cs.Buckets = append(cs.Buckets, &gantryv1.Bucket{
				TNs:   uint64(b.TNs),
				Min:   b.Min,
				Max:   b.Max,
				Mean:  b.Mean,
				Count: uint32(b.Count),
			})
		}
		return cs
	}

	for _, s := range samples {
		rp := &gantryv1.RawPoint{TNs: uint64(s.TNs)}
		if s.Numeric {
			rp.Value = s.Num
		} else {
			rp.Text = s.Text
		}
		cs.Raw = append(cs.Raw, rp)
	}
	return cs
}

// seriesKind reports the value kind for a series: the first non-unspecified
// sample kind (all samples on a channel share a kind in practice).
func seriesKind(samples []query.Sample) gantryv1.ValueKind {
	for _, s := range samples {
		if s.Kind != gantryv1.ValueKind_VALUE_KIND_UNSPECIFIED {
			return s.Kind
		}
	}
	return gantryv1.ValueKind_VALUE_KIND_UNSPECIFIED
}
