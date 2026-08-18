package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeContainerRunner is a devContainerRunner test double that never shells
// out to a real Docker daemon. It records how many container generations
// were launched and lets a test choose between two behaviors:
//
//   - killErr set: Run blocks until its context is cancelled (simulating a
//     container that keeps running until it is deliberately killed for a
//     rebuild, or until the outer watch loop shuts down), then returns
//     killErr -- the same shape as the real "signal: killed" error
//     runDevContainer produces when its exec.CommandContext is cancelled.
//   - selfExitErr set (or left nil): Run returns immediately without
//     waiting on ctx, simulating a container that exits on its own.
type fakeContainerRunner struct {
	killErr     error
	selfExitErr error

	calls     atomic.Int32
	completed atomic.Int32
}

func (f *fakeContainerRunner) Run(ctx context.Context, _ *slog.Logger, _ *devFlags, _ string) error {
	f.calls.Add(1)
	if f.killErr == nil {
		// Self-exiting mode: return right away, as if the container's own
		// process exited without anyone killing it.
		f.completed.Add(1)
		return f.selfExitErr
	}
	<-ctx.Done()
	f.completed.Add(1)
	return f.killErr
}

func (f *fakeContainerRunner) Calls() int     { return int(f.calls.Load()) }
func (f *fakeContainerRunner) Completed() int { return int(f.completed.Load()) }

// fakeDevBuilder is a devBuilder test double that records invocations
// instead of running the real build pipeline (which needs a real
// SvelteKit project, bun, and a Docker daemon).
type fakeDevBuilder struct {
	mu  sync.Mutex
	n   int
	err error
}

func (f *fakeDevBuilder) Build(_ context.Context, _ *slog.Logger, _ *devFlags, _, _ string) error {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	return f.err
}

func (f *fakeDevBuilder) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// waitForCondition polls cond until it returns true or timeout elapses,
// failing the test on timeout.
func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// assertStillRunning blocks for a short grace window and fails the test if
// watchAndRunDevContainer has already returned by then. This is how the
// regression test actually catches the bug: the buggy implementation
// returns almost immediately after a rebuild (misreading the superseded
// generation's stale exit value), so a bounded wait reliably distinguishes
// "still watching" from "quietly gave up."
func assertStillRunning(t *testing.T, doneCh <-chan error, grace time.Duration) {
	t.Helper()
	select {
	case err := <-doneCh:
		t.Fatalf("watchAndRunDevContainer returned early (want it to still be watching): %v", err)
	case <-time.After(grace):
	}
}

