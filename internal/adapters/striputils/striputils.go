package striputils

import (
	"context"
	"debug/elf"
	"errors"
	"fmt"
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

// ErrNoStripTool indicates a valid ELF binary was found but no strip tool on
// the host was able to process it. It wraps the underlying lookup/exec
// failures so callers (and tests) can distinguish "nothing to do" (a
// non-ELF or malformed file, where StripELFFile returns a nil error) from
// "this should have been stripped and silently wasn't".
var ErrNoStripTool = errors.New("striputils: no ELF-capable strip tool available")

// fallbackStripPaths are well-known Homebrew keg-only install locations for
// llvm-strip, checked after $PATH comes up empty. Homebrew does not link
// llvm onto PATH by default (it conflicts with Xcode's own tools), so a
// Homebrew user with llvm installed would otherwise never be found even
// though a perfectly good ELF-capable strip is sitting right there.
//
// This is a var, not a const, purely so the package's own tests can zero it
// out to deterministically simulate "no ELF-capable strip tool anywhere on
// this host" on a dev machine that happens to have Homebrew LLVM installed
// (as opposed to requiring a machine without it, which the CI/dev host
// running these tests cannot guarantee).
var fallbackStripPaths = []string{
	"/opt/homebrew/opt/llvm/bin/llvm-strip", // Apple Silicon Homebrew (keg-only)
	"/usr/local/opt/llvm/bin/llvm-strip",    // Intel Homebrew (keg-only)
}

// candidateStripPaths returns, in order of preference, the paths of strip
// tools worth trying: llvm-strip and strip as found on $PATH, followed by
// fallbackStripPaths. This is a best-effort convenience, not full
// toolchain discovery: anything more exotic still requires a strip tool on
// PATH.
func candidateStripPaths() []string {
	var candidates []string

	for _, tool := range []string{"llvm-strip", "strip"} {
		if p, err := exec.LookPath(tool); err == nil {
			candidates = append(candidates, p)
		}
	}

	for _, p := range fallbackStripPaths {
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			candidates = append(candidates, p)
		}
	}

	return candidates
}

// StripELFFile strips unneeded debug symbols and sections from an ELF binary in-place,
// updating modTime to match the pinned build epoch.
//
// A nil (false, nil) result means there was genuinely nothing to strip: the
// file is not ELF at all, or it failed to parse as one. A non-nil error
// (always paired with false) means the opposite: this *is* a valid ELF
// binary that a caller would reasonably expect to be stripped, but no
// working strip tool could be found or run for it — for example on a plain
// macOS host, where the built-in `strip` is Xcode's Mach-O-only tool and
// exits with "unrecognized option: --strip-unneeded" on an ELF file. That
// distinction lets callers surface a real warning instead of the size
// reduction silently not happening.
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

	tools := candidateStripPaths()
	if len(tools) == 0 {
		return false, fmt.Errorf("%w: no llvm-strip or strip binary found on PATH (path: %s)", ErrNoStripTool, path)
	}

	// Try each candidate strip tool with --strip-unneeded. Record the last
	// failure's stderr so that, if every candidate fails, the caller gets a
	// concrete reason (e.g. macOS's Mach-O-only strip rejecting the flag)
	// rather than a bare "it didn't work".
	var lastErr error
	for _, toolPath := range tools {
		var stderr strings.Builder
		cmd := exec.CommandContext(ctx, toolPath, "--strip-unneeded", path)
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		if runErr == nil {
			_ = os.Chtimes(path, modTime, modTime)
			return true, nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = runErr.Error()
		}
		lastErr = fmt.Errorf("%s: %s", filepath.Base(toolPath), msg)
	}

	return false, fmt.Errorf("%w for %s: %w", ErrNoStripTool, path, lastErr)
}

// StripDirectory walks dir and strips all ELF binaries (.node, .so, etc.) in-place,
// preserving the pinned modTime.
//
// stripped is the number of files successfully stripped. skipped lists the
// paths of files that were confirmed to be valid ELF binaries but could not
// be stripped (see StripELFFile) — callers should treat a non-empty skipped
// list as worth surfacing to the user, since it means the size-reduction
// feature silently did nothing for those files. err is only non-nil for a
// filesystem-level failure walking dir; per-file strip failures do not stop
// the walk and are reported via skipped instead.
func StripDirectory(ctx context.Context, dir string, modTime time.Time) (stripped int, skipped []string, err error) {
	if dir == "" {
		return 0, nil, nil
	}
	info, statErr := os.Stat(dir)
	if statErr != nil || !info.IsDir() {
		return 0, nil, nil
	}

	var firstErr error
	walkErr := filepath.WalkDir(dir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(p))
		isTarget := ext == ".node" || ext == ".so" || strings.Contains(filepath.Base(p), ".so.")
		if !isTarget && !IsELFBinary(p) {
			return nil
		}

		ok, stripErr := StripELFFile(ctx, p, modTime)
		switch {
		case stripErr != nil:
			skipped = append(skipped, p)
			if firstErr == nil {
				firstErr = stripErr
			}
		case ok:
			stripped++
		}
		return nil
	})
	if walkErr != nil {
		return stripped, skipped, walkErr
	}

	return stripped, skipped, firstErr
}
