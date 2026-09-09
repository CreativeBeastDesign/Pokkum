package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestInitCommand_CreatesIgnoreAndConfig(t *testing.T) {
	tmpDir := t.TempDir()

	opts := &initOptions{
		dir:    tmpDir,
		output: "json",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runInit(nil, opts)

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error running init: %v", err)
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("init --output=json emitted invalid JSON: %v", err)
	}

	ignorePath := filepath.Join(tmpDir, ".pokkumignore")
	if _, err := os.Stat(ignorePath); os.IsNotExist(err) {
		t.Errorf("expected .pokkumignore to be created at %s", ignorePath)
	}

	configPath := filepath.Join(tmpDir, ports.ConfigFilename)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Errorf("expected %s to be created at %s", ports.ConfigFilename, configPath)
	}
}

func TestInitCommand_InteractivePrompts(t *testing.T) {
	tmpDir := t.TempDir()

	// Prompt order: registry, strategy, [runtime — skipped for static], base,
	// local profile, CVE threshold.
	input := "ghcr.io/acme/my-app\nstatic\nchainguard\ny\nhigh\n"
	opts := &initOptions{
		dir:      tmpDir,
		defaults: false,
		inReader: strings.NewReader(input),
	}

	err := runInit(nil, opts)
	if err != nil {
		t.Fatalf("unexpected error running interactive init: %v", err)
	}

	cfgMgr, err := config.New(tmpDir, nil)
	if err != nil {
		t.Fatalf("config.New failed: %v", err)
	}

	loaded, err := cfgMgr.Load(tmpDir)
	if err != nil {
		t.Fatalf("cfgMgr.Load failed: %v", err)
	}

	if loaded.Docker.Repo != "ghcr.io/acme/my-app" {
		t.Errorf("expected repo ghcr.io/acme/my-app, got %q", loaded.Docker.Repo)
	}
	if loaded.Base != "chainguard" {
		t.Errorf("expected base chainguard, got %q", loaded.Base)
	}
	if loaded.Strategy != "static" {
		t.Errorf("expected strategy static, got %q", loaded.Strategy)
	}
	if loaded.Security.FailOnCVE != "high" {
		t.Errorf("expected FailOnCVE high, got %q", loaded.Security.FailOnCVE)
	}
	if _, ok := loaded.Profiles["local"]; !ok {
		t.Errorf("expected 'local' profile to be configured")
	}
}

func TestInitCommand_PreservesExistingFiles(t *testing.T) {
	tmpDir := t.TempDir()

	ignorePath := filepath.Join(tmpDir, ".pokkumignore")
	_ = os.WriteFile(ignorePath, []byte("# custom ignore\nsecret\n"), 0644)

	configPath := filepath.Join(tmpDir, ports.ConfigFilename)
	_ = os.WriteFile(configPath, []byte("version: 1\nstrategy: static\n"), 0644)

	opts := &initOptions{
		dir:      tmpDir,
		defaults: true,
	}

	err := runInit(nil, opts)
	if err != nil {
		t.Fatalf("unexpected error running init: %v", err)
	}

	ignoreContent, _ := os.ReadFile(ignorePath)
	if !strings.Contains(string(ignoreContent), "secret") {
		t.Errorf("expected existing .pokkumignore to be preserved intact")
	}

	configContent, _ := os.ReadFile(configPath)
	if !strings.Contains(string(configContent), "strategy: static") {
		t.Errorf("expected existing %s to be preserved intact", ports.ConfigFilename)
	}
}

