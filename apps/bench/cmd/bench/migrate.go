package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// legacyDataDirName is the pre-rename default data directory leaf. The product
// renamed Edge→Bench; the old binary defaulted to ./data/edge. adoptLegacyDataDir
// moves a sibling ./data/edge to the new ./data/bench so an operator who upgrades
// keeps their JetStream store, segments, and SQLite index without any manual step.
const legacyDataDirName = "edge"

// adoptLegacyDataDir renames a legacy "<parent>/edge" directory to dataDir when
// dataDir's base is "bench", the target does not yet exist, and the legacy dir
// is present and is a directory. Any other shape (custom -data-dir, target
// already there, no legacy dir) is a no-op, so it is safe to call unconditionally
// on every boot. It never deletes or overwrites an existing target.
func adoptLegacyDataDir(dataDir string) error {
	if filepath.Base(dataDir) != "bench" {
		return nil
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		return nil // new dir already present (or stat failed) — do not clobber
	}
	legacy := filepath.Join(filepath.Dir(dataDir), legacyDataDirName)
	fi, err := os.Stat(legacy)
	if err != nil || !fi.IsDir() {
		return nil // no legacy dir to adopt
	}
	if err := os.Rename(legacy, dataDir); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", legacy, dataDir, err)
	}
	return nil
}
