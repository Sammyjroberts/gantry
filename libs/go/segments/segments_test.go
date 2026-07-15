package segments

import (
	"context"
	"testing"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/libs/go/blob"
	"github.com/Sammyjroberts/gantry/libs/go/edgedb"
	"github.com/Sammyjroberts/gantry/libs/go/ingest"
	"github.com/Sammyjroberts/gantry/libs/go/registry"
	"github.com/Sammyjroberts/gantry/libs/go/stream"
)

// harness wires an embedded bus, a blob store, a catalog, and an ingest engine
// in a temp dir — the same parts Edge assembles, at test scale.
type harness struct {
	bus    *stream.Bus
	store  *blob.FS
	cat    *SQLCatalog
	engine *ingest.Engine
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()

	bus, err := stream.NewEmbedded(dir)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(bus.Close)
	if err := bus.EnsureStream(ctx); err != nil {
		t.Fatalf("ensure stream: %v", err)
	}
	db, err := edgedb.Open(ctx, dir+"/edge.db")
	if err != nil {
		t.Fatalf("edgedb: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := blob.NewFS(dir + "/blobs")
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	return &harness{
		bus:    bus,
		store:  store,
		cat:    NewSQLCatalog(db),
		engine: ingest.New(bus, registry.New()),
	}
}

// publish sends one batch (device, seq) with the given frames through the ingest
// engine so received_ns is stamped exactly as in production.
func (h *harness) publish(t *testing.T, device string, seq uint64, frames ...*gantryv1.Frame) {
	t.Helper()
	batch := &gantryv1.FrameBatch{DeviceId: device, Sequence: seq, Frames: frames}
	if _, err := h.engine.PublishBatch(context.Background(), batch); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

func f64Frame(packet, channel string, tsNs int64, v float64) *gantryv1.Frame {
	return &gantryv1.Frame{Packet: packet, Channel: channel, TimestampNs: uint64(tsNs),
		Value: &gantryv1.Value{Kind: &gantryv1.Value_F64{F64: v}}}
}

func textFrame(channel string, tsNs int64, s string) *gantryv1.Frame {
	return &gantryv1.Frame{Channel: channel, TimestampNs: uint64(tsNs),
		Value: &gantryv1.Value{Kind: &gantryv1.Value_Text{Text: s}}}
}

// waitBuffered polls until the writer has buffered at least n rows or times out.
func waitBuffered(t *testing.T, w *Writer, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if w.bufferedRows() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d buffered rows (have %d)", n, w.bufferedRows())
}

func readAll(t *testing.T, r *Reader, device string, channels []string, t1, t2 int64) []Row {
	t.Helper()
	var got []Row
	if err := r.Read(context.Background(), device, channels, t1, t2, func(row Row) error {
		got = append(got, row)
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	return got
}

// TestWriterReaderRoundTrip proves frames drained by the writer are read back
// exactly — including packet, kind, value, and the server-stamped received_ns.
func TestWriterReaderRoundTrip(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.publish(t, "rover-1", 1, f64Frame("imu", "pitch_deg", 1000, 1.5), f64Frame("imu", "roll_deg", 1000, -2.5))
	h.publish(t, "rover-1", 2, f64Frame("imu", "pitch_deg", 2000, 3.0), textFrame("mode", 2000, "armed"))

	w := NewWriter(h.bus, h.store, h.cat, Options{MaxInterval: time.Hour})
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitBuffered(t, w, 4)
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := w.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	r := NewReader(h.store, h.cat)
	got := readAll(t, r, "rover-1", nil, 0, 1<<62)
	if len(got) != 4 {
		t.Fatalf("read %d rows, want 4: %+v", len(got), got)
	}

	// received_ns must be populated (server-stamped) and identical within a batch.
	for _, row := range got {
		if row.ReceivedNs == 0 {
			t.Fatalf("row has zero received_ns: %+v", row)
		}
	}
	// Verify the text frame round-tripped with its value and kind.
	var sawText bool
	for _, row := range got {
		if row.Channel == "mode" {
			sawText = true
			if row.Kind_() != gantryv1.ValueKind_VALUE_KIND_TEXT || row.VStr != "armed" {
				t.Fatalf("text row wrong: %+v", row)
			}
		}
	}
	if !sawText {
		t.Fatal("text frame not read back")
	}

	// Channel filter + time filter.
	pitch := readAll(t, r, "rover-1", []string{"pitch_deg"}, 0, 1500)
	if len(pitch) != 1 || pitch[0].VF64 != 1.5 {
		t.Fatalf("filtered read wrong: %+v", pitch)
	}
}

// TestRotation forces a size-based rotation and checks multiple segments are
// written and all rows are still readable across them.
func TestRotation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Tiny MaxBytes so each batch's frames rotate almost immediately.
	w := NewWriter(h.bus, h.store, h.cat, Options{MaxBytes: 1, MaxInterval: time.Hour})
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	const batches = 5
	for i := 0; i < batches; i++ {
		ts := int64(1000 * (i + 1))
		h.publish(t, "rover-1", uint64(i+1), f64Frame("imu", "pitch_deg", ts, float64(i)))
	}
	// Drain: with MaxBytes=1 the inline flush fires per message; wait until the
	// catalog holds all segments.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		segs, _ := h.cat.Overlapping(ctx, "rover-1", 0, 1<<62)
		if len(segs) >= batches {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = w.Flush(ctx)
	if err := w.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	segs, err := h.cat.Overlapping(ctx, "rover-1", 0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) < 2 {
		t.Fatalf("expected multiple segments from rotation, got %d", len(segs))
	}

	r := NewReader(h.store, h.cat)
	got := readAll(t, r, "rover-1", nil, 0, 1<<62)
	if len(got) != batches {
		t.Fatalf("read %d rows across segments, want %d", len(got), batches)
	}
}

// TestRecoveryHighWater proves a second writer resumes from the persisted
// checkpoint and does not re-emit already-flushed frames.
func TestRecoveryHighWater(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.publish(t, "rover-1", 1, f64Frame("imu", "pitch_deg", 1000, 1))
	h.publish(t, "rover-1", 2, f64Frame("imu", "pitch_deg", 2000, 2))

	w1 := NewWriter(h.bus, h.store, h.cat, Options{MaxInterval: time.Hour})
	if err := w1.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitBuffered(t, w1, 2)
	if err := w1.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w1.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	hw, err := h.cat.HighWaterSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hw == 0 {
		t.Fatal("high-water not persisted after flush")
	}

	// New frames arrive, then a fresh writer starts — it must resume past hw.
	h.publish(t, "rover-1", 3, f64Frame("imu", "pitch_deg", 3000, 3))

	w2 := NewWriter(h.bus, h.store, h.cat, Options{MaxInterval: time.Hour})
	if err := w2.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitBuffered(t, w2, 1)
	// It should have buffered ONLY the new frame (seq 3), not the two already
	// flushed — resume-from-checkpoint, not re-read-from-zero.
	if n := w2.bufferedRows(); n != 1 {
		t.Fatalf("resumed writer buffered %d rows, want 1 (must skip flushed frames)", n)
	}
	if err := w2.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w2.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	// All three rows are present exactly once across all segments.
	r := NewReader(h.store, h.cat)
	got := readAll(t, r, "rover-1", nil, 0, 1<<62)
	if len(got) != 3 {
		t.Fatalf("read %d rows total, want 3 (no duplicates, no loss)", len(got))
	}
}

// TestCatalogOverlap checks the catalog's overlap predicate and device filter.
func TestCatalogOverlap(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	add := func(id, dev string, s, e int64) {
		if err := h.cat.Add(ctx, SegmentMeta{ID: id, DeviceID: dev, StartNs: s, EndNs: e, BlobKey: "k/" + id, CreatedNs: 1}, 0); err != nil {
			t.Fatal(err)
		}
	}
	add("a", "d1", 100, 200)
	add("b", "d1", 200, 300)
	add("c", "d1", 400, 500)
	add("d", "d2", 150, 250)

	// [210,260] overlaps b (d1) and d (d2); device filter narrows to d1.
	got, err := h.cat.Overlapping(ctx, "d1", 210, 260)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("overlap d1 [210,260] = %+v, want just b", got)
	}
	// All devices in [140,160]: a (d1) and d (d2).
	got, _ = h.cat.Overlapping(ctx, "", 140, 160)
	if len(got) != 2 {
		t.Fatalf("overlap all [140,160] = %d segments, want 2", len(got))
	}
	// Horizon spans everything.
	minS, maxE, ok, err := h.cat.Horizon(ctx)
	if err != nil || !ok || minS != 100 || maxE != 500 {
		t.Fatalf("horizon = (%d,%d,%v,%v), want (100,500,true,nil)", minS, maxE, ok, err)
	}
}
