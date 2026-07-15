// Package video implements chunk-shaped video storage for Gantry. Capture
// clients upload self-contained ~2s WebM/MP4 chunks; each chunk is one
// independently-playable blob plus one catalog row. There are no streaming
// protocols and no long-lived byte streams: live-follow and replay are both
// catalog-driven chunk fetches (list a camera's chunks over a time range, then
// GET each chunk by id). The design is storage-agnostic — a blob store
// interface plus a SQL catalog — so it runs identically on Edge (fs blobs +
// SQLite) and core (s3 blobs + Postgres).
package video

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/Sammyjroberts/gantry/libs/go/blob"
)

// Validation / policy errors. The HTTP surface maps these to status codes.
var (
	// ErrInvalid is a malformed request (bad camera id, non-positive start/duration).
	ErrInvalid = errors.New("invalid video chunk request")
	// ErrUnsupportedMime is a mime type outside the allowlist.
	ErrUnsupportedMime = errors.New("unsupported video mime type")
	// ErrTooLarge is a chunk body over the configured size cap.
	ErrTooLarge = errors.New("video chunk exceeds size limit")
)

const (
	// DefaultMaxChunkBytes caps a single chunk. Self-contained ~2s chunks are
	// far smaller; 16 MiB is generous headroom that still bounds memory (chunks
	// are buffered whole to compute size and to store atomically).
	DefaultMaxChunkBytes = 16 << 20
	// DefaultRetention is how long chunks are kept by the janitor by default.
	DefaultRetention = 24 * time.Hour
	// DefaultJanitorInterval is how often the janitor prunes.
	DefaultJanitorInterval = 5 * time.Minute
	// idBytes → 16 hex chars, collision-negligible at bench scale.
	idBytes = 8
)

// mimeExt is the allowlist of accepted mime types mapped to their blob key
// extension. Anything not here is rejected with ErrUnsupportedMime.
var mimeExt = map[string]string{
	"video/webm": "webm",
	"video/mp4":  "mp4",
}

// Service is the video engine over a catalog Store and a BlobStore. It validates
// uploads, stores blobs, maintains the catalog, and prunes on retention. now is
// injectable for deterministic tests.
type Service struct {
	store         *Store
	blobs         BlobStore
	maxChunkBytes int64
	now           func() time.Time
}

// Option configures a Service.
type Option func(*Service)

// WithMaxChunkBytes overrides the per-chunk size cap.
func WithMaxChunkBytes(n int64) Option {
	return func(s *Service) {
		if n > 0 {
			s.maxChunkBytes = n
		}
	}
}

