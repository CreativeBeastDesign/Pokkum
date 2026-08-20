package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunruntime"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/nativeinspect"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/secretguard"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// leakedGoogleAPIKey is a syntactically valid (but fake) Google API key —
// the same value internal/core/pipeline_test.go's
// TestBuild_PostBuildSecretScan_* fixtures use — chosen so this test proves
// detection with the same rule (secretguard's "Google API Key" pattern:
// \bAIzaSy[a-zA-Z0-9_-]{33}\b) rather than inventing an untested one.
const leakedGoogleAPIKey = `AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY`

// leakedEnvVarName is the process-env variable name whose value gets
// statically inlined into the built server bundle via Vite's `define`, per
// injectSecretIntoHealthRoute below.
const leakedEnvVarName = "POKKUM_TEST_LEAKED_KEY"

// injectSecretIntoHealthRoute overwrites the sveltekit-basic fixture copy's
// existing +server.ts route and vite.config.ts so that the built server
// bundle contains leakedGoogleAPIKey as a literal string, while neither
// file written to disk here contains the secret text itself.
//
// vite.config.ts gains a `define: { __POKKUM_TEST_LEAKED_KEY__:
// JSON.stringify(process.env.POKKUM_TEST_LEAKED_KEY ?? ”) }` entry — a
// reference to an env var NAME, not its value — and +server.ts references
// the resulting global identifier directly (no import; Vite's `define`
// performs raw esbuild-level text/AST replacement wherever the identifier
// appears). The actual secret VALUE is supplied only via
// core.CompileOptions.Env / ports.PrepareRequest.Env at build time (see
// leakedBuildEnv), so it never touches disk as a file the pre-build scan
// could see.
//
// This is precisely the "a Vite `define` replacement" scenario
// docs/roadmap/supply-chain.yaml's exe-secret-scan-gap names as a surface
// the post-build scan exists to catch (the sibling mechanism named there,
// "$env/static/* baking", was tried first for this test and abandoned: the
// installed @sveltejs/vite-plugin-svelte@3 / SvelteKit combination in this
// fixture's node_modules did not statically export a plain, unprefixed
// process-env-only key from '$env/static/private' — `vite build` failed
// with "not exported by virtual:env/static/private" even though the same
// key was independently confirmed present in
// @sveltejs/kit/src/exports/vite/utils.js's get_env() private map. `define`
// sidesteps that virtual-module resolution entirely and was confirmed by
// hand to both build cleanly and leave the literal secret in
// .svelte-kit/jesterkit-sveltekit/server/entries/endpoints/api/health/_server.ts.js
// (prep.OutputDir, exactly what postBuildScanDirs scans for StrategyExe) —
// and, since @jesterkit/exe-sveltekit's adapt() runs its own internal `bun
// build --compile` as part of `bun run build`, in the resulting compiled
// executable's raw bytes too.
func injectSecretIntoHealthRoute(t *testing.T, projectDir string) {
	t.Helper()

	viteConfig := filepath.Join(projectDir, "vite.config.ts")
	viteConfigContent := `import { defineConfig } from 'vite';
import { sveltekit } from '@sveltejs/kit/vite';

// Planted by exe_secret_scan_real_bun_test.go: define's value is a
// reference to an env var NAME (` + leakedEnvVarName + `), not the secret
// itself -- see injectSecretIntoHealthRoute's doc comment.
export default defineConfig({
  plugins: [sveltekit()],
  define: {
    __POKKUM_TEST_LEAKED_KEY__: JSON.stringify(process.env.` + leakedEnvVarName + ` ?? '')
  }
});
`
	if err := os.WriteFile(viteConfig, []byte(viteConfigContent), 0o644); err != nil {
		t.Fatalf("injectSecretIntoHealthRoute: write %q: %v", viteConfig, err)
	}

	route := filepath.Join(projectDir, "src", "routes", "api", "health", "+server.ts")
	routeContent := `import { json } from '@sveltejs/kit';

// Planted by exe_secret_scan_real_bun_test.go: __POKKUM_TEST_LEAKED_KEY__ is
// a Vite define global, not a literal here -- see
// injectSecretIntoHealthRoute's doc comment. The literal secret value exists
// only in the compiled server output, never in this source file.
declare const __POKKUM_TEST_LEAKED_KEY__: string;

export function GET() {
  return json({ ok: true, leaked: __POKKUM_TEST_LEAKED_KEY__ });
}
`
	if err := os.WriteFile(route, []byte(routeContent), 0o644); err != nil {
		t.Fatalf("injectSecretIntoHealthRoute: write %q: %v", route, err)
	}
}

