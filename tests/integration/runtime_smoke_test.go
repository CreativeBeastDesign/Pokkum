package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/baseimage"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunruntime"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/nativeinspect"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/staticserver"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/supervisor"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestRuntimeSmoke_LayeredStrategy_BootsAndServes closes this repo's single
// largest test-coverage gap, per Lessons.md's 2026-08-17 entry: every other
// test in this package proves layer *structure* (tar members, byte-for-byte
// determinism, golden manifests) but nothing ever actually boots a produced
// image and asks whether the thing inside it runs. That gap is exactly what
// let every --strategy=layered image this codebase ever built ship without
// its own entrypoint (/app/server/index.js packaged as a *subdirectory* of
// what a real @sveltejs/adapter-node build emits it as a *sibling* of) —
// every structural/golden test passed throughout; the bug was found only by
// manually extracting a real packaged layer and running `bun index.js`
// against it. mem:self_review_checklist row 17 exists because of that
// incident and says packaged output must be proven to execute, not merely
// to exist with the right bytes. This test is the automated, permanent
// version of that manual step: it drives the real pipeline end to end
// (real bun build, real bunexec.Compiler, real packager, the real embedded
// pokkum-init supervisor, a real network-resolved distroless base image),
// loads the resulting image into a real container runtime, runs it, and
// polls pokkum-init's real /healthz and /readyz probe endpoints plus the
// SvelteKit app's own port until they answer for real.
//
// Deliberately real, not mocked, in three places other real-bun tests in
// this package leave mocked:
//   - BaseImages: internal/adapters/baseimage.NewResolver, resolving the
//     real gcr.io/distroless/cc-debian12:nonroot default over the network.
//     TestRealBuildIsReproducibleAcrossRuns and
//     TestRealBuild_StrategyLayered_PrerenderedRoute both fake this (a
//     bare ca-certificates.crt layer with no libc) because neither test
//     cares whether the base actually boots anything; this one does, so it
//     needs a base that genuinely provides glibc for the embedded Bun
//     binary to link against (see baseimage's own package doc comment on
//     "the libc check").
//   - Supervisor: internal/adapters/supervisor.New, the real embedded
//     pokkum-init ELF (committed pre-built at
//     internal/adapters/supervisor/bin/pokkum-init-linux-*.zst, see the
//     Makefile's `supervisor` target) rather than mockSupervisorProvider's
//     placeholder bytes, which are not a valid executable and could never
//     run as PID 1.
//   - The published artifact is actually run: `docker load` (or `podman
//     load`) the produced OCI archive, `docker run` it, and poll its ports
//     from outside the container, exactly the step no other test performs.
//
// Isolation from shared fixture state: ProjectDir points at a scratch copy
// of testdata/fixtures/sveltekit-adapter-node (see copyFixtureProject),
// with node_modules symlinked rather than copied. This matters specifically
// because a *real* BaseImageResolver — unlike every mock in this package —
// writes/updates a pokkum.lock file next to ProjectDir on first resolve
// (internal/adapters/baseimage/resolver.go's lockfile-pinning block); running
// this test directly against the checked-in fixture directory would leave
// that lockfile (and this test's own build scratch: .svelte-kit/, .pokkum/,
// build/) behind in a directory three other tests/agents also touch.
//
// Gating (must skip cleanly, never fail, when the local/CI environment can't
// support it):
//   - testing.Short(): skipped, like every other real-bun test here.
//   - `bun` not on PATH, or the fixture's node_modules not installed: skipped,
//     matching TestRealBuild_StrategyLayered_PrerenderedRoute's convention.
//   - no container runtime (`docker`, falling back to `podman`) on PATH, or
//     found but its daemon/service isn't reachable: skipped with a clear
//     message — this is the new dependency this test adds over every other
//     test in the package.
//   - no reachable network path to the base image registry (gcr.io:443):
//     skipped — resolving a real base image needs it, unlike every other
//     real-bun test here, which fakes BaseImages specifically to avoid it.
//   - a stale pinned Bun runtime checksum, or missing fixture deps discovered
//     only once core.Build actually runs: skipped, matching
//     TestRealBuild_StrategyLayered_PrerenderedRoute's existing tolerance for
//     both, since neither is this test's own regression target.
//
// Every one of those gates goes through smokeGateSkipf, not t.Skip directly:
// where the environment guarantees the preconditions (CI on ubuntu-latest, which
// ships Docker and installs Bun and the fixture deps in the same job), setting
// POKKUM_REQUIRE_RUNTIME_SMOKE=1 turns each of them into a hard failure naming
// the precondition. A SKIP there is not "not applicable", it is the silent loss
// of this repo's only boot coverage while the step still reports ok — see
// runtime_smoke_gate_test.go and mem:self_review_checklist rows 39 and 47.
func TestRuntimeSmoke_LayeredStrategy_BootsAndServes(t *testing.T) {
	if testing.Short() {
		smokeGateSkipf(t, "skipping real bun+docker runtime smoke test in short mode")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		smokeGateSkipf(t, "bun not found on PATH: %v", err)
	}

	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-adapter-node"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixtureDir, "node_modules")); statErr != nil {
		smokeGateSkipf(t, "fixture dependencies not installed (run `bun install` in %s): %v", fixtureDir, statErr)
	}

	runtimeBin, ok := requireContainerRuntime(t)
	if !ok {
		return // requireContainerRuntime already called smokeGateSkipf.
	}

	requireNetworkPathTo(t, "gcr.io:443")

	projectDir := copyFixtureProject(t, fixtureDir)
	// The fixture has no production dependencies of its own, so without this
	// the image ships no /app/node_modules at all and this test cannot say
	// anything about whether that root is attested — see
	// runtime_smoke_nodemodules_test.go's package note and the b439e6b
	// post-mortem in Lessons.md.
	injectProductionDependency(t, projectDir)

	tarballPath := filepath.Join(t.TempDir(), "runtime-smoke.tar")
	repo := "pokkum.local/runtime-smoke"
	tag := uniqueName("smoke")
	imageRef := repo + ":" + tag

	logger := testLogger()
	deps := core.Deps{
		Compiler: bunexec.NewCompiler(logger),
		BaseImages: baseimage.NewResolver(logger,
			baseimage.WithCosignSigner(cosign.NewSigner(logger)),
			baseimage.WithKeylessVerifier(sigstore.NewVerifier(logger))),
		Supervisor:      supervisor.New(logger),
		Packager:        packager.NewPackager(logger),
		BunRuntime:      bunruntime.NewResolver("", nil),
		Tarballs:        registry.NewAdapter(logger),
		NativeInspector: nativeinspect.NewClosuredAdapter(),
		Logger:          logger,
		Version:         "0.1.0-runtime-smoke-test",
	}

	req := core.BuildRequest{
		ProjectDir: projectDir,
		Repo:       repo,
		Tags:       []string{tag},
		Platforms:  []ports.Platform{ports.LocalPlatform()},
		Compile:    core.CompileOptions{Strategy: core.StrategyLayered},
		Output: core.OutputOptions{
			Mode:        core.OutputTarball,
			TarballPath: tarballPath,
		},
		SBOM: core.SBOMOptions{Format: ports.SBOMFormatNone},
		BaseImage: core.BaseImageOptions{
			// Resolving the real base is the point (see the "libc check" note
			// above); verifying its keyless Sigstore signature is not what
			// this test exists to guard and would add Fulcio/Rekor network
			// calls unrelated to the regression this test targets.
			NoVerifyBase: true,
		},
		SourceDateEpoch: testEpoch,
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "no such file or directory") && strings.Contains(err.Error(), "node_modules") {
			smokeGateSkipf(t, "fixture dependencies not installed (run `bun install` in %s): %v", fixtureDir, err)
		}
		if strings.Contains(err.Error(), "bunruntime:") && strings.Contains(err.Error(), "checksum mismatch") {
			// See TestRealBuild_StrategyLayered_PrerenderedRoute's identical
			// tolerance: a stale pinned Bun release checksum is a real,
			// pre-existing, unrelated supply-chain pin issue, not something
			// this test's own regression guard (does the packaged image
			// boot?) should be judged on.
			smokeGateSkipf(t, "bun runtime checksum pin appears stale, unrelated to this test's regression guard: %v", err)
		}
		if isNetworkError(err) {
			smokeGateSkipf(t, "base image resolution could not reach the network: %v", err)
		}
		t.Fatalf("core.Build failed for a real --strategy=layered build: %v", err)
	}
	if res.Image.IsIndex {
		t.Fatalf("single-platform request produced an index; want a single manifest")
	}

	loadImageIntoRuntime(t, runtimeBin, tarballPath, imageRef)
	containerName := runContainerAndAssertServes(t, runtimeBin, imageRef)
	// Serving is necessary but not sufficient: build time and runtime also
	// agree when neither side sees any node_modules. Assert what was actually
	// covered.
	assertNodeModulesAttestedAtRuntime(t, tarballPath, containerLogs(t, runtimeBin, containerName))
}