// touchFileAt writes a file with explicit, strictly-increasing mtimes
// (via os.Chtimes) rather than relying on filesystem clock resolution or
// real elapsed wall time -- this keeps the "did the source directory
// change" detection in watchAndRunDevContainer deterministic in tests.
func touchFileAt(t *testing.T, path string, generation int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(fmt.Sprintf("gen-%d", generation)), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	mt := time.Now().Add(time.Duration(generation) * time.Hour)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// TestWatchAndRunDevContainer_SurvivesMultipleRebuilds is the core
// regression test for the "dev --watch dies after one rebuild" bug: it
// drives the loop through two rebuild cycles and asserts the loop is still
// alive and relaunching after each one, then confirms a clean shutdown on
// outer context cancellation.
//
// Against the pre-fix implementation (a single shared, reused cmdErrChan)
// this test fails after the first rebuild: the superseded generation's
// stale "killed" result is read by the next select iteration as if the
// *new* generation had crashed, and the function returns long before the
// second rebuild is ever triggered.
func TestWatchAndRunDevContainer_SurvivesMultipleRebuilds(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	runner := &fakeContainerRunner{killErr: errors.New("signal: killed")}
	builder := &fakeDevBuilder{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- watchAndRunDevContainer(ctx, discardLogger(), &devFlags{}, tempDir, "pokkum.local/test:dev", runner, builder, 10*time.Millisecond)
	}()

	waitForCondition(t, 2*time.Second, func() bool {
		return runner.Calls() == 1
	}, "initial container launch")

	// --- Rebuild cycle #1 ---
	touchFileAt(t, filepath.Join(srcDir, "a.svelte"), 1)
	waitForCondition(t, 2*time.Second, func() bool {
		return builder.Calls() >= 1 && runner.Calls() >= 2
	}, "first rebuild cycle to be detected and a new generation launched")
	assertStillRunning(t, doneCh, 300*time.Millisecond)

	// --- Rebuild cycle #2 ---
	touchFileAt(t, filepath.Join(srcDir, "b.svelte"), 2)
	waitForCondition(t, 2*time.Second, func() bool {
		return builder.Calls() >= 2 && runner.Calls() >= 3
	}, "second rebuild cycle to be detected and a new generation launched")
	assertStillRunning(t, doneCh, 300*time.Millisecond)

	// Clean shutdown: cancelling the outer context must return ctx.Err(),
	// not the last generation's raw exit error.
	cancel()
	select {
	case err := <-doneCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled on shutdown, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watchAndRunDevContainer did not return after outer context cancellation")
	}

	// No orphaned goroutines: every launched runner.Run call must have
	// observed its context being cancelled and returned -- across several
	// rebuild cycles, not just the first.
	waitForCondition(t, 2*time.Second, func() bool {
		return runner.Completed() == runner.Calls()
	}, "all container runner goroutines (across every generation) to finish")

	if builder.Calls() < 2 {
		t.Fatalf("expected at least 2 rebuilds, got %d", builder.Calls())
	}
}

// TestWatchAndRunDevContainer_ContainerExitsOnItsOwn covers case (a): the
// current generation's container exits on its own (not superseded by a
// rebuild, not cancelled by the outer context) -- the loop must report the
// error and stop, without ever attempting a rebuild.
func TestWatchAndRunDevContainer_ContainerExitsOnItsOwn(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	crashErr := errors.New("container crashed on its own")
	runner := &fakeContainerRunner{selfExitErr: crashErr}
	builder := &fakeDevBuilder{}

	err := watchAndRunDevContainer(context.Background(), discardLogger(), &devFlags{}, tempDir, "pokkum.local/test:dev", runner, builder, 10*time.Millisecond)

	if !errors.Is(err, crashErr) {
		t.Fatalf("expected crashErr to be returned, got: %v", err)
	}
	if builder.Calls() != 0 {
		t.Fatalf("expected no rebuild attempts, got %d", builder.Calls())
	}
	if runner.Calls() != 1 {
		t.Fatalf("expected exactly one container generation, got %d", runner.Calls())
	}
}

// TestWatchAndRunDevContainer_OuterContextCancelled covers case (c): the
// outer context is cancelled (user Ctrl-C) before any rebuild ever
// happens -- the loop must shut down cleanly, returning ctx.Err(), without
// ever attempting a rebuild.
func TestWatchAndRunDevContainer_OuterContextCancelled(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	runner := &fakeContainerRunner{killErr: errors.New("signal: killed")}
	builder := &fakeDevBuilder{}

	ctx, cancel := context.WithCancel(context.Background())

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- watchAndRunDevContainer(ctx, discardLogger(), &devFlags{}, tempDir, "pokkum.local/test:dev", runner, builder, 10*time.Millisecond)
	}()

	waitForCondition(t, time.Second, func() bool {
		return runner.Calls() == 1
	}, "initial container launch")

	cancel()

	select {
	case err := <-doneCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchAndRunDevContainer did not return after outer context cancellation")
	}

	if builder.Calls() != 0 {
		t.Fatalf("expected no rebuild to have been triggered, got %d builder calls", builder.Calls())
	}

	waitForCondition(t, time.Second, func() bool {
		return runner.Completed() == runner.Calls()
	}, "the single container runner goroutine to finish")
}
