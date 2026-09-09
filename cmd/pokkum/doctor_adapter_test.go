package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// putFakeBunOnPath writes an executable shell script named "bun" into a fresh
// temp directory, prepends that directory to PATH for the duration of the
// test (t.Setenv restores it automatically), and returns nothing — callers
// only need it on PATH. This is a local copy of
// internal/adapters/bunexec/compiler_test.go's identical helper: that
// package's test helpers are not exported for cross-package reuse.
//
// Used so TestCheckSvelteKitAdapter_AgreesWithBuildPreflight's
// correctly-configured fixture drives bunexec.Compiler.Prepare hermetically
// (no dependency on a real `bun` being installed, and no real subprocess
// work) rather than needing a real, working `bun run build` to reach the
// point where checkEffectiveAdapter's PASS verdict is observable.
func putFakeBunOnPath(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake bun shell script fixture is POSIX-shell only")
	}
	dir := t.TempDir()
	bunPath := filepath.Join(dir, "bun")
	content := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(bunPath, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// realSvCreateDefaultViteConfigTS is vite.config.ts exactly as emitted by a
// fresh `bunx sv create --template minimal --types ts --no-add-ons` scaffold
// (sv@0.17.0, @sveltejs/kit@2.63.0 range) — no svelte.config.js at all,
// adapter is @sveltejs/adapter-auto, configured via options passed to the
// sveltekit() plugin (which makes SvelteKit ignore svelte.config.js
// entirely). This is the exact shape the bug this test guards against was
// filed about: `pokkum doctor` reported this project as a "valid SvelteKit
// project" while `pokkum build` refused it outright.
//
// Duplicated (not imported) from
// internal/adapters/bunexec/compiler_test.go's identical constant — that
// package's test helpers are not exported for reuse across packages, and
// bunexec/compiler_test.go's own doc comment on this same fixture notes the
// same tradeoff for its own duplicate of sveltekitutils/project_test.go's
// copy.
const doctorTestRealSvCreateViteConfig = `import adapter from '@sveltejs/adapter-auto';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		sveltekit({
			adapter: adapter()
		})
	]
});
`

const doctorTestPackageJSON = `{
	"name": "sveltekit-basic",
	"devDependencies": {
		"@sveltejs/kit": "^2.63.0",
		"@sveltejs/adapter-auto": "^3.0.0",
		"@sveltejs/adapter-node": "^5.0.0"
	},
	"scripts": {
		"build": "vite build"
	}
}`

// discardTestLogger returns a slog.Logger that writes nowhere, for tests that
// exercise a code path taking a *slog.Logger but don't want its output.
func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newAdapterAutoFixture writes package.json + vite.config.ts (no
// svelte.config.js) matching a real, unconfigured `sv create` scaffold into a
// fresh temp directory and returns its path.
func newAdapterAutoFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(doctorTestPackageJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vite.config.ts"), []byte(doctorTestRealSvCreateViteConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// newAdapterNodeFixture is the same project, correctly configured: the Vite
// config passes @sveltejs/adapter-node instead of adapter-auto, matching what
// `pokkum adopt` (or a manual fix following the build preflight's own
// remediation) would produce.
func newAdapterNodeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(doctorTestPackageJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	viteConfig := strings.ReplaceAll(doctorTestRealSvCreateViteConfig, "@sveltejs/adapter-auto", "@sveltejs/adapter-node")
	if err := os.WriteFile(filepath.Join(dir, "vite.config.ts"), []byte(viteConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestCheckSvelteKitAdapter_FailsOnAdapterAutoFixture is the guard named in
// this task: a project on the unconfigured `sv create` default
// (@sveltejs/adapter-auto only, --strategy=layered's default requires
// @sveltejs/adapter-node) must FAIL doctor's adapter check, not pass it. This
// is the exact bug being fixed — see checkSvelteKitAdapter's doc comment.
func TestCheckSvelteKitAdapter_FailsOnAdapterAutoFixture(t *testing.T) {
	dir := newAdapterAutoFixture(t)

	check := checkSvelteKitAdapter(dir, discardTestLogger())

	if check.Passed {
		t.Fatalf("expected FAIL for an @sveltejs/adapter-auto-only project under the default (layered) strategy, got PASS: %+v", check)
	}
	if !strings.Contains(check.Message, "@sveltejs/adapter-node") {
		t.Errorf("expected message to name the required adapter @sveltejs/adapter-node, got: %s", check.Message)
	}
	if check.Remediation == "" {
		t.Errorf("expected a non-empty remediation for a FAILed adapter check")
	}
	if !strings.Contains(check.Remediation, "bun add -D @sveltejs/adapter-node") {
		t.Errorf("expected remediation to give the exact install command, got: %s", check.Remediation)
	}
}

// TestCheckSvelteKitAdapter_PassesOnCorrectlyConfiguredAdapter is the
// complement of the FAIL test above: a project whose Vite config names the
// strategy's required adapter must PASS.
func TestCheckSvelteKitAdapter_PassesOnCorrectlyConfiguredAdapter(t *testing.T) {
	dir := newAdapterNodeFixture(t)

	check := checkSvelteKitAdapter(dir, discardTestLogger())

	if !check.Passed {
		t.Fatalf("expected PASS for a correctly configured @sveltejs/adapter-node project, got FAIL: %+v", check)
	}
	if !strings.Contains(check.Message, "@sveltejs/adapter-node") {
		t.Errorf("expected message to name the effective adapter, got: %s", check.Message)
	}
}

// TestCheckSvelteKitAdapter_AgreesWithBuildPreflight is the real point of
// this task: doctor's verdict and bunexec.Compiler.Prepare's own
// checkEffectiveAdapter verdict must agree on the same fixture, for both the
// misconfigured and the correctly-configured shape. This is the guard against
// the two implementations drifting apart — the exact bug this whole change
// fixes (doctor passing what build refuses) — and it only has teeth because
// it drives the REAL production Prepare function, not a second call into the
// same shared library function doctor already calls internally.
func TestCheckSvelteKitAdapter_AgreesWithBuildPreflight(t *testing.T) {
	cases := []struct {
		name        string
		newFixture  func(t *testing.T) string
		wantBuildOK bool
	}{
		{"adapter-auto misconfigured", newAdapterAutoFixture, false},
		{"adapter-node correctly configured", newAdapterNodeFixture, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.newFixture(t)

			doctorCheck := checkSvelteKitAdapter(dir, discardTestLogger())

			// A hermetic fake `bun`: only reached when checkEffectiveAdapter's
			// own verdict is PASS (the misconfigured fixture fails before any
			// subprocess is spawned, matching
			// bunexec/compiler_test.go's own
			// TestPrepare_FailsFastWhenViteConfigOverridesAdapter assertion).
			putFakeBunOnPath(t, `
case "$1 $2" in
  "run build")
    mkdir -p build
    touch build/index.js
    echo 'path.join(dir, "prerendered")' > build/handler.js
    exit 0
    ;;
esac
exit 0
`)
			compiler := bunexec.NewCompiler(discardTestLogger())
			_, buildErr := compiler.Prepare(context.Background(), ports.PrepareRequest{
				ProjectDir:      dir,
				Strategy:        ports.StrategyLayered,
				NoInject:        true,
				SourceDateEpoch: time.Unix(0, 0),
			})
			buildOK := !errors.Is(buildErr, core.ErrAdapterMisconfigured)

			if buildOK != tc.wantBuildOK {
				t.Fatalf("[TEST SETUP] expected bunexec.Prepare adapter-misconfiguration verdict %v for fixture %q, got err=%v", tc.wantBuildOK, tc.name, buildErr)
			}

			if doctorCheck.Passed != buildOK {
				t.Fatalf("doctor and build preflight DISAGREE on fixture %q: doctor.Passed=%v, build ok=%v (build err: %v)",
					tc.name, doctorCheck.Passed, buildOK, buildErr)
			}
		})
	}
}

// TestCheckSvelteKitAdapter_CannotDetermineOnUnreadableSvelteConfig proves
// doctor reports neither a clean pass nor a confident "wrong adapter"
// failure when svelte.config.js exists but cannot actually be read — see
// checkSvelteKitAdapter's doc comment on why guessing here would be false
// confidence in either direction.
func TestCheckSvelteKitAdapter_CannotDetermineOnUnreadableSvelteConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores file permissions")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(doctorTestPackageJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "svelte.config.js")
	if err := os.WriteFile(cfgPath, []byte("export default {};"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfgPath, 0o644) })

	check := checkSvelteKitAdapter(dir, discardTestLogger())

	if check.Passed {
		t.Fatalf("expected a non-pass result when svelte.config.js cannot be read, got PASS: %+v", check)
	}
	if strings.Contains(check.Message, "adapter-auto") || strings.Contains(check.Message, "does not reference it") {
		t.Errorf("an unreadable config must not be reported with the same wording as a confirmed wrong-adapter failure, got: %s", check.Message)
	}
	if !strings.Contains(check.Message, "cannot determine") {
		t.Errorf("expected the message to honestly say the adapter cannot be determined, got: %s", check.Message)
	}
}

// TestRunDoctor_WiresAdapterCheck exercises runDoctor itself, not just
// checkSvelteKitAdapter in isolation — mem:self_review_checklist row 13
// (caller-chain coverage): a correctly implemented check that was never
// actually appended to runDoctor's checks slice, or appended with its
// Passed value read backwards into allPassed, would still pass every test
// above while `pokkum doctor` itself kept reporting the adapter-auto project
// clean. This drives the real command entry point and inspects its real
// text output, the same surface an operator actually sees.
func TestRunDoctor_WiresAdapterCheck(t *testing.T) {
	dir := newAdapterAutoFixture(t)
	opts := &doctorOptions{dir: dir, output: "text"}

	oldStdout := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	os.Stdout = w
	runErr := runDoctor(discardTestLogger(), opts)
	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	out := buf.String()

	if runErr == nil {
		t.Errorf("expected runDoctor to return an error for a project with a misconfigured adapter, got nil")
	}
	if !strings.Contains(out, "[✗ FAIL] SvelteKit Adapter:") {
		t.Fatalf("expected runDoctor's text output to include a FAILed \"SvelteKit Adapter\" check line, got:\n%s", out)
	}
	if !strings.Contains(out, "@sveltejs/adapter-node") {
		t.Errorf("expected the FAIL line or its remediation to name the required adapter, got:\n%s", out)
	}
}
