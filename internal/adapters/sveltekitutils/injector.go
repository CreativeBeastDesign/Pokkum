package sveltekitutils

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// InjectorOptions configures the behavior of the config transformer.
type InjectorOptions struct {
	// SourceEpoch is the SOURCE_DATE_EPOCH value to use for version pinning.
	// If empty, defaults to "pokkum-reproducible-build".
	SourceEpoch string

	// TargetAdapter is the npm package name of the adapter to inject.
	// If empty, defaults to "@jesterkit/exe-sveltekit".
	TargetAdapter string

	// DefaultBinaryName is the output binary name to configure in the adapter.
	// Defaults to "server".
	DefaultBinaryName string

	// EnableTelemetry enables kit.experimental.tracing & instrumentation in svelte.config.js.
	EnableTelemetry bool

	// UserSvelteConfigFile is the project's own svelte config filename
	// ("svelte.config.js", "svelte.config.ts") or "" when it has none.
	//
	// It exists because passing ANY options to sveltekit() makes SvelteKit skip
	// svelte.config.js entirely — it only calls load_svelte_config() when the
	// argument is undefined (verified in @sveltejs/kit's
	// src/exports/vite/index.js). So rewriting a bare sveltekit() into
	// sveltekit({ adapter: adapter() }) to inject an adapter silently discarded
	// the project's whole SvelteKit configuration: aliases, csp, prerender
	// settings, and kit.experimental flags such as remoteFunctions — which fails
	// the build outright with "Could not load virtual:env/dynamic/private".
	//
	// When set, the injected config imports that file and merges it, so only the
	// adapter is replaced.
	UserSvelteConfigFile string

	// RoutesDir, when set, overrides kit.files.routes — pointing SvelteKit at a
	// filtered mirror of src/routes so excluded routes never become bundle
	// entry points. Interpreted relative to the build's working directory,
	// which is how SvelteKit resolves it (it calls path.resolve against
	// process.cwd(), not against the config file's location).
	RoutesDir string
}

// VirtualConfigResult holds the result of preparing a virtual svelte.config.js.
type VirtualConfigResult struct {
	// TransformedSource is the modified configuration code.
	TransformedSource string

	// VirtualConfigPath is the path to the written temporary config file.
	VirtualConfigPath string

	// InjectedAdapter indicates whether adapter injection took place.
	InjectedAdapter bool

	// PinnedVersion indicates whether version pinning was injected.
	PinnedVersion bool

	// RoutesRedirected reports whether kit.files.routes was pointed at a
	// filtered mirror. False means excluded routes are still bundle entry
	// points and the caller must say so rather than assume they were removed.
	RoutesRedirected bool

	// InjectedTelemetry indicates whether telemetry experimental flags were injected.
	InjectedTelemetry bool
}

// DefaultInjectorOptions returns sensible defaults for injection.
func DefaultInjectorOptions() InjectorOptions {
	return InjectorOptions{
		SourceEpoch:       "pokkum-reproducible-build",
		TargetAdapter:     "@jesterkit/exe-sveltekit",
		DefaultBinaryName: "server",
		EnableTelemetry:   false,
	}
}

// TransformConfig transforms a svelte.config.js source string in a single pass to inject the
// target adapter, pin kit.version.name, and optionally inject kit.experimental telemetry flags.
func TransformConfig(source string, opts InjectorOptions) (string, error) {
	if opts.TargetAdapter == "" {
		opts.TargetAdapter = "@jesterkit/exe-sveltekit"
	}
	if opts.DefaultBinaryName == "" {
		opts.DefaultBinaryName = "server"
	}

	result := source

	// 1. Check if target adapter is already configured
	if !AdapterConfigured(result, opts.TargetAdapter) {
		result = replaceAdapterImport(result, opts.TargetAdapter)
	}

	// 2. Ensure kit.version.name is pinned if not already referencing SOURCE_DATE_EPOCH
	if !strings.Contains(result, "SOURCE_DATE_EPOCH") {
		result = injectVersionPin(result)
	}

	// 3. Ensure kit.experimental tracing & instrumentation are configured if telemetry is enabled
	if opts.EnableTelemetry && !strings.Contains(result, "tracing") {
		result = injectExperimentalFlags(result)
	}

	return result, nil
}

// PrepareVirtualConfig inspects projectDir's svelte.config.js, applies injection
// if needed, and writes the virtual config file to <projectDir>/.pokkum/svelte.config.js.
func PrepareVirtualConfig(projectDir string, opts InjectorOptions) (*VirtualConfigResult, error) {
	configPath := filepath.Join(projectDir, "svelte.config.js")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read svelte.config.js: %w", err)
	}

	source := string(data)
	injectedAdapter := !AdapterConfigured(source, opts.TargetAdapter)
	pinnedVersion := !strings.Contains(source, "SOURCE_DATE_EPOCH")
	injectedTelemetry := opts.EnableTelemetry && !strings.Contains(source, "tracing")

	transformed, err := TransformConfig(source, opts)
	if err != nil {
		return nil, fmt.Errorf("transform svelte.config.js: %w", err)
	}

	pokkumDir := filepath.Join(projectDir, ".pokkum")
	if err := os.MkdirAll(pokkumDir, 0o700); err != nil {
		return nil, fmt.Errorf("create .pokkum directory: %w", err)
	}

	virtualPath := filepath.Join(pokkumDir, "svelte.config.js")
	if err := os.WriteFile(virtualPath, []byte(transformed), 0o600); err != nil {
		return nil, fmt.Errorf("write virtual svelte.config.js: %w", err)
	}

	return &VirtualConfigResult{
		TransformedSource: transformed,
		VirtualConfigPath: virtualPath,
		InjectedAdapter:   injectedAdapter,
		RoutesRedirected:  opts.RoutesDir != "" && strings.Contains(transformed, "__pokkumWithRoutes"),
		PinnedVersion:     pinnedVersion,
		InjectedTelemetry: injectedTelemetry,
	}, nil
}

