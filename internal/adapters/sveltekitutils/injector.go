package sveltekitutils

import (
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
)

func replaceAdapterImport(source, targetAdapter string) string {
	if adapterImportRegex.MatchString(source) {
		return adapterImportRegex.ReplaceAllString(source, fmt.Sprintf("import adapter from '%s';", targetAdapter))
	}
	if requireRegex.MatchString(source) {
		return requireRegex.ReplaceAllString(source, fmt.Sprintf("const adapter = require('%s');", targetAdapter))
	}

	// If no standard adapter import was found, prepend the import at top of file
	return fmt.Sprintf("import adapter from '%s';\n%s", targetAdapter, source)
}

func replaceViteAdapterImport(source, targetAdapter string) string {
	if viteAdapterImportRegex.MatchString(source) {
		return viteAdapterImportRegex.ReplaceAllString(source, fmt.Sprintf("import adapter from '%s';", targetAdapter))
	}
	if viteRequireRegex.MatchString(source) {
		return viteRequireRegex.ReplaceAllString(source, fmt.Sprintf("const adapter = require('%s');", targetAdapter))
	}

	return fmt.Sprintf("import adapter from '%s';\n%s", targetAdapter, source)
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
	if trimmedArgs == "" {
		newArgs = "{ adapter: adapter() }"
	} else if strings.HasPrefix(trimmedArgs, "{") && strings.HasSuffix(trimmedArgs, "}") {
		if start, end, found := findLiveAdapterProp(args); found {
			newArgs = args[:start] + "adapter: adapter()" + args[end:]
		} else {
			firstBrace := strings.Index(args, "{")
			newArgs = args[:firstBrace+1] + "\n\t\t\tadapter: adapter()," + args[firstBrace+1:]
		}
	} else {
		newArgs = "{ adapter: adapter() }"
	}

	result = result[:openParen+1] + newArgs + result[closeParen:]
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
	return relativeImportSpecifierRegex.ReplaceAllString(source, "${1}${2}../${3}")
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

var kitBlockRegex = regexp.MustCompile(`kit\s*:\s*\{`)

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
