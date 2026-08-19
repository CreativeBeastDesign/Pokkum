package integration_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// CLI workflow tests: multi-command sequences run against the real built
// binary, in a real project directory, in the order a user runs them.
//
// Why this file exists. Every other test in this package drives Pokkum through
// its Go API. That is the right level for most things, but it left one whole
// class uncovered: whether the commands work *together*. `pokkum init` shipped
// writing `sbom.attach: attestation`, which `pokkum build` refused outright, so
// the first two commands a new user runs were incompatible — and nothing caught
// it, because no test ever ran one command's output into the next. It was found
// by a maintainer typing the two commands.
//
// Unit tests cannot close that gap by construction: they assert the pieces, and
// the failure lived in the seam between two working pieces. So these tests do
// what a person does — run the binary, keep the directory, run the next command
// against what the previous one left behind.
//
// Deliberately fast by default. Nothing here needs Docker, a registry, Bun or
// the network: a `build` that stops at request validation still exercises the
// whole config-load-and-validate path, which is exactly where the bug lived.
// Steps that need a real toolchain are gated on -short and marked as such.

var (
	cliBinOnce sync.Once
	cliBinPath string
	cliBinErr  error
)

// pokkumBinary builds cmd/pokkum once per test run and returns its path.
//
// Built with the same -buildvcs=false the Makefile uses for embedded binaries,
// so a dirty working tree cannot change the output and make these tests behave
// differently between a clean checkout and a work-in-progress one.
func pokkumBinary(t *testing.T) string {
	t.Helper()
	cliBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pokkum-cli-workflow-*")
		if err != nil {
			cliBinErr = err
			return
		}
		bin := filepath.Join(dir, "pokkum")
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/pokkum")
		cmd.Dir = repoRootDir(t)
		if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
			cliBinErr = &cliBuildError{err: buildErr, output: string(out)}
			return
		}
		cliBinPath = bin
	})
	if cliBinErr != nil {
		t.Fatalf("building the pokkum CLI: %v", cliBinErr)
	}
	return cliBinPath
}

type cliBuildError struct {
	err    error
	output string
}

func (e *cliBuildError) Error() string { return e.err.Error() + "\n" + e.output }

// repoRootDir walks up from the test's working directory to the module root, so
// these tests do not depend on being invoked from a particular directory.
func repoRootDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod above the test's working directory")
		}
		dir = parent
	}
}

// cliStep is one command in a workflow.
type cliStep struct {
	// name describes the step in failure output and subtest names.
	name string
	args []string

	// wantErr is whether the command is expected to exit non-zero. Both
	// directions are asserted: a step expected to succeed and failing is as much
	// a finding as the reverse.
	wantErr bool

	// wantOut are substrings that must appear in combined output, and absent are
	// substrings that must not. absent is the load-bearing half for this file's
	// purpose: the reported bug was a *specific* error appearing where a
	// different one belonged, and "build failed" alone cannot tell those apart.
	wantOut []string
	absent  []string

	// check runs after the step against the project directory, for assertions
	// about what the command left on disk for the next command to consume.
	check func(t *testing.T, projectDir, output string)

	// needsToolchain marks a step requiring Bun/Docker/network, skipped in
	// -short mode.
	needsToolchain bool
}

// runWorkflow executes steps in order against one project directory, stopping at
// the first step whose outcome is not what the workflow declared. Stopping is
// deliberate: once a step misbehaves, everything after it is being run against a
// state the workflow never described, so later failures would be noise.
func runWorkflow(t *testing.T, projectDir string, steps []cliStep) {
	t.Helper()
	bin := pokkumBinary(t)

	for i, step := range steps {
		if step.needsToolchain && testing.Short() {
			t.Logf("step %d (%s): skipped in -short mode (needs a real toolchain)", i+1, step.name)
			continue
		}

		args := append([]string{}, step.args...)
		cmd := exec.Command(bin, args...)
		cmd.Dir = projectDir
		// An empty DOCKER_CONFIG keeps the CLI off the host keychain, the same
		// isolation this package's TestMain applies to in-process tests — a
		// credential helper can otherwise block for the full test timeout.
		cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+t.TempDir(), "NO_COLOR=1")
		// No stdin: init only prompts on a TTY, so every workflow here takes the
		// non-interactive path. Prompt behaviour is covered by unit tests against
		// promptInitOptions, which can actually drive it.
		cmd.Stdin = bytes.NewReader(nil)

		done := make(chan struct{})
		var out []byte
		var runErr error
		go func() {
			out, runErr = cmd.CombinedOutput()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Minute):
			_ = cmd.Process.Kill()
			t.Fatalf("step %d (%s): `pokkum %s` did not finish within 3 minutes",
				i+1, step.name, strings.Join(args, " "))
		}
		output := string(out)

		if step.wantErr && runErr == nil {
			t.Fatalf("step %d (%s): `pokkum %s` succeeded but the workflow expects it to fail.\nOutput:\n%s",
				i+1, step.name, strings.Join(args, " "), output)
		}
		if !step.wantErr && runErr != nil {
			t.Fatalf("step %d (%s): `pokkum %s` failed: %v\nOutput:\n%s",
				i+1, step.name, strings.Join(args, " "), runErr, output)
		}
		for _, want := range step.wantOut {
			if !strings.Contains(output, want) {
				t.Fatalf("step %d (%s): output missing %q.\nOutput:\n%s", i+1, step.name, want, output)
			}
		}
		for _, bad := range step.absent {
			if strings.Contains(output, bad) {
				t.Fatalf("step %d (%s): output contains %q, which this workflow asserts must not appear.\nOutput:\n%s",
					i+1, step.name, bad, output)
			}
		}
		if step.check != nil {
			step.check(t, projectDir, output)
		}
	}
}

