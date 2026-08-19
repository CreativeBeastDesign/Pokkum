package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestRealBuild_SvelteConfigSurvivesAdapterInjection is the empirical regression
// test for a reported build failure:
//
//	[vite-plugin-sveltekit-guard] Could not load virtual:env/dynamic/private:
//	To enable remote functions, add kit.experimental.remoteFunctions
//
// The project had that flag, in svelte.config.js. SvelteKit calls
// load_svelte_config() only when the sveltekit() plugin receives no argument, so
// rewriting a bare sveltekit() into sveltekit({ adapter: adapter() }) to inject an
// adapter discarded the whole file — aliases, csp, prerender settings and every
// kit.experimental flag.
//
// The unit tests in sveltekitutils assert the generated source contains the right
// spreads. That is not the same claim as "SvelteKit honours it": the two config
// shapes differ (svelte.config.js nests under kit, the Vite form is flat, and
// split_config routes keys by name), so a config that looks right and is shaped
// wrong would pass those and still lose everything. This test runs a real build
// and checks an observable that only appears if the file was actually applied.
//
// kit.appDir is the observable: it renames the client asset directory, so its
// custom value appearing in the output proves the merge reached SvelteKit, and
// the default "_app" appearing would prove it did not.
func TestRealBuild_SvelteConfigSurvivesAdapterInjection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real bun build in short mode")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not found on PATH; skipping real build")
	}

	const probeAppDir = "pokkum_merge_probe"

	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-adapter-node"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture, "node_modules")); statErr != nil {
		t.Skipf("fixture dependencies not installed (run `bun install` in %s): %v", fixture, statErr)
	}
	dir := copyFixtureProject(t, fixture)

	// Reshape the fixture into the shape that triggered the bug: configuration in
	// svelte.config.js, and a bare sveltekit() with nothing passed to it.
	writeProjectFile(t, dir, "vite.config.ts", `import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit()]
});
`)
	writeProjectFile(t, dir, "svelte.config.js", `export default {
	kit: {
		appDir: '`+probeAppDir+`'
	}
};
`)
	// Stale output from the fixture would make the assertion meaningless.
	for _, junk := range []string{"build", ".svelte-kit", ".pokkum"} {
		if err := os.RemoveAll(filepath.Join(dir, junk)); err != nil {
			t.Fatal(err)
		}
	}

	// Drives the real compiler, which is what performs the injection. Prepare
	// alone is enough: the question is whether SvelteKit honoured the merged
	// config, which is settled by the build output, with no packaging or
	// container runtime involved.
	c := bunexec.NewCompiler(testLogger())
	prep, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir: dir,
		Strategy:   ports.StrategyLayered,
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// Confirm injection actually engaged, or the assertion below would be
	// checking an ordinary build and proving nothing about the merge.
	if _, statErr := os.Stat(filepath.Join(dir, ".pokkum", "vite.config.ts")); statErr != nil {
		t.Fatalf("premise broken: no virtual vite config was written, so adapter injection did not run: %v", statErr)
	}

	clientDir := filepath.Join(prep.OutputDir, "client")
	entries, err := os.ReadDir(clientDir)
	if err != nil {
		t.Fatalf("read client output %s: %v", clientDir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}

	var sawProbe, sawDefault bool
	for _, n := range names {
		switch n {
		case probeAppDir:
			sawProbe = true
		case "_app":
			sawDefault = true
		}
	}
	if !sawProbe {
		t.Errorf("svelte.config.js's kit.appDir was not applied: expected %q in the client output, got %v.\n"+
			"That means adapter injection discarded the project's SvelteKit config.", probeAppDir, names)
	}
	if sawDefault {
		t.Errorf("client output contains the default \"_app\", so svelte.config.js was ignored: %v", names)
	}
}

// writeProjectFile overwrites a file in the scratch copy. Local to this file
// because the sibling package's equivalent is not importable: tests/integration
// deliberately holds both `package integration` (the harness and the real-build
// tests, which need its unexported helpers) and `package integration_test` (the
// CLI workflow tests, which drive the built binary and need none of them).
func writeProjectFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