// WithClock overrides the time source (tests).
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// NewService builds a Service over an already-migrated *sql.DB (via NewStore)
// and an injected blob store.
func NewService(store *Store, blobs BlobStore, opts ...Option) *Service {
	s := &Service{
		store:         store,
		blobs:         blobs,
		maxChunkBytes: DefaultMaxChunkBytes,
		now:           time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// MaxChunkBytes reports the configured per-chunk size cap (the HTTP layer uses
// it for a pre-read Content-Length rejection).
func (s *Service) MaxChunkBytes() int64 { return s.maxChunkBytes }

// IngestChunk validates and stores one chunk. cameraID must be a safe token,
// mime must be in the allowlist, startNs and durationMs must be positive, and
// the body must not exceed the size cap. r is read fully into memory (bounded by
// the cap) so the byte count is known and the blob is written atomically.
//
// Storage order is blob-first, row-second: a visible catalog row therefore
// always has a backing blob. If the row insert fails we make a best-effort blob
// delete; a process crash between the two writes leaves an orphan blob with no
// row, which is acceptable in v1 (orphans are unreferenced and reclaimed by a
// future sweep — never served, since serving is catalog-driven).
func (s *Service) IngestChunk(ctx context.Context, cameraID string, startNs, durationMs int64, mime string, r io.Reader) (string, error) {
	if !validCameraID(cameraID) {
		return "", fmt.Errorf("%w: camera id %q must be non-empty [A-Za-z0-9_-]", ErrInvalid, cameraID)
	}
	if startNs <= 0 {
		return "", fmt.Errorf("%w: start_ns must be positive", ErrInvalid)
	}
	if durationMs <= 0 {
		return "", fmt.Errorf("%w: duration_ms must be positive", ErrInvalid)
	}
	ext, ok := mimeExt[mime]
	if !ok {
		return "", fmt.Errorf("%w: %q (allowed: video/webm, video/mp4)", ErrUnsupportedMime, mime)
	}

	// Read at most cap+1 bytes: if we get cap+1 the body is over the limit.
	data, err := io.ReadAll(io.LimitReader(r, s.maxChunkBytes+1))
	if err != nil {
		return "", fmt.Errorf("video: read body: %w", err)
	}
	if int64(len(data)) > s.maxChunkBytes {
		return "", fmt.Errorf("%w: >%d bytes", ErrTooLarge, s.maxChunkBytes)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("%w: empty chunk body", ErrInvalid)
	}

	id, err := newID()
	if err != nil {
		return "", err
	}
	key := keyFor(cameraID, startNs, ext)

	// Blob first.
	if err := s.blobs.Put(ctx, key, bytes.NewReader(data)); err != nil {
		return "", fmt.Errorf("video: put blob: %w", err)
	}
	// Row second.
	chunk := Chunk{
		ID:         id,
		CameraID:   cameraID,
		StartNs:    startNs,
		DurationMs: durationMs,
		Mime:       mime,
		Bytes:      int64(len(data)),
		CreatedNs:  s.now().UnixNano(),
		BlobKey:    key,
	}
	if err := s.store.Insert(ctx, chunk); err != nil {
		// Best-effort cleanup so a failed insert doesn't orphan the blob.
		_ = s.blobs.Delete(ctx, key)
		return "", err
	}
	return id, nil
}

// ListChunks returns a camera's chunks over [fromNs, toNs] (zero bounds are
// open), ascending by start. This is the query behind both live-follow (poll a
// trailing window) and replay (a fixed past window).
func (s *Service) ListChunks(ctx context.Context, cameraID string, fromNs, toNs int64) ([]Chunk, error) {
	if cameraID == "" {
		return nil, fmt.Errorf("%w: camera is required", ErrInvalid)
	}
	return s.store.List(ctx, cameraID, fromNs, toNs)
}

// ListCameras returns the distinct cameras with their latest chunk start.
func (s *Service) ListCameras(ctx context.Context) ([]Camera, error) {
	return s.store.ListCameras(ctx)
}

// GetChunk resolves a chunk id to its catalog row and an open blob reader. The
// caller must close the reader. ErrNotFound if the id is unknown.
func (s *Service) GetChunk(ctx context.Context, id string) (Chunk, io.ReadCloser, error) {
	c, err := s.store.Get(ctx, id)
	if err != nil {
		return Chunk{}, nil, err
	}
	rc, err := s.blobs.Get(ctx, c.BlobKey)
	if err != nil {
		return Chunk{}, nil, fmt.Errorf("video: get blob %q: %w", c.BlobKey, err)
	}
	return c, rc, nil
}

// Prune deletes every chunk whose start_ns is before olderThanNs, removing the
// catalog row first and then its blob. Row-first is the mirror of ingest's
// blob-first: a surviving row always has a blob, and a crash mid-prune leaves an
// orphan blob (acceptable, reclaimed later) rather than a dangling row. Returns
// the number of chunks pruned. Blob-delete failures are logged and skipped so
// one bad key can't stall retention.
func (s *Service) Prune(ctx context.Context, olderThanNs int64) (int, error) {
	stale, err := s.store.ListOlderThan(ctx, olderThanNs)
	if err != nil {
		return 0, err
	}
	pruned := 0
	for _, c := range stale {
		if err := s.store.Delete(ctx, c.ID); err != nil {
			return pruned, err
		}
		// A blob already gone (ErrNotFound) is a satisfied goal, not a failure.
		if err := s.blobs.Delete(ctx, c.BlobKey); err != nil && !errors.Is(err, blob.ErrNotFound) {
			log.Printf("video: prune: delete blob %q: %v (row removed; blob orphaned)", c.BlobKey, err)
			continue
		}
		pruned++
	}
	return pruned, nil
}

// StartJanitor launches a background goroutine that prunes chunks older than
// retention every interval, until ctx is cancelled. It returns immediately.
// Non-positive retention/interval fall back to the defaults. Prune errors are
// logged (a bench product should not crash on a retention hiccup).
func (s *Service) StartJanitor(ctx context.Context, retention, interval time.Duration) {
	if retention <= 0 {
		retention = DefaultRetention
	}
	if interval <= 0 {
		interval = DefaultJanitorInterval
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cutoff := s.now().Add(-retention).UnixNano()
				if n, err := s.Prune(ctx, cutoff); err != nil {
					log.Printf("video: janitor prune: %v", err)
				} else if n > 0 {
					log.Printf("video: janitor pruned %d chunk(s) older than %s", n, retention)
				}
			}
		}
	}()
}

// keyFor builds the blob key for a chunk: video/<camera>/<start_ns>.<ext>.
// cameraID is pre-validated to a traversal-safe token by validCameraID.
func keyFor(cameraID string, startNs int64, ext string) string {
	return fmt.Sprintf("video/%s/%d.%s", cameraID, startNs, ext)
}

// validCameraID accepts non-empty [A-Za-z0-9_-]+ only. Dots and slashes are
// disallowed so a camera id can never inject ".." or a path separator into a
// blob key (fsblob writes keys as file paths).
func validCameraID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// newID returns a short random hex id from crypto/rand.
func newID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("video: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
