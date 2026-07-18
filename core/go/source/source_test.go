package source

import (
	"context"
	"errors"
	"testing"

	"github.com/Sammyjroberts/gantry/core/go/benchdb"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

func newSvc(t *testing.T) *Service {
	t.Helper()
	ctx := context.Background()
	db, err := benchdb.Open(ctx, t.TempDir()+"/bench.db")
	if err != nil {
		t.Fatalf("benchdb.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewService(db)
}

func TestUpsertGeneratesIDAndStamps(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	got, err := svc.Upsert(ctx, &gantryv1.Source{
		Type: "foxglove", Name: "lab bench", Url: "ws://127.0.0.1:8765", MappingJson: `{"profile":"lerobot"}`, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got.Id == "" {
		t.Error("expected a generated id")
	}
	if got.CreatedNs == 0 || got.UpdatedNs == 0 {
		t.Errorf("timestamps not stamped: %+v", got)
	}
	if !got.Enabled {
		t.Error("enabled not persisted")
	}
}

func TestUpsertDefaultsType(t *testing.T) {
	svc := newSvc(t)
	got, err := svc.Upsert(context.Background(), &gantryv1.Source{Url: "ws://host:1", MappingJson: ""})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got.Type != "foxglove" {
		t.Errorf("type = %q, want foxglove (defaulted)", got.Type)
	}
}

func TestUpsertPreservesCreatedNs(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	first, err := svc.Upsert(ctx, &gantryv1.Source{Type: "foxglove", Url: "ws://host:1"})
	if err != nil {
		t.Fatalf("Upsert 1: %v", err)
	}
	second, err := svc.Upsert(ctx, &gantryv1.Source{Id: first.Id, Type: "foxglove", Url: "ws://host:2", Name: "renamed"})
	if err != nil {
		t.Fatalf("Upsert 2: %v", err)
	}
	if second.CreatedNs != first.CreatedNs {
		t.Errorf("created_ns changed on update: %d -> %d", first.CreatedNs, second.CreatedNs)
	}
	if second.Url != "ws://host:2" || second.Name != "renamed" {
		t.Errorf("update did not apply: %+v", second)
	}
}

func TestUpsertValidation(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	cases := []struct {
		name string
		src  *gantryv1.Source
	}{
		{"bad type", &gantryv1.Source{Type: "mqtt", Url: "ws://host:1"}},
		{"bad url scheme", &gantryv1.Source{Type: "foxglove", Url: "http://host:1"}},
		{"empty url", &gantryv1.Source{Type: "foxglove", Url: ""}},
		{"bad mapping json", &gantryv1.Source{Type: "foxglove", Url: "ws://host:1", MappingJson: "{not json"}},
		{"unknown profile", &gantryv1.Source{Type: "foxglove", Url: "ws://host:1", MappingJson: `{"profile":"nope"}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Upsert(ctx, tc.src)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

func TestUpsertAcceptsWssAndEmptyMapping(t *testing.T) {
	svc := newSvc(t)
	if _, err := svc.Upsert(context.Background(), &gantryv1.Source{Type: "foxglove", Url: "wss://remote:443/ws"}); err != nil {
		t.Fatalf("wss + empty mapping should be valid: %v", err)
	}
}

func TestListAndDelete(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	a, _ := svc.Upsert(ctx, &gantryv1.Source{Type: "foxglove", Name: "a", Url: "ws://h:1"})
	svc.Upsert(ctx, &gantryv1.Source{Type: "foxglove", Name: "b", Url: "ws://h:2"})

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 sources, got %d", len(list))
	}
	// Ordered by name.
	if list[0].Name != "a" || list[1].Name != "b" {
		t.Errorf("unexpected order: %q, %q", list[0].Name, list[1].Name)
	}

	if err := svc.Delete(ctx, a.Id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete(ctx, a.Id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: want ErrNotFound, got %v", err)
	}
	list2, _ := svc.List(ctx)
	if len(list2) != 1 {
		t.Fatalf("want 1 source after delete, got %d", len(list2))
	}
}

func TestGetNotFound(t *testing.T) {
	svc := newSvc(t)
	if _, err := svc.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}
