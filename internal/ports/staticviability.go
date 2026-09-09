package ports

import "context"

// StaticVerdict is the outcome of a static-viability analysis.
//
// Three states, not two, and deliberately so: "scanned the project and found
// nothing that blocks static" and "could not scan the project" must never share
// a representation. A verdict keyed on `len(Blockers) == 0` would report the
// most reassuring answer for an unreadable directory, a project with no routes
// directory, and a typo in a path.
type StaticVerdict string

const (
	// StaticUnknown means the analysis could not run. Callers MUST NOT read
	// this as "static is fine", and MUST NOT refuse a build over it: an
	// unscannable project is the absence of evidence, not evidence of a
	// problem. StaticReport.UndecidedWhy says what stopped it.
	StaticUnknown StaticVerdict = "unknown"

	// StaticBlocked means the scan completed and found code SvelteKit refuses
	// to prerender. This is the only verdict a build gate may act on.
	StaticBlocked StaticVerdict = "blocked"

	// StaticViable means the scan completed and found no blockers.
	//
	// A sound NEGATIVE ("these N things rule static out") inverted for
	// convenience — NOT a proof that a static build will succeed. Static
	// viability is not decidable from source alone: a load function fetching a
	// runtime-only API, or a dynamic route with no inbound links for the
	// crawler to follow, survives this scan and fails later.
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
	// finding. Empty means the finding is unconditional — SvelteKit refuses
	// the construct outright and no source edit short of removing it helps.
	Override string
}

// StaticReport is the result of a static-viability analysis.
type StaticReport struct {
	Verdict StaticVerdict

	// UndecidedWhy is set only for StaticUnknown.
	UndecidedWhy string

	// Blockers rule static out. Caveats do not, but are worth showing.
	Blockers []StaticFinding
	Caveats  []StaticFinding

	// FilesScanned is the number of source files actually read — a floor a
	// caller can assert on, so "the walk matched nothing because the
	// classifier broke" cannot masquerade as "the project is clean".
	FilesScanned int

	// RoutesDir is the routes directory the scan used, slash-separated and
	// relative to the project directory.
	RoutesDir string

	// RootPrerenderDeclared reports whether a root-level `export const
	// prerender = true` was found in the routes root.
	RootPrerenderDeclared bool
}

// StaticViabilityRequest asks for an analysis of one project.
type StaticViabilityRequest struct {
	ProjectDir string
}

// StaticViabilityAnalyzer is the boundary port for deciding whether a project's
// source contains anything a purely static (prerendered) build cannot carry.
//
// Deliberately returns no error: an unreadable or absent project is a
// StaticUnknown verdict carrying its reason, because every caller wants to
// degrade to "could not check" rather than abort the operation it is advising.
// A gate that refused a build because the ANALYSIS failed would be strictly
// worse than no gate.
type StaticViabilityAnalyzer interface {
	AnalyzeStaticViability(ctx context.Context, req StaticViabilityRequest) StaticReport
}
