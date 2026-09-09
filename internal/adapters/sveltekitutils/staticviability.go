package sveltekitutils

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// The static-viability vocabulary lives in internal/ports and is re-exported
// here as type aliases.
//
// Aliases, not a parallel set of types with a translation layer: the port and
// this implementation must never be able to disagree about what a verdict
// means, and "the same concept declared twice in two packages" is the
// mirrored-constant drift shape logged 2026-08-21. This mirrors how
// internal/core re-exports port vocabulary (core.Platform = ports.Platform).
type (
	StaticVerdict = ports.StaticVerdict
	StaticFinding = ports.StaticFinding
	StaticReport  = ports.StaticReport
)

const (
	StaticUnknown = ports.StaticUnknown
	StaticBlocked = ports.StaticBlocked
	StaticViable  = ports.StaticViable
)

// dirsSkippedInScan are never walked: build output and dependency trees are not
// user-authored source, and node_modules alone would dominate the scan's cost
// while contributing nothing but false positives.
var dirsSkippedInScan = map[string]bool{
	"node_modules": true,
	".git":         true,
	".svelte-kit":  true,
	".pokkum":      true,
	"build":        true,
	"dist":         true,
	".vercel":      true,
	".netlify":     true,
	".output":      true,
}

// scannedExtensions are the source extensions that can carry the declarations
// this analysis looks for.
var scannedExtensions = map[string]bool{
	".ts":  true,
	".js":  true,
	".mjs": true,
	".cjs": true,
}

// bodyDependentMethods mirrors @sveltejs/kit's BODY_DEPENDENT_METHODS
// (src/constants.js: `[...MUTATIVE_METHODS, 'QUERY']`). A +server file
// exporting any of these cannot be prerendered; GET, HEAD and OPTIONS can.
//
// Order is SvelteKit's own, so a multi-method message reads in a familiar
// order rather than in map-iteration order.
var bodyDependentMethods = []string{"POST", "PUT", "PATCH", "DELETE", "QUERY"}

// exportedHandlerRe matches an exported HTTP handler for method, in the three
// shapes SvelteKit accepts: `export function POST`, `export async function
// POST`, and `export const POST =`.
func exportedHandlerRe(method string) *regexp.Regexp {
	return regexp.MustCompile(`export\s+(?:async\s+)?(?:function\s+` + method +
		`\b|(?:const|let|var)\s+` + method + `\s*[:=])`)
}

var (
	prerenderTrueRe  = regexp.MustCompile(`export\s+const\s+prerender\s*(?::[^=]*)?=\s*true\b`)
	prerenderFalseRe = regexp.MustCompile(`export\s+const\s+prerender\s*(?::[^=]*)?=\s*false\b`)
	actionsExportRe  = regexp.MustCompile(`export\s+const\s+actions\b`)
)

// remoteServerHelpers are the SvelteKit remote-function helpers that require a
// running server. `prerender` is deliberately absent: a remote `prerender()` is
// resolved at build time and ships as static data, so a .remote.ts using only
// that one does not rule static out.
var remoteServerHelpers = []string{"query", "form", "command"}

