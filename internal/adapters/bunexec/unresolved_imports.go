package bunexec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
)

// verifyProductionDependenciesResolvable reports which of the project's
// declared production dependencies will not resolve inside the image.
//
// The externals set is not guessed. @sveltejs/adapter-node builds its server
// bundle with rollup's `external` set to exactly
//
//	Object.keys(pkg.dependencies || {}).map(d => new RegExp(`^${d}(\/.*)?$`))
//
// (adapter-node 5.5.7 index.js:76-79; 6.0.0-next.10 adds only
// `@opentelemetry/api`). Everything else — including every devDependency — is
// bundled into the output. So "declared in dependencies" *is* the list of
// specifiers that must exist in a node_modules tree at runtime, read from the
// same file the bundler read it from.
//
// This is why the guard checks the manifest rather than the built bundle. The
// previous attempt scanned the output for bare specifiers and was reverted for
// failing correct builds: it reported packages that adapter-node had bundled
// away, and specifiers that only ever appeared inside JSDoc comments
// (`/** @import { X } from 'types' */`). Both classes are unreachable from
// here — a comment is not a manifest entry, and a bundled-away package is by
// construction absent from `dependencies`.
func verifyProductionDependenciesResolvable(projectDir, stagedModulesDir string) ([]string, error) {
	pkg, err := sveltekitutils.ReadPackageJSON(projectDir)
	if err != nil {
		return nil, fmt.Errorf("bunexec: reading package.json for dependency check: %w", err)
	}
	if len(pkg.Dependencies) == 0 {
		return nil, nil
	}

	var missing []string
	for name := range pkg.Dependencies {
		if stagedModulesDir == "" {
			// Dependencies are declared but nothing was staged at all — every
			// one of them is missing, which is the original bug in its purest
			// form rather than an edge case.
			missing = append(missing, name)
			continue
		}
		if !packageResolves(stagedModulesDir, name) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing, nil
}

// packageResolves reports whether name resolves as a package directory in the
// staged tree. Presence of the directory is not enough: Node and Bun resolve a
// bare specifier through the package's own package.json, so a directory
// without one is not a resolvable package.
func packageResolves(modulesDir, name string) bool {
	// Reject anything that would escape the tree before touching the
	// filesystem. A package name cannot contain "..", so this is a malformed
	// manifest rather than a path to follow.
	if name == "" || strings.Contains(name, "..") || filepath.IsAbs(name) {
		return false
	}
	// Scoped names ("@scope/pkg") are two path segments on disk.
	parts := strings.Split(name, "/")
	dir := filepath.Join(append([]string{modulesDir}, parts...)...)
	info, err := os.Stat(filepath.Join(dir, "package.json"))
	return err == nil && !info.IsDir()
}

// formatMissingDependencies renders the build-failure message.
func formatMissingDependencies(missing []string, stagedModulesDir string) string {
	where := "no dependency tree was staged at all"
	if stagedModulesDir != "" {
		where = "they are absent from " + stagedModulesDir
	}
	return fmt.Sprintf(
		"declared in package.json \"dependencies\" but will not resolve inside the image: %s (%s). "+
			"adapter-node keeps every production dependency external, so the running server imports these by name and the first "+
			"route touching one returns 500 — the container still starts and both probes still pass, which is why this fails the "+
			"build instead. Run `bun install` and rebuild; if a package is not actually needed at runtime, move it to devDependencies",
		strings.Join(missing, ", "), where)
}