// requireContainerRuntime finds a usable container runtime CLI (docker,
// falling back to podman) with a reachable daemon/service, or calls smokeGateSkipf
// with a clear reason and returns ("", false). Callers must return
// immediately when ok is false.
func requireContainerRuntime(t *testing.T) (string, bool) {
	t.Helper()
	for _, bin := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(bin); err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, bin, "info").CombinedOutput(); err != nil {
			t.Logf("%s found on PATH but its daemon/service is not reachable: %v\n%s", bin, err, out)
			continue
		}
		return bin, true
	}
	smokeGateSkipf(t, "no reachable container runtime (docker or podman) found")
	return "", false
}

// requireNetworkPathTo skips the test with a clear message if addr cannot be
// dialed within a short timeout. Resolving a real base image (unlike every
// other real-bun test in this package, which fakes BaseImageResolver
// specifically to avoid this) needs an actual network path to the registry;
// this is a fast, explicit precondition check rather than letting core.Build
// fail deep inside base image resolution with a less obvious error.
func requireNetworkPathTo(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		smokeGateSkipf(t, "no network path to %s (needed to resolve the real base image): %v", addr, err)
		return
	}
	_ = conn.Close()
}

// isNetworkError reports whether err looks like it came from a network
// operation failing (DNS, connection refused/reset, timeout) rather than
// from a genuine build defect. Used only to decide whether a core.Build
// failure should skip (environment can't reach the network right now) or
// fail (something is actually broken) — deliberately conservative: it only
// matches well-known net package error shapes, never a bare substring of
// the error text that a real bug could also produce.
func isNetworkError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := err.Error()
	for _, sub := range []string{"no such host", "connection refused", "network is unreachable", "i/o timeout", "TLS handshake timeout"} {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}

