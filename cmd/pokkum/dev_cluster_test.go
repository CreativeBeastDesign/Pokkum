package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// --- flag contract --------------------------------------------------------

// devCmdWithArgs builds a real `pokkum dev` command and parses args against
// it, so these assertions run through the same cobra/pflag wiring production
// does rather than against a hand-built flag set.
func devCmdWithArgs(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newDevCommand(context.Background(), discardLogger())
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return validateDevFlags(cmd)
}

// TestDevClusterFlagContract walks the whole accept/reject matrix. The
// rejected cases are all shapes that would otherwise parse cleanly and then
// do nothing — the failure mode this repo has already shipped once with
// --bun-version.
func TestDevClusterFlagContract(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"valid", []string{"--cluster", "--namespace", "dev", "--selector", "app=web"}, ""},
		{"valid with container", []string{"--cluster", "--namespace", "dev", "--selector", "app=web", "--container", "app"}, ""},
		{"no cluster flags at all", nil, ""},

		{"missing namespace", []string{"--cluster", "--selector", "app=web"}, "--namespace"},
		{"missing selector", []string{"--cluster", "--namespace", "dev"}, "--selector"},
		{"missing both", []string{"--cluster"}, "--namespace and --selector"},
		{"empty namespace", []string{"--cluster", "--namespace", "  ", "--selector", "app=web"}, "--namespace"},

		{"with --no-container", []string{"--cluster", "--no-container", "--namespace", "dev", "--selector", "app=web"}, "mutually exclusive"},
		{"with --debug", []string{"--cluster", "--namespace", "dev", "--selector", "app=web", "--debug"}, "--debug"},
		{"with --platform", []string{"--cluster", "--namespace", "dev", "--selector", "app=web", "--platform", "linux/amd64"}, "--platform"},
		{"with --bun-version", []string{"--cluster", "--namespace", "dev", "--selector", "app=web", "--bun-version", "1.2.0"}, "--bun-version"},
		{"with --bun-variant", []string{"--cluster", "--namespace", "dev", "--selector", "app=web", "--bun-variant", "baseline"}, "--bun-variant"},

		// The reverse direction: a cluster-only flag given without --cluster
		// is inert, so it is a usage error rather than a silent no-op.
		{"namespace without cluster", []string{"--namespace", "dev"}, "only apply to --cluster"},
		{"selector without cluster", []string{"--selector", "app=web"}, "only apply to --cluster"},
		{"container without cluster", []string{"--container", "app"}, "only apply to --cluster"},
		{"orphans with --no-container", []string{"--no-container", "--namespace", "dev"}, "only apply to --cluster"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := devCmdWithArgs(t, tc.args...)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("args %v rejected: %v", tc.args, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("args %v were accepted, want an error mentioning %q", tc.args, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
			if !errors.Is(err, core.ErrInvalidRequest) {
				t.Errorf("error should wrap core.ErrInvalidRequest, got: %v", err)
			}
		})
	}
}

// TestDevClusterFlagsAreRegistered guards against the flags existing only in
// this test's expectations: --cluster's whole contract is meaningless if the
// flags are not actually on the command.
func TestDevClusterFlagsAreRegistered(t *testing.T) {
	cmd := newDevCommand(context.Background(), discardLogger())
	for _, name := range []string{"cluster", "namespace", "selector", "container"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("`pokkum dev` has no --%s flag", name)
		}
	}
}

// --- the loop -------------------------------------------------------------

// fakeSyncer records everything the loop asks of it and can be told to fail
// on a chosen call, so the "first cycle is fatal, later ones are not" rule is
// directly assertable.
type fakeSyncer struct {
	mu sync.Mutex

	target    ports.ClusterTarget
	targetErr error

	queries []ports.ClusterTargetQuery
	syncs   []ports.ClusterSyncRequest

	// failSyncAt makes the Nth (1-based) Sync call fail. Zero never fails.
	failSyncAt int
	result     ports.ClusterSyncResult

	synced chan struct{}
}

func newFakeSyncer(target ports.ClusterTarget) *fakeSyncer {
	return &fakeSyncer{
		target: target,
		result: ports.ClusterSyncResult{Files: 3, Bytes: 30, Restarted: true},
		synced: make(chan struct{}, 64),
	}
}

func (f *fakeSyncer) ResolveTarget(_ context.Context, q ports.ClusterTargetQuery) (ports.ClusterTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	return f.target, f.targetErr
}

func (f *fakeSyncer) Sync(_ context.Context, req ports.ClusterSyncRequest) (ports.ClusterSyncResult, error) {
	f.mu.Lock()
	f.syncs = append(f.syncs, req)
	n := len(f.syncs)
	fail := f.failSyncAt == n
	res := f.result
	f.mu.Unlock()

	select {
	case f.synced <- struct{}{}:
	default:
	}
	if fail {
		return ports.ClusterSyncResult{}, fmt.Errorf("sync %d exploded", n)
	}
	return res, nil
}

