package sveltekitutils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPackageJSON(t *testing.T) {
	dir := t.TempDir()
	content := `{"name": "app", "dependencies": {"@sveltejs/kit": "^2.5.0"}, "devDependencies": {"@jesterkit/exe-sveltekit": "^0.4.0"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	pkg, err := ReadPackageJSON(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pkg.Name != "app" {
		t.Errorf("Name = %q, want %q", pkg.Name, "app")
	}
	if !pkg.HasDependency("@sveltejs/kit") {
		t.Error("HasDependency(@sveltejs/kit) = false, want true")
	}
	if !pkg.HasDependency("@jesterkit/exe-sveltekit") {
		t.Error("HasDependency(@jesterkit/exe-sveltekit) = false, want true (devDependency)")
	}
	if pkg.HasDependency("left-pad") {
		t.Error("HasDependency(left-pad) = true, want false")
	}
}

func TestReadPackageJSON_Missing(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadPackageJSON(dir); err == nil {
		t.Fatal("expected error for missing package.json, got nil")
	}
}

func TestAdapterConfigured(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{"esm import", `import adapter from "@jesterkit/exe-sveltekit";`, true},
		{"require", `const adapter = require("@jesterkit/exe-sveltekit");`, true},
		{"absent", `import adapter from "@sveltejs/adapter-auto";`, false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AdapterConfigured(tt.source, "@jesterkit/exe-sveltekit"); got != tt.want {
				t.Errorf("AdapterConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTargetsLinuxX64(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{"double quotes", `adapter({ target: "linux-x64" })`, true},
		{"single quotes", `adapter({ target: 'linux-x64' })`, true},
		{"extra whitespace", `adapter({ target :   "linux-x64" })`, true},
		{"different target", `adapter({ target: "linux-x64-musl" })`, false},
		{"no target option", `adapter({ embedStatic: true })`, false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TargetsLinuxX64(tt.source); got != tt.want {
				t.Errorf("TargetsLinuxX64() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveVersion_PrefersInstalledOverDeclared(t *testing.T) {
	dir := t.TempDir()
	nodeModulesPkg := filepath.Join(dir, "node_modules", "@jesterkit", "exe-sveltekit")
	if err := os.MkdirAll(nodeModulesPkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeModulesPkg, "package.json"), []byte(`{"version": "0.4.2"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	pkg := PackageJSON{DevDependencies: map[string]string{"@jesterkit/exe-sveltekit": "^0.4.0"}}
	if got, want := ResolveVersion(dir, "@jesterkit/exe-sveltekit", pkg), "0.4.2"; got != want {
		t.Errorf("ResolveVersion() = %q, want installed version %q", got, want)
	}
}

func TestResolveVersion_FallsBackToDeclaredRange(t *testing.T) {
	dir := t.TempDir() // no node_modules at all
	pkg := PackageJSON{DevDependencies: map[string]string{"@jesterkit/exe-sveltekit": "^0.4.0"}}
	if got, want := ResolveVersion(dir, "@jesterkit/exe-sveltekit", pkg), "^0.4.0"; got != want {
		t.Errorf("ResolveVersion() = %q, want declared range %q", got, want)
	}
}

func TestResolveVersion_EmptyWhenUnknown(t *testing.T) {
	dir := t.TempDir()
	pkg := PackageJSON{}
	if got := ResolveVersion(dir, "@jesterkit/exe-sveltekit", pkg); got != "" {
		t.Errorf("ResolveVersion() = %q, want empty string", got)
	}
}

func TestIsVersionAtLeast(t *testing.T) {
	tests := []struct {
		ver  string
		want bool
	}{
		{"2.31.0", true},
		{"2.32.1", true},
		{"3.0.0", true},
		{"^2.31.0", true},
		{"~2.31.5", true},
		{"2.30.9", false},
		{"^2.10.0", false},
		{"1.99.0", false},
	}
	for _, tt := range tests {
		if got := IsVersionAtLeast(tt.ver, 2, 31); got != tt.want {
			t.Errorf("IsVersionAtLeast(%q, 2, 31) = %v, want %v", tt.ver, got, tt.want)
		}
	}
}

func TestCheckTelemetrySupported(t *testing.T) {
	dir := t.TempDir()
	content := `{"name": "app", "dependencies": {"@sveltejs/kit": "^2.31.0"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	supported, ver, err := CheckTelemetrySupported(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !supported {
		t.Errorf("expected supported=true for @sveltejs/kit %s", ver)
	}
}

func TestStaticFallbackFilename(t *testing.T) {
	tests := []struct {
		name    string
		cfg     string
		want    string
		enabled bool
	}{
		{
			name:    "single-quoted string",
			cfg:     `import adapter from '@sveltejs/adapter-static'; export default { kit: { adapter: adapter({ fallback: '200.html' }) } };`,
			want:    "200.html",
			enabled: true,
		},
		{
			name:    "double-quoted string",
			cfg:     `adapter({ fallback: "index.html" })`,
			want:    "index.html",
			enabled: true,
		},
		{
			name:    "backtick string",
			cfg:     "adapter({ fallback: `app.html` })",
			want:    "app.html",
			enabled: true,
		},
		{
			name:    "boolean true maps to adapter-static default 200.html",
			cfg:     `adapter({ fallback: true })`,
			want:    "200.html",
			enabled: true,
		},
		{
			name:    "explicit false disables",
			cfg:     `adapter({ fallback: false })`,
			want:    "",
			enabled: false,
		},
		{
			name:    "no fallback option",
			cfg:     `adapter({})`,
			want:    "",
			enabled: false,
		},
		{
			name:    "false beats a later true",
			cfg:     `adapter({ fallback: true, fallback: false })`,
			want:    "",
			enabled: false,
		},
		{
			// Regression: a commented-out mention must never be treated as
			// live config. Before the comment-stripping fix, this made
			// bunexec.Prepare hard-fail every build of a project whose
			// config merely mentions "fallback" in a comment, since
			// adapter-static never actually emits a fallback file for it.
			name:    "commented single-line mention does not enable",
			cfg:     "// fallback: '200.html'  // uncomment for SPA mode\nadapter({})",
			want:    "",
			enabled: false,
		},
		{
			// Regression: a commented `fallback: false` must never suppress
			// a real, live `fallback: '...'` elsewhere in the file. Before
			// the fix, staticFallbackFalsePattern short-circuited on the
			// comment text alone, silently shipping an SPA project with no
			// fallback shell staged at all.
			name:    "commented false does not suppress real live fallback",
			cfg:     "// set fallback: false to disable\nadapter({ fallback: '200.html' })",
			want:    "200.html",
			enabled: true,
		},
		{
			name:    "block comment around false does not suppress real live fallback",
			cfg:     "/* fallback: false */\nadapter({ fallback: '200.html' })",
			want:    "200.html",
			enabled: true,
		},
		{
			// String-literal awareness: the "//" inside the URL must not be
			// misread as a line-comment opener, which would otherwise eat
			// the real config that follows on the same line.
			name:    "string literal with slashes does not break real config detection",
			cfg:     `const help = "see https://example.com/docs#fallback"; adapter({ fallback: '200.html' })`,
			want:    "200.html",
			enabled: true,
		},
		{
			// A string literal merely containing the substring "fallback:"
			// (a route path / log message, not a real option) must not
			// enable detection on its own.
			name:    "string literal mention alone does not enable",
			cfg:     `const help = "path is /docs/fallback: see the manual"; adapter({})`,
			want:    "",
			enabled: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, enabled := StaticFallbackFilename(tt.cfg)
			if enabled != tt.enabled {
				t.Errorf("enabled = %v, want %v", enabled, tt.enabled)
			}
			if got != tt.want {
				t.Errorf("filename = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripJSComments(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{"no comments", `adapter({ fallback: '200.html' })`, `adapter({ fallback: '200.html' })`},
		{"line comment stripped", "adapter({}) // trailing note", "adapter({}) "},
		{"block comment stripped to a space", "x/*comment*/y", "x y"},
		{"multiline block comment stripped", "a/*\nmulti\nline\n*/b", "a b"},
		{
			"line comment inside single-quoted string preserved",
			`const u = 'http://example.com';`,
			`const u = 'http://example.com';`,
		},
		{
			"line comment inside double-quoted string preserved",
			`const u = "http://example.com";`,
			`const u = "http://example.com";`,
		},
		{
			"block-comment-looking text inside backtick string preserved",
			"const s = `/* not a comment */`;",
			"const s = `/* not a comment */`;",
		},
		{
			"escaped quote inside string does not end the literal early",
			`const s = "a \"quoted\" // word"; // real comment`,
			`const s = "a \"quoted\" // word"; `,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripJSComments(tt.source); got != tt.want {
				t.Errorf("stripJSComments(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

// The three fixtures below are verbatim captures, not hand-crafted samples —
// see Lessons.md's 2026-08-16 entries on why a fixture shaped to satisfy the
// code under test, rather than to match a real tool's output, is worse than
// no fixture at all.

// realSvelteKitBasicViteConfig is testdata/fixtures/sveltekit-basic/vite.config.ts
// verbatim: a bare `sveltekit()` call with no options, paired with that
// fixture's own svelte.config.js (which names @jesterkit/exe-sveltekit). It is
// the one fixture in this file proven to build successfully through the real
// pipeline (tests/integration/reproducibility_e2e_test.go), so it is the
// canonical "must not regress" case: a bare call must never be treated as
// overriding svelte.config.js.
const realSvelteKitBasicViteConfig = `import { defineConfig } from 'vite';
import { sveltekit } from '@sveltejs/kit/vite';

export default defineConfig({
  plugins: [sveltekit()]
});
`

// realSvCreateDefaultViteConfig is vite.config.ts exactly as emitted by
// `bunx sv create --template minimal --types ts --no-add-ons` (sv@0.17.0,
// @sveltejs/kit@2.63.0 range, @sveltejs/vite-plugin-svelte@7.1.2), captured
// 2026-08-17. This is the default, totally unconfigured scaffold: no
// svelte.config.js is generated at all, and the adapter is @sveltejs/adapter-auto
// — not whatever strategy the caller actually wants.
const realSvCreateDefaultViteConfig = `import adapter from '@sveltejs/adapter-auto';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},

			// adapter-auto only supports some environments, see https://svelte.dev/docs/kit/adapter-auto for a list.
			// If your environment is not supported, or you settled on a specific environment, switch out the adapter.
			// See https://svelte.dev/docs/kit/adapters for more information about adapters.
			adapter: adapter()
		})
	]
});
`

// realSvCreateAdapterNodeViteConfig is vite.config.ts exactly as emitted by
// `bunx sv create --template minimal --types ts --add sveltekit-adapter=adapter:node`
// (same versions as above, plus @sveltejs/adapter-node@5.5.7), captured
// 2026-08-17. Confirmed by a real `bun install` + `bun run build` against this
// exact project (with a decoy svelte.config.js importing @sveltejs/adapter-static
// placed alongside it) that the build genuinely uses adapter-node — printing
// "svelte.config.js is ignored when options are passed via your Vite config"
// and "> Using @sveltejs/adapter-node" — not the decoy.
const realSvCreateAdapterNodeViteConfig = `import adapter from '@sveltejs/adapter-node';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) => filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			adapter: adapter()
		})
	]
});
`

// realDecoySvelteConfig is the deliberately-wrong svelte.config.js placed
// alongside realSvCreateAdapterNodeViteConfig during the real build described
// above, to confirm empirically that Vite's config wins over it.
const realDecoySvelteConfig = `import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

export default {
	preprocess: vitePreprocess(),
	kit: {
		adapter: adapter()
	}
};
`

func TestViteConfigOverridesSvelteConfig(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{"empty", ``, false},
		{"bare sveltekit() call — real sveltekit-basic fixture", realSvelteKitBasicViteConfig, false},
		{"sv create default scaffold — real capture, adapter-auto options", realSvCreateDefaultViteConfig, true},
		{"sv create adapter-node scaffold — real capture", realSvCreateAdapterNodeViteConfig, true},
		{"commented-out options object does not count", "sveltekit(/* { adapter: adapter() } */)", false},
		{"non-plugin call of the same name is ignored", `mySveltekit({ adapter: adapter() })`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ViteConfigOverridesSvelteConfig(tt.source); got != tt.want {
				t.Errorf("ViteConfigOverridesSvelteConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEffectiveAdapterConfigured(t *testing.T) {
	tests := []struct {
		name            string
		svelteConfigSrc string
		viteConfigSrc   string
		viteConfigName  string
		pkgName         string
		wantConfigured  bool
		wantReadFrom    string
		wantOverridden  bool
	}{
		{
			name:            "real sveltekit-basic fixture: bare vite.config.ts, svelte.config.js governs and matches",
			svelteConfigSrc: `import adapter from '@jesterkit/exe-sveltekit';`,
			viteConfigSrc:   realSvelteKitBasicViteConfig,
			viteConfigName:  "vite.config.ts",
			pkgName:         "@jesterkit/exe-sveltekit",
			wantConfigured:  true,
			wantReadFrom:    "svelte.config.js",
			wantOverridden:  false,
		},
		{
			name:            "no vite.config.ts at all: svelte.config.js governs by default",
			svelteConfigSrc: `import adapter from '@sveltejs/adapter-node';`,
			viteConfigSrc:   "",
			viteConfigName:  "",
			pkgName:         "@sveltejs/adapter-node",
			wantConfigured:  true,
			wantReadFrom:    "svelte.config.js",
			wantOverridden:  false,
		},
		{
			name:            "sv create default scaffold: no svelte.config.js, vite.config.ts overrides with adapter-auto — target adapter-node not found",
			svelteConfigSrc: "",
			viteConfigSrc:   realSvCreateDefaultViteConfig,
			viteConfigName:  "vite.config.ts",
			pkgName:         "@sveltejs/adapter-node",
			wantConfigured:  false,
			wantReadFrom:    "vite.config.ts",
			wantOverridden:  true,
		},
		{
			name:            "sv create adapter-node scaffold: no svelte.config.js, vite.config.ts overrides and matches",
			svelteConfigSrc: "",
			viteConfigSrc:   realSvCreateAdapterNodeViteConfig,
			viteConfigName:  "vite.config.ts",
			pkgName:         "@sveltejs/adapter-node",
			wantConfigured:  true,
			wantReadFrom:    "vite.config.ts",
			wantOverridden:  true,
		},
		{
			name:            "compounding case: decoy svelte.config.js present, but real vite.config.ts governs and matches — proven by a real build",
			svelteConfigSrc: realDecoySvelteConfig,
			viteConfigSrc:   realSvCreateAdapterNodeViteConfig,
			viteConfigName:  "vite.config.ts",
			pkgName:         "@sveltejs/adapter-node",
			wantConfigured:  true,
			wantReadFrom:    "vite.config.ts",
			wantOverridden:  true,
		},
		{
			name:            "compounding case, wrong target: decoy svelte.config.js does name adapter-static, but vite.config.ts governs and does not — proven by the same real build using adapter-node, not adapter-static",
			svelteConfigSrc: realDecoySvelteConfig,
			viteConfigSrc:   realSvCreateAdapterNodeViteConfig,
			viteConfigName:  "vite.config.ts",
			pkgName:         "@sveltejs/adapter-static",
			wantConfigured:  false,
			wantReadFrom:    "vite.config.ts",
			wantOverridden:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configured, readFrom, overridden := EffectiveAdapterConfigured(tt.svelteConfigSrc, tt.viteConfigSrc, tt.viteConfigName, tt.pkgName)
			if configured != tt.wantConfigured {
				t.Errorf("configured = %v, want %v", configured, tt.wantConfigured)
			}
			if readFrom != tt.wantReadFrom {
				t.Errorf("readFrom = %q, want %q", readFrom, tt.wantReadFrom)
			}
			if overridden != tt.wantOverridden {
				t.Errorf("overridden = %v, want %v", overridden, tt.wantOverridden)
			}
		})
	}
}
