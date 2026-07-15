package query

import (
	"context"
	"testing"
	"time"

	"github.com/Sammyjroberts/gantry/libs/go/stream"
)

// fakeSegments is a canned SegmentReader: it holds pre-stored samples and a
// fixed horizon, returning whatever falls in [startNs, endNs] and matches the
// channel/device filter.
type fakeSegments struct {
	rows     []segRow
	minStart int64
	maxEnd   int64
	has      bool
}

type segRow struct {
	key SeriesKey
	s   Sample
}

func (f *fakeSegments) ReadRange(_ context.Context, device string, channels []string, startNs, endNs int64, visit func(SeriesKey, Sample) error) error {
	want := map[string]bool{}
	for _, c := range channels {
		want[c] = true
	}
	for _, r := range f.rows {
		if device != "" && r.key.Device != device {
			continue
		}
		if len(want) > 0 && !want[r.key.Channel] {
			continue
		}
		if r.s.TNs < startNs || r.s.TNs > endNs {
			continue
		}
		if err := visit(r.key, r.s); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeSegments) Horizon(context.Context) (int64, int64, bool, error) {
	return f.minStart, f.maxEnd, f.has, nil
}

func seg(device, packet, ch string, tNs int64, v float64) segRow {
	return segRow{
		key: SeriesKey{Device: device, Packet: packet, Channel: ch},
		s:   Sample{TNs: tNs, Packet: packet, Num: v, Numeric: true, Kind: 1},
	}
}

// TestPlannerMergeNoDoubleCount is the core planner-seam assertion: the historical
// span comes from segments, the tail from replay, the boundary sample exists in
// BOTH, and it must appear exactly once (segments preferred).
func TestPlannerMergeNoDoubleCount(t *testing.T) {
	// Use a recent base so the tail replay's relative window covers it.
	base := time.Now().Add(-30 * time.Second).UnixNano()
	segEnd := base + 3_000_000 // newest segment end (the boundary ts)

	segs := &fakeSegments{
		has:      true,
		minStart: base,
		maxEnd:   segEnd,
		rows: []segRow{
			seg("rover-1", "imu", "pitch_deg", base+0, 10),
			seg("rover-1", "imu", "pitch_deg", base+1_000_000, 11),
			seg("rover-1", "imu", "pitch_deg", segEnd, 12), // boundary — also in tail
		},
	}
	rep := &fakeReplayer{frames: []stream.Delivered{
		// The tail replay: the boundary frame (duplicate) plus two newer frames.
		f64Delivered("rover-1", "imu", "pitch_deg", 10, segEnd, 12),
		f64Delivered("rover-1", "imu", "pitch_deg", 11, segEnd+1_000_000, 13),
		f64Delivered("rover-1", "imu", "pitch_deg", 12, segEnd+2_000_000, 14),
	}}

	c, err := CollectWithSegments(context.Background(), rep, segs, Options{
		DeviceID: "rover-1",
		StartNs:  base,
		EndNs:    segEnd + 5_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := SeriesKey{Device: "rover-1", Packet: "imu", Channel: "pitch_deg"}
	got := c.SortedByTime(key)
	// 3 from segments + 2 net-new from tail (boundary deduped) = 5.
	if len(got) != 5 {
		t.Fatalf("merged samples = %d, want 5 (boundary must not double-count): %+v", len(got), got)
	}
	// Ensure the boundary ts appears exactly once.
	n := 0
	for _, s := range got {
		if s.TNs == segEnd {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("boundary ts appears %d times, want 1", n)
	}
	if c.Total != 5 {
		t.Fatalf("Total = %d, want 5", c.Total)
	}
}

// TestPlannerRetentionHorizon proves TruncatedByRetention reflects the SEGMENT
// horizon, not the stream tail.
func TestPlannerRetentionHorizon(t *testing.T) {
	base := time.Now().Add(-20 * time.Second).UnixNano()
	segs := &fakeSegments{has: true, minStart: base, maxEnd: base + 1_000_000,
		rows: []segRow{seg("d", "p", "c", base, 1)}}
	rep := &fakeReplayer{}

	// Query starts BEFORE the oldest segment → truncated.
	c, err := CollectWithSegments(context.Background(), rep, segs, Options{StartNs: base - 1_000_000, EndNs: base + 10_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if !c.TruncatedByRetention {
		t.Fatal("expected TruncatedByRetention when start predates segment horizon")
	}

	// Query starts AT the horizon → not truncated.
	c, _ = CollectWithSegments(context.Background(), rep, segs, Options{StartNs: base, EndNs: base + 10_000_000})
	if c.TruncatedByRetention {
		t.Fatal("did not expect truncation when start == segment horizon")
	}
}

// TestPlannerNilSegmentsFallsBack proves a nil SegmentReader is the pure-replay
// path (identical to Collect).
func TestPlannerNilSegmentsFallsBack(t *testing.T) {
	base := time.Now().Add(-10 * time.Second).UnixNano()
	rep := &fakeReplayer{frames: []stream.Delivered{
		f64Delivered("d", "p", "c", 1, base, 1),
		f64Delivered("d", "p", "c", 2, base+1_000_000, 2),
	}}
	c, err := CollectWithSegments(context.Background(), rep, nil, Options{StartNs: base - 1_000_000, EndNs: base + 10_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 2 {
		t.Fatalf("fallback Total = %d, want 2", c.Total)
	}
}
