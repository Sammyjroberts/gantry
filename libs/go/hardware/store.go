package hardware

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// ErrNotFound is returned when a hardware row (device_id) does not exist.
var ErrNotFound = errors.New("hardware not found")

// Store is the persistence layer for hardware over the Edge SQLite database
// (libs/go/edgedb). The same SQL runs on core Postgres — only the driver
// differs. It maps rows to and from the proto Hardware message and owns no
// policy: all validation, JSON-size capping, and time defaulting lives in
// Service.
type Store struct {
	db *sql.DB
}

// NewStore builds a Store over an already-migrated *sql.DB.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Upsert creates or updates a hardware row keyed by device_id. created_ns is set
// only on insert (ON CONFLICT preserves the original), while every other column
// — including updated_ns — is overwritten from hw. Callers pass created_ns and
// updated_ns already stamped (see Service). The canonical row is then re-read so
// the returned created_ns reflects a prior insert on an update.
func (s *Store) Upsert(ctx context.Context, hw *gantryv1.Hardware) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO hardware
		   (device_id, display_name, description, notes, viz_config_json, panel_defaults_json, created_ns, updated_ns)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(device_id) DO UPDATE SET
		   display_name        = excluded.display_name,
		   description         = excluded.description,
		   notes               = excluded.notes,
		   viz_config_json     = excluded.viz_config_json,
		   panel_defaults_json = excluded.panel_defaults_json,
		   updated_ns          = excluded.updated_ns`,
		hw.DeviceId, hw.DisplayName, hw.Description, hw.Notes,
		hw.VizConfigJson, hw.PanelDefaultsJson, int64(hw.CreatedNs), int64(hw.UpdatedNs))
	if err != nil {
		return fmt.Errorf("hardware: upsert: %w", err)
	}
	return nil
}

// Get returns one hardware row by device_id, or ErrNotFound.
func (s *Store) Get(ctx context.Context, deviceID string) (*gantryv1.Hardware, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT device_id, display_name, description, notes, viz_config_json, panel_defaults_json, created_ns, updated_ns
		 FROM hardware WHERE device_id = ?`, deviceID)
	hw, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hardware: get: %w", err)
	}
	return hw, nil
}

// List returns all hardware rows ordered by display_name then device_id (a
// stable, human-friendly order for the console). An empty result is not an
// error.
func (s *Store) List(ctx context.Context) ([]*gantryv1.Hardware, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT device_id, display_name, description, notes, viz_config_json, panel_defaults_json, created_ns, updated_ns
		 FROM hardware ORDER BY display_name ASC, device_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("hardware: list: %w", err)
	}
	defer rows.Close()

	var out []*gantryv1.Hardware
	for rows.Next() {
		hw, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("hardware: list scan: %w", err)
		}
		out = append(out, hw)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hardware: list rows: %w", err)
	}
	return out, nil
}

// Delete removes a hardware row by device_id. Returns ErrNotFound if no row
// matched.
func (s *Store) Delete(ctx context.Context, deviceID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM hardware WHERE device_id = ?`, deviceID)
	if err != nil {
		return fmt.Errorf("hardware: delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("hardware: rows affected: %w", err)
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

// scan reads one hardware row. created_ns/updated_ns are stored as signed
// INTEGER (SQLite has no unsigned type) and re-widened to the proto's fixed64.
func scan(sc scanner) (*gantryv1.Hardware, error) {
	var (
		hw                   gantryv1.Hardware
		createdNs, updatedNs int64
	)
	if err := sc.Scan(&hw.DeviceId, &hw.DisplayName, &hw.Description, &hw.Notes,
		&hw.VizConfigJson, &hw.PanelDefaultsJson, &createdNs, &updatedNs); err != nil {
		return nil, err
	}
	hw.CreatedNs = uint64(createdNs)
	hw.UpdatedNs = uint64(updatedNs)
	return &hw, nil
}
