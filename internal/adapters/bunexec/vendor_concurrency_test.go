package bunexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// The production-dependency install runs concurrently with the SvelteKit
// build. These pin the three properties that concurrency put at risk:
//
//  1. A failing install still fails Prepare, with its own error surfaced, even
//     when the build it ran alongside succeeded — the install is no longer
//     positioned to fail first, so nothing about ordering can be relied on to
//     surface it.
//  2. When both fail, the install's error is the one reported. Before the
//     install was made concurrent it ran to completion first and always won;
//     a scheduling change must not silently change which failure an operator
//     sees.
//  3. Tearing the build down tears the install down with it — no leaked bun
//     process, no leaked goroutine — because the install now outlives most of
//     Prepare's early-return paths.

// layeredSvelteConfig is a svelte.config.js configuring @sveltejs/adapter-node,
// the adapter StrategyLayered requires (checkEffectiveAdapter fails Prepare
// before any of this otherwise).
func layeredSvelteConfig() string {
	return fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-node")
}

// succeedingBuildScript is the fake `bun run build` body that materialises
// exactly what Prepare's layered post-build validation looks for.
const succeedingBuildScript = `set -e
mkdir -p build
printf 'export default {};\n' > build/index.js
cat > build/handler.js <<'EOF'
` + validHandlerJS + `EOF
exit 0
`

func TestPrepare_VendorInstallFailureFailsTheBuildWhenTheViteBuildSucceeds(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, layeredSvelteConfig())
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte(vendorLockA), 0o600); err != nil {
		t.Fatal(err)
	}
	putFakeBunOnPath(t, `
case "$1" in
  install)
    echo "error: lockfile had changes, but lockfile is frozen" >&2
    exit 1
    ;;
esac
`+succeedingBuildScript)

	c := NewCompiler(discardLogger())
	_, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyLayered,
		SourceDateEpoch: time.Unix(0, 0),
	})
	if err == nil {
		t.Fatal("Prepare succeeded despite a failing production-dependency install; the image would ship a server bundle with no dependency tree")
	}
	if !strings.Contains(err.Error(), "bun install --production failed") {
		t.Errorf("err = %v, want the vendor install's own failure surfaced", err)
	}
	if !strings.Contains(err.Error(), "lockfile is frozen") {
		t.Errorf("err = %v, want bun's own output carried through", err)
	}
	// The build genuinely succeeded, so its artifacts are on disk — this is
	// the case where nothing but the joined install result can fail Prepare.
	if _, statErr := os.Stat(filepath.Join(dir, "build", "index.js")); statErr != nil {
		t.Fatalf("test does not exercise the intended case: the fake build did not run (%v)", statErr)
	}
}

func TestPrepare_VendorInstallErrorWinsWhenTheViteBuildAlsoFails(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, layeredSvelteConfig())
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte(vendorLockA), 0o600); err != nil {
		t.Fatal(err)
	}
	putFakeBunOnPath(t, `
case "$1" in
  install)
    echo "error: lockfile had changes, but lockfile is frozen" >&2
    exit 1
    ;;
esac
echo "vite: transform failed" >&2
exit 2`)

	c := NewCompiler(discardLogger())
	_, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyLayered,
		SourceDateEpoch: time.Unix(0, 0),
	})
	if err == nil {
		t.Fatal("Prepare succeeded with both the install and the build failing")
	}
	if !strings.Contains(err.Error(), "lockfile is frozen") {
		t.Errorf("err = %v, want the vendor install's error to win when both failed", err)
	}
	// Nothing that was visible before becomes invisible: the build's own
	// failure is folded in rather than dropped.
	if !strings.Contains(err.Error(), "also failed") {
		t.Errorf("err = %v, want it to also report that the concurrent build failed", err)
	}
}

// TestVendorJob_StopTearsDownARunningInstall drives the exact mechanism
// Prepare's `defer vendor.stop()` relies on. It is asserted here rather than
// only through Prepare because the paths that need it most — a hermetic
// sandbox verification failure, a cmd.Start failure — are the ones a test
// cannot readily provoke, and a cleanup that only works on the path you can
// reach is not cleanup.
func TestVendorJob_StopTearsDownARunningInstall(t *testing.T) {
	dir := t.TempDir()
	writeVendorProject(t, dir, vendorManifestA, vendorLockA)

	markers := t.TempDir()
	started := filepath.Join(markers, "started")
	finished := filepath.Join(markers, "finished")
	// The marker is touched by a *grandchild* (a backgrounded subshell), not by
	// the bun script itself. That distinction is the whole test: killing only
	// the direct child would suppress the marker for free and prove nothing,
	// because the dead shell is the thing that would have touched it. A
	// grandchild survives a plain kill and touches the marker anyway, so this
	// fixture can tell a process-group teardown from a single-process one —
	// which is exactly what `bun install` does when it forks helpers.
	putFakeBunOnPath(t, `
case "$1" in
  install)
    touch `+started+`
    ( sleep 3; touch `+finished+` ) &
    wait
    ;;
esac
exit 0`)

	goroutinesBefore := runtime.NumGoroutine()

	job := startVendorInstall(context.Background(), dir, false, discLogger())
	waitForFile(t, started, 10*time.Second)

	stopped := make(chan struct{})
	go func() {
		job.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("stop() did not return: the install goroutine leaked")
	}

	// The bun process was killed rather than merely abandoned. If it had been
	// left running, its own `sleep 3` would complete and touch the marker.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(finished); err == nil {
			t.Fatal("the vendor install process survived stop(): it ran to completion after the job was torn down")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// stop() must not merely signal the goroutine, it must *wait* for it: these
	// fields are written by the install goroutine, so reading them here is
	// well-defined only if stop() has already joined it. Under `go test -race`
	// a stop() that returns early turns this line into a reported data race,
	// which is the assertion — the values themselves are incidental.
	if job.err == nil && job.modulesDir == "" {
		t.Log("install produced no modules, as expected for a cancelled run")
	}

	assertGoroutinesSettled(t, goroutinesBefore)
}

