package sveltekit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// Regular expressions for adapter import replacement
var (
	adapterImportRegex = regexp.MustCompile(`import\s+([a-zA-Z0-9_$]+)\s+from\s+['"]@sveltejs/adapter-[a-z-]+['"];?`)
	requireRegex       = regexp.MustCompile(`const\s+([a-zA-Z0-9_$]+)\s*=\s*require\(['"]@sveltejs/adapter-[a-z-]+['"]\);?`)
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