func (f *fakeSyncer) syncCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.syncs)
}

func (f *fakeSyncer) lastSync() ports.ClusterSyncRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.syncs[len(f.syncs)-1]
}

// fakePreparer stands in for the SvelteKit build.
type fakePreparer struct {
	mu       sync.Mutex
	calls    int
	out      devClusterOutputs
	failAt   int
	failWith error
}

func (p *fakePreparer) Prepare(context.Context, *slog.Logger, *devFlags, string) (devClusterOutputs, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.failAt == p.calls {
		return devClusterOutputs{}, p.failWith
	}
	return p.out, nil
}

func clusterFlags() *devFlags {
	return &devFlags{cluster: true, namespace: "dev", selector: "app=web"}
}

func readyTarget() ports.ClusterTarget {
	return ports.ClusterTarget{Namespace: "dev", Pod: "web-aaa", Container: "app", DevModeEnabled: true}
}

// runLoopBounded runs the loop with a deadline and returns its error.
//
// The deadline is not a convenience: several of these tests assert that the
// loop REFUSES or FAILS, and a loop that stopped refusing would otherwise
// watch for source changes forever and turn a failed assertion into a
// ten-minute test timeout. With a bound, the same regression fails in
// seconds with a message that names the actual problem — every caller below
// asserts on what the error says, never merely that one occurred, so a
// deadline error cannot be mistaken for the refusal being tested.
func runLoopBounded(t *testing.T, flags *devFlags, dir string, syncer ports.ClusterDevSyncer, preparer devClusterPreparer) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return runClusterDev(ctx, discardLogger(), flags, dir, syncer, preparer, time.Millisecond)
}

// TestRunClusterDev_RefusesWithoutDevModeOnTheContainer is the fail-closed
// gate. Without POKKUM_DEV_MODE the in-pod pokkum-init rejects the sync
// anyway, so proceeding would mean spending a full SvelteKit build to earn an
// error naming a subcommand the developer never typed.
func TestRunClusterDev_RefusesWithoutDevModeOnTheContainer(t *testing.T) {
	target := readyTarget()
	target.DevModeEnabled = false
	syncer := newFakeSyncer(target)
	preparer := &fakePreparer{}

	err := runLoopBounded(t, clusterFlags(), t.TempDir(), syncer, preparer)
	if err == nil {
		t.Fatal("expected a refusal for a container without POKKUM_DEV_MODE")
	}
	if !strings.Contains(err.Error(), ports.EnvDevMode) || !strings.Contains(err.Error(), "kubectl set env") {
		t.Errorf("error should name the variable and the exact remediation, got: %v", err)
	}
	if preparer.calls != 0 {
		t.Errorf("the project was built %d time(s) before the refusal; the check must come first", preparer.calls)
	}
	if syncer.syncCount() != 0 {
		t.Error("a sync was attempted despite the refusal")
	}
}

// TestRunClusterDev_FirstCycleFailureIsFatal: a first-cycle failure is a
// configuration problem, and looping on it would bury the one error that
// matters under a rebuild every poll interval.
func TestRunClusterDev_FirstCycleFailureIsFatal(t *testing.T) {
	t.Run("build fails", func(t *testing.T) {
		syncer := newFakeSyncer(readyTarget())
		preparer := &fakePreparer{failAt: 1, failWith: errors.New("bun exploded")}

		err := runLoopBounded(t, clusterFlags(), t.TempDir(), syncer, preparer)
		if err == nil || !strings.Contains(err.Error(), "bun exploded") {
			t.Fatalf("err = %v, want the build failure", err)
		}
	})

	t.Run("sync fails", func(t *testing.T) {
		syncer := newFakeSyncer(readyTarget())
		syncer.failSyncAt = 1
		preparer := &fakePreparer{}

		err := runLoopBounded(t, clusterFlags(), t.TempDir(), syncer, preparer)
		if err == nil || !strings.Contains(err.Error(), "exploded") {
			t.Fatalf("err = %v, want the sync failure", err)
		}
	})
}

