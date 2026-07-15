package segments

import (
	"context"
	"testing"
	"time"
)

// failOnceCatalog wraps a Catalog and fails the first Add call, simulating a
// crash in the flush window AFTER the blob has been written but BEFORE the
// catalog row + checkpoint are committed (see Writer.flushDevice: "blob first,
// catalog second ... A crash between the two leaves an orphan blob"). The
// deterministic blob key means the retry overwrites the orphan rather than
// creating a duplicate.
type failOnceCatalog struct {
	Catalog
	failed bool
}

func (c *failOnceCatalog) Add(ctx context.Context, m SegmentMeta, highWaterSeq uint64) error {
	if !c.failed {
		c.failed = true
		return context.DeadlineExceeded // stand-in for a crash/IO failure at commit
	}
	return c.Catalog.Add(ctx, m, highWaterSeq)
}

// TestFlushCrashWindowNoDuplicates proves the documented at-least-once /
// idempotent-key behavior: when a flush writes the segment blob but then fails
// to commit the catalog row (the crash window), the checkpoint is NOT advanced
// and the rows are re-buffered. The retry re-writes the SAME deterministic blob
// key and commits exactly one catalog row, so a reader sees each row exactly
// once — no duplicates, no loss.
func TestFlushCrashWindowNoDuplicates(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.publish(t, "rover-1", 1, f64Frame("imu", "pitch_deg", 1000, 1.5))
	h.publish(t, "rover-1", 2, f64Frame("imu", "pitch_deg", 2000, 2.5))

	cat := &failOnceCatalog{Catalog: h.cat}
	// Long interval so only our explicit Flush calls fire; MaxBytes default so no
	// inline size flush races the test.
	w := NewWriter(h.bus, h.store, cat, Options{MaxInterval: time.Hour})
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitBuffered(t, w, 2)

	// First flush: blob is written, catalog Add fails -> rows re-buffered, the
	// recovery checkpoint stays at 0 (nothing durably committed).
	if err := w.Flush(ctx); err == nil {
		t.Fatal("expected first flush to surface the injected catalog failure")
	}
	if seq := w.FlushSeq(); seq != 0 {
		t.Fatalf("checkpoint advanced to %d despite failed Add; want 0", seq)
	}

	// Retry: same rows, same deterministic blob key (overwrites the orphan),
	// catalog Add now succeeds and advances the checkpoint.
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("retry flush: %v", err)
	}
	if seq := w.FlushSeq(); seq == 0 {
		t.Fatal("checkpoint not advanced after successful retry")
	}
	if err := w.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Exactly one segment, and each row read back exactly once.
	segs, err := h.cat.Overlapping(ctx, "rover-1", 0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 {
		t.Fatalf("want exactly 1 committed segment after crash-retry, got %d", len(segs))
	}
	r := NewReader(h.store, h.cat)
	got := readAll(t, r, "rover-1", nil, 0, 1<<62)
	if len(got) != 2 {
		t.Fatalf("read %d rows after crash-window retry, want 2 (no dup, no loss): %+v", len(got), got)
	}
}
