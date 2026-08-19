package lockfileutils_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/lockfileutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestLockfileLoadAndSave(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")

	lf := &ports.PokkumLockfile{
		Version:   1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Bases:     make(map[string]ports.BaseLockEntry),
	}

	entry := ports.BaseLockEntry{
		Ref:       "gcr.io/distroless/cc-debian12:nonroot",
		Digest:    "sha256:1111222233334444555566667777888899990000111122223333444455556666",
		PinnedRef: "gcr.io/distroless/cc-debian12@sha256:1111222233334444555566667777888899990000111122223333444455556666",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	lockfileutils.SetLockedBase(lf, "distroless", entry)

	err := lockfileutils.SaveLockfile(lockPath, lf)
	if err != nil {
		t.Fatalf("SaveLockfile failed: %v", err)
	}

	loaded, err := lockfileutils.LoadLockfile(lockPath)
	if err != nil {
		t.Fatalf("LoadLockfile failed: %v", err)
	}

	if loaded.Version != 1 {
		t.Errorf("expected version 1, got %d", loaded.Version)
	}

	gotEntry, ok := lockfileutils.GetLockedBase(loaded, "distroless")
	if !ok {
		t.Fatalf("expected to find distroless locked base")
	}

	if gotEntry.Digest != entry.Digest {
		t.Errorf("expected digest %s, got %s", entry.Digest, gotEntry.Digest)
	}
}

// TestDeleteLockedBase_ExactKeyOnly guards the asymmetry between GetLockedBase
// and DeleteLockedBase: lookups fall back to case-insensitive and Ref-based
// matching, which is harmless when it guesses wrong, but a delete that guessed
// wrong would discard a pin the caller never named. The per-reference custom
// lock-slot migration relies on this: it deletes the legacy bare "custom" slot
// and must not touch any "custom:<hash>" sibling.
func TestDeleteLockedBase_ExactKeyOnly(t *testing.T) {
	lf := &ports.PokkumLockfile{Bases: map[string]ports.BaseLockEntry{
		"custom":              {Ref: "example.com/a:v1"},
		"custom:0123456789ab": {Ref: "example.com/a:v1"},
		"distroless":          {Ref: "gcr.io/distroless/cc-debian12:nonroot"},
	}}

	lockfileutils.DeleteLockedBase(lf, "custom")

	if _, ok := lf.Bases["custom"]; ok {
		t.Error("DeleteLockedBase did not remove the exact key")
	}
	for _, survivor := range []string{"custom:0123456789ab", "distroless"} {
		if _, ok := lf.Bases[survivor]; !ok {
			t.Errorf("DeleteLockedBase(%q) also removed %q", "custom", survivor)
		}
	}

	// Neither a case variant nor a matching Ref may act as a delete key.
	lockfileutils.DeleteLockedBase(lf, "DISTROLESS")
	lockfileutils.DeleteLockedBase(lf, "example.com/a:v1")
	if len(lf.Bases) != 2 {
		t.Errorf("fuzzy delete removed entries: %v", lf.Bases)
	}

	// Nil-safe, so callers do not need a guard of their own.
	lockfileutils.DeleteLockedBase(nil, "custom")
	lockfileutils.DeleteLockedBase(&ports.PokkumLockfile{}, "custom")
}

func TestLoadNonExistentLockfile(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "nonexistent.lock")

	_, err := lockfileutils.LoadLockfile(lockPath)
	if !os.IsNotExist(err) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}
