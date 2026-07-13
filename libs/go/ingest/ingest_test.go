package ingest

import (
	"context"
	"errors"
	"testing"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/libs/go/registry"
)

type fakePub struct {
	batches []*gantryv1.FrameBatch
	seq     uint64
	err     error
}

func (f *fakePub) Publish(_ context.Context, b *gantryv1.FrameBatch) (uint64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.batches = append(f.batches, b)
	f.seq++
	return f.seq, nil
}

func goodFrame(ch string) *gantryv1.Frame {
	return &gantryv1.Frame{Channel: ch, TimestampNs: 42, Value: &gantryv1.Value{Kind: &gantryv1.Value_F64{F64: 1}}}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		batch   *gantryv1.FrameBatch
		wantErr bool
	}{
		{"nil", nil, true},
		{"empty device", &gantryv1.FrameBatch{Frames: []*gantryv1.Frame{goodFrame("a")}}, true},
		{"empty channel", &gantryv1.FrameBatch{DeviceId: "d", Frames: []*gantryv1.Frame{{TimestampNs: 1, Value: &gantryv1.Value{}}}}, true},
		{"zero ts", &gantryv1.FrameBatch{DeviceId: "d", Frames: []*gantryv1.Frame{{Channel: "a", Value: &gantryv1.Value{}}}}, true},
		{"nil value", &gantryv1.FrameBatch{DeviceId: "d", Frames: []*gantryv1.Frame{{Channel: "a", TimestampNs: 1}}}, true},
		{"nil frame", &gantryv1.FrameBatch{DeviceId: "d", Frames: []*gantryv1.Frame{nil}}, true},
		{"ok", &gantryv1.FrameBatch{DeviceId: "d", Sequence: 1, Frames: []*gantryv1.Frame{goodFrame("a")}}, false},
		{"ok empty frames", &gantryv1.FrameBatch{DeviceId: "d", Sequence: 1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(c.batch)
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate err=%v, wantErr=%v", err, c.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidBatch) {
				t.Fatalf("err not wrapping ErrInvalidBatch: %v", err)
			}
		})
	}
}

func TestPublishBatchAcksSequence(t *testing.T) {
	pub := &fakePub{}
	e := New(pub, registry.New())
	batch := &gantryv1.FrameBatch{DeviceId: "d", Sequence: 7, Frames: []*gantryv1.Frame{goodFrame("speed")}}
	acked, err := e.PublishBatch(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if acked != 7 {
		t.Fatalf("acked = %d, want 7 (per-device sequence)", acked)
	}
	if len(pub.batches) != 1 {
		t.Fatalf("published %d batches, want 1", len(pub.batches))
	}
	// Auto-registration should have happened.
	list := e.Registry().List("d")
	if len(list) != 1 || len(list[0].Channels) != 1 || list[0].Channels[0].Name != "speed" {
		t.Fatalf("channel not auto-registered: %+v", list)
	}
}

func TestPublishBatchDeviceIDBatchWins(t *testing.T) {
	pub := &fakePub{}
	e := New(pub, registry.New())
	// Frames arrive with a device_id that disagrees with the batch (and one
	// empty). The batch's device_id is authoritative: both must be overwritten,
	// no error.
	f1 := goodFrame("a")
	f1.DeviceId = "wrong-device"
	f2 := goodFrame("b") // empty device_id
	batch := &gantryv1.FrameBatch{DeviceId: "rover-1", Sequence: 1, Frames: []*gantryv1.Frame{f1, f2}}
	if _, err := e.PublishBatch(context.Background(), batch); err != nil {
		t.Fatalf("PublishBatch: %v", err)
	}
	if len(pub.batches) != 1 {
		t.Fatalf("published %d batches, want 1", len(pub.batches))
	}
	for i, f := range pub.batches[0].Frames {
		if f.DeviceId != "rover-1" {
			t.Errorf("frame %d device_id = %q, want rover-1 (batch wins)", i, f.DeviceId)
		}
	}
}

func TestPublishBatchRejectsInvalid(t *testing.T) {
	pub := &fakePub{}
	e := New(pub, registry.New())
	_, err := e.PublishBatch(context.Background(), &gantryv1.FrameBatch{Sequence: 1})
	if !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("want ErrInvalidBatch, got %v", err)
	}
	if len(pub.batches) != 0 {
		t.Fatal("invalid batch should not have been published")
	}
}

func TestPublishBatchPublishErrorNoAck(t *testing.T) {
	pub := &fakePub{err: errors.New("boom")}
	e := New(pub, registry.New())
	_, err := e.PublishBatch(context.Background(), &gantryv1.FrameBatch{DeviceId: "d", Sequence: 1, Frames: []*gantryv1.Frame{goodFrame("a")}})
	if err == nil {
		t.Fatal("expected publish error to propagate (no ack on non-durable write)")
	}
}