// newCLIProject creates a minimal project directory. Enough of a SvelteKit shape
// for the config and preflight paths to behave as they would for a real project,
// without needing an install or a build.
func newCLIProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeCLIFile(t, dir, "package.json", `{
  "name": "cli-workflow-fixture",
  "private": true,
  "type": "module",
  "dependencies": { "@sveltejs/kit": "^2.31.0", "svelte": "^5.0.0" }
}`)
	writeCLIFile(t, dir, "vite.config.ts", "import { sveltekit } from '@sveltejs/kit/vite';\nexport default { plugins: [sveltekit()] };\n")
	writeCLIFile(t, dir, "src/routes/+page.svelte", "<h1>hello</h1>\n")
	return dir
}

func writeCLIFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// --- workflows ----------------------------------------------------------

// TestCLIWorkflow_InitThenBuild is the regression test for the reported bug, in
// the form it was reported: run `pokkum init`, then run `pokkum build`.
//
// The failure was not "build broke" but "build rejected init's own output":
//
//	invalid sbom attach mode: sbom attach mode "attestation"
//
// So asserting only that build exits non-zero would have passed while the bug
// was live — this project has no repo configured, so build legitimately fails
// either way. What distinguishes the two is *which* error appears, which is why
// the absent list carries the weight here.
func TestCLIWorkflow_InitThenBuild(t *testing.T) {
	dir := newCLIProject(t)
	runWorkflow(t, dir, []cliStep{
		{
			name:    "init",
			args:    []string{"init", "--defaults"},
			wantOut: []string{"Created .pokkum.yaml"},
			check: func(t *testing.T, projectDir, _ string) {
				body := readCLIFile(t, projectDir, ".pokkum.yaml")
				// Pin the value itself, not just validity: "attach:" being
				// present proves nothing, and the whole bug was one wrong word.
				if !strings.Contains(body, "attach: auto") {
					t.Errorf("generated config must set a valid sbom attach mode, got:\n%s", body)
				}
			},
		},
		{
			name:    "config validate accepts what init wrote",
			args:    []string{"config", "validate"},
			wantOut: []string{"is valid"},
		},
		{
			name:    "build gets past config load and fails only on the missing repo",
			args:    []string{"build"},
			wantErr: true,
			// The genuine, actionable error for a project with no registry.
			wantOut: []string{"destination repository"},
			// Every config-rejection shape. If any of these appears, init wrote
			// something build will not accept — which is the bug, whatever the
			// exit code says.
			absent: []string{
				"invalid sbom attach mode",
				"invalid sbom format",
				"invalid strategy",
				"invalid base",
				"invalid runtime",
				"invalid cache verify mode",
				"unknown field",
				"cannot unmarshal",
			},
		},
	})
}

// TestCLIWorkflow_InitIsIdempotent covers re-running init over an existing
// project, which is what happens when someone runs it again out of habit or a
// script runs it unconditionally. It must not clobber a config the user has
// since edited.
func TestCLIWorkflow_InitIsIdempotent(t *testing.T) {
	dir := newCLIProject(t)
	const marker = "ghcr.io/example/edited-by-hand"
	runWorkflow(t, dir, []cliStep{
		{name: "first init", args: []string{"init", "--defaults"}, wantOut: []string{"Created .pokkum.yaml"}},
		{
			name: "user edits the config",
			args: []string{"--version"}, // no-op step; the edit happens in check
			check: func(t *testing.T, projectDir, _ string) {
				body := readCLIFile(t, projectDir, ".pokkum.yaml")
				body = strings.Replace(body, "repo: \"\"", "repo: "+marker, 1)
				if !strings.Contains(body, marker) {
					// The base config writes an empty repo; if that shape ever
					// changes, append instead of silently testing nothing.
					body += "\ndocker:\n    repo: " + marker + "\n"
				}
				writeCLIFile(t, projectDir, ".pokkum.yaml", body)
			},
		},
		{
			name: "second init leaves the edit intact",
			args: []string{"init", "--defaults"},
			check: func(t *testing.T, projectDir, _ string) {
				if body := readCLIFile(t, projectDir, ".pokkum.yaml"); !strings.Contains(body, marker) {
					t.Errorf("re-running init overwrote a hand-edited config; the edit is gone:\n%s", body)
				}
			},
		},
		{
			name:    "and the edited config is still valid",
			args:    []string{"config", "validate"},
			wantOut: []string{"is valid"},
		},
	})
}