// TestPrepare_CancelledContextLeaksNeitherInstallNorGoroutine is the same
// property through Prepare itself, on the path an operator actually takes:
// Ctrl-C during a build.
//
// The fake install used to run for a fixed `sleep 3` and the test polled for
// a "finished" marker never appearing within a window comfortably longer than
// that. That raced two independent clocks against each other: the fixed
// install duration, and however long cancellation took to propagate through
// Prepare's plumbing (context cancellation -> the vendor job's goroutine ->
// cmd.Cancel -> SIGKILL). On a loaded runner the second clock could exceed
// the first even though teardown was completely correct, which is exactly
// what happened on the v1.2.0 macOS CI job (see
// docs/roadmap/testing-infra.yaml#vendor-cancellation-test-flake) — a failure
// with no code change behind it, and a clean pass on an identical re-run.
//
// The fake install below never completes on its own: it blocks forever
// (`while :; do sleep …; done`, unbroken by anything the test writes) so the
// only way it can ever stop is by being killed. That removes the race
// entirely — there is no clock left to lose against — and lets the test
// assert directly on the teardown signal that actually matters: the OS
// process Prepare's vendor job started is gone.
func TestPrepare_CancelledContextLeaksNeitherInstallNorGoroutine(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, layeredSvelteConfig())
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte(vendorLockA), 0o600); err != nil {
		t.Fatal(err)
	}

	markers := t.TempDir()
	started := filepath.Join(markers, "install-started")
	pidFile := filepath.Join(markers, "install-pid")
	putFakeBunOnPath(t, `
case "$1" in
  install)
    echo $$ > `+pidFile+`
    touch `+started+`
    while :; do sleep 0.1; done
    ;;
esac
sleep 3
exit 0`)

	goroutinesBefore := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := NewCompiler(discardLogger()).Prepare(ctx, ports.PrepareRequest{
			ProjectDir:      dir,
			Strategy:        ports.StrategyLayered,
			SourceDateEpoch: time.Unix(0, 0),
		})
		done <- err
	}()

	waitForFile(t, started, 10*time.Second)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Prepare reported success for a cancelled build")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Prepare did not return after the context was cancelled")
	}

	assertInstallProcessExited(t, pidFile, 6*time.Second)
	assertGoroutinesSettled(t, goroutinesBefore)
}

// assertInstallProcessExited polls for the OS process recorded in pidFile (by
// the fake install script, which never exits on its own — see the comment on
// TestPrepare_CancelledContextLeaksNeitherInstallNorGoroutine) to disappear.
// Sending it signal 0 is the standard existence probe: it delivers nothing,
// but the error return distinguishes "still running" from "gone" (ESRCH,
// surfaced by the os package as ErrProcessDone).
func assertInstallProcessExited(t *testing.T, pidFile string, within time.Duration) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("could not read the recorded install pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("recorded install pid %q is not a number: %v", raw, err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		// Unreachable on the unix-only platforms this test runs on (see
		// putFakeBunOnPath's Windows skip): os.FindProcess never fails there.
		return
	}

	// The fake install never exits on its own, so if this assertion FAILS the
	// process it started outlives the test run and sits in a sleep loop until
	// the machine is rebooted. That is not hypothetical: proving this guard
	// could fail left exactly one such orphan behind, which had to be found
	// with `ps` and killed by hand.
	//
	// A red test must not also litter the machine, so the kill is
	// unconditional cleanup rather than part of the assertion below — it runs
	// whether the test passed, failed, or panicked, and killing an
	// already-dead pid is a harmless no-op.
	t.Cleanup(func() { _ = proc.Kill() })

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if sigErr := proc.Signal(syscall.Signal(0)); sigErr != nil {
			return // The process is gone.
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the vendor install outlived the cancelled Prepare: its process was still alive after the deadline")
}

func waitForFile(t *testing.T, path string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

// assertGoroutinesSettled polls rather than sampling once: a goroutine that
// has been told to exit still takes a moment to be scheduled, and a bare
// before/after comparison would flake on that rather than on a real leak.
func assertGoroutinesSettled(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last int
	for time.Now().Before(deadline) {
		last = runtime.NumGoroutine()
		if last <= before {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("goroutines did not settle: %d before, still %d after (the vendor install goroutine leaked)", before, last)
}
