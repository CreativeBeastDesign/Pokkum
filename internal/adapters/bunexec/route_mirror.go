package bunexec

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/routefilterutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// routesMirrorRelDir is where the filtered routes mirror is staged, relative to
// the project. SvelteKit resolves kit.files.routes against the build's working
// directory — path.resolve(process.cwd(), ...) in its config loader, not
// against the config file's own location — and the build runs with the project
// as its working directory, so a project-relative path is what belongs here.
// Keeping it relative also keeps absolute host paths out of anything the build
// might emit.
const routesMirrorRelDir = ".pokkum/routes"

// minKitVersionForInlineConfig is the first SvelteKit release that accepts
// configuration passed inline to the sveltekit() Vite plugin. Below it there is
// no way to override kit.files.routes without editing the user's own
// svelte.config.js, which Pokkum does not do.
const minKitVersionForInlineConfig = "2.62.0"

// preserveSymlinksRe finds an enabled resolve.preserveSymlinks setting.
var preserveSymlinksRe = regexp.MustCompile(`preserveSymlinks\s*:\s*true`)

// routeMirrorOutcome reports what stageRoutesMirror did.
type routeMirrorOutcome struct {
	// RoutesDir is the project-relative mirror path to hand the injector, or
	// "" when no mirror was staged.
	RoutesDir string
	// ExcludedRoutes are the routes kept out of the build.
	ExcludedRoutes []string
	// UnmatchedPatterns matched no route directory.
	UnmatchedPatterns []string
	// Skipped explains why no mirror was staged, for the caller to log. Empty
	// when a mirror was staged or when no exclusions were requested.
	Skipped string
}

// stageRoutesMirror builds a filtered mirror of the project's routes directory,
// or explains why it could not.
//
// Excluding a route at build time is the only way to keep its *code* out of the
// image: a +page.svelte is a bundle entry point, and reachability is the
// definition of an entry point, so nothing downstream can tree-shake it away.
// When this cannot run, the caller still has output filtering — which removes
// the rendered page but not its chunks — and must say which of the two the user
// actually got.
func stageRoutesMirror(projectDir string, patterns []string, kitVersion string, log *slog.Logger) (routeMirrorOutcome, error) {
	if len(patterns) == 0 {
		return routeMirrorOutcome{}, nil
	}

	if !kitSupportsInlineConfig(kitVersion) {
		return routeMirrorOutcome{Skipped: fmt.Sprintf(
			"this project's SvelteKit (%s) predates %s, which is the first release that accepts configuration "+
				"passed inline to the sveltekit() Vite plugin. Overriding kit.files.routes below that would mean "+
				"editing your svelte.config.js, which Pokkum does not do", kitVersion, minKitVersionForInlineConfig)}, nil
	}

	// A Vite config that preserves symlinks turns the mirror into a broken
	// build rather than a filtered one: every relative import that escapes the
	// routes tree ("../../lib/x") resolves from the mirror's location instead
	// of the file's real one and fails to resolve at all. Verified against a
	// real build, where it fails loudly with UNRESOLVED_IMPORT — but failing
	// during the user's build with a confusing message is worse than declining
	// here with an accurate one.
	if src, _ := readViteConfigSource(projectDir); preserveSymlinksRe.MatchString(src) {
		return routeMirrorOutcome{}, fmt.Errorf(
			"%w: this project's Vite config sets resolve.preserveSymlinks: true, which is incompatible with "+
				"build-time route exclusion — the filtered routes mirror is built from symlinks, and preserving them "+
				"breaks every relative import that reaches outside the routes directory. Remove the setting, or drop "+
				"the route exclusions to build without them", core.ErrInvalidRequest)
	}

	routesDir := resolveRoutesDir(projectDir)
	if _, err := os.Stat(routesDir); err != nil {
		return routeMirrorOutcome{Skipped: fmt.Sprintf("no routes directory found at %s", routesDir)}, nil
	}

	mirror := filepath.Join(projectDir, filepath.FromSlash(routesMirrorRelDir))
	res, err := routefilterutils.BuildFilteredRoutesMirror(routesDir, mirror, patterns)
	if err != nil {
		return routeMirrorOutcome{}, fmt.Errorf("bunexec: staging filtered routes mirror: %w", err)
	}
	if len(res.ExcludedRoutes) == 0 {
		// Nothing matched, so pointing the build at a mirror would add risk for
		// no benefit. Leave the real routes directory in place.
		if rmErr := os.RemoveAll(mirror); rmErr != nil {
			log.Warn("bunexec: could not clean up an unused routes mirror", "path", mirror, "err", rmErr)
		}
		return routeMirrorOutcome{UnmatchedPatterns: res.UnmatchedPatterns}, nil
	}

	log.Info("bunexec: staged a filtered routes mirror; excluded routes are not bundle entry points and their code is not in the image",
		"mirror", mirror, "excluded", res.ExcludedRoutes)
	return routeMirrorOutcome{
		RoutesDir:         routesMirrorRelDir,
		ExcludedRoutes:    res.ExcludedRoutes,
		UnmatchedPatterns: res.UnmatchedPatterns,
	}, nil
}

// resolveRoutesDir returns the project's routes directory, honouring a
// kit.files.routes the project set for itself.
func resolveRoutesDir(projectDir string) string {
	sources := []string{readConfigSource(projectDir)}
	if viteSrc, _ := readViteConfigSource(projectDir); viteSrc != "" {
		sources = append(sources, viteSrc)
	}
	for _, src := range sources {
		if m := routesFilesRe.FindStringSubmatch(src); len(m) == 2 {
			return filepath.Join(projectDir, filepath.FromSlash(m[1]))
		}
	}
	return filepath.Join(projectDir, "src", "routes")
}

var routesFilesRe = regexp.MustCompile(`routes\s*:\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`)

// kitSupportsInlineConfig reports whether kitVersion is at least
// minKitVersionForInlineConfig. An unparseable or empty version is treated as
// supported: refusing a feature because a version string could not be read
// would be a worse failure than attempting it, and the build fails loudly if
// the attempt is genuinely unsupported.
func kitSupportsInlineConfig(kitVersion string) bool {
	got, ok := parseSemverPrefix(kitVersion)
	if !ok {
		return true
	}
	want, _ := parseSemverPrefix(minKitVersionForInlineConfig)
	for i := 0; i < 3; i++ {
		if got[i] != want[i] {
			return got[i] > want[i]
		}
	}
	return true
}

// parseSemverPrefix extracts major/minor/patch from a version string, ignoring
// any range prefix ("^2.62.0") and any suffix ("2.62.0-next.1").
func parseSemverPrefix(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimLeft(strings.TrimSpace(v), "^~>=v ")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) == 0 || parts[0] == "" {
		return out, false
	}
	for i := 0; i < 3 && i < len(parts); i++ {
		n := 0
		for _, r := range parts[i] {
			if r < '0' || r > '9' {
				return out, false
			}
			n = n*10 + int(r-'0')
		}
		out[i] = n
	}
	return out, true
}

// kitVersionForProject resolves the project's installed @sveltejs/kit version,
// falling back to its declared range when node_modules has not been installed.
func kitVersionForProject(projectDir string) string {
	pkg, err := sveltekitutils.ReadPackageJSON(projectDir)
	if err != nil {
		return ""
	}
	return sveltekitutils.ResolveVersion(projectDir, kitPackage, pkg)
}
