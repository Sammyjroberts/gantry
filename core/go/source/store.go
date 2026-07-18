package source

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// ErrNotFound is returned when a source id does not exist.
var ErrNotFound = errors.New("source not found")

// Store is the persistence layer for telemetry sources over the Bench SQLite
// database (core/go/benchdb). The same SQL runs on core Postgres — only the
// driver differs. It maps rows to and from the proto Source message and owns no
// policy: all validation, id generation, mapping parsing, and time defaulting
// lives in Service.
type Store struct {
	db *sql.DB
}

// NewStore builds a Store over an already-migrated *sql.DB.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Upsert creates or updates a source row keyed by id. created_ns is set only on
// insert (ON CONFLICT preserves the original), while every other column —
// including updated_ns — is overwritten from src. Callers pass created_ns and
// updated_ns already stamped (see Service). enabled is stored as 0/1 (SQLite has
// no boolean type).
func (s *Store) Upsert(ctx context.Context, src *gantryv1.Source) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sources (id, type, name, url, mapping_json, enabled, created_ns, updated_ns)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   type         = excluded.type,
		   name         = excluded.name,
		   url          = excluded.url,
		   mapping_json = excluded.mapping_json,
		   enabled      = excluded.enabled,
		   updated_ns   = excluded.updated_ns`,
		src.Id, src.Type, src.Name, src.Url, src.MappingJson, boolToInt(src.Enabled),
		int64(src.CreatedNs), int64(src.UpdatedNs))
	if err != nil {
		return fmt.Errorf("source: upsert: %w", err)
	}
	return nil
}

// Get returns one source row by id, or ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (*gantryv1.Source, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, type, name, url, mapping_json, enabled, created_ns, updated_ns
		 FROM sources WHERE id = ?`, id)
	src, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("source: get: %w", err)
	}
	return src, nil
}

// List returns all source rows ordered by name then id (a stable, human-friendly
// order for the console). An empty result is not an error.
func (s *Store) List(ctx context.Context) ([]*gantryv1.Source, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, name, url, mapping_json, enabled, created_ns, updated_ns
		 FROM sources ORDER BY name ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("source: list: %w", err)
	}
	defer rows.Close()

	var out []*gantryv1.Source
	for rows.Next() {
		src, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("source: list scan: %w", err)
		}
		out = append(out, src)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("source: list rows: %w", err)
	}
	return out, nil
}

// Delete removes a source row by id. Returns ErrNotFound if no row matched.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("source: delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("source: rows affected: %w", err)
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

// scan reads one source row. enabled is stored as INTEGER 0/1; created_ns /
// updated_ns are stored as signed INTEGER (SQLite has no unsigned type) and
// re-widened to the proto's fixed64.
func scan(sc scanner) (*gantryv1.Source, error) {
	var (
		src                  gantryv1.Source
		enabled              int64
		createdNs, updatedNs int64
	)
	if err := sc.Scan(&src.Id, &src.Type, &src.Name, &src.Url, &src.MappingJson,
		&enabled, &createdNs, &updatedNs); err != nil {
		return nil, err
	}
	src.Enabled = enabled != 0
	src.CreatedNs = uint64(createdNs)
	src.UpdatedNs = uint64(updatedNs)
	return &src, nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
