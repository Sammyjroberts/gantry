package registry

import (
	"fmt"
	"sync"
	"testing"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

func f64(c string, v float64) *gantryv1.Frame {
	return &gantryv1.Frame{Channel: c, TimestampNs: 1, Value: &gantryv1.Value{Kind: &gantryv1.Value_F64{F64: v}}}
}
func i64(c string, v int64) *gantryv1.Frame {
	return &gantryv1.Frame{Channel: c, TimestampNs: 1, Value: &gantryv1.Value{Kind: &gantryv1.Value_I64{I64: v}}}
}

func TestInferKind(t *testing.T) {
	cases := []struct {
		v    *gantryv1.Value
		want gantryv1.ValueKind
	}{
		{&gantryv1.Value{Kind: &gantryv1.Value_F64{}}, gantryv1.ValueKind_VALUE_KIND_F64},
		{&gantryv1.Value{Kind: &gantryv1.Value_I64{}}, gantryv1.ValueKind_VALUE_KIND_I64},
		{&gantryv1.Value{Kind: &gantryv1.Value_Flag{}}, gantryv1.ValueKind_VALUE_KIND_BOOL},
		{&gantryv1.Value{Kind: &gantryv1.Value_Text{}}, gantryv1.ValueKind_VALUE_KIND_TEXT},
		{&gantryv1.Value{Kind: &gantryv1.Value_Raw{}}, gantryv1.ValueKind_VALUE_KIND_RAW},
		{nil, gantryv1.ValueKind_VALUE_KIND_UNSPECIFIED},
	}
	for _, c := range cases {
		if got := InferKind(c.v); got != c.want {
			t.Errorf("InferKind(%v) = %v, want %v", c.v, got, c.want)
		}
	}
}

func TestAutoRegisterInfersKind(t *testing.T) {
	r := New()
	r.ObserveBatch(&gantryv1.FrameBatch{
		DeviceId: "dev1",
		Frames:   []*gantryv1.Frame{f64("speed", 1.2), i64("count", 3)},
	})
	got := r.List("dev1")
	if len(got) != 1 || got[0].DeviceId != "dev1" {
		t.Fatalf("unexpected list: %+v", got)
	}
	kinds := map[string]gantryv1.ValueKind{}
	for _, ci := range got[0].Channels {
		kinds[ci.Name] = ci.Kind
	}
	if kinds["speed"] != gantryv1.ValueKind_VALUE_KIND_F64 {
		t.Errorf("speed kind = %v", kinds["speed"])
	}
	if kinds["count"] != gantryv1.ValueKind_VALUE_KIND_I64 {
		t.Errorf("count kind = %v", kinds["count"])
	}
}

func TestMetadataMergeExplicitWins(t *testing.T) {
	r := New()
	// Auto-register with inferred kind, no unit.
	r.ObserveBatch(&gantryv1.FrameBatch{DeviceId: "dev1", Frames: []*gantryv1.Frame{f64("speed", 1)}})
	// Explicit metadata adds unit + description; kind stays F64.
	r.Register("dev1", []*gantryv1.ChannelInfo{{
		Name: "speed", Kind: gantryv1.ValueKind_VALUE_KIND_F64, Unit: "m/s", Description: "ground speed",
	}})
	ci := r.List("dev1")[0].Channels[0]
	if ci.Unit != "m/s" || ci.Description != "ground speed" {
		t.Fatalf("merge lost metadata: %+v", ci)
	}

	// A later observation of the same channel must not clobber explicit metadata.
	r.ObserveBatch(&gantryv1.FrameBatch{DeviceId: "dev1", Frames: []*gantryv1.Frame{f64("speed", 2)}})
	ci = r.List("dev1")[0].Channels[0]
	if ci.Unit != "m/s" {
		t.Fatalf("observation clobbered unit: %+v", ci)
	}
}

func TestRegisterFillsThenObserveKeeps(t *testing.T) {
	r := New()
	// Explicit without kind, then observation fills the kind in.
	r.Register("dev1", []*gantryv1.ChannelInfo{{Name: "speed", Unit: "m/s"}})
	r.ObserveBatch(&gantryv1.FrameBatch{DeviceId: "dev1", Frames: []*gantryv1.Frame{f64("speed", 1)}})
	ci := r.List("dev1")[0].Channels[0]
	if ci.Kind != gantryv1.ValueKind_VALUE_KIND_F64 || ci.Unit != "m/s" {
		t.Fatalf("expected kind filled + unit kept: %+v", ci)
	}
}

func TestConcurrentAutoRegistration(t *testing.T) {
	r := New()
	const goroutines = 16
	const perG = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				ch := fmt.Sprintf("chan_%d", i)
				r.ObserveBatch(&gantryv1.FrameBatch{
					DeviceId: fmt.Sprintf("dev_%d", g%4),
					Frames:   []*gantryv1.Frame{f64(ch, float64(i))},
				})
			}
		}(g)
	}
	// Concurrent readers to shake out races under -race.
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				_ = r.List("")
			}
		}()
	}
	wg.Wait()

	all := r.List("")
	if len(all) != 4 {
		t.Fatalf("want 4 devices, got %d", len(all))
	}
	for _, d := range all {
		if len(d.Channels) != perG {
			t.Errorf("device %s: want %d channels, got %d", d.DeviceId, perG, len(d.Channels))
		}
	}
}
