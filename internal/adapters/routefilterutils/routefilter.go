// Package routefilterutils drops prerendered routes from a built SvelteKit
// output tree before it is packaged, and reports links left pointing at what
// was dropped.
//
// The motivating case is a route that is fine to build but has no business in
// a production image — a component gallery, a design-system playground, an
// internal dashboard. Excluding it at the source (removing it from the app)
// costs a build configuration; excluding it here costs one line of
// .pokkum.yaml and leaves the developer's own `vite build` untouched.
//
// Scope is deliberately narrow and worth stating plainly: this filters
// *prerendered output files*. A server-rendered route on the layered strategy
// lives inside the bundled server JS and cannot be removed by deleting a file,
// so ApplyExclusions reports it as unmatched rather than pretending it was
// excluded — see Result.UnmatchedPatterns.
package routefilterutils

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// IsUtilityPackage marks this as a shared helper rather than a port adapter.
const IsUtilityPackage = true

// Result reports what ApplyExclusions did, in deterministic order.
type Result struct {
	// ExcludedRoutes are the route paths whose files were removed.
	ExcludedRoutes []string
	// RemovedFiles are the tree-relative paths that were deleted.
	RemovedFiles []string
	// UnmatchedPatterns are configured patterns that matched no prerendered
	// route. A pattern can go unmatched because it is a typo, or because it
	// names a server-rendered route that is not a file at all — both are worth
	// telling the operator about, because in both cases the route they asked
	// to exclude is still reachable in the image.
	UnmatchedPatterns []string
	// SkippedSymlinks are tree-relative paths that matched an exclusion but
	// were left alone because they are symlinks.
	SkippedSymlinks []string
}

// DeadLink is a link in a surviving page that points at an excluded route.
type DeadLink struct {
	// FromPage is the tree-relative path of the page containing the link.
	FromPage string
	// Href is the link target, as written in the HTML.
	Href string
	// Route is the normalized route Href resolves to.
	Route string
}

// ValidatePatterns reports the first structural problem in a pattern set, so a
// typo surfaces at config-validation time rather than as a silently unmatched
// pattern after a full build.
func ValidatePatterns(patterns []string) error {
	for _, p := range patterns {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			return fmt.Errorf("route exclusion pattern is empty")
		}
		if !strings.HasPrefix(trimmed, "/") {
			return fmt.Errorf("route exclusion pattern %q must start with %q (routes are absolute paths)", p, "/")
		}
		if strings.Contains(trimmed, "://") {
			return fmt.Errorf("route exclusion pattern %q looks like a URL; use a route path such as %q", p, "/storybook")
		}
	}
	return nil
}

// RouteForFile maps a prerendered file's tree-relative path to the route it
// serves. SvelteKit emits a route as either "<route>.html" or
// "<route>/index.html" depending on the trailing-slash setting, and both must
// map to the same route or an exclusion would catch only one shape.
func RouteForFile(rel string) string {
	clean := path.Clean("/" + filepath.ToSlash(rel))
	// A precompressed sibling serves the same route: index.html.br is what a
	// browser sending Accept-Encoding: br actually receives, so excluding
	// "/" while leaving index.html.br behind would remove the route for
	// nobody. Pokkum's own precompression runs downstream of this filter and
	// so never produces one for an excluded route, but a build that emitted
	// its own would otherwise slip through.
	for _, enc := range []string{".br", ".gz", ".zst"} {
		if strings.HasSuffix(clean, enc) {
			clean = strings.TrimSuffix(clean, enc)
			break
		}
	}
	switch {
	case clean == "/index.html":
		return "/"
	case strings.HasSuffix(clean, "/index.html"):
		return strings.TrimSuffix(clean, "/index.html")
	case strings.HasSuffix(clean, ".html"):
		return strings.TrimSuffix(clean, ".html")
	}
	// Prerendered endpoints (.json, .xml, .txt) are routes too, and keep their
	// extension — /sitemap.xml is the route, not /sitemap.
	return clean
}

