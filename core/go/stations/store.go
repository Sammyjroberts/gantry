package stations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// ErrNotFound is returned when a station or lease id does not exist. ErrTaken is
// returned by GrantLease when a station already holds an active lease.
var (
	ErrNotFound = errors.New("station entity not found")
	ErrTaken    = errors.New("station already leased")
)

// Store is the persistence layer for stations and leases over the Bench SQLite
// database. Availability is not stored — it is derived by Service at read time.
type Store struct {
	db *sql.DB
}

// NewStore builds a Store over an already-migrated *sql.DB.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// UpsertStation inserts or updates a station (id is the natural key), stamping
// last_seen_ns.
func (s *Store) UpsertStation(ctx context.Context, st *gantryv1.Station, lastSeenNs, createdNs uint64) error {
	tagsJSON, err := json.Marshal(st.Tags)
	if err != nil {
		return fmt.Errorf("stations: marshal tags: %w", err)
	}
	devJSON, err := json.Marshal(st.DeviceIds)
	if err != nil {
		return fmt.Errorf("stations: marshal device ids: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO stations (id, bench_host_id, tags_json, device_ids_json, health_json, last_seen_ns, created_ns)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   bench_host_id=excluded.bench_host_id, tags_json=excluded.tags_json,
		   device_ids_json=excluded.device_ids_json, health_json=excluded.health_json,
		   last_seen_ns=excluded.last_seen_ns`,
		st.Id, st.BenchHostId, string(tagsJSON), string(devJSON), st.HealthJson, int64(lastSeenNs), int64(createdNs))
	if err != nil {
		return fmt.Errorf("stations: upsert station: %w", err)
	}
	return nil
}

// GetStation returns one station (no lease/availability; Service hydrates those).
func (s *Store) GetStation(ctx context.Context, id string) (*gantryv1.Station, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, bench_host_id, tags_json, device_ids_json, health_json, last_seen_ns
		 FROM stations WHERE id = ?`, id)
	st, err := scanStation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("stations: get station: %w", err)
	}
	return st, nil
}

