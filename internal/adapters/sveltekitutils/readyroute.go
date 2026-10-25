package sveltekitutils

import (
	"errors"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
)

// ReadyRouteID is the SvelteKit route a project defines to own its container
// readiness: src/routes/api/ready/+server.(ts|js), served at /api/ready (under
// any paths.base). pokkum-init polls it when the build bakes its path in.
const ReadyRouteID = "api/ready"

// ReadyRoute is what DetectReadyRoute found.
type ReadyRoute struct {
	// Found reports whether the project defines the readiness route.
	Found bool

	// File is the route's +server file, relative to the project directory.
	File string

	// URLPath is the path the running app serves it at, including a literal
	// paths.base. Empty when Found is false, or when the base could not be
	// determined (see Reason).
	URLPath string

	// Reason explains an empty URLPath for a found route: paths.base is set
	// to something other than a string literal (an env lookup, a template),
	// so the served path cannot be known at build time.
	Reason string
}

// DetectReadyRoute looks for the readiness route in the project's routes
// directory (honouring kit.files.routes via ResolveRoutesDir). Route groups
// such as (api) never appear in a URL and are ignored when matching, so
// src/routes/(internal)/api/ready/+server.ts counts.
//
// It never reports a route it did not find: a missing routes directory is
// simply "not found", which keeps readiness at its TCP-connect default.
func DetectReadyRoute(projectDir string) (ReadyRoute, error) {
	routesDir := ResolveRoutesDir(projectDir)
	var found string
	err := filepath.WalkDir(routesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		if d.IsDir() || (d.Name() != "+server.ts" && d.Name() != "+server.js") {
			return nil
		}
		rel, err := filepath.Rel(routesDir, filepath.Dir(path))
		if err != nil {
			return nil
		}
		if routeIDWithoutGroups(rel) == ReadyRouteID {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return ReadyRoute{}, err
	}
	if found == "" {
		return ReadyRoute{}, nil
	}

	route := ReadyRoute{Found: true}
	if rel, err := filepath.Rel(projectDir, found); err == nil {
		route.File = filepath.ToSlash(rel)
	}
	base, known := literalPathsBase(projectDir)
	if !known {
		route.Reason = "kit.paths.base is not a string literal, so the URL the app serves /" + ReadyRouteID + " at is unknown at build time"
		return route, nil
	}
	route.URLPath = strings.TrimSuffix(base, "/") + "/" + ReadyRouteID
	return route, nil
}

// routeIDWithoutGroups turns a routes-relative directory into its URL route
// id, dropping (group) segments.
func routeIDWithoutGroups(rel string) string {
	var kept []string
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == "" || seg == "." || (strings.HasPrefix(seg, "(") && strings.HasSuffix(seg, ")")) {
			continue
		}
		kept = append(kept, seg)
	}
	return strings.Join(kept, "/")
}

var (
	// pathsKeyRe finds the start of a `paths: {` object; pathsObject then
	// scans to its matching brace, because a template literal in it
	// (`${prefix}/x`) contains braces a [^{}]* regex would stop at.
	pathsKeyRe = regexp.MustCompile(`\bpaths\s*:\s*\{`)
	// baseKeyRe finds a `base:` key inside it; baseLiteralRe its literal value.
	baseKeyRe     = regexp.MustCompile(`\bbase\s*:`)
	baseLiteralRe = regexp.MustCompile(`\bbase\s*:\s*(["'` + "`" + `])([^"'` + "`" + `]*)["'` + "`" + `]`)
)

// pathsObject returns the body of the first `paths: { ... }` object in src,
// or ok=false when there is none or its braces never close.
func pathsObject(src string) (body string, ok bool) {
	loc := pathsKeyRe.FindStringIndex(src)
	if loc == nil {
		return "", false
	}
	depth := 1
	for i := loc[1]; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[loc[1]:i], true
			}
		}
	}
	return "", false
}

// literalPathsBase returns the project's kit.paths.base when it is absent
// (""), or a plain string literal. known is false when a base is configured
// but is an expression or template, which is common (process.env.BASE_PATH)
// and cannot be evaluated here.
func literalPathsBase(projectDir string) (base string, known bool) {
	for _, src := range routesConfigSources(projectDir) {
		body, ok := pathsObject(stripJSComments(src))
		if !ok || !baseKeyRe.MatchString(body) {
			continue
		}
		m := baseLiteralRe.FindStringSubmatch(body)
		if m == nil || strings.Contains(m[2], "${") {
			return "", false
		}
		return m[2], true
	}
	return "", true
}
