package registry

import (
	"sync"
	"testing"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// TestConcurrentPacketScopedOps hammers the registry with concurrent
// packet-scoped writes (the same param name under two different packets),
// concurrent explicit Register calls, and concurrent readers. Its value is
// under `go test -race` (now wired in CI): the (packet, name) keying and the
// register/observe merge must stay race-free while two packets legitimately
// carry a param with the same name but different kinds.
//
// This complements TestConcurrentAutoRegistration (distinct names, no packets)
// and TestPacketScopedKindConflict (sequential) by exercising the packet-scoped
// merge path under contention.
func TestConcurrentPacketScopedOps(t *testing.T) {
	r := New()
	const goroutines = 16
	const iters = 100
	const devices = 4

	var wg sync.WaitGroup

	// Writers: every goroutine observes BOTH imu.temp (F64) and power.temp (I64)
	// on its device, so the same (device, "temp") name is contended across two
	// packets simultaneously from many goroutines. Each device is written by
	// several goroutines at once (goroutines >> devices).
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			dev := deviceName(g % devices)
			for i := 0; i < iters; i++ {
				r.ObserveBatch(&gantryv1.FrameBatch{
					DeviceId: dev,
					Frames: []*gantryv1.Frame{
						f64p("imu", "temp", 36.6),
						i64p("power", "temp", 42),
					},
				})
			}
		}(g)
	}

	// Explicit registrations racing the observations (metadata merge path).
	for g := 0; g < devices; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			dev := deviceName(g)
			for i := 0; i < iters; i++ {
				r.Register(dev, []*gantryv1.ChannelInfo{
					{Name: "temp", Packet: "imu", Kind: gantryv1.ValueKind_VALUE_KIND_F64, Unit: "degC"},
				})
			}
		}(g)
	}

	// Readers to shake out map/read races.
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				for _, d := range r.List("") {
					_ = d.Channels
				}
			}
		}()
	}

	wg.Wait()

	// Final invariant: every device carries exactly two distinct (packet, name)
	// channels for "temp", with the packet-scoped kinds intact and the explicit
	// unit landing only on imu.temp.
	for d := 0; d < devices; d++ {
		dev := deviceName(d)
		list := r.List(dev)
		if len(list) != 1 {
			t.Fatalf("device %s: want 1 device entry, got %d", dev, len(list))
		}
		byKey := map[chanKey]*gantryv1.ChannelInfo{}
		for _, ci := range list[0].Channels {
			byKey[chanKey{packet: ci.Packet, name: ci.Name}] = ci
		}
		if len(byKey) != 2 {
			t.Fatalf("device %s: want 2 (packet,name) channels, got %d: %+v", dev, len(byKey), byKey)
		}
		imu := byKey[chanKey{"imu", "temp"}]
		pwr := byKey[chanKey{"power", "temp"}]
		if imu == nil || imu.Kind != gantryv1.ValueKind_VALUE_KIND_F64 {
			t.Fatalf("device %s: imu.temp missing or wrong kind: %+v", dev, imu)
		}
		if imu.Unit != "degC" {
			t.Fatalf("device %s: imu.temp lost explicit unit: %+v", dev, imu)
		}
		if pwr == nil || pwr.Kind != gantryv1.ValueKind_VALUE_KIND_I64 {
			t.Fatalf("device %s: power.temp missing or wrong kind: %+v", dev, pwr)
		}
		if pwr.Unit != "" {
			t.Fatalf("device %s: power.temp unit = %q, want empty (untouched)", dev, pwr.Unit)
		}
	}
}

func deviceName(i int) string {
	return "dev_" + string(rune('a'+i))
}
