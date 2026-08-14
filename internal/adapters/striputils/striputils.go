package striputils

import (
	"context"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// IsUtilityPackage marks this as a reusable utility, not a port adapter.
const IsUtilityPackage = true

// IsELFBinary reports whether a file starts with the standard 4-byte ELF magic header (\x7fELF).
func IsELFBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var magic [4]byte
	if _, err := f.Read(magic[:]); err != nil {
		return false
	}
	return magic[0] == 0x7f && magic[1] == 'E' && magic[2] == 'L' && magic[3] == 'F'
}

// StripELFFile strips unneeded debug symbols and sections from an ELF binary in-place,
// updating modTime to match the pinned build epoch.
func StripELFFile(ctx context.Context, path string, modTime time.Time) (bool, error) {
	if !IsELFBinary(path) {
		return false, nil
	}

	// Verify it can be parsed as a valid ELF file
	ef, err := elf.Open(path)
	if err != nil {
		return false, nil // skip non-standard or malformed ELF
	}
	_ = ef.Close()

	// Try running system strip / llvm-strip with --strip-unneeded
	stripped := false
	for _, tool := range []string{"llvm-strip", "strip"} {
		if stripPath, err := exec.LookPath(tool); err == nil {
			cmd := exec.CommandContext(ctx, stripPath, "--strip-unneeded", path)
			if err := cmd.Run(); err == nil {
				stripped = true
				break
			}
		}
	}

	if stripped {
		_ = os.Chtimes(path, modTime, modTime)
		return true, nil
	}

	return false, nil
}

// StripDirectory walks dir and strips all ELF binaries (.node, .so, etc.) in-place,
// preserving the pinned modTime.
func StripDirectory(ctx context.Context, dir string, modTime time.Time) (int, error) {
	if dir == "" {
		return 0, nil
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return 0, nil
	}

	count := 0
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(p))
		isTarget := ext == ".node" || ext == ".so" || strings.Contains(filepath.Base(p), ".so.")
		if !isTarget && !IsELFBinary(p) {
			return nil
		}

		if ok, err := StripELFFile(ctx, p, modTime); err == nil && ok {
			count++
		}
		return nil
	})

	return count, err
}