// leakedBuildEnv is the KEY=VALUE entry to pass as
// core.CompileOptions.Env / ports.PrepareRequest.Env so the real `vite
// build` subprocess's `define` config inlines leakedGoogleAPIKey, per
// injectSecretIntoHealthRoute's doc comment.
func leakedBuildEnv() []string {
	return []string{leakedEnvVarName + "=" + leakedGoogleAPIKey}
}

// realBuildDeps returns the same real-adapter core.Deps wiring
// TestRealBuildIsReproducibleAcrossRuns uses (real bunexec.Compiler, real
// packager, real registry/tarball writer, real native inspector), plus a
// real secretguard.Adapter — the one dependency that test deliberately never
// wires up. BaseImages and Supervisor stay faked for the same reasons cited
// there: resolving a real base image needs registry network access, and
// neither port is implicated in secret scanning.
func realBuildDeps(t *testing.T) core.Deps {
	t.Helper()
	return core.Deps{
		Compiler:        bunexec.NewCompiler(testLogger()),
		BaseImages:      &mockBaseResolver{t: t},
		Supervisor:      &mockSupervisorProvider{},
		Packager:        packager.NewPackager(testLogger()),
		BunRuntime:      bunruntime.NewResolver("", nil),
		Tarballs:        registry.NewAdapter(testLogger()),
		NativeInspector: nativeinspect.NewClosuredAdapter(),
		SecretGuard:     secretguard.NewAdapter(),
		Logger:          testLogger(),
		Version:         "0.1.0-secret-scan-test",
	}
}

