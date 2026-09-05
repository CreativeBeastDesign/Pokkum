package striputils

import (
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
	return hasELFMagic(f)
}

// hasELFMagic reads the 4-byte ELF magic from the start of f.
func hasELFMagic(f *os.File) bool {
	var magic [4]byte
	if _, err := f.ReadAt(magic[:], 0); err != nil {
		return false
	}
	return magic[0] == 0x7f && magic[1] == 'E' && magic[2] == 'L' && magic[3] == 'F'
}

// probeELF answers, in ONE file open, the two questions the strip path needs:
// does this file carry the ELF magic, and does debug/elf parse it.
//
// The three separate opens this replaces were not free: StripDirectory called
// IsELFBinary, StripELFFile then called IsELFBinary again, and elf.Open opened
// the file a third time — per candidate file, across a native/vendor tree that
// can hold hundreds of them, on every platform of a fan-out.
func probeELF(path string) (isELF, parses bool) {
	f, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer func() { _ = f.Close() }()

	if !hasELFMagic(f) {
		return false, false
	}
	ef, err := elf.NewFile(f)
	if err != nil {
		return true, false
	}
	_ = ef.Close()
	return true, true
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
	isELF, parses := probeELF(path)
	if !isELF || !parses {
		// Not ELF at all, or a non-standard/malformed ELF: genuinely nothing
		// to strip, which is the (false, nil) case documented above.
		return false, nil
	}
	return stripProbedELF(ctx, path, modTime)
}

// stripProbedELF is StripELFFile for a path already known to be a parseable
// ELF binary. StripDirectory probes once and calls this, so a tree walk does
// not re-open and re-parse every candidate it has just classified.
func stripProbedELF(ctx context.Context, path string, modTime time.Time) (bool, error) {
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

// dirLocks serialises StripDirectory per directory, mirroring the identical
// mechanism in precompressutils (read that package's dirLocks doc comment; the
// reasoning transfers wholesale).
//
// StripDirectory rewrites files IN PLACE. A multi-platform build fans out over
// platforms, every platform packages from the same host directories, and the
// packager's tree walk reads each file's size from its dirent and then copies
// that many bytes into the tar. A strip landing between those two steps
// produces a short read — surfacing either as a size-mismatch error or, worse,
// as a layer whose recorded digest and tar bytes disagree. That is a
// reproducibility hazard, not just a slowdown.
//
// Be precise about what this lock does and does not buy, because the
// distinction is exactly the one precompressutils' doc comment spells out.
// The lock alone is NOT sufficient: it stops two strips from overlapping, but
// the first platform releases it and starts tarring while the second acquires
// it and starts writing. What closes the race is the second platform doing no
// work at all, which requires a per-BUILD memo — and build lifetime is a
// concept this package deliberately has none of. That memo therefore lives in
// the packager, which does own the build's context (see stripTreeOnce in
// internal/adapters/packager/packager.go). This lock is what keeps direct
// callers — tests, and any future caller outside that memo — from interleaving
// two rewrites of the same tree.
var dirLocks sync.Map // cleaned absolute path -> *sync.Mutex

func lockForDir(dir string) *sync.Mutex {
	key := dir
	if abs, err := filepath.Abs(dir); err == nil {
		key = filepath.Clean(abs)
	}
	actual, _ := dirLocks.LoadOrStore(key, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// stripOutcome is one file's result, kept per-index so the aggregate is
// assembled in path order regardless of which worker finished first.
type stripOutcome struct {
	path     string
	stripped bool
	err      error
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
//
// On macOS every one of these strip invocations fails (the built-in strip is
// Xcode's Mach-O-only tool and rejects --strip-unneeded), so every ELF file
// lands in skipped and stripped stays 0. That is the honest report and it must
// stay honest: skipped is built from the files this call actually probed, and
// the packager's per-build memo replays this exact list to every platform
// rather than handing later platforms an empty one.
//
// The per-file work is dispatched across a bounded worker pool: each file
// costs an exec of an external strip tool, which is what dominates, and those
// are independent. Ordering is not left to the pool: outcomes are collected
// into a slice indexed by the candidate's position in the walk and aggregated
// in that order, so skipped comes back in exactly the walk order it had
// before and none of the returned values depend on scheduling.
func StripDirectory(ctx context.Context, dir string, modTime time.Time) (stripped int, skipped []string, err error) {
	if dir == "" {
		return 0, nil, nil
	}
	info, statErr := os.Stat(dir)
	if statErr != nil || !info.IsDir() {
		return 0, nil, nil
	}

	mu := lockForDir(dir)
	mu.Lock()
	defer mu.Unlock()

	// Candidates are collected first, then stripped, so that the fallible
	// per-file work never runs inside the WalkDir callback and no dispatch
	// decision sits between a goroutine's launch and the Wait below.
	var candidates []string
	walkErr := filepath.WalkDir(dir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		// One probe per file, replacing the old IsELFBinary-then-
		// StripELFFile-then-elf.Open sequence.
		//
		// The old .node/.so extension pre-filter is gone, and dropping it
		// changes nothing: a file whose extension did NOT match still had
		// IsELFBinary called on it (same single open this probe does), and a
		// file whose extension DID match but which was not a parseable ELF
		// was still discarded — by StripELFFile's own (false, nil) return
		// rather than here. Same set of files stripped, same set skipped,
		// one open instead of two or three.
		isELF, parses := probeELF(p)
		if !isELF || !parses {
			return nil
		}
		candidates = append(candidates, p)
		return nil
	})
	if walkErr != nil {
		return 0, nil, walkErr
	}
	if len(candidates) == 0 {
		return 0, nil, nil
	}

	outcomes := stripCandidates(ctx, candidates, modTime)

	var firstErr error
	for _, o := range outcomes {
		switch {
		case o.err != nil:
			skipped = append(skipped, o.path)
			if firstErr == nil {
				firstErr = o.err
			}
		case o.stripped:
			stripped++
		}
	}
	return stripped, skipped, firstErr
}

// stripCandidates strips paths through a bounded worker pool and returns one
// outcome per input, in input order.
func stripCandidates(ctx context.Context, paths []string, modTime time.Time) []stripOutcome {
	outcomes := make([]stripOutcome, len(paths))
	for i, p := range paths {
		outcomes[i].path = p
	}

	workers := runtime.GOMAXPROCS(0)
	if workers > len(paths) {
		workers = len(paths)
	}
	if workers < 1 {
		workers = 1
	}

	if workers == 1 {
		for i, p := range paths {
			outcomes[i].stripped, outcomes[i].err = stripProbedELF(ctx, p, modTime)
		}
		return outcomes
	}

	var next atomic.Int64
	var wg sync.WaitGroup
	// No fallible call sits between the dispatch loop and Wait: each worker's
	// only exit is the index bound, and per-file failures are recorded in
	// outcomes rather than returned.
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				idx := int(next.Add(1) - 1)
				if idx >= len(paths) {
					return
				}
				outcomes[idx].stripped, outcomes[idx].err = stripProbedELF(ctx, paths[idx], modTime)
			}
		}()
	}
	wg.Wait()

	return outcomes
}
