package server_test

import (
	"context"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
)

// TestQueryRangeEndToEnd is the QueryService vertical slice: publish a dense and
// a sparse channel across a known time span, then exercise QueryRange for raw
// (small), bucketed (large), range-subset correctness, an unknown channel, and a
// bad range.
func TestQueryRangeEndToEnd(t *testing.T) {
	baseURL := startEdge(t)
	httpClient := h2cClient()
	ingestClient := gantryv1connect.NewIngestServiceClient(httpClient, baseURL)
	queryClient := gantryv1connect.NewQueryServiceClient(httpClient, baseURL)
	ctx := context.Background()
	const device = "rover-1"
	const packet = "imu"

	// Dense pitch_deg: 1000 frames at 1ms spacing. Sparse roll_deg: 3 frames.
	base := time.Now().Add(-5 * time.Second).UnixNano()
	const n = 1000
	frames := make([]*gantryv1.Frame, 0, n+3)
	for i := 0; i < n; i++ {
		frames = append(frames, f64FrameP(packet, "pitch_deg", base+int64(i)*1_000_000, float64(i)))
	}
	frames = append(frames,
		f64FrameP(packet, "roll_deg", base, 1),
		f64FrameP(packet, "roll_deg", base+500_000_000, 2),
		f64FrameP(packet, "roll_deg", base+1_000_000_000, 3),
	)
	if _, err := ingestClient.PublishBatch(ctx, connect.NewRequest(&gantryv1.PublishBatchRequest{
		Batch: &gantryv1.FrameBatch{DeviceId: device, Sequence: 1, Frames: frames},
	})); err != nil {
		t.Fatalf("PublishBatch: %v", err)
	}

	nowNs := uint64(time.Now().UnixNano())
	fullStart := uint64(base - int64(time.Second))

	// ---- full range: pitch downsamples, roll comes back raw ----
	{
		resp, err := queryClient.QueryRange(ctx, connect.NewRequest(&gantryv1.QueryRangeRequest{
			DeviceId: device, StartNs: fullStart, EndNs: nowNs,
		}))
		if err != nil {
			t.Fatalf("QueryRange full: %v", err)
		}
		byCh := map[string]*gantryv1.ChannelSeries{}
		for _, s := range resp.Msg.Series {
			byCh[s.Channel] = s
			// Series identity must be (device, packet, channel).
			if s.DeviceId != device || s.Packet != packet {
				t.Errorf("series identity wrong: %+v", s)
			}
		}
		pitch := byCh["pitch_deg"]
		if pitch == nil {
			t.Fatalf("no pitch_deg series; got %v", byCh)
		}
		if len(pitch.Buckets) == 0 || len(pitch.Raw) != 0 {
			t.Fatalf("pitch should be bucketed: buckets=%d raw=%d", len(pitch.Buckets), len(pitch.Raw))
		}
		if len(pitch.Buckets) > queryDefaultMaxPoints() {
			t.Fatalf("pitch buckets = %d, want <= default cap", len(pitch.Buckets))
		}
		var total uint32
		for _, b := range pitch.Buckets {
			total += b.Count
		}
		if total != n {
			t.Fatalf("bucket counts sum = %d, want %d", total, n)
		}
		if pitch.Kind != gantryv1.ValueKind_VALUE_KIND_F64 {
			t.Errorf("pitch kind = %v, want f64", pitch.Kind)
		}

		roll := byCh["roll_deg"]
		if roll == nil {
			t.Fatalf("no roll_deg series")
		}
		if len(roll.Raw) != 3 || len(roll.Buckets) != 0 {
			t.Fatalf("roll should be 3 raw points: raw=%d buckets=%d", len(roll.Raw), len(roll.Buckets))
		}
		if roll.Raw[0].Value != 1 || roll.Raw[2].Value != 3 {
			t.Errorf("roll raw values = %v", roll.Raw)
		}
	}

	// ---- explicit small max_points forces pitch into few buckets ----
	{
		resp, err := queryClient.QueryRange(ctx, connect.NewRequest(&gantryv1.QueryRangeRequest{
			DeviceId: device, Channels: []string{"pitch_deg"},
			StartNs: fullStart, EndNs: nowNs, MaxPointsPerChannel: 50,
		}))
		if err != nil {
			t.Fatalf("QueryRange bucketed: %v", err)
		}
		if len(resp.Msg.Series) != 1 {
			t.Fatalf("want 1 series, got %d", len(resp.Msg.Series))
		}
		b := resp.Msg.Series[0].Buckets
		if len(b) == 0 || len(b) > 50 {
			t.Fatalf("buckets = %d, want 1..50", len(b))
		}
	}

	// ---- range subset: only pitch frames whose ts is in [base+100ms, base+199ms] ----
	{
		subStart := uint64(base + 100_000_000)
		subEnd := uint64(base + 199_000_000)
		resp, err := queryClient.QueryRange(ctx, connect.NewRequest(&gantryv1.QueryRangeRequest{
			DeviceId: device, Channels: []string{"pitch_deg"},
			StartNs: subStart, EndNs: subEnd,
		}))
		if err != nil {
			t.Fatalf("QueryRange subset: %v", err)
		}
		if len(resp.Msg.Series) != 1 {
			t.Fatalf("want 1 series, got %d", len(resp.Msg.Series))
		}
		raw := resp.Msg.Series[0].Raw
		// i in 100..199 -> 100 frames, below the cap so raw.
		if len(raw) != 100 {
			t.Fatalf("subset raw = %d, want 100", len(raw))
		}
		for _, p := range raw {
			if p.TNs < subStart || p.TNs > subEnd {
				t.Fatalf("subset point t=%d outside [%d,%d]", p.TNs, subStart, subEnd)
			}
		}
	}

	// ---- unknown channel: no series ----
	{
		resp, err := queryClient.QueryRange(ctx, connect.NewRequest(&gantryv1.QueryRangeRequest{
			DeviceId: device, Channels: []string{"does_not_exist"},
			StartNs: fullStart, EndNs: nowNs,
		}))
		if err != nil {
			t.Fatalf("QueryRange unknown: %v", err)
		}
		if len(resp.Msg.Series) != 0 {
			t.Fatalf("unknown channel returned %d series, want 0", len(resp.Msg.Series))
		}
	}

	// ---- bad range: end <= start is InvalidArgument ----
	{
		_, err := queryClient.QueryRange(ctx, connect.NewRequest(&gantryv1.QueryRangeRequest{
			DeviceId: device, StartNs: nowNs, EndNs: fullStart,
		}))
		if err == nil {
			t.Fatal("bad range (end<start) should error")
		}
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("bad range code = %v, want InvalidArgument", connect.CodeOf(err))
		}
	}
}

// queryDefaultMaxPoints mirrors the server default (500) for the test's cap
// assertion without exporting the internal constant.
func queryDefaultMaxPoints() int { return 500 }
