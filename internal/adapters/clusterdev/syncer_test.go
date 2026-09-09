package clusterdev

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// podJSON renders a `kubectl get pods -o json` payload from a compact
// description, so each test case names only what it is actually about.
type podSpec struct {
	name       string
	phase      string
	deleting   bool
	containers []containerJSON
}

type containerJSON struct {
	name string
	env  map[string]string
}

func podListJSON(pods ...podSpec) []byte {
	var items []string
	for _, p := range pods {
		var cs []string
		for _, c := range p.containers {
			var envs []string
			for k, v := range c.env {
				envs = append(envs, fmt.Sprintf(`{"name":%q,"value":%q}`, k, v))
			}
			cs = append(cs, fmt.Sprintf(`{"name":%q,"env":[%s]}`, c.name, strings.Join(envs, ",")))
		}
		deletion := ""
		if p.deleting {
			deletion = `,"deletionTimestamp":"2026-01-01T00:00:00Z"`
		}
		items = append(items, fmt.Sprintf(
			`{"metadata":{"name":%q,"namespace":"dev"%s},"spec":{"containers":[%s]},"status":{"phase":%q}}`,
			p.name, deletion, strings.Join(cs, ","), p.phase))
	}
	return []byte(fmt.Sprintf(`{"items":[%s]}`, strings.Join(items, ",")))
}

func appContainer(name string, env map[string]string) containerJSON {
	return containerJSON{name: name, env: env}
}

// TestSelectTarget_PicksTheLowestNamedRunningPod is the multi-item case: with
// several replicas matching one selector, selection must be by a stable rule
// rather than by whatever order the API server returned, or repeated runs
// would hop between replicas and leave most of the fleet stale.
func TestSelectTarget_PicksTheLowestNamedRunningPod(t *testing.T) {
	payload := podListJSON(
		podSpec{name: "web-zzz", phase: "Running", containers: []containerJSON{appContainer("app", map[string]string{ports.EnvDevMode: "1"})}},
		podSpec{name: "web-aaa", phase: "Running", containers: []containerJSON{appContainer("app", map[string]string{ports.EnvDevMode: "1"})}},
		podSpec{name: "web-mmm", phase: "Running", containers: []containerJSON{appContainer("app", map[string]string{ports.EnvDevMode: "1"})}},
	)
	got, err := SelectTarget(payload, ports.ClusterTargetQuery{Namespace: "dev", Selector: "app=web"})
	if err != nil {
		t.Fatalf("SelectTarget: %v", err)
	}
	if got.Pod != "web-aaa" {
		t.Errorf("Pod = %q, want web-aaa (lowest name wins, so repeated runs hit the same replica)", got.Pod)
	}
	if !got.DevModeEnabled {
		t.Error("DevModeEnabled = false, want true")
	}
	if got.AttestationEnabled {
		t.Error("AttestationEnabled = true, but no attestation digest is set")
	}
}

// TestSelectTarget_SkipsPodsThatCannotBeSyncedInto covers the two states a
// pod can be in that would accept an exec and then throw the result away.
func TestSelectTarget_SkipsPodsThatCannotBeSyncedInto(t *testing.T) {
	payload := podListJSON(
		podSpec{name: "web-aaa", phase: "Pending", containers: []containerJSON{appContainer("app", nil)}},
		podSpec{name: "web-bbb", phase: "Running", deleting: true, containers: []containerJSON{appContainer("app", nil)}},
		podSpec{name: "web-ccc", phase: "Running", containers: []containerJSON{appContainer("app", map[string]string{ports.EnvDevMode: "1"})}},
	)
	got, err := SelectTarget(payload, ports.ClusterTargetQuery{Namespace: "dev", Selector: "app=web"})
	if err != nil {
		t.Fatalf("SelectTarget: %v", err)
	}
	if got.Pod != "web-ccc" {
		t.Errorf("Pod = %q, want web-ccc (Pending and terminating pods are skipped)", got.Pod)
	}
}

