// Package registry is a concurrency-safe channel registry: device -> channels,
// where a channel is identified by (packet, name) so distinct packets may carry
// same-named params with different kinds. It is populated two ways, merged:
// explicit RegisterChannels metadata, and auto-registration of previously unseen
// channels observed in ingested frames (kind inferred from the value, packet
// carried from the frame). This in-memory implementation is the milestone-2
// stand-in for the SQLite-backed registry that lands later.
package registry

import (
	"sort"
	"sync"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"google.golang.org/protobuf/proto"
)

// chanKey identifies a channel within a device by (packet, name). Packets are
// first-class (telemetry.proto): two packets may each expose a param of the
// same name with different kinds/units, so the packet is part of the identity —
// keying on name alone would collide them.
type chanKey struct {
	packet string
	name   string
}

// Registry holds channel metadata per device.
type Registry struct {
	mu sync.RWMutex
	// device id -> (packet, name) -> info
	devices map[string]map[chanKey]*gantryv1.ChannelInfo
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{devices: make(map[string]map[chanKey]*gantryv1.ChannelInfo)}
}

// Register merges explicit channel metadata for a device. Explicit fields win:
// a provided kind/unit/description overwrites what auto-registration inferred,
// but empty provided fields do not clobber existing values.
func (r *Registry) Register(deviceID string, channels []*gantryv1.ChannelInfo) {
	if deviceID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	dev := r.devices[deviceID]
	if dev == nil {
		dev = make(map[chanKey]*gantryv1.ChannelInfo)
		r.devices[deviceID] = dev
	}
	for _, ci := range channels {
		if ci == nil || ci.Name == "" {
			continue
		}
		key := chanKey{packet: ci.Packet, name: ci.Name}
		existing := dev[key]
		if existing == nil {
			dev[key] = proto.Clone(ci).(*gantryv1.ChannelInfo)
			continue
		}
		if ci.Kind != gantryv1.ValueKind_VALUE_KIND_UNSPECIFIED {
			existing.Kind = ci.Kind
		}
		if ci.Unit != "" {
			existing.Unit = ci.Unit
		}
		if ci.Description != "" {
			existing.Description = ci.Description
		}
	}
}

// ObserveBatch auto-registers any channel in the batch not already known. Known
// channels are left untouched, except that an inferred kind fills in a channel
// previously registered with an unspecified kind.
func (r *Registry) ObserveBatch(batch *gantryv1.FrameBatch) {
	if batch == nil || batch.DeviceId == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	dev := r.devices[batch.DeviceId]
	if dev == nil {
		dev = make(map[chanKey]*gantryv1.ChannelInfo)
		r.devices[batch.DeviceId] = dev
	}
	for _, f := range batch.Frames {
		if f == nil || f.Channel == "" {
			continue
		}
		kind := InferKind(f.Value)
		key := chanKey{packet: f.Packet, name: f.Channel}
		existing := dev[key]
		if existing == nil {
			dev[key] = &gantryv1.ChannelInfo{Name: f.Channel, Kind: kind, Packet: f.Packet}
			continue
		}
		if existing.Kind == gantryv1.ValueKind_VALUE_KIND_UNSPECIFIED {
			existing.Kind = kind
		}
	}
}

// List returns known channels. If deviceID is empty, all devices are returned.
// Results are sorted (device, then channel) for deterministic output.
func (r *Registry) List(deviceID string) []*gantryv1.DeviceChannels {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var deviceIDs []string
	if deviceID != "" {
		if _, ok := r.devices[deviceID]; ok {
			deviceIDs = []string{deviceID}
		}
	} else {
		for id := range r.devices {
			deviceIDs = append(deviceIDs, id)
		}
		sort.Strings(deviceIDs)
	}

	out := make([]*gantryv1.DeviceChannels, 0, len(deviceIDs))
	for _, id := range deviceIDs {
		chans := r.devices[id]
		keys := make([]chanKey, 0, len(chans))
		for k := range chans {
			keys = append(keys, k)
		}
		// Deterministic order: by packet, then channel name.
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].packet != keys[j].packet {
				return keys[i].packet < keys[j].packet
			}
			return keys[i].name < keys[j].name
		})
		infos := make([]*gantryv1.ChannelInfo, 0, len(keys))
		for _, k := range keys {
			infos = append(infos, proto.Clone(chans[k]).(*gantryv1.ChannelInfo))
		}
		out = append(out, &gantryv1.DeviceChannels{DeviceId: id, Channels: infos})
	}
	return out
}

// InferKind maps a telemetry Value's oneof arm to its ValueKind.
func InferKind(v *gantryv1.Value) gantryv1.ValueKind {
	if v == nil {
		return gantryv1.ValueKind_VALUE_KIND_UNSPECIFIED
	}
	switch v.Kind.(type) {
	case *gantryv1.Value_F64:
		return gantryv1.ValueKind_VALUE_KIND_F64
	case *gantryv1.Value_I64:
		return gantryv1.ValueKind_VALUE_KIND_I64
	case *gantryv1.Value_Flag:
		return gantryv1.ValueKind_VALUE_KIND_BOOL
	case *gantryv1.Value_Text:
		return gantryv1.ValueKind_VALUE_KIND_TEXT
	case *gantryv1.Value_Raw:
		return gantryv1.ValueKind_VALUE_KIND_RAW
	default:
		return gantryv1.ValueKind_VALUE_KIND_UNSPECIFIED
	}
}