// AnalyzeStaticViability scans a SvelteKit project for code that a purely
// static (prerendered, serverless) build cannot carry.
//
// It is a DISQUALIFIER scan. It answers "does anything here rule static out?"
// soundly, and does not attempt the undecidable converse — see StaticViable.
//
// It never returns an error: an unreadable or absent project is a StaticUnknown
// verdict carrying the reason, because every caller wants to degrade to "could
// not check" rather than abort the command it is advising.
func AnalyzeStaticViability(projectDir string) StaticReport {
	report := StaticReport{Verdict: StaticUnknown}

	routesDir := ResolveRoutesDir(projectDir)
	rel, err := filepath.Rel(projectDir, routesDir)
	if err != nil {
		rel = routesDir
	}
	report.RoutesDir = filepath.ToSlash(rel)

	if info, statErr := os.Stat(routesDir); statErr != nil || !info.IsDir() {
		report.UndecidedWhy = fmt.Sprintf("no routes directory at %s — this does not look like a SvelteKit project, or it keeps its routes somewhere this scan could not find", report.RoutesDir)
		return report
	}

	// Reads go through an os.Root scoped to the project, not through
	// os.ReadFile on the walked path.
	//
	// This walk traverses a tree a dependency's install step can write into,
	// and a path that was a regular file when WalkDir stat'd it can be a
	// symlink out of the project by the time it is opened. os.Root refuses to
	// traverse a symlink that escapes it, which makes the class structurally
	// unrepresentable here rather than merely unlikely — the same conversion
	// the repo's other walk callbacks already carry (gosec G122).
	projectRoot, err := os.OpenRoot(projectDir)
	if err != nil {
		report.UndecidedWhy = fmt.Sprintf("could not open the project directory: %v", err)
		return report
	}
	defer func() { _ = projectRoot.Close() }()

	// Route directories holding a +page and/or a +server file. SvelteKit
	// refuses to prerender a route that has both ("Cannot prerender a route
	// with both +page and +server files", analyse.js:102), and that is a
	// property of a DIRECTORY, not of either file — so it cannot be decided
	// during the per-file walk and is resolved afterwards.
	routePages := map[string]string{}
	routeEndpoints := map[string]string{}

	// Route directories whose leaf exports an `entries()` generator. SvelteKit
	// reads it from the universal or server page module, or from the endpoint
	// module (analyse.js:202 and :222), so any scanned route file in the
	// directory counts.
	routeEntries := map[string]bool{}

	// Walk the project rather than only the routes directory: remote functions
	// (*.remote.ts) are ordinary modules that live wherever the author put
	// them, commonly src/lib, and a routes-only walk would miss every one of
	// them while reporting a completed scan.
	var walkErr error
	err = filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A single unreadable subtree degrades that subtree, not the whole
			// run — but it is recorded, so a mostly-failed walk cannot be
			// mistaken for a clean project.
			if path != projectDir {
				walkErr = err
				return fs.SkipDir
			}
			return err
		}
		if d.IsDir() {
			if path != projectDir && dirsSkippedInScan[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		relPath, relErr := filepath.Rel(projectDir, path)
		if relErr != nil {
			return nil
		}
		slashPath := filepath.ToSlash(relPath)

		// Recorded before the extension filter: +page.svelte is not a scanned
		// source (there is nothing in it this analysis reads), but its
		// PRESENCE is what the co-location rule turns on.
		if isUnderDir(path, routesDir) {
			switch {
			case strings.HasPrefix(d.Name(), "+page."):
				routePages[path4Dir(slashPath)] = slashPath
			case strings.HasPrefix(d.Name(), "+server."):
				routeEndpoints[path4Dir(slashPath)] = slashPath
			}
		}

		if !scannedExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}

		underRoutes := isUnderDir(path, routesDir)
		if !underRoutes && !isRemoteModule(d.Name()) && !isServerHooks(slashPath) {
			// Nothing outside the routes tree matters except remote modules
			// and the server hooks file. Reading every other .ts in src/ would
			// cost time and buy nothing.
			return nil
		}

		// A whole-file read, deliberately, rather than a bufio.Scanner. A
		// default 64KiB scanner token silently reports "found nothing" on a
		// single long line and returns the reassuring answer — the identical
		// defect shipped twice in this repo (secretguard 2026-08-18, and this
		// very package's dynamic_import.go 2026-09-05). Route sources are
		// small; whole-file reads make the whole class unreachable here rather
		// than merely unlikely.
		raw, readErr := projectRoot.ReadFile(filepath.ToSlash(relPath))
		if readErr != nil {
			walkErr = readErr
			return nil
		}
		report.FilesScanned++
		src := blankJSStringsAndComments(string(raw))

		if underRoutes && entriesExportRe.MatchString(src) {
			routeEntries[path4Dir(slashPath)] = true
		}

		classifyFile(&report, slashPath, d.Name(), src, underRoutes)
		return nil
	})

	if err != nil {
		report.UndecidedWhy = fmt.Sprintf("could not read the project directory: %v", err)
		return report
	}
	if report.FilesScanned == 0 {
		report.UndecidedWhy = fmt.Sprintf("found no readable route sources under %s, so there is nothing to base a recommendation on", report.RoutesDir)
		return report
	}
	if walkErr != nil {
		report.UndecidedWhy = fmt.Sprintf("part of the project could not be read (%v), so this scan cannot rule out server-side code in the part it did not see", walkErr)
		return report
	}

	report.RootPrerenderDeclared = rootPrerenderDeclared(routesDir)

	// The co-location rule. Reported against the endpoint, since removing or
	// moving that is the fix, and it names the page so the pair is obvious.
	for dir, endpoint := range routeEndpoints {
		if page, ok := routePages[dir]; ok {
			report.Blockers = append(report.Blockers, StaticFinding{
				File: endpoint,
				Reason: fmt.Sprintf("shares a route with %s, and SvelteKit cannot prerender a route that has both a page and an endpoint",
					page),
			})
		}
	}

	addDynamicRouteCaveats(&report, projectDir, routePages, routeEndpoints, routeEntries)

	sortFindings(report.Blockers)
	sortFindings(report.Caveats)

	if len(report.Blockers) > 0 {
		report.Verdict = StaticBlocked
	} else {
		report.Verdict = StaticViable
	}
	return report
}

