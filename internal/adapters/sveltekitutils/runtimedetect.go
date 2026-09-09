package sveltekitutils

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// RuntimeSignal is what a project's own toolchain says about which runtime it
// expects, together with the evidence that says it.
//
// This is a PREFERENCE signal, not a compatibility verdict. Nothing here proves
// a project cannot run under the other runtime; a Bun-managed project usually
// runs fine under Node and vice versa. It exists so `pokkum init` can pick the
// least surprising default and say why, not so anything can refuse a build.
type RuntimeSignal struct {
	// Runtime is ports.RuntimeBun, ports.RuntimeNode, or "" when the evidence
	// is absent or contradictory. "" means "no opinion" — callers keep their
	// own default rather than being handed a guess.
	Runtime ports.AppRuntime

	// Reason is a user-facing sentence fragment naming the evidence, e.g.
	// "package.json sets packageManager: bun@1.2.4".
	Reason string

	// Conflict is set when signals pointed both ways. Runtime is "" whenever
	// this is set: a contradiction is a reason to ask, never to pick a side.
	Conflict string
}

// runtimeMarkerFile maps a lockfile to the runtime its presence argues for.
type runtimeMarkerFile struct {
	name    string
	runtime ports.AppRuntime
}

// runtimeLockfiles are checked in a fixed order so the reported evidence is
// deterministic when a project has more than one.
var runtimeLockfiles = []runtimeMarkerFile{
	{"bun.lock", ports.RuntimeBun},
	{"bun.lockb", ports.RuntimeBun},
	{"package-lock.json", ports.RuntimeNode},
	{"pnpm-lock.yaml", ports.RuntimeNode},
	{"yarn.lock", ports.RuntimeNode},
}

// DetectAppRuntime infers the runtime a project's own toolchain implies.
//
// Precedence, strongest evidence first, because these disagree in real
// projects and an arbitrary order would make the answer depend on which check
// happened to run first:
//
//  1. package.json `packageManager` — an explicit, singular declaration.
//  2. Lockfiles — what the project is actually installed with.
//  3. package.json `engines` — a compatibility range, often stale.
//
// A tie inside a tier (a bun.lock AND a package-lock.json) returns no opinion
// with Conflict set. Reporting reduced confidence beats synthesising a verdict
// from evidence that points both ways.
func DetectAppRuntime(projectDir string) RuntimeSignal {
	pkg, err := ReadPackageJSON(projectDir)
	if err == nil {
		if sig, ok := runtimeFromPackageManager(pkg.PackageManager); ok {
			return sig
		}
	}

	if sig, ok := runtimeFromLockfiles(projectDir); ok {
		return sig
	}

	if err == nil {
		if sig, ok := runtimeFromEngines(pkg.Engines); ok {
			return sig
		}
	}

	return RuntimeSignal{}
}

func runtimeFromPackageManager(field string) (RuntimeSignal, bool) {
	name, _, _ := strings.Cut(strings.TrimSpace(field), "@")
	switch strings.ToLower(name) {
	case "bun":
		return RuntimeSignal{
			Runtime: ports.RuntimeBun,
			Reason:  fmt.Sprintf("package.json declares packageManager: %s", strings.TrimSpace(field)),
		}, true
	case "npm", "pnpm", "yarn":
		return RuntimeSignal{
			Runtime: ports.RuntimeNode,
			Reason:  fmt.Sprintf("package.json declares packageManager: %s, which is a Node package manager", strings.TrimSpace(field)),
		}, true
	}
	return RuntimeSignal{}, false
}

func runtimeFromLockfiles(projectDir string) (RuntimeSignal, bool) {
	var found []runtimeMarkerFile
	for _, lf := range runtimeLockfiles {
		if _, err := os.Stat(filepath.Join(projectDir, lf.name)); err == nil {
			found = append(found, lf)
		}
	}
	if len(found) == 0 {
		return RuntimeSignal{}, false
	}

	// Collapse to the distinct runtimes argued for. Two Node lockfiles are not
	// a conflict; a Bun one alongside a Node one is.
	seen := map[ports.AppRuntime][]string{}
	for _, f := range found {
		seen[f.runtime] = append(seen[f.runtime], f.name)
	}
	if len(seen) > 1 {
		names := make([]string, 0, len(found))
		for _, f := range found {
			names = append(names, f.name)
		}
		sort.Strings(names)
		return RuntimeSignal{
			Conflict: fmt.Sprintf("this project has lockfiles for more than one runtime (%s), so Pokkum will not guess", strings.Join(names, ", ")),
		}, true
	}

	for runtime, names := range seen {
		return RuntimeSignal{
			Runtime: runtime,
			Reason:  fmt.Sprintf("found %s in the project root", names[0]),
		}, true
	}
	return RuntimeSignal{}, false
}

func runtimeFromEngines(engines map[string]string) (RuntimeSignal, bool) {
	_, hasBun := engines["bun"]
	_, hasNode := engines["node"]
	switch {
	case hasBun && !hasNode:
		return RuntimeSignal{
			Runtime: ports.RuntimeBun,
			Reason:  fmt.Sprintf("package.json engines requires bun %s", engines["bun"]),
		}, true
	case hasNode && !hasBun:
		return RuntimeSignal{
			Runtime: ports.RuntimeNode,
			Reason:  fmt.Sprintf("package.json engines requires node %s", engines["node"]),
		}, true
	case hasNode && hasBun:
		return RuntimeSignal{
			Conflict: "package.json engines names both bun and node, so Pokkum will not guess",
		}, true
	}
	return RuntimeSignal{}, false
}

// RecommendedBaseForRuntime returns the base image preset that pairs with
// runtime, and a one-line reason.
//
// The pairing is not cosmetic: RuntimeNode takes its Node binary FROM the base
// image, so a base without one produces an image that cannot start. Keeping
// this next to the detection means the two defaults init writes cannot drift
// apart into a combination that does not boot.
func RecommendedBaseForRuntime(runtime ports.AppRuntime) (ports.BaseImagePreset, string) {
	if runtime == ports.RuntimeNode {
		return ports.BaseImageDistrolessNode,
			"the node runtime uses the base image's own Node binary, so the base must ship one"
	}
	return ports.BaseImageDistroless,
		"the bun runtime is downloaded, verified and layered in by Pokkum, so the base needs no language runtime at all"
}
