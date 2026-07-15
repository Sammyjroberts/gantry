package hardware_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sammyjroberts/gantry/core/go/benchdb"
	"github.com/Sammyjroberts/gantry/core/go/hardware"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// fakeDevices is an injectable DeviceLister for the unconfigured-merge tests.
type fakeDevices struct{ ids []string }

func (f *fakeDevices) SeenDeviceIDs() []string { return f.ids }

func newSvc(t *testing.T, devices hardware.DeviceLister) (*hardware.Service, *sql.DB) {
	t.Helper()
	db, err := benchdb.Open(context.Background(), filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatalf("benchdb.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return hardware.NewService(db, devices), db
}

// TestCRUD exercises upsert(create) → get → upsert(update) → list → delete.
func TestCRUD(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t, nil)

	// Create.
	created, err := svc.Upsert(ctx, &gantryv1.Hardware{
		DeviceId:      "rover-1",
		DisplayName:   "Rover One",
		Description:   "climb bot",
		Notes:         "bearings replaced",
		VizConfigJson: `{"v":1,"bindings":{}}`,
	})
	if err != nil {
		t.Fatalf("Upsert create: %v", err)
	}
	if created.CreatedNs == 0 || created.UpdatedNs == 0 {
		t.Fatalf("timestamps not stamped: %+v", created)
	}
	if created.DisplayName != "Rover One" || created.VizConfigJson != `{"v":1,"bindings":{}}` {
		t.Fatalf("create mismatch: %+v", created)
	}

	// Get round-trips.
	got, err := svc.Get(ctx, "rover-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DisplayName != "Rover One" || got.Description != "climb bot" || got.Notes != "bearings replaced" {
		t.Fatalf("Get mismatch: %+v", got)
	}

	// Update: change display name + viz config; created_ns must be preserved,
	// updated_ns must advance (>= created_ns).
	updated, err := svc.Upsert(ctx, &gantryv1.Hardware{
		DeviceId:      "rover-1",
		DisplayName:   "Rover One v2",
		VizConfigJson: `{"v":1,"bindings":{"pitch":{}}}`,
	})
	if err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	if updated.CreatedNs != created.CreatedNs {
		t.Fatalf("created_ns changed on update: %d -> %d", created.CreatedNs, updated.CreatedNs)
	}
	if updated.UpdatedNs < created.UpdatedNs {
		t.Fatalf("updated_ns went backwards: %d -> %d", created.UpdatedNs, updated.UpdatedNs)
	}
	if updated.DisplayName != "Rover One v2" {
		t.Fatalf("update not applied: %+v", updated)
	}

	// List returns exactly the one row, no unconfigured (nil lister).
	rows, unconfigured, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].DeviceId != "rover-1" {
		t.Fatalf("List rows = %+v", rows)
	}
	if len(unconfigured) != 0 {
		t.Fatalf("unconfigured = %v, want none", unconfigured)
	}

	// Delete, then Get fails.
	if err := svc.Delete(ctx, "rover-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, "rover-1"); !errors.Is(err, hardware.ErrNotFound) {
		t.Fatalf("Get after delete err = %v, want ErrNotFound", err)
	}
}

// TestValidation covers rejected requests and unknown-id errors.
func TestValidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t, nil)

	// Empty device_id.
	if _, err := svc.Upsert(ctx, &gantryv1.Hardware{DeviceId: ""}); !errors.Is(err, hardware.ErrInvalid) {
		t.Fatalf("empty device_id err = %v, want ErrInvalid", err)
	}
	// Illegal charset (dot / slash / space).
	for _, bad := range []string{"has space", "dot.dot", "slash/slash", "uni€ode"} {
		if _, err := svc.Upsert(ctx, &gantryv1.Hardware{DeviceId: bad}); !errors.Is(err, hardware.ErrInvalid) {
			t.Fatalf("device_id %q err = %v, want ErrInvalid", bad, err)
		}
	}
	// Oversized JSON fields.
	big := strings.Repeat("x", hardware.MaxJSONBytes+1)
	if _, err := svc.Upsert(ctx, &gantryv1.Hardware{DeviceId: "d1", VizConfigJson: big}); !errors.Is(err, hardware.ErrInvalid) {
		t.Fatalf("oversized viz_config err = %v, want ErrInvalid", err)
	}
	if _, err := svc.Upsert(ctx, &gantryv1.Hardware{DeviceId: "d1", PanelDefaultsJson: big}); !errors.Is(err, hardware.ErrInvalid) {
		t.Fatalf("oversized panel_defaults err = %v, want ErrInvalid", err)
	}
	// Exactly at the cap is allowed.
	atCap := strings.Repeat("x", hardware.MaxJSONBytes)
	if _, err := svc.Upsert(ctx, &gantryv1.Hardware{DeviceId: "d1", VizConfigJson: atCap}); err != nil {
		t.Fatalf("at-cap viz_config err = %v, want nil", err)
	}

	// Unknown ids.
	if _, err := svc.Get(ctx, "nope"); !errors.Is(err, hardware.ErrNotFound) {
		t.Fatalf("Get unknown err = %v, want ErrNotFound", err)
	}
	if err := svc.Delete(ctx, "nope"); !errors.Is(err, hardware.ErrNotFound) {
		t.Fatalf("Delete unknown err = %v, want ErrNotFound", err)
	}
}

// TestUnconfiguredMerge verifies ListHardware reports seen-minus-configured.
func TestUnconfiguredMerge(t *testing.T) {
	ctx := context.Background()
	// Registry has seen four devices; two will be configured.
	dev := &fakeDevices{ids: []string{"rover-1", "rover-2", "arm-7", "", "rover-1"}}
	svc, _ := newSvc(t, dev)

	// Configure rover-1 only.
	if _, err := svc.Upsert(ctx, &gantryv1.Hardware{DeviceId: "rover-1", DisplayName: "R1"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	rows, unconfigured, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].DeviceId != "rover-1" {
		t.Fatalf("rows = %+v", rows)
	}
	// Seen minus configured, de-duped, empty filtered, sorted: arm-7, rover-2.
	want := []string{"arm-7", "rover-2"}
	if len(unconfigured) != len(want) {
		t.Fatalf("unconfigured = %v, want %v", unconfigured, want)
	}
	for i := range want {
		if unconfigured[i] != want[i] {
			t.Fatalf("unconfigured = %v, want %v", unconfigured, want)
		}
	}

	// After configuring arm-7 it drops out of the unconfigured set.
	if _, err := svc.Upsert(ctx, &gantryv1.Hardware{DeviceId: "arm-7"}); err != nil {
		t.Fatalf("Upsert arm-7: %v", err)
	}
	_, unconfigured2, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List 2: %v", err)
	}
	if len(unconfigured2) != 1 || unconfigured2[0] != "rover-2" {
		t.Fatalf("unconfigured2 = %v, want [rover-2]", unconfigured2)
	}
}

// TestNilListerNoUnconfigured proves a nil DeviceLister yields no unconfigured
// devices (Service must not panic).
func TestNilListerNoUnconfigured(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t, nil)
	if _, err := svc.Upsert(ctx, &gantryv1.Hardware{DeviceId: "d1"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	_, unconfigured, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(unconfigured) != 0 {
		t.Fatalf("unconfigured = %v, want none", unconfigured)
	}
}