// TestSelectTarget_NoUsablePod distinguishes "the selector matched nothing"
// from "the selector matched pods that are all unusable" in the error text,
// because the two have completely different fixes.
func TestSelectTarget_NoUsablePod(t *testing.T) {
	t.Run("nothing matched", func(t *testing.T) {
		_, err := SelectTarget([]byte(`{"items":[]}`), ports.ClusterTargetQuery{Namespace: "dev", Selector: "app=web"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "0 pod(s) matched") {
			t.Errorf("error should say nothing matched, got: %v", err)
		}
	})
	t.Run("matched but none running", func(t *testing.T) {
		payload := podListJSON(
			podSpec{name: "web-aaa", phase: "Pending", containers: []containerJSON{appContainer("app", nil)}},
			podSpec{name: "web-bbb", phase: "Failed", containers: []containerJSON{appContainer("app", nil)}},
		)
		_, err := SelectTarget(payload, ports.ClusterTargetQuery{Namespace: "dev", Selector: "app=web"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "2 pod(s) matched") {
			t.Errorf("error should report that pods matched but none were Running, got: %v", err)
		}
	})
}

// TestSelectTarget_MultiContainerRequiresExplicitChoice is the sidecar case.
// Pokkum's own --with-otel-sidecar produces exactly this shape, so "the first
// container" would be a coin flip between the application and a collector.
func TestSelectTarget_MultiContainerRequiresExplicitChoice(t *testing.T) {
	payload := podListJSON(podSpec{
		name: "web-aaa", phase: "Running",
		containers: []containerJSON{
			appContainer("otel-collector", nil),
			appContainer("app", map[string]string{ports.EnvDevMode: "1"}),
		},
	})

	_, err := SelectTarget(payload, ports.ClusterTargetQuery{Namespace: "dev", Selector: "app=web"})
	if err == nil {
		t.Fatal("a two-container pod was resolved without --container")
	}
	if !strings.Contains(err.Error(), "otel-collector") || !strings.Contains(err.Error(), "--container") {
		t.Errorf("error should list the containers and name --container, got: %v", err)
	}

	got, err := SelectTarget(payload, ports.ClusterTargetQuery{Namespace: "dev", Selector: "app=web", Container: "app"})
	if err != nil {
		t.Fatalf("SelectTarget with an explicit container: %v", err)
	}
	if got.Container != "app" {
		t.Errorf("Container = %q, want app", got.Container)
	}
	if !got.DevModeEnabled {
		t.Error("env was read from the wrong container: the collector has no POKKUM_DEV_MODE, the app does")
	}

	if _, err := SelectTarget(payload, ports.ClusterTargetQuery{Namespace: "dev", Selector: "app=web", Container: "nope"}); err == nil {
		t.Error("a container name that does not exist was accepted")
	}
}

// TestSelectTarget_ReportsDevModeAndAttestationHonestly walks the env
// spellings that decide whether the command layer refuses (no dev mode) or
// warns (attestation on).
func TestSelectTarget_ReportsDevModeAndAttestationHonestly(t *testing.T) {
	cases := []struct {
		env          map[string]string
		wantDev      bool
		wantAttestOn bool
	}{
		{nil, false, false},
		{map[string]string{ports.EnvDevMode: "1"}, true, false},
		{map[string]string{ports.EnvDevMode: "true"}, true, false},
		{map[string]string{ports.EnvDevMode: "0"}, false, false},
		{map[string]string{ports.EnvDevMode: "yes"}, false, false},
		{map[string]string{ports.EnvDevMode: ""}, false, false},
		{map[string]string{ports.EnvDevMode: "1", ports.EnvAttestationDigest: strings.Repeat("a", 64)}, true, true},
		{map[string]string{ports.EnvAttestationDigest: ""}, false, false},
	}
	for _, tc := range cases {
		payload := podListJSON(podSpec{name: "p", phase: "Running", containers: []containerJSON{appContainer("app", tc.env)}})
		got, err := SelectTarget(payload, ports.ClusterTargetQuery{Namespace: "dev", Selector: "app=web"})
		if err != nil {
			t.Fatalf("env %v: %v", tc.env, err)
		}
		if got.DevModeEnabled != tc.wantDev {
			t.Errorf("env %v: DevModeEnabled = %v, want %v", tc.env, got.DevModeEnabled, tc.wantDev)
		}
		if got.AttestationEnabled != tc.wantAttestOn {
			t.Errorf("env %v: AttestationEnabled = %v, want %v", tc.env, got.AttestationEnabled, tc.wantAttestOn)
		}
	}
}

func TestSelectTarget_RejectsUnparseablePayload(t *testing.T) {
	_, err := SelectTarget([]byte("not json"), ports.ClusterTargetQuery{Namespace: "dev", Selector: "app=web"})
	if err == nil {
		t.Fatal("expected an error for an unparseable pod list")
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("error should wrap core.ErrInvalidRequest, got: %v", err)
	}
}

func TestResolveTarget_RequiresNamespaceAndSelector(t *testing.T) {
	s := New(nil, "kubectl")
	for _, q := range []ports.ClusterTargetQuery{
		{Selector: "app=web"},
		{Namespace: "dev"},
		{Namespace: "  ", Selector: " "},
	} {
		if _, err := s.ResolveTarget(context.Background(), q); err == nil {
			t.Errorf("query %+v was accepted", q)
		}
	}
}

// fakeKubectl writes a shell script that stands in for kubectl. It records
// the argv it was called with, drains stdin, and prints whatever the caller
// asked it to. Using a real subprocess rather than an injected command runner
// keeps the exec plumbing itself — argv construction, the stdin pipe, the
// exit-code and stderr handling — inside the test's reach, which is where the
// bugs in a shell-out adapter actually live.
func fakeKubectl(t *testing.T, script string) (path, argvLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake kubectl is a POSIX shell script")
	}
	dir := t.TempDir()
	path = filepath.Join(dir, "kubectl")
	argvLog = filepath.Join(dir, "argv")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvLog + "\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil { //nolint:gosec // an executable test fixture
		t.Fatalf("write fake kubectl: %v", err)
	}
	return path, argvLog
}

