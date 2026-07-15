// Package benchdb is Bench's persistent store: a pure-Go SQLite database
// (modernc.org/sqlite, no CGo) whose schema is managed exclusively by goose
// migrations compiled into the binary via embed.FS. The Bench binary migrates
// its own database at startup, which is what a single-binary offline product
// must do (see docs/BACKLOG.md "Database migrations — goose").
//
// The package is deliberately generic: Open + Migrate hand back a *sql.DB and
// nothing schema-specific lives here. Migration 0001 creates the experiments
// table; future schemas (the segment index, sessions, channel registry) land as
// 0002+ with no change to this code.
package benchdb

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// migrationsFS embeds the SQL migrations so the Bench binary carries its own
// schema and can migrate a fresh data dir on first boot with no external files.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// legacyDBName is the pre-rename database filename. The product renamed
// Edge→Bench; a data dir written by an older binary holds edge.db. Open adopts
// it (below) so existing bench data upgrades in place without operator action.
const legacyDBName = "edge.db"

// DriverName is the database/sql driver registered by modernc.org/sqlite.
const DriverName = "sqlite"

// dsnParams tunes the connection for a single-binary bench product:
//   - busy_timeout: wait rather than immediately erroring on a locked DB.
//   - journal_mode(WAL): concurrent readers while a writer is active.
//   - foreign_keys(1): enforce referential integrity (off by default in SQLite).
const dsnParams = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"

// Open opens (creating if absent) the SQLite database at path, verifies the
// connection, and applies all pending migrations. The returned *sql.DB is ready
// for use; the caller owns it and must Close it.
//
// SQLite is single-writer; callers that write concurrently should funnel writes
// through one connection. We cap the pool at a single connection so WAL-mode
// writes never contend for the write lock inside one process.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if err := adoptLegacyDB(path); err != nil {
		return nil, err
	}
	db, err := sql.Open(DriverName, path+dsnParams)
	if err != nil {
		return nil, fmt.Errorf("benchdb: open %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("benchdb: ping %q: %w", path, err)
	}
	if err := Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// adoptLegacyDB renames a pre-rename edge.db sitting beside the requested path
// to path when the new file does not yet exist. This lets a data dir written by
// an older Edge-named binary open cleanly under the Bench name on first boot,
// carrying its history forward. It is a no-op unless path's base is the current
// bench.db name, the legacy file is present, and the target is absent — so a
// fresh dir, an already-migrated dir, or a caller using some other filename are
// all untouched. WAL/SHM sidecars (edge.db-wal, edge.db-shm) are left for SQLite
// to recreate; only the main DB file carries committed state.
func adoptLegacyDB(path string) error {
	if filepath.Base(path) != "bench.db" {
		return nil
	}
	legacy := filepath.Join(filepath.Dir(path), legacyDBName)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		return nil // new DB already present (or stat failed) — do not clobber
	}
	if _, err := os.Stat(legacy); err != nil {
		return nil // no legacy DB to adopt
	}
	if err := os.Rename(legacy, path); err != nil {
		return fmt.Errorf("benchdb: adopt legacy %s: %w", legacyDBName, err)
	}
	return nil
}

// Migrate applies all pending embedded migrations to db. It is idempotent: goose
// records applied versions in its own bookkeeping table, so re-running Migrate on
// an up-to-date database is a no-op. Exposed separately from Open for tests and
// for callers that manage their own *sql.DB.
func Migrate(ctx context.Context, db *sql.DB) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("benchdb: sub migrations fs: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		return fmt.Errorf("benchdb: goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("benchdb: migrate: %w", err)
	}
	return nil
}