// MatchesAny reports whether route is covered by any pattern.
//
// A pattern without a wildcard covers the route and everything beneath it:
// "/storybook" excludes /storybook, /storybook/button and /storybook/a/b. That
// is what "exclude this route" is nearly always taken to mean, and requiring
// "/storybook" plus "/storybook/**" to express it would make the common case
// the verbose one. Wildcards are available where narrower matching is wanted:
// "*" matches within one segment, "**" matches across segments.
func MatchesAny(route string, patterns []string) bool {
	for _, p := range patterns {
		if matches(route, strings.TrimSpace(p)) {
			return true
		}
	}
	return false
}

func matches(route, pattern string) bool {
	if pattern == "" {
		return false
	}
	pattern = path.Clean(pattern)
	route = path.Clean(route)
	if pattern == route {
		return true
	}
	if strings.Contains(pattern, "*") {
		if strings.HasSuffix(pattern, "/**") {
			return strings.HasPrefix(route, strings.TrimSuffix(pattern, "/**")+"/")
		}
		// Translate "**" to a segment-crossing match by comparing on a
		// separator path.Match cannot see through.
		if strings.Contains(pattern, "**") {
			re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*\*`, ".*") + "$"
			re = strings.ReplaceAll(re, `\*`, "[^/]*")
			ok, err := regexp.MatchString(re, route)
			return err == nil && ok
		}
		ok, err := path.Match(pattern, route)
		return err == nil && ok
	}
	// Bare prefix: cover the subtree, on a segment boundary so that
	// "/admin" does not swallow "/administration".
	return strings.HasPrefix(route, pattern+"/")
}

// ApplyExclusions removes every prerendered file whose route matches a
// pattern.
//
// Deletion goes through an os.Root scoped to the output tree, and the walk
// only *collects* matches — nothing is removed inside the WalkDir callback.
// Both halves matter: a path resolved during a walk and acted on afterwards is
// the classic symlink TOCTOU (a build tree is attacker-influenced whenever a
// dependency's build step can write into it), and os.Root refuses to traverse
// a symlink out of the tree even if one is swapped in between the two passes.
// See Lessons.md's walk-callback-symlink-toctou entry.
func ApplyExclusions(prerenderedDir string, patterns []string) (Result, error) {
	var res Result
	if len(patterns) == 0 {
		return res, nil
	}
	if _, err := os.Stat(prerenderedDir); os.IsNotExist(err) {
		res.UnmatchedPatterns = normalizedPatterns(patterns)
		return res, nil
	}

	type match struct {
		rel     string
		route   string
		symlink bool
	}
	var matched []match
	matchedPattern := make(map[string]bool, len(patterns))

	walkErr := filepath.WalkDir(prerenderedDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(prerenderedDir, p)
		if relErr != nil {
			return relErr
		}
		route := RouteForFile(rel)

		var hit string
		for _, pat := range patterns {
			if matches(route, strings.TrimSpace(pat)) {
				hit = strings.TrimSpace(pat)
				break
			}
		}
		if hit == "" {
			return nil
		}
		matchedPattern[hit] = true
		matched = append(matched, match{
			rel:     filepath.ToSlash(rel),
			route:   route,
			symlink: d.Type()&os.ModeSymlink != 0,
		})
		return nil
	})
	if walkErr != nil {
		return Result{}, fmt.Errorf("routefilterutils: walking %s: %w", prerenderedDir, walkErr)
	}

	root, err := os.OpenRoot(prerenderedDir)
	if err != nil {
		return Result{}, fmt.Errorf("routefilterutils: opening %s: %w", prerenderedDir, err)
	}
	defer func() { _ = root.Close() }()

	excluded := make(map[string]bool, len(matched))
	for _, m := range matched {
		if m.symlink {
			res.SkippedSymlinks = append(res.SkippedSymlinks, m.rel)
			continue
		}
		if rmErr := root.Remove(filepath.FromSlash(m.rel)); rmErr != nil {
			return Result{}, fmt.Errorf("routefilterutils: removing excluded route file %s: %w", m.rel, rmErr)
		}
		res.RemovedFiles = append(res.RemovedFiles, m.rel)
		excluded[m.route] = true
	}

	for _, pat := range normalizedPatterns(patterns) {
		if !matchedPattern[pat] {
			res.UnmatchedPatterns = append(res.UnmatchedPatterns, pat)
		}
	}
	for r := range excluded {
		res.ExcludedRoutes = append(res.ExcludedRoutes, r)
	}
	sort.Strings(res.ExcludedRoutes)
	sort.Strings(res.RemovedFiles)
	sort.Strings(res.SkippedSymlinks)
	sort.Strings(res.UnmatchedPatterns)
	return res, nil
}

func normalizedPatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	seen := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		t := strings.TrimSpace(p)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

var hrefRe = regexp.MustCompile(`(?i)\b(?:href|src)\s*=\s*["']([^"']+)["']`)

// FindDeadLinks scans the surviving pages for links into an excluded route.
//
// Excluding a route the rest of the site still links to leaves a visitor a
// 404, which is a worse outcome than the page they were not supposed to see —
// so this is reported rather than left to be discovered in production.
func FindDeadLinks(prerenderedDir string, patterns []string) ([]DeadLink, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	if _, err := os.Stat(prerenderedDir); os.IsNotExist(err) {
		return nil, nil
	}
	root, rootErr := os.OpenRoot(prerenderedDir)
	if rootErr != nil {
		return nil, fmt.Errorf("routefilterutils: opening %s: %w", prerenderedDir, rootErr)
	}
	defer func() { _ = root.Close() }()

	var links []DeadLink
	err := filepath.WalkDir(prerenderedDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if ext := strings.ToLower(filepath.Ext(p)); ext != ".html" && ext != ".htm" {
			return nil
		}
		rel, relErr := filepath.Rel(prerenderedDir, p)
		if relErr != nil {
			return relErr
		}
		f, openErr := root.Open(rel)
		if openErr != nil {
			return fmt.Errorf("routefilterutils: opening %s: %w", rel, openErr)
		}
		body, readErr := io.ReadAll(f)
		_ = f.Close()
		if readErr != nil {
			return fmt.Errorf("routefilterutils: reading %s: %w", rel, readErr)
		}
		seen := make(map[string]bool)
		// FindAllStringSubmatch, not FindStringSubmatch: prerendered HTML is
		// routinely one very long line, and stopping at the first match per
		// file would hide every link after it (self_review_checklist row 26).
		for _, m := range hrefRe.FindAllStringSubmatch(string(body), -1) {
			href := m[1]
			route, ok := routeForHref(href)
			if !ok || seen[href] || !MatchesAny(route, patterns) {
				continue
			}
			seen[href] = true
			links = append(links, DeadLink{FromPage: filepath.ToSlash(rel), Href: href, Route: route})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].FromPage != links[j].FromPage {
			return links[i].FromPage < links[j].FromPage
		}
		return links[i].Href < links[j].Href
	})
	return links, nil
}

// routeForHref normalizes an href to a route, reporting false for anything
// that does not address a route on this site (external URLs, anchors,
// mailto:, protocol-relative links).
func routeForHref(href string) (string, bool) {
	h := strings.TrimSpace(href)
	if h == "" || strings.HasPrefix(h, "#") || strings.HasPrefix(h, "//") || strings.Contains(h, "://") {
		return "", false
	}
	for _, scheme := range []string{"mailto:", "tel:", "data:"} {
		if strings.HasPrefix(h, scheme) {
			return "", false
		}
	}
	if !strings.HasPrefix(h, "/") {
		// Relative links would need the containing page's own route to
		// resolve; out of scope rather than guessed at.
		return "", false
	}
	if i := strings.IndexAny(h, "?#"); i >= 0 {
		h = h[:i]
	}
	if h == "" {
		return "", false
	}
	return path.Clean(h), true
}
