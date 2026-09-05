package sveltekitutils

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// NativeCheckResult reports whether a SvelteKit project relies on native C++ Node
// packages (.node binaries, node-gyp addons, or known native C++ binding packages).
// Pokkum's bun build --compile pipeline and distroless base image currently cannot
// bundle or execute native C++ addons.
type NativeCheckResult struct {
	// HasNativeModules is true if any native module, .node binary, or native package
	// dependency was detected.
	HasNativeModules bool

	// DetectedModules contains the names of detected native packages or relative paths of .node binaries.
	DetectedModules []string

	// Reasons provides human-readable explanations for each detected native module.
	Reasons []string
}

// Known native C++ Node package names commonly used in Node / SvelteKit apps.
var knownNativePackages = map[string]string{
	"better-sqlite3": "native C++ SQLite3 bindings",
	"sqlite3":        "native C++ SQLite3 bindings",
	"sharp":          "native C++ libvips image processing bindings",
	"canvas":         "native C++ Cairo/Pango graphics bindings",
	"bcrypt":         "native C++ password hashing bindings",
	"deasync":        "native C++ event-loop sync addon",
	"bufferutil":     "native C++ WebSocket buffer utilities",
	"utf-8-validate": "native C++ UTF-8 validation utilities",
	"zeromq":         "native C++ ZeroMQ bindings",
	"nodegit":        "native C++ libgit2 bindings",
	"serialport":     "native C++ hardware serial port bindings",
	"argon2":         "native C++ Argon2 hashing bindings",
	"re2":            "native C++ RE2 regex bindings",
	"leveldown":      "native C++ LevelDB bindings",
	"rocksdb":        "native C++ RocksDB bindings",
}

// NativeBuildTools lists packages that indicate native module building.
var nativeBuildTools = map[string]string{
	"node-gyp":             "native Node build tool",
	"node-pre-gyp":         "prebuilt native binary helper",
	"@mapbox/node-pre-gyp": "prebuilt native binary helper",
	"prebuild-install":     "prebuilt native binary downloader",
	"cmake-js":             "CMake-based native addon builder",
}

// NativeModuleScan is everything ONE traversal of node_modules yields.
//
// It exists because two callers used to want two different things out of the
// same tree and each walked it separately: sveltekitutils.CheckNativeModules
// looked for .node addons, and nativeinspect.ClosuredNativeAdapter then walked
// the identical tree again looking for .node/.so ELF binaries to build the
// shared-library closure from. On a realistic 40k-file node_modules that is two
// full traversals where one suffices.
type NativeModuleScan struct {
	// Native is the native-module verdict — identical to what
	// CheckNativeModules returns, because CheckNativeModules now returns
	// exactly this field.
	Native NativeCheckResult

	// ELFCandidates holds absolute paths of files worth opening as ELF
	// binaries (.node, .so, and versioned .so.N), in traversal order.
	//
	// Classification is deliberately kept as two independent predicates rather
	// than one merged rule: the .node addon check is a case-SENSITIVE suffix
	// test and the ELF candidate check is a case-INSENSITIVE extension test.
	// They disagree on names like "addon.NODE", and folding them into a single
	// predicate would have silently changed a build verdict while looking like
	// pure deduplication.
	ELFCandidates []string
}

// CheckNativeModules inspects a SvelteKit project directory for native Node package dependencies
// and binary .node files inside node_modules.
//
// It is side-effect free and read-only. It is a convenience wrapper around
// ScanNativeModules for callers that have no context and no use for the ELF
// candidate list.
func CheckNativeModules(projectDir string, pkg PackageJSON) NativeCheckResult {
	scan, _ := ScanNativeModules(context.Background(), projectDir, pkg)
	return scan.Native
}

// ScanNativeModules inspects projectDir's package.json dependency maps and then
// traverses node_modules exactly once, classifying every entry for both the
// native-module verdict and the ELF-binary closure in the same pass.
//
// The traversal uses filepath.WalkDir rather than filepath.Walk: Walk calls
// os.Lstat on every entry it visits, while WalkDir reuses the fs.DirEntry the
// directory read already produced. Neither predicate below needs anything from
// a FileInfo, so on a large node_modules that is one avoidable syscall per file
// per walk, eliminated.
//
// ctx is checked inside the walk callback, so a cancelled build stops
// traversing instead of running to completion. A cancellation is returned as an
// error and the returned scan is partial — callers must not treat a partial
// scan as a verdict.
//
// It is side-effect free and read-only.
func ScanNativeModules(ctx context.Context, projectDir string, pkg PackageJSON) (NativeModuleScan, error) {
	var scan NativeModuleScan
	seenModules := make(map[string]bool)

	addMatch := func(modName, reason string) {
		if !seenModules[modName] {
			seenModules[modName] = true
			scan.Native.HasNativeModules = true
			scan.Native.DetectedModules = append(scan.Native.DetectedModules, modName)
			scan.Native.Reasons = append(scan.Native.Reasons, fmt.Sprintf("%s (%s)", modName, reason))
		}
	}

	// 1. Inspect package.json dependencies and devDependencies.
	//
	// Names are sorted before iteration: ranging a map directly made
	// DetectedModules/Reasons ordering random across runs for any project with
	// more than one native dependency, so the strict adapter's failure message
	// listed the same findings in a different order on every invocation.
	checkDepMap := func(deps map[string]string, section string) {
		names := make([]string, 0, len(deps))
		for name := range deps {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			if desc, ok := knownNativePackages[name]; ok {
				addMatch(name, fmt.Sprintf("declared in %s: %s", section, desc))
			} else if desc, ok := nativeBuildTools[name]; ok {
				addMatch(name, fmt.Sprintf("native build tool in %s: %s", section, desc))
			} else if strings.HasPrefix(name, "@napi-rs/") && !strings.Contains(name, "cli") {
				addMatch(name, fmt.Sprintf("N-API Rust binding in %s", section))
			}
		}
	}

	checkDepMap(pkg.Dependencies, "dependencies")
	checkDepMap(pkg.DevDependencies, "devDependencies")

	// Also check optionalDependencies if present in package.json
	data, err := os.ReadFile(filepath.Join(projectDir, "package.json"))
	if err == nil {
		var rawPkg struct {
			OptionalDependencies map[string]string `json:"optionalDependencies"`
		}
		if json.Unmarshal(data, &rawPkg) == nil && len(rawPkg.OptionalDependencies) > 0 {
			checkDepMap(rawPkg.OptionalDependencies, "optionalDependencies")
		}
	}

	if err := ctx.Err(); err != nil {
		return scan, err
	}

	// 2. Traverse node_modules once, classifying each entry for both consumers.
	nodeModulesDir := filepath.Join(projectDir, "node_modules")
	info, err := os.Stat(nodeModulesDir)
	if err != nil || !info.IsDir() {
		return scan, nil
	}

	walkErr := filepath.WalkDir(nodeModulesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			return nil
		}

		name := d.Name()

		// Predicate 1 — native .node addon (case-sensitive suffix, as before).
		if strings.HasSuffix(name, ".node") {
			relPath, relErr := filepath.Rel(projectDir, path)
			if relErr != nil {
				relPath = path
			}
			addMatch(relPath, "binary .node C++ addon found in node_modules")
		}

		// Predicate 2 — ELF candidate (case-insensitive extension, as before).
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".node" || ext == ".so" || strings.Contains(name, ".so.") {
			scan.ELFCandidates = append(scan.ELFCandidates, path)
		}

		return nil
	})
	if walkErr != nil {
		return scan, walkErr
	}

	return scan, nil
}
