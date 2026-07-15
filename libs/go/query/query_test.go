package query

import (
	"context"
	"testing"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/libs/go/stream"
)

// fakeReplayer emits a fixed set of pre-recorded frames (honoring the device and
// channel filters) then closes the channel, so Collect terminates
// deterministically without depending on the idle timer or a high-water mark.
type fakeReplayer struct {
	frames []stream.Delivered
}

func (f *fakeReplayer) Subscribe(ctx context.Context, opts stream.SubscribeOptions) (<-chan stream.Delivered, error) {
	out := make(chan stream.Delivered, len(f.frames)+1)
	chanSet := map[string]bool{}
	for _, c := range opts.Channels {
		chanSet[c] = true
	}
	go func() {
		defer close(out)
		for _, d := range f.frames {
			if opts.DeviceID != "" && d.DeviceID != opts.DeviceID {
				continue
			}
			if len(chanSet) > 0 && !chanSet[d.Frame.Channel] {
				continue
			}
			select {
			case out <- d:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func f64Delivered(device, packet, ch string, seq uint64, tNs int64, v float64) stream.Delivered {
	return stream.Delivered{
		DeviceID:  device,
		StreamSeq: seq,
		Frame:     &gantryv1.Frame{Channel: ch, Packet: packet, TimestampNs: uint64(tNs), Value: &gantryv1.Value{Kind: &gantryv1.Value_F64{F64: v}}},
	}
}

// TestCollectAbsoluteRange collects a subset window and asserts only the frames
// whose emitter timestamp falls in [StartNs, EndNs] are buffered.
func TestCollectAbsoluteRange(t *testing.T) {
	base := time.Now().Add(-time.Minute).UnixNano()
	rep := &fakeReplayer{frames: []stream.Delivered{
		f64Delivered("rover-1", "imu", "pitch_deg", 1, base+0, 10),
		f64Delivered("rover-1", "imu", "pitch_deg", 2, base+1_000_000, 11),
		f64Delivered("rover-1", "imu", "pitch_deg", 3, base+2_000_000, 12),
		f64Delivered("rover-1", "imu", "pitch_deg", 4, base+3_000_000, 13),
		f64Delivered("rover-1", "imu", "pitch_deg", 5, base+4_000_000, 14),
	}}
	// Window brackets samples 2,3,4 (base+1ms .. base+3ms).
	coll, err := Collect(context.Background(), rep, Options{
		DeviceID: "rover-1", Channels: []string{"pitch_deg"},
		StartNs: base + 1_000_000, EndNs: base + 3_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := SeriesKey{Device: "rover-1", Packet: "imu", Channel: "pitch_deg"}
	got := coll.SortedByTime(key)
	if len(got) != 3 {
		t.Fatalf("in-range samples = %d, want 3: %+v", len(got), got)
	}
	if got[0].Num != 11 || got[2].Num != 13 {
		t.Fatalf("range subset wrong: %+v", got)
	}
	if coll.Total != 3 {
		t.Errorf("Total = %d, want 3", coll.Total)
	}
	if coll.TruncatedByRetention {
		t.Errorf("unexpected retention truncation")
	}
}

// TestCollectUnboundedEnd covers the last-N-seconds callers: EndNs <= 0 means no
// upper bound, so frames slightly in the future (a common test/skew artifact)
// are still collected.
func TestCollectUnboundedEnd(t *testing.T) {
	now := time.Now().UnixNano()
	rep := &fakeReplayer{frames: []stream.Delivered{
		f64Delivered("rover-1", "imu", "pitch_deg", 1, now, 1),
		f64Delivered("rover-1", "imu", "pitch_deg", 2, now+1000, 2),
		f64Delivered("rover-1", "imu", "pitch_deg", 3, now+2000, 3),
	}}
	coll, err := Collect(context.Background(), rep, Options{
		DeviceID: "rover-1", Channels: []string{"pitch_deg"},
		StartNs: now - int64(time.Minute), EndNs: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := SeriesKey{Device: "rover-1", Packet: "imu", Channel: "pitch_deg"}
	if got := coll.SortedByTime(key); len(got) != 3 {
		t.Fatalf("unbounded-end samples = %d, want 3", len(got))
	}
}

// TestCollectPacketIdentity proves the engine keys series by
// (device, packet, channel): the same channel name under two packets yields two
// distinct series.
func TestCollectPacketIdentity(t *testing.T) {
	base := time.Now().Add(-time.Minute).UnixNano()
	rep := &fakeReplayer{frames: []stream.Delivered{
		f64Delivered("rover-1", "imu", "temp", 1, base+0, 20),
		f64Delivered("rover-1", "batt", "temp", 2, base+1_000_000, 30),
	}}
	coll, err := Collect(context.Background(), rep, Options{
		DeviceID: "rover-1", Channels: []string{"temp"},
		StartNs: base - int64(time.Second), EndNs: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(coll.Series) != 2 {
		t.Fatalf("series count = %d, want 2 (one per packet)", len(coll.Series))
	}
	imu := coll.Series[SeriesKey{Device: "rover-1", Packet: "imu", Channel: "temp"}]
	batt := coll.Series[SeriesKey{Device: "rover-1", Packet: "batt", Channel: "temp"}]
	if len(imu) != 1 || imu[0].Num != 20 {
		t.Errorf("imu.temp = %+v", imu)
	}
	if len(batt) != 1 || batt[0].Num != 30 {
		t.Errorf("batt.temp = %+v", batt)
	}
}

// TestCollectRetentionTruncation asserts the flag is set when StartNs predates
// the stream's first retained timestamp, and clear otherwise.
func TestCollectRetentionTruncation(t *testing.T) {
	now := time.Now().UnixNano()
	firstTs := now - int64(10*time.Minute)
	rep := &fakeReplayer{}

	// Start before the retention horizon -> truncated.
	coll, err := Collect(context.Background(), rep, Options{
		StartNs: firstTs - int64(time.Minute), EndNs: now, FirstTsNs: firstTs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !coll.TruncatedByRetention {
		t.Errorf("want TruncatedByRetention=true when start predates first_ts")
	}

	// Start at/after the horizon -> not truncated.
	coll2, err := Collect(context.Background(), rep, Options{
		StartNs: firstTs + int64(time.Minute), EndNs: now, FirstTsNs: firstTs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if coll2.TruncatedByRetention {
		t.Errorf("want TruncatedByRetention=false when start is within retention")
	}
}

// TestCollectHighWaterStop stops the drain at the snapshot high-water sequence
// and excludes frames published beyond it.
func TestCollectHighWaterStop(t *testing.T) {
	base := time.Now().Add(-time.Minute).UnixNano()
	rep := &fakeReplayer{frames: []stream.Delivered{
		f64Delivered("rover-1", "imu", "pitch_deg", 1, base+0, 1),
		f64Delivered("rover-1", "imu", "pitch_deg", 2, base+1_000_000, 2),
		f64Delivered("rover-1", "imu", "pitch_deg", 3, base+2_000_000, 3), // beyond high-water
	}}
	coll, err := Collect(context.Background(), rep, Options{
		DeviceID: "rover-1", Channels: []string{"pitch_deg"},
		StartNs: base - int64(time.Second), EndNs: 0,
		HighWater: 2, HasHighWater: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := SeriesKey{Device: "rover-1", Packet: "imu", Channel: "pitch_deg"}
	if got := coll.SortedByTime(key); len(got) != 2 {
		t.Fatalf("high-water stop kept %d samples, want 2 (seq 3 excluded)", len(got))
	}
}

// TestCollectEmptyStream short-circuits: an empty stream (high-water 0) yields no
// series without waiting on the drain timers.
func TestCollectEmptyStream(t *testing.T) {
	coll, err := Collect(context.Background(), &fakeReplayer{}, Options{
		StartNs: time.Now().Add(-time.Minute).UnixNano(), EndNs: 0,
		HighWater: 0, HasHighWater: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(coll.Series) != 0 {
		t.Fatalf("empty stream produced %d series", len(coll.Series))
	}
}

// endlessReplayer delivers a fixed backlog and then endless live frames far past
// the range end, mimicking a busy stream when the queried range lies in the past.
type endlessReplayer struct {
	backlog []stream.Delivered
}

func (e *endlessReplayer) Subscribe(ctx context.Context, opts stream.SubscribeOptions) (<-chan stream.Delivered, error) {
	out := make(chan stream.Delivered, 64)
	go func() {
		defer close(out)
		for _, d := range e.backlog {
			select {
			case out <- d:
			case <-ctx.Done():
				return
			}
		}
		seq := uint64(1000)
		ts := e.backlog[len(e.backlog)-1].Frame.TimestampNs
		for {
			seq++
			ts += uint64(20 * time.Millisecond)
			select {
			case out <- f64Delivered("dev", "imu", "pitch", seq, int64(ts), 1.0):
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// TestCollectPastEndStopsEarly is the perf regression guard: a query whose EndNs
// lies in the past must terminate as soon as arrival-ordered timestamps pass
// EndNs (+slop) instead of draining the stream to now (measured ~2s per query
// against a live 300fps stream before the fix).
func TestCollectPastEndStopsEarly(t *testing.T) {
	base := time.Now().Add(-10 * time.Minute).UnixNano()
	rep := &endlessReplayer{backlog: []stream.Delivered{
		f64Delivered("dev", "imu", "pitch", 1, base, 1),
		f64Delivered("dev", "imu", "pitch", 2, base+int64(time.Second), 2),
		f64Delivered("dev", "imu", "pitch", 3, base+9*int64(time.Second), 3), // past end+slop
	}}
	start := time.Now()
	coll, err := Collect(context.Background(), rep, Options{
		StartNs: base, EndNs: base + 2*int64(time.Second),
		HighWater: ^uint64(0) - 1, HasHighWater: true, // high-water unreachable: only the end-stop can terminate promptly
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("past-end query took %v; end-stop did not fire", elapsed)
	}
	key := SeriesKey{Device: "dev", Packet: "imu", Channel: "pitch"}
	if got := len(coll.Series[key]); got != 2 {
		t.Fatalf("want 2 in-range samples, got %d", got)
	}
}