// TestRealBuild_ExeStrategy_PostBuildSecretScanCatchesRealBunOutput closes
// docs/roadmap/supply-chain.yaml's exe-secret-scan-gap follow-up: the
// pre-compile post-build secret scan for --strategy=exe (internal/core
// pipeline.go's runSecretScan, invoked over postBuildScanDirs) was, until
// this test, exercised only against hand-written synthetic fixtures
// (internal/core/pipeline_test.go's TestBuild_PostBuildSecretScan_* — a
// mockCompiler writing a JS chunk by hand) or against a real bunexec.Compiler
// with SecretGuard left nil (tests/integration's
// TestRealBuildIsReproducibleAcrossRuns / TestFixtureDrivenE2E). No test
// combined a REAL `bun run build` (via bunexec.Compiler.Prepare, invoked
// through core.Build) with the REAL secretguard.Adapter.
//
// That combination matters because a real SvelteKit/Vite/Bun build's output
// shape — routinely one minified line spanning tens of KB — is exactly what
// interacts with the scanner's line/size handling (see
// internal/adapters/secretguard/guard.go's doc comment on why it moved off
// bufio.Scanner's 64KB token limit, and Lessons.md's entry on the bug that
// caused: a scan error silently treated as "nothing found"). A synthetic
// fixture, by construction, cannot reproduce that shape.
//
// The secret is planted in src/routes/api/health/+server.ts (see
// injectSecretIntoHealthRoute), a file that already exists in
// testdata/fixtures/sveltekit-basic and is always included in the server
// bundle — so this is a real dependency-compromise/misused-env-var shape,
// not a scan-tool self-test.
//
// Only in tests/integration/**, and the fixture edit lands in a
// copyFixtureProject scratch copy (t.TempDir()), never in the checked-out
// testdata/fixtures/sveltekit-basic tree other tests/packages share.
func TestRealBuild_ExeStrategy_PostBuildSecretScanCatchesRealBunOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real bun build secret-scan check in short mode")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not found on PATH; skipping real build secret-scan check")
	}

	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}
	projectDir := copyFixtureProject(t, fixtureDir)
	injectSecretIntoHealthRoute(t, projectDir)

	deps := realBuildDeps(t)

	req := core.BuildRequest{
		ProjectDir: projectDir,
		Platforms:  []ports.Platform{ports.LinuxAMD64},
		Compile:    core.CompileOptions{Strategy: core.StrategyExe, Env: leakedBuildEnv()},
		Output: core.OutputOptions{
			Mode:        core.OutputTarball,
			TarballPath: filepath.Join(t.TempDir(), "image.tar"),
		},
		SBOM:            core.SBOMOptions{Format: ports.SBOMFormatNone},
		SourceDateEpoch: testEpoch,
	}

	start := time.Now()
	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	elapsed := time.Since(start)
	t.Logf("core.Build (real bun, secret planted via vite define at build time) took %s", elapsed)

	if err == nil {
		t.Fatalf("expected core.Build to fail on a secret baked into REAL bun-produced server output; got a clean build (res=%+v)", res)
	}
	t.Logf("core.Build error (expected): %v", err)

	if !errors.Is(err, core.ErrSecretInlined) {
		t.Errorf("err = %v, want it to wrap core.ErrSecretInlined", err)
	}
	if !strings.Contains(err.Error(), "post-build") {
		t.Errorf("err = %v, want it to identify the post-build stage (so an operator isn't left thinking their SOURCE is the problem)", err)
	}
	// ErrSecretScanIncomplete is the OTHER sentinel runSecretScan can return
	// (a file it could not inspect — oversized or otherwise unreadable). If
	// that fires instead of ErrSecretInlined, the scanner did not actually
	// evaluate the planted secret; per mem:self_review_checklist rows 39/47,
	// that is a false negative masquerading as a caught secret and must not
	// be conflated with detection.
	if errors.Is(err, core.ErrSecretScanIncomplete) {
		t.Errorf("err = %v: scan reported a file it could NOT inspect rather than detecting the secret — this is exactly the false-negative shape Lessons.md warns about, not a genuine detection", err)
	}
}

// TestRealBuild_ExeStrategy_CleanRealBunOutputStillPublishes is the mirror
// case: wiring a real secretguard.Adapter into a real bun build must not
// turn a genuinely clean project into a false positive. Without this, the
// detection test above would prove nothing about precision — a scanner that
// flags everything would also "pass" it.
func TestRealBuild_ExeStrategy_CleanRealBunOutputStillPublishes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real bun build secret-scan check in short mode")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not found on PATH; skipping real build secret-scan check")
	}

	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}
	// Deliberately NOT calling injectSecretIntoHealthRoute: this fixture copy
	// stays exactly what testdata/fixtures/sveltekit-basic ships.
	projectDir := copyFixtureProject(t, fixtureDir)

	deps := realBuildDeps(t)

	req := core.BuildRequest{
		ProjectDir: projectDir,
		Platforms:  []ports.Platform{ports.LinuxAMD64},
		Compile:    core.CompileOptions{Strategy: core.StrategyExe},
		Output: core.OutputOptions{
			Mode:        core.OutputTarball,
			TarballPath: filepath.Join(t.TempDir(), "image.tar"),
		},
		SBOM:            core.SBOMOptions{Format: ports.SBOMFormatNone},
		SourceDateEpoch: testEpoch,
	}

	start := time.Now()
	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	elapsed := time.Since(start)
	t.Logf("core.Build (real bun, clean project) took %s", elapsed)

	if err != nil {
		t.Fatalf("expected a clean real bun build to publish successfully with SecretGuard wired in, got: %v", err)
	}
	if res.Image.Digest.String() == "" {
		t.Errorf("expected a non-empty image digest, got empty")
	}
}