// TestRunClusterDev_SyncsClientBeforeServerAndAlwaysRestarts pins the request
// the loop actually builds: the destinations, their order, the packager's
// exclusion list on the server tree, and the restart.
func TestRunClusterDev_SyncsClientBeforeServerAndAlwaysRestarts(t *testing.T) {
	syncer := newFakeSyncer(readyTarget())
	out := devClusterOutputs{ServerDir: filepath.Join(t.TempDir(), "output"), ClientDir: filepath.Join(t.TempDir(), "output", "client")}
	preparer := &fakePreparer{out: out}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runClusterDev(ctx, discardLogger(), clusterFlags(), t.TempDir(), syncer, preparer, time.Hour)
	}()

	select {
	case <-syncer.synced:
	case err := <-done:
		t.Fatalf("loop returned before syncing: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no sync within 5s")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	req := syncer.lastSync()
	if !req.Restart {
		t.Error("Restart = false; server code is read once at process start, so a sync without a restart serves the old build")
	}
	if len(req.Dirs) != 2 {
		t.Fatalf("Dirs = %+v, want exactly client then server", req.Dirs)
	}
	if req.Dirs[0].RemoteDir != ports.AppClientDirPrefix || req.Dirs[0].LocalDir != out.ClientDir {
		t.Errorf("Dirs[0] = %+v, want the client tree at %s", req.Dirs[0], ports.AppClientDirPrefix)
	}
	if req.Dirs[1].RemoteDir != ports.AppServerDirPrefix || req.Dirs[1].LocalDir != out.ServerDir {
		t.Errorf("Dirs[1] = %+v, want the server tree at %s", req.Dirs[1], ports.AppServerDirPrefix)
	}
	if len(req.Dirs[0].ExcludeDirs) != 0 {
		t.Errorf("the client tree must not exclude anything, got %v", req.Dirs[0].ExcludeDirs)
	}
	if strings.Join(req.Dirs[1].ExcludeDirs, ",") != strings.Join(devClusterServerExcludes, ",") {
		t.Errorf("server ExcludeDirs = %v, want %v", req.Dirs[1].ExcludeDirs, devClusterServerExcludes)
	}
	if req.Target != readyTarget() {
		t.Errorf("Target = %+v, want the resolved target", req.Target)
	}
}

// TestRunClusterDev_RebuildsOnSourceChangeAndSurvivesAFailure exercises the
// steady state a dev session actually lives in: several cycles, with a
// failure in the middle. A loop that dies on the first syntax error is
// useless, and a loop that reports a failed sync as a success is worse.
func TestRunClusterDev_RebuildsOnSourceChangeAndSurvivesAFailure(t *testing.T) {
	projectDir := t.TempDir()
	srcDir := filepath.Join(projectDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	syncer := newFakeSyncer(readyTarget())
	syncer.failSyncAt = 2 // the cycle after the initial one
	preparer := &fakePreparer{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runClusterDev(ctx, discardLogger(), clusterFlags(), projectDir, syncer, preparer, 5*time.Millisecond)
	}()

	waitSync := func(t *testing.T, which int) {
		t.Helper()
		select {
		case <-syncer.synced:
		case err := <-done:
			t.Fatalf("loop returned before sync %d: %v", which, err)
		case <-time.After(10 * time.Second):
			t.Fatalf("sync %d never happened", which)
		}
	}
	touch := func(t *testing.T, name string) {
		t.Helper()
		// Each touched file is newer than the last poll, which is what the
		// mtime watcher keys on.
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(name), 0o644); err != nil { //nolint:gosec // test fixture
			t.Fatalf("write %s: %v", name, err)
		}
		future := time.Now().Add(time.Duration(len(name)) * time.Second)
		if err := os.Chtimes(filepath.Join(srcDir, name), future, future); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}

	waitSync(t, 1)
	touch(t, "a.ts")
	waitSync(t, 2) // this one fails inside the fake
	touch(t, "bb.ts")
	waitSync(t, 3)

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	if got := syncer.syncCount(); got < 3 {
		t.Errorf("sync count = %d, want at least 3 (the loop must survive the failing cycle)", got)
	}
	if preparer.calls < 3 {
		t.Errorf("prepare count = %d, want at least 3 (every cycle rebuilds)", preparer.calls)
	}
	if len(syncer.queries) != 1 {
		t.Errorf("ResolveTarget was called %d time(s); the target is resolved once per session", len(syncer.queries))
	}
}

// TestRunClusterDev_PropagatesTheTargetQuery proves the flags reach the
// syncer rather than being read from somewhere else.
func TestRunClusterDev_PropagatesTheTargetQuery(t *testing.T) {
	target := readyTarget()
	target.DevModeEnabled = false // stop the loop immediately; the query is already recorded
	syncer := newFakeSyncer(target)

	flags := clusterFlags()
	flags.namespace = "staging"
	flags.selector = "app=storefront,tier=web"
	flags.container = "sveltekit"

	_ = runLoopBounded(t, flags, t.TempDir(), syncer, &fakePreparer{})

	if len(syncer.queries) != 1 {
		t.Fatalf("ResolveTarget called %d time(s), want 1", len(syncer.queries))
	}
	want := ports.ClusterTargetQuery{Namespace: "staging", Selector: "app=storefront,tier=web", Container: "sveltekit"}
	if syncer.queries[0] != want {
		t.Errorf("query = %+v, want %+v", syncer.queries[0], want)
	}
}