// TestCLIWorkflow_CorruptedConfigFailsClearly checks the other direction: when
// the config really is wrong, the message must name the offending field so the
// user can fix it. A generic "build failed" here would send them to the wrong
// place — which is precisely what made the original bug expensive to diagnose.
func TestCLIWorkflow_CorruptedConfigFailsClearly(t *testing.T) {
	dir := newCLIProject(t)
	runWorkflow(t, dir, []cliStep{
		{name: "init", args: []string{"init", "--defaults"}},
		{
			name: "hand-break the sbom attach mode",
			args: []string{"--version"},
			check: func(t *testing.T, projectDir, _ string) {
				body := readCLIFile(t, projectDir, ".pokkum.yaml")
				broken := strings.Replace(body, "attach: auto", "attach: attestation", 1)
				if broken == body {
					t.Fatal("fixture no longer contains `attach: auto`, so this test would prove nothing")
				}
				writeCLIFile(t, projectDir, ".pokkum.yaml", broken)
			},
		},
		{
			name:    "config validate rejects it, naming the field and the value",
			args:    []string{"config", "validate"},
			wantErr: true,
			wantOut: []string{"sbom attach", "attestation"},
		},
		{
			name:    "build rejects it too, rather than only config validate",
			args:    []string{"build"},
			wantErr: true,
			wantOut: []string{"attestation"},
		},
	})
}

// TestCLIWorkflow_JSONOutputIsMachineReadable covers the scripted path: --output=json
// must emit parseable JSON at every step of a workflow, including the failing
// one. A half-JSON error path is worse than none, because the caller's parse
// failure hides the real message.
func TestCLIWorkflow_JSONOutputIsMachineReadable(t *testing.T) {
	dir := newCLIProject(t)
	assertJSON := func(t *testing.T, _, output string) {
		t.Helper()
		trimmed := strings.TrimSpace(output)
		if trimmed == "" {
			t.Fatal("--output=json produced nothing")
		}
		// The envelope is pretty-printed across several lines, and log output may
		// sit either side of it, so decode one complete value starting at the
		// first brace rather than picking a single line. An earlier line-based
		// version of this helper reported the CLI as emitting invalid JSON when
		// the CLI was fine and the helper was wrong — worth avoiding, since a
		// test that blames the wrong component is worse than no test.
		start := strings.Index(trimmed, "{")
		if start < 0 {
			t.Fatalf("no JSON object found in --output=json output:\n%s", output)
		}
		var payload map[string]any
		if err := json.NewDecoder(strings.NewReader(trimmed[start:])).Decode(&payload); err != nil {
			t.Fatalf("--output=json emitted invalid JSON: %v\nOutput:\n%s", err, output)
		}
		// A well-formed envelope is not enough: it must actually carry the
		// contract scripts depend on.
		for _, key := range []string{"schema_version", "command", "status"} {
			if _, ok := payload[key]; !ok {
				t.Errorf("--output=json envelope is missing %q; scripted callers rely on it.\nOutput:\n%s", key, output)
			}
		}
	}
	runWorkflow(t, dir, []cliStep{
		{name: "init --output=json", args: []string{"init", "--defaults", "--output=json"}, check: assertJSON},
		{name: "config validate --output=json", args: []string{"config", "validate", "--output=json"}, check: assertJSON},
		{name: "config view --output=json", args: []string{"config", "view", "--output=json"}, check: assertJSON},
	})
}

// TestCLIWorkflow_LocalProfileRoundTrip walks the profile path end to end: init
// generates a `local` profile, and selecting it must produce a request the build
// path accepts. A profile is merged over the base config by different code than
// the base itself, so a value valid at the top level and invalid in a profile
// would slip past a base-only check.
func TestCLIWorkflow_LocalProfileRoundTrip(t *testing.T) {
	dir := newCLIProject(t)
	runWorkflow(t, dir, []cliStep{
		{
			name: "init generates a local profile",
			args: []string{"init", "--defaults"},
			check: func(t *testing.T, projectDir, _ string) {
				if body := readCLIFile(t, projectDir, ".pokkum.yaml"); !strings.Contains(body, "local:") {
					t.Errorf("expected a local profile in the generated config:\n%s", body)
				}
			},
		},
		{
			name:    "the generated profile validates",
			args:    []string{"config", "validate"},
			wantOut: []string{"is valid"},
		},
		{
			name: "building with --local gets past config and profile merge",
			args: []string{"build", "--local"},
			// --local targets a daemon rather than a registry, so this can fail
			// for environmental reasons; what must not happen is a config or
			// profile rejection.
			wantErr: true,
			absent: []string{
				"invalid sbom attach mode",
				"invalid sbom format",
				"invalid strategy",
				"invalid output",
				"unknown profile",
				"cannot unmarshal",
			},
		},
	})
}

func readCLIFile(t *testing.T, dir, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(body)
}
