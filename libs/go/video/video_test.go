package video_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sammyjroberts/gantry/libs/go/blob"
	"github.com/Sammyjroberts/gantry/libs/go/edgedb"
	"github.com/Sammyjroberts/gantry/libs/go/video"
)

// memBlob is an in-memory BlobStore fake satisfying video.BlobStore structurally.
type memBlob struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemBlob() *memBlob { return &memBlob{m: map[string][]byte{}} }

func (b *memBlob) Put(_ context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[key] = data
	return nil
}

func (b *memBlob) Get(_ context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.m[key]
	if !ok {
		return nil, blob.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), nil
}

func (b *memBlob) List(_ context.Context, prefix string) ([]blob.ObjectInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []blob.ObjectInfo
	for k, v := range b.m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, blob.ObjectInfo{Key: k, Size: int64(len(v))})
		}
	}
	return out, nil
}

func (b *memBlob) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.m[key]; !ok {
		return blob.ErrNotFound
	}
	delete(b.m, key)
	return nil
}

func (b *memBlob) has(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.m[key]
	return ok
}

func (b *memBlob) keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for k := range b.m {
		out = append(out, k)
	}
	return out
}

func newSvc(t *testing.T, opts ...video.Option) (*video.Service, *memBlob) {
	t.Helper()
	db, err := edgedb.Open(context.Background(), filepath.Join(t.TempDir(), "edge.db"))
	if err != nil {
		t.Fatalf("edgedb.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	blob := newMemBlob()
	return video.NewService(video.NewStore(db), blob, opts...), blob
}

// TestIngestAndGetRoundTrip proves an uploaded chunk comes back byte-exact with
// its metadata, and that the blob landed under the documented key.
func TestIngestAndGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, blob := newSvc(t)

	payload := []byte("\x1aE\xdf\xa3fake-webm-bytes")
	start := time.Now().UnixNano()
	id, err := svc.IngestChunk(ctx, "cam-front", start, 2000, "video/webm", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("IngestChunk: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}

	// Blob must exist at video/<camera>/<start>.webm.
	wantKey := "video/cam-front/" + itoa(start) + ".webm"
	if !blob.has(wantKey) {
		t.Fatalf("blob key %q missing; have %v", wantKey, blob.keys())
	}

	chunk, rc, err := svc.GetChunk(ctx, id)
	if err != nil {
		t.Fatalf("GetChunk: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Fatalf("bytes not exact: got %q want %q", got, payload)
	}
	if chunk.CameraID != "cam-front" || chunk.Mime != "video/webm" || chunk.StartNs != start ||
		chunk.DurationMs != 2000 || chunk.Bytes != int64(len(payload)) {
		t.Fatalf("metadata mismatch: %+v", chunk)
	}
}

// TestMimeRejection rejects a mime outside the allowlist and stores nothing.
func TestMimeRejection(t *testing.T) {
	ctx := context.Background()
	svc, blob := newSvc(t)
	_, err := svc.IngestChunk(ctx, "cam", time.Now().UnixNano(), 2000, "video/avi", bytes.NewReader([]byte("x")))
	if !errors.Is(err, video.ErrUnsupportedMime) {
		t.Fatalf("err = %v, want ErrUnsupportedMime", err)
	}
	if len(blob.keys()) != 0 {
		t.Fatalf("rejected upload left blobs: %v", blob.keys())
	}
}

// TestSizeCap rejects a body over the configured cap.
func TestSizeCap(t *testing.T) {
	ctx := context.Background()
	svc, blob := newSvc(t, video.WithMaxChunkBytes(16))
	_, err := svc.IngestChunk(ctx, "cam", time.Now().UnixNano(), 2000, "video/webm", bytes.NewReader(make([]byte, 17)))
	if !errors.Is(err, video.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	// Exactly at the cap must succeed.
	if _, err := svc.IngestChunk(ctx, "cam", time.Now().UnixNano(), 2000, "video/webm", bytes.NewReader(make([]byte, 16))); err != nil {
		t.Fatalf("at-cap ingest: %v", err)
	}
	if len(blob.keys()) != 1 {
		t.Fatalf("blob count = %d, want 1 (only the at-cap chunk)", len(blob.keys()))
	}
}

// TestCameraValidation rejects unsafe camera ids so no traversal reaches a key.
func TestCameraValidation(t *testing.T) {
	ctx := context.Background()
	svc, blob := newSvc(t)
	for _, bad := range []string{"", "../etc", "a/b", "cam..1", "cam id", "cam.front"} {
		if _, err := svc.IngestChunk(ctx, bad, 1, 1, "video/webm", bytes.NewReader([]byte("x"))); !errors.Is(err, video.ErrInvalid) {
			t.Fatalf("camera %q err = %v, want ErrInvalid", bad, err)
		}
	}
	if len(blob.keys()) != 0 {
		t.Fatalf("invalid cameras produced blobs: %v", blob.keys())
	}
}

// TestStartDurationValidation rejects non-positive start/duration.
func TestStartDurationValidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	if _, err := svc.IngestChunk(ctx, "cam", 0, 2000, "video/webm", bytes.NewReader([]byte("x"))); !errors.Is(err, video.ErrInvalid) {
		t.Fatalf("start=0 err = %v, want ErrInvalid", err)
	}
	if _, err := svc.IngestChunk(ctx, "cam", 1, 0, "video/webm", bytes.NewReader([]byte("x"))); !errors.Is(err, video.ErrInvalid) {
		t.Fatalf("duration=0 err = %v, want ErrInvalid", err)
	}
}

// TestListChunksWindow lists a camera's chunks and honors from/to bounds.
func TestListChunksWindow(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	base := time.Now().UnixNano()
	for i := int64(0); i < 5; i++ {
		if _, err := svc.IngestChunk(ctx, "cam", base+i*1_000_000, 2000, "video/webm", bytes.NewReader([]byte{byte(i)})); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}
	// Another camera that must not appear.
	if _, err := svc.IngestChunk(ctx, "other", base, 2000, "video/mp4", bytes.NewReader([]byte("z"))); err != nil {
		t.Fatalf("ingest other: %v", err)
	}

	all, err := svc.ListChunks(ctx, "cam", 0, 0)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("all = %d, want 5", len(all))
	}
	// Ascending by start.
	for i := 1; i < len(all); i++ {
		if all[i].StartNs <= all[i-1].StartNs {
			t.Fatalf("not ascending at %d", i)
		}
	}
	// Windowed: [base+1e6, base+3e6] inclusive → 3 chunks.
	win, err := svc.ListChunks(ctx, "cam", base+1_000_000, base+3_000_000)
	if err != nil {
		t.Fatalf("windowed ListChunks: %v", err)
	}
	if len(win) != 3 {
		t.Fatalf("window = %d, want 3", len(win))
	}
}

// TestListCameras returns distinct cameras with latest start.
func TestListCameras(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	base := time.Now().UnixNano()
	_, _ = svc.IngestChunk(ctx, "cam-a", base+10, 2000, "video/webm", bytes.NewReader([]byte("1")))
	_, _ = svc.IngestChunk(ctx, "cam-a", base+30, 2000, "video/webm", bytes.NewReader([]byte("2")))
	_, _ = svc.IngestChunk(ctx, "cam-b", base+20, 2000, "video/webm", bytes.NewReader([]byte("3")))

	cams, err := svc.ListCameras(ctx)
	if err != nil {
		t.Fatalf("ListCameras: %v", err)
	}
	if len(cams) != 2 {
		t.Fatalf("cameras = %d, want 2", len(cams))
	}
	latest := map[string]int64{}
	for _, c := range cams {
		latest[c.CameraID] = c.LatestStartNs
	}
	if latest["cam-a"] != base+30 {
		t.Fatalf("cam-a latest = %d, want %d", latest["cam-a"], base+30)
	}
	if latest["cam-b"] != base+20 {
		t.Fatalf("cam-b latest = %d, want %d", latest["cam-b"], base+20)
	}
}

// TestPruneDeletesRowAndBlob proves retention removes both the row and the blob
// for stale chunks and keeps fresh ones.
func TestPruneDeletesRowAndBlob(t *testing.T) {
	ctx := context.Background()
	svc, blob := newSvc(t)

	now := time.Now()
	oldStart := now.Add(-48 * time.Hour).UnixNano()
	freshStart := now.Add(-1 * time.Hour).UnixNano()
	oldID, _ := svc.IngestChunk(ctx, "cam", oldStart, 2000, "video/webm", bytes.NewReader([]byte("old")))
	freshID, _ := svc.IngestChunk(ctx, "cam", freshStart, 2000, "video/webm", bytes.NewReader([]byte("new")))

	cutoff := now.Add(-24 * time.Hour).UnixNano()
	n, err := svc.Prune(ctx, cutoff)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}

	// Old gone (row + blob), fresh intact.
	if _, _, err := svc.GetChunk(ctx, oldID); !errors.Is(err, video.ErrNotFound) {
		t.Fatalf("old chunk still present: %v", err)
	}
	if blob.has("video/cam/" + itoa(oldStart) + ".webm") {
		t.Fatal("old blob not deleted")
	}
	if _, _, err := svc.GetChunk(ctx, freshID); err != nil {
		t.Fatalf("fresh chunk pruned: %v", err)
	}
}

// TestGetChunkNotFound: unknown id → ErrNotFound.
func TestGetChunkNotFound(t *testing.T) {
	svc, _ := newSvc(t)
	if _, _, err := svc.GetChunk(context.Background(), "deadbeef"); !errors.Is(err, video.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
