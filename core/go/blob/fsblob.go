package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FS is a filesystem-backed Store rooted at a directory. This is Bench's blob
// store (root: <data-dir>/blobs). Writes are atomic (temp file + fsync +
// rename) so a crash mid-write never exposes a truncated object, and a Get
// concurrent with a Put sees either the old bytes or the new, never a mix.
type FS struct {
	root string
}

// NewFS returns an FS rooted at dir, creating dir if absent. dir is made
// absolute so LocalPath is stable regardless of the process working directory.
func NewFS(dir string) (*FS, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("blob: abs %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("blob: mkdir %q: %w", abs, err)
	}
	return &FS{root: abs}, nil
}

// Root returns the absolute directory objects live under.
func (f *FS) Root() string { return f.root }

// LocalPath maps a logical key to its absolute on-disk path (the file need not
// exist), rejecting unsafe keys.
func (f *FS) LocalPath(key string) (string, error) {
	clean, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(f.root, filepath.FromSlash(clean)), nil
}

func (f *FS) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := f.LocalPath(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("blob: mkdir %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("blob: temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return fmt.Errorf("blob: write %q: %w", key, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("blob: fsync %q: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("blob: close %q: %w", key, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("blob: rename %q: %w", key, err)
	}
	return nil
}

func (f *FS) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := f.LocalPath(key)
	if err != nil {
		return nil, err
	}
	fh, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("blob: get %q: %w", key, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("blob: get %q: %w", key, err)
	}
	return fh, nil
}

func (f *FS) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []ObjectInfo
	err := filepath.WalkDir(f.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip in-flight temp files from a concurrent Put.
		if strings.HasPrefix(d.Name(), ".tmp-") {
			return nil
		}
		rel, err := filepath.Rel(f.root, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, ObjectInfo{Key: key, Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("blob: list %q: %w", prefix, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *FS) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := f.LocalPath(key)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob: delete %q: %w", key, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("blob: delete %q: %w", key, err)
	}
	return nil
}

// Compile-time assertions.
var (
	_ Store      = (*FS)(nil)
	_ LocalStore = (*FS)(nil)
)
