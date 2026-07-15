// Package hardware implements the operator-authored identity layer over
// telemetry devices (see proto/gantry/v1/hardware.proto). A device exists
// implicitly the moment it emits frames; a Hardware row adds a display name,
// notes, and the evolving JSON configs the console owns (3D visualization +
// panel defaults). It owns the CRUD service logic and its ConnectRPC handler.
// Metadata is persisted in the Bench SQLite store (core/go/benchdb) behind the
// same Store shape as experiments (SQLite now, Postgres later).
//
// ListHardware also reports which devices have been seen in live telemetry but
// have no Hardware row yet ("unconfigured"), so the console can offer one-click
// promotion. The set of seen devices is injected via the narrow DeviceLister
// interface (the channel registry satisfies it) so this package never depends
// on the registry directly.
package hardware

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// Validation errors. The ConnectRPC handler maps these to status codes.
var (
	// ErrInvalid is a bad request (e.g. empty/illegal device_id, oversized JSON).
	ErrInvalid = errors.New("invalid hardware request")
)

// MaxJSONBytes caps each opaque JSON config field (viz_config_json,
// panel_defaults_json). 256 KiB is generous for pose bindings + panel defaults
// while bounding a single row and the request body.
const MaxJSONBytes = 256 * 1024

// DeviceLister reports the device_ids currently seen in live telemetry. The
// channel registry satisfies this via a thin adapter in the server; injecting a
// narrow interface keeps this package free of a registry dependency and makes
// the "unconfigured devices" merge trivially testable.
type DeviceLister interface {
	// SeenDeviceIDs returns the set of device_ids observed in telemetry. Order
	// is not significant (Service sorts the merged result).
	SeenDeviceIDs() []string
}

// Service is the hardware CRUD engine. It validates requests, caps the opaque
// JSON fields, stamps timestamps, and computes the unconfigured-device set by
// subtracting configured rows from the injected DeviceLister. now is injectable
// so tests get deterministic timestamps.
type Service struct {
	store   *Store
	devices DeviceLister
	now     func() time.Time
}

// NewService builds a Service over an already-migrated *sql.DB and a
// DeviceLister (may be nil; then ListHardware reports no unconfigured devices).
func NewService(db *sql.DB, devices DeviceLister) *Service {
	return &Service{store: NewStore(db), devices: devices, now: time.Now}
}

// Store exposes the underlying store (parity with experiments; handy for tests
// and future read paths).
func (s *Service) Store() *Store { return s.store }

// Upsert creates or updates the Hardware row for hw.DeviceId. device_id must be
// a non-empty [A-Za-z0-9_-] token (same charset as video camera ids); the two
// JSON fields must each be within MaxJSONBytes. created_ns is preserved across
// updates; updated_ns is stamped now. The stored, canonical row is returned.
func (s *Service) Upsert(ctx context.Context, hw *gantryv1.Hardware) (*gantryv1.Hardware, error) {
	if hw == nil {
		return nil, fmt.Errorf("%w: hardware is required", ErrInvalid)
	}
	if !validDeviceID(hw.DeviceId) {
		return nil, fmt.Errorf("%w: device_id %q must be non-empty [A-Za-z0-9_-]", ErrInvalid, hw.DeviceId)
	}
	if len(hw.VizConfigJson) > MaxJSONBytes {
		return nil, fmt.Errorf("%w: viz_config_json is %d bytes (max %d)", ErrInvalid, len(hw.VizConfigJson), MaxJSONBytes)
	}
	if len(hw.PanelDefaultsJson) > MaxJSONBytes {
		return nil, fmt.Errorf("%w: panel_defaults_json is %d bytes (max %d)", ErrInvalid, len(hw.PanelDefaultsJson), MaxJSONBytes)
	}

	nowNs := uint64(s.now().UnixNano())
	row := &gantryv1.Hardware{
		DeviceId:          hw.DeviceId,
		DisplayName:       hw.DisplayName,
		Description:       hw.Description,
		Notes:             hw.Notes,
		VizConfigJson:     hw.VizConfigJson,
		PanelDefaultsJson: hw.PanelDefaultsJson,
		CreatedNs:         nowNs, // only applied on insert (ON CONFLICT keeps the original)
		UpdatedNs:         nowNs,
	}
	if err := s.store.Upsert(ctx, row); err != nil {
		return nil, err
	}
	// Re-read so created_ns reflects a prior insert when this was an update.
	return s.store.Get(ctx, hw.DeviceId)
}

// Get returns one hardware row by device_id (ErrNotFound if unknown).
func (s *Service) Get(ctx context.Context, deviceID string) (*gantryv1.Hardware, error) {
	return s.store.Get(ctx, deviceID)
}

// List returns all configured hardware rows plus the sorted set of device_ids
// seen in telemetry that have no row yet (promotable).
func (s *Service) List(ctx context.Context) (rows []*gantryv1.Hardware, unconfigured []string, err error) {
	rows, err = s.store.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	unconfigured = s.unconfiguredIDs(rows)
	return rows, unconfigured, nil
}

// Delete removes a hardware row (ErrNotFound if unknown).
func (s *Service) Delete(ctx context.Context, deviceID string) error {
	return s.store.Delete(ctx, deviceID)
}

// unconfiguredIDs computes (seen devices) minus (configured rows), sorted. A nil
// DeviceLister yields an empty set.
func (s *Service) unconfiguredIDs(rows []*gantryv1.Hardware) []string {
	if s.devices == nil {
		return nil
	}
	configured := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		configured[r.DeviceId] = struct{}{}
	}
	seenSet := make(map[string]struct{})
	for _, id := range s.devices.SeenDeviceIDs() {
		if id == "" {
			continue
		}
		if _, ok := configured[id]; ok {
			continue
		}
		seenSet[id] = struct{}{}
	}
	out := make([]string, 0, len(seenSet))
	for id := range seenSet {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// validDeviceID accepts non-empty [A-Za-z0-9_-]+ only, matching the video camera
// id charset. This keeps device ids safe as blob-key / path components elsewhere
// and consistent across the product.
func validDeviceID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}
