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
