package striputils

// White-box guards for the per-directory lock and the parallel walk.
//
// StripDirectory rewrites files in place while other platforms of the same
// fan-out are reading the very same tree into a tar. Two properties have to
// hold and both are asserted here rather than reasoned about: the call really
// does take the directory's lock for its whole duration, and parallelising the
// per-file work changed none of the values it reports.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var lockTestEpoch = time.Unix(1700000000, 0)

// workingStripTool points PATH at a fake strip that succeeds, so the success
// path (which on a real macOS host is unreachable — every strip invocation
// fails there) can be exercised deterministically on any host.
func workingStripTool(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "strip")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake strip: %v", err)
	}
	t.Setenv("PATH", binDir)
	orig := fallbackStripPaths
	fallbackStripPaths = nil
	t.Cleanup(func() { fallbackStripPaths = orig })
}

// TestStripDirectoryHoldsPerDirectoryLock proves StripDirectory is actually
// serialised per directory: with the directory's lock already held, the call
// must block until it is released.
//
// Without the lock the goroutine returns immediately and the first select
// fires, which is what makes this a guard rather than a decoration.
func TestStripDirectoryHoldsPerDirectoryLock(t *testing.T) {
	dir := t.TempDir()

	mu := lockForDir(dir)
	mu.Lock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = StripDirectory(context.Background(), dir, lockTestEpoch)
	}()

	select {
	case <-done:
		mu.Unlock()
		t.Fatal("StripDirectory returned while the per-directory lock was held: it is not taking the lock, so two platforms can rewrite the same tree concurrently")
	case <-time.After(300 * time.Millisecond):
	}

	mu.Unlock()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StripDirectory never returned after the lock was released")
	}
}

// TestLockForDirKeysOnCleanedAbsolutePath pins the lock's identity: two
// spellings of the same directory must share one mutex, or the serialisation
// silently does not apply between two callers that named it differently.
func TestLockForDirKeysOnCleanedAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	a := lockForDir(dir)
	b := lockForDir(filepath.Join(dir, "sub", ".."))
	c := lockForDir(dir + string(filepath.Separator) + ".")
	if a != b || a != c {
		t.Fatalf("lockForDir returned different mutexes for equivalent spellings of %q", dir)
	}
	if a == lockForDir(t.TempDir()) {
		t.Fatal("lockForDir returned the same mutex for two different directories")
	}
}

// writeStripTree materialises n ELF binaries across nested directories and
// returns their paths in filepath.WalkDir order.
func writeStripTree(t *testing.T, root string, n int) []string {
	t.Helper()
	var paths []string
	for i := 0; i < n; i++ {
		rel := filepath.Join("a", "deep", "addon-"+string(rune('a'+i))+".node")
		if i%3 == 1 {
			rel = filepath.Join("b", "addon-"+string(rune('a'+i))+".so")
		}
		if i%3 == 2 {
			rel = filepath.Join("addon-" + string(rune('a'+i)) + ".node")
		}
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		buildELFFixture(t, p)
		paths = append(paths, p)
	}
	// Non-ELF noise the walk must ignore entirely.
	for _, name := range []string{"index.js", "package.json", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("not an elf binary at all"), 0o644); err != nil {
			t.Fatalf("write noise: %v", err)
		}
	}

	var walkOrder []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		for _, e := range paths {
			if e == p {
				walkOrder = append(walkOrder, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return walkOrder
}

// TestStripDirectoryParallelWalkReportsEveryFile checks the parallel walk
// against the values callers act on: every ELF file is stripped exactly once,
// non-ELF files are untouched, and nothing lands in skipped.
func TestStripDirectoryParallelWalkReportsEveryFile(t *testing.T) {
	root := t.TempDir()
	want := writeStripTree(t, root, 6)
	workingStripTool(t)

	stripped, skipped, err := StripDirectory(context.Background(), root, lockTestEpoch)
	if err != nil {
		t.Fatalf("StripDirectory: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("expected nothing skipped with a working strip tool, got %v", skipped)
	}
	if stripped != len(want) {
		t.Errorf("stripped %d files, expected %d (%v)", stripped, len(want), want)
	}
}

// TestStripDirectorySkippedOrderIsWalkOrder is the honesty guard the macOS
// note in StripDirectory's doc comment demands: when no strip tool works,
// EVERY ELF file must be reported as skipped, in walk order, and repeatedly —
// a parallel walk that dropped or reordered entries would make the warning
// packager.warnUnstripped prints wrong without failing anything else.
func TestStripDirectorySkippedOrderIsWalkOrder(t *testing.T) {
	root := t.TempDir()
	want := writeStripTree(t, root, 6)
	noWorkingStripTool(t)

	for run := 0; run < 5; run++ {
		stripped, skipped, err := StripDirectory(context.Background(), root, lockTestEpoch)
		if err == nil {
			t.Fatal("expected an error explaining why files were left unstripped")
		}
		if stripped != 0 {
			t.Errorf("run %d: stripped=%d, expected 0", run, stripped)
		}
		if len(skipped) != len(want) {
			t.Fatalf("run %d: skipped %d files, expected %d\n got: %v\nwant: %v", run, len(skipped), len(want), skipped, want)
		}
		for i := range want {
			if skipped[i] != want[i] {
				t.Fatalf("run %d: skipped[%d] = %s, want %s (full: %v)", run, i, skipped[i], want[i], skipped)
			}
		}
	}
}