// TestPromptInitOptions_RejectsUnknownChoiceAndReAsks covers the prompt path
// directly, because it cannot be reached through runInit from a test: init only
// prompts when stdin is a TTY, so piping answers to the built binary silently
// skips prompting entirely and every value stays at its default. A "test" that
// piped an invalid answer and observed a valid config would therefore prove
// nothing at all — it would be measuring the default, not the validation.
//
// The bug this guards: the prompts used to accept whatever was typed, verbatim.
// Combined with the prompt offering "chainguard-static" — an unimplemented
// roadmap item, not a preset — anyone picking option 3 got a .pokkum.yaml that
// `pokkum build` refused to start with, a long way from the cause.
func TestPromptInitOptions_RejectsUnknownChoiceAndReAsks(t *testing.T) {
	t.Run("invalid base is re-asked, and the retry is honoured", func(t *testing.T) {
		// repo, strategy, runtime, invalid base, valid base, local, cve
		got := promptForTest(t, "\n\n\nchainguard-static\nchainguard\n\n\n")
		// chainguard proves the retry was consumed; distroless would mean the
		// bad answer merely fell through to the default, which is a different
		// (and untested) behaviour.
		if got.BasePreset != "chainguard" {
			t.Errorf("BasePreset = %q, want %q — the re-asked answer must be honoured", got.BasePreset, "chainguard")
		}
	})

	t.Run("no longer offers a preset that does not exist", func(t *testing.T) {
		// Feeding the removed option as the only answer must not set it.
		got := promptForTest(t, "\n\n\nchainguard-static\n\n\n\n")
		if got.BasePreset == "chainguard-static" {
			t.Error("BasePreset = \"chainguard-static\", which is not a real preset — pokkum build refuses it")
		}
	})

	t.Run("every default is a value the config validator accepts", func(t *testing.T) {
		// All answers empty: the defaults this prompt hands to GenerateDefault
		// must themselves be valid, or an operator who just presses Enter five
		// times gets a broken project.
		got := promptForTest(t, "\n\n\n\n\n\n")
		cfg := configManagerForTest(t).GenerateDefault(got)
		if problems := validateGeneratedConfig(cfg); len(problems) > 0 {
			t.Errorf("pressing Enter through every prompt produced an invalid config: %v", problems)
		}
	})

	t.Run("valid answers are accepted as given", func(t *testing.T) {
		got := promptForTest(t, "ghcr.io/example/app\nstatic\ndistroless-node\nn\nhigh\n")
		if got.BasePreset != "distroless-node" || got.Strategy != "static" || got.FailOnCVE != "high" {
			t.Errorf("valid answers not honoured: base=%q strategy=%q cve=%q", got.BasePreset, got.Strategy, got.FailOnCVE)
		}
		if got.EnableLocalProfile {
			t.Error("answering n to the local-profile prompt must disable it")
		}
		cfg := configManagerForTest(t).GenerateDefault(got)
		if problems := validateGeneratedConfig(cfg); len(problems) > 0 {
			t.Errorf("a config built from valid prompt answers must validate: %v", problems)
		}
	})
}

func configManagerForTest(t *testing.T) *config.Manager {
	t.Helper()
	m, err := config.New(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("config.New: %v", err)
	}
	return m
}

// promptForTest drives promptInitOptions with the same starting defaults
// runInit uses, and a zero-value analysis — the honest "learned nothing about
// this project" case, so these assertions measure the prompts rather than the
// detection.
func promptForTest(t *testing.T, input string) ports.InitConfigOptions {
	t.Helper()
	return promptInitOptions(strings.NewReader(input), ports.InitConfigOptions{
		BasePreset:         string(ports.BaseImageDistroless),
		Strategy:           string(ports.StrategyLayered),
		EnableLocalProfile: true,
	}, projectAnalysis{})
}

// --- Detection-driven init -------------------------------------------------

func initProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestInit_StrategyDefaultFollowsTheScan is the reach assertion for the whole
// feature (mem:self_review_checklist row 27a): drive the real entry point and
// assert an effect that exists only because the scan ran. Reading the
// recommendation out of the written .pokkum.yaml, not out of the analyzer,
// is what proves the two are actually connected.
func TestInit_StrategyDefaultFollowsTheScan(t *testing.T) {
	tests := []struct {
		name         string
		files        map[string]string
		wantStrategy string
	}{
		{
			name: "fully prerenderable project defaults to static",
			files: map[string]string{
				"package.json":            `{"dependencies":{"@sveltejs/kit":"^2.31.0"}}`,
				"src/routes/+layout.ts":   "export const prerender = true;\n",
				"src/routes/+page.svelte": "<h1>hi</h1>\n",
			},
			wantStrategy: string(ports.StrategyStatic),
		},
		{
			name: "a single server endpoint defaults to layered",
			files: map[string]string{
				"package.json":              `{"dependencies":{"@sveltejs/kit":"^2.31.0"}}`,
				"src/routes/+page.svelte":   "<h1>hi</h1>\n",
				"src/routes/api/+server.ts": "export const GET = async () => new Response('x');\n",
			},
			wantStrategy: string(ports.StrategyLayered),
		},
		{
			name: "a remote function defaults to layered",
			files: map[string]string{
				"package.json":            `{"dependencies":{"@sveltejs/kit":"^2.31.0"}}`,
				"src/routes/+layout.ts":   "export const prerender = true;\n",
				"src/routes/+page.svelte": "<h1>hi</h1>\n",
				"src/lib/data.remote.ts":  "import { query } from '$app/server';\nexport const all = query(async () => []);\n",
			},
			wantStrategy: string(ports.StrategyLayered),
		},
		{
			name: "an unscannable project defaults to layered, never static",
			files: map[string]string{
				"package.json": `{"name":"no-routes-here"}`,
			},
			wantStrategy: string(ports.StrategyLayered),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := initProject(t, tc.files)
			if err := runInit(nil, &initOptions{dir: dir, defaults: true}); err != nil {
				t.Fatalf("runInit: %v", err)
			}
			loaded, err := configManagerForTest(t).Load(dir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if loaded.Strategy != tc.wantStrategy {
				t.Errorf("Strategy = %q, want %q", loaded.Strategy, tc.wantStrategy)
			}
		})
	}
}

