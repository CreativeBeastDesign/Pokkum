package pruneutils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// IsUtilityPackage marks this as a reusable utility, not a port adapter.
const IsUtilityPackage = true

// DefaultJunkPatterns defines default patterns for files in node_modules/vendor directories
// that are not needed at production runtime.
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

	// Documentation and project metadata
	"**/README*",
	"**/readme*",
	"**/Readme*",
	"**/CHANGELOG*",
	"**/changelog*",
	"**/CHANGES*",
	"**/HISTORY*",
	"**/LICENSE*",
	"**/license*",
	"**/LICENCE*",
	"**/licence*",
	"**/AUTHORS*",
	"**/CONTRIBUTORS*",
	"**/.npmignore",
	"**/.eslintrc*",
	"**/.prettierrc*",
	"**/.editorconfig",

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

// PruneOptions configures vendor pruning behaviour.
type PruneOptions struct {
	NoPrune       bool     // Disable pruning entirely
	KeepSourcemap bool     // Preserve *.map files even when pruning is enabled
	KeepPatterns  []string // Additional glob patterns to keep/whitelist
}

// PruneResult records what was removed during a prune run.
type PruneResult struct {
	FilesPruned int
	BytesSaved  int64
	PrunedPaths []string
}

// IsJunk returns true if relPath (slash-separated relative path) matches junk blocklists
// and is not exempted by KeepPatterns or KeepSourcemap.
func IsJunk(relPath string, isDir bool, opts PruneOptions) bool {
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

// PruneDirectory walks dir and deletes any files matching junk patterns.
// Empty directories resulting from pruned files are also cleaned up.
// It returns a PruneResult summarizing files removed and bytes saved.
func PruneDirectory(dir string, opts PruneOptions) (PruneResult, error) {
	if opts.NoPrune {
		return PruneResult{}, nil
	}

	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return PruneResult{}, nil
		}
		return PruneResult{}, fmt.Errorf("pruneutils: stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return PruneResult{}, fmt.Errorf("pruneutils: %s is not a directory", dir)
	}

	var res PruneResult
	var dirsToClean []string

	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}

		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}

		isDir := d.IsDir()
		if IsJunk(rel, isDir, opts) {
			if isDir {
				// Remove entire directory and track
				var dirSize int64
				_ = filepath.WalkDir(p, func(_ string, subD os.DirEntry, _ error) error {
					if subD != nil && !subD.IsDir() {
						if fi, err := subD.Info(); err == nil {
							dirSize += fi.Size()
							res.FilesPruned++
						}
					}
					return nil
				})
				if rmErr := os.RemoveAll(p); rmErr == nil {
					res.BytesSaved += dirSize
					res.PrunedPaths = append(res.PrunedPaths, rel)
				}
				return filepath.SkipDir
			}

			// Single regular file
			if fi, fiErr := d.Info(); fiErr == nil {
				res.BytesSaved += fi.Size()
			}
			if rmErr := os.Remove(p); rmErr == nil {
				res.FilesPruned++
				res.PrunedPaths = append(res.PrunedPaths, rel)
			}
			return nil
		}

		if isDir {
			dirsToClean = append(dirsToClean, p)
		}
		return nil
	})

	if err != nil {
		return res, fmt.Errorf("pruneutils: walk %s: %w", dir, err)
	}

	// Clean up empty directories from deepest to shallowest
	for i := len(dirsToClean) - 1; i >= 0; i-- {
		d := dirsToClean[i]
		entries, err := os.ReadDir(d)
		if err == nil && len(entries) == 0 {
			_ = os.Remove(d)
		}
	}

	return res, nil
}
