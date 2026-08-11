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

func TestLoadNonExistentLockfile(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "nonexistent.lock")

	_, err := lockfileutils.LoadLockfile(lockPath)
	if !os.IsNotExist(err) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}
