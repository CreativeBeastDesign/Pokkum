package sveltekitutils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransformConfig_AdapterReplacement(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
	}{
		{
			name: "replace adapter-auto import",
			input: `import adapter from '@sveltejs/adapter-auto';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

export default {
	preprocess: vitePreprocess(),
	kit: {
		adapter: adapter()
	}
};`,
			contains: []string{
				"import adapter from '@jesterkit/exe-sveltekit';",
				"process.env.SOURCE_DATE_EPOCH",
			},
		},
		{
			name: "replace adapter-node require",
			input: `const adapter = require('@sveltejs/adapter-node');
module.exports = {
	kit: {
		adapter: adapter()
	}
};`,
			contains: []string{
				"const adapter = require('@jesterkit/exe-sveltekit');",
				"process.env.SOURCE_DATE_EPOCH",
			},
		},
		{
			name: "idempotent when adapter already present",
			input: `import adapter from '@jesterkit/exe-sveltekit';
export default {
	kit: {
		adapter: adapter(),
		version: {
			name: process.env.SOURCE_DATE_EPOCH ?? 'dev'
		}
	}
};`,
			contains: []string{
				"import adapter from '@jesterkit/exe-sveltekit';",
				"process.env.SOURCE_DATE_EPOCH",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := DefaultInjectorOptions()
			transformed, err := TransformConfig(tt.input, opts)
			if err != nil {
				t.Fatalf("TransformConfig failed: %v", err)
			}

			for _, expected := range tt.contains {
				if !strings.Contains(transformed, expected) {
					t.Errorf("transformed config missing %q.\nGot:\n%s", expected, transformed)
				}
			}
		})
	}
}

