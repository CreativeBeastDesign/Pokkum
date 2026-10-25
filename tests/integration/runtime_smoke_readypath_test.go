package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/supervisor"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// The app-controlled readiness routes added to the fixture copy. /api/ready
// answers 503 until /api/ready-toggle is POSTed, standing in for "the database
// is not reachable yet".
const (
	readyRoute = `let ready = false;
globalThis.__pokkumReadyToggle = () => { ready = true; };
export const GET = () => ready ? new Response('ok') : new Response('db: unreachable', { status: 503 });
`
	readyToggleRoute = `export const POST = () => { globalThis.__pokkumReadyToggle?.(); return new Response('toggled'); };
`
)

// TestRuntimeSmoke_AppReadyEndpointControlsReadyz is the end-to-end proof of
// app-controlled readiness: the project's own api/ready route is detected,
// baked into the image as POKKUM_READY_PATH, read by the real pokkum-init,
// and decides /readyz. The decisive phase is the first one: the app port
// serves 200 the whole time, so a TCP-connect readiness check would say
// "ready"; only the app's own answer keeps /readyz at 503.
func TestRuntimeSmoke_AppReadyEndpointControlsReadyz(t *testing.T) {
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
		return
	}
	requireNetworkPathTo(t, "gcr.io:443")

	projectDir := copyFixtureProject(t, fixtureDir)
	for rel, body := range map[string]string{
		"src/routes/api/ready/+server.ts":        readyRoute,
		"src/routes/api/ready-toggle/+server.ts": readyToggleRoute,
	} {
		p := filepath.Join(projectDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The same detection `pokkum build` runs (resolveReadyPath in cmd/pokkum).
	route, err := sveltekitutils.DetectReadyRoute(projectDir)
	if err != nil || route.URLPath != "/api/ready" {
		t.Fatalf("DetectReadyRoute = %+v, %v; want /api/ready", route, err)
	}

	tarballPath := filepath.Join(t.TempDir(), "readypath-smoke.tar")
	repo := "pokkum.local/readypath-smoke"
	tag := uniqueName("ready")
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
		Version:         "0.1.0-readypath-smoke-test",
	}
	req := core.BuildRequest{
		ProjectDir:      projectDir,
		Repo:            repo,
		Tags:            []string{tag},
		Platforms:       []ports.Platform{ports.LocalPlatform()},
		Compile:         core.CompileOptions{Strategy: core.StrategyLayered},
		Output:          core.OutputOptions{Mode: core.OutputTarball, TarballPath: tarballPath},
		SBOM:            core.SBOMOptions{Format: ports.SBOMFormatNone},
		BaseImage:       core.BaseImageOptions{NoVerifyBase: true},
		Runtime:         ports.RuntimeConfig{ReadyPath: route.URLPath},
		SourceDateEpoch: testEpoch,
	}
	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		if isNetworkError(err) {
			smokeGateSkipf(t, "base image resolution could not reach the network: %v", err)
		}
		t.Fatalf("core.Build: %v", err)
	}

	loadImageIntoRuntime(t, runtimeBin, tarballPath, imageRef)
	name := uniqueName("pokkum-readypath-smoke")
	runCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, runErr := exec.CommandContext(runCtx, runtimeBin, "run", "-d", "--name", name, "-P", imageRef).CombinedOutput()
	t.Cleanup(func() {
		ctx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		if logs, err := exec.CommandContext(ctx, runtimeBin, "logs", name).CombinedOutput(); err == nil {
			t.Logf("container %s logs:\n%s", name, logs)
		}
		_ = exec.CommandContext(ctx, runtimeBin, "rm", "-f", name).Run()
	})
	if runErr != nil {
		t.Fatalf("%s run: %v\n%s", runtimeBin, runErr, out)
	}
	assertContainerRunning(t, runtimeBin, name)
	probePort := hostPortFor(t, runtimeBin, name, ports.DefaultProbePort)
	appPort := hostPortFor(t, runtimeBin, name, ports.DefaultPort)
	readyz := fmt.Sprintf("http://127.0.0.1:%d/readyz", probePort)

	// Phase 1: the app is up and serving, its readiness endpoint says no.
	if !pollHTTP200(fmt.Sprintf("http://127.0.0.1:%d/", appPort), 30*time.Second) {
		t.Fatal("the app never served / with 200")
	}
	if !pollHTTP200(fmt.Sprintf("http://127.0.0.1:%d/healthz", probePort), 10*time.Second) {
		t.Fatal("/healthz never returned 200; a failing readiness endpoint must not affect liveness")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		resp, err := client.Get(readyz)
		if err != nil {
			t.Fatalf("GET /readyz: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("/readyz = %d while the app's /api/ready answers 503; readiness is not following the app (a TCP check would say ready)", resp.StatusCode)
		}
	}

	// Phase 2: the dependency "comes back"; readiness follows within a tick.
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/api/ready-toggle", appPort), "application/json", strings.NewReader("{}")) // JSON: SvelteKit CSRF-blocks form content types
	if err != nil {
		t.Fatalf("POST /api/ready-toggle: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/ready-toggle = %d", resp.StatusCode)
	}
	if !pollHTTP200(readyz, 10*time.Second) {
		t.Fatal("/readyz never turned 200 after the app's readiness endpoint started answering 200")
	}

	if logs := containerLogs(t, runtimeBin, name); !strings.Contains(logs, "application not ready") {
		t.Errorf("pokkum-init never logged why the app was not ready; logs:\n%s", logs)
	}
}
