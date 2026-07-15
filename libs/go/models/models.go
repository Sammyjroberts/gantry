// Package models serves per-device model files (URDF, meshes) for Gantry over
// the same blob store abstraction as video. A model file is just a named blob
// under the "models/" prefix — no catalog, no database. The web console fetches
// these to render a robot in 3D (URDF + referenced STL/GLB/DAE meshes). Like
// video, it runs identically on Edge (fs blobs) and core (s3 blobs); only the
// injected BlobStore differs.
package models

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/Sammyjroberts/gantry/libs/go/blob"
)

// Policy errors, mapped to HTTP status codes by the server surface.
var (
	// ErrInvalid is a bad or unsafe filename (traversal, separators, bad name).
	ErrInvalid = errors.New("invalid model file name")
	// ErrUnsupportedExt is an extension outside the allowlist.
	ErrUnsupportedExt = errors.New("unsupported model file extension")
	// ErrTooLarge is a body over the size cap.
	ErrTooLarge = errors.New("model file exceeds size limit")
)

// DefaultMaxFileBytes caps a stored model file (meshes can be large).
const DefaultMaxFileBytes = 32 << 20

// blobPrefix namespaces model blobs so List/Get never collide with video keys.
const blobPrefix = "models/"

// extContentType is the extension allowlist and the Content-Type served for
// each. URDF and DAE are XML; STL and GLB have their own model/* types.
var extContentType = map[string]string{
	".urdf": "application/xml",
	".stl":  "model/stl",
	".glb":  "model/gltf-binary",
	".dae":  "application/xml",
}

// BlobStore is the blob dependency of the models service: a type alias for
// libs/go/blob.Store, the same shared object-storage seam the video service
// uses. See libs/go/video/blob.go for why this is an alias rather than a
// re-declared interface.
type BlobStore = blob.Store

// Service stores and serves model files over a BlobStore.
type Service struct {
	blobs        BlobStore
	maxFileBytes int64
}

// Option configures a Service.
type Option func(*Service)

// WithMaxFileBytes overrides the size cap.
func WithMaxFileBytes(n int64) Option {
	return func(s *Service) {
		if n > 0 {
			s.maxFileBytes = n
		}
	}
}

// NewService builds a Service over an injected blob store.
func NewService(blobs BlobStore, opts ...Option) *Service {
	s := &Service{blobs: blobs, maxFileBytes: DefaultMaxFileBytes}
	for _, o := range opts {
		o(s)
	}
	return s
}

// MaxFileBytes reports the configured size cap (the HTTP layer uses it for a
// pre-read Content-Length rejection).
func (s *Service) MaxFileBytes() int64 { return s.maxFileBytes }

// List returns the model file names (base names, prefix stripped), sorted.
func (s *Service) List(ctx context.Context) ([]string, error) {
	keys, err := s.blobs.List(ctx, blobPrefix)
	if err != nil {
		return nil, fmt.Errorf("models: list: %w", err)
	}
	out := make([]string, 0, len(keys))
	for _, obj := range keys {
		name := strings.TrimPrefix(obj.Key, blobPrefix)
		if name == "" || strings.Contains(name, "/") {
			continue // defensive: models are flat, ignore anything nested
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// Get opens a model file by name, returning the reader and the Content-Type to
// serve. The caller closes the reader. Returns ErrInvalid / ErrUnsupportedExt
// for bad names before touching the store.
func (s *Service) Get(ctx context.Context, name string) (io.ReadCloser, string, error) {
	ct, err := contentType(name)
	if err != nil {
		return nil, "", err
	}
	rc, err := s.blobs.Get(ctx, blobPrefix+name)
	if err != nil {
		return nil, "", fmt.Errorf("models: get %q: %w", name, err)
	}
	return rc, ct, nil
}

// Put stores a model file under name after validating the name, the extension
// allowlist, and the size cap. The body is read fully into memory (bounded by
// the cap) so the size is enforced before the blob is written.
func (s *Service) Put(ctx context.Context, name string, r io.Reader) error {
	if _, err := contentType(name); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(r, s.maxFileBytes+1))
	if err != nil {
		return fmt.Errorf("models: read body: %w", err)
	}
	if int64(len(data)) > s.maxFileBytes {
		return fmt.Errorf("%w: >%d bytes", ErrTooLarge, s.maxFileBytes)
	}
	if len(data) == 0 {
		return fmt.Errorf("%w: empty file", ErrInvalid)
	}
	if err := s.blobs.Put(ctx, blobPrefix+name, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("models: put %q: %w", name, err)
	}
	return nil
}

// contentType validates name and returns the Content-Type for its extension.
// A name is valid iff it is a bare filename (no path separators, no "..", not
// hidden) whose extension is in the allowlist. This is the traversal guard: a
// name that passes here is safe to append to the blob prefix.
func contentType(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalid)
	}
	// Reject any path structure. path.Clean + comparison catches ".", "..",
	// "a/b", leading slashes, and "./x"; the separator checks catch backslashes
	// that Clean would leave intact on the wire.
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || path.Clean(name) != name {
		return "", fmt.Errorf("%w: %q", ErrInvalid, name)
	}
	if strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("%w: %q (hidden)", ErrInvalid, name)
	}
	ext := strings.ToLower(path.Ext(name))
	ct, ok := extContentType[ext]
	if !ok {
		return "", fmt.Errorf("%w: %q (allowed: urdf, stl, glb, dae)", ErrUnsupportedExt, ext)
	}
	return ct, nil
}