func TestPrepareVirtualConfig(t *testing.T) {
	tempDir := t.TempDir()

	configContent := `import adapter from '@sveltejs/adapter-auto';
export default {
	kit: {
		adapter: adapter()
	}
};`
	if err := os.WriteFile(filepath.Join(tempDir, "svelte.config.js"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	opts := DefaultInjectorOptions()
	res, err := PrepareVirtualConfig(tempDir, opts)
	if err != nil {
		t.Fatalf("PrepareVirtualConfig failed: %v", err)
	}

	if !res.InjectedAdapter {
		t.Errorf("expected InjectedAdapter = true")
	}
	if !res.PinnedVersion {
		t.Errorf("expected PinnedVersion = true")
	}

	if _, err := os.Stat(res.VirtualConfigPath); os.IsNotExist(err) {
		t.Errorf("virtual config file was not created at %s", res.VirtualConfigPath)
	}
}

func TestBuildEnv(t *testing.T) {
	base := []string{"PATH=/usr/bin", "FOO=bar"}
	env := BuildEnv(base, "1234567890")

	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		envMap[parts[0]] = parts[1]
	}

	if envMap["SOURCE_DATE_EPOCH"] != "1234567890" {
		t.Errorf("SOURCE_DATE_EPOCH = %q, want %q", envMap["SOURCE_DATE_EPOCH"], "1234567890")
	}
	if envMap["POKKUM_AUTO_INJECT"] != "1" {
		t.Errorf("POKKUM_AUTO_INJECT = %q, want %q", envMap["POKKUM_AUTO_INJECT"], "1")
	}
	if envMap["FOO"] != "bar" {
		t.Errorf("FOO = %q, want %q", envMap["FOO"], "bar")
	}
}

func TestTransformConfig_TelemetryInjection(t *testing.T) {
	input := `import adapter from '@sveltejs/adapter-auto';
export default {
	kit: {
		adapter: adapter()
	}
};`

	opts := InjectorOptions{
		EnableTelemetry: true,
	}

	transformed, err := TransformConfig(input, opts)
	if err != nil {
		t.Fatalf("TransformConfig failed: %v", err)
	}

	if !strings.Contains(transformed, "tracing: { server: true }") {
		t.Errorf("expected experimental.tracing flag in transformed config:\n%s", transformed)
	}
	if !strings.Contains(transformed, "instrumentation: { server: true }") {
		t.Errorf("expected experimental.instrumentation flag in transformed config:\n%s", transformed)
	}
}

func TestTransformViteConfig_AdapterAutoToAdapterNode(t *testing.T) {
	input := `import adapter from '@sveltejs/adapter-auto';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		sveltekit({
			compilerOptions: {
				runes: ({ filename }) => filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			adapter: adapter()
		})
	]
});`

	opts := InjectorOptions{
		TargetAdapter: "@sveltejs/adapter-node",
	}

	transformed, err := TransformViteConfig(input, opts)
	if err != nil {
		t.Fatalf("TransformViteConfig failed: %v", err)
	}

	if !strings.Contains(transformed, "import adapter from '@sveltejs/adapter-node';") {
		t.Errorf("expected adapter-node import, got:\n%s", transformed)
	}
	if strings.Contains(transformed, "@sveltejs/adapter-auto") {
		t.Errorf("expected adapter-auto to be removed, got:\n%s", transformed)
	}
	if !strings.Contains(transformed, "compilerOptions:") || !strings.Contains(transformed, "runes:") {
		t.Errorf("expected compilerOptions.runes to be preserved, got:\n%s", transformed)
	}
}

func TestTransformViteConfig_BareSvelteKitCall(t *testing.T) {
	input := `import { defineConfig } from 'vite';
import { sveltekit } from '@sveltejs/kit/vite';

export default defineConfig({
	plugins: [sveltekit()]
});`

	opts := InjectorOptions{
		TargetAdapter: "@sveltejs/adapter-node",
	}

	transformed, err := TransformViteConfig(input, opts)
	if err != nil {
		t.Fatalf("TransformViteConfig failed: %v", err)
	}

	if !strings.Contains(transformed, "import adapter from '@sveltejs/adapter-node';") {
		t.Errorf("expected adapter import injected, got:\n%s", transformed)
	}
	if !strings.Contains(transformed, "sveltekit({ adapter: adapter() })") {
		t.Errorf("expected sveltekit({ adapter: adapter() }), got:\n%s", transformed)
	}
}

func TestTransformViteConfig_MultiPluginPreserved(t *testing.T) {
	input := `import tailwindcss from '@tailwindcss/vite';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		tailwindcss(),
		sveltekit({
			compilerOptions: { runes: true }
		})
	]
});`

	opts := InjectorOptions{
		TargetAdapter: "@sveltejs/adapter-node",
	}

	transformed, err := TransformViteConfig(input, opts)
	if err != nil {
		t.Fatalf("TransformViteConfig failed: %v", err)
	}

	if !strings.Contains(transformed, "tailwindcss()") {
		t.Errorf("expected tailwindcss() to be preserved, got:\n%s", transformed)
	}
	if !strings.Contains(transformed, "compilerOptions: { runes: true }") {
		t.Errorf("expected compilerOptions to be preserved, got:\n%s", transformed)
	}
	if !strings.Contains(transformed, "adapter: adapter()") {
		t.Errorf("expected adapter: adapter() injected, got:\n%s", transformed)
	}
}

func TestTransformViteConfig_StaticAdapter(t *testing.T) {
	input := `import adapter from '@sveltejs/adapter-auto';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit({ adapter: adapter() })]
});`

	opts := InjectorOptions{
		TargetAdapter: "@sveltejs/adapter-static",
	}

	transformed, err := TransformViteConfig(input, opts)
	if err != nil {
		t.Fatalf("TransformViteConfig failed: %v", err)
	}

	if !strings.Contains(transformed, "import adapter from '@sveltejs/adapter-static';") {
		t.Errorf("expected adapter-static import, got:\n%s", transformed)
	}
}

func TestPrepareVirtualViteConfig(t *testing.T) {
	tempDir := t.TempDir()

	configContent := `import adapter from '@sveltejs/adapter-auto';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit({ adapter: adapter() })]
});`

	opts := InjectorOptions{
		TargetAdapter: "@sveltejs/adapter-node",
	}

	res, err := PrepareVirtualViteConfig(tempDir, "vite.config.ts", configContent, opts)
	if err != nil {
		t.Fatalf("PrepareVirtualViteConfig failed: %v", err)
	}

	if !res.InjectedAdapter {
		t.Errorf("expected InjectedAdapter = true")
	}

	if _, err := os.Stat(res.VirtualConfigPath); os.IsNotExist(err) {
		t.Errorf("virtual config file was not created at %s", res.VirtualConfigPath)
	}

	data, err := os.ReadFile(res.VirtualConfigPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !strings.Contains(string(data), "@sveltejs/adapter-node") {
		t.Errorf("written virtual config missing target adapter:\n%s", string(data))
	}
}

// TestShimDirnameAndImportMetaURL guards the fix for PB-5 (see Roadmap.md/
// Lessons.md): the virtual config written to <projectDir>/.pokkum/ executes
// one directory level deeper than the real vite.config.ts it was copied
// from, so a real Bun runtime's ambient __dirname (and any
// fileURLToPath(import.meta.url) computation) would otherwise silently
// resolve into .pokkum/ instead of the real project directory — breaking
// the near-universal path.resolve(__dirname, './src/lib') pattern used to
// build resolve.alias entries.
func TestShimDirnameAndImportMetaURL(t *testing.T) {
	realConfigPath := "/home/user/myapp/vite.config.ts"
	out := shimDirnameAndImportMetaURL("const x = 1;\n", realConfigPath)

	if !strings.Contains(out, `const __dirname = "/home/user/myapp";`) {
		t.Errorf("expected __dirname shadowed to the real project dir, got:\n%s", out)
	}
	if !strings.Contains(out, `const __filename = "/home/user/myapp/vite.config.ts";`) {
		t.Errorf("expected __filename shadowed to the real config path, got:\n%s", out)
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "const x = 1;") {
		t.Errorf("expected original source preserved after the preamble, got:\n%s", out)
	}
}

func TestShimDirnameAndImportMetaURL_ReplacesImportMetaURL(t *testing.T) {
	realConfigPath := "/home/user/myapp/vite.config.ts"
	input := "const dir = path.dirname(fileURLToPath(import.meta.url));\n"
	out := shimDirnameAndImportMetaURL(input, realConfigPath)

	if strings.Contains(out, "import.meta.url") {
		t.Errorf("expected import.meta.url token replaced, still present in:\n%s", out)
	}
	if !strings.Contains(out, `fileURLToPath("file:///home/user/myapp/vite.config.ts")`) {
		t.Errorf("expected import.meta.url replaced with the real file:// URL, got:\n%s", out)
	}
}

// TestPrepareVirtualViteConfig_DirnameResolvesToRealProjectDir is an
// end-to-end regression test through the public entrypoint: a vite.config.ts
// using both the __dirname pattern and the import.meta.url pattern to build
// resolve.alias entries must, once staged into .pokkum/, still compute paths
// rooted at the REAL project directory — not <project>/.pokkum.
func TestPrepareVirtualViteConfig_DirnameResolvesToRealProjectDir(t *testing.T) {
	tempDir := t.TempDir()

	configContent := `import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

const metaDir = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
	plugins: [sveltekit()],
	resolve: {
		alias: {
			'$lib-dirname': path.resolve(__dirname, './src/lib'),
			'$lib-meta': path.resolve(metaDir, './src/lib'),
		},
	},
});`

	opts := InjectorOptions{TargetAdapter: "@sveltejs/adapter-node"}
	res, err := PrepareVirtualViteConfig(tempDir, "vite.config.ts", configContent, opts)
	if err != nil {
		t.Fatalf("PrepareVirtualViteConfig failed: %v", err)
	}

	realDirLiteral := `"` + tempDir + `"`
	if !strings.Contains(res.TransformedSource, "const __dirname = "+realDirLiteral+";") {
		t.Errorf("expected __dirname shadowed to %s, got:\n%s", tempDir, res.TransformedSource)
	}
	wantFileURL := `"file://` + tempDir + `/vite.config.ts"`
	if !strings.Contains(res.TransformedSource, wantFileURL) {
		t.Errorf("expected import.meta.url replaced with %s, got:\n%s", wantFileURL, res.TransformedSource)
	}
	if strings.Contains(res.TransformedSource, "import.meta.url") {
		t.Errorf("import.meta.url token should have been substituted away, got:\n%s", res.TransformedSource)
	}
}

func TestTransformViteConfig_AdversarialComplexCases(t *testing.T) {
	t.Run("commonjs_require_transformed", func(t *testing.T) {
		input := `const adapter = require('@sveltejs/adapter-auto');
const { sveltekit } = require('@sveltejs/kit/vite');
module.exports = { plugins: [sveltekit({ compilerOptions: { runes: true }, adapter: adapter({ out: 'build' }) })] };`

		opts := InjectorOptions{TargetAdapter: "@sveltejs/adapter-node"}
		out, err := TransformViteConfig(input, opts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "require('@sveltejs/adapter-node')") {
			t.Errorf("expected require of adapter-node, got:\n%s", out)
		}
		if strings.Contains(out, "@sveltejs/adapter-auto") {
			t.Errorf("expected adapter-auto removed, got:\n%s", out)
		}
		if !strings.Contains(out, "adapter: adapter()") {
			t.Errorf("expected adapter: adapter(), got:\n%s", out)
		}
		if !strings.Contains(out, "compilerOptions: { runes: true }") {
			t.Errorf("expected compilerOptions preserved, got:\n%s", out)
		}
	})

	t.Run("confusing_identifiers_and_comments", func(t *testing.T) {
		input := `// sveltekit({ adapter: fakeAdapter() })
import adapter from '@sveltejs/adapter-auto';
import { sveltekit } from '@sveltejs/kit/vite';
import { not_sveltekit } from './helper';

export default {
	plugins: [
		not_sveltekit({ foo: "sveltekit({ adapter: fake })" }),
		sveltekit({
			// comment inside options
			compilerOptions: { runes: true }
		})
	]
};`

		opts := InjectorOptions{TargetAdapter: "@sveltejs/adapter-node"}
		out, err := TransformViteConfig(input, opts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "import adapter from '@sveltejs/adapter-node';") {
			t.Errorf("expected import adapter-node, got:\n%s", out)
		}
		if !strings.Contains(out, "not_sveltekit({ foo: \"sveltekit({ adapter: fake })\" })") {
			t.Errorf("expected not_sveltekit call preserved verbatim, got:\n%s", out)
		}
		if !strings.Contains(out, "adapter: adapter(),") {
			t.Errorf("expected adapter: adapter(), injected, got:\n%s", out)
		}
		if !strings.Contains(out, "compilerOptions: { runes: true }") {
			t.Errorf("expected compilerOptions preserved, got:\n%s", out)
		}
	})

	t.Run("already_configured_is_idempotent", func(t *testing.T) {
		input := `import adapter from '@sveltejs/adapter-node';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit({ adapter: adapter() })]
});`

		opts := InjectorOptions{TargetAdapter: "@sveltejs/adapter-node"}
		out, err := TransformViteConfig(input, opts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count := strings.Count(out, "@sveltejs/adapter-node"); count != 1 {
			t.Errorf("expected exactly 1 adapter-node import, got %d:\n%s", count, out)
		}
		if count := strings.Count(out, "adapter: adapter()"); count != 1 {
			t.Errorf("expected exactly 1 adapter: adapter() invocation, got %d:\n%s", count, out)
		}
	})
}
