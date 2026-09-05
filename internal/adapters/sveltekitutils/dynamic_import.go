package sveltekitutils

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DynamicImportCheckResult reports whether JavaScript/TypeScript files in a built
// SvelteKit application contain computed or dynamic import() expressions (e.g.
// import(moduleName), import("./routes/" + name), import(`./pages/${page}`)).
// Bun's single-executable compiler (bun build --compile) cannot statically trace or
// embed dynamically computed module paths, causing runtime failure if invoked.
type DynamicImportCheckResult struct {
	// HasUnsupportedDynamicImports is true if any computed dynamic import() call was found.
	HasUnsupportedDynamicImports bool

	// DetectedLocations lists the relative file path and line number of detected dynamic imports.
	DetectedLocations []string

	// Reasons provides human-readable explanations for each detected computed import.
	Reasons []string

	// SkippedFiles lists files the scan could NOT actually inspect — today only
	// files above the size ceiling, or one that failed mid-read after opening
	// cleanly.
	//
	// This field exists because "I looked at this file and found nothing" and "I
	// never actually looked at this file" are different statements, and a caller
	// that gates a build on HasUnsupportedDynamicImports must be able to tell
	// them apart. Collapsing the two is precisely how secretguard came to report
	// a clean directory it had never read (Lessons.md, 2026-08-18); this check
	// gates builds through StrictNativeAdapter, so it carries the same
	// distinction rather than repeating the same silent false-negative.
	SkippedFiles []DynamicImportSkip
}

// DynamicImportSkip records one file the scan could not inspect, and why.
type DynamicImportSkip struct {
	// FilePath is relative to the scanned target directory where possible.
	FilePath string
	// Reason is a human-readable explanation of why the file was not scanned.
	Reason string
}

// DefaultMaxDynamicImportScanBytes is the per-file text-scan ceiling used when
// ScanDynamicImports is called with maxFileSizeBytes <= 0.
//
// Deliberately the same 16MiB value as secretguard's defaultMaxFileSizeBytes,
// and for the same reason: it comfortably covers a single minified/bundled JS
// chunk from a real SvelteKit build, which is exactly the input class both
// scanners exist to look at. The two scanners walking the same tree with
// different ceilings would mean one of them silently covering less than the
// other for no stated reason.
const DefaultMaxDynamicImportScanBytes = 16 * 1024 * 1024

// dynamicImportSniffBytes is how much of a file's head is read to decide whether
// it looks like binary content, regardless of the file's total size — the same
// cheap NUL sniff secretguard uses, so a mislabeled binary blob with a .js
// extension costs one bounded read rather than a full load.
const dynamicImportSniffBytes = 512

// dynamicImportPrunedDirs are directory names never descended into during the
// project-tree walk.
//
// node_modules and .git were always pruned. The generated-output directories
// were added because they are pure cost with no signal: .svelte-kit/, build/,
// dist/ and .output/ hold Vite/Rollup output, which is both the overwhelming
// bulk of the bytes under a project root and content the developer cannot act
// on — the advice this check prints ("rewrite the computed import as a static
// one") can only be followed in source. Walking them meant opening and
// line-scanning every generated chunk on every build.
//
// Pruning .svelte-kit/ and build/ also removes a concurrency hazard flagged in
// internal/core/pipeline.go: native inspection runs in the same errgroup as
// Prepare, which is concurrently WRITING into those two trees, so a strict
// wiring could previously see a verdict that depended on how far the build had
// gotten. A tree that is never read cannot be read at a nondeterministic moment.
//
// .pokkum/ is the zero-mutation injection sandbox holding Pokkum's own
// generated files; a user cannot edit those either, and secretguard prunes it
// for the same reason.
//
// A caller that genuinely wants to scan one built bundle can still pass that
// file's path directly as targetDir — the non-directory path below scans it
// unconditionally, without consulting this set.
//
// ignoreutils.DefaultPatterns() was considered for this and does not fit: it
// deliberately keeps build/ and dist/ IN scope (its comment says so explicitly,
// as a policy choice for the secret scanner, where generated output is exactly
// where a baked-in secret shows up), and adds rules (.env*, *.map,
// storybook-static/) that are meaningless to a .js/.ts import scan. Widening it
// to cover this case would have changed secretguard's and remotecacheutils'
// coverage as a side effect.
var dynamicImportPrunedDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	".svelte-kit":  true,
	".pokkum":      true,
	"build":        true,
	"dist":         true,
	".output":      true,
}

// Regex matching import(...) call patterns.
var importCallPattern = regexp.MustCompile(`import\s*\(([^)]+)\)`)

// IsStaticImportLiteral reports whether the argument inside import(...) is a
// compile-time static string literal without dynamic expressions.
func IsStaticImportLiteral(arg string) bool {
	arg = strings.TrimSpace(arg)
	if len(arg) < 2 {
		return false
	}

	// Single or double quoted literal: 'path/to/mod' or "path/to/mod"
	if (strings.HasPrefix(arg, "'") && strings.HasSuffix(arg, "'")) ||
		(strings.HasPrefix(arg, `"`) && strings.HasSuffix(arg, `"`)) {
		// Ensure it doesn't contain binary + string concatenation outside quotes
		return !strings.Contains(arg[1:len(arg)-1], "'+'") && !strings.Contains(arg[1:len(arg)-1], `"+"`)
	}

	// Backtick template literal without expression interpolation (${...})
	if strings.HasPrefix(arg, "`") && strings.HasSuffix(arg, "`") {
		return !strings.Contains(arg, "${")
	}

	return false
}

