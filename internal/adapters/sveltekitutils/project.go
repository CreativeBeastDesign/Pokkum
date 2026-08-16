// Package sveltekitutils holds small, dependency-free helpers for detecting how a
// SvelteKit project is wired, so that internal/adapters/bunexec does not have
// to embed a JavaScript parser to answer questions like "is
// @jesterkit/exe-sveltekit configured as the adapter" or "what version of
// @sveltejs/kit is installed".
package sveltekitutils

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// IsUtilityPackage marks this as a reusable utility, not a port adapter.
const IsUtilityPackage = true

// PackageJSON is the subset of a package.json this package cares about.
type PackageJSON struct {
	Name            string            `json:"name"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// ReadPackageJSON reads and parses <dir>/package.json. The returned error is
// exactly the os.ReadFile or json.Unmarshal error, unwrapped; callers that
// need to classify "missing" vs "malformed" should use errors.Is(err,
// os.ErrNotExist) themselves rather than have this package impose a
// classification that belongs to the caller's error-wrapping policy.
func ReadPackageJSON(dir string) (PackageJSON, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return PackageJSON{}, err
	}
	var pkg PackageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return PackageJSON{}, err
	}
	return pkg, nil
}

// HasDependency reports whether name appears in either the dependencies or
// devDependencies map, regardless of the version range recorded there.
func (p PackageJSON) HasDependency(name string) bool {
	if _, ok := p.Dependencies[name]; ok {
		return true
	}
	_, ok := p.DevDependencies[name]
	return ok
}

// DeclaredVersion returns the version range package.json declares for name
// ("" if the package is not a dependency at all). This is a semver *range*
// ("^1.2.0"), not a resolved version — prefer ResolveVersion, which falls
// back to this only when node_modules has not been installed.
func (p PackageJSON) DeclaredVersion(name string) (string, bool) {
	if v, ok := p.Dependencies[name]; ok {
		return v, true
	}
	if v, ok := p.DevDependencies[name]; ok {
		return v, true
	}
	return "", false
}

// AdapterConfigured reports whether svelte.config.js source text references
// pkgName, e.g. via `import adapter from "@jesterkit/exe-sveltekit"` or
// `require("@jesterkit/exe-sveltekit")`. It is a plain substring search: good
// enough to catch every normal import form, and deliberately not a JS parse.
func AdapterConfigured(svelteConfigSource, pkgName string) bool {
	return strings.Contains(svelteConfigSource, pkgName)
}

// targetLinuxX64Pattern matches an adapter options object containing
// `target: "linux-x64"` (single or double quoted, any amount of whitespace
// around the colon).
var targetLinuxX64Pattern = regexp.MustCompile(`target\s*:\s*['"]linux-x64['"]`)

// TargetsLinuxX64 reports whether svelte.config.js source text appears to set
// the adapter's `target` option to "linux-x64".
//
// Why this matters: @jesterkit/exe-sveltekit's adapt() shells out to its own
// `bun build --compile` as an unavoidable part of running `bun run build` —
// there is no flag to skip it. That internal pass compiles for whatever
// `target` the adapter config specifies, defaulting to the host architecture
// when unset. Pokkum ignores that internal artifact entirely (it compiles the
// real artifacts itself, per-platform, against the generated
// temp-server/index.ts in stage two), so a wrong or missing target here does
// not break anything Pokkum depends on — it only wastes the time and disk
// space of an internal build whose output is never used. Setting
// target: "linux-x64" makes that wasted pass at least produce a usable
// linux/amd64 binary instead of a macOS or Windows one nobody asked for.
//
// This is therefore a soft, informational check: callers should log a
// recommendation when it returns false, never fail a build over it.
func TargetsLinuxX64(svelteConfigSource string) bool {
	return targetLinuxX64Pattern.MatchString(svelteConfigSource)
}

// stripJSComments removes `//` line comments and `/* */` block comments from
// JS/TS source text before it is handed to the plain-regex detectors in this
// file (StaticFallbackFilename in particular), so a commented-out mention of
// a config key — e.g. "// fallback: '200.html'" — is never mistaken for live
// config, and a real, live config is never silently defeated by an unrelated
// comment mentioning the same key (e.g. "// set fallback: false to disable").
//
// It is string-literal-aware: a `//` or `/*` sequence that appears inside a
// '...', "..." or `...` string literal does NOT start a comment. This is
// deliberately not a JS parser — it only tracks one bit of state ("am I
// currently inside a string literal, and if so which quote closes it")
// alongside the plain char-by-char scan, which is enough to stop the
// stripper from misreading a slash inside a string (e.g. a URL like
// "https://example.com/fallback") as a comment opener and corrupting
// everything after it on the line. String contents are passed through
// completely unchanged (not blanked) — the existing regexes still need to
// see literal quote characters and their contents to capture a real
// `fallback: '200.html'` value; only actual comment text is removed.
func stripJSComments(source string) string {
	var out strings.Builder
	out.Grow(len(source))
	runes := []rune(source)
	n := len(runes)
	for i := 0; i < n; {
		c := runes[i]
		switch {
		case c == '/' && i+1 < n && runes[i+1] == '/':
			// Line comment: drop everything up to (not including) the next
			// newline, so surrounding line structure is preserved.
			i += 2
			for i < n && runes[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && runes[i+1] == '*':
			// Block comment: drop up to and including the closing "*/". An
			// unterminated block comment (malformed source) drops the rest
			// of the file, matching how a real JS parser would also fail to
			// recover from it.
			i += 2
			for i < n && !(runes[i] == '*' && i+1 < n && runes[i+1] == '/') {
				i++
			}
			if i < n {
				i += 2 // skip the closing "*/"
			}
			out.WriteByte(' ') // keep token separation, e.g. "x/*c*/y" -> "x y"
		case c == '\'' || c == '"' || c == '`':
			// String literal: copy verbatim (including its delimiters) until
			// the matching closing quote, honoring backslash escapes so an
			// escaped quote doesn't end the literal early.
			quote := c
			out.WriteRune(c)
			i++
			for i < n && runes[i] != quote {
				if runes[i] == '\\' && i+1 < n {
					out.WriteRune(runes[i])
					i++
				}
				out.WriteRune(runes[i])
				i++
			}
			if i < n {
				out.WriteRune(runes[i]) // closing quote
				i++
			}
		default:
			out.WriteRune(c)
			i++
		}
	}
	return out.String()
}

// staticFallbackStringPattern matches an adapter-static `fallback:` option
// whose value is a quoted filename, e.g.
//
//	adapter: adapter({ fallback: '200.html' })
//
// supporting single, double or backtick quotes. The captured group is the
// filename (any run of non-quote characters, so filenames — which never
// contain quotes — are matched verbatim).
var staticFallbackStringPattern = regexp.MustCompile(`fallback\s*:\s*['"` + "`" + `]([^'"` + "`" + `]+)['"` + "`" + `]`)

// staticFallbackTruePattern matches an adapter-static `fallback: true` option,
// which adapter-static resolves to its own documented default filename
// "200.html".
var staticFallbackTruePattern = regexp.MustCompile(`fallback\s*:\s*true`)

// staticFallbackFalsePattern matches an explicit `fallback: false`, which
// disables SPA fallback regardless of any other match.
var staticFallbackFalsePattern = regexp.MustCompile(`fallback\s*:\s*false`)

// StaticFallbackFilename extracts the SPA-fallback filename @sveltejs/
// adapter-static will emit for this project, from svelte.config.js source text.
//
// It mirrors TargetsLinuxX64's plain-regex approach (this package deliberately
// contains no JS parser). The return contract:
//
//   - (name, true): an SPA fallback page is configured. name is the filename
//     adapter-static emits at the client output root — the configured string
//     verbatim, or "200.html" for `fallback: true` (adapter-static's own
//     documented default, not a Pokkum guess).
//   - ("", false): no fallback is configured (option absent, or an explicit
//     `fallback: false`).
//
// This is config-driven detection only; the caller must still verify the named
// file was actually emitted in the staged output (see bunexec.Prepare) so the
// packager's source of truth is what adapter-static really wrote, never a
// hardcoded magic filename.
func StaticFallbackFilename(svelteConfigSource string) (string, bool) {
	// Match against comment-stripped source: a raw regex over the unparsed
	// file text would treat a commented-out mention of `fallback:` as live
	// config (hard-failing builds that never actually configured a fallback)
	// or let a commented `fallback: false` silently defeat a real, live
	// `fallback: '...'` elsewhere in the same file. See stripJSComments.
	source := stripJSComments(svelteConfigSource)

	// An explicit false defeats every positive match below.
	if staticFallbackFalsePattern.MatchString(source) {
		return "", false
	}
	if m := staticFallbackStringPattern.FindStringSubmatch(source); m != nil {
		return m[1], true
	}
	if staticFallbackTruePattern.MatchString(source) {
		// adapter-static maps `fallback: true` to its default "200.html".
		return "200.html", true
	}
	return "", false
}

// ResolveVersion returns the best available version string for an npm
// package: the resolved version recorded in
// <projectDir>/node_modules/<pkgName>/package.json if node_modules has been
// installed, falling back to the semver range package.json declares, and
// finally "" if the package is not referenced at all. It never returns an
// error, matching the "informational only" contract on
// ports.PreflightResult.AdapterVersion / SvelteKitVersion.
func ResolveVersion(projectDir, pkgName string, pkg PackageJSON) string {
	data, err := os.ReadFile(filepath.Join(projectDir, "node_modules", pkgName, "package.json"))
	if err == nil {
		var installed struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &installed) == nil && installed.Version != "" {
			return installed.Version
		}
	}
	if v, ok := pkg.DeclaredVersion(pkgName); ok {
		return v
	}
	return ""
}

// IsVersionAtLeast checks if a semver string (or range like "^2.31.0") is at least minMajor.minMinor.
func IsVersionAtLeast(versionStr string, minMajor, minMinor int) bool {
	clean := strings.TrimLeft(versionStr, "^~>=<v ")
	parts := strings.Split(clean, ".")
	if len(parts) == 0 {
		return false
	}
	var major, minor int
	_, _ = fmt.Sscanf(parts[0], "%d", &major)
	if len(parts) > 1 {
		_, _ = fmt.Sscanf(parts[1], "%d", &minor)
	}
	if major > minMajor {
		return true
	}
	if major == minMajor && minor >= minMinor {
		return true
	}
	return false
}

// CheckTelemetrySupported inspects the project's @sveltejs/kit version and reports
// whether it is >= 2.31.0 (the minimum version for native OpenTelemetry support).
func CheckTelemetrySupported(projectDir string) (bool, string, error) {
	pkg, err := ReadPackageJSON(projectDir)
	if err != nil {
		return false, "", err
	}
	ver := ResolveVersion(projectDir, "@sveltejs/kit", pkg)
	if ver == "" {
		return false, "", nil
	}
	return IsVersionAtLeast(ver, 2, 31), ver, nil
}