// classifyFile records the findings a single already-blanked source produces.
//
// The rules below are SvelteKit's own, read out of its source rather than
// inferred. Every "cannot prerender" error @sveltejs/kit can raise is:
//
//	src/core/postbuild/analyse.js:102   route with both +page and +server files
//	src/core/postbuild/analyse.js:185   +server with BODY_DEPENDENT_METHODS handlers
//	src/core/postbuild/prerender.js:539 root +server.js returning a non-HTML response
//	src/runtime/server/page/index.js:89 pages with actions
//
// and BODY_DEPENDENT_METHODS is `[...MUTATIVE_METHODS, 'QUERY']` =
// POST, PUT, PATCH, DELETE, QUERY (src/constants.js:23).
//
// Two things follow that the first cut of this file got WRONG, both in the
// over-rejecting direction:
//
//   - A GET-only +server.ts prerenders perfectly well. Only a body-dependent
//     handler makes it impossible. Treating every endpoint as a blocker
//     wrongly disqualifies the extremely common read-only /api/health shape —
//     this repo's own testdata/fixtures/sveltekit-basic is exactly that.
//   - A +page.server.ts / +layout.server.ts `load` prerenders fine: it runs at
//     build time. SvelteKit raises no error for one, and reading a CMS or the
//     filesystem in a server load is the normal way to build a static site.
//     Only `export const actions` is genuinely impossible.
//
// Getting this wrong matters more than it looks: `pokkum build` refuses a
// static build on these findings, and Lessons.md's 2026-08-17 entry is a
// preflight check that blocked every real project by making exactly this kind
// of independent, untested assumption.
func classifyFile(report *StaticReport, slashPath, name, src string, underRoutes bool) {
	// An explicit opt-out anywhere rules static out on its own, wherever it
	// sits — a +page.ts, a +layout.ts, or a server file.
	if prerenderFalseRe.MatchString(src) {
		report.Blockers = append(report.Blockers, StaticFinding{
			File:   slashPath,
			Reason: "sets `export const prerender = false`, which opts this route out of prerendering",
			// The only finding a source edit can retire. Every other blocker
			// names something SvelteKit refuses outright (a body-dependent
			// handler, form actions, a page and endpoint sharing a route), so
			// offering an "override" for those would be advice that does not
			// work — see StaticFinding.Override.
			Override: "remove the line, or set it to true, if this route can be rendered at build time",
		})
	}

	switch {
	case isRemoteModule(name):
		classifyRemoteModule(report, slashPath, src)

	case isServerHooks(slashPath):
		// Not a blocker. Server hooks DO run during prerendering, so a project
		// with hooks.server.ts can still build statically — what it loses is
		// anything those hooks were meant to do per-request at runtime. Calling
		// this a blocker would wrongly disqualify projects that build fine
		// today, so it is reported as what it is: something to look at.
		report.Caveats = append(report.Caveats, StaticFinding{
			File:   slashPath,
			Reason: "server hooks run only while prerendering in a static build; per-request logic here (auth, redirects, locals) will not run for real visitors",
		})

	case underRoutes && strings.HasPrefix(name, "+server."):
		classifyEndpoint(report, slashPath, src)

	case underRoutes && (strings.HasPrefix(name, "+page.server.") || strings.HasPrefix(name, "+layout.server.")):
		classifyServerLoadFile(report, slashPath, src)
	}
}

// classifyEndpoint reports a +server file that cannot be prerendered.
//
// Only a body-dependent handler makes that true. A GET/HEAD/OPTIONS endpoint
// is prerendered to a static response and ships fine.
func classifyEndpoint(report *StaticReport, slashPath, src string) {
	var found []string
	for _, method := range bodyDependentMethods {
		if exportedHandlerRe(method).MatchString(src) {
			found = append(found, method)
		}
	}
	if len(found) == 0 {
		return
	}
	report.Blockers = append(report.Blockers, StaticFinding{
		File: slashPath,
		Reason: fmt.Sprintf("exports %s, and SvelteKit cannot prerender a +server file with a handler whose response depends on the request body",
			joinList(found, "a "+found[0]+" handler", "handlers")),
	})
}

