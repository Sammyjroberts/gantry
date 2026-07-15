package stream

import (
	"context"
	"testing"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

func mkBatch(dev string, seq uint64, ch string, v float64) *gantryv1.FrameBatch {
	return &gantryv1.FrameBatch{DeviceId: dev, Sequence: seq, Frames: []*gantryv1.Frame{
		{Channel: ch, TimestampNs: uint64(time.Now().UnixNano()), Value: &gantryv1.Value{Kind: &gantryv1.Value_F64{F64: v}}},
	}}
}

func TestBusReplayThenLive(t *testing.T) {
	bus, err := NewEmbedded(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	ctx := context.Background()
	if err := bus.EnsureStream(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.Publish(ctx, mkBatch("d", 1, "a", 1)); err != nil {
		t.Fatal(err)
	}

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := bus.Subscribe(subCtx, SubscribeOptions{DeviceID: "d", ReplaySeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	// replayed
	select {
	case d := <-ch:
		if d.Frame.GetValue().GetF64() != 1 {
			t.Fatalf("replay value = %v", d.Frame.GetValue().GetF64())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no replayed frame")
	}
	// live
	if _, err := bus.Publish(ctx, mkBatch("d", 2, "a", 2)); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-ch:
		if d.Frame.GetValue().GetF64() != 2 {
			t.Fatalf("live value = %v", d.Frame.GetValue().GetF64())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no live frame")
	}
}

func TestBusLiveOnly(t *testing.T) {
	bus, err := NewEmbedded(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	ctx := context.Background()
	if err := bus.EnsureStream(ctx); err != nil {
		t.Fatal(err)
	}
	// historical frame that must NOT be replayed
	if _, err := bus.Publish(ctx, mkBatch("d", 1, "a", 7)); err != nil {
		t.Fatal(err)
	}

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := bus.Subscribe(subCtx, SubscribeOptions{DeviceID: "d", ReplaySeconds: 0})
	if err != nil {
		t.Fatal(err)
	}

	// publish live frames until we see one (bounded)
	deadline := time.After(10 * time.Second)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-subCtx.Done():
				return
			default:
			}
			_, _ = bus.Publish(ctx, mkBatch("d", uint64(100+i), "a", 42))
			time.Sleep(50 * time.Millisecond)
		}
	}()
	for {
		select {
		case d := <-ch:
			v := d.Frame.GetValue().GetF64()
			if v == 7 {
				t.Fatal("live-only replayed historical frame")
			}
			if v == 42 {
				return
			}
		case <-deadline:
			t.Fatal("no live frame within deadline")
		}
	}
}
