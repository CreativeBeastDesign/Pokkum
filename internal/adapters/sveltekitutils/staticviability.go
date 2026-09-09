package sveltekitutils

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// StaticVerdict is the outcome of a static-viability analysis.
//
// Three states, not two, and deliberately so: "scanned the project and found
// nothing that blocks static" and "could not scan the project" must never share
// a representation. A verdict keyed on `len(Blockers) == 0` would report the
// most reassuring answer for an unreadable directory, a project with no routes
// directory, and a typo in a path — the exact fail-open shape logged three times
// in one pass on 2026-08-21 (`mem:self_review_checklist` rows 52 and 53).
type StaticVerdict string

const (
	// StaticUnknown means the analysis could not run. Callers MUST NOT read
	// this as "static is fine"; StaticReport.UndecidedWhy says what stopped it.
	StaticUnknown StaticVerdict = "unknown"

	// StaticBlocked means the scan completed and found server-side code that
	// a purely static build cannot carry.
	StaticBlocked StaticVerdict = "blocked"

	// StaticViable means the scan completed and found no blockers.
	//
	// This is a sound NEGATIVE ("these N things rule static out") inverted for
	// convenience — it is NOT a proof that a static build will succeed. Static
	// viability is not decidable from source alone: a `+page.ts` load that
	// fetches a runtime-only API, a dynamic route with no inbound links for the
	// crawler to follow, or a fetch against a service that only exists in
	// production all survive this scan and fail later. Report it as "nothing
	// here rules static out", never as "static will work".
	StaticViable StaticVerdict = "viable"
)

// StaticFinding is one piece of evidence, always carrying the file that
// produced it so a user can go look rather than take the tool's word for it.
type StaticFinding struct {
	// File is slash-separated and relative to the project directory.
	File string
	// Reason is a single user-facing sentence fragment.
	Reason string
	// Override, when non-empty, names the change that would retire this
	// finding (e.g. adding `export const prerender = true`). Empty means the
	// finding is unconditional — a form action cannot be prerendered at all.
	Override string
}

// StaticReport is the result of AnalyzeStaticViability.
type StaticReport struct {
	Verdict StaticVerdict

	// UndecidedWhy is set only for StaticUnknown.
	UndecidedWhy string

	// Blockers rule static out. Caveats do not, but are worth showing.
	Blockers []StaticFinding
	Caveats  []StaticFinding

	// FilesScanned is the number of source files actually read.
	//
	// A floor a caller can assert on, so "the walk matched nothing because the
	// classifier broke" cannot masquerade as "the project is clean"
	// (`mem:self_review_checklist` row 47).
	FilesScanned int

	// RoutesDir is the routes directory the scan used, slash-separated and
	// relative to the project directory.
	RoutesDir string

	// RootPrerenderDeclared reports whether a root-level `export const
	// prerender = true` was found in the routes root's +layout file. Its
	// absence is what makes a viable project still not build statically today,
	// and is the basis of the recommendation init prints.
	RootPrerenderDeclared bool
}

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
		if !scannedExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}

		relPath, relErr := filepath.Rel(projectDir, path)
		if relErr != nil {
			return nil
		}
		slashPath := filepath.ToSlash(relPath)

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
func classifyFile(report *StaticReport, slashPath, name, src string, underRoutes bool) {
	// An explicit opt-out anywhere rules static out on its own, wherever it
	// sits — a +page.ts, a +layout.ts, or a server file.
	if prerenderFalseRe.MatchString(src) {
		report.Blockers = append(report.Blockers, StaticFinding{
			File:   slashPath,
			Reason: "sets `export const prerender = false`, which opts this route out of prerendering",
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

	case underRoutes && isServerRouteFile(name):
		classifyServerRouteFile(report, slashPath, name, src)
	}
}

func classifyServerRouteFile(report *StaticReport, slashPath, name, src string) {
	// Form actions are the one unconditional case: `export const actions` is
	// request handling by definition and no prerender flag makes it work.
	// Checked before the prerender override so a +page.server.ts that sets
	// BOTH prerender = true and actions is still reported.
	if actionsExportRe.MatchString(src) {
		report.Blockers = append(report.Blockers, StaticFinding{
			File:   slashPath,
			Reason: "declares form actions, which handle POST requests at runtime and cannot be prerendered",
		})
		return
	}
	if prerenderTrueRe.MatchString(src) {
		// Already opted in; the server code runs at build time only.
		return
	}
	if strings.HasPrefix(name, "+server.") {
		report.Blockers = append(report.Blockers, StaticFinding{
			File:     slashPath,
			Reason:   "is a server endpoint, which needs a running server to answer requests",
			Override: "add `export const prerender = true` if its responses are the same for every visitor",
		})
		return
	}
	report.Blockers = append(report.Blockers, StaticFinding{
		File:     slashPath,
		Reason:   "runs a server-side load function on every request",
		Override: "add `export const prerender = true` if its data is fixed at build time",
	})
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

// isServerRouteFile reports whether name is a SvelteKit route file that only
// exists to run on a server.
//
// +page.ts / +layout.ts are absent on purpose: those are universal load
// functions that prerender fine.
func isServerRouteFile(name string) bool {
	for _, prefix := range []string{"+server.", "+page.server.", "+layout.server."} {
		if strings.HasPrefix(name, prefix) && scannedExtensions[strings.ToLower(filepath.Ext(name))] {
			return true
		}
	}
	return false
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
