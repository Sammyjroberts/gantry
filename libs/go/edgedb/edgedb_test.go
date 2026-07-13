package edgedb_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Sammyjroberts/gantry/libs/go/edgedb"
)

// TestOpenMigratesFromEmpty proves a fresh data dir gets the schema applied:
// the experiments table exists and is queryable right after Open.
func TestOpenMigratesFromEmpty(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "edge.db")

	db, err := edgedb.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// The experiments table must exist and be empty.
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM experiments`).Scan(&n); err != nil {
		t.Fatalf("query experiments: %v", err)
	}
	if n != 0 {
		t.Fatalf("fresh experiments table has %d rows, want 0", n)
	}

	// The start_ns index must exist (0001 creates it).
	var idxName string
	err = db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_experiments_start_ns'`).Scan(&idxName)
	if err != nil {
		t.Fatalf("start_ns index missing: %v", err)
	}
}

// TestReopenIsIdempotent proves re-opening an already-migrated DB is a no-op that
// preserves data: goose records applied versions, so Migrate does not re-run.
func TestReopenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "edge.db")

	db1, err := edgedb.Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := db1.ExecContext(ctx,
		`INSERT INTO experiments (id, name, start_ns, created_ns) VALUES ('abc', 'run', 1, 1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Re-open the same file: migration must not error or wipe the row.
	db2, err := edgedb.Open(ctx, path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer db2.Close()

	var name string
	if err := db2.QueryRowContext(ctx, `SELECT name FROM experiments WHERE id = 'abc'`).Scan(&name); err != nil {
		t.Fatalf("row lost after reopen: %v", err)
	}
	if name != "run" {
		t.Fatalf("name = %q, want %q", name, "run")
	}

	// Applied migration version must be recorded exactly once at 1.
	var version int64
	if err := db2.QueryRowContext(ctx,
		`SELECT max(version_id) FROM goose_db_version`).Scan(&version); err != nil {
		t.Fatalf("goose bookkeeping missing: %v", err)
	}
	if version != 1 {
		t.Fatalf("current migration version = %d, want 1", version)
	}
}
