package main

import (
	"os"
	"path/filepath"
	"testing"
)

// All cases use t.TempDir() only — never the live ./data dir, which the running
// bench binary holds open (see task constraints).

func TestAdoptLegacyDataDir_RenamesLegacy(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "edge")
	target := filepath.Join(root, "bench")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(legacy, "marker")
	if err := os.WriteFile(marker, []byte("state"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := adoptLegacyDataDir(target); err != nil {
		t.Fatalf("adoptLegacyDataDir: %v", err)
	}

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy dir should be gone, stat err = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(target, "marker"))
	if err != nil {
		t.Fatalf("read migrated marker: %v", err)
	}
	if string(got) != "state" {
		t.Errorf("marker content = %q, want %q", got, "state")
	}
}

func TestAdoptLegacyDataDir_NoLegacyIsNoop(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "bench")
	if err := adoptLegacyDataDir(target); err != nil {
		t.Fatalf("adoptLegacyDataDir: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("target should not be created when no legacy dir exists, err = %v", err)
	}
}

func TestAdoptLegacyDataDir_ExistingTargetNotClobbered(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "edge")
	target := filepath.Join(root, "bench")
	for _, d := range []string{legacy, target} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(legacy, "old"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "new"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := adoptLegacyDataDir(target); err != nil {
		t.Fatalf("adoptLegacyDataDir: %v", err)
	}

	// Target kept its own content; legacy left untouched (not merged/deleted).
	if _, err := os.Stat(filepath.Join(target, "new")); err != nil {
		t.Errorf("target content should be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacy, "old")); err != nil {
		t.Errorf("legacy dir should be left intact when target exists: %v", err)
	}
}

func TestAdoptLegacyDataDir_CustomDirNameIgnored(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "edge")
	custom := filepath.Join(root, "my-data")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := adoptLegacyDataDir(custom); err != nil {
		t.Fatalf("adoptLegacyDataDir: %v", err)
	}
	// A non-"bench" target must not trigger adoption of ./edge.
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("legacy dir should be untouched for a custom -data-dir: %v", err)
	}
	if _, err := os.Stat(custom); !os.IsNotExist(err) {
		t.Errorf("custom dir should not be created by migration: %v", err)
	}
}
