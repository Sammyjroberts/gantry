// Package duckdb is Gantry's embedded-SQL tier: it runs read-only SQL over the
// Parquet segment store using a DuckDB engine binary managed next to the data
// dir — NO CGo, NO toolchain change. DuckDB reads Parquet natively, so a
// prepared view over the segment glob (`tlm`) turns "SELECT ... FROM tlm WHERE
// ..." into a real analytical query with zero custom query code, and the SQLite
// catalog can be ATTACHed so experiments are joinable.
//
// Acquisition is deliberately one small seam (Provider): the binary can come
// from an env var, from a conventional path under the data dir, or later from a
// go:embed'd asset the coordinator wires in — the Engine does not care. When no
// binary is present the SQL surface returns a clear ErrNotInstalled with an
// install hint and everything else in Edge keeps working.
package duckdb

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// ErrNotInstalled is returned (wrapped) when no DuckDB binary can be located. It
// carries an install hint via Error so HTTP/MCP callers can surface it directly.
var ErrNotInstalled = errors.New("duckdb engine not installed: place the duckdb binary at <data-dir>/duckdb/duckdb" + exeSuffix() + " or set GANTRY_DUCKDB to its path (download: https://duckdb.org/docs/installation)")

// Provider locates the DuckDB engine binary. Binary returns the absolute path
// and true when a usable binary is available. Implementations must not run it.
type Provider interface {
	Binary() (path string, ok bool)
}

// EnvProvider resolves the binary from the GANTRY_DUCKDB environment variable.
type EnvProvider struct{}

func (EnvProvider) Binary() (string, bool) {
	p := os.Getenv("GANTRY_DUCKDB")
	if p == "" {
		return "", false
	}
	if !fileExists(p) {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	return abs, true
}

// DirProvider resolves the binary from a conventional path under a data dir:
// <dataDir>/duckdb/duckdb(.exe). This is how Edge ships it alongside ./data.
type DirProvider struct {
	DataDir string
}

func (d DirProvider) Binary() (string, bool) {
	if d.DataDir == "" {
		return "", false
	}
	p := filepath.Join(d.DataDir, "duckdb", "duckdb"+exeSuffix())
	if !fileExists(p) {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	return abs, true
}

// PathProvider is a fixed absolute path (used by tests that download a binary to
// a temp dir).
type PathProvider struct{ Path string }

func (p PathProvider) Binary() (string, bool) {
	if p.Path == "" || !fileExists(p.Path) {
		return "", false
	}
	return p.Path, true
}

// Providers tries each provider in order and returns the first binary found.
type Providers []Provider

func (ps Providers) Binary() (string, bool) {
	for _, p := range ps {
		if bin, ok := p.Binary(); ok {
			return bin, true
		}
	}
	return "", false
}

// DefaultProvider is the standard Edge resolution order: explicit env override
// first, then the conventional data-dir location.
func DefaultProvider(dataDir string) Provider {
	return Providers{EnvProvider{}, DirProvider{DataDir: dataDir}}
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
