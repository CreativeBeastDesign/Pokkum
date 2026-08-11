package sveltekitutils

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// CheckNativeModules inspects a SvelteKit project directory for native Node package dependencies
// and binary .node files inside node_modules.
//
// It is side-effect free and read-only.
func CheckNativeModules(projectDir string, pkg PackageJSON) NativeCheckResult {
	var result NativeCheckResult
	seenModules := make(map[string]bool)

	addMatch := func(modName, reason string) {
		if !seenModules[modName] {
			seenModules[modName] = true
			result.HasNativeModules = true
			result.DetectedModules = append(result.DetectedModules, modName)
			result.Reasons = append(result.Reasons, fmt.Sprintf("%s (%s)", modName, reason))
		}
	}

	// 1. Inspect package.json dependencies and devDependencies
	checkDepMap := func(deps map[string]string, section string) {
		for name := range deps {
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

	// 2. Scan node_modules directory for any .node binary files if node_modules exists
	nodeModulesDir := filepath.Join(projectDir, "node_modules")
	if info, err := os.Stat(nodeModulesDir); err == nil && info.IsDir() {
		_ = filepath.Walk(nodeModulesDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if !info.IsDir() && strings.HasSuffix(info.Name(), ".node") {
				relPath, err := filepath.Rel(projectDir, path)
				if err != nil {
					relPath = path
				}
				addMatch(relPath, "binary .node C++ addon found in node_modules")
			}
			return nil
		})
	}

	return result
}