// copyFixtureProject and its skipDirNames table used to live here, since this
// was the first test in the package to need them. They moved to
// harness_test.go (2026-08-19, the "isolate every real-build test from the
// shared testdata/fixtures tree" pass) once TestFixtureDrivenE2E_Static,
// TestFixtureDrivenE2E_Static_SPAFallback, TestFixtureDrivenE2E_AllStrategies,
// TestRealBuildIsReproducibleAcrossRuns, and
// TestRealBuild_StrategyLayered_PrerenderedRoute all needed the same helper —
// see harness_test.go's doc comment on copyFixtureProject for why an isolated
// scratch copy per test, rather than building against the fixture in place,
// is required at all.

// uniqueName returns a name unique enough that parallel or repeated
// invocations of this test (or a stale leftover from a previous crashed
// run) cannot collide. It is used for the image tag and the container name,
// neither of which is part of any produced image's bytes or digest — this
// is test-harness bookkeeping, not a build timestamp, so using the real
// clock here does not conflict with the zero-clock-access invariant that
// applies to adapters.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, os.Getpid(), time.Now().UnixNano())
}

// loadImageIntoRuntime loads the OCI archive at tarballPath into the given
// container runtime (docker/podman load), which understands the legacy
// docker-save-compatible format go-containerregistry's tarball writer
// produces — the same format `docker load` has always consumed, so no
// intermediate conversion is needed between pokkum's --tarball output and a
// real runtime. imageRef is only used for the log line; the tags actually
// registered come from what was baked into the archive itself.
func loadImageIntoRuntime(t *testing.T, runtimeBin, tarballPath, imageRef string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, runtimeBin, "load", "-i", tarballPath).CombinedOutput()

	// Registered unconditionally, before the error check: a `load` that
	// fails partway (e.g. disk pressure while unpacking one of several
	// tagged layers) could still leave something registered under imageRef,
	// and rmi on a name the daemon never actually has is a harmless no-op
	// (logged, not fatal) — see the identical reasoning, empirically
	// confirmed for `docker run`, on runContainerAndAssertServes's cleanup.
	t.Cleanup(func() {
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rmCancel()
		if out, err := exec.CommandContext(rmCtx, runtimeBin, "rmi", "-f", imageRef).CombinedOutput(); err != nil {
			t.Logf("cleanup: %s rmi -f %s: %v\n%s", runtimeBin, imageRef, err, out)
		}
	})

	if err != nil {
		t.Fatalf("%s load -i %s: %v\n%s", runtimeBin, tarballPath, err, out)
	}
	t.Logf("%s load: %s", runtimeBin, strings.TrimSpace(string(out)))
}

// runContainerAndAssertServes runs imageRef as a detached container with all
// exposed ports published to random host ports, polls pokkum-init's real
// /healthz and /readyz probe endpoints (see supervisor/cmd/pokkum-init's
// probe.go/config.go: POKKUM_PROBE_PORT, default 8081) until both answer
// 200, and additionally polls the application's own port (PORT, default
// 3000) and asserts the real SvelteKit app answers with its actual homepage
// content, not just that *something* is listening on it.
func runContainerAndAssertServes(t *testing.T, runtimeBin, imageRef string) string {
	t.Helper()
	name := uniqueName("pokkum-runtime-smoke")

	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()
	runArgs := []string{"run", "-d", "--name", name, "-P", imageRef}
	runOut, runErr := exec.CommandContext(runCtx, runtimeBin, runArgs...).CombinedOutput()

	// `docker run -d --name X` allocates and registers the named container
	// before it ever attempts to start it, so a failed run (e.g. a
	// nonexistent entrypoint binary — exactly the deliberate-breakage shape
	// this test was verified against) still leaves a real container object
	// behind. Cleanup is therefore registered unconditionally, before the
	// error check below, not only on the success path — the reverse order
	// was tried during development and confirmed to leak a stopped
	// container on every run failure (see this test's own doc comment /
	// this task's report for the incident).
	t.Cleanup(func() {
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rmCancel()
		if out, err := exec.CommandContext(rmCtx, runtimeBin, "logs", name).CombinedOutput(); err == nil {
			t.Logf("container %s logs:\n%s", name, out)
		}
		if out, err := exec.CommandContext(rmCtx, runtimeBin, "rm", "-f", name).CombinedOutput(); err != nil {
			t.Logf("cleanup: %s rm -f %s: %v\n%s", runtimeBin, name, err, out)
		}
	})

	if runErr != nil {
		t.Fatalf("%s %s: %v\n%s", runtimeBin, strings.Join(runArgs, " "), runErr, runOut)
	}

	// A container that refused to start (the startup attestation failing is the
	// live example: pokkum-init exits 125 before the child ever runs) surfaces
	// at hostPortFor as "no public port '8081/tcp' published", which names the
	// wrong cause entirely. Ask the runtime directly first, so the failure
	// report is the container's own exit code and logs.
	assertContainerRunning(t, runtimeBin, name)

	probePort := hostPortFor(t, runtimeBin, name, ports.DefaultProbePort)
	appPort := hostPortFor(t, runtimeBin, name, ports.DefaultPort)

	const grace = 30 * time.Second
	healthzOK := pollHTTP200(fmt.Sprintf("http://127.0.0.1:%d/healthz", probePort), grace)
	readyzOK := pollHTTP200(fmt.Sprintf("http://127.0.0.1:%d/readyz", probePort), grace)
	appOK, appBody := pollHTTP200Body(fmt.Sprintf("http://127.0.0.1:%d/", appPort), grace)

	if !healthzOK {
		t.Errorf("pokkum-init /healthz never returned 200 within %s (probe host port %d)", grace, probePort)
	}
	if !readyzOK {
		t.Errorf("pokkum-init /readyz never returned 200 within %s (probe host port %d) — the packaged app likely never started listening on its own port (see Lessons.md's missing-entrypoint incident)", grace, probePort)
	}
	if !appOK {
		t.Errorf("the packaged SvelteKit app never answered 200 on its own port within %s (app host port %d)", grace, appPort)
	} else if !strings.Contains(appBody, "Welcome to SvelteKit") {
		t.Errorf("app responded 200 but body did not contain the fixture's known homepage text; got %d bytes: %.200q", len(appBody), appBody)
	}

	if t.Failed() {
		t.Fatalf("runtime smoke test failed: the packaged image did not boot and serve correctly")
	}
	return name
}