// TestInit_RuntimeDefaultFollowsTheLockfile: same reach assertion for the
// second detector, including the base image it drags along.
func TestInit_RuntimeDefaultFollowsTheLockfile(t *testing.T) {
	base := map[string]string{
		"src/routes/+page.svelte":   "<h1>hi</h1>\n",
		"src/routes/api/+server.ts": "export const GET = async () => new Response('x');\n",
	}

	t.Run("a node lockfile writes runtime node and the base that carries it", func(t *testing.T) {
		files := map[string]string{"package.json": `{"packageManager":"pnpm@9.1.0"}`}
		for k, v := range base {
			files[k] = v
		}
		dir := initProject(t, files)
		if err := runInit(nil, &initOptions{dir: dir, defaults: true}); err != nil {
			t.Fatalf("runInit: %v", err)
		}
		loaded, err := configManagerForTest(t).Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Runtime != string(ports.RuntimeNode) {
			t.Errorf("Runtime = %q, want %q", loaded.Runtime, ports.RuntimeNode)
		}
		if loaded.Base != string(ports.BaseImageDistrolessNode) {
			t.Errorf("Base = %q, want %q — runtime node takes its Node binary from the base, "+
				"so any other preset produces an image that cannot start", loaded.Base, ports.BaseImageDistrolessNode)
		}
	})

	t.Run("a bun lockfile leaves the default runtime unwritten", func(t *testing.T) {
		files := map[string]string{"package.json": "{}", "bun.lock": "{}"}
		for k, v := range base {
			files[k] = v
		}
		dir := initProject(t, files)
		if err := runInit(nil, &initOptions{dir: dir, defaults: true}); err != nil {
			t.Fatalf("runInit: %v", err)
		}
		loaded, err := configManagerForTest(t).Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Runtime != "" && loaded.Runtime != string(ports.RuntimeBun) {
			t.Errorf("Runtime = %q, want bun or unset", loaded.Runtime)
		}
		if loaded.Base != string(ports.BaseImageDistroless) {
			t.Errorf("Base = %q, want %q", loaded.Base, ports.BaseImageDistroless)
		}
	})
}

// TestInit_NeverEmitsStaticWithNodeRuntime guards the composition core rejects
// but per-field config validation accepts. This is the shape that shipped
// twice from init already: two individually-valid values that do not compose.
func TestInit_NeverEmitsStaticWithNodeRuntime(t *testing.T) {
	// A project whose toolchain says node, but whose routes are fully static —
	// so the two detectors genuinely pull in opposite directions.
	dir := initProject(t, map[string]string{
		"package.json":            `{"packageManager":"pnpm@9.1.0"}`,
		"src/routes/+layout.ts":   "export const prerender = true;\n",
		"src/routes/+page.svelte": "<h1>hi</h1>\n",
	})
	if err := runInit(nil, &initOptions{dir: dir, defaults: true}); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	loaded, err := configManagerForTest(t).Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Strategy == string(ports.StrategyStatic) && loaded.Runtime == string(ports.RuntimeNode) {
		t.Fatalf("wrote strategy=static with runtime=node, which core rejects "+
			"(`--runtime=node supports --strategy=layered only`); got %+v", loaded)
	}
}

