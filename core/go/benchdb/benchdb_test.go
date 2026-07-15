package benchdb_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sammyjroberts/gantry/core/go/benchdb"
)

// TestOpenMigratesFromEmpty proves a fresh data dir gets the schema applied:
// the experiments table exists and is queryable right after Open.
func TestOpenMigratesFromEmpty(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bench.db")

	db, err := benchdb.Open(ctx, path)
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

// TestOpenAdoptsLegacyEdgeDB proves the Edge→Bench rename migration: a data dir
// that still holds a pre-rename edge.db is opened by adopting that file as
// bench.db, so existing bench history survives the rename with no operator step.
// Temp dir only — never the live data dir (the running bench holds it open).
func TestOpenAdoptsLegacyEdgeDB(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Seed a legacy edge.db with a row, via the same package (filename is the
	// only thing that differs from the new default).
	legacy := filepath.Join(dir, "edge.db")
	seed, err := benchdb.Open(ctx, legacy)
	if err != nil {
		t.Fatalf("seed legacy Open: %v", err)
	}
	if _, err := seed.ExecContext(ctx,
		`INSERT INTO experiments (id, name, start_ns, created_ns) VALUES ('legacy', 'run', 1, 1)`); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	// Open at the new bench.db path: the legacy file must be adopted.
	path := filepath.Join(dir, "bench.db")
	db, err := benchdb.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open (adopt): %v", err)
	}
	defer db.Close()

	var name string
	if err := db.QueryRowContext(ctx, `SELECT name FROM experiments WHERE id = 'legacy'`).Scan(&name); err != nil {
		t.Fatalf("legacy row not carried forward: %v", err)
	}
	if name != "run" {
		t.Fatalf("name = %q, want %q", name, "run")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy edge.db should have been renamed away, stat err = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("bench.db should exist after adoption: %v", err)
	}
}

// TestOpenDoesNotClobberExistingBenchDB proves adoption never overwrites a new
// bench.db when a stray legacy edge.db also exists — the current file wins.
func TestOpenDoesNotClobberExistingBenchDB(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Create the new DB first with its own row.
	path := filepath.Join(dir, "bench.db")
	cur, err := benchdb.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open bench.db: %v", err)
	}
	if _, err := cur.ExecContext(ctx,
		`INSERT INTO experiments (id, name, start_ns, created_ns) VALUES ('current', 'run', 1, 1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := cur.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Now drop a legacy edge.db beside it and reopen.
	legacy := filepath.Join(dir, "edge.db")
	old, err := benchdb.Open(ctx, legacy)
	if err != nil {
		t.Fatalf("Open legacy: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	db, err := benchdb.Open(ctx, path)
	if err != nil {
		t.Fatalf("re-Open bench.db: %v", err)
	}
	defer db.Close()

	var name string
	if err := db.QueryRowContext(ctx, `SELECT name FROM experiments WHERE id = 'current'`).Scan(&name); err != nil {
		t.Fatalf("existing bench.db row lost — it was clobbered: %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("legacy edge.db should be left untouched when bench.db exists: %v", err)
	}
}

// TestReopenIsIdempotent proves re-opening an already-migrated DB is a no-op that
// preserves data: goose records applied versions, so Migrate does not re-run.
func TestReopenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bench.db")

	db1, err := benchdb.Open(ctx, path)
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
	db2, err := benchdb.Open(ctx, path)
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

	// Goose bookkeeping must record at least the first migration. We assert
	// >= 1 rather than an exact version so this test stays valid as later
	// migrations (0002+, owned by other schemas) are added; idempotency is
	// proven by the surviving row above and by Reopen not erroring.
	var version int64
	if err := db2.QueryRowContext(ctx,
		`SELECT max(version_id) FROM goose_db_version`).Scan(&version); err != nil {
		t.Fatalf("goose bookkeeping missing: %v", err)
	}
	if version < 1 {
		t.Fatalf("current migration version = %d, want >= 1", version)
	}
}
