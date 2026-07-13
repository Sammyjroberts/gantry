package mcp

import (
	"context"
	"testing"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/libs/go/registry"
	"github.com/Sammyjroberts/gantry/libs/go/stream"
)

// fakeReplayer emits a fixed set of pre-recorded frames (honoring the device and
// channel filters) then closes the channel, so collectWindow terminates
// deterministically without depending on the idle timer.
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

type fakeStater struct{ st StreamState }

func (f fakeStater) StreamState(context.Context) (StreamState, error) { return f.st, nil }

func f64Delivered(device, ch string, seq uint64, tNs int64, v float64) stream.Delivered {
	return stream.Delivered{
		DeviceID:  device,
		StreamSeq: seq,
		Frame:     &gantryv1.Frame{Channel: ch, Packet: "imu", TimestampNs: uint64(tNs), Value: &gantryv1.Value{Kind: &gantryv1.Value_F64{F64: v}}},
	}
}

func newReg(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New()
	reg.Register("rover-1", []*gantryv1.ChannelInfo{
		{Name: "pitch_deg", Packet: "imu", Kind: gantryv1.ValueKind_VALUE_KIND_F64, Unit: "deg"},
		{Name: "roll_deg", Packet: "imu", Kind: gantryv1.ValueKind_VALUE_KIND_F64, Unit: "deg"},
	})
	return reg
}

func TestListChannels(t *testing.T) {
	d := Deps{Channels: newReg(t), Replay: &fakeReplayer{}}
	_, res, err := d.listChannels(context.Background(), nil, listChannelsArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Devices) != 1 || res.Devices[0].DeviceID != "rover-1" {
		t.Fatalf("devices = %+v", res.Devices)
	}
	if len(res.Devices[0].Channels) != 2 {
		t.Fatalf("channels = %+v", res.Devices[0].Channels)
	}
}