func syncTarget() ports.ClusterTarget {
	return ports.ClusterTarget{Namespace: "dev", Pod: "web-aaa", Container: "app", DevModeEnabled: true}
}

func syncDirs(t *testing.T) []ports.ClusterSyncDir {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.js"), "hello")
	return []ports.ClusterSyncDir{{LocalDir: dir, RemoteDir: ports.AppServerDirPrefix}}
}

// TestSync_BuildsTheExpectedExecArgvAndReportsTheExtractorsCounts is the
// happy path through the real subprocess plumbing.
func TestSync_BuildsTheExpectedExecArgvAndReportsTheExtractorsCounts(t *testing.T) {
	// cat >/dev/null drains the tar so the writer never blocks on a full pipe.
	kubectl, argvLog := fakeKubectl(t, `cat >/dev/null; echo '{"files":1,"bytes":5,"restarted":true}'`)
	s := New(nil, kubectl)

	res, err := s.Sync(context.Background(), ports.ClusterSyncRequest{
		Target: syncTarget(), Dirs: syncDirs(t), Restart: true,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Files != 1 || res.Bytes != 5 || !res.Restarted {
		t.Errorf("result = %+v, want {Files:1 Bytes:5 Restarted:true}", res)
	}

	argv, err := os.ReadFile(argvLog) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(argv)), "\n")
	want := []string{
		"exec", "-i", "-n", "dev", "web-aaa", "-c", "app", "--",
		ports.SupervisorPath, ports.DevSyncSubcommand,
		"--root", ports.AppServerDirPrefix, "--restart",
	}
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestSync_OmitsRestartFlagWhenNotRequested keeps the two argv shapes honest,
// so the --restart flag cannot become unconditional without a test noticing.
func TestSync_OmitsRestartFlagWhenNotRequested(t *testing.T) {
	kubectl, argvLog := fakeKubectl(t, `cat >/dev/null; echo '{"files":1,"bytes":5,"restarted":false}'`)
	s := New(nil, kubectl)

	if _, err := s.Sync(context.Background(), ports.ClusterSyncRequest{Target: syncTarget(), Dirs: syncDirs(t)}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	argv, err := os.ReadFile(argvLog) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	if strings.Contains(string(argv), "--restart") {
		t.Errorf("--restart was passed without Restart being requested: %s", argv)
	}
}

// TestSync_RefusesResultsThatDoNotAddUp is the end-to-end integrity check.
// Each case is a way for the far end to report something other than "I wrote
// exactly what you sent", and every one of them must be an error rather than
// a smaller success.
func TestSync_RefusesResultsThatDoNotAddUp(t *testing.T) {
	cases := map[string]struct {
		script  string
		restart bool
		want    string
	}{
		"fewer files written": {
			script: `cat >/dev/null; echo '{"files":0,"bytes":0,"restarted":true}'`, restart: true,
			want: "were sent",
		},
		"fewer bytes written": {
			script: `cat >/dev/null; echo '{"files":1,"bytes":2,"restarted":true}'`, restart: true,
			want: "were sent",
		},
		"restart silently skipped": {
			script: `cat >/dev/null; echo '{"files":1,"bytes":5,"restarted":false}'`, restart: true,
			want: "still serving the previous build",
		},
		"unparseable summary": {
			script: `cat >/dev/null; echo 'not json'`, restart: true,
			want: "parse extractor summary",
		},
		"no summary at all": {
			script: `cat >/dev/null`, restart: true,
			want: "parse extractor summary",
		},
		"kubectl failed": {
			script: `cat >/dev/null; echo 'Error from server (Forbidden): pods "web-aaa" is forbidden' >&2; exit 1`, restart: true,
			want: "Forbidden",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			kubectl, _ := fakeKubectl(t, tc.script)
			s := New(nil, kubectl)
			res, err := s.Sync(context.Background(), ports.ClusterSyncRequest{
				Target: syncTarget(), Dirs: syncDirs(t), Restart: tc.restart,
			})
			if err == nil {
				t.Fatalf("Sync reported success (%+v) for %q", res, name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
			if res.Files != 0 || res.Restarted {
				t.Errorf("a failed sync returned a non-zero result: %+v", res)
			}
		})
	}
}

func TestSync_RejectsIncompleteRequests(t *testing.T) {
	s := New(nil, "kubectl")
	cases := map[string]ports.ClusterSyncRequest{
		"no target":    {Dirs: []ports.ClusterSyncDir{{LocalDir: t.TempDir(), RemoteDir: "/app/server"}}},
		"no container": {Target: ports.ClusterTarget{Namespace: "dev", Pod: "p"}, Dirs: []ports.ClusterSyncDir{{LocalDir: t.TempDir(), RemoteDir: "/app/server"}}},
		"no dirs":      {Target: syncTarget()},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Sync(context.Background(), req); err == nil {
				t.Fatal("request was accepted")
			}
		})
	}
}
