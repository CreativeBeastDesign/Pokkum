package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// projectAnalysis is what init learned by looking at the project, before it
// asks the user anything.
//
// The design rule for the whole feature: every question that can be answered by
// reading the project is not asked, it is pre-answered with its evidence shown.
// The user still overrides any of it — the prompts are unchanged in shape, only
// their defaults and their explanations now come from the project rather than
// from a constant.
type projectAnalysis struct {
	static  sveltekitutils.StaticReport
	runtime sveltekitutils.RuntimeSignal

	// adapterStaticConfigured reports whether the project already has
	// @sveltejs/adapter-static as a dependency.
	adapterStaticConfigured bool
}

func analyzeProject(dir string) projectAnalysis {
	a := projectAnalysis{
		static:  sveltekitutils.AnalyzeStaticViability(dir),
		runtime: sveltekitutils.DetectAppRuntime(dir),
	}
	if pkg, err := sveltekitutils.ReadPackageJSON(dir); err == nil {
		a.adapterStaticConfigured = pkg.HasDependency("@sveltejs/adapter-static")
	}
	return a
}

// suggestedStrategy returns the strategy the analysis argues for, and the
// evidence for it.
//
// Note what this deliberately does NOT do: it never returns "static" on a
// StaticUnknown verdict. "Could not check" and "checked and found nothing
// blocking" are different answers and only one of them is a recommendation.
func (a projectAnalysis) suggestedStrategy() (strategy string, why string) {
	switch a.static.Verdict {
	case sveltekitutils.StaticViable:
		return string(ports.StrategyStatic), fmt.Sprintf(
			"scanned %d route sources under %s and found no server-side code",
			a.static.FilesScanned, a.static.RoutesDir)
	case sveltekitutils.StaticBlocked:
		return string(ports.StrategyLayered), fmt.Sprintf(
			"%s needs a server", pluralFiles(len(a.static.Blockers)))
	default:
		return string(ports.StrategyLayered), ""
	}
}

func pluralFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// writeStaticFindings prints the evidence behind the strategy recommendation.
//
// It prints a line for the unknown case too. A check that says nothing when it
// could not run is indistinguishable from one that ran and found nothing, and
// that is the difference between a recommendation and a guess.
func writeStaticFindings(w io.Writer, a projectAnalysis) {
	r := a.static

	switch r.Verdict {
	case sveltekitutils.StaticUnknown:
		fmt.Fprintf(w, "  ? Could not check whether a static build is possible: %s\n", r.UndecidedWhy)
		fmt.Fprintf(w, "    Defaulting to layered, which works for any SvelteKit project.\n")
		return

	case sveltekitutils.StaticBlocked:
		fmt.Fprintf(w, "  → This project needs a server. %s under %s:\n",
			pluralFiles(len(r.Blockers)), r.RoutesDir)
		for _, b := range r.Blockers {
			fmt.Fprintf(w, "      %s %s\n", b.File, b.Reason)
			if b.Override != "" {
				fmt.Fprintf(w, "        (%s)\n", b.Override)
			}
		}

	case sveltekitutils.StaticViable:
		fmt.Fprintf(w, "  → Nothing in this project rules out a static build: %d route sources scanned under %s,\n",
			r.FilesScanned, r.RoutesDir)
		fmt.Fprintf(w, "    no server endpoints, server load functions, form actions or remote functions.\n")

		// The inverse flag: viable, but not yet set up to actually build that
		// way. Two independent prerequisites, reported separately because a
		// project can be missing either one.
		var missing []string
		if !a.adapterStaticConfigured {
			missing = append(missing, "@sveltejs/adapter-static is not in package.json")
		}
		if !r.RootPrerenderDeclared {
			missing = append(missing, fmt.Sprintf(
				"no `export const prerender = true` in %s/+layout.ts", r.RoutesDir))
		}
		if len(missing) > 0 {
			fmt.Fprintf(w, "    To build it that way you still need: %s.\n", strings.Join(missing, "; and "))
		}

		// Honest about what the scan cannot know. A sound negative inverted is
		// not a positive, and saying so here is cheaper than a support thread
		// about a build that crawled zero pages.
		fmt.Fprintf(w, "    This rules static OUT soundly; it cannot promise it will work — a load function\n")
		fmt.Fprintf(w, "    hitting a runtime-only API, or a dynamic route with no links to crawl, still fails.\n")
	}

	for _, c := range r.Caveats {
		fmt.Fprintf(w, "  ! %s %s\n", c.File, c.Reason)
	}
}

// writeRuntimeFinding prints what the project's toolchain says about bun vs node.
func writeRuntimeFinding(w io.Writer, sig sveltekitutils.RuntimeSignal) {
	switch {
	case sig.Conflict != "":
		fmt.Fprintf(w, "  ? Runtime: %s — keeping the default (%s).\n", sig.Conflict, ports.DefaultAppRuntime)
	case sig.Runtime != "":
		base, why := sveltekitutils.RecommendedBaseForRuntime(sig.Runtime)
		fmt.Fprintf(w, "  → Runtime: %s — %s.\n", sig.Runtime, sig.Reason)
		fmt.Fprintf(w, "    Pairs with base %s: %s.\n", base, why)
	default:
		fmt.Fprintf(w, "  → Runtime: no bun/node preference found in package.json or a lockfile; using the default (%s).\n",
			ports.DefaultAppRuntime)
	}
}

// basePresetAdvice returns the one-line recommendation shown next to each base
// preset in the prompt.
//
// Sourced from the presets that actually exist in ports, so a preset added or
// removed there cannot leave a stale line here — the 2026-08-19 bug where the
// prompt offered `chainguard-static`, an unimplemented roadmap item, was
// exactly a hand-maintained list drifting from the real set.
func basePresetAdvice(preset ports.BaseImagePreset) string {
	switch preset {
	case ports.BaseImageDistroless:
		return "smallest; no shell, no package manager, no language runtime. The default, and the right answer unless you need one of the others"
	case ports.BaseImageChainguard:
		return "Chainguard's hardened base; comparable size, its own CVE feed and update cadence"
	case ports.BaseImageDistrolessNode:
		return "distroless plus a Node.js binary. Required for runtime: node, unnecessary otherwise"
	default:
		return ""
	}
}

// initBasePresets is the set of presets init offers, taken from ports rather
// than written out here.
func initBasePresets() []ports.BaseImagePreset {
	return []ports.BaseImagePreset{
		ports.BaseImageDistroless,
		ports.BaseImageChainguard,
		ports.BaseImageDistrolessNode,
	}
}