// assertContainerRunning fails, with the container's exit code and its full
// logs, if the container is not running — the shape a bricked image takes
// (pokkum-init refusing to exec its child) rather than a serving failure.
func assertContainerRunning(t *testing.T, runtimeBin, containerName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, runtimeBin, "inspect",
		"-f", "{{.State.Running}} {{.State.ExitCode}}", containerName).CombinedOutput()
	if err != nil {
		t.Fatalf("%s inspect %s: %v\n%s", runtimeBin, containerName, err, out)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		t.Fatalf("unexpected %s inspect output %q", runtimeBin, out)
	}
	if fields[0] != "true" {
		t.Fatalf("the container is not running (exit code %s): the packaged image refused to start rather than "+
			"failing to serve. Its own logs say why:\n%s", fields[1], containerLogs(t, runtimeBin, containerName))
	}
}

// containerLogs returns everything the container has written to stdout/stderr
// so far. pokkum-init's startup log lines (notably "startup attestation
// verified ... files=N") are the only place the runtime side of the attestation
// is observable from outside the container, and asserting on them is what makes
// "the image booted" distinguishable from "the image booted with the control
// covering nothing".
func containerLogs(t *testing.T, runtimeBin, containerName string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, runtimeBin, "logs", containerName).CombinedOutput()
	if err != nil {
		t.Fatalf("%s logs %s: %v\n%s", runtimeBin, containerName, err, out)
	}
	return string(out)
}

// hostPortFor asks the runtime which host port a container's containerPort
// was published to, retrying briefly since `docker run -d` can return before
// port publishing metadata is queryable.
func hostPortFor(t *testing.T, runtimeBin, containerName string, containerPort int) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	var lastOut []byte
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, runtimeBin, "port", containerName, strconv.Itoa(containerPort)+"/tcp").CombinedOutput()
		cancel()
		if err == nil {
			if port, ok := parseHostPort(string(out)); ok {
				return port
			}
			lastErr = fmt.Errorf("could not parse host port from %q", out)
		} else {
			lastErr = err
			lastOut = out
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s port %s %d/tcp: %v\n%s", runtimeBin, containerName, containerPort, lastErr, lastOut)
	return 0
}

// parseHostPort extracts the port number from `docker port` output, which
// looks like "0.0.0.0:54321\n" (and, on some daemons, a second "[::]:54321"
// line) for a single published container port.
func parseHostPort(out string) (int, bool) {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		idx := strings.LastIndex(line, ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(line[idx+1:])
		if err != nil {
			continue
		}
		return port, true
	}
	return 0, false
}

// pollHTTP200 polls url until it returns HTTP 200 or the deadline elapses.
func pollHTTP200(url string, timeout time.Duration) bool {
	ok, _ := pollHTTP200Body(url, timeout)
	return ok
}

