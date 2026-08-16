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
	Scripts         map[string]string `json:"scripts"`
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

// isJSIdentByte reports whether b can appear in a JavaScript identifier, so a
// call-site scan can tell `sveltekit(` apart from `notSveltekit(`.
func isJSIdentByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_', b == '$':
		return true
	}
	return false
}

// scanCallArgs returns the source text between the parenthesis at
// source[open] and its matching close parenthesis, reporting false when the
// call is unbalanced (truncated or malformed source).
//
// It skips over '...', "..." and `...` string literals wholesale — honoring
// backslash escapes — so a parenthesis inside a string (a regex-ish route
// pattern, a URL) never unbalances the count. Indexing is by byte, which is
// safe because every byte of a multi-byte UTF-8 rune is >= 0x80 and can
// therefore never collide with the ASCII delimiters scanned for here.
func scanCallArgs(source string, open int) (string, bool) {
	depth := 0
	for i := open; i < len(source); i++ {
		switch c := source[i]; c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return source[open+1 : i], true
			}
		case '\'', '"', '`':
			j := i + 1
			for j < len(source) && source[j] != c {
				if source[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(source) {
				return "", false
			}
			i = j
		}
	}
	return "", false
}

// sveltekitPluginOptions returns the argument text of every `sveltekit(...)`
// plugin call in comment-stripped Vite config source, in source order. A bare
// `sveltekit()` contributes an empty string; a call whose parentheses never
// close contributes nothing at all.
func sveltekitPluginOptions(source string) []string {
	const call = "sveltekit("
	var out []string
	for i := 0; i < len(source); {
		rel := strings.Index(source[i:], call)
		if rel < 0 {
			break
		}
		start := i + rel
		i = start + len(call)
		if start > 0 && isJSIdentByte(source[start-1]) {
			// A suffix of a longer identifier, e.g. `mySveltekit(` — not the
			// @sveltejs/kit/vite plugin.
			continue
		}
		if args, ok := scanCallArgs(source, start+len(call)-1); ok {
			out = append(out, args)
		}
	}
	return out
}

// ViteConfigOverridesSvelteConfig reports whether a project's vite.config.{ts,js}
// passes an options object to its `sveltekit()` plugin call — the condition
// under which SvelteKit stops reading svelte.config.js altogether and takes its
// configuration (the adapter included) from the Vite config instead.
//
// This is not a heuristic about what "should" happen: SvelteKit itself prints
//
//	svelte.config.js is ignored when options are passed via your Vite config
//
// at both `svelte-kit sync` (which the scaffold's `prepare` script runs during
// `bun install`) and `vite build` time, and a real build confirms the Vite
// config wins — a project whose vite.config.ts passes `adapter: adapter()` from
// @sveltejs/adapter-node builds with adapter-node even when svelte.config.js
// names a different adapter entirely.
//
// Current `sv create` scaffolds emit no svelte.config.js at all and configure
// the adapter exclusively this way, so this is the common shape, not an exotic
// one. Any non-empty options object counts (SvelteKit's own message says
// "options", not "an adapter option"); a bare `sveltekit()` — what
// testdata/fixtures/sveltekit-basic has — does not, and leaves svelte.config.js
// governing as before.
//
// Like the rest of this file it is a plain textual scan, deliberately not a JS
// parse: comments are stripped first (see stripJSComments) so a commented-out
// options object is not mistaken for a live one.
func ViteConfigOverridesSvelteConfig(viteConfigSource string) bool {
	for _, args := range sveltekitPluginOptions(stripJSComments(viteConfigSource)) {
		if strings.TrimSpace(args) != "" {
			return true
		}
	}
	return false
}

// EffectiveAdapterConfigured answers the only question worth asking before a
// `bun run build`: will SvelteKit see pkgName configured as this project's
// adapter, in the file it will actually read?
//
// viteConfigName is the filename of the project's Vite config
// ("vite.config.ts", "vite.config.js", ...) or "" when it has none;
// viteConfigSource is that file's text. svelteConfigSource is
// svelte.config.js's text, or "" when that file is absent (current `sv create`
// scaffolds ship none).
//
// The returned values are:
//
//   - configured: pkgName is referenced by whichever file governs.
//   - readFrom: the name of the file that governs — the one a caller's error
//     message must tell the user to edit. Editing the other one has no effect.
//   - overridden: the Vite config governs and svelte.config.js is dead text,
//     whether it exists or not (see ViteConfigOverridesSvelteConfig).
//
// "Referenced" is the same plain substring test AdapterConfigured uses, applied
// to comment-stripped source so a commented-out import never counts. It cannot
// see through an adapter re-exported from a local module
// (`import adapter from './shared-adapter.js'`); callers should say so in their
// error message rather than silently guessing.
func EffectiveAdapterConfigured(svelteConfigSource, viteConfigSource, viteConfigName, pkgName string) (configured bool, readFrom string, overridden bool) {
	if viteConfigName != "" && ViteConfigOverridesSvelteConfig(viteConfigSource) {
		return AdapterConfigured(stripJSComments(viteConfigSource), pkgName), viteConfigName, true
	}
	return AdapterConfigured(stripJSComments(svelteConfigSource), pkgName), "svelte.config.js", false
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
