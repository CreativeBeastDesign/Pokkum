package sveltekit

import (
	"bufio"
	"fmt"
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

// CheckDynamicImports recursively scans JavaScript and TypeScript files in targetDir
// (or a specific build file) for computed or dynamic import() expressions that Bun
// cannot resolve at compile time.
//
// It skips node_modules and hidden directories (.git, .svelte-kit cache internals unless specified).
func CheckDynamicImports(targetDir string) DynamicImportCheckResult {
	var result DynamicImportCheckResult
	seen := make(map[string]bool)

	addMatch := func(relPath string, lineNum int, expr string) {
		loc := fmt.Sprintf("%s:%d", relPath, lineNum)
		if !seen[loc] {
			seen[loc] = true
			result.HasUnsupportedDynamicImports = true
			result.DetectedLocations = append(result.DetectedLocations, loc)
			result.Reasons = append(result.Reasons, fmt.Sprintf("%s: computed import(%s) cannot be statically traced by Bun --compile", loc, strings.TrimSpace(expr)))
		}
	}

	scanFile := func(filePath string) {
		file, err := os.Open(filePath)
		if err != nil {
			return
		}
		defer file.Close()

		relPath, err := filepath.Rel(targetDir, filePath)
		if err != nil {
			relPath = filePath
		}

		scanner := bufio.NewScanner(file)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()

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

	info, err := os.Stat(targetDir)
	if err != nil {
		return result
	}

	if !info.IsDir() {
		scanFile(targetDir)
		return result
	}

	_ = filepath.Walk(targetDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == "node_modules" || name == ".git" {
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

	return result
}