// pollHTTP200Body polls url until it returns HTTP 200 (returning its body
// too) or the deadline elapses.
func pollHTTP200Body(url string, timeout time.Duration) (bool, string) {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for {
		resp, err := client.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK {
				return true, string(body)
			}
		}
		if time.Now().After(deadline) {
			return false, ""
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// TestRuntimeSmoke_StaticStrategy_BootsAndServes is
// TestRuntimeSmoke_LayeredStrategy_BootsAndServes's --strategy=static
// counterpart. It exists for the identical reason: every other
// static-strategy test in this package (see
// tests/integration/static_e2e_test.go's TestFixtureDrivenE2E_Static) drives
// a synthetic staticFixtureCompiler that fabricates
// .svelte-kit/output/{client,prerendered} directly in the exact shape
// internal/adapters/bunexec.Compiler's StrategyStatic branch and
// internal/core/pipeline.go's AppPrerenderedDir assignment already assume,
// and never exercises internal/adapters/bunexec.Compiler.Preflight at all
// (the mock ports.Compiler doubles used everywhere else implement Preflight
// as a stub that always succeeds); and supervisor/cmd/pokkum-static's own
// tests (main_test.go, integration_test.go) only ever exercise its HTTP
// handlers in-process via httptest — nothing in this repo, before this
// test, ever ran the real pokkum-static main()/ListenAndServe path or a
// real Compiler.Preflight against a real adapter-static project. All three
// real, pre-existing production bugs below were undetectable by any other
// test in this repo for exactly that reason.
//
// This test drives the real pipeline end to end against
// testdata/fixtures/sveltekit-static (a genuine `sv create ... --add
// sveltekit-adapter=adapter:static` project, built with the real
// @sveltejs/adapter-static, not a hand-fabricated fixture), loads the
// resulting image into a real container runtime, and polls the real
// pokkum-static PID-1 process's probe endpoints and served content — the
// same shape as the layered test. It is deliberately written to assert the
// CORRECT contract throughout (Preflight accepts a real, correctly
// configured adapter-static project; pokkum-static listens on its
// configured PORT/POKKUM_PROBE_PORT; the prerendered homepage answers 200
// with its real content, same as the layered test's own homepage
// assertion) rather than to assert whatever the pipeline currently happens
// to do. As of this writing it fails, and it fails for three real,
// independent, pre-existing production bugs, not a bug in this test —
// listed here in the order a caller would actually hit them:
//
//  1. internal/adapters/bunexec.Compiler.Preflight (compiler.go) hard-codes
//     its adapter check to `@jesterkit/exe-sveltekit` or
//     `@sveltejs/adapter-node` (the package-level `adapterPackage` const)
//     regardless of strategy — it never even sees the strategy in the first
//     place: ports.PreflightRequest (ports/compiler.go) has no Strategy
//     field at all, and internal/core/pipeline.go's Preflight call site
//     (the `deps.Compiler.Preflight(ctx, ports.PreflightRequest{...})`
//     block) does not pass req.Compile.Strategy through. Prepare's own
//     adapter check (checkEffectiveAdapter, a few hundred lines later in
//     the same file) IS correctly strategy-aware — it computes
//     `targetAdapter = "@sveltejs/adapter-static"` for
//     req.Strategy.ApplyStatic() — but Preflight runs first and rejects the
//     build before Prepare's correct logic ever gets a chance to run. Net
//     effect: --strategy=static fails on EVERY real project whose
//     package.json does not also happen to list adapter-node or
//     exe-sveltekit, which is to say every real, correctly-configured
//     adapter-static-only project, i.e. the entire intended use case of
//     this strategy. Confirmed empirically: `core.Build` against this
//     fixture fails immediately with "bunexec: preflight ...:
//     @jesterkit/exe-sveltekit or @sveltejs/adapter-node is not configured
//     in svelte.config.js or listed in package.json ...: sveltekit adapter
//     missing" — before a single subprocess for the actual build ever
//     runs. This is the failure this test currently stops at (see the
//     errors.Is(err, core.ErrAdapterMissing) branch below).
//  2. Even past that (confirmed by temporarily working around bug #1 in a
//     throwaway scratch copy so a real image could actually be built and
//     run — never in the committed fixture or any production code):
//     supervisor/cmd/pokkum-static/main.go constructs both http.Server
//     values (the content server `svc` and the probe server `probe`)
//     without ever setting their Addr field. The local `addr :=
//     net.JoinHostPort("", strconv.Itoa(cfg.Port))` (and the ProbePort
//     equivalent) is computed and logged but never assigned to
//     svc.Addr/probe.Addr, so `svc.ListenAndServe()` and
//     `probe.ListenAndServe()` both fall back to net/http's documented
//     default address, ":http" (port 80) — completely ignoring
//     cfg.Port/cfg.ProbePort (3000/8081) and the PORT/POKKUM_PROBE_PORT env
//     vars the image config sets. Confirmed directly (no container
//     involved): running the real built pokkum-static binary locally with
//     PORT=3000 POKKUM_PROBE_PORT=8081 set, `lsof -p <pid>` shows it bound
//     to `*:80`, not 3000 or 8081; curl to 3000 and 8081 both fail to
//     connect, curl to 80 answers 200. Whichever server wins the race for
//     port 80 "succeeds" (its "listening" log line claims the configured
//     port, which is simply false); the other permanently fails to bind
//     with "address already in use" and never serves anything at all —
//     in practice this means /healthz and /readyz are frequently the ones
//     that never come up, not just content. No test in this repo (see this
//     doc comment's opening paragraph) ever called the real
//     ListenAndServe path to catch this. This bug alone makes every
//     --strategy=static image built by this codebase non-functional
//     through any documented port, independent of bugs #1 and #3.
//  3. Even past bugs #1 and #2 (verified two ways: driving `bun run build`
//     directly against this fixture outside core.Build, matching exactly
//     what Prepare would invoke; and, once bug #2's local Addr fix was
//     applied only in a throwaway local binary copy for verification,
//     confirmed again through a real container): internal/adapters/bunexec.Compiler's
//     StrategyStatic branch reads SvelteKit's pre-adapter internal build
//     staging directory (<ProjectDir>/.svelte-kit/output), not
//     @sveltejs/adapter-static's own final adapt() output
//     (<ProjectDir>/build). That staging directory nests EVERY prerendered
//     route, including the site root, under prerendered/pages/<route>.html
//     — a real build of this fixture with @sveltejs/kit 2.70.3 produces
//     .svelte-kit/output/prerendered/pages/index.html and .../pages/about.html;
//     there is no top-level .svelte-kit/output/prerendered/index.html at
//     all. internal/core/pipeline.go packages that directory verbatim as
//     /app/prerendered, preserving the pages/ nesting, but
//     supervisor/cmd/pokkum-static's server (server.go's tryServe) does a
//     flat, literal URL-path-to-file lookup with no knowledge of that
//     nesting: a request for "/" looks for /app/prerendered/index.html
//     directly and never tries /app/prerendered/pages/index.html.
//     @sveltejs/adapter-static's OWN adapt() step — precisely the step
//     Pokkum bypasses — is what flattens prerendered/pages/*.html into a
//     flat build/*.html tree matching pokkum-static's flat lookup contract;
//     confirmed by inspecting this fixture's real `build/` output, which
//     has build/index.html and build/about.html at the top level, no
//     pages/ subdirectory at all. Net effect, once bugs #1 and #2 above are
//     both fixed: pokkum-static would serve /healthz, /readyz and any
//     flatly-staged client asset (e.g. /robots.txt) correctly, but 404 on
//     every prerendered page, including the site root. This is the
//     static-strategy analogue of Lessons.md's 2026-08-17 "every
//     --strategy=layered image was missing its own entrypoint" entry.
//
// All three bugs are exactly the class mem:self_review_checklist rows 12
// and 17 exist to catch, undetected until now because the only
// static-strategy test fixture in this repo (staticFixtureCompiler in
// static_e2e_test.go) is a mock ports.Compiler that implements Preflight as
// an unconditional success and fabricates .svelte-kit/output/{client,prerendered}
// already flattened, and because nothing anywhere in this repo ever called
// pokkum-static's real main() end to end. This test is deliberately left
// asserting the correct, fully-working contract rather than any bug's
// current (broken) behavior — a smoke test that quietly encodes a known
// bug as "expected output" would defeat its own purpose the next time a
// similar bug is introduced elsewhere in the packaging path. As written, it
// currently fails at core.Build on bug #1 and therefore never reaches the
// container-boot assertions below through its own execution; those
// assertions remain here, written against the correct contract, for the
// day bugs #1 and #2 are fixed elsewhere and bug #3 becomes the next (and,
// as far as this investigation found, the last) thing blocking a real
// --strategy=static image from actually serving its site.
//
// Gating mirrors TestRuntimeSmoke_LayeredStrategy_BootsAndServes exactly,
// with two differences: the fixture is testdata/fixtures/sveltekit-static,
// and the network reachability check targets cgr.dev (the static
// strategy's default base, cgr.dev/chainguard/static) instead of gcr.io.
func TestRuntimeSmoke_StaticStrategy_BootsAndServes(t *testing.T) {
	if testing.Short() {
		smokeGateSkipf(t, "skipping real bun+docker runtime smoke test in short mode")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		smokeGateSkipf(t, "bun not found on PATH: %v", err)
	}

	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-static"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixtureDir, "node_modules")); statErr != nil {
		smokeGateSkipf(t, "fixture dependencies not installed (run `bun install` in %s): %v", fixtureDir, statErr)
	}

	runtimeBin, ok := requireContainerRuntime(t)
	if !ok {
		return // requireContainerRuntime already called smokeGateSkipf.
	}

	requireNetworkPathTo(t, "cgr.dev:443")

	projectDir := copyFixtureProject(t, fixtureDir)

	tarballPath := filepath.Join(t.TempDir(), "runtime-smoke-static.tar")
	repo := "pokkum.local/runtime-smoke-static"
	tag := uniqueName("smoke")
	imageRef := repo + ":" + tag

	logger := testLogger()
	deps := core.Deps{
		Compiler: bunexec.NewCompiler(logger),
		BaseImages: baseimage.NewResolver(logger,
			baseimage.WithCosignSigner(cosign.NewSigner(logger)),
			baseimage.WithKeylessVerifier(sigstore.NewVerifier(logger))),
		// Deps.validate requires Supervisor unconditionally, even though a
		// static build never calls Supervisor.Binary/Version — see
		// static_e2e_test.go's TestFixtureDrivenE2E_Static's identical note.
		Supervisor:      supervisor.New(logger),
		StaticServer:    staticserver.New(logger),
		Packager:        packager.NewPackager(logger),
		Tarballs:        registry.NewAdapter(logger),
		NativeInspector: nativeinspect.NewClosuredAdapter(),
		Logger:          logger,
		Version:         "0.1.0-runtime-smoke-test",
		// BunRuntime is intentionally left nil: internal/core/pipeline.go's
		// fan-out only dereferences it for StrategyLayered.
	}

	req := core.BuildRequest{
		ProjectDir: projectDir,
		Repo:       repo,
		Tags:       []string{tag},
		Platforms:  []ports.Platform{ports.LocalPlatform()},
		Compile:    core.CompileOptions{Strategy: core.StrategyStatic},
		Output: core.OutputOptions{
			Mode:        core.OutputTarball,
			TarballPath: tarballPath,
		},
		SBOM: core.SBOMOptions{Format: ports.SBOMFormatNone},
		BaseImage: core.BaseImageOptions{
			// Mirrors what cmd/pokkum's --static flag reconciliation computes
			// (core.Build itself does no strategy-specific preset defaulting).
			Preset: core.BaseImageChainguard,
			Ref:    core.StaticBaseRef,
			// See TestRuntimeSmoke_LayeredStrategy_BootsAndServes's identical
			// field: verifying the base's keyless Sigstore signature is not
			// what this test exists to guard and would add unrelated
			// Fulcio/Rekor network calls.
			NoVerifyBase: true,
		},
		SourceDateEpoch: testEpoch,
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "no such file or directory") && strings.Contains(err.Error(), "node_modules") {
			smokeGateSkipf(t, "fixture dependencies not installed (run `bun install` in %s): %v", fixtureDir, err)
		}
		if isNetworkError(err) {
			smokeGateSkipf(t, "base image resolution could not reach the network: %v", err)
		}
		if errors.Is(err, core.ErrAdapterMissing) {
			// Bug #1 from this test's own doc comment: bunexec.Compiler.Preflight
			// hard-codes its adapter check to exe-sveltekit/adapter-node and never
			// sees req.Compile.Strategy at all, so a real, correctly-configured
			// adapter-static project (exactly what this fixture is) always fails
			// here. This is a genuine production bug, not an environment/skip
			// condition, so it must fail loudly (t.Fatalf), not skip.
			t.Fatalf("core.Build failed for a real --strategy=static build at the Preflight stage: %v\n"+
				"this is bug #1 from this test's own doc comment: bunexec.Compiler.Preflight is not "+
				"strategy-aware (ports.PreflightRequest has no Strategy field) and unconditionally "+
				"requires @jesterkit/exe-sveltekit or @sveltejs/adapter-node, rejecting every real, "+
				"correctly-configured @sveltejs/adapter-static project — see internal/adapters/bunexec/compiler.go's "+
				"Preflight and internal/core/pipeline.go's ports.PreflightRequest{...} call site", err)
		}
		t.Fatalf("core.Build failed for a real --strategy=static build: %v", err)
	}
	if res.Image.IsIndex {
		t.Fatalf("single-platform request produced an index; want a single manifest")
	}

	loadImageIntoRuntime(t, runtimeBin, tarballPath, imageRef)
	runContainerAndAssertServesStatic(t, runtimeBin, imageRef, projectDir)
}

