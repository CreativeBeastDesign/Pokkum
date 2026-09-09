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

// StaticBlockerKind enumerates the distinct conditions the static-viability
// analyser can report, independent of the free-form Reason text.
//
// Reason is prose meant for a human terminal; Kind is a stable identifier
// meant for a machine (a JSON consumer of `pokkum init --output json`, or a
// test asserting the guide's documentation still matches the analyser). The
// two must never be collapsed into one: prose is free to be rephrased or
// reworded without breaking anything that matches on it, and a stable
// identifier is free to stay put while its prose improves.
//
// One constant per distinct condition the analyser can produce — not one per
// blocker/caveat call site with duplicated meaning, and not a coarser
// "blocked" vs "caveat" split, which the Blockers/Caveats slices already
// carry. Adding a new disqualifying rule to the analyser means adding a kind
// here too; see internal/adapters/sveltekitutils/staticviability.go's
// classifyFile family for the source of truth this enum mirrors.
type StaticBlockerKind string

const (
	// StaticBlockerPrerenderFalse: an explicit `export const prerender =
	// false` was found, anywhere in the project.
	StaticBlockerPrerenderFalse StaticBlockerKind = "prerender-false"

	// StaticBlockerPageAndServerCoexist: a route directory holds both a
	// +page and a +server file. SvelteKit refuses to prerender a route with
	// both (analyse.js:102).
	StaticBlockerPageAndServerCoexist StaticBlockerKind = "page-and-server-coexist"

	// StaticBlockerBodyDependentHandler: a +server file exports a handler
	// whose response depends on the request body — POST, PUT, PATCH, DELETE
	// or QUERY (analyse.js:185).
	StaticBlockerBodyDependentHandler StaticBlockerKind = "body-dependent-handler"

	// StaticBlockerFormActions: a +page.server / +layout.server file
	// declares `export const actions`, which SvelteKit refuses to
	// prerender unconditionally (page/index.js:89).
	StaticBlockerFormActions StaticBlockerKind = "form-actions"

	// StaticBlockerRemoteServerHelper: a `*.remote` module calls one of the
	// server-only remote-function helpers (query, form, command), each of
	// which runs on the server for every call.
	StaticBlockerRemoteServerHelper StaticBlockerKind = "remote-server-helper"

	// StaticBlockerUnrecognizedRemoteModule: a `*.remote` module whose
	// exports the scan did not recognise. Fail closed rather than report
	// "nothing found" for a file whose entire purpose is server execution.
	StaticBlockerUnrecognizedRemoteModule StaticBlockerKind = "unrecognized-remote-module"

	// StaticBlockerServerHooks (a CAVEAT, not a blocker): hooks.server runs
	// only while prerendering in a static build, so per-request logic there
	// (auth, redirects, locals) will not run for real visitors.
	StaticBlockerServerHooks StaticBlockerKind = "server-hooks"

	// StaticBlockerUnreachableDynamicRoute (a CAVEAT, not a blocker): a
	// dynamic route with no `entries()` export is prerendered only if the
	// crawler reaches it from a link; SvelteKit fails the build for a
	// prerenderable route it never reached.
	StaticBlockerUnreachableDynamicRoute StaticBlockerKind = "unreachable-dynamic-route"
)

// StaticFinding is one piece of evidence, always carrying the file that
// produced it so a user can go look rather than take the tool's word for it.
type StaticFinding struct {
	// File is slash-separated and relative to the project directory.
	File string
	// Kind identifies which condition produced this finding. Every
	// StaticFinding the analyser constructs MUST set a non-empty Kind — an
	// empty one is a bug, not a valid "uncategorised" state.
	Kind StaticBlockerKind
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
