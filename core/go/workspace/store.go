package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// ErrNotFound is returned when a workspace id does not exist.
var ErrNotFound = errors.New("workspace not found")

// Store is the persistence layer for workspaces over the Bench SQLite database
// (core/go/benchdb). The same SQL runs on core Postgres — only the driver
// differs. It maps rows to and from the proto Workspace message and owns no
// policy: all validation, id generation, JSON-size capping, and time defaulting
// lives in Service.
type Store struct {
	db *sql.DB
}

// NewStore builds a Store over an already-migrated *sql.DB.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Upsert creates or updates a workspace row keyed by id. created_ns is set only
// on insert (ON CONFLICT preserves the original), while every other column —
// including updated_ns — is overwritten from ws. Callers pass created_ns and
// updated_ns already stamped (see Service).
func (s *Store) Upsert(ctx context.Context, ws *gantryv1.Workspace) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, layout_json, created_ns, updated_ns)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name        = excluded.name,
		   layout_json = excluded.layout_json,
		   updated_ns  = excluded.updated_ns`,
		ws.Id, ws.Name, ws.LayoutJson, int64(ws.CreatedNs), int64(ws.UpdatedNs))
	if err != nil {
		return fmt.Errorf("workspace: upsert: %w", err)
	}
	return nil
}

// Get returns one workspace row by id (including layout_json), or ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (*gantryv1.Workspace, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, layout_json, created_ns, updated_ns
		 FROM workspaces WHERE id = ?`, id)
	ws, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("workspace: get: %w", err)
	}
	return ws, nil
}

// List returns all workspace rows ordered by name then id (a stable,
// human-friendly order for the console). layout_json is deliberately omitted —
// the list view only needs name + timestamps; callers fetch a single workspace
// to load its layout (see proto/gantry/v1/workspace.proto). An empty result is
// not an error.
func (s *Store) List(ctx context.Context) ([]*gantryv1.Workspace, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, created_ns, updated_ns
		 FROM workspaces ORDER BY name ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("workspace: list: %w", err)
	}
	defer rows.Close()

	var out []*gantryv1.Workspace
	for rows.Next() {
		var (
			ws                   gantryv1.Workspace
			createdNs, updatedNs int64
		)
		if err := rows.Scan(&ws.Id, &ws.Name, &createdNs, &updatedNs); err != nil {
			return nil, fmt.Errorf("workspace: list scan: %w", err)
		}
		ws.CreatedNs = uint64(createdNs)
		ws.UpdatedNs = uint64(updatedNs)
		out = append(out, &ws)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workspace: list rows: %w", err)
	}
	return out, nil
}

// Delete removes a workspace row by id. Returns ErrNotFound if no row matched.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("workspace: delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("workspace: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scan reads one full workspace row (with layout_json). created_ns/updated_ns
// are stored as signed INTEGER (SQLite has no unsigned type) and re-widened to
// the proto's fixed64.
func scan(sc scanner) (*gantryv1.Workspace, error) {
	var (
		ws                   gantryv1.Workspace
		createdNs, updatedNs int64
	)
	if err := sc.Scan(&ws.Id, &ws.Name, &ws.LayoutJson, &createdNs, &updatedNs); err != nil {
		return nil, err
	}
	ws.CreatedNs = uint64(createdNs)
	ws.UpdatedNs = uint64(updatedNs)
	return &ws, nil
}