// runContainerAndAssertServesStatic is runContainerAndAssertServes's
// --strategy=static counterpart. It differs in three ways beyond the obvious
// repo/name bookkeeping:
//
//  1. It additionally asserts /robots.txt (a static/ asset copied verbatim
//     into the client root, unaffected by the prerendered/pages/ nesting bug
//     documented on TestRuntimeSmoke_StaticStrategy_BootsAndServes) to prove
//     pokkum-static's flat file lookup genuinely works for content that
//     really is staged flat — isolating "the server can serve a file at
//     all" from "the packager staged this particular file at the path the
//     server looks for", which is exactly where the known bug lives.
//  2. It discovers one real immutable client chunk's URL by globbing
//     projectDir's real build output rather than hard-coding a
//     content-hashed filename, then asserts pokkum-static negotiates a real
//     Brotli precompression sidecar for it (Accept-Encoding: br =>
//     Content-Encoding: br) — static-server-specific behavior the layered
//     strategy has no equivalent of.
//  3. Root ("/") and "/about" are asserted with the same rigor as the
//     layered test's homepage check — deliberately not softened into a skip
//     or a "known failure" tolerance, per this test's own doc comment.
func runContainerAndAssertServesStatic(t *testing.T, runtimeBin, imageRef, projectDir string) string {
	t.Helper()
	name := uniqueName("pokkum-runtime-smoke-static")

	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()
	runArgs := []string{"run", "-d", "--name", name, "-P", imageRef}
	runOut, runErr := exec.CommandContext(runCtx, runtimeBin, runArgs...).CombinedOutput()

	// See runContainerAndAssertServes's identical comment: cleanup is
	// registered unconditionally, before the error check, because
	// `docker run -d --name X` allocates and registers the named container
	// before it ever attempts to start it.
	t.Cleanup(func() {
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rmCancel()
		if out, err := exec.CommandContext(rmCtx, runtimeBin, "logs", name).CombinedOutput(); err == nil {
			t.Logf("container %s logs:\n%s", name, out)
		}
		if out, err := exec.CommandContext(rmCtx, runtimeBin, "rm", "-f", name).CombinedOutput(); err != nil {
			t.Logf("cleanup: %s rm -f %s: %v\n%s", runtimeBin, name, err, out)
		}
	})

	if runErr != nil {
		t.Fatalf("%s %s: %v\n%s", runtimeBin, strings.Join(runArgs, " "), runErr, runOut)
	}

	// A container that refused to start (the startup attestation failing is the
	// live example: pokkum-init exits 125 before the child ever runs) surfaces
	// at hostPortFor as "no public port '8081/tcp' published", which names the
	// wrong cause entirely. Ask the runtime directly first, so the failure
	// report is the container's own exit code and logs.
	assertContainerRunning(t, runtimeBin, name)

	probePort := hostPortFor(t, runtimeBin, name, ports.DefaultProbePort)
	appPort := hostPortFor(t, runtimeBin, name, ports.DefaultPort)

	const grace = 30 * time.Second
	healthzOK := pollHTTP200(fmt.Sprintf("http://127.0.0.1:%d/healthz", probePort), grace)
	readyzOK := pollHTTP200(fmt.Sprintf("http://127.0.0.1:%d/readyz", probePort), grace)
	// robots.txt is staged flat at the client root (client/robots.txt =>
	// /app/client/robots.txt) and requested at a flat URL, so it is
	// unaffected by the prerendered/pages/ nesting bug documented above —
	// this poll also doubles as "wait until the content server itself is
	// actually accepting connections" before the one-shot checks below.
	robotsOK, robotsBody := pollHTTP200Body(fmt.Sprintf("http://127.0.0.1:%d/robots.txt", appPort), grace)

	if !healthzOK {
		t.Errorf("pokkum-static /healthz never returned 200 within %s (probe host port %d)", grace, probePort)
	}
	if !readyzOK {
		t.Errorf("pokkum-static /readyz never returned 200 within %s (probe host port %d)", grace, probePort)
	}
	if !robotsOK {
		t.Errorf("pokkum-static never served /robots.txt (a flat static/ asset, unaffected by the known prerendered/pages/ nesting bug) within %s (app host port %d) — this would mean the static content server itself is not working at all, a more basic failure than the known prerendered-routing bug", grace, appPort)
	} else if !strings.Contains(robotsBody, "User-agent") {
		t.Errorf("/robots.txt responded 200 but body did not contain the fixture's known content; got %d bytes: %.200q", len(robotsBody), robotsBody)
	}

	appOK, appBody := pollHTTP200Body(fmt.Sprintf("http://127.0.0.1:%d/", appPort), grace)
	if !appOK {
		t.Errorf("the packaged SvelteKit static site never answered 200 at / within %s (app host port %d) — see TestRuntimeSmoke_StaticStrategy_BootsAndServes's own doc comment: this is a real, pre-existing production bug (bunexec.Compiler's StrategyStatic branch stages prerendered pages under prerendered/pages/<route>.html, but pokkum-static's server does a flat URL-to-file lookup that never looks there), not a defect in this test", grace, appPort)
	} else if !strings.Contains(appBody, "Welcome to SvelteKit") {
		t.Errorf("/ responded 200 but body did not contain the fixture's known homepage text; got %d bytes: %.200q", len(appBody), appBody)
	}

	aboutOK, aboutBody := pollHTTP200Body(fmt.Sprintf("http://127.0.0.1:%d/about", appPort), grace)
	if !aboutOK {
		t.Errorf("the packaged SvelteKit static site never answered 200 at /about within %s (app host port %d) — same known root cause as the / failure above", grace, appPort)
	} else if !strings.Contains(aboutBody, "This page is prerendered") {
		t.Errorf("/about responded 200 but body did not contain the fixture's known content; got %d bytes: %.200q", len(aboutBody), aboutBody)
	}

	// Precompression negotiation: discover a real immutable client chunk by
	// globbing the real build output (never a hard-coded content hash, which
	// changes on every SvelteKit/Vite version bump) and confirm pokkum-static
	// negotiates the real .br sidecar precompressutils generated for it. This
	// path is a flat, non-prerendered client asset, so it is unaffected by
	// the known routing bug above.
	if chunkRel, ok := findImmutableChunk(projectDir); ok {
		chunkURL := fmt.Sprintf("http://127.0.0.1:%d/%s", appPort, chunkRel)
		resp, err := getWithHeader(chunkURL, "Accept-Encoding", "br")
		if err != nil {
			t.Errorf("GET %s with Accept-Encoding: br: %v", chunkURL, err)
		} else {
			if resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s with Accept-Encoding: br: status = %d, want 200", chunkURL, resp.StatusCode)
			}
			if got := resp.Header.Get("Content-Encoding"); got != "br" {
				t.Errorf("GET %s with Accept-Encoding: br: Content-Encoding = %q, want %q (a real precompressed Brotli sidecar should have been negotiated)", chunkURL, got, "br")
			}
			resp.Body.Close()
		}
	} else {
		t.Logf("no immutable client chunk found under %s to test precompression negotiation against; skipping that assertion", projectDir)
	}

	if t.Failed() {
		t.Fatalf("static runtime smoke test failed: see individual assertion failures above")
	}
	return name
}