// classifyServerLoadFile reports a +page.server / +layout.server file that
// cannot be prerendered.
//
// `export const actions` is the only such case: SvelteKit raises "Cannot
// prerender pages with actions" unconditionally, so no prerender flag retires
// it. A `load` in the same file is NOT a finding — it runs at build time.
func classifyServerLoadFile(report *StaticReport, slashPath, src string) {
	if actionsExportRe.MatchString(src) {
		report.Blockers = append(report.Blockers, StaticFinding{
			File:   slashPath,
			Reason: "declares form actions, which handle POST requests at runtime and cannot be prerendered",
		})
	}
}

// joinList renders a method list: the singular form when there is one, or
// "POST, PUT and DELETE handlers" when there are several.
func joinList(items []string, singular, pluralNoun string) string {
	if len(items) == 1 {
		return singular
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1] + " " + pluralNoun
}

func classifyRemoteModule(report *StaticReport, slashPath, src string) {
	var used []string
	for _, helper := range remoteServerHelpers {
		if regexp.MustCompile(`\b` + helper + `\s*\(`).MatchString(src) {
			used = append(used, helper)
		}
	}
	if len(used) > 0 {
		report.Blockers = append(report.Blockers, StaticFinding{
			File:   slashPath,
			Reason: fmt.Sprintf("declares remote %s, which run on the server for every call", joinHelpers(used)),
		})
		return
	}
	if regexp.MustCompile(`\bprerender\s*\(`).MatchString(src) {
		// A remote prerender() is resolved at build time and ships as data.
		return
	}
	// A .remote.ts whose helpers this scan did not recognise. Fail closed:
	// reporting "nothing found" for a file whose entire purpose is server-side
	// execution is the reassuring-answer-from-no-evidence shape this analysis
	// exists to avoid.
	report.Blockers = append(report.Blockers, StaticFinding{
		File:   slashPath,
		Reason: "is a remote-function module whose exports this scan did not recognise; remote functions run on the server, so this is treated as needing one",
	})
}

func joinHelpers(used []string) string {
	for i, u := range used {
		used[i] = u + "()"
	}
	if len(used) == 1 {
		return used[0]
	}
	return strings.Join(used[:len(used)-1], ", ") + " and " + used[len(used)-1]
}

// isRemoteModule reports whether name is a SvelteKit remote-functions module
// (`*.remote.ts` / `*.remote.js`).
func isRemoteModule(name string) bool {
	lower := strings.ToLower(name)
	for ext := range scannedExtensions {
		if strings.HasSuffix(lower, ".remote"+ext) {
			return true
		}
	}
	return false
}

// isServerHooks reports whether the project-relative slash path is the
// SvelteKit server hooks module.
func isServerHooks(slashPath string) bool {
	base := path4Base(slashPath)
	for ext := range scannedExtensions {
		if base == "hooks.server"+ext {
			return true
		}
	}
	return false
}

// path4Dir returns the directory portion of a slash path, or "" at the root.
func path4Dir(slashPath string) string {
	if i := strings.LastIndex(slashPath, "/"); i >= 0 {
		return slashPath[:i]
	}
	return ""
}

func path4Base(slashPath string) string {
	if i := strings.LastIndex(slashPath, "/"); i >= 0 {
		return slashPath[i+1:]
	}
	return slashPath
}

// rootPrerenderDeclared reports whether the routes root declares
// `export const prerender = true`, which is what adapter-static needs in order
// to prerender the whole site.
func rootPrerenderDeclared(routesDir string) bool {
	for _, name := range []string{
		"+layout.ts", "+layout.js", "+layout.server.ts", "+layout.server.js",
		"+page.ts", "+page.js", "+page.server.ts", "+page.server.js",
	} {
		raw, err := os.ReadFile(filepath.Join(routesDir, name))
		if err != nil {
			continue
		}
		if prerenderTrueRe.MatchString(blankJSStringsAndComments(string(raw))) {
			return true
		}
	}
	return false
}

// isUnderDir reports whether path sits inside dir.
func isUnderDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sortFindings(f []StaticFinding) {
	sort.Slice(f, func(i, j int) bool {
		if f[i].File != f[j].File {
			return f[i].File < f[j].File
		}
		return f[i].Reason < f[j].Reason
	})
}

// ResolveRoutesDir returns the project's routes directory, honouring a
// kit.files.routes the project set for itself in either svelte.config.js or
// vite.config.ts, and falling back to SvelteKit's own src/routes default.
//
// Exported from here rather than duplicated per caller: bunexec needs the same
// answer to stage its filtered routes mirror, and two independent
// implementations of "where are the routes" is the mirrored-constant drift
// shape logged on 2026-08-21.
func ResolveRoutesDir(projectDir string) string {
	for _, src := range routesConfigSources(projectDir) {
		if src == "" {
			continue
		}
		if m := routesFilesRe.FindStringSubmatch(stripJSComments(src)); len(m) == 2 {
			return filepath.Join(projectDir, filepath.FromSlash(m[1]))
		}
	}
	return filepath.Join(projectDir, "src", "routes")
}

