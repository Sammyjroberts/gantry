package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sammyjroberts/gantry/apps/edge/internal/server"
	"github.com/Sammyjroberts/gantry/libs/go/blob"
	"github.com/Sammyjroberts/gantry/libs/go/edgedb"
	"github.com/Sammyjroberts/gantry/libs/go/models"
	"github.com/Sammyjroberts/gantry/libs/go/video"
)

// memBlob is an in-memory BlobStore fake shared by the video and models handler
// tests. It satisfies both video.BlobStore and models.BlobStore structurally.
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

// newVideoServer wires a standalone httptest server with just the video routes
// over a temp SQLite catalog and an in-memory blob store. It intentionally does
// not go through server.New (which the coordinator owns); it exercises the
// Register-style surface directly.
func newVideoServer(t *testing.T, opts ...video.Option) (*httptest.Server, *memBlob) {
	t.Helper()
	db, err := edgedb.Open(context.Background(), filepath.Join(t.TempDir(), "edge.db"))
	if err != nil {
		t.Fatalf("edgedb.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	blob := newMemBlob()
	svc := video.NewService(video.NewStore(db), blob, opts...)
	mux := http.NewServeMux()
	server.RegisterVideo(mux, svc)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, blob
}

func newModelsServer(t *testing.T, opts ...models.Option) (*httptest.Server, *memBlob) {
	t.Helper()
	blob := newMemBlob()
	svc := models.NewService(blob, opts...)
	mux := http.NewServeMux()
	server.RegisterModels(mux, svc)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, blob
}

// TestVideoRoundTrip is the full handler slice: upload → list → fetch byte-exact
// → cameras.
func TestVideoRoundTrip(t *testing.T) {
	ts, _ := newVideoServer(t)
	payload := []byte("\x1aE\xdf\xa3webm-payload-bytes")
	start := time.Now().UnixNano()

	// Upload.
	url := ts.URL + "/video/chunks?camera=cam-front&start_ns=" + itoa(start) + "&duration_ms=2000"
	resp, err := http.Post(url, "video/webm", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201", resp.StatusCode)
	}
	var created struct {
		ID string `json:"id"`
	}
	decodeJSON(t, resp, &created)
	if created.ID == "" {
		t.Fatal("empty id in response")
	}

	// List.
	lresp := httpGet(t, ts.URL+"/video/chunks?camera=cam-front")
	if lresp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200", lresp.StatusCode)
	}
	var listed struct {
		Chunks []video.Chunk `json:"chunks"`
	}
	decodeJSON(t, lresp, &listed)
	if len(listed.Chunks) != 1 || listed.Chunks[0].ID != created.ID {
		t.Fatalf("list = %+v, want the one chunk", listed.Chunks)
	}
	if listed.Chunks[0].StartNs != start || listed.Chunks[0].Bytes != int64(len(payload)) {
		t.Fatalf("chunk metadata wrong: %+v", listed.Chunks[0])
	}

	// Fetch bytes byte-exact with the stored mime.
	fresp := httpGet(t, ts.URL+"/video/chunks/"+created.ID)
	if fresp.StatusCode != http.StatusOK {
		t.Fatalf("fetch status = %d, want 200", fresp.StatusCode)
	}
	if ct := fresp.Header.Get("Content-Type"); ct != "video/webm" {
		t.Fatalf("fetch Content-Type = %q, want video/webm", ct)
	}
	body := readAll(t, fresp)
	if !bytes.Equal(body, payload) {
		t.Fatalf("fetched bytes not exact")
	}

	// Cameras.
	cresp := httpGet(t, ts.URL+"/video/cameras")
	var cams struct {
		Cameras []video.Camera `json:"cameras"`
	}
	decodeJSON(t, cresp, &cams)
	if len(cams.Cameras) != 1 || cams.Cameras[0].CameraID != "cam-front" || cams.Cameras[0].LatestStartNs != start {
		t.Fatalf("cameras = %+v, want cam-front@%d", cams.Cameras, start)
	}
}

// TestVideoMimeRejection: a disallowed Content-Type is 415.
func TestVideoMimeRejection(t *testing.T) {
	ts, blob := newVideoServer(t)
	url := ts.URL + "/video/chunks?camera=cam&start_ns=1&duration_ms=2000"
	resp, err := http.Post(url, "video/avi", bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", resp.StatusCode)
	}
	if len(blob.m) != 0 {
		t.Fatal("rejected upload stored a blob")
	}
}

// TestVideoSizeCapPreRead: a body over the cap is rejected 413. With a real
// Content-Length the handler rejects pre-read.
func TestVideoSizeCapPreRead(t *testing.T) {
	ts, blob := newVideoServer(t, video.WithMaxChunkBytes(16))
	url := ts.URL + "/video/chunks?camera=cam&start_ns=1&duration_ms=2000"
	resp, err := http.Post(url, "video/webm", bytes.NewReader(make([]byte, 100)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	if len(blob.m) != 0 {
		t.Fatal("oversize upload stored a blob")
	}
}

// TestVideoUnknownChunk404: fetching an unknown id is 404.
func TestVideoUnknownChunk404(t *testing.T) {
	ts, _ := newVideoServer(t)
	if got := httpGet(t, ts.URL+"/video/chunks/deadbeef").StatusCode; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

// TestVideoListRequiresCamera: list without a camera is 400.
func TestVideoListRequiresCamera(t *testing.T) {
	ts, _ := newVideoServer(t)
	if got := httpGet(t, ts.URL+"/video/chunks").StatusCode; got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", got)
	}
}

// ---- small helpers (shared with models_e2e_test.go) ----

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode json: %v", err)
	}
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}