// findImmutableChunk globs projectDir's real build output for one
// content-hashed client chunk under _app/immutable/chunks/ (SvelteKit's own
// hashed-asset convention) and returns its URL path relative to the served
// client root (e.g. "_app/immutable/chunks/abcd1234.js"), so the
// precompression negotiation check above never has to hard-code a
// version-specific hash.
func findImmutableChunk(projectDir string) (string, bool) {
	clientDir := filepath.Join(projectDir, ".svelte-kit", "output", "client")
	matches, err := filepath.Glob(filepath.Join(clientDir, "_app", "immutable", "chunks", "*.js"))
	if err != nil || len(matches) == 0 {
		return "", false
	}
	// Prefer the largest chunk: precompressutils only produces sidecars for
	// files >= 64 bytes with genuine compressible repetition (see
	// internal/adapters/precompressutils.PrecompressFile) — the largest
	// bundled chunk is the safest bet to actually clear that bar.
	var best string
	var bestSize int64
	for _, m := range matches {
		fi, statErr := os.Stat(m)
		if statErr != nil {
			continue
		}
		if fi.Size() > bestSize {
			best = m
			bestSize = fi.Size()
		}
	}
	if best == "" {
		return "", false
	}
	rel, err := filepath.Rel(clientDir, best)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// getWithHeader issues a single GET request to url with one extra header
// set, without following redirects or polling — used only after the
// server's liveness has already been confirmed by an earlier poll in this
// file.
func getWithHeader(url, headerKey, headerValue string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(headerKey, headerValue)
	client := &http.Client{Timeout: 5 * time.Second}
	return client.Do(req) //nolint:bodyclose // caller closes resp.Body explicitly.
}
