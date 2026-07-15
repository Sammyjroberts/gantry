package duckdb_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"

	"github.com/Sammyjroberts/gantry/libs/go/duckdb"
)

// tlmRow mirrors the segment Parquet schema (segments.Row) closely enough for
// the tlm view's SELECT * — the DuckDB tier only needs the column names.
type tlmRow struct {
	Device  string  `parquet:"device"`
	Packet  string  `parquet:"packet"`
	Channel string  `parquet:"channel"`
	Kind    int32   `parquet:"kind"`
	TsNs    int64   `parquet:"ts_ns"`
	VF64    float64 `parquet:"v_f64"`
}

// writeSegment writes rows to <root>/segments/<device>/<name>.parquet.
func writeSegment(t *testing.T, root, device, name string, rows []tlmRow) {
	t.Helper()
	dir := filepath.Join(root, "segments", device)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := parquet.NewGenericWriter[tlmRow](f)
	if _, err := w.Write(rows); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// provider resolves the DuckDB binary for the test: GANTRY_DUCKDB, else skip.
func testProvider(t *testing.T) duckdb.Provider {
	p := duckdb.EnvProvider{}
	if _, ok := p.Binary(); !ok {
		t.Skip("GANTRY_DUCKDB not set to a duckdb binary; skipping DuckDB integration test")
	}
	return p
}

func newEngine(t *testing.T, root string) *duckdb.Engine {
	t.Helper()
	e, err := duckdb.New(testProvider(t), duckdb.Config{
		SegmentsGlob: filepath.Join(root, "segments", "*", "*.parquet"),
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return e
}

func TestEngine_NotInstalled(t *testing.T) {
	// PathProvider with an empty path yields no binary → ErrNotInstalled.
	_, err := duckdb.New(duckdb.PathProvider{Path: ""}, duckdb.Config{})
	if !errors.Is(err, duckdb.ErrNotInstalled) {
		t.Fatalf("want ErrNotInstalled, got %v", err)
	}
}

func TestEngine_QueryParquet(t *testing.T) {
	root := t.TempDir()
	e := newEngine(t, root)
	ctx := context.Background()

	writeSegment(t, root, "rover-1", "1-3.parquet", []tlmRow{
		{Device: "rover-1", Packet: "imu", Channel: "pitch_deg", Kind: 1, TsNs: 1000, VF64: 1.5},
		{Device: "rover-1", Packet: "imu", Channel: "pitch_deg", Kind: 1, TsNs: 2000, VF64: 2.5},
		{Device: "rover-1", Packet: "imu", Channel: "roll_deg", Kind: 1, TsNs: 2000, VF64: -1.0},
	})
	writeSegment(t, root, "rover-2", "1-1.parquet", []tlmRow{
		{Device: "rover-2", Packet: "imu", Channel: "pitch_deg", Kind: 1, TsNs: 1500, VF64: 9.0},
	})

	// Aggregation across all segments (the "killer move": SQL over telemetry).
	res, err := e.Query(ctx, "SELECT device, count(*) AS n, avg(v_f64) AS mean FROM tlm GROUP BY device ORDER BY device")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(res.Rows), res.Rows)
	}
	if res.Rows[0]["device"] != "rover-1" {
		t.Fatalf("first device = %v, want rover-1", res.Rows[0]["device"])
	}
	// count(*) for rover-1 is 3.
	if got := jsonNum(res.Rows[0]["n"]); got != 3 {
		t.Fatalf("rover-1 count = %v, want 3", res.Rows[0]["n"])
	}
	// Column order preserved.
	if strings.Join(res.Columns, ",") != "device,n,mean" {
		t.Fatalf("columns = %v, want [device n mean]", res.Columns)
	}

	// Filtered query.
	res, err = e.Query(ctx, "SELECT ts_ns, v_f64 FROM tlm WHERE channel = 'pitch_deg' AND device = 'rover-1' ORDER BY ts_ns")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("filtered rows = %d, want 2", len(res.Rows))
	}
}

func TestEngine_EmptyGlobStillWorks(t *testing.T) {
	root := t.TempDir() // no segments written
	e := newEngine(t, root)
	res, err := e.Query(context.Background(), "SELECT count(*) AS n FROM tlm")
	if err != nil {
		t.Fatalf("query on empty store: %v", err)
	}
	if len(res.Rows) != 1 || jsonNum(res.Rows[0]["n"]) != 0 {
		t.Fatalf("empty tlm count = %+v, want 0", res.Rows)
	}
}

func TestEngine_RowCap(t *testing.T) {
	root := t.TempDir()
	e, err := duckdb.New(testProvider(t), duckdb.Config{
		SegmentsGlob: filepath.Join(root, "segments", "*", "*.parquet"),
		MaxRows:      3,
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]tlmRow, 10)
	for i := range rows {
		rows[i] = tlmRow{Device: "d", Channel: "c", Kind: 1, TsNs: int64(i), VF64: float64(i)}
	}
	writeSegment(t, root, "d", "seg.parquet", rows)

	res, err := e.Query(context.Background(), "SELECT * FROM tlm")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Rows) != 3 {
		t.Fatalf("cap: truncated=%v rows=%d, want truncated=true rows=3", res.Truncated, len(res.Rows))
	}
}

func TestEngine_RejectsNonReadOnly(t *testing.T) {
	root := t.TempDir()
	e := newEngine(t, root)
	ctx := context.Background()
	for _, q := range []string{
		"INSERT INTO tlm VALUES (1)",
		"DROP VIEW tlm",
		"SELECT 1; DROP VIEW tlm",
		"ATTACH 'x.db' AS y",
		"COPY tlm TO 'out.csv'",
		"PRAGMA database_list",
		"",
	} {
		if _, err := e.Query(ctx, q); err == nil {
			t.Fatalf("query %q was accepted, want rejection", q)
		} else if !isReadOnlyErr(err) {
			t.Fatalf("query %q rejected with unexpected error: %v", q, err)
		}
	}
}

func isReadOnlyErr(err error) bool {
	var roErr duckdb.ErrNotReadOnly
	return errors.As(err, &roErr)
}

// jsonNum coerces a JSON number (float64) to int for assertions.
func jsonNum(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return -1
	}
}
