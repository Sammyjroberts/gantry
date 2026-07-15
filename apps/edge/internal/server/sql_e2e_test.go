package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/libs/go/duckdb"
	"github.com/Sammyjroberts/gantry/libs/go/edgedb"
	"github.com/Sammyjroberts/gantry/libs/go/ingest"
	"github.com/Sammyjroberts/gantry/libs/go/registry"
	"github.com/Sammyjroberts/gantry/libs/go/stream"
)

// imuFrame builds an f64 frame on packet "imu" for the SQL end-to-end test.
func imuFrame(channel string, tsNs int64, v float64) *gantryv1.Frame {
	return &gantryv1.Frame{Packet: "imu", Channel: channel, TimestampNs: uint64(tsNs),
		Value: &gantryv1.Value{Kind: &gantryv1.Value_F64{F64: v}}}
}

// TestSQLEndToEnd drives the full durable path: ingest → JetStream → segment
// flush → DuckDB SQL over the Parquet segments, through the real POST /sql
// handler and the mcp.SQLRunner adapter. Skips when no DuckDB binary is present.
func TestSQLEndToEnd(t *testing.T) {
	if _, ok := (duckdb.EnvProvider{}).Binary(); !ok {
		t.Skip("GANTRY_DUCKDB not set; skipping SQL end-to-end test")
	}
	ctx := context.Background()
	dir := t.TempDir()

	bus, err := stream.NewEmbedded(filepath.Join(dir, "js"))
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	if err := bus.EnsureStream(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := edgedb.Open(ctx, filepath.Join(dir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	p, err := NewPersistence(ctx, dir, db, bus)
	if err != nil {
		t.Fatal(err)
	}
	if p.SQL == nil {
		t.Fatal("expected DuckDB engine to be available (GANTRY_DUCKDB set)")
	}
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}

	engine := ingest.New(bus, registry.New())
	publish := func(device string, seq uint64, frames ...*gantryv1.Frame) {
		if _, err := engine.PublishBatch(ctx, &gantryv1.FrameBatch{DeviceId: device, Sequence: seq, Frames: frames}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	publish("rover-1", 1, imuFrame("pitch_deg", 1000, 1.5), imuFrame("pitch_deg", 2000, 2.5))
	publish("rover-1", 2, imuFrame("roll_deg", 2000, -3.0))
	publish("rover-2", 3, imuFrame("pitch_deg", 1500, 9.0))

	// Wait for the flusher to drain, then force a flush so segments exist.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p.Writer.FlushSeq() > 0 {
			break
		}
		_ = p.Writer.Flush(ctx)
		time.Sleep(50 * time.Millisecond)
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	// --- POST /sql handler ---
	h := NewSQLHandler(p.SQL)
	body := `{"sql":"SELECT device, count(*) AS n FROM tlm GROUP BY device ORDER BY device"}`
	req := httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sql status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res duckdb.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if len(res.Rows) != 2 {
		t.Fatalf("device groups = %d, want 2: %+v", len(res.Rows), res.Rows)
	}

	// --- MCP SQLRunner adapter ---
	runner := NewSQLRunner(p.SQL)
	r2, err := runner.Query(ctx, "SELECT count(*) AS n FROM tlm WHERE channel = 'pitch_deg'")
	if err != nil {
		t.Fatalf("runner query: %v", err)
	}
	if len(r2.Rows) != 1 {
		t.Fatalf("runner rows = %d, want 1", len(r2.Rows))
	}
	// 3 pitch_deg samples were ingested (2 on rover-1, 1 on rover-2).
	if n, _ := r2.Rows[0]["n"].(float64); int(n) != 3 {
		t.Fatalf("pitch_deg count = %v, want 3", r2.Rows[0]["n"])
	}

	// --- read-only rejection through the HTTP surface ---
	bad := httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader(`{"sql":"DROP VIEW tlm"}`))
	brec := httptest.NewRecorder()
	h.ServeHTTP(brec, bad)
	if brec.Code != http.StatusBadRequest {
		t.Fatalf("DROP rejected status = %d, want 400", brec.Code)
	}
}
