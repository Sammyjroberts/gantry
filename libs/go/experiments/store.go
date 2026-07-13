package experiments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// ErrNotFound is returned when an experiment id does not exist.
var ErrNotFound = errors.New("experiment not found")

// Store is the persistence layer for experiments over the Edge SQLite database.
// It maps rows to and from the proto Experiment message and owns no policy: all
// validation and time defaulting lives in Service.
type Store struct {
	db *sql.DB
}

// NewStore builds a Store over an already-migrated *sql.DB (see libs/go/edgedb).
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Insert writes a fully-populated experiment row.
func (s *Store) Insert(ctx context.Context, e *gantryv1.Experiment) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO experiments (id, name, notes, device_id, start_ns, end_ns, created_ns)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.Id, e.Name, e.Notes, e.DeviceId,
		int64(e.StartNs), int64(e.EndNs), int64(e.CreatedNs))
	if err != nil {
		return fmt.Errorf("experiments: insert: %w", err)
	}
	return nil
}

// Get returns one experiment by id, or ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (*gantryv1.Experiment, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, notes, device_id, start_ns, end_ns, created_ns
		 FROM experiments WHERE id = ?`, id)
	e, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("experiments: get: %w", err)
	}
	return e, nil
}

// List returns experiments newest-first (by start_ns desc, then id for a stable
// tiebreak). An empty deviceID returns all experiments; otherwise only those
// whose device_id matches exactly.
func (s *Store) List(ctx context.Context, deviceID string) ([]*gantryv1.Experiment, error) {
	var (
		rows *sql.Rows
		err  error
	)
	const cols = `SELECT id, name, notes, device_id, start_ns, end_ns, created_ns FROM experiments`
	if deviceID == "" {
		rows, err = s.db.QueryContext(ctx, cols+` ORDER BY start_ns DESC, id DESC`)
	} else {
		rows, err = s.db.QueryContext(ctx, cols+` WHERE device_id = ? ORDER BY start_ns DESC, id DESC`, deviceID)
	}
	if err != nil {
		return nil, fmt.Errorf("experiments: list: %w", err)
	}
	defer rows.Close()

	var out []*gantryv1.Experiment
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("experiments: list scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("experiments: list rows: %w", err)
	}
	return out, nil
}

// UpdateMeta sets name and notes on an existing experiment. Returns ErrNotFound
// if no row matched.
func (s *Store) UpdateMeta(ctx context.Context, id, name, notes string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE experiments SET name = ?, notes = ? WHERE id = ?`, name, notes, id)
	if err != nil {
		return fmt.Errorf("experiments: update: %w", err)
	}
	return affectedOrNotFound(res)
}

// SetEnd sets end_ns on a running experiment (end_ns = 0). The end_ns = 0 guard
// makes stopping idempotent-safe and prevents re-stopping an already-stopped
// experiment; callers distinguish "not found" from "not running" via Get.
func (s *Store) SetEnd(ctx context.Context, id string, endNs int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE experiments SET end_ns = ? WHERE id = ? AND end_ns = 0`, endNs, id)
	if err != nil {
		return fmt.Errorf("experiments: set end: %w", err)
	}
	return affectedOrNotFound(res)
}

// Delete removes an experiment. Returns ErrNotFound if no row matched.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM experiments WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("experiments: delete: %w", err)
	}
	return affectedOrNotFound(res)
}

// affectedOrNotFound maps a zero-rows-affected result to ErrNotFound.
func affectedOrNotFound(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("experiments: rows affected: %w", err)
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

// scan reads one experiment row. start_ns/end_ns/created_ns are stored as signed
// INTEGER (SQLite has no unsigned type) and re-widened to the proto's fixed64.
func scan(sc scanner) (*gantryv1.Experiment, error) {
	var (
		e                         gantryv1.Experiment
		startNs, endNs, createdNs int64
	)
	if err := sc.Scan(&e.Id, &e.Name, &e.Notes, &e.DeviceId, &startNs, &endNs, &createdNs); err != nil {
		return nil, err
	}
	e.StartNs = uint64(startNs)
	e.EndNs = uint64(endNs)
	e.CreatedNs = uint64(createdNs)
	return &e, nil
}