func TestGetWindowRaw(t *testing.T) {
	now := time.Now().UnixNano()
	rep := &fakeReplayer{frames: []stream.Delivered{
		f64Delivered("rover-1", "pitch_deg", 1, now, 1),
		f64Delivered("rover-1", "pitch_deg", 2, now+1000, 2),
		f64Delivered("rover-1", "pitch_deg", 3, now+2000, 3),
	}}
	d := Deps{Channels: newReg(t), Replay: rep}
	_, res, err := d.getWindow(context.Background(), nil, getWindowArgs{DeviceID: "rover-1", Channels: []string{"pitch_deg"}, Seconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Channels) != 1 {
		t.Fatalf("channels = %+v", res.Channels)
	}
	c := res.Channels[0]
	if c.Downsampled || c.RawCount != 3 || len(c.Points) != 3 {
		t.Fatalf("want 3 raw points, got %+v", c)
	}
	if c.Kind != "f64" || c.Unit != "deg" || !c.Numeric {
		t.Errorf("metadata wrong: %+v", c)
	}
}

func TestGetWindowDownsamples(t *testing.T) {
	now := time.Now().UnixNano()
	var frames []stream.Delivered
	for i := 0; i < 1000; i++ {
		frames = append(frames, f64Delivered("rover-1", "pitch_deg", uint64(i+1), now+int64(i)*1_000_000, float64(i)))
	}
	d := Deps{Channels: newReg(t), Replay: &fakeReplayer{frames: frames}}
	_, res, err := d.getWindow(context.Background(), nil, getWindowArgs{
		DeviceID: "rover-1", Channels: []string{"pitch_deg"}, Seconds: 60, MaxPointsPerChannel: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Channels[0]
	if !c.Downsampled || c.RawCount != 1000 {
		t.Fatalf("want downsampled raw=1000, got %+v", c)
	}
	if len(c.Buckets) == 0 || len(c.Buckets) > 100 {
		t.Fatalf("buckets = %d, want 1..100", len(c.Buckets))
	}
	if len(c.Points) != 0 {
		t.Errorf("downsampled result should not carry raw points")
	}
}

func TestGetWindowUnknownChannel(t *testing.T) {
	d := Deps{Channels: newReg(t), Replay: &fakeReplayer{}}
	_, res, err := d.getWindow(context.Background(), nil, getWindowArgs{DeviceID: "rover-1", Channels: []string{"ptch_deg"}, Seconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.UnknownChannels) != 1 || res.UnknownChannels[0].Requested != "ptch_deg" {
		t.Fatalf("unknown = %+v", res.UnknownChannels)
	}
	if len(res.UnknownChannels[0].Nearest) == 0 || res.UnknownChannels[0].Nearest[0] != "pitch_deg" {
		t.Fatalf("want pitch_deg suggested, got %+v", res.UnknownChannels[0].Nearest)
	}
}

func TestGetWindowSecondsValidation(t *testing.T) {
	d := Deps{Channels: newReg(t), Replay: &fakeReplayer{}}
	if _, _, err := d.getWindow(context.Background(), nil, getWindowArgs{Channels: []string{"pitch_deg"}, Seconds: 0}); err == nil {
		t.Error("seconds=0 should error")
	}
	if _, _, err := d.getWindow(context.Background(), nil, getWindowArgs{Channels: []string{"pitch_deg"}, Seconds: 5000}); err == nil {
		t.Error("seconds>1800 should error")
	}
}

func TestGetLastValueAndStale(t *testing.T) {
	now := time.Now().UnixNano()
	rep := &fakeReplayer{frames: []stream.Delivered{
		f64Delivered("rover-1", "pitch_deg", 1, now-2_000_000_000, 10), // 2s ago
		f64Delivered("rover-1", "pitch_deg", 2, now-1_000_000_000, 11), // 1s ago (latest)
		// roll_deg has no data -> stale
	}}
	d := Deps{Channels: newReg(t), Replay: rep}
	_, res, err := d.getLast(context.Background(), nil, getLastArgs{DeviceID: "rover-1"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]lastValue{}
	for _, c := range res.Channels {
		byName[c.Channel] = c
	}
	pitch, ok := byName["pitch_deg"]
	if !ok || pitch.Stale || pitch.Value == nil || *pitch.Value != 11 {
		t.Fatalf("pitch = %+v", pitch)
	}
	if pitch.AgeSeconds == nil || *pitch.AgeSeconds < 0.5 || *pitch.AgeSeconds > 5 {
		t.Errorf("pitch age = %v, want ~1s", pitch.AgeSeconds)
	}
	roll, ok := byName["roll_deg"]
	if !ok || !roll.Stale || roll.Value != nil {
		t.Fatalf("roll should be stale, got %+v", roll)
	}
}

func TestEdgeStatus(t *testing.T) {
	now := time.Now().UnixNano()
	rep := &fakeReplayer{frames: []stream.Delivered{
		f64Delivered("rover-1", "pitch_deg", 1, now-500_000_000, 1),
		f64Delivered("rover-1", "pitch_deg", 2, now-100_000_000, 2),
	}}
	stater := fakeStater{st: StreamState{Name: "TLM", Msgs: 2, Bytes: 128, FirstSeq: 1, LastSeq: 2, FirstTsNs: now - 500_000_000, LastTsNs: now - 100_000_000}}
	d := Deps{Channels: newReg(t), Replay: rep, Stream: stater, StartedAt: time.Now().Add(-30 * time.Second)}
	_, res, err := d.edgeStatus(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stream == nil || res.Stream.Msgs != 2 || res.Stream.Name != "TLM" {
		t.Fatalf("stream = %+v", res.Stream)
	}
	if res.UptimeSeconds < 25 || res.UptimeSeconds > 40 {
		t.Errorf("uptime = %v, want ~30", res.UptimeSeconds)
	}
	if len(res.Devices) != 1 || res.Devices[0].DeviceID != "rover-1" || res.Devices[0].ChannelCount != 2 {
		t.Fatalf("devices = %+v", res.Devices)
	}
	if res.Devices[0].LastSeenAgeSecond == nil || *res.Devices[0].LastSeenAgeSecond > 5 {
		t.Errorf("last-seen age = %v", res.Devices[0].LastSeenAgeSecond)
	}
}
