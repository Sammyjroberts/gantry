package workspace_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sammyjroberts/gantry/core/go/benchdb"
	"github.com/Sammyjroberts/gantry/core/go/workspace"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

func newSvc(t *testing.T) (*workspace.Service, *sql.DB) {
	t.Helper()
	db, err := benchdb.Open(context.Background(), filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatalf("benchdb.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return workspace.NewService(db), db
}

// TestCRUD exercises create (empty id → generated) → get → update → delete.
func TestCRUD(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	// Create with empty id: an id must be generated.
	created, err := svc.Upsert(ctx, &gantryv1.Workspace{
		Name:       "Drive Dashboard",
		LayoutJson: `{"v":1,"panels":[]}`,
	})
	if err != nil {
		t.Fatalf("Upsert create: %v", err)
	}
	if created.Id == "" {
		t.Fatalf("create did not generate an id: %+v", created)
	}
	if created.CreatedNs == 0 || created.UpdatedNs == 0 {
		t.Fatalf("timestamps not stamped: %+v", created)
	}
	if created.Name != "Drive Dashboard" || created.LayoutJson != `{"v":1,"panels":[]}` {
		t.Fatalf("create mismatch: %+v", created)
	}

	// Get round-trips the full row including layout_json.
	got, err := svc.Get(ctx, created.Id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Drive Dashboard" || got.LayoutJson != `{"v":1,"panels":[]}` {
		t.Fatalf("Get mismatch: %+v", got)
	}

	// Update: same id, new name + layout. created_ns preserved, updated_ns advances.
	updated, err := svc.Upsert(ctx, &gantryv1.Workspace{
		Id:         created.Id,
		Name:       "Drive Dashboard v2",
		LayoutJson: `{"v":1,"panels":[{"id":"p1"}]}`,
	})
	if err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	if updated.Id != created.Id {
		t.Fatalf("id changed on update: %s -> %s", created.Id, updated.Id)
	}
	if updated.CreatedNs != created.CreatedNs {
		t.Fatalf("created_ns changed on update: %d -> %d", created.CreatedNs, updated.CreatedNs)
	}
	if updated.UpdatedNs < created.UpdatedNs {
		t.Fatalf("updated_ns went backwards: %d -> %d", created.UpdatedNs, updated.UpdatedNs)
	}
	if updated.Name != "Drive Dashboard v2" || updated.LayoutJson != `{"v":1,"panels":[{"id":"p1"}]}` {
		t.Fatalf("update not applied: %+v", updated)
	}

	// Delete, then Get fails.
	if err := svc.Delete(ctx, created.Id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, created.Id); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("Get after delete err = %v, want ErrNotFound", err)
	}
}

// TestValidation covers rejected requests and unknown-id errors.
func TestValidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	// Empty name.
	if _, err := svc.Upsert(ctx, &gantryv1.Workspace{Name: ""}); !errors.Is(err, workspace.ErrInvalid) {
		t.Fatalf("empty name err = %v, want ErrInvalid", err)
	}
	// Whitespace-only name (trimmed to empty).
	if _, err := svc.Upsert(ctx, &gantryv1.Workspace{Name: "   "}); !errors.Is(err, workspace.ErrInvalid) {
		t.Fatalf("whitespace name err = %v, want ErrInvalid", err)
	}
	// Name is stored trimmed.
	ws, err := svc.Upsert(ctx, &gantryv1.Workspace{Name: "  Padded  "})
	if err != nil {
		t.Fatalf("Upsert padded name: %v", err)
	}
	if ws.Name != "Padded" {
		t.Fatalf("name not trimmed: %q", ws.Name)
	}
	// Oversized layout_json.
	big := strings.Repeat("x", workspace.MaxLayoutBytes+1)
	if _, err := svc.Upsert(ctx, &gantryv1.Workspace{Name: "big", LayoutJson: big}); !errors.Is(err, workspace.ErrInvalid) {
		t.Fatalf("oversized layout err = %v, want ErrInvalid", err)
	}
	// Exactly at the cap is allowed.
	atCap := strings.Repeat("x", workspace.MaxLayoutBytes)
	if _, err := svc.Upsert(ctx, &gantryv1.Workspace{Name: "atcap", LayoutJson: atCap}); err != nil {
		t.Fatalf("at-cap layout err = %v, want nil", err)
	}

	// Unknown ids.
	if _, err := svc.Get(ctx, "nope"); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("Get unknown err = %v, want ErrNotFound", err)
	}
	if err := svc.Delete(ctx, "nope"); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("Delete unknown err = %v, want ErrNotFound", err)
	}
}

// TestListOmitsLayout proves List returns name + timestamps but NOT layout_json
// (the light list view), while Get returns the full document.
func TestListOmitsLayout(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	created, err := svc.Upsert(ctx, &gantryv1.Workspace{
		Name:       "Alpha",
		LayoutJson: `{"v":1,"panels":[{"id":"heavy"}]}`,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	rows, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("List rows = %d, want 1", len(rows))
	}
	if rows[0].Id != created.Id || rows[0].Name != "Alpha" {
		t.Fatalf("List row mismatch: %+v", rows[0])
	}
	if rows[0].LayoutJson != "" {
		t.Fatalf("List must omit layout_json, got %q", rows[0].LayoutJson)
	}
	if rows[0].CreatedNs == 0 || rows[0].UpdatedNs == 0 {
		t.Fatalf("List row missing timestamps: %+v", rows[0])
	}

	// Get still returns the full layout.
	got, err := svc.Get(ctx, created.Id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LayoutJson != `{"v":1,"panels":[{"id":"heavy"}]}` {
		t.Fatalf("Get layout mismatch: %q", got.LayoutJson)
	}
}

// TestListSorted verifies List orders by name.
func TestListSorted(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	for _, n := range []string{"Zephyr", "Alpha", "Mike"} {
		if _, err := svc.Upsert(ctx, &gantryv1.Workspace{Name: n}); err != nil {
			t.Fatalf("Upsert %q: %v", n, err)
		}
	}
	rows, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"Alpha", "Mike", "Zephyr"}
	if len(rows) != len(want) {
		t.Fatalf("List rows = %d, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i].Name != w {
			t.Fatalf("List[%d].Name = %q, want %q", i, rows[i].Name, w)
		}
	}
}
