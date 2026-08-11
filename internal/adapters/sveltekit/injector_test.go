package sveltekit

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