// routesConfigSources returns the config sources that can carry kit.files.routes,
// in the order SvelteKit itself would honour them.
func routesConfigSources(projectDir string) []string {
	var out []string
	for _, name := range []string{
		findUserSvelteConfigName(projectDir),
		"vite.config.ts", "vite.config.js", "vite.config.mts", "vite.config.mjs",
	} {
		if name == "" {
			continue
		}
		if raw, err := os.ReadFile(filepath.Join(projectDir, name)); err == nil {
			out = append(out, string(raw))
		}
	}
	return out
}

var routesFilesRe = regexp.MustCompile(`routes\s*:\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`)

// entriesExportRe matches an exported `entries` generator, in the shapes
// SvelteKit accepts: `export function entries`, `export async function
// entries`, `export const entries =`.
var entriesExportRe = regexp.MustCompile(`export\s+(?:async\s+)?(?:function\s+entries\b|(?:const|let|var)\s+entries\s*[:=])`)

// dynamicSegmentRe matches a SvelteKit dynamic route segment: [slug],
// [...rest], [[optional]], and matcher forms like [id=integer].
//
// Route groups — (marketing) — are deliberately NOT matched: they shape the
// layout tree and never appear in a URL, so they need no entries().
var dynamicSegmentRe = regexp.MustCompile(`\[[^\]]*\]`)

// unseenRoutesHandledRe matches a prerender.handleUnseenRoutes set to anything
// other than the default.
//
// The default is `undefined`, which THROWS
// (@sveltejs/kit src/core/postbuild/prerender.js:103) — an unseen prerenderable
// route fails the build. A project that set 'warn' or 'ignore' has opted out of
// that, so the caveat below would be noise for it.
var unseenRoutesHandledRe = regexp.MustCompile(`handleUnseenRoutes\s*:\s*['"` + "`" + `](warn|ignore)['"` + "`" + `]`)

// addDynamicRouteCaveats reports dynamic routes that adapter-static can only
// prerender if the crawler happens to find a link to them.
//
// A CAVEAT, never a blocker, and the distinction is the whole point. SvelteKit
// prerenders a dynamic route when it is reachable by crawling from a
// prerendered page, when the route exports `entries()`, or when it is listed in
// config.prerender.entries. The first of those depends on the rendered HTML of
// every other page — which this scan does not render and cannot predict — so
// "no entries() export" means "this MIGHT not be prerendered", never "this will
// not build". Reporting it as a blocker would refuse the extremely ordinary
// blog-with-linked-posts, which builds correctly today.
func addDynamicRouteCaveats(report *StaticReport, projectDir string, pages, endpoints map[string]string, entries map[string]bool) {
	if projectOptedOutOfUnseenRouteErrors(projectDir) {
		return
	}

	seen := map[string]bool{}
	for _, group := range []map[string]string{pages, endpoints} {
		for dir, file := range group {
			if seen[dir] || entries[dir] {
				continue
			}
			// dir is already the project-relative slash path of the route
			// directory, so it is matched directly. Comparing against
			// routesDir is unnecessary: a dynamic segment anywhere in the
			// route's own path makes the route dynamic, and no path outside
			// the routes tree reaches these maps.
			if !dynamicSegmentRe.MatchString(dir) {
				continue
			}
			seen[dir] = true
			report.Caveats = append(report.Caveats, StaticFinding{
				File: file,
				Reason: "is a dynamic route with no `entries()` export, so it is prerendered only if " +
					"something links to it; SvelteKit fails the build for a prerenderable route it never " +
					"reached while crawling",
				Override: "export an `entries()` generator listing its parameters, or add the paths to config.prerender.entries",
			})
		}
	}
}

// projectOptedOutOfUnseenRouteErrors reports whether the project set
// prerender.handleUnseenRoutes to 'warn' or 'ignore'.
func projectOptedOutOfUnseenRouteErrors(projectDir string) bool {
	for _, src := range routesConfigSources(projectDir) {
		// stripJSComments, NOT blankJSStringsAndComments: this matches a string
		// VALUE ('warn'/'ignore'), so the contents must survive. Using the
		// blanking variant here made the regex unmatchable — the exact
		// distinction stripJS's doc comment exists to force a choice about.
		if unseenRoutesHandledRe.MatchString(stripJSComments(src)) {
			return true
		}
	}
	return false
}
