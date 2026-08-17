package pruneutils

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// IsUtilityPackage marks this as a reusable utility, not a port adapter.
const IsUtilityPackage = true

// DefaultJunkPatterns defines default patterns for files in node_modules/vendor directories
// that are not needed at production runtime.
//
// Documentation/metadata files (README, LICENSE, CHANGELOG, ...) are deliberately NOT
// listed here as wildcard prefixes: see docBaseNames/isDocFile below for why, and for
// how they are matched instead.
var DefaultJunkPatterns = []string{
	// TypeScript definitions and configs
	"**/*.d.ts",
	"**/*.d.ts.map",
	"**/*.d.mts",
	"**/*.d.mts.map",
	"**/*.d.cts",
	"**/*.d.cts.map",
	"**/tsconfig.json",
	"**/tsconfig.*.json",

	// Source maps (pruned unless KeepSourcemap is true)
	"**/*.map",

	// Project metadata that isn't a "doc file" in the README/LICENSE sense but is
	// still never needed at runtime.
	"**/.npmignore",
	"**/.eslintrc*",
	"**/.prettierrc*",
	"**/.editorconfig",
	"**/CODEOWNERS",

	// OS/editor cruft, VCS files and lockfiles: never read at runtime, and their
	// names don't collide with real module names, so a plain wildcard is safe here.
	"**/.DS_Store",
	"**/.gitignore",
	"**/.gitattributes",
	"**/.npmrc",
	"**/yarn.lock",
	"**/package-lock.json",
	"**/pnpm-lock.yaml",
	"**/.vscode/**",
	"**/.idea/**",
	"**/.yarn/cache/**",
	"**/*.log",

	// Tests and tooling
	"**/__tests__/**",
	"**/test/**",
	"**/tests/**",
	"**/*.test.js",
	"**/*.spec.js",
	"**/*.test.mjs",
	"**/*.spec.mjs",
	"**/*.test.ts",
	"**/*.spec.ts",
	"**/*.test.d.ts",
	"**/.github/**",
	"**/.git/**",
	"**/.circleci/**",
	"**/.travis.yml",

	// Build scripts and manifests
	"**/Makefile",
	"**/Dockerfile*",
	"**/docker-compose*.yml",
}

// docBaseNames are the canonical, lowercase basenames of common documentation and
// project-metadata files. isDocFile compares a file's basename against these
// case-insensitively, so README/Readme/readme/ReadMe are all treated alike.
var docBaseNames = []string{
	"readme",
	"changelog",
	"changes",
	"history",
	"license",
	"licence",
	"authors",
	"contributors",
	"notice",
}

// docExtensions are the file extensions (including the leading dot) recognized as
// documentation/text formats when appended to a docBaseNames entry. The empty
// string matches the bare name (e.g. "LICENSE" with no extension at all, which is
// the most common real-world convention).
var docExtensions = []string{
	"",
	".md", ".markdown", ".mdown", ".mkd", ".mkdn",
	".txt", ".text",
	".rst", ".adoc", ".asciidoc", ".textile", ".rdoc", ".org", ".creole", ".pod",
	".wiki", ".1st", ".rtf",
}

// isDocFile reports whether base (a bare filename, no directory component) is a
// documentation/metadata file such as README.md or LICENSE.
//
// This is deliberately NOT a "**/README*" style wildcard prefix match: that
// pattern also matches real runtime source files that merely start with the same
// word, e.g. "readme.js" or "license-checker.js", and would silently delete them
// from the vendor layer. Instead this requires the basename (case-insensitively)
// to be exactly one of the known words, optionally followed by a recognized
// documentation extension — so "README", "README.md" and "readme.txt" match, but
// "readme.js" and "license-checker.js" do not.
func isDocFile(base string) bool {
	lower := strings.ToLower(base)
	for _, name := range docBaseNames {
		if !strings.HasPrefix(lower, name) {
			continue
		}
		suffix := lower[len(name):]
		for _, ext := range docExtensions {
			if suffix == ext {
				return true
			}
		}
	}
	return false
}

// PruneOptions configures vendor pruning behaviour.
type PruneOptions struct {
	NoPrune       bool     // Disable pruning entirely
	KeepSourcemap bool     // Preserve *.map files even when pruning is enabled
	KeepPatterns  []string // Additional glob patterns to keep/whitelist

	// ExcludeDirs names top-level subdirectories (relative to the tree being
	// walked, e.g. "client") to skip entirely — unlike NoPrune/KeepPatterns,
	// which decide file-by-file whether something is disposable junk, this
	// unconditionally omits a whole subtree regardless of NoPrune, because
	// its contents are packaged into a different layer entirely and including
	// them here would duplicate them, not just fail to prune them.
	ExcludeDirs []string
}

// PruneResult records what was pruned from a vendor directory tree: how many
// files were excluded from the packaged layer, how many bytes that saved, and
// (for callers that want the detail) which relative paths were excluded.
//
// This is populated by packager.BuildDirectoryTreeLayerWithPruning as it walks
// the tree deciding, file by file, what to include in the layer — pruning here
// never touches the host filesystem, it only omits junk files from the tar
// stream being built.
type PruneResult struct {
	FilesPruned int
	BytesSaved  int64
	PrunedPaths []string
}

// IsExcludedDir reports whether relPath (slash-separated, relative to the
// tree being walked) is one of opts.ExcludeDirs — an exact top-level
// subdirectory name, not a glob. Unlike IsJunk, this applies even when
// opts.NoPrune is set, since ExcludeDirs isn't about disposable junk, it's
// about a subtree that belongs to a different layer entirely.
func IsExcludedDir(relPath string, opts PruneOptions) bool {
	normPath := strings.TrimPrefix(filepath.ToSlash(relPath), "/")
	for _, ex := range opts.ExcludeDirs {
		if normPath == strings.Trim(ex, "/") {
			return true
		}
	}
	return false
}

// IsJunk returns true if relPath (slash-separated relative path) matches junk blocklists
// and is not exempted by KeepPatterns or KeepSourcemap. The isDir flag is accepted for
// parity with callers that already know whether relPath is a directory (matching itself
// is path/pattern based and works the same for files and directories), but is not
// currently needed to decide the match.
func IsJunk(relPath string, _ bool, opts PruneOptions) bool {
	if opts.NoPrune {
		return false
	}

	normPath := filepath.ToSlash(relPath)
	normPath = strings.TrimPrefix(normPath, "/")
	normPath = strings.TrimPrefix(normPath, "./")

	base := filepath.Base(normPath)

	// Check if matching keep patterns
	for _, keep := range opts.KeepPatterns {
		if matched, _ := doublestar.Match(keep, normPath); matched {
			return false
		}
		if matched, _ := doublestar.Match(keep, base); matched {
			return false
		}
	}

	// Preserve source maps if KeepSourcemap is true
	if opts.KeepSourcemap && strings.HasSuffix(normPath, ".map") && !strings.HasSuffix(normPath, ".d.ts.map") {
		return false
	}

	if isDocFile(base) {
		return true
	}

	for _, pattern := range DefaultJunkPatterns {
		if !opts.KeepSourcemap && pattern == "**/*.map" && strings.HasSuffix(normPath, ".map") {
			return true
		}
		if matched, _ := doublestar.Match(pattern, normPath); matched {
			return true
		}
		if matched, _ := doublestar.Match(pattern, base); matched {
			return true
		}
	}

	return false
}