// BuildEnv constructs an environment variable slice containing SOURCE_DATE_EPOCH
// and POKKUM_AUTO_INJECT flags for subprocess execution.
func BuildEnv(baseEnv []string, sourceEpoch string) []string {
	if sourceEpoch == "" {
		sourceEpoch = "pokkum-reproducible-build"
	}

	envMap := make(map[string]string)
	for _, entry := range baseEnv {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	envMap["SOURCE_DATE_EPOCH"] = sourceEpoch
	envMap["POKKUM_AUTO_INJECT"] = "1"

	out := make([]string, 0, len(envMap))
	for k, v := range envMap {
		out = append(out, fmt.Sprintf("%s=%s", k, v))
	}
	return out
}

var (
	adapterImportRegex     = regexp.MustCompile(`import\s+([a-zA-Z0-9_$]+)\s+from\s+['"]@sveltejs/adapter-[a-z-]+['"];?`)
	requireRegex           = regexp.MustCompile(`const\s+([a-zA-Z0-9_$]+)\s*=\s*require\(['"]@sveltejs/adapter-[a-z-]+['"]\);?`)
	viteAdapterImportRegex = regexp.MustCompile(`import\s+([a-zA-Z0-9_$]+)\s+from\s+['"](@sveltejs/adapter-[a-z-]+|@jesterkit/exe-sveltekit)['"];?`)
	viteRequireRegex       = regexp.MustCompile(`const\s+([a-zA-Z0-9_$]+)\s*=\s*require\(['"](@sveltejs/adapter-[a-z-]+|@jesterkit/exe-sveltekit)['"]\);?`)

	// identUsedAsAdapterRegex finds the identifier invoked as the adapter
	// factory in a svelte.config.js kit block, e.g. "adapter: adapterBun()"
	// captures "adapterBun". This is the real signal for which import
	// statement configures the project's *current* adapter: the "adapter"
	// property key is fixed by SvelteKit's config shape, but the value
	// bound to it can be any local identifier, imported under any local
	// name, from any package -- not just @sveltejs/adapter-*.
	identUsedAsAdapterRegex = regexp.MustCompile(`\badapter\s*:\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)
)

// replaceAdapterImport replaces the import that currently supplies the
// project's SvelteKit adapter with an import of targetAdapter, or prepends a
// new import if none exists.
//
// It first traces which *identifier* the config actually invokes as the
// adapter factory (via identUsedAsAdapterRegex) and looks for the specific
// import/require statement that binds that identifier, regardless of which
// package it comes from or what local name it uses. This is what makes a
// third-party adapter package (e.g. "svelte-adapter-bun", which does not
// match "@sveltejs/adapter-*") get replaced instead of silently ignored --
// ignoring it left the old import in place and prepended a second,
// colliding "import adapter from ...", which is a duplicate-binding
// SyntaxError at parse time, not just visual noise.
//
// The narrower @sveltejs/adapter-* patterns are kept as a fallback for a
// config that doesn't wire kit.adapter to the imported binding yet (so
// there is no "adapter: X(" to trace), and prepending remains the last
// resort for a config with no existing adapter import at all.
func replaceAdapterImport(source, targetAdapter string) string {
	newImport := fmt.Sprintf("import adapter from '%s';", targetAdapter)

	if m := identUsedAsAdapterRegex.FindStringSubmatch(source); m != nil {
		local := m[1]
		if replaced, ok := replaceImportBinding(source, local, targetAdapter); ok {
			return replaced
		}
		if identAlreadyBound(source, local) {
			// local is imported some way this function deliberately doesn't
			// try to isolate and rewrite (e.g. one name among several in a
			// multi-specifier named import, "{ adapter, other } from
			// 'pkg'"). Leave the source untouched rather than prepend a
			// second declaration of the same name -- that would trade one
			// duplicate-binding bug for another. checkEffectiveAdapter's
			// fail-fast validation catches the unconfigured result
			// downstream instead of this function guessing.
			return source
		}
	}

	if adapterImportRegex.MatchString(source) {
		return adapterImportRegex.ReplaceAllString(source, newImport)
	}
	if requireRegex.MatchString(source) {
		return requireRegex.ReplaceAllString(source, newImport)
	}

	// If no standard adapter import was found, prepend the import at top of file
	return fmt.Sprintf("%s\n%s", newImport, source)
}

// replaceImportBinding finds the single import/require statement in source
// that binds identifier local, and replaces just that statement with an
// import (or require, for the require-form match) of targetAdapter bound to
// "adapter" -- the local name every other transform in this file assumes
// the config's adapter factory is called through. It recognizes every shape
// a real adapter import realistically takes:
//
//   - a default import:              import local from 'pkg';
//   - a default re-exported by name: import { default as local } from 'pkg';
//   - a CommonJS require:            const local = require('pkg');
//   - a solo named import:           import { local } from 'pkg';
//   - a solo aliased named import:   import { X as local } from 'pkg';
//
// A named import sharing braces with other, unrelated specifiers
// (import { local, other } from 'pkg') is deliberately left unmatched --
// splicing one specifier out of a multi-name import without disturbing the
// others needs real parsing, and the risk of mangling an unrelated import
// outweighs handling that rare shape here.
func replaceImportBinding(source, local, targetAdapter string) (string, bool) {
	ident := regexp.QuoteMeta(local)
	newImport := fmt.Sprintf("import adapter from '%s';", targetAdapter)
	newRequire := fmt.Sprintf("const adapter = require('%s');", targetAdapter)

	patterns := []struct {
		re          *regexp.Regexp
		replacement string
	}{
		{regexp.MustCompile(`import\s+` + ident + `\s+from\s+['"][^'"]+['"];?`), newImport},
		{regexp.MustCompile(`import\s*\{\s*default\s+as\s+` + ident + `\s*\}\s+from\s+['"][^'"]+['"];?`), newImport},
		{regexp.MustCompile(`const\s+` + ident + `\s*=\s*require\(['"][^'"]+['"]\)\s*;?`), newRequire},
		{regexp.MustCompile(`import\s*\{\s*` + ident + `\s*\}\s+from\s+['"][^'"]+['"];?`), newImport},
		{regexp.MustCompile(`import\s*\{\s*[A-Za-z_$][A-Za-z0-9_$]*\s+as\s+` + ident + `\s*\}\s+from\s+['"][^'"]+['"];?`), newImport},
	}
	for _, p := range patterns {
		if p.re.MatchString(source) {
			return p.re.ReplaceAllString(source, p.replacement), true
		}
	}
	return source, false
}

// identAlreadyBound is a broad, best-effort check for whether local is
// bound by *any* import or require statement in source, including shapes
// (like a multi-specifier named import) that replaceImportBinding
// deliberately does not rewrite. It exists only to decide whether
// prepending a fresh "import adapter from ...;" is safe: if local is
// already bound some way this file can't isolate, prepending a second
// declaration of the same name would reintroduce the exact
// duplicate-binding bug replaceAdapterImport exists to fix.
func identAlreadyBound(source, local string) bool {
	ident := regexp.QuoteMeta(local)
	broad := regexp.MustCompile(`\bimport\b[^;]*\b` + ident + `\b[^;]*;|\bconst\s+` + ident + `\s*=\s*require\(`)
	return broad.MatchString(source)
}

func replaceViteAdapterImport(source, targetAdapter string) string {
	// Trace the identifier actually invoked as `adapter: X()` first, exactly as
	// replaceAdapterImport does for svelte.config.js.
	//
	// Matching only the package NAME (@sveltejs/adapter-*, @jesterkit/...) is
	// the bug that made `adopt --write-config` emit two `adapter` bindings for
	// a project using a third-party adapter such as svelte-adapter-bun:
	// unmatched, so a second import was prepended beside the existing one, and
	// the config then died with "Identifier 'adapter' has already been
	// declared". That was fixed for the svelte.config.js path; this Vite path
	// carried the identical package-pattern-only matching and the identical
	// failure, on the ALWAYS-ON virtual injection path rather than only under
	// an opt-in flag. The signal is the binding the config actually uses, not
	// the package it happens to come from.
	if m := identUsedAsAdapterRegex.FindStringSubmatch(source); m != nil {
		if replaced, ok := replaceImportBinding(source, m[1], targetAdapter); ok {
			return replaced
		}
	}
	if viteAdapterImportRegex.MatchString(source) {
		return viteAdapterImportRegex.ReplaceAllString(source, fmt.Sprintf("import adapter from '%s';", targetAdapter))
	}
	if viteRequireRegex.MatchString(source) {
		return viteRequireRegex.ReplaceAllString(source, fmt.Sprintf("const adapter = require('%s');", targetAdapter))
	}

	return fmt.Sprintf("import adapter from '%s';\n%s", targetAdapter, source)
}

// HasLiveSvelteKitCall reports whether source contains a live, uncommented
// sveltekit( plugin call. Exported so the compiler can decide whether a
// project's Vite config is one Pokkum can meaningfully wrap.
func HasLiveSvelteKitCall(source string) bool {
	return findLiveSvelteKitCall(source) >= 0
}

// findLiveSvelteKitCall finds the byte index of a live, uncommented sveltekit( call in JS/TS source.
func findLiveSvelteKitCall(source string) int {
	const call = "sveltekit("
	n := len(source)
	for i := 0; i < n; {
		c := source[i]
		switch {
		case c == '/' && i+1 < n && source[i+1] == '/':
			i += 2
			for i < n && source[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && source[i+1] == '*':
			i += 2
			for i < n && !(source[i] == '*' && i+1 < n && source[i+1] == '/') {
				i++
			}
			if i < n {
				i += 2
			}
		case c == '\'' || c == '"' || c == '`':
			quote := c
			i++
			for i < n && source[i] != quote {
				if source[i] == '\\' && i+1 < n {
					i++
				}
				i++
			}
			if i < n {
				i++
			}
		case strings.HasPrefix(source[i:], call):
			if i == 0 || !isJSIdentByte(source[i-1]) {
				return i
			}
			i += len(call)
		default:
			i++
		}
	}
	return -1
}

// findLiveAdapterProp finds the byte range [start, end] of a live "adapter: <value>" property in args.
func findLiveAdapterProp(args string) (int, int, bool) {
	n := len(args)
	for i := 0; i < n; {
		c := args[i]
		switch {
		case c == '/' && i+1 < n && args[i+1] == '/':
			i += 2
			for i < n && args[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && args[i+1] == '*':
			i += 2
			for i < n && !(args[i] == '*' && i+1 < n && args[i+1] == '/') {
				i++
			}
			if i < n {
				i += 2
			}
		case c == '\'' || c == '"' || c == '`':
			quote := c
			i++
			for i < n && args[i] != quote {
				if args[i] == '\\' && i+1 < n {
					i++
				}
				i++
			}
			if i < n {
				i++
			}
		case strings.HasPrefix(args[i:], "adapter"):
			if i > 0 && isJSIdentByte(args[i-1]) {
				i += 7
				continue
			}
			j := i + 7
			for j < n && (args[j] == ' ' || args[j] == '\t' || args[j] == '\n' || args[j] == '\r') {
				j++
			}
			if j < n && args[j] == ':' {
				valStart := j + 1
				// Skip whitespace (including newlines) between the colon and
				// the value itself, e.g. "adapter:\n\tadapter()" — otherwise
				// the break condition below fires on that leading newline
				// before any value token is scanned, producing a zero-length
				// match that corrupts the transform's splice.
				for valStart < n && (args[valStart] == ' ' || args[valStart] == '\t' || args[valStart] == '\n' || args[valStart] == '\r') {
					valStart++
				}
				parenDepth, braceDepth, bracketDepth := 0, 0, 0
				k := valStart
				for k < n {
					kc := args[k]
					if parenDepth == 0 && braceDepth == 0 && bracketDepth == 0 && (kc == ',' || kc == '}' || kc == '\n') {
						break
					}
					switch {
					case kc == '/' && k+1 < n && args[k+1] == '/':
						k += 2
						for k < n && args[k] != '\n' {
							k++
						}
					case kc == '/' && k+1 < n && args[k+1] == '*':
						k += 2
						for k < n && !(args[k] == '*' && k+1 < n && args[k+1] == '/') {
							k++
						}
						if k < n {
							k += 2
						}
					case kc == '\'' || kc == '"' || kc == '`':
						q := kc
						k++
						for k < n && args[k] != q {
							if args[k] == '\\' && k+1 < n {
								k++
							}
							k++
						}
						if k < n {
							k++
						}
					case kc == '(':
						parenDepth++
						k++
					case kc == ')':
						if parenDepth > 0 {
							parenDepth--
						}
						k++
					case kc == '{':
						braceDepth++
						k++
					case kc == '}':
						if braceDepth > 0 {
							braceDepth--
						}
						k++
					case kc == '[':
						bracketDepth++
						k++
					case kc == ']':
						if bracketDepth > 0 {
							bracketDepth--
						}
						k++
					default:
						k++
					}
				}
				return i, k, true
			}
			i += 7
		default:
			i++
		}
	}
	return -1, -1, false
}

// TransformViteConfig transforms a Vite config source string (vite.config.ts/js) in a single pass to
// surgically inject/override the target adapter inside the sveltekit() plugin call while preserving
// all other Vite plugins and sveltekit() options.
func TransformViteConfig(source string, opts InjectorOptions) (string, error) {
	if opts.TargetAdapter == "" {
		opts.TargetAdapter = "@sveltejs/adapter-node"
	}

	result := source

	// 1. Ensure target adapter is imported
	if !AdapterConfigured(result, opts.TargetAdapter) {
		result = replaceViteAdapterImport(result, opts.TargetAdapter)
	}

	// 2. Locate the live sveltekit(...) plugin call in the source
	const call = "sveltekit("
	idx := findLiveSvelteKitCall(result)
	if idx < 0 {
		// A silent no-op here would report injection as successful while the
		// adapter was never actually changed — the caller (PrepareVirtualViteConfig)
		// has no other way to detect that, and would proceed to build against
		// a config that still doesn't configure the target adapter. Returning
		// an error instead lets the existing fallback to Option C's fail-fast
		// checkEffectiveAdapter error take over.
		return result, fmt.Errorf("no live sveltekit() plugin call found to inject the adapter into")
	}

	openParen := idx + len(call) - 1
	args, ok := scanCallArgs(result, openParen)
	if !ok {
		return result, fmt.Errorf("sveltekit() plugin call at byte offset %d has unbalanced parentheses", idx)
	}

	closeParen := openParen + 1 + len(args)
	trimmedArgs := strings.TrimSpace(args)

	var newArgs string
	var prelude string
	switch {
	case trimmedArgs == "" && opts.UserSvelteConfigFile != "":
		// A bare sveltekit() means the project keeps its configuration in
		// svelte.config.js, and SvelteKit loads that file only while the plugin
		// receives no arguments. Injecting an adapter necessarily supplies an
		// argument, so the file must be merged in by hand or everything in it is
		// lost.
		//
		// The two shapes differ and the conversion is the point: svelte.config.js
		// nests SvelteKit's own options under `kit`, while the Vite-config form is
		// flat — split_config() destructures extensions/compilerOptions/vitePlugin/
		// preprocess, routes every other recognised key into kit, and passes the
		// rest to vite-plugin-svelte. A literal `kit` key is not one of those
		// recognised options, so spreading the file unflattened would hand `kit`
		// to vite-plugin-svelte as an unknown option and still lose the contents.
		//
		// Hence: spread the non-kit remainder, then the flattened kit options,
		// then the adapter last so it always wins.
		prelude = fmt.Sprintf(
			"import __pokkumUserSvelteConfig from './%s';\n"+
				"const { kit: __pokkumKitOptions = {}, ...__pokkumSvelteRest } = __pokkumUserSvelteConfig ?? {};\n",
			opts.UserSvelteConfigFile)
		newArgs = "{ ...__pokkumSvelteRest, ...__pokkumKitOptions, adapter: adapter() }"
	case trimmedArgs == "":
		// No svelte config to preserve, so there is nothing to merge.
		newArgs = "{ adapter: adapter() }"
	case strings.HasPrefix(trimmedArgs, "{") && strings.HasSuffix(trimmedArgs, "}"):
		// The project already passes options, so it has itself opted out of
		// svelte.config.js and there is nothing to preserve beyond what is here.
		if start, end, found := findLiveAdapterProp(args); found {
			newArgs = args[:start] + "adapter: adapter()" + args[end:]
		} else {
			firstBrace := strings.Index(args, "{")
			newArgs = args[:firstBrace+1] + "\n\t\t\tadapter: adapter()," + args[firstBrace+1:]
		}
	default:
		newArgs = "{ adapter: adapter() }"
	}

	// Pin kit.version.name, exactly as TransformConfig step 2 does for a
	// svelte.config.js project.
	//
	// Without this, SvelteKit falls back to its default version name of
	// Date.now(), which lands in the client bundle as
	// _app/version.json's {"version":"1787339446040"} and cascades into every
	// downstream Vite chunk hash — roughly fifty renamed .js/.gz/.br files and
	// two differing OCI layers between two builds of identical committed
	// source. That makes the image non-reproducible, which is the property
	// this tool exists to provide.
	//
	// This path was missed when vite-config injection was added: the
	// svelte.config.js path pinned the version and this one did not, so any
	// project whose vite.config.ts calls sveltekit() directly — the officially
	// supported pattern, and the one Vitest's `projects:` config requires —
	// silently produced unreproducible builds while the README promised
	// bit-for-bit reproducibility. Found by a real two-build byte diff, not by
	// any test: every fixture used the svelte.config.js path.
	newArgs = injectViteVersionPin(newArgs)

	// Point kit.files.routes at the filtered mirror, if one was staged.
	//
	// Applied as a wrapper rather than an extra key so it works for all three
	// argument shapes above without string-surgery on each: the object is
	// evaluated once (adapter() must not be called twice), any `files` the
	// project already configured is preserved key by key, and `routes` wins
	// because it is spread last.
	if opts.RoutesDir != "" {
		prelude += fmt.Sprintf(
			"const __pokkumRoutesDir = %s;\n"+
				"const __pokkumWithRoutes = (o) => ({ ...o, files: { ...(o?.files ?? {}), routes: __pokkumRoutesDir } });\n",
			jsStringLiteral(filepath.ToSlash(opts.RoutesDir)))
		newArgs = "__pokkumWithRoutes(" + newArgs + ")"
	}

	result = result[:openParen+1] + newArgs + result[closeParen:]
	if prelude != "" {
		// Prepended rather than inserted after the imports: ESM import
		// declarations are hoisted and bound before any module body runs, so the
		// destructuring const may precede them textually and still see the value.
		result = prelude + result
	}
	return result, nil
}

// relativeImportSpecifierRegex matches a relative module specifier ("./..."
// or "../...") immediately following `from`, `import(`, or `require(` — the
// contexts where a bare string literal is actually a module path, not
// arbitrary config data. Used by rewriteRelativeImportSpecifiers to correct
// for the virtual config living one directory level deeper than the file it
// was copied from.
var relativeImportSpecifierRegex = regexp.MustCompile(`(from\s+|import\(\s*|require\(\s*)(['"])(\.\.?/)`)

// rewriteRelativeImportSpecifiers prefixes every relative import/require
// specifier in source with an extra "../", compensating for
// PrepareVirtualViteConfig writing the virtual config to
// <projectDir>/.pokkum/<name> — one directory level below the real
// vite.config.ts a relative specifier like "./local-plugin" or
// "../../shared/vite-config-base" was written relative to. Without this, any
// project whose vite.config.ts imports a relative/workspace-local module
// fails to resolve it once copied into the sandbox.
func rewriteRelativeImportSpecifiers(source string) string {
	out := relativeImportSpecifierRegex.ReplaceAllString(source, "${1}${2}../${3}")
	// Collapse the ".././" this produces for a "./x" specifier into "../x".
	// Purely cosmetic — the two resolve identically — but the generated config is
	// something a user reads when diagnosing a build, and ".././" invites the
	// suspicion that the rewrite is buggy.
	return strings.ReplaceAll(out, ".././", "../")
}

// importMetaURLRegex matches the literal token import.meta.url wherever it
// appears — always used as a read expression, never assigned to, so a
// textual substitution is safe without parsing how the surrounding
// expression (e.g. fileURLToPath(import.meta.url)) consumes it.
var importMetaURLRegex = regexp.MustCompile(`import\.meta\.url`)

// shimDirnameAndImportMetaURL compensates for the virtual config executing
// from <projectDir>/.pokkum/ instead of realConfigPath's real directory (see
// rewriteRelativeImportSpecifiers' doc comment for why the virtual config
// lives one level deeper). Bun injects __dirname/__filename as ambient
// globals even under ESM, so the near-universal
// path.resolve(__dirname, './src/lib') pattern — commonly used to build
// resolve.alias entries — silently resolves one directory too deep once
// relocated, landing on <project>/.pokkum/src/lib instead of
// <project>/src/lib.
//
// Two shims, for two different reasons:
//
//   - __dirname/__filename are shadowed with module-scoped `const`
//     declarations pointing at realConfigPath's real directory/path. A
//     top-level `const` in the same module takes precedence over Bun's
//     ambient global for every reference within that module (JS scoping),
//     so this corrects every direct __dirname/__filename usage without
//     needing to know how it's used.
//   - import.meta.url cannot be shadowed the same way — it is a read-only,
//     module-URL-tied meta property, not a reassignable binding — so every
//     literal occurrence of the token is textually replaced with a
//     precomputed string constant holding realConfigPath's real file://
//     URL, matching rewriteRelativeImportSpecifiers' existing textual
//     substitution approach rather than introducing a JS/TS parser.
//
// realConfigPath must be the original vite.config.ts's real, absolute path
// on disk — not the virtual .pokkum/ copy's path.
func shimDirnameAndImportMetaURL(source, realConfigPath string) string {
	realDir := filepath.Dir(realConfigPath)
	fileURL := "file://" + filepath.ToSlash(realConfigPath)

	source = importMetaURLRegex.ReplaceAllLiteralString(source, strconv.Quote(fileURL))

	preamble := fmt.Sprintf("const __dirname = %s;\nconst __filename = %s;\n", strconv.Quote(realDir), strconv.Quote(realConfigPath))
	return preamble + source
}

// PrepareVirtualViteConfig inspects the project's Vite config, transforms it to configure targetAdapter,
// and writes the virtual config file to <projectDir>/.pokkum/<viteConfigName>.
func PrepareVirtualViteConfig(projectDir, viteConfigName, viteConfigSource string, opts InjectorOptions) (*VirtualConfigResult, error) {
	if viteConfigName == "" {
		viteConfigName = "vite.config.ts"
	}
	if viteConfigSource == "" {
		viteConfigSource = fmt.Sprintf("import adapter from '%s';\nimport { sveltekit } from '@sveltejs/kit/vite';\nimport { defineConfig } from 'vite';\n\nexport default defineConfig({\n\tplugins: [sveltekit({ adapter: adapter() })]\n});\n", opts.TargetAdapter)
	}

	injectedAdapter := !AdapterConfigured(viteConfigSource, opts.TargetAdapter)

	transformed, err := TransformViteConfig(viteConfigSource, opts)
	if err != nil {
		return nil, fmt.Errorf("transform %s: %w", viteConfigName, err)
	}
	transformed = rewriteRelativeImportSpecifiers(transformed)
	transformed = remoteManifestSortPrelude + transformed
	transformed = shimDirnameAndImportMetaURL(transformed, filepath.Join(projectDir, viteConfigName))

	pokkumDir := filepath.Join(projectDir, ".pokkum")
	if err := os.MkdirAll(pokkumDir, 0o700); err != nil {
		return nil, fmt.Errorf("create .pokkum directory: %w", err)
	}

	virtualPath := filepath.Join(pokkumDir, viteConfigName)
	if err := os.WriteFile(virtualPath, []byte(transformed), 0o600); err != nil {
		return nil, fmt.Errorf("write virtual %s: %w", viteConfigName, err)
	}

	return &VirtualConfigResult{
		TransformedSource: transformed,
		VirtualConfigPath: virtualPath,
		InjectedAdapter:   injectedAdapter,
	}, nil
}

// PrepareVirtualViteConfigPassthrough writes a virtual Vite config that is the
// project's own, unchanged except for the determinism prelude (and the
// relative-specifier and __dirname fixups every virtual config needs because
// it lives one directory deeper).
//
// It exists because PrepareVirtualViteConfig only runs when the adapter needs
// injecting. A project that already configures adapter-node correctly — the
// documented happy path — took the `bun run build` branch instead, never got a
// Pokkum-authored config, and therefore never got the remote-manifest sort.
// The reproducibility fix would have reached only projects whose configuration
// was wrong, which is close to the opposite of the intended audience.
//
// The adapter is deliberately NOT touched here: it is already correct, and
// rewriting the sveltekit() call would risk changing semantics (SvelteKit
// ignores svelte.config.js the moment the plugin receives any argument) for no
// benefit.
func PrepareVirtualViteConfigPassthrough(projectDir, viteConfigName, viteConfigSource, routesDir string) (*VirtualConfigResult, error) {
	if viteConfigName == "" {
		viteConfigName = "vite.config.ts"
	}
	if strings.TrimSpace(viteConfigSource) == "" {
		return nil, fmt.Errorf("no vite config source to pass through")
	}

	transformed, pinned := pinViteConfigVersion(rewriteRelativeImportSpecifiers(viteConfigSource), projectDir)
	transformed, routesPointed := pointViteConfigRoutes(transformed, routesDir)
	transformed = remoteManifestSortPrelude + transformed
	transformed = shimDirnameAndImportMetaURL(transformed, filepath.Join(projectDir, viteConfigName))

	pokkumDir := filepath.Join(projectDir, ".pokkum")
	if err := os.MkdirAll(pokkumDir, 0o700); err != nil {
		return nil, fmt.Errorf("create .pokkum directory: %w", err)
	}
	virtualPath := filepath.Join(pokkumDir, viteConfigName)
	if err := os.WriteFile(virtualPath, []byte(transformed), 0o600); err != nil {
		return nil, fmt.Errorf("write virtual %s: %w", viteConfigName, err)
	}
	return &VirtualConfigResult{
		TransformedSource: transformed,
		VirtualConfigPath: virtualPath,
		InjectedAdapter:   false,
		PinnedVersion:     pinned,
		RoutesRedirected:  routesPointed,
	}, nil
}

// pinViteConfigVersion pins kit.version.name in a config Pokkum is passing
// through unchanged, returning whether it could.
//
// This was missed when the passthrough path was added: it applied the remote
// manifest sort but not the version pin, so a project whose adapter was
// already correct — every SvelteKit 3 project, since svelte.config.js is a
// hard error there, and the common case on SvelteKit 2 — still emitted
// SvelteKit's default Date.now() version name into _app/version.json and was
// therefore not reproducible. Measured: two builds produced
// {"version":"1787380297729"} and {"version":"1787380341039"}.
//
// The bare-call case is deliberately NOT pinned when a svelte.config.js
// exists. SvelteKit ignores svelte.config.js the moment the plugin receives
// any argument, so turning `sveltekit()` into `sveltekit({ version: ... })`
// would silently discard the project's aliases, csp, prerender settings and
// experimental flags — trading a reproducibility gap for a correctness bug.
// The caller warns instead.
func pinViteConfigVersion(source, projectDir string) (string, bool) {
	if strings.Contains(source, "SOURCE_DATE_EPOCH") {
		return source, true // the project pins it itself
	}
	idx := findLiveSvelteKitCall(source)
	if idx < 0 {
		return source, false
	}
	openParen := idx + len("sveltekit(") - 1
	args, ok := scanCallArgs(source, openParen)
	if !ok {
		return source, false
	}
	closeParen := openParen + 1 + len(args)
	trimmed := strings.TrimSpace(args)

	var newArgs string
	switch {
	case strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}"):
		// Already passing options, so the project has itself opted out of
		// svelte.config.js and nothing can be lost by adding one more key.
		newArgs = injectViteVersionPin(args)
	case trimmed == "" && findUserSvelteConfigName(projectDir) == "":
		// No options and no svelte.config.js to shadow: safe to supply one.
		newArgs = "{" + viteVersionProp + "\n\t\t}"
	default:
		return source, false
	}
	if newArgs == args {
		return source, false
	}
	return source[:openParen+1] + newArgs + source[closeParen:], true
}

// findUserSvelteConfigName returns the project's svelte.config.* filename, or
// "" when the project has none (which SvelteKit 3 requires).
func findUserSvelteConfigName(projectDir string) string {
	for _, name := range []string{"svelte.config.js", "svelte.config.ts", "svelte.config.mjs"} {
		if _, err := os.Stat(filepath.Join(projectDir, name)); err == nil {
			return name
		}
	}
	return ""
}

var kitBlockRegex = regexp.MustCompile(`kit\s*:\s*\{`)

// viteVersionProp is the kit.version.name pin, in the flat form the Vite
// config's sveltekit() options object takes. Shared by both the injection and
// the passthrough paths so the two cannot drift — the passthrough silently
// lacking it is exactly the bug this const exists to prevent recurring.
const viteVersionProp = "\n\t\t\tversion: { name: process.env.SOURCE_DATE_EPOCH || 'pokkum-reproducible-build' },"

// injectViteVersionPin inserts a pinned kit.version.name into the flat options
// object passed to sveltekit().
//
// The flat Vite form routes every recognised SvelteKit option into kit, so a
// top-level `version` here becomes kit.version — the same setting
// injectVersionPin writes into a svelte.config.js `kit` block.
//
// It is inserted FIRST, ahead of any spread, so that a project which sets its
// own version (directly, or via a spread of its svelte.config.js) still wins:
// later keys and later spreads override earlier ones in a JS object literal.
// And if the args already mention version at all, this leaves them completely
// alone rather than emitting a duplicate key.
func injectViteVersionPin(args string) string {
	if strings.Contains(args, "SOURCE_DATE_EPOCH") || strings.Contains(args, "version") {
		return args
	}
	firstBrace := strings.Index(args, "{")
	if firstBrace < 0 {
		return args
	}
	return args[:firstBrace+1] + viteVersionProp + args[firstBrace+1:]
}

func injectVersionPin(source string) string {
	versionCode := `
		version: {
			name: process.env.SOURCE_DATE_EPOCH || 'pokkum-reproducible-build'
		},`

	if kitBlockRegex.MatchString(source) {
		return kitBlockRegex.ReplaceAllString(source, "kit: {"+versionCode)
	}

	return source
}

var experimentalBlockRegex = regexp.MustCompile(`experimental\s*:\s*\{`)

func injectExperimentalFlags(source string) string {
	if experimentalBlockRegex.MatchString(source) {
		return source
	}
	expCode := `
		experimental: {
			tracing: { server: true },
			instrumentation: { server: true }
		},`

	if kitBlockRegex.MatchString(source) {
		return kitBlockRegex.ReplaceAllString(source, "kit: {"+expCode)
	}

	return source
}

// remoteManifestSortPrelude is prepended to the virtual Vite config so the
// generated server manifest lists remote-function entries in a deterministic
// order.
//
// The problem it solves: SvelteKit builds its `remotes` array by pushing each
// remote module as Vite resolves it (src/exports/vite/index.js), and
// generate_manifest maps over that array without sorting it. Two builds of
// identical committed source therefore emit the same entries in a different
// ORDER — verified identical once sorted. That changes manifest.js's bytes,
// which changes the content hash Rollup gives its chunk, which renames the
// chunk, which changes handler.js and index.js that import it: one differing
// layer and a different image digest, for a project that changed nothing.
// Confirmed on @sveltejs/kit 2.68.0 and byte-identical in the 3.0.0 release
// candidate, so this covers both major versions.
//
// Why it patches fs.writeFileSync rather than post-processing the output:
// the ordering is baked in BEFORE Rollup hashes the chunk. Sorting afterwards
// would mean recomputing that hash, renaming the file, rewriting every
// importer, and re-hashing those in turn — reimplementing Rollup's own
// hash-and-rename pass. Sorting at the write keeps the fix to one place and
// lets every downstream name fall out deterministically on its own.
//
// Why here rather than a Vite plugin: this runs at module scope of the config
// Pokkum itself generates, so it is in effect before SvelteKit's plugin writes
// anything and needs no assumption about the user's `plugins: [...]` array
// shape. bun's --preload was measured and does NOT reach the build (bun
// spawns vite as a child process), so that route is unavailable.
//
// It fails loudly. An unrecognised entry shape throws rather than writing
// through unchanged, so a future SvelteKit release that changes the manifest
// format surfaces as a build error naming this file — not as reproducibility
// silently regressing while every check stays green.
const remoteManifestSortPrelude = `// Added by Pokkum: deterministic ordering for SvelteKit's remote-function
// manifest. See sveltekitutils.remoteManifestSortPrelude for why this exists.
import __pokkumFs from 'node:fs';
(() => {
	const original = __pokkumFs.writeFileSync;
	const entryRe = /^'[^']+':\s*__memo\(/;
	__pokkumFs.writeFileSync = function (file, data, ...rest) {
		if (typeof data === 'string' && /manifest(-full)?\.js$/.test(String(file)) && data.includes('remotes: {')) {
			data = __pokkumSortRemotes(data, String(file));
		}
		return original.call(this, file, data, ...rest);
	};
	globalThis.__pokkumSortRemotes = function (source, file) {
		const open = source.indexOf('remotes: {');
		if (open < 0) return source;
		const bodyStart = open + 'remotes: {'.length;
		const close = source.indexOf('}', bodyStart);
		if (close < 0) return source;
		const body = source.slice(bodyStart, close);
		const entries = body.split(',\n').map((e) => e.trim()).filter((e) => e.length > 0);
		if (entries.length === 0) return source;
		for (const entry of entries) {
			if (!entryRe.test(entry)) {
				throw new Error(
					'pokkum: cannot sort the remote-function manifest in ' + file + ': unrecognised entry ' +
					JSON.stringify(entry.slice(0, 80)) + '. SvelteKit\'s manifest format has changed, so Pokkum can no ' +
					'longer guarantee a deterministic build. Refusing rather than silently producing an ' +
					'unreproducible image.'
				);
			}
		}
		const sorted = entries.slice().sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
		return source.slice(0, bodyStart) + '\n\t\t\t' + sorted.join(',\n\t\t\t') + '\n\t\t' + source.slice(close);
	};
})();
`

// VersionNamePinned reports whether a SvelteKit config already fixes
// kit.version.name to something stable.
//
// It matters because SvelteKit's default is Date.now(). That timestamp lands in
// _app/version.json, is embedded in the client bundle, and cascades through
// every content hash — two builds of identical committed source produce
// differently-named chunks and differing OCI layers. Pokkum pins it whenever it
// injects a config, but a project that already configures its adapter correctly
// gets no injection and therefore no pin, which inverts the property: the
// better-configured project is the one whose builds do not reproduce.
//
// Deliberately conservative. It returns true on any plausible pin, because the
// cost of a false negative is a warning the user cannot act on, while a false
// positive merely leaves today's silence. Recognised: a SOURCE_DATE_EPOCH
// reference (what Pokkum's own injection writes) and an explicit name inside a
// version block.
func VersionNamePinned(sources ...string) bool {
	for _, src := range sources {
		if src == "" {
			continue
		}
		if strings.Contains(src, "SOURCE_DATE_EPOCH") {
			return true
		}
		if versionNameRe.MatchString(src) {
			return true
		}
	}
	return false
}

// versionNameRe matches a `version: { name: ... }` block, allowing whitespace
// and an optional quote style, e.g. `version: { name: "1.2.3" }` or
// `version:{name:pkg.version}`.
var versionNameRe = regexp.MustCompile(`version\s*:\s*\{[^}]*\bname\s*:`)

// jsStringLiteral renders s as a JavaScript string literal. JSON's string
// encoding is a subset of JavaScript's, so this is exact for any path.
func jsStringLiteral(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		// json.Marshal only fails on unencodable types, never on a string.
		return `""`
	}
	return string(b)
}

