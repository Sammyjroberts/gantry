package benchdb

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestAdoptLegacyDBMovesSidecars is the regression guard for the orphaned-WAL
// bug: in WAL mode committed writes live in edge.db-wal until checkpointed, so
// the adoption rename must carry the -wal/-shm sidecars with the base file or
// recent data is silently lost.
func TestAdoptLegacyDBMovesSidecars(t *testing.T) {
	dir := t.TempDir()
	for suffix, content := range map[string]string{"": "base", "-wal": "wal", "-shm": "shm"} {
		if err := os.WriteFile(filepath.Join(dir, "edge.db"+suffix), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := adoptLegacyDB(filepath.Join(dir, "bench.db")); err != nil {
		t.Fatal(err)
	}
	for suffix, want := range map[string]string{"": "base", "-wal": "wal", "-shm": "shm"} {
		got, err := os.ReadFile(filepath.Join(dir, "bench.db"+suffix))
		if err != nil {
			t.Fatalf("bench.db%s not adopted: %v", suffix, err)
		}
		if string(got) != want {
			t.Fatalf("bench.db%s content = %q, want %q", suffix, got, want)
		}
		if _, err := os.Stat(filepath.Join(dir, "edge.db"+suffix)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("edge.db%s still present after adoption", suffix)
		}
	}
}