// TestInit_EveryEmittableCombinationBuilds is row 44 applied across the whole
// generator surface rather than only its defaults: for every combination of
// strategy, runtime and base preset init can emit, the resulting config must be
// accepted BOTH by `pokkum config validate`'s per-field checks AND by core's
// cross-field validation — the second is where static+node and
// node+runtime-less-base actually get caught.
func TestInit_EveryEmittableCombinationBuilds(t *testing.T) {
	strategies := []string{string(ports.StrategyLayered), string(ports.StrategyStatic)}
	runtimes := []string{"", string(ports.RuntimeBun), string(ports.RuntimeNode)}
	mgr := configManagerForTest(t)

	checked := 0
	for _, strategy := range strategies {
		for _, runtime := range runtimes {
			for _, preset := range initBasePresets() {
				for _, local := range []bool{true, false} {
					name := fmt.Sprintf("%s/%s/%s/local=%v", strategy, orUnset(runtime), preset, local)
					t.Run(name, func(t *testing.T) {
						cfg := mgr.GenerateDefault(ports.InitConfigOptions{
							Repo:               "ghcr.io/example/app",
							Strategy:           strategy,
							Runtime:            runtime,
							BasePreset:         string(preset),
							EnableLocalProfile: local,
						})
						if problems := validateGeneratedConfig(cfg); len(problems) > 0 {
							t.Fatalf("config validate rejected a config init can emit: %v", problems)
						}

						req := core.BuildRequest{
							ProjectDir: t.TempDir(),
							Repo:       cfg.Docker.Repo,
							AppRuntime: ports.AppRuntime(cfg.Runtime),
							Compile:    core.CompileOptions{Strategy: core.BuildStrategy(cfg.Strategy)},
							BaseImage:  core.BaseImageOptions{Preset: core.BaseImagePreset(cfg.Base)},
							Output:     core.OutputOptions{Mode: core.OutputLocal},
						}
						req.Normalize()
						if err := req.Validate(); err != nil &&
							(strings.Contains(err.Error(), "runtime") || strings.Contains(err.Error(), "base preset")) {
							t.Fatalf("core rejected a runtime/base combination init can emit: %v\n"+
								"strategy=%q runtime=%q base=%q", err, cfg.Strategy, cfg.Runtime, cfg.Base)
						}
					})
					checked++
				}
			}
		}
	}
	// Row 47: a loop that enumerated nothing must not read as a clean pass.
	if want := len(strategies) * len(runtimes) * len(initBasePresets()) * 2; checked != want {
		t.Errorf("checked %d combinations, want %d", checked, want)
	}
}

func orUnset(s string) string {
	if s == "" {
		return "unset"
	}
	return s
}

// TestWriteStaticFindings_DistinguishesUnknownFromClean is the row 52 guard at
// the output layer: the two verdicts a user could most easily confuse must not
// print the same thing.
func TestWriteStaticFindings_DistinguishesUnknownFromClean(t *testing.T) {
	var unknown, viable strings.Builder

	writeStaticFindings(&unknown, projectAnalysis{static: sveltekitutils.StaticReport{
		Verdict:      sveltekitutils.StaticUnknown,
		UndecidedWhy: "no routes directory at src/routes",
	}})
	writeStaticFindings(&viable, projectAnalysis{static: sveltekitutils.StaticReport{
		Verdict:      sveltekitutils.StaticViable,
		FilesScanned: 4,
		RoutesDir:    "src/routes",
	}})

	if unknown.String() == viable.String() {
		t.Fatal("an unscannable project and a clean one printed the same text")
	}
	if !strings.Contains(unknown.String(), "Could not check") {
		t.Errorf("unknown output must say it could not check, got:\n%s", unknown.String())
	}
	if !strings.Contains(viable.String(), "4 route sources scanned") {
		t.Errorf("viable output must report how much it actually read, got:\n%s", viable.String())
	}
}

// TestWriteStaticFindings_FlagsTheMissingStaticSetup is the QoL case: static is
// possible, but the project is not set up to build that way, so init says which
// two things are missing rather than silently recommending something that would
// fail.
func TestWriteStaticFindings_FlagsTheMissingStaticSetup(t *testing.T) {
	var out strings.Builder
	writeStaticFindings(&out, projectAnalysis{
		adapterStaticConfigured: false,
		static: sveltekitutils.StaticReport{
			Verdict:               sveltekitutils.StaticViable,
			FilesScanned:          3,
			RoutesDir:             "src/routes",
			RootPrerenderDeclared: false,
		},
	})
	got := out.String()
	for _, want := range []string{"@sveltejs/adapter-static", "export const prerender = true"} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not mention %q; got:\n%s", want, got)
		}
	}

	// The converse: a project that already has both must not be nagged.
	var configured strings.Builder
	writeStaticFindings(&configured, projectAnalysis{
		adapterStaticConfigured: true,
		static: sveltekitutils.StaticReport{
			Verdict:               sveltekitutils.StaticViable,
			FilesScanned:          3,
			RoutesDir:             "src/routes",
			RootPrerenderDeclared: true,
		},
	})
	if strings.Contains(configured.String(), "you still need") {
		t.Errorf("a fully configured project was told it was missing something:\n%s", configured.String())
	}
}