// pointViteConfigRoutes rewrites a passed-through Vite config so SvelteKit
// reads its routes from the filtered mirror, reporting whether it could.
//
// Same wrapper shape TransformViteConfig uses, for the same reasons: the
// argument is evaluated once, and any `files` the project already configured
// survives key by key with `routes` spread last so it wins.
func pointViteConfigRoutes(source, routesDir string) (string, bool) {
	if routesDir == "" {
		return source, false
	}
	idx := findLiveSvelteKitCall(source)
	if idx < 0 {
		return source, false
	}
	openParen := idx + len("sveltekit(") - 1
	args, ok := scanCallArgs(source, openParen)
	if !ok {
		return source, false
	}
	closeParen := openParen + len(args) + 1

	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		// A bare sveltekit() loads svelte.config.js, and passing any argument
		// makes SvelteKit skip that file entirely — so injecting routes here
		// would silently discard the project's whole configuration. The
		// caller reports this as "could not apply" rather than doing it.
		return source, false
	}

	prelude := fmt.Sprintf(
		"const __pokkumRoutesDir = %s;\n"+
			"const __pokkumWithRoutes = (o) => ({ ...o, files: { ...(o?.files ?? {}), routes: __pokkumRoutesDir } });\n",
		jsStringLiteral(filepath.ToSlash(routesDir)))

	return prelude + source[:openParen+1] + "__pokkumWithRoutes(" + args + ")" + source[closeParen:], true
}