// ListStations returns every station (ordered by id), lease/availability unset.
func (s *Store) ListStations(ctx context.Context) ([]*gantryv1.Station, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, bench_host_id, tags_json, device_ids_json, health_json, last_seen_ns
		 FROM stations ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("stations: list: %w", err)
	}
	defer rows.Close()
	var out []*gantryv1.Station
	for rows.Next() {
		st, err := scanStation(rows)
		if err != nil {
			return nil, fmt.Errorf("stations: scan: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// GrantLease atomically reserves a station: within a transaction it releases any
// expired lease on the station, then inserts the new lease under the active-lease
// unique index. If the station already holds an active lease the insert violates
// the index and GrantLease returns ErrTaken — so concurrent grabbers serialize at
// the database instead of relying on an app-level check (finding #1).
func (s *Store) GrantLease(ctx context.Context, l *gantryv1.Lease, idempotencyKey string, nowNs uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("stations: begin grant: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE station_leases SET released = 1 WHERE station_id = ? AND released = 0 AND expires_ns <= ?`,
		l.StationId, int64(nowNs)); err != nil {
		return fmt.Errorf("stations: reap expired: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO station_leases (id, station_id, holder, reason, acquired_ns, expires_ns, released, idempotency_key)
		 VALUES (?, ?, ?, ?, ?, ?, 0, ?)`,
		l.Id, l.StationId, l.Holder, l.Reason, int64(l.AcquiredNs), int64(l.ExpiresNs), idempotencyKey)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrTaken
		}
		return fmt.Errorf("stations: insert lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		if isUniqueViolation(err) {
			return ErrTaken
		}
		return fmt.Errorf("stations: commit grant: %w", err)
	}
	return nil
}

// isUniqueViolation reports whether err is a SQL unique-constraint violation
// (modernc SQLite and lib/pq both surface a recognisable message).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE") ||
		strings.Contains(msg, "duplicate key value")
}

// ActiveLease returns the current active lease for a station (released = 0 and
// not expired at nowNs), or ErrNotFound.
func (s *Store) ActiveLease(ctx context.Context, stationID string, nowNs uint64) (*gantryv1.Lease, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, station_id, holder, reason, acquired_ns, expires_ns
		 FROM station_leases
		 WHERE station_id = ? AND released = 0 AND expires_ns > ?
		 ORDER BY expires_ns DESC LIMIT 1`, stationID, int64(nowNs))
	l, err := scanLease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("stations: active lease: %w", err)
	}
	return l, nil
}

// ActiveLeasesByIdempotencyKey returns the still-active leases created under a
// key (released = 0, not expired). A rolled-back or expired prior attempt is not
// returned, so a retry re-leases cleanly instead of adopting a dead reservation.
func (s *Store) ActiveLeasesByIdempotencyKey(ctx context.Context, key string, nowNs uint64) ([]*gantryv1.Lease, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, station_id, holder, reason, acquired_ns, expires_ns
		 FROM station_leases WHERE idempotency_key = ? AND released = 0 AND expires_ns > ?
		 ORDER BY station_id`, key, int64(nowNs))
	if err != nil {
		return nil, fmt.Errorf("stations: leases by idem: %w", err)
	}
	defer rows.Close()
	var out []*gantryv1.Lease
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, fmt.Errorf("stations: scan lease: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetLease returns one lease by id (active or not), or ErrNotFound.
func (s *Store) GetLease(ctx context.Context, id string) (*gantryv1.Lease, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, station_id, holder, reason, acquired_ns, expires_ns
		 FROM station_leases WHERE id = ? AND released = 0`, id)
	l, err := scanLease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("stations: get lease: %w", err)
	}
	return l, nil
}

// RenewLease extends a still-active lease's expiry. It refuses to renew a lease
// that is already released or expired at nowNs (finding #8: renewing an expired
// lease could resurrect a reservation another holder may already have taken).
func (s *Store) RenewLease(ctx context.Context, id string, expiresNs, nowNs uint64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE station_leases SET expires_ns = ? WHERE id = ? AND released = 0 AND expires_ns > ?`,
		int64(expiresNs), id, int64(nowNs))
	if err != nil {
		return fmt.Errorf("stations: renew lease: %w", err)
	}
	return affectedOrNotFound(res)
}

// ReleaseLease marks a lease released (frees its station).
func (s *Store) ReleaseLease(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE station_leases SET released = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("stations: release lease: %w", err)
	}
	return affectedOrNotFound(res)
}

func affectedOrNotFound(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("stations: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func scanStation(sc scanner) (*gantryv1.Station, error) {
	st := &gantryv1.Station{}
	var tagsJSON, devJSON string
	var lastSeen int64
	if err := sc.Scan(&st.Id, &st.BenchHostId, &tagsJSON, &devJSON, &st.HealthJson, &lastSeen); err != nil {
		return nil, err
	}
	st.LastSeenNs = uint64(lastSeen)
	if tagsJSON != "" {
		if err := json.Unmarshal([]byte(tagsJSON), &st.Tags); err != nil {
			return nil, fmt.Errorf("stations: unmarshal tags: %w", err)
		}
	}
	if devJSON != "" {
		if err := json.Unmarshal([]byte(devJSON), &st.DeviceIds); err != nil {
			return nil, fmt.Errorf("stations: unmarshal device ids: %w", err)
		}
	}
	return st, nil
}

func scanLease(sc scanner) (*gantryv1.Lease, error) {
	l := &gantryv1.Lease{}
	var acquired, expires int64
	if err := sc.Scan(&l.Id, &l.StationId, &l.Holder, &l.Reason, &acquired, &expires); err != nil {
		return nil, err
	}
	l.AcquiredNs, l.ExpiresNs = uint64(acquired), uint64(expires)
	return l, nil
}
