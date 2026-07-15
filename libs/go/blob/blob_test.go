package blob_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/Sammyjroberts/gantry/libs/go/blob"
)

// contractSuite exercises the Store contract independent of implementation. It
// is run against fsblob unconditionally and against s3blob when a MinIO/S3
// endpoint is configured (GANTRY_S3_ENDPOINT).
func contractSuite(t *testing.T, newStore func(t *testing.T) blob.Store) {
	ctx := context.Background()

	t.Run("put_get_roundtrip", func(t *testing.T) {
		s := newStore(t)
		want := []byte("hello gantry \x00\x01 binary")
		if err := s.Put(ctx, "segments/dev1/100-200.parquet", bytes.NewReader(want)); err != nil {
			t.Fatalf("put: %v", err)
		}
		rc, err := s.Get(ctx, "segments/dev1/100-200.parquet")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("roundtrip mismatch: got %q want %q", got, want)
		}
	})

	t.Run("get_missing_is_notfound", func(t *testing.T) {
		s := newStore(t)
		_, err := s.Get(ctx, "segments/nope.parquet")
		if !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("overwrite_replaces", func(t *testing.T) {
		s := newStore(t)
		key := "a/b.bin"
		if err := s.Put(ctx, key, strings.NewReader("first")); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(ctx, key, strings.NewReader("second-longer")); err != nil {
			t.Fatal(err)
		}
		rc, err := s.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		got, _ := io.ReadAll(rc)
		if string(got) != "second-longer" {
			t.Fatalf("overwrite: got %q", got)
		}
	})

	t.Run("list_prefix_ordered", func(t *testing.T) {
		s := newStore(t)
		keys := []string{
			"segments/devA/1-2.parquet",
			"segments/devA/3-4.parquet",
			"segments/devB/5-6.parquet",
			"other/x.txt",
		}
		for _, k := range keys {
			if err := s.Put(ctx, k, strings.NewReader(k)); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.List(ctx, "segments/devA/")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("list devA: want 2, got %d (%v)", len(got), got)
		}
		if got[0].Key != "segments/devA/1-2.parquet" || got[1].Key != "segments/devA/3-4.parquet" {
			t.Fatalf("list order/keys wrong: %v", got)
		}
		if got[0].Size == 0 {
			t.Fatalf("list size not populated: %v", got[0])
		}
		// Empty prefix lists all.
		all, err := s.List(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != len(keys) {
			t.Fatalf("list all: want %d, got %d", len(keys), len(all))
		}
	})

	t.Run("delete", func(t *testing.T) {
		s := newStore(t)
		if err := s.Put(ctx, "d/e.bin", strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, "d/e.bin"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := s.Get(ctx, "d/e.bin"); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("get after delete: want ErrNotFound, got %v", err)
		}
		if err := s.Delete(ctx, "d/e.bin"); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("delete missing: want ErrNotFound, got %v", err)
		}
	})

	t.Run("invalid_keys_rejected", func(t *testing.T) {
		s := newStore(t)
		for _, k := range []string{"", "/abs", "a/../../escape", "..", "a/../../x"} {
			if err := s.Put(ctx, k, strings.NewReader("x")); !errors.Is(err, blob.ErrInvalidKey) {
				t.Fatalf("key %q: want ErrInvalidKey, got %v", k, err)
			}
		}
	})
}

func TestFS_Contract(t *testing.T) {
	contractSuite(t, func(t *testing.T) blob.Store {
		s, err := blob.NewFS(t.TempDir())
		if err != nil {
			t.Fatalf("new fs: %v", err)
		}
		return s
	})
}

// TestFS_TraversalStaysInRoot proves a crafted key cannot write outside root.
func TestFS_TraversalStaysInRoot(t *testing.T) {
	root := t.TempDir()
	s, err := blob.NewFS(root)
	if err != nil {
		t.Fatal(err)
	}
	// LocalPath of any accepted key must stay within root.
	p, err := s.LocalPath("segments/dev/1-2.parquet")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, s.Root()) {
		t.Fatalf("path %q escaped root %q", p, s.Root())
	}
	if _, err := s.LocalPath("../../etc/passwd"); !errors.Is(err, blob.ErrInvalidKey) {
		t.Fatalf("traversal key: want ErrInvalidKey, got %v", err)
	}
}

// TestS3_Contract runs the same suite against a live MinIO/S3 endpoint when
// GANTRY_S3_ENDPOINT is set, else skips. Each subtest gets a fresh bucket.
func TestS3_Contract(t *testing.T) {
	endpoint := os.Getenv("GANTRY_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("GANTRY_S3_ENDPOINT not set; skipping live S3 contract test")
	}
	access := envOr("GANTRY_S3_ACCESS_KEY", "minioadmin")
	secret := envOr("GANTRY_S3_SECRET_KEY", "minioadmin")

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(access, secret, ""),
		Secure: os.Getenv("GANTRY_S3_SSL") == "1",
	})
	if err != nil {
		t.Fatalf("minio client: %v", err)
	}

	var n int
	contractSuite(t, func(t *testing.T) blob.Store {
		ctx := context.Background()
		n++
		bucket := fmt.Sprintf("gantry-test-%d-%d", os.Getpid(), n)
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			t.Fatalf("make bucket: %v", err)
		}
		t.Cleanup(func() {
			// Drain then drop the bucket.
			for obj := range client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
				_ = client.RemoveObject(ctx, bucket, obj.Key, minio.RemoveObjectOptions{})
			}
			_ = client.RemoveBucket(ctx, bucket)
		})
		return blob.NewS3WithClient(client, bucket)
	})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
