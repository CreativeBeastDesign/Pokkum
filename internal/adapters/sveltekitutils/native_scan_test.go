package sveltekitutils

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// nChecksCtx is a context whose Err() reports cancellation only after the Nth
// call. It exists because a pre-cancelled context is caught by the cheap guard
// at the top of each Scan* function, so it cannot distinguish "the walk stops
// on cancellation" from "the function returns early before walking" — and the
// guard being held here is the one INSIDE the walk callback.
type nChecksCtx struct {
	context.Context

	mu     sync.Mutex
	calls  int
	limit  int
	done   chan struct{}
	closed sync.Once
}

func cancelAfterNChecks(limit int) context.Context {
	return &nChecksCtx{Context: context.Background(), limit: limit, done: make(chan struct{})}
}

func (c *nChecksCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls > c.limit {
		c.closed.Do(func() { close(c.done) })
		return context.Canceled
	}
	return nil
}

func (c *nChecksCtx) Done() <-chan struct{} { return c.done }

// legacyNativeWalk reproduces, verbatim, the filepath.Walk loop CheckNativeModules
// used before the two walks were merged: a case-sensitive ".node" suffix test,
// project-relative paths, first-occurrence dedupe.
func legacyNativeWalk(projectDir string) []string {
	var modules []string
	seen := make(map[string]bool)

	nodeModulesDir := filepath.Join(projectDir, "node_modules")
	if info, err := os.Stat(nodeModulesDir); err == nil && info.IsDir() {
		_ = filepath.Walk(nodeModulesDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if !info.IsDir() && strings.HasSuffix(info.Name(), ".node") {
				relPath, err := filepath.Rel(projectDir, path)
				if err != nil {
					relPath = path
				}
				if !seen[relPath] {
					seen[relPath] = true
					modules = append(modules, relPath)
				}
			}
			return nil
		})
	}
	return modules
}

// legacyELFWalk reproduces, verbatim, the SECOND filepath.Walk that
// ClosuredNativeAdapter.Inspect ran over the same tree: a case-insensitive
// extension test plus the versioned-".so." substring test.
func legacyELFWalk(projectDir string) []string {
	var candidates []string

	nodeModulesDir := filepath.Join(projectDir, "node_modules")
	if info, err := os.Stat(nodeModulesDir); err == nil && info.IsDir() {
		_ = filepath.Walk(nodeModulesDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".node" || ext == ".so" || strings.Contains(info.Name(), ".so.") {
				candidates = append(candidates, path)
			}
			return nil
		})
	}
	return candidates
}

