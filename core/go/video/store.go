package video

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned when a chunk id does not exist in the catalog.
var ErrNotFound = errors.New("video chunk not found")

// Chunk is one self-contained video chunk's catalog row. The bytes live in the
// blob store under BlobKey; this struct is metadata only. JSON tags define the
// plain-HTTP v1 wire shape (BlobKey is internal and never leaves the process).
type Chunk struct {
	ID         string `json:"id"`
	CameraID   string `json:"camera_id"`
	StartNs    int64  `json:"start_ns"`
	DurationMs int64  `json:"duration_ms"`
	Mime       string `json:"mime"`
	Bytes      int64  `json:"bytes"`
	CreatedNs  int64  `json:"created_ns"`
	BlobKey    string `json:"-"`
}

// Camera is a distinct camera_id with the start time of its most recent chunk.
type Camera struct {
	CameraID      string `json:"camera_id"`
	LatestStartNs int64  `json:"latest_start_ns"`
}

// Store is the persistence layer for the video catalog over the Bench SQLite
// database (core/go/benchdb). The same SQL runs on core Postgres — only the
// driver differs. It owns no policy: validation and blob orchestration live in
// Service.
type Store struct {
	db *sql.DB
}

// NewStore builds a Store over an already-migrated *sql.DB.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Insert writes a fully-populated chunk row.
func (s *Store) Insert(ctx context.Context, c Chunk) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO video_chunks (id, camera_id, start_ns, duration_ms, blob_key, mime, bytes, created_ns)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.CameraID, c.StartNs, c.DurationMs, c.BlobKey, c.Mime, c.Bytes, c.CreatedNs)
	if err != nil {
		return fmt.Errorf("video: insert: %w", err)
	}
	return nil
}

// Get returns one chunk by id, or ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (Chunk, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, camera_id, start_ns, duration_ms, blob_key, mime, bytes, created_ns
		 FROM video_chunks WHERE id = ?`, id)
	c, err := scanChunk(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Chunk{}, ErrNotFound
	}
	if err != nil {
		return Chunk{}, fmt.Errorf("video: get: %w", err)
	}
	return c, nil
}

// List returns a camera's chunks in ascending start order. fromNs/toNs bound the
// start_ns range; a zero bound is treated as open (0 fromNs = from the
// beginning, 0 toNs = up to now). toNs is inclusive so a single-instant query
// (from == to) returns a chunk that starts exactly there.
func (s *Store) List(ctx context.Context, cameraID string, fromNs, toNs int64) ([]Chunk, error) {
	const cols = `SELECT id, camera_id, start_ns, duration_ms, blob_key, mime, bytes, created_ns FROM video_chunks`
	query := cols + ` WHERE camera_id = ?`
	args := []any{cameraID}
	if fromNs > 0 {
		query += ` AND start_ns >= ?`
		args = append(args, fromNs)
	}
	if toNs > 0 {
		query += ` AND start_ns <= ?`
		args = append(args, toNs)
	}
	query += ` ORDER BY start_ns ASC, id ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("video: list: %w", err)
	}
	defer rows.Close()

	var out []Chunk
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			return nil, fmt.Errorf("video: list scan: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("video: list rows: %w", err)
	}
	return out, nil
}

// ListCameras returns the distinct camera_ids with the newest start_ns each,
// newest camera first. This is the "which cameras exist" query — cameras are
// implicit in the chunk rows.
func (s *Store) ListCameras(ctx context.Context) ([]Camera, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT camera_id, MAX(start_ns) AS latest
		 FROM video_chunks GROUP BY camera_id ORDER BY latest DESC, camera_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("video: list cameras: %w", err)
	}
	defer rows.Close()

	var out []Camera
	for rows.Next() {
		var cam Camera
		if err := rows.Scan(&cam.CameraID, &cam.LatestStartNs); err != nil {
			return nil, fmt.Errorf("video: list cameras scan: %w", err)
		}
		out = append(out, cam)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("video: list cameras rows: %w", err)
	}
	return out, nil
}

// ListOlderThan returns chunks whose start_ns is strictly before cutoffNs. Used
// by Prune to learn which blobs to delete alongside the rows.
func (s *Store) ListOlderThan(ctx context.Context, cutoffNs int64) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, camera_id, start_ns, duration_ms, blob_key, mime, bytes, created_ns
		 FROM video_chunks WHERE start_ns < ? ORDER BY start_ns ASC`, cutoffNs)
	if err != nil {
		return nil, fmt.Errorf("video: list older: %w", err)
	}
	defer rows.Close()

	var out []Chunk
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			return nil, fmt.Errorf("video: list older scan: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("video: list older rows: %w", err)
	}
	return out, nil
}

// Delete removes a chunk row by id. A missing row is not an error (prune may
// race a manual delete); callers that need existence use Get first.
func (s *Store) Delete(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM video_chunks WHERE id = ?`, id); err != nil {
		return fmt.Errorf("video: delete: %w", err)
	}
	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanChunk(sc scanner) (Chunk, error) {
	var c Chunk
	if err := sc.Scan(&c.ID, &c.CameraID, &c.StartNs, &c.DurationMs, &c.BlobKey, &c.Mime, &c.Bytes, &c.CreatedNs); err != nil {
		return Chunk{}, err
	}
	return c, nil
}
