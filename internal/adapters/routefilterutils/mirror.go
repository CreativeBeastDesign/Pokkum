package routefilterutils

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MirrorResult reports what BuildFilteredRoutesMirror produced.
type MirrorResult struct {
	// ExcludedRoutes are the route paths whose directories were left out of
	// the mirror, in sorted order.
	ExcludedRoutes []string
	// ExcludedDirs are the excluded directories, relative to the routes root.
	ExcludedDirs []string
	// UnmatchedPatterns matched no route directory.
	UnmatchedPatterns []string
}

// BuildFilteredRoutesMirror creates mirrorDir as a symlink mirror of routesDir
// with every route matching patterns left out, and returns what it excluded.
//
// This is the build-time half of route exclusion, and the only half that keeps
// a route's *code* out of the image: SvelteKit treats every `+page.svelte` as a
// bundle entry point, so a route that exists is reachable by definition and no
// amount of tree-shaking removes it. Pointing `kit.files.routes` at a mirror
// that never contained the route is what makes it absent — from the client
// bundle, the server bundle and the route manifest alike.
//
// Directories are symlinked whole wherever possible. That is not just cheaper
// than mirroring file by file, it is safer: a partially mirrored directory that
// omitted its `+layout.svelte` builds cleanly, serves the child route wrapped
// in the *root* layout instead, and warns about nothing — verified against a
// real build. Mirroring every surviving entry of a partially excluded directory,
// layouts included, is what avoids that.
//
// Symlinks (rather than copies) are required for correctness, not convenience.
// Vite resolves a symlinked module to its real path, so a route's relative
// import that escapes the routes tree (`../../lib/thing.js`) still resolves
// against the original location. A copied tree breaks every such import.
func BuildFilteredRoutesMirror(routesDir, mirrorDir string, patterns []string) (MirrorResult, error) {
	var res MirrorResult
	if len(patterns) == 0 {
		return res, nil
	}
	info, err := os.Stat(routesDir)
	if err != nil || !info.IsDir() {
		return res, fmt.Errorf("routefilterutils: routes directory %s is not readable: %w", routesDir, err)
	}

	absRoutes, err := filepath.Abs(routesDir)
	if err != nil {
		return res, err
	}

	if rmErr := os.RemoveAll(mirrorDir); rmErr != nil {
		return res, fmt.Errorf("routefilterutils: clearing mirror %s: %w", mirrorDir, rmErr)
	}

	matched := make(map[string]bool, len(patterns))
	excluded := map[string]string{} // route -> dir

	if err := mirrorLevel(absRoutes, mirrorDir, "", patterns, matched, excluded); err != nil {
		return res, err
	}

	for r, d := range excluded {
		res.ExcludedRoutes = append(res.ExcludedRoutes, r)
		res.ExcludedDirs = append(res.ExcludedDirs, d)
	}
	for _, p := range normalizedPatterns(patterns) {
		if !matched[p] {
			res.UnmatchedPatterns = append(res.UnmatchedPatterns, p)
		}
	}
	sort.Strings(res.ExcludedRoutes)
	sort.Strings(res.ExcludedDirs)
	sort.Strings(res.UnmatchedPatterns)
	return res, nil
}

// mirrorLevel mirrors one directory level. relDir is the path relative to the
// routes root ("" at the top).
func mirrorLevel(srcRoot, mirrorDir, relDir string, patterns []string, matched map[string]bool, excluded map[string]string) error {
	srcDir := filepath.Join(srcRoot, filepath.FromSlash(relDir))
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("routefilterutils: reading %s: %w", srcDir, err)
	}
	// Sorted so the mirror tree is byte-identical between builds.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	if err := os.MkdirAll(mirrorDir, 0o750); err != nil {
		return fmt.Errorf("routefilterutils: creating mirror dir %s: %w", mirrorDir, err)
	}

	for _, e := range entries {
		childRel := e.Name()
		if relDir != "" {
			childRel = relDir + "/" + e.Name()
		}
		srcPath := filepath.Join(srcRoot, filepath.FromSlash(childRel))
		dstPath := filepath.Join(mirrorDir, e.Name())

		if !e.IsDir() {
			// Files at this level are always carried over: a +layout.svelte
			// here governs every surviving sibling route below it.
			if err := os.Symlink(srcPath, dstPath); err != nil {
				return fmt.Errorf("routefilterutils: linking %s: %w", childRel, err)
			}
			continue
		}

		route := RouteForDir(childRel)
		if hit := matchingPattern(route, patterns); hit != "" {
			matched[hit] = true
			excluded[route] = childRel
			continue
		}

		if subtreeHasExclusion(srcRoot, childRel, patterns) {
			// Something beneath this directory is excluded, so it cannot be
			// linked whole — recreate it and mirror its surviving entries,
			// which carries its own +layout.svelte across with them.
			if err := mirrorLevel(srcRoot, dstPath, childRel, patterns, matched, excluded); err != nil {
				return err
			}
			continue
		}

		if err := os.Symlink(srcPath, dstPath); err != nil {
			return fmt.Errorf("routefilterutils: linking %s: %w", childRel, err)
		}
	}
	return nil
}

// subtreeHasExclusion reports whether any directory beneath relDir matches a
// pattern, which decides whether relDir can be symlinked whole.
func subtreeHasExclusion(srcRoot, relDir string, patterns []string) bool {
	entries, err := os.ReadDir(filepath.Join(srcRoot, filepath.FromSlash(relDir)))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		childRel := relDir + "/" + e.Name()
		if matchingPattern(RouteForDir(childRel), patterns) != "" {
			return true
		}
		if subtreeHasExclusion(srcRoot, childRel, patterns) {
			return true
		}
	}
	return false
}

func matchingPattern(route string, patterns []string) string {
	for _, p := range patterns {
		if matches(route, strings.TrimSpace(p)) {
			return strings.TrimSpace(p)
		}
	}
	return ""
}

// RouteForDir maps a directory path under src/routes to the URL route it
// serves. SvelteKit's group directories — "(marketing)" — organise files
// without contributing a URL segment, so a pattern written the way the route
// is actually addressed ("/pricing") must match "(marketing)/pricing".
func RouteForDir(relDir string) string {
	segments := strings.Split(filepath.ToSlash(relDir), "/")
	kept := make([]string, 0, len(segments))
	for _, s := range segments {
		if s == "" || (strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")) {
			continue
		}
		kept = append(kept, s)
	}
	if len(kept) == 0 {
		return "/"
	}
	return "/" + path.Join(kept...)
}