// differentialFixture builds a node_modules tree covering every classification
// the two legacy walks disagreed about, plus decoys outside node_modules that
// neither walk ever saw.
func differentialFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	files := []string{
		// Plain native addon: matched by BOTH predicates.
		filepath.Join("node_modules", "better-sqlite3", "build", "Release", "addon.node"),
		// Prebuilt addon, deeper path.
		filepath.Join("node_modules", "sharp", "prebuilds", "linux-x64", "sharp.node"),
		// Plain shared object: ELF candidate only.
		filepath.Join("node_modules", "sharp", "vendor", "lib", "libvips.so"),
		// Versioned shared object: ELF candidate via the ".so." substring rule.
		filepath.Join("node_modules", "sharp", "vendor", "lib", "libglib-2.0.so.0"),
		// Nested node_modules — never pruned by either walk.
		filepath.Join("node_modules", "pkg-a", "node_modules", "deep", "deep.node"),
		// Case difference: ELF candidate (case-insensitive ext) but NOT a native
		// addon match (case-sensitive suffix). This is the pair that makes a
		// naive "one merged predicate" refactor change a verdict.
		filepath.Join("node_modules", "pkg-b", "addon.NODE"),
		// Decoys inside node_modules: neither predicate may fire.
		filepath.Join("node_modules", "pkg-c", "notes.node.txt"),
		filepath.Join("node_modules", "pkg-c", "README.md"),
		// Decoy that DOES match the ".so." substring rule, and must keep doing so.
		filepath.Join("node_modules", "pkg-c", "libx.so.txt"),
		// Decoys OUTSIDE node_modules — including one in a directory the
		// dynamic-import scan prunes — which neither walk ever reached.
		filepath.Join("build", "decoy.node"),
		filepath.Join("src", "lib", "decoy.node"),
		filepath.Join("dist", "libdecoy.so"),
	}

	for _, rel := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("not a real binary"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

// TestScanNativeModules_MatchesLegacyTwoWalkClassification is the differential
// guard for the walk merge. Both legacy walks are reimplemented above and run
// against the same fixture, so the two classifications are compared directly
// rather than by inspection.
func TestScanNativeModules_MatchesLegacyTwoWalkClassification(t *testing.T) {
	dir := differentialFixture(t)

	// No package.json and an empty PackageJSON: this test isolates the
	// filesystem classification, which is the only part the walk merge touched.
	scan, err := ScanNativeModules(context.Background(), dir, PackageJSON{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantModules := legacyNativeWalk(dir)
	wantELF := legacyELFWalk(dir)

	// Fixture floor: a fixture that produced nothing would make every
	// comparison below vacuously true.
	if len(wantModules) == 0 || len(wantELF) == 0 {
		t.Fatalf("degenerate fixture: legacy walks found %d modules and %d ELF candidates", len(wantModules), len(wantELF))
	}
	if len(wantModules) == len(wantELF) {
		t.Fatalf("degenerate fixture: the two legacy predicates classified identically (%d each); the fixture must contain at least one file where they disagree", len(wantModules))
	}

	if !reflect.DeepEqual(scan.Native.DetectedModules, wantModules) {
		t.Errorf("DetectedModules mismatch\n single pass: %v\n legacy walk: %v", scan.Native.DetectedModules, wantModules)
	}
	if !scan.Native.HasNativeModules {
		t.Error("HasNativeModules = false, want true")
	}
	if !reflect.DeepEqual(scan.ELFCandidates, wantELF) {
		t.Errorf("ELFCandidates mismatch\n single pass: %v\n legacy walk: %v", scan.ELFCandidates, wantELF)
	}

	// Explicit: the decoys outside node_modules must appear in neither list.
	// Matched by basename, since node_modules itself legitimately contains
	// directories named build/ and lib/.
	decoyNames := map[string]bool{"decoy.node": true, "libdecoy.so": true}
	for _, got := range append(append([]string{}, scan.Native.DetectedModules...), scan.ELFCandidates...) {
		if decoyNames[filepath.Base(got)] {
			t.Errorf("classified %q, which lives outside node_modules and must never be reached", got)
		}
	}

	// And CheckNativeModules, the back-compat wrapper, must still agree with
	// its own former self.
	if legacy := CheckNativeModules(dir, PackageJSON{}); !reflect.DeepEqual(legacy.DetectedModules, wantModules) {
		t.Errorf("CheckNativeModules wrapper drifted: %v, want %v", legacy.DetectedModules, wantModules)
	}
}

// TestScanNativeModules_DependencyOrderIsDeterministic pins the map-iteration
// fix: ranging the dependency map directly made the reported order random.
func TestScanNativeModules_DependencyOrderIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	pkg := PackageJSON{
		Dependencies: map[string]string{
			"sharp": "^0.33.0", "better-sqlite3": "^9.0.0", "bcrypt": "^5.1.0",
			"canvas": "^2.11.0", "argon2": "^0.31.0", "re2": "^1.20.0",
		},
	}

	first := CheckNativeModules(dir, pkg)
	if len(first.DetectedModules) != 6 {
		t.Fatalf("DetectedModules = %v, want all 6 native dependencies", first.DetectedModules)
	}
	for i := 0; i < 25; i++ {
		got := CheckNativeModules(dir, pkg)
		if !reflect.DeepEqual(got.DetectedModules, first.DetectedModules) {
			t.Fatalf("run %d: DetectedModules = %v, want stable %v", i, got.DetectedModules, first.DetectedModules)
		}
		if !reflect.DeepEqual(got.Reasons, first.Reasons) {
			t.Fatalf("run %d: Reasons order drifted", i)
		}
	}
}

// TestScanNativeModules_CancelledContextStopsWalk proves the traversal aborts
// mid-tree instead of running to completion on a cancelled build.
func TestScanNativeModules_CancelledContextStopsWalk(t *testing.T) {
	dir := t.TempDir()
	const pkgs = 60
	for i := 0; i < pkgs; i++ {
		sub := filepath.Join(dir, "node_modules", fmt.Sprintf("pkg%02d", i), "build", "Release")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "addon.node"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Fixture floor.
	baseline, err := ScanNativeModules(context.Background(), dir, PackageJSON{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(baseline.Native.DetectedModules) != pkgs {
		t.Fatalf("baseline DetectedModules = %d, want %d", len(baseline.Native.DetectedModules), pkgs)
	}

	scan, err := ScanNativeModules(cancelAfterNChecks(10), dir, PackageJSON{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(scan.Native.DetectedModules) >= pkgs {
		t.Errorf("DetectedModules = %d, want fewer than the full %d: a cancelled scan must stop walking", len(scan.Native.DetectedModules), pkgs)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	scan, err = ScanNativeModules(cancelled, dir, PackageJSON{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(scan.Native.DetectedModules) != 0 {
		t.Errorf("DetectedModules = %v, want none for an already-cancelled context", scan.Native.DetectedModules)
	}
}
