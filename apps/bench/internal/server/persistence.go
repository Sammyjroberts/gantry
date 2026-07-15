package server

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/Sammyjroberts/gantry/core/go/blob"
	"github.com/Sammyjroberts/gantry/core/go/duckdb"
	"github.com/Sammyjroberts/gantry/core/go/segments"
	"github.com/Sammyjroberts/gantry/core/go/stream"
)

// Persistence is Bench's durable telemetry tier, assembled from the shared libs:
// a filesystem blob store, the SQLite segment catalog, the Parquet segment
// Writer (the flusher draining JetStream) and Reader (the historical query
// path), and — when a DuckDB binary is present — the embedded SQL engine over
// the segment glob. It is a thin assembly the Bench server wires in; Cloud
// builds the same parts over S3 + Postgres + clustered NATS.
//
// SQL is best-effort: a missing DuckDB binary leaves SQL nil and everything else
// works (the /sql surface and query_sql tool then report a clear install hint).
type Persistence struct {
	Store   *blob.FS
	Catalog *segments.SQLCatalog
	Writer  *segments.Writer
	Reader  *segments.Reader
	SQL     *duckdb.Engine // nil when no DuckDB binary is available
}

// NewPersistence builds the durable tier under storeDir, sharing the already-
// migrated *sql.DB (segments/segment_state tables come from migration 0002) and
// the telemetry bus. It does NOT start the flusher; call Start.
func NewPersistence(ctx context.Context, storeDir string, db *sql.DB, bus *stream.Bus) (*Persistence, error) {
	store, err := blob.NewFS(filepath.Join(storeDir, "blobs"))
	if err != nil {
		return nil, fmt.Errorf("edge: blob store: %w", err)
	}
	cat := segments.NewSQLCatalog(db)
	writer := segments.NewWriter(bus, store, cat, segments.Options{})
	reader := segments.NewReader(store, cat)

	p := &Persistence{Store: store, Catalog: cat, Writer: writer, Reader: reader}

	// DuckDB tier: resolve a binary from GANTRY_DUCKDB or <storeDir>/duckdb/.
	// Absence is not an error — SQL simply stays disabled.
	engine, err := duckdb.New(duckdb.DefaultProvider(storeDir), duckdb.Config{
		SegmentsGlob: filepath.Join(store.Root(), "segments", "*", "*.parquet"),
		CatalogPath:  filepath.Join(storeDir, "bench.db"),
	})
	if err == nil {
		p.SQL = engine
	}
	return p, nil
}

// Start begins draining the stream into segments (the flusher).
func (p *Persistence) Start(ctx context.Context) error {
	return p.Writer.Start(ctx)
}

// Stop halts the flusher and flushes any buffered rows.
func (p *Persistence) Stop(ctx context.Context) error {
	return p.Writer.Stop(ctx)
}

// SegmentReader exposes the reader as the query planner's segment seam, or nil.
// Passing this into query.CollectWithSegments activates the segment-backed range
// path; a nil value selects the pure-replay fallback.
func (p *Persistence) SegmentReader() *segments.Reader { return p.Reader }
