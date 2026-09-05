package striputils

// Baselines for the two costs StripDirectory is made of: classifying a
// candidate file, and running the external strip tool over a whole tree.
//
// The strip tool on PATH is a no-op stand-in in both cases. That is
// deliberate: on a macOS host the real tool fails on every ELF file, so a
// benchmark against it would measure an error path, and on a Linux host it
// would measure LLVM's speed rather than this package's.

import (
	"context"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var benchEpoch = time.Unix(1700000000, 0)

// benchStripTree materialises n ELF binaries (one cross-compile, then copies)
// plus non-ELF noise, and points PATH at a strip that succeeds instantly.
func benchStripTree(b *testing.B, n int) string {
	b.Helper()

	seedDir := b.TempDir()
	seed := filepath.Join(seedDir, "seed.node")
	buildELFFixtureB(b, seed)
	payload, err := os.ReadFile(seed)
	if err != nil {
		b.Fatalf("read seed: %v", err)
	}

	root := b.TempDir()
	for i := 0; i < n; i++ {
		p := filepath.Join(root, "pkg", "sub", "addon-"+itoa(i)+".node")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, payload, 0o755); err != nil {
			b.Fatalf("write: %v", err)
		}
	}
	// Non-ELF files the walk must classify and skip.
	for i := 0; i < n*4; i++ {
		p := filepath.Join(root, "pkg", "js", "chunk-"+itoa(i)+".js")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("export default function(){return 1}"), 0o644); err != nil {
			b.Fatalf("write: %v", err)
		}
	}

	binDir := b.TempDir()
	fake := filepath.Join(binDir, "strip")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		b.Fatalf("write fake strip: %v", err)
	}
	b.Setenv("PATH", binDir)
	orig := fallbackStripPaths
	fallbackStripPaths = nil
	b.Cleanup(func() { fallbackStripPaths = orig })

	return root
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// buildELFFixtureB mirrors buildELFFixture for a *testing.B.
func buildELFFixtureB(b *testing.B, dst string) {
	b.Helper()
	srcDir := b.TempDir()
	srcPath := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(srcPath, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		b.Fatalf("write fixture source: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", dst, srcPath)
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		b.Skipf("could not cross-compile ELF fixture: %v\n%s", err, out)
	}
}

// BenchmarkProbeELF contrasts the single-open probe against the three-open
// sequence it replaced: IsELFBinary in the walk, IsELFBinary again inside
// StripELFFile, then elf.Open.
func BenchmarkProbeELF(b *testing.B) {
	dir := b.TempDir()
	p := filepath.Join(dir, "addon.node")
	buildELFFixtureB(b, p)

	b.Run("single_probe", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if isELF, parses := probeELF(p); !isELF || !parses {
				b.Fatal("fixture is not a parseable ELF")
			}
		}
	})

	b.Run("legacy_three_opens", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if !IsELFBinary(p) {
				b.Fatal("not elf")
			}
			if !IsELFBinary(p) {
				b.Fatal("not elf")
			}
			ef, err := elf.Open(p)
			if err != nil {
				b.Fatalf("elf.Open: %v", err)
			}
			_ = ef.Close()
		}
	})
}

// BenchmarkStripDirectory measures the whole walk: classify every file, then
// exec the strip tool once per ELF binary.
func BenchmarkStripDirectory(b *testing.B) {
	root := benchStripTree(b, 64)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stripped, skipped, err := StripDirectory(context.Background(), root, benchEpoch)
		if err != nil {
			b.Fatalf("StripDirectory: %v", err)
		}
		if stripped != 64 || len(skipped) != 0 {
			b.Fatalf("degenerate fixture: stripped=%d skipped=%d", stripped, len(skipped))
		}
	}
}
