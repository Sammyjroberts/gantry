// Package blob is Gantry's object-storage abstraction: a small key/value blob
// interface shared by Bench and Cloud so the segment store (and anything else
// that persists immutable files) is written once against one seam. Bench wires
// the filesystem implementation (fsblob, rooted under <data-dir>/blobs);
// Cloud wires the S3-compatible implementation (s3blob, against MinIO locally
// and GCS in the cloud). Keys are always "/"-separated logical paths
// (e.g. "segments/<device>/<start>-<end>.parquet"); implementations map them to
// their native namespace (a file path, an object key) and MUST reject traversal
// outside their root.
package blob

import (
	"context"
	"errors"
	"io"
	"strings"
)

// ErrNotFound is returned (wrapped) by Get/Delete when a key does not exist. It
// unifies the filesystem's os.ErrNotExist and S3's NoSuchKey so callers can test
// existence portably with errors.Is.
var ErrNotFound = errors.New("blob: not found")

// ErrInvalidKey is returned when a key is empty, absolute, or attempts to escape
// the store root via "..". Keys are logical "/"-separated paths with no leading
// slash and no "." / ".." segments.
var ErrInvalidKey = errors.New("blob: invalid key")

// ObjectInfo describes one stored object. Size is the byte length of the blob.
type ObjectInfo struct {
	Key  string
	Size int64
}

// Store is the blob persistence contract. Implementations are safe for
// concurrent use. Keys are logical "/"-separated paths (see package doc).
type Store interface {
	// Put writes the full contents of r under key, replacing any existing object
	// atomically (a concurrent Get sees either the old or the new object, never a
	// partial write). It returns only after the write is durable.
	Put(ctx context.Context, key string, r io.Reader) error
	// Get opens the object at key for reading. The caller must Close the returned
	// reader. A missing key returns a wrapped ErrNotFound.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// List returns objects whose key begins with prefix, in lexical key order.
	// An empty prefix lists everything. The prefix is matched literally against
	// the logical key (it need not align to a "/" boundary).
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
	// Delete removes the object at key. A missing key returns a wrapped
	// ErrNotFound.
	Delete(ctx context.Context, key string) error
}

// LocalStore is the optional capability of a Store that is backed by a local
// directory. The DuckDB tier uses it to point read_parquet() at the segment
// files directly (an in-process file glob is far faster than streaming every
// object through Get). s3blob does not satisfy it; on Cloud the equivalent is
// a DuckDB httpfs/gcs scan, out of scope for v1.
type LocalStore interface {
	// LocalPath maps a logical key to its absolute on-disk path. The file need
	// not exist. It returns ErrInvalidKey for an unsafe key.
	LocalPath(key string) (string, error)
	// Root is the absolute directory all objects live under.
	Root() string
}

// cleanKey validates and normalises a logical key. It rejects empty keys,
// absolute keys, and any key with a "." or ".." segment (traversal defence),
// and collapses duplicate slashes. The returned key uses "/" separators.
func cleanKey(key string) (string, error) {
	if key == "" {
		return "", ErrInvalidKey
	}
	// Normalise separators; callers on Windows may hand us backslashes.
	key = strings.ReplaceAll(key, "\\", "/")
	if strings.HasPrefix(key, "/") {
		return "", ErrInvalidKey
	}
	segs := strings.Split(key, "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		switch s {
		case "", ".":
			// collapse empty ("//") and current-dir segments
			continue
		case "..":
			return "", ErrInvalidKey
		default:
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return "", ErrInvalidKey
	}
	return strings.Join(out, "/"), nil
}