// TestDevClusterServerExcludes_MatchPackager keeps this file's copy of the
// exclusion list in lockstep with the packager's. The two are separate
// literals because an adapter may not be imported from the command layer, and
// a mirrored constant that drifts is a named failure class in this repo:
// syncing without one of these exclusions would push the client tree into
// /app/server as well, which no test of this package alone would notice.
func TestDevClusterServerExcludes_MatchPackager(t *testing.T) {
	src, err := os.ReadFile("../../internal/adapters/packager/packager.go")
	if err != nil {
		t.Fatalf("read packager source: %v", err)
	}
	const marker = `ExcludeDirs: []string{`
	idx := strings.Index(string(src), marker)
	if idx < 0 {
		t.Fatalf("packager.go no longer contains %q; this guard has gone blind", marker)
	}
	rest := string(src)[idx+len(marker):]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatal("could not find the end of the packager's ExcludeDirs literal")
	}

	var got []string
	for _, part := range strings.Split(rest[:end], ",") {
		if v := strings.Trim(strings.TrimSpace(part), `"`); v != "" {
			got = append(got, v)
		}
	}
	if strings.Join(got, ",") != strings.Join(devClusterServerExcludes, ",") {
		t.Errorf("packager excludes %v from the /app/server layer but dev --cluster excludes %v; the synced tree would not match a built image",
			got, devClusterServerExcludes)
	}
}

// --- runDevWithDeps branching --------------------------------------------

// recordingClusterStarter captures the arguments the dispatch hands it.
type recordingClusterStarter struct {
	calls  int
	absDir string
	flags  *devFlags
}

func (r *recordingClusterStarter) Start(_ context.Context, _ *slog.Logger, flags *devFlags, absDir string) error {
	r.calls++
	r.flags = flags
	r.absDir = absDir
	return nil
}

// TestRunDevWithDeps_Cluster_NeverInvokesContainerSeams is --cluster's
// counterpart to the --no-container dispatch test: it must reach the cluster
// starter and must never touch the container-parity seams, because nothing is
// built into an image and no daemon is contacted.
//
// Without it, deleting the `if flags.cluster` dispatch entirely leaves every
// other test in this file green — the loop itself is exercised directly — and
// `pokkum dev --cluster` silently falls through to building a Docker image.
func TestRunDevWithDeps_Cluster_NeverInvokesContainerSeams(t *testing.T) {
	tempDir := t.TempDir()

	containerRunner := &fakeContainerRunner{}
	containerBuilder := &fakeDevBuilder{}
	localRunner := &fakeLocalRunner{}
	starter := &recordingClusterStarter{}

	flags := &devFlags{cluster: true, namespace: "dev", selector: "app=web"}
	if err := runDevWithDeps(context.Background(), discardLogger(), flags, []string{tempDir}, containerRunner, containerBuilder, localRunner, starter); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if starter.calls != 1 {
		t.Fatalf("cluster starter invoked %d time(s), want 1", starter.calls)
	}
	if starter.flags != flags {
		t.Error("the cluster starter was handed a different flags value than the command parsed")
	}
	// The dispatch resolves the project directory to an absolute path before
	// handing it over; the loop joins "src" onto it to watch for changes, so
	// a relative path here would watch the wrong tree.
	if !filepath.IsAbs(starter.absDir) {
		t.Errorf("absDir = %q, want an absolute path", starter.absDir)
	}
	if containerRunner.Calls() != 0 {
		t.Errorf("container runner invoked %d time(s); --cluster builds no image and contacts no daemon", containerRunner.Calls())
	}
	if containerBuilder.Calls() != 0 {
		t.Errorf("container builder invoked %d time(s); --cluster builds no image", containerBuilder.Calls())
	}
	if got := localRunner.calls.Load(); got != 0 {
		t.Errorf("local dev-server runner invoked %d time(s); that is --no-container's seam", got)
	}
}

// TestRunDevWithDeps_NonCluster_NeverInvokesClusterStarter is the other
// direction: the default container-parity mode must not be routed at the
// cluster starter. failingClusterStarter (dev_nocontainer_test.go) already
// enforces this for every other case in that file; this pins it explicitly
// for the plain default with no flags set at all.
func TestRunDevWithDeps_NonCluster_NeverInvokesClusterStarter(t *testing.T) {
	tempDir := t.TempDir()
	starter := &recordingClusterStarter{}

	flags := &devFlags{watch: false}
	if err := runDevWithDeps(context.Background(), discardLogger(), flags, []string{tempDir},
		&fakeContainerRunner{}, &fakeDevBuilder{}, &fakeLocalRunner{}, starter); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if starter.calls != 0 {
		t.Errorf("cluster starter invoked %d time(s) for a non-cluster run", starter.calls)
	}
}
