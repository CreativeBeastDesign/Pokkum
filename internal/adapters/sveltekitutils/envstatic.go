package sveltekitutils

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// staticEnvImportRegex matches an import statement pulling from
// $env/static/public or $env/static/private — SvelteKit's virtual modules
// that inline process.env values as literal `export const KEY = "value"`
// statements at BUILD time (resolved by the SvelteKit Vite plugin's own
// resolveId/load hooks; no file is ever written to disk for them). This is
// deliberately distinct from $env/dynamic/*, which is read at container
// startup and never baked in — a specifier containing "static" is required,
// so dynamic imports never match.
//
// Capture groups: (1) the "type " keyword if present (import type is erased
// at compile time and bakes nothing), (2) the brace-enclosed named-binding
// list if this is a named import, (3) a "* as NAME" namespace binding if
// this is a namespace import, (4) which of public/private was imported.
var staticEnvImportRegex = regexp.MustCompile(
	`import\s+(type\s+)?(?:\{([^}]*)\}|(\*\s+as\s+\w+))\s+from\s+['"]\$env/static/(public|private)['"]`,
)

// sourceFileExtensions are the file types SvelteKit source lives in that a
// build-time import scan needs to look inside. .svelte files carry
// <script>/<script module> blocks that import exactly like plain .ts/.js.
var sourceFileExtensions = []string{".ts", ".js", ".svelte", ".mts", ".mjs"}

// skippedSourceDirs are never real project source — walking into them would
// mean scanning vendored/generated code (or, worse, a previous Pokkum
// build's own virtual sandbox) for imports the user never wrote.
var skippedSourceDirs = map[string]bool{
	"node_modules": true,
	".svelte-kit":  true,
	".pokkum":      true,
	".git":         true,
	"build":        true,
	"dist":         true,
}

// DetectStaticEnvBindings scans srcDir (recursively) for imports from
// $env/static/public or $env/static/private, returning the sorted, deduped
// set of binding names found. A namespace import (`import * as env from
// '$env/static/public'`) cannot be resolved to individual names statically,
// so it is reported as "<module> (namespace import)" instead.
//
// This is a source-level heuristic, not a data-flow analysis: it will not
// follow a re-export (a shared src/lib/config.ts that imports
// $env/static/public and re-exports it, with consumers importing from
// config.ts instead) and will not see a dynamically-computed import
// specifier. Both are real gaps a determined user could hide baked values
// behind, but the goal here — matching PB-3's own framing — is to catch the
// overwhelmingly common direct-import case honestly, not to provide an
// airtight guarantee; nothing in this codebase's reproducibility model
// claims otherwise.
func DetectStaticEnvBindings(srcDir string) ([]string, error) {
	var found []string

	err := filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if skippedSourceDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !slices.Contains(sourceFileExtensions, filepath.Ext(p)) {
			return nil
		}

		data, readErr := readFileLimited(p)
		if readErr != nil {
			// A file that vanished or became unreadable between WalkDir's
			// stat and this read is not this scan's problem to solve —
			// skip it the same way a race against a concurrent editor would
			// be tolerated by any other best-effort source scan.
			return nil
		}

		for _, m := range staticEnvImportRegex.FindAllStringSubmatch(data, -1) {
			isTypeOnly, named, namespace, module := m[1], m[2], m[3], m[4]
			if isTypeOnly != "" {
				continue // erased at compile time, bakes nothing
			}
			if namespace != "" {
				found = append(found, "$env/static/"+module+" (namespace import)")
				continue
			}
			for _, binding := range strings.Split(named, ",") {
				binding = strings.TrimSpace(binding)
				if binding == "" {
					continue
				}
				// "Original as Alias" — the baked value is the original
				// exported name, not the local alias.
				if before, _, ok := strings.Cut(binding, " as "); ok {
					binding = strings.TrimSpace(before)
				}
				found = append(found, binding)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	slices.Sort(found)
	found = slices.Compact(found)
	return found, nil
}

// readFileLimited reads a source file capped at 4MB — real SvelteKit source
// files are a few KB; a cap avoids a pathological huge file (an accidentally
// committed bundle, a binary misnamed with a source extension) turning a
// cheap pre-build scan into an unbounded read.
func readFileLimited(p string) (string, error) {
	const maxSourceFileSize = 4 << 20
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSourceFileSize))
	if err != nil {
		return "", err
	}
	return string(data), nil
}
