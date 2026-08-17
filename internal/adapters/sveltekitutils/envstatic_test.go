package sveltekitutils

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeSourceFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func TestDetectStaticEnvBindings_NamedImports(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "routes/+page.server.ts", `
import { PUBLIC_API_URL } from '$env/static/public';
import { DB_PASSWORD, API_KEY as key } from "$env/static/private";

export function load() {
	return { url: PUBLIC_API_URL, key };
}
`)

	got, err := DetectStaticEnvBindings(dir)
	if err != nil {
		t.Fatalf("DetectStaticEnvBindings: %v", err)
	}
	want := []string{"API_KEY", "DB_PASSWORD", "PUBLIC_API_URL"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDetectStaticEnvBindings_SvelteComponent(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "routes/+page.svelte", `
<script>
	import { PUBLIC_APP_NAME } from '$env/static/public';
</script>
<h1>{PUBLIC_APP_NAME}</h1>
`)

	got, err := DetectStaticEnvBindings(dir)
	if err != nil {
		t.Fatalf("DetectStaticEnvBindings: %v", err)
	}
	if len(got) != 1 || got[0] != "PUBLIC_APP_NAME" {
		t.Errorf("expected PUBLIC_APP_NAME detected in a .svelte file, got %v", got)
	}
}

func TestDetectStaticEnvBindings_NamespaceImport(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "lib/env.ts", `import * as publicEnv from '$env/static/public';`)

	got, err := DetectStaticEnvBindings(dir)
	if err != nil {
		t.Fatalf("DetectStaticEnvBindings: %v", err)
	}
	want := []string{"$env/static/public (namespace import)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDetectStaticEnvBindings_TypeOnlyImportExcluded(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "lib/types.ts", `import type { PUBLIC_API_URL } from '$env/static/public';`)

	got, err := DetectStaticEnvBindings(dir)
	if err != nil {
		t.Fatalf("DetectStaticEnvBindings: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected type-only import to be excluded (erased at compile time, bakes nothing), got %v", got)
	}
}

func TestDetectStaticEnvBindings_DynamicEnvExcluded(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "routes/+page.server.ts", `import { PUBLIC_API_URL } from '$env/dynamic/public';`)

	got, err := DetectStaticEnvBindings(dir)
	if err != nil {
		t.Fatalf("DetectStaticEnvBindings: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected $env/dynamic/* to be excluded (read at runtime, never baked), got %v", got)
	}
}

func TestDetectStaticEnvBindings_DedupesAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "routes/a/+page.server.ts", `import { PUBLIC_API_URL } from '$env/static/public';`)
	writeSourceFile(t, dir, "routes/b/+page.server.ts", `import { PUBLIC_API_URL } from '$env/static/public';`)

	got, err := DetectStaticEnvBindings(dir)
	if err != nil {
		t.Fatalf("DetectStaticEnvBindings: %v", err)
	}
	if len(got) != 1 || got[0] != "PUBLIC_API_URL" {
		t.Errorf("expected exactly one deduped PUBLIC_API_URL, got %v", got)
	}
}

func TestDetectStaticEnvBindings_SkipsNodeModulesAndBuildDirs(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "node_modules/some-pkg/index.js", `import { X } from '$env/static/public';`)
	writeSourceFile(t, dir, ".svelte-kit/generated/foo.js", `import { Y } from '$env/static/public';`)
	writeSourceFile(t, dir, ".pokkum/vite.config.ts", `import { Z } from '$env/static/public';`)
	writeSourceFile(t, dir, "build/server/index.js", `import { W } from '$env/static/public';`)
	writeSourceFile(t, dir, "routes/+page.server.ts", `import { REAL } from '$env/static/public';`)

	got, err := DetectStaticEnvBindings(dir)
	if err != nil {
		t.Fatalf("DetectStaticEnvBindings: %v", err)
	}
	want := []string{"REAL"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v — expected only real project source to be scanned", got, want)
	}
}

func TestDetectStaticEnvBindings_NoImportsReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "routes/+page.svelte", `<h1>Hello world</h1>`)

	got, err := DetectStaticEnvBindings(dir)
	if err != nil {
		t.Fatalf("DetectStaticEnvBindings: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no bindings, got %v", got)
	}
}

func TestDetectStaticEnvBindings_MissingDirReturnsError(t *testing.T) {
	_, err := DetectStaticEnvBindings(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected an error for a nonexistent source directory")
	}
}
