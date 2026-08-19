package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// captureLogger returns a *slog.Logger that writes to an in-memory buffer
// (at every level, unlike discardLogger which filters everything below
// Error+1) so tests can assert on the exact warning text logged.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// writePackageJSONWithDevScript writes a minimal package.json declaring a
// "dev" script into dir. The script's actual content is irrelevant to every
// test using it: --no-container invokes flags.bunBinary directly with
// ["run", "dev"], so it is bun (or the fake standing in for it), not this
// script text, that determines what actually executes.
func writePackageJSONWithDevScript(t *testing.T, dir string) {
	t.Helper()
	content := `{"name":"fixture","scripts":{"dev":"vite dev"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
}

// fakeLocalRunner is a devLocalRunner test double mirroring
// fakeContainerRunner's shape (dev_watch_test.go): it never shells out to a
// real bun/dev-server process, records how many times it was invoked and
// with which absDir, and optionally blocks until its context is cancelled
// to exercise the clean-shutdown-on-cancel path.
type fakeLocalRunner struct {
	blockUntilCancel bool
	err              error

	calls  atomic.Int32
	gotDir atomic.Pointer[string]
}

func (f *fakeLocalRunner) Run(ctx context.Context, _ *slog.Logger, _ *devFlags, absDir string) error {
	f.calls.Add(1)
	dir := absDir
	f.gotDir.Store(&dir)
	if f.blockUntilCancel {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.err
}

// --- validateDevFlags -------------------------------------------------

func TestValidateDevFlags(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(cmd *cobra.Command)
		wantErr []string // substrings expected in the error; nil means no error
	}{
		{
			name: "container-parity mode ignores every flag, including --debug",
			setup: func(cmd *cobra.Command) {
				must(t, cmd.Flags().Set("debug", "true"))
				must(t, cmd.Flags().Set("platform", "linux/amd64"))
				must(t, cmd.Flags().Set("bun-version", "1.2.3"))
			},
			wantErr: nil,
		},
		{
			name: "no-container rejects --debug",
			setup: func(cmd *cobra.Command) {
				must(t, cmd.Flags().Set("no-container", "true"))
				must(t, cmd.Flags().Set("debug", "true"))
			},
			wantErr: []string{"--debug"},
		},
		{
			name: "no-container rejects --platform",
			setup: func(cmd *cobra.Command) {
				must(t, cmd.Flags().Set("no-container", "true"))
				must(t, cmd.Flags().Set("platform", "linux/amd64"))
			},
			wantErr: []string{"--platform"},
		},
		{
			name: "no-container rejects --bun-version",
			setup: func(cmd *cobra.Command) {
				must(t, cmd.Flags().Set("no-container", "true"))
				must(t, cmd.Flags().Set("bun-version", "1.2.3"))
			},
			wantErr: []string{"--bun-version"},
		},
		{
			name: "no-container rejects --bun-variant",
			setup: func(cmd *cobra.Command) {
				must(t, cmd.Flags().Set("no-container", "true"))
				must(t, cmd.Flags().Set("bun-variant", "baseline"))
			},
			wantErr: []string{"--bun-variant"},
		},
		{
			name: "no-container rejects multiple incompatible flags at once",
			setup: func(cmd *cobra.Command) {
				must(t, cmd.Flags().Set("no-container", "true"))
				must(t, cmd.Flags().Set("debug", "true"))
				must(t, cmd.Flags().Set("platform", "linux/amd64"))
			},
			wantErr: []string{"--debug", "--platform"},
		},
		{
			name: "no-container alone is valid",
			setup: func(cmd *cobra.Command) {
				must(t, cmd.Flags().Set("no-container", "true"))
			},
			wantErr: nil,
		},
		{
			name: "no-container tolerates --port, --watch, --env-file, --bun-binary",
			setup: func(cmd *cobra.Command) {
				must(t, cmd.Flags().Set("no-container", "true"))
				must(t, cmd.Flags().Set("port", "4000:4000"))
				must(t, cmd.Flags().Set("watch", "false"))
				must(t, cmd.Flags().Set("env-file", "/tmp/dev.env"))
				must(t, cmd.Flags().Set("bun-binary", "/usr/local/bin/bun"))
			},
			wantErr: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newDevCommand(context.Background(), discardLogger())
			tc.setup(cmd)

			err := validateDevFlags(cmd)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %v, got nil", tc.wantErr)
			}
			if !errors.Is(err, core.ErrInvalidRequest) {
				t.Fatalf("expected error to wrap core.ErrInvalidRequest, got: %v", err)
			}
			for _, substr := range tc.wantErr {
				if !strings.Contains(err.Error(), substr) {
					t.Fatalf("error %q does not mention %q", err.Error(), substr)
				}
			}
		})
	}
}

// --- warnIneffectiveNoContainerFlags -----------------------------------

func TestWarnIneffectiveNoContainerFlags(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(cmd *cobra.Command)
		wantContain []string
		wantAbsent  []string
	}{
		{
			name: "container-parity mode never warns",
			setup: func(cmd *cobra.Command) {
				must(t, cmd.Flags().Set("port", "4000:4000"))
				must(t, cmd.Flags().Set("watch", "false"))
			},
			wantAbsent: []string{"--port", "--watch"},
		},
		{
			name: "no-container with defaults warns about nothing",
			setup: func(cmd *cobra.Command) {
				must(t, cmd.Flags().Set("no-container", "true"))
			},
			wantAbsent: []string{"--port", "--watch"},
		},
		{
			name: "no-container with explicit --port warns",
			setup: func(cmd *cobra.Command) {
				must(t, cmd.Flags().Set("no-container", "true"))
				must(t, cmd.Flags().Set("port", "4000:4000"))
			},
			wantContain: []string{"--port"},
			wantAbsent:  []string{"--watch"},
		},
		{
			name: "no-container with explicit --watch warns",
			setup: func(cmd *cobra.Command) {
				must(t, cmd.Flags().Set("no-container", "true"))
				must(t, cmd.Flags().Set("watch", "false"))
			},
			wantContain: []string{"--watch"},
			wantAbsent:  []string{"--port"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newDevCommand(context.Background(), discardLogger())
			tc.setup(cmd)

			logger, buf := captureLogger()
			warnIneffectiveNoContainerFlags(cmd, logger)

			out := buf.String()
			for _, substr := range tc.wantContain {
				if !strings.Contains(out, substr) {
					t.Errorf("expected log output to contain %q, got: %s", substr, out)
				}
			}
			for _, substr := range tc.wantAbsent {
				if strings.Contains(out, substr) {
					t.Errorf("expected log output to NOT contain %q, got: %s", substr, out)
				}
			}
		})
	}
}

// --- runDevWithDeps branching -------------------------------------------

// TestRunDevWithDeps_NoContainer_NeverInvokesContainerSeams is the
// load-bearing assertion for --no-container: it must never touch the
// container-parity seams (devContainerRunner, devBuilder) at all. Only the
// devLocalRunner seam may be invoked.
func TestRunDevWithDeps_NoContainer_NeverInvokesContainerSeams(t *testing.T) {
	tempDir := t.TempDir()

	containerRunner := &fakeContainerRunner{}
	containerBuilder := &fakeDevBuilder{}
	localRunner := &fakeLocalRunner{}

	flags := &devFlags{noContainer: true}
	if err := runDevWithDeps(context.Background(), discardLogger(), flags, []string{tempDir}, containerRunner, containerBuilder, localRunner); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if containerRunner.Calls() != 0 {
		t.Fatalf("container runner invoked %d times, want 0 -- --no-container must never touch it", containerRunner.Calls())
	}
	if containerBuilder.Calls() != 0 {
		t.Fatalf("container builder invoked %d times, want 0 -- --no-container must never touch it", containerBuilder.Calls())
	}
	if got := localRunner.calls.Load(); got != 1 {
		t.Fatalf("local runner invoked %d times, want 1", got)
	}
	if got := localRunner.gotDir.Load(); got == nil || *got == "" {
		t.Fatalf("local runner did not receive a project directory")
	}
}

// TestRunDevWithDeps_NoContainer_CleanShutdownOnContextCancel proves the
// --no-container path propagates outer context cancellation as
// context.Canceled, the same clean-shutdown contract watchAndRunDevContainer
// already provides for container-parity mode.
func TestRunDevWithDeps_NoContainer_CleanShutdownOnContextCancel(t *testing.T) {
	tempDir := t.TempDir()

	containerRunner := &fakeContainerRunner{}
	containerBuilder := &fakeDevBuilder{}
	localRunner := &fakeLocalRunner{blockUntilCancel: true}

	flags := &devFlags{noContainer: true}
	ctx, cancel := context.WithCancel(context.Background())

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- runDevWithDeps(ctx, discardLogger(), flags, []string{tempDir}, containerRunner, containerBuilder, localRunner)
	}()

	waitForCondition(t, 15*time.Second, func() bool {
		return localRunner.calls.Load() == 1
	}, "local dev server to start")

	cancel()

	select {
	case err := <-doneCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runDevWithDeps did not return after context cancellation")
	}

	if containerRunner.Calls() != 0 || containerBuilder.Calls() != 0 {
		t.Fatalf("container seams were touched during --no-container shutdown")
	}
}

// TestRunDevWithDeps_DefaultPath_SingleShotUnchanged proves the default
// (container-parity, non-watch) path is unaffected by this change: it still
// builds exactly once and runs the container exactly once, and never touches
// the new local-process seam.
func TestRunDevWithDeps_DefaultPath_SingleShotUnchanged(t *testing.T) {
	tempDir := t.TempDir()

	crashErr := errors.New("container crashed on its own")
	containerRunner := &fakeContainerRunner{selfExitErr: crashErr}
	containerBuilder := &fakeDevBuilder{}
	localRunner := &fakeLocalRunner{}

	flags := &devFlags{watch: false}
	err := runDevWithDeps(context.Background(), discardLogger(), flags, []string{tempDir}, containerRunner, containerBuilder, localRunner)

	if !errors.Is(err, crashErr) {
		t.Fatalf("expected crashErr, got: %v", err)
	}
	if containerBuilder.Calls() != 1 {
		t.Fatalf("expected exactly 1 build, got %d", containerBuilder.Calls())
	}
	if containerRunner.Calls() != 1 {
		t.Fatalf("expected exactly 1 container run, got %d", containerRunner.Calls())
	}
	if got := localRunner.calls.Load(); got != 0 {
		t.Fatalf("local runner should never be invoked on the default path, got %d calls", got)
	}
}

// TestRunDevWithDeps_DefaultPath_WatchLoopUnchanged proves the default
// path's --watch plumbing through runDevWithDeps (repoName construction,
// seam wiring into watchAndRunDevContainer) is unaffected: it must still
// reach watchAndRunDevContainer and behave exactly as
// dev_watch_test.go's TestWatchAndRunDevContainer_ContainerExitsOnItsOwn
// describes case (a).
func TestRunDevWithDeps_DefaultPath_WatchLoopUnchanged(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempDir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	crashErr := errors.New("container crashed on its own")
	containerRunner := &fakeContainerRunner{selfExitErr: crashErr}
	containerBuilder := &fakeDevBuilder{}
	localRunner := &fakeLocalRunner{}

	flags := &devFlags{watch: true}
	err := runDevWithDeps(context.Background(), discardLogger(), flags, []string{tempDir}, containerRunner, containerBuilder, localRunner)

	if !errors.Is(err, crashErr) {
		t.Fatalf("expected crashErr, got: %v", err)
	}
	if containerBuilder.Calls() != 1 {
		t.Fatalf("expected exactly 1 initial build, got %d", containerBuilder.Calls())
	}
	if containerRunner.Calls() != 1 {
		t.Fatalf("expected exactly 1 container run, got %d", containerRunner.Calls())
	}
	if got := localRunner.calls.Load(); got != 0 {
		t.Fatalf("local runner should never be invoked on the default path, got %d calls", got)
	}
}

// --- runLocalDevProcess (real subprocess, fake script -- never real bun) --

func TestRunLocalDevProcess_MissingPackageJSON(t *testing.T) {
	projectDir := t.TempDir()

	err := runLocalDevProcess(context.Background(), discardLogger(), &devFlags{}, projectDir)
	if !errors.Is(err, core.ErrProjectNotFound) {
		t.Fatalf("expected core.ErrProjectNotFound, got: %v", err)
	}
}

func TestRunLocalDevProcess_MissingDevScript(t *testing.T) {
	projectDir := t.TempDir()
	content := `{"name":"fixture","scripts":{"build":"vite build"}}`
	if err := os.WriteFile(filepath.Join(projectDir, "package.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	err := runLocalDevProcess(context.Background(), discardLogger(), &devFlags{}, projectDir)
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Fatalf("expected core.ErrInvalidRequest, got: %v", err)
	}
	if !strings.Contains(err.Error(), `"dev" script`) {
		t.Fatalf("expected error to mention the missing dev script, got: %v", err)
	}
}

func TestRunLocalDevProcess_RunsConfiguredExecutableAndMergesEnvFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake dev-server shell script fixture is POSIX-shell only")
	}

	projectDir := t.TempDir()
	writePackageJSONWithDevScript(t, projectDir)

	outFile := filepath.Join(t.TempDir(), "out.txt")
	scriptPath := filepath.Join(t.TempDir(), "fake-bun")
	script := "#!/bin/sh\necho \"$TEST_ENV_VAR\" > \"" + outFile + "\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bun script: %v", err)
	}

	envFilePath := filepath.Join(t.TempDir(), "dev.env")
	envFileContent := "# a comment, then a blank line\n\nTEST_ENV_VAR=hello_from_envfile\n"
	if err := os.WriteFile(envFilePath, []byte(envFileContent), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	flags := &devFlags{bunBinary: scriptPath, envFile: envFilePath}
	if err := runLocalDevProcess(context.Background(), discardLogger(), flags, projectDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("reading out file: %v", err)
	}
	if want := "hello_from_envfile"; strings.TrimSpace(string(got)) != want {
		t.Fatalf("got %q, want %q", strings.TrimSpace(string(got)), want)
	}
}

// TestRunLocalDevProcess_CleanShutdownOnContextCancel proves the real
// signal-based shutdown logic (SIGINT via cmd.Cancel, ctx.Err() check after
// cmd.Run()) actually works end-to-end against a real (but fully
// test-controlled, non-bun) subprocess -- not just through the
// devLocalRunner fake used by the runDevWithDeps-level shutdown test above.
func TestRunLocalDevProcess_CleanShutdownOnContextCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal-trapping shell script fixture is POSIX-shell only")
	}

	projectDir := t.TempDir()
	writePackageJSONWithDevScript(t, projectDir)

	scriptDir := t.TempDir()
	scriptPath := filepath.Join(scriptDir, "fake-bun")
	startedFile := filepath.Join(scriptDir, "started")
	script := "#!/bin/sh\n" +
		"touch \"" + startedFile + "\"\n" +
		"trap 'exit 0' INT TERM\n" +
		"while true; do sleep 0.05; done\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bun script: %v", err)
	}

	flags := &devFlags{bunBinary: scriptPath}
	ctx, cancel := context.WithCancel(context.Background())

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- runLocalDevProcess(ctx, discardLogger(), flags, projectDir)
	}()

	waitForCondition(t, 15*time.Second, func() bool {
		_, err := os.Stat(startedFile)
		return err == nil
	}, "fake dev server to start")

	cancel()

	select {
	case err := <-doneCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(devGenerationStopTimeout + 2*time.Second):
		t.Fatal("runLocalDevProcess did not return promptly after context cancellation")
	}
}

// --- parseSimpleEnvFile --------------------------------------------------

func TestParseSimpleEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev.env")
	content := "# a comment\n\nFOO=bar\nBAZ=qux with spaces\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	pairs, err := parseSimpleEnvFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"FOO=bar", "BAZ=qux with spaces"}
	if len(pairs) != len(want) {
		t.Fatalf("got %v, want %v", pairs, want)
	}
	for i, w := range want {
		if pairs[i] != w {
			t.Fatalf("pairs[%d] = %q, want %q", i, pairs[i], w)
		}
	}
}

func TestParseSimpleEnvFile_MalformedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev.env")
	if err := os.WriteFile(path, []byte("NOT_A_KEY_VALUE_LINE\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	if _, err := parseSimpleEnvFile(path); err == nil {
		t.Fatal("expected an error for a malformed line, got nil")
	}
}

func TestParseSimpleEnvFile_MissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := parseSimpleEnvFile(filepath.Join(dir, "does-not-exist.env")); err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}

// must fails the test immediately if err is non-nil. Used for flag-setup
// calls in table-driven tests where a Set failure indicates a typo in the
// test itself (an unregistered flag name), not something worth a
// per-case assertion.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected setup error: %v", err)
	}
}
