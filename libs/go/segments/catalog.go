package segments

import (
	"context"
	"database/sql"
	"fmt"
)

// SegmentMeta is one row of the segment catalog: the metadata locating an
// immutable Parquet segment file in the blob store and describing its contents.
type SegmentMeta struct {
	ID        string
	DeviceID  string
	StartNs   int64
	EndNs     int64
	Frames    int64
	Bytes     int64
	BlobKey   string
	CreatedNs int64
}

// Catalog is the segment index behind an interface: SQLite on Edge today
// (SQLCatalog), Postgres on Backend later. Add commits a flushed segment and
// advances the writer recovery checkpoint atomically; Overlapping and Horizon
// serve the reader/planner. Implementations are safe for concurrent use.
type Catalog interface {
	// Add records a flushed segment and advances the high-water sequence in ONE
	// transaction, so a committed segment and the checkpoint that skips re-reading
	// its frames can never disagree.
	Add(ctx context.Context, m SegmentMeta, highWaterSeq uint64) error
	// Overlapping returns segments whose [start_ns, end_ns] intersects
	// [startNs, endNs], oldest first. deviceID == "" spans all devices.
	Overlapping(ctx context.Context, deviceID string, startNs, endNs int64) ([]SegmentMeta, error)
	// HighWaterSeq returns the persisted recovery checkpoint (0 on a fresh store).
	HighWaterSeq(ctx context.Context) (uint64, error)
	// Horizon returns the min start_ns and max end_ns across all segments and
	// whether any segment exists. It gives the planner the true retention horizon
	// (segments reach far further back than the JetStream tail).
	Horizon(ctx context.Context) (minStartNs, maxEndNs int64, ok bool, err error)
}

// SQLCatalog implements Catalog over the Edge SQLite database (the segments +
// segment_state tables from migration 0002). It shares the *sql.DB with the rest
// of Edge; edgedb caps the pool at one connection so writes never contend.
type SQLCatalog struct {
	db *sql.DB
}

// NewSQLCatalog builds a catalog over an already-migrated *sql.DB.
func NewSQLCatalog(db *sql.DB) *SQLCatalog { return &SQLCatalog{db: db} }

func (c *SQLCatalog) Add(ctx context.Context, m SegmentMeta, highWaterSeq uint64) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("segments: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO segments (id, device_id, start_ns, end_ns, frames, bytes, blob_key, created_ns)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.DeviceID, m.StartNs, m.EndNs, m.Frames, m.Bytes, m.BlobKey, m.CreatedNs); err != nil {
		return fmt.Errorf("segments: insert %q: %w", m.ID, err)
	}
	// Monotonic guard: never move the checkpoint backwards (a late/small segment
	// must not un-checkpoint later frames).
	if _, err := tx.ExecContext(ctx,
		`UPDATE segment_state SET high_water_seq = ? WHERE id = 1 AND high_water_seq < ?`,
		int64(highWaterSeq), int64(highWaterSeq)); err != nil {
		return fmt.Errorf("segments: advance high-water: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("segments: commit: %w", err)
	}
	return nil
}

func (c *SQLCatalog) Overlapping(ctx context.Context, deviceID string, startNs, endNs int64) ([]SegmentMeta, error) {
	const cols = `SELECT id, device_id, start_ns, end_ns, frames, bytes, blob_key, created_ns FROM segments`
	// Overlap test: a segment [s,e] intersects the query [t1,t2] iff e >= t1 AND
	// s <= t2. Oldest first so the reader emits in time order across segments.
	var (
		rows *sql.Rows
		err  error
	)
	if deviceID == "" {
		rows, err = c.db.QueryContext(ctx,
			cols+` WHERE end_ns >= ? AND start_ns <= ? ORDER BY start_ns, id`, startNs, endNs)
	} else {
		rows, err = c.db.QueryContext(ctx,
			cols+` WHERE device_id = ? AND end_ns >= ? AND start_ns <= ? ORDER BY start_ns, id`,
			deviceID, startNs, endNs)
	}
	if err != nil {
		return nil, fmt.Errorf("segments: overlap query: %w", err)
	}
	defer rows.Close()

	var out []SegmentMeta
	for rows.Next() {
		var m SegmentMeta
		if err := rows.Scan(&m.ID, &m.DeviceID, &m.StartNs, &m.EndNs, &m.Frames, &m.Bytes, &m.BlobKey, &m.CreatedNs); err != nil {
			return nil, fmt.Errorf("segments: overlap scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("segments: overlap rows: %w", err)
	}
	return out, nil
}

func (c *SQLCatalog) HighWaterSeq(ctx context.Context) (uint64, error) {
	var seq int64
	err := c.db.QueryRowContext(ctx, `SELECT high_water_seq FROM segment_state WHERE id = 1`).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("segments: high-water: %w", err)
	}
	return uint64(seq), nil
}

func (c *SQLCatalog) Horizon(ctx context.Context) (int64, int64, bool, error) {
	var minStart, maxEnd sql.NullInt64
	err := c.db.QueryRowContext(ctx,
		`SELECT MIN(start_ns), MAX(end_ns) FROM segments`).Scan(&minStart, &maxEnd)
	if err != nil {
		return 0, 0, false, fmt.Errorf("segments: horizon: %w", err)
	}
	if !minStart.Valid || !maxEnd.Valid {
		return 0, 0, false, nil
	}
	return minStart.Int64, maxEnd.Int64, true, nil
}

// Compile-time assertion.
var _ Catalog = (*SQLCatalog)(nil)
