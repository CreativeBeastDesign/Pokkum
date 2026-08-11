package lockfileutils

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// IsUtilityPackage marks this package as a shared utility package.
const IsUtilityPackage = true

// LockfileSchemaVersion is the current pokkum.lock schema version.
const LockfileSchemaVersion = 1

// LoadLockfile reads and parses a pokkum.lock file from disk.
// Returns os.ErrNotExist if the file does not exist.
func LoadLockfile(path string) (*ports.PokkumLockfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var lf ports.PokkumLockfile
	if err := json.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("lockfileutils: parse %s: %w", path, err)
	}

	if lf.Bases == nil {
		lf.Bases = make(map[string]ports.BaseLockEntry)
	}

	return &lf, nil
}

// SaveLockfile writes a pokkum.lock file to disk formatted with JSON indentation.
func SaveLockfile(path string, lf *ports.PokkumLockfile) error {
	if lf == nil {
		return errors.New("lockfileutils: lockfile is nil")
	}

	lf.Version = LockfileSchemaVersion
	lf.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if lf.Bases == nil {
		lf.Bases = make(map[string]ports.BaseLockEntry)
	}

	data, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return fmt.Errorf("lockfileutils: marshal lockfile: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("lockfileutils: create dir %s: %w", dir, err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("lockfileutils: write lockfile %s: %w", path, err)
	}

	return nil
}

// GetLockedBase finds the locked base entry for a preset name or reference.
func GetLockedBase(lf *ports.PokkumLockfile, key string) (ports.BaseLockEntry, bool) {
	if lf == nil || lf.Bases == nil {
		return ports.BaseLockEntry{}, false
	}

	key = strings.TrimSpace(key)
	if entry, ok := lf.Bases[key]; ok {
		return entry, true
	}

	// Also check by canonical preset string (e.g., "distroless", "chainguard")
	for name, entry := range lf.Bases {
		if strings.EqualFold(name, key) || strings.EqualFold(entry.Ref, key) {
			return entry, true
		}
	}

	return ports.BaseLockEntry{}, false
}

// SetLockedBase adds or updates a locked base image entry in the lockfile.
func SetLockedBase(lf *ports.PokkumLockfile, key string, entry ports.BaseLockEntry) {
	if lf.Bases == nil {
		lf.Bases = make(map[string]ports.BaseLockEntry)
	}
	if entry.UpdatedAt == "" {
		entry.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	lf.Bases[key] = entry
}
