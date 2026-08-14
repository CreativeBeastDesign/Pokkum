package striputils

// White-box (same-package) regression tests for the confirmed bug: on a
// host with no ELF-capable strip tool on PATH (e.g. plain macOS, where
// llvm-strip usually isn't installed and the built-in strip is the Xcode
// Mach-O tool, which exits 1 on an ELF file with "unrecognized option:
// --strip-unneeded"), StripELFFile used to swallow that failure and return
// (false, nil) -- indistinguishable from "there was nothing to strip".
// Native addons ended up packaged completely unstripped with zero warning.
//
// This file lives in package striputils (not striputils_test) solely so it
// can zero out fallbackStripPaths for the duration of a test. Without that,
// a dev machine with Homebrew LLVM installed (a common setup, and true of
// the machine these tests were authored on) would have its real,
// ELF-capable llvm-strip picked up by the Homebrew-fallback search added
// alongside this fix, silently making the "nothing works" scenario
// unreproducible.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildELFFixture cross-compiles a trivial Go program for linux/amd64 into
// dst, producing a real, parseable ELF binary regardless of the host OS.
// This needs no cgo and no network access -- the linux/amd64 standard
// library ships inside GOROOT -- so it works even on a macOS host with no
// ELF tooling of its own, which is exactly the scenario these tests
// exercise. The test is skipped if the go toolchain or its linux/amd64
// support is unavailable.
func buildELFFixture(t *testing.T, dst string) {
	t.Helper()

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build ELF fixture")
	}

	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(srcPath, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write fixture source: %v", err)
	}

	buildCmd := exec.Command("go", "build", "-o", dst, srcPath)
	buildCmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Skipf("could not cross-compile ELF fixture: %v\n%s", err, out)
	}

	if !IsELFBinary(dst) {
		t.Fatal("cross-compiled fixture is not recognized as ELF")
	}
}

// noWorkingStripTool isolates the environment so that no ELF-capable strip
// tool can be found by StripELFFile/StripDirectory:
//
//   - PATH is pointed at a directory containing only a fake "strip" that
//     behaves like macOS's built-in Mach-O-only /usr/bin/strip when given
//     an ELF file with --strip-unneeded: it exits non-zero and complains
//     about the flag, matching what was empirically observed. No
//     llvm-strip is on this PATH.
//   - fallbackStripPaths (the Homebrew keg-only llvm-strip locations this
//     fix also added) is temporarily cleared, so a real llvm-strip
//     installed via Homebrew on the machine running the test can't sneak
//     back in and make the file strip successfully after all.
func noWorkingStripTool(t *testing.T) {
	t.Helper()

	binDir := t.TempDir()
	fakeStrip := filepath.Join(binDir, "strip")
	script := "#!/bin/sh\necho 'error: unrecognized option: --strip-unneeded' >&2\nexit 1\n"
	if err := os.WriteFile(fakeStrip, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake strip: %v", err)
	}
	t.Setenv("PATH", binDir)

	origFallback := fallbackStripPaths
	fallbackStripPaths = nil
	t.Cleanup(func() { fallbackStripPaths = origFallback })
}

func TestStripELFFile_NoWorkingStripTool(t *testing.T) {
	tmpDir := t.TempDir()
	elfPath := filepath.Join(tmpDir, "addon.node")
	buildELFFixture(t, elfPath)

	noWorkingStripTool(t)

	ctx := context.Background()
	modTime := time.Unix(1700000000, 0)

	ok, err := StripELFFile(ctx, elfPath, modTime)
	if ok {
		t.Errorf("expected ok=false when no working strip tool is available")
	}
	if err == nil {
		t.Fatal("expected a non-nil error surfacing the missing/broken strip tool, got nil (silent failure)")
	}
	if !errors.Is(err, ErrNoStripTool) {
		t.Errorf("expected error to wrap ErrNoStripTool, got: %v", err)
	}
	if !strings.Contains(err.Error(), "strip-unneeded") {
		t.Errorf("expected error to carry the underlying tool's complaint, got: %v", err)
	}
}

// TestStripDirectory_NoWorkingStripTool_SurfacesSkipped mirrors
// TestStripELFFile_NoWorkingStripTool at the StripDirectory level: the
// caller in packager.go only has StripDirectory's return values to work
// with, so the skip has to survive the directory walk, not just the
// single-file call.
func TestStripDirectory_NoWorkingStripTool_SurfacesSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	nativeDir := filepath.Join(tmpDir, "native")
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatalf("mkdir native dir: %v", err)
	}

	elfPath := filepath.Join(nativeDir, "addon.node")
	buildELFFixture(t, elfPath)

	noWorkingStripTool(t)

	ctx := context.Background()
	modTime := time.Unix(1700000000, 0)

	stripped, skipped, err := StripDirectory(ctx, nativeDir, modTime)
	if stripped != 0 {
		t.Errorf("expected stripped=0, got %d", stripped)
	}
	if len(skipped) != 1 || skipped[0] != elfPath {
		t.Errorf("expected skipped=[%s], got %v", elfPath, skipped)
	}
	if err == nil {
		t.Fatal("expected a non-nil error explaining why the file was left unstripped, got nil")
	}
	if !errors.Is(err, ErrNoStripTool) {
		t.Errorf("expected error to wrap ErrNoStripTool, got: %v", err)
	}
}
