package segments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Sammyjroberts/gantry/core/go/blob"
	"github.com/Sammyjroberts/gantry/core/go/query"
	"github.com/parquet-go/parquet-go"
)

// Compile-time assertion that *Reader satisfies the query planner's segment seam.
var _ query.SegmentReader = (*Reader)(nil)

// Reader answers bounded time-range queries against the segment store. It is the
// historical read path: catalog overlap lookup narrows to the segments touching
// the window, then within each segment the Parquet page index prunes row groups
// whose ts_ns bounds miss the window, so a wide range is O(segments + overlapping
// row groups) rather than O(all rows). Column pruning is implicit: only the
// columns of Row are materialised.
type Reader struct {
	blob blob.Store
	cat  Catalog
}

// NewReader builds a reader over a blob store and catalog.
func NewReader(store blob.Store, cat Catalog) *Reader {
	return &Reader{blob: store, cat: cat}
}

// rowSchema is the Parquet schema derived from the Row struct. Reconstructing
// against it (rather than the schema parsed from a file's metadata) decodes rows
// into Row values instead of generic maps. Segments are always written from the
// same Row type, so the leaf column order matches.
var rowSchema = parquet.SchemaOf(Row{})

// Read streams every stored Row in [startNs, endNs] for the device/channel
// filter to visit, in ascending segment (start_ns) order; within a segment rows
// are in stored order (the caller sorts per series if needed, as the replay path
// does). deviceID == "" spans all devices; empty channels spans all channels.
// Returning an error from visit aborts the read.
func (r *Reader) Read(ctx context.Context, deviceID string, channels []string, startNs, endNs int64, visit func(Row) error) error {
	segs, err := r.cat.Overlapping(ctx, deviceID, startNs, endNs)
	if err != nil {
		return err
	}
	var want map[string]struct{}
	if len(channels) > 0 {
		want = make(map[string]struct{}, len(channels))
		for _, c := range channels {
			want[c] = struct{}{}
		}
	}
	for _, seg := range segs {
		if err := r.readSegment(ctx, seg, want, startNs, endNs, visit); err != nil {
			return err
		}
	}
	return nil
}

// readSegment reads one segment file, pruning row groups by ts_ns page-index
// bounds and filtering rows by channel and [startNs, endNs].
func (r *Reader) readSegment(ctx context.Context, seg SegmentMeta, want map[string]struct{}, startNs, endNs int64, visit func(Row) error) error {
	rc, err := r.blob.Get(ctx, seg.BlobKey)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			// Catalog references a missing blob: skip rather than fail the whole
			// query (a compaction/GC race). Surfaces in logs upstream if needed.
			return nil
		}
		return err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("segments: read blob %q: %w", seg.BlobKey, err)
	}
	f, err := parquet.OpenFile(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("segments: open %q: %w", seg.BlobKey, err)
	}
	// Prune row groups using the physical (file) schema's leaf order to locate
	// the ts_ns column chunk, but reconstruct rows against the Go-type schema:
	// a schema parsed from file metadata reconstructs into a map, whereas
	// rowSchema (derived from the Row struct) reconstructs into the struct.
	tsCol := leafColumn(f.Schema(), "ts_ns")

	for _, rg := range f.RowGroups() {
		if tsCol >= 0 {
			lo, hi, ok := rowGroupTsBounds(rg, tsCol)
			if ok && (hi < startNs || lo > endNs) {
				continue // no row in this group can fall in the window
			}
		}
		if err := readRowGroup(rg, rowSchema, func(row Row) bool {
			if row.TsNs < startNs || row.TsNs > endNs {
				return false
			}
			if want != nil {
				if _, ok := want[row.Channel]; !ok {
					return false
				}
			}
			return true
		}, visit); err != nil {
			return err
		}
	}
	return nil
}

// readRowGroup materialises rows from one row group, reconstructing each into a
// Row and passing those the predicate accepts to visit.
func readRowGroup(rg parquet.RowGroup, schema *parquet.Schema, keep func(Row) bool, visit func(Row) error) error {
	rows := rg.Rows()
	defer rows.Close()
	buf := make([]parquet.Row, 256)
	for {
		n, readErr := rows.ReadRows(buf)
		for i := 0; i < n; i++ {
			var out Row
			if err := schema.Reconstruct(&out, buf[i]); err != nil {
				return fmt.Errorf("segments: reconstruct: %w", err)
			}
			if keep(out) {
				if err := visit(out); err != nil {
					return err
				}
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("segments: read rows: %w", readErr)
		}
	}
}

// rowGroupTsBounds derives the [min,max] ts_ns of a row group from its ts_ns
// column page index. ok is false when the index is absent (then the caller must
// not prune).
func rowGroupTsBounds(rg parquet.RowGroup, tsCol int) (min, max int64, ok bool) {
	chunks := rg.ColumnChunks()
	if tsCol >= len(chunks) {
		return 0, 0, false
	}
	idx, err := chunks[tsCol].ColumnIndex()
	if err != nil {
		return 0, 0, false
	}
	n := idx.NumPages()
	for i := 0; i < n; i++ {
		if idx.NullPage(i) {
			continue
		}
		lo := idx.MinValue(i).Int64()
		hi := idx.MaxValue(i).Int64()
		if !ok {
			min, max, ok = lo, hi, true
			continue
		}
		if lo < min {
			min = lo
		}
		if hi > max {
			max = hi
		}
	}
	return min, max, ok
}

// ReadRange adapts the Reader to query.SegmentReader: it streams stored samples
// as (series key, sample) pairs so the query planner can merge segment history
// with the JetStream tail on one path. It reuses Row.SeriesKey/Row.Sample for
// the projection.
func (r *Reader) ReadRange(ctx context.Context, deviceID string, channels []string, startNs, endNs int64, visit func(query.SeriesKey, query.Sample) error) error {
	return r.Read(ctx, deviceID, channels, startNs, endNs, func(row Row) error {
		return visit(row.SeriesKey(), row.Sample())
	})
}

// Horizon reports the durable retention horizon (min start, max end across all
// segments), delegating to the catalog. It completes the query.SegmentReader
// interface.
func (r *Reader) Horizon(ctx context.Context) (minStartNs, maxEndNs int64, ok bool, err error) {
	return r.cat.Horizon(ctx)
}

// leafColumn returns the leaf column index whose path is exactly [name], or -1.
func leafColumn(schema *parquet.Schema, name string) int {
	for i, path := range schema.Columns() {
		if len(path) == 1 && path[0] == name {
			return i
		}
	}
	return -1
}