// CheckDynamicImports scans targetDir with the default size ceiling and a
// background context. It is the convenience wrapper around ScanDynamicImports
// for callers with neither a context nor a size policy of their own.
func CheckDynamicImports(targetDir string) DynamicImportCheckResult {
	res, _ := ScanDynamicImports(context.Background(), targetDir, 0)
	return res
}

// ScanDynamicImports recursively scans JavaScript and TypeScript files in
// targetDir (or a single file, if targetDir is not a directory) for computed or
// dynamic import() expressions that Bun cannot resolve at compile time.
//
// maxFileSizeBytes bounds how large a file may be before it is reported as
// skipped instead of scanned; <= 0 selects DefaultMaxDynamicImportScanBytes.
//
// Files are read whole (up to that ceiling) and split with bytes.Split rather
// than fed through a bufio.Scanner. bufio.Scanner caps a token at 64KiB by
// default, and a minified bundle routinely emits a single line far longer than
// that — Scan() then returns false with bufio.ErrTooLong, which the previous
// implementation did not check, so the loop simply ended and the file was
// reported as containing no dynamic imports at all. That is a silent false
// negative on exactly the input class this check exists to reject, and it is
// the identical failure mode Lessons.md already records for secretguard
// (2026-08-18).
//
// It returns a non-nil error only if ctx is cancelled; a per-file failure is
// reported through DynamicImportCheckResult.SkippedFiles instead.
func ScanDynamicImports(ctx context.Context, targetDir string, maxFileSizeBytes int64) (DynamicImportCheckResult, error) {
	var result DynamicImportCheckResult

	if maxFileSizeBytes <= 0 {
		maxFileSizeBytes = DefaultMaxDynamicImportScanBytes
	}

	seen := make(map[string]bool)

	addMatch := func(relPath string, lineNum int, expr string) {
		loc := fmt.Sprintf("%s:%d", relPath, lineNum)
		expr = strings.TrimSpace(expr)
		// Keyed on location AND expression, not location alone. One "line" of a
		// minified bundle is frequently the whole file, so a location-only key
		// reported at most ONE computed import per file however many were
		// present — the sibling one-match-per-line bug from the same secretguard
		// post-mortem. The boolean verdict was unaffected; the diagnostics a
		// developer needs in order to fix the build were not.
		key := loc + "\x00" + expr
		if !seen[key] {
			seen[key] = true
			result.HasUnsupportedDynamicImports = true
			result.DetectedLocations = append(result.DetectedLocations, loc)
			result.Reasons = append(result.Reasons, fmt.Sprintf("%s: computed import(%s) cannot be statically traced by Bun --compile", loc, expr))
		}
	}

	addSkip := func(relPath, reason string) {
		result.SkippedFiles = append(result.SkippedFiles, DynamicImportSkip{FilePath: relPath, Reason: reason})
		result.Reasons = append(result.Reasons, fmt.Sprintf("%s: not scanned for dynamic imports (%s)", relPath, reason))
	}

	scanFile := func(filePath string) {
		relPath, err := filepath.Rel(targetDir, filePath)
		if err != nil {
			relPath = filePath
		}

		f, err := os.Open(filePath)
		if err != nil {
			// A file that cannot be opened at all (permissions, a symlink race,
			// deleted between walk and open) is skipped silently — the same
			// single deliberate exception secretguard's ScanDirectory documents.
			return
		}
		defer f.Close()

		head := make([]byte, dynamicImportSniffBytes)
		n, err := f.Read(head)
		if err != nil && err != io.EOF {
			addSkip(relPath, fmt.Sprintf("read failed: %v", err))
			return
		}
		if bytes.IndexByte(head[:n], 0) >= 0 {
			// Binary content behind a .js/.ts extension was never scannable
			// text, so there is no coverage gap to report.
			return
		}

		info, err := f.Stat()
		if err != nil {
			addSkip(relPath, fmt.Sprintf("stat failed: %v", err))
			return
		}
		if info.Size() > maxFileSizeBytes {
			addSkip(relPath, fmt.Sprintf("file is %d bytes, exceeds the %d byte text-scan limit", info.Size(), maxFileSizeBytes))
			return
		}

		if _, err := f.Seek(0, io.SeekStart); err != nil {
			addSkip(relPath, fmt.Sprintf("seek failed: %v", err))
			return
		}
		data, err := io.ReadAll(f)
		if err != nil {
			addSkip(relPath, fmt.Sprintf("read failed: %v", err))
			return
		}

		for i, lineBytes := range bytes.Split(data, []byte("\n")) {
			lineNum := i + 1
			line := string(lineBytes)

			// Quick skip for lines without import(
			if !strings.Contains(line, "import(") && !strings.Contains(line, "import (") {
				continue
			}

			// Ignore lines that look like comments
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
				continue
			}

			matches := importCallPattern.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				if len(m) >= 2 {
					arg := m[1]
					if !IsStaticImportLiteral(arg) {
						addMatch(relPath, lineNum, arg)
					}
				}
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return result, err
	}

	info, err := os.Stat(targetDir)
	if err != nil {
		return result, nil
	}

	if !info.IsDir() {
		scanFile(targetDir)
		return result, nil
	}

	walkErr := filepath.WalkDir(targetDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			if path != targetDir && dynamicImportPrunedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".js" || ext == ".mjs" || ext == ".cjs" || ext == ".ts" {
			scanFile(path)
		}
		return nil
	})
	if walkErr != nil {
		return result, walkErr
	}

	return result, nil
}