// TestRealBuild_ExeStrategy_CompiledBinaryDirectScanIsANoOp is a diagnostic,
// not a regression guard: it empirically checks the option
// docs/roadmap/supply-chain.yaml's exe-secret-scan-gap explicitly rejected
// ("Scan the compiled binary directly") against a genuine artifact, rather
// than the documented reasoning alone. It drives bunexec.Compiler directly
// (Prepare then Compile) — bypassing core.Build's pre-compile secret-scan
// gate on purpose — so the secret survives into a REAL `bun build --compile`
// executable, then runs the REAL secretguard.Adapter directly against the
// directory holding that executable.
//
// Expected (and asserted) outcome: the scan finds nothing, because
// secretguard's looksBinary NUL-byte sniff (guard.go) skips the compiled
// executable outright — binary content was never scannable text to begin
// with. That is NOT a bug: production never asks secretguard to scan a
// compiled exe (postBuildScanDirs only ever hands it the pre-compile
// bundled-JS tree). This test exists so that limitation rests on a real
// compiled artifact instead of an assumption. If this test starts failing
// (i.e. the scan starts reporting Passed=false), secretguard's binary
// handling has changed and supply-chain.yaml's documented limitation and
// this test's premise need to be revisited together.
func TestRealBuild_ExeStrategy_CompiledBinaryDirectScanIsANoOp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real bun compile diagnostic in short mode")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not found on PATH; skipping real bun compile diagnostic")
	}

	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}
	projectDir := copyFixtureProject(t, fixtureDir)
	injectSecretIntoHealthRoute(t, projectDir)

	logger := testLogger()
	compiler := bunexec.NewCompiler(logger)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	prep, err := compiler.Prepare(ctx, ports.PrepareRequest{
		Strategy:        ports.StrategyExe,
		ProjectDir:      projectDir,
		SourceDateEpoch: testEpoch,
		Env:             leakedBuildEnv(),
	})
	if err != nil {
		t.Fatalf("Prepare against secret-planted fixture failed: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "app-linux-amd64")
	start := time.Now()
	artifact, err := compiler.Compile(ctx, ports.CompileRequest{
		ProjectDir:      projectDir,
		EntrypointPath:  prep.EntrypointPath,
		Platform:        ports.LinuxAMD64,
		OutputPath:      outPath,
		SourceDateEpoch: testEpoch,
		Minify:          true,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Compile against secret-planted fixture failed: %v", err)
	}
	t.Logf("real `bun build --compile` produced %s (%d bytes, sha256=%s) in %s", artifact.Path, artifact.Size, artifact.SHA256, elapsed)

	binBytes, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatalf("read compiled binary: %v", err)
	}
	if !bytes.Contains(binBytes, []byte(leakedGoogleAPIKey)) {
		t.Fatalf("genuinely compiled binary does not contain the planted secret's literal bytes; this diagnostic needs a shape where the secret survives compilation verbatim to be meaningful — got a %d byte binary with no match", len(binBytes))
	}
	t.Logf("confirmed: the planted secret's literal bytes ARE present in the genuine compiled binary at %s", artifact.Path)

	guard := secretguard.NewAdapter()
	res, err := guard.ScanDirectory(ctx, ports.SecretScanRequest{ProjectDir: filepath.Dir(artifact.Path)})
	if err != nil {
		t.Fatalf("ScanDirectory against the directory holding the real compiled binary: %v", err)
	}
	t.Logf("direct secretguard scan of the directory holding the real compiled binary: passed=%v findings=%d skipped=%d",
		res.Passed, len(res.Matches), len(res.Skipped))

	if !res.Passed {
		t.Errorf("expected scanning the real compiled binary directly to be a silent no-op (Passed=true, binary content skipped by looksBinary's NUL-byte sniff), got Passed=false findings=%v skipped=%v — secretguard's binary handling appears to have changed; supply-chain.yaml's exe-secret-scan-gap decision and limitations note this test's premise",
			res.Matches, res.Skipped)
	}
}
