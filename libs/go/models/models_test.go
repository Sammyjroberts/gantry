package models_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/Sammyjroberts/gantry/libs/go/blob"
	"github.com/Sammyjroberts/gantry/libs/go/models"
)

// memBlob is an in-memory BlobStore fake satisfying models.BlobStore.
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

func (b *memBlob) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.m)
}

// TestPutGetRoundTrip stores a URDF and reads it back with the right content type.
func TestPutGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	blob := newMemBlob()
	svc := models.NewService(blob)

	body := []byte(`<robot name="arm"/>`)
	if err := svc.Put(ctx, "arm.urdf", bytes.NewReader(body)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !blob.has("models/arm.urdf") {
		t.Fatal("blob not stored under models/ prefix")
	}
	rc, ct, err := svc.Get(ctx, "arm.urdf")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, body) {
		t.Fatalf("bytes = %q, want %q", got, body)
	}
	if ct != "application/xml" {
		t.Fatalf("content type = %q, want application/xml", ct)
	}
}

// TestContentTypes covers each allowed extension's served type.
func TestContentTypes(t *testing.T) {
	ctx := context.Background()
	svc := models.NewService(newMemBlob())
	cases := map[string]string{
		"a.urdf": "application/xml",
		"a.stl":  "model/stl",
		"a.glb":  "model/gltf-binary",
		"a.dae":  "application/xml",
	}
	for name, wantCT := range cases {
		if err := svc.Put(ctx, name, bytes.NewReader([]byte("data"))); err != nil {
			t.Fatalf("Put %s: %v", name, err)
		}
		_, ct, err := svc.Get(ctx, name)
		if err != nil {
			t.Fatalf("Get %s: %v", name, err)
		}
		if ct != wantCT {
			t.Fatalf("%s content type = %q, want %q", name, ct, wantCT)
		}
	}
}

// TestExtensionAllowlist rejects extensions outside the allowlist.
func TestExtensionAllowlist(t *testing.T) {
	ctx := context.Background()
	blob := newMemBlob()
	svc := models.NewService(blob)
	for _, name := range []string{"a.txt", "a.exe", "noext", "a.png"} {
		if err := svc.Put(ctx, name, bytes.NewReader([]byte("x"))); !errors.Is(err, models.ErrUnsupportedExt) {
			t.Fatalf("Put %q err = %v, want ErrUnsupportedExt", name, err)
		}
	}
	if blob.count() != 0 {
		t.Fatal("rejected files were stored")
	}
}

// TestTraversalRejected proves no name with path structure is accepted.
func TestTraversalRejected(t *testing.T) {
	ctx := context.Background()
	blob := newMemBlob()
	svc := models.NewService(blob)
	bad := []string{
		"../secret.urdf",
		"../../etc/passwd.urdf",
		"a/b.urdf",
		`a\b.urdf`,
		"..urdf",       // contains ".."
		".hidden.urdf", // hidden
		"foo/../bar.urdf",
	}
	for _, name := range bad {
		if err := svc.Put(ctx, name, bytes.NewReader([]byte("x"))); !errors.Is(err, models.ErrInvalid) {
			t.Fatalf("Put %q err = %v, want ErrInvalid", name, err)
		}
		if _, _, err := svc.Get(ctx, name); !errors.Is(err, models.ErrInvalid) {
			t.Fatalf("Get %q err = %v, want ErrInvalid", name, err)
		}
	}
	if blob.count() != 0 {
		t.Fatalf("traversal names stored blobs: keys present")
	}
}

// TestSizeCap enforces the byte cap.
func TestSizeCap(t *testing.T) {
	ctx := context.Background()
	svc := models.NewService(newMemBlob(), models.WithMaxFileBytes(8))
	if err := svc.Put(ctx, "a.stl", bytes.NewReader(make([]byte, 9))); !errors.Is(err, models.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if err := svc.Put(ctx, "a.stl", bytes.NewReader(make([]byte, 8))); err != nil {
		t.Fatalf("at-cap Put: %v", err)
	}
}

// TestListSortedBaseNames returns sorted base names, prefix stripped.
func TestListSortedBaseNames(t *testing.T) {
	ctx := context.Background()
	svc := models.NewService(newMemBlob())
	for _, n := range []string{"zoo.glb", "arm.urdf", "base.stl"} {
		if err := svc.Put(ctx, n, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatalf("Put %s: %v", n, err)
		}
	}
	files, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"arm.urdf", "base.stl", "zoo.glb"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Fatalf("List = %v, want %v", files, want)
	}
}

// TestGetMissingIsNotFound surfaces blob.ErrNotFound for an unknown name (so the
// server maps it to 404).
func TestGetMissingIsNotFound(t *testing.T) {
	_, _, err := models.NewService(newMemBlob()).Get(context.Background(), "gone.urdf")
	if !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("err = %v, want blob.ErrNotFound", err)
	}
}
